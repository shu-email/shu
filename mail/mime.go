package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"html"
	"io"
	"net/mail"
	"strconv"
	"strings"

	gomsg "github.com/emersion/go-message"
	gomsgmail "github.com/emersion/go-message/mail"
	htmlnode "golang.org/x/net/html"
)

// Caps that keep one huge message from filling the cache or the memory.
const (
	maxBodyPart    = 1 << 20  // 1 MiB per text part
	maxAttachments = 25 << 20 // 25 MiB per message
	// maxInlineImage is the most one inline image may be to be embedded
	// in the HTML body: bigger ones stay out, so one picture cannot fill
	// the cache.
	maxInlineImage = 8 << 20
)

// ParseMessage parses a raw RFC 822 message into the cache's model. The
// bodies are kept in HTML and plain text; attachments carry their bytes.
func ParseMessage(raw []byte) (Message, error) {
	m, _, err := parseMessage(raw, maxAttachments)
	return m, err
}

// parseMessage parses a message, keeping at most attachmentCap bytes of
// attachments, and counts the parts it left out: attachments past the
// cap, and parts it could not read or had no way to list. A reader
// shows what it got; whoever would write the message back must know that
// something is missing.
func parseMessage(raw []byte, attachmentCap int64) (Message, int, error) {
	var m Message
	dropped := 0
	m.CachedAt = nowUTC()
	r, err := gomsgmail.CreateReader(bytes.NewReader(raw))
	if err != nil && !gomsg.IsUnknownCharset(err) {
		return m, dropped, err
	}
	if r == nil {
		return m, dropped, err
	}
	defer r.Close()

	h := r.Header
	m.Subject, _ = h.Subject()
	m.MessageID, _ = h.MessageID()
	if ids := msgIDs(h.Get("Message-Id")); m.MessageID == "" && len(ids) > 0 {
		m.MessageID = ids[0]
	}
	if ids := msgIDs(h.Get("In-Reply-To")); len(ids) > 0 {
		m.InReplyTo = ids[0]
	}
	m.References = msgIDs(h.Get("References"))
	m.ThreadIndex = cleanThreadIndex(h.Get("Thread-Index"))
	if d, err := h.Date(); err == nil {
		m.Date = d
	}
	m.From = firstAddress(h, "From")
	if m.From.Email == "" {
		m.From = firstAddress(h, "Sender")
	}
	m.To = addresses(h, "To")
	m.Cc = addresses(h, "Cc")
	m.ReplyTo = addresses(h, "Reply-To")
	m.RawHeaders = keptHeaders(h)

	var text, htmlBody strings.Builder
	// inline maps a Content-ID to the data URI its part becomes, so that
	// the HTML body shows it without a request.
	inline := map[string]string{}
	var attachmentBytes int64
	truncated := false
	part := 0
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomsg.IsUnknownCharset(err) {
			return m, dropped, err
		}
		if p == nil {
			break
		}
		part++
		contentType, params := partContentType(p.Header)
		if bodyText(p.Header, contentType) {
			data, cut := readCapped(p.Body, maxBodyPart)
			truncated = truncated || cut
			switch {
			case strings.HasPrefix(contentType, "text/html"):
				htmlBody.WriteString(data)
			default:
				text.WriteString(data)
			}
			_ = params
			continue
		}
		if cid := partContentID(p.Header); cid != "" && strings.HasPrefix(contentType, "image/") {
			data, err := io.ReadAll(io.LimitReader(p.Body, maxInlineImage+1))
			switch {
			case err != nil || len(data) > maxInlineImage:
				truncated = true
				dropped++
			default:
				inline[cid] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
			continue
		}
		name, ok := attachmentName(p.Header, contentType, params)
		if !ok {
			// Unknown inline part: read and drop so the reader can move on.
			_, _ = io.Copy(io.Discard, p.Body)
			dropped++
			continue
		}
		data, err := io.ReadAll(p.Body)
		if err != nil {
			dropped++
			continue
		}
		if attachmentBytes+int64(len(data)) > attachmentCap {
			truncated = true
			dropped++
			continue
		}
		attachmentBytes += int64(len(data))
		sum := sha256.Sum256(data)
		m.Attachments = append(m.Attachments, Attachment{
			Part:        strconv.Itoa(part),
			Filename:    name,
			ContentType: contentType,
			Size:        int64(len(data)),
			ContentHash: hex.EncodeToString(sum[:]),
			Data:        data,
		})
	}
	m.BodyText = normalizeText(text.String())
	m.BodyHTML = embedInline(strings.TrimSpace(htmlBody.String()), inline)
	if m.BodyText == "" && m.BodyHTML != "" {
		m.BodyText = HTMLToText(m.BodyHTML)
	}
	if m.Date.IsZero() {
		m.Date = m.CachedAt
	}
	m.BodyTruncated = truncated
	m.HasAttachments = len(m.Attachments) > 0
	m.Preview = preview(m.BodyText)
	return m, dropped, nil
}

// partContentID returns a part's Content-ID without its angle brackets.
func partContentID(h gomsgmail.PartHeader) string {
	cid := strings.TrimSpace(h.Get("Content-ID"))
	cid = strings.TrimPrefix(cid, "<")
	cid = strings.TrimSuffix(cid, ">")
	return cid
}

// embedInline replaces the cid: references of a body with the data URIs
// of their parts.
func embedInline(body string, inline map[string]string) string {
	if body == "" || len(inline) == 0 {
		return body
	}
	for cid, uri := range inline {
		body = strings.ReplaceAll(body, "cid:"+cid, uri)
	}
	return body
}

// attachmentName is the file name of a part that is no text of the body,
// and whether it is one of the message's attachments: any such part with
// a name, inline or not, and a picture or a document without one, which
// is still worth listing.
func attachmentName(h gomsgmail.PartHeader, contentType string, params map[string]string) (string, bool) {
	name := ""
	if ah, ok := h.(*gomsgmail.AttachmentHeader); ok {
		name, _ = ah.Filename()
	}
	if name == "" {
		name = params["name"]
	}
	if name == "" && !strings.HasPrefix(contentType, "image/") && !strings.HasPrefix(contentType, "application/") {
		return "", false
	}
	return name, true
}

// bodyText reports whether a part is text of the message's body rather
// than an attachment.
func bodyText(h gomsgmail.PartHeader, contentType string) bool {
	_, inline := h.(*gomsgmail.InlineHeader)
	return inline && strings.HasPrefix(contentType, "text/")
}

// partContentType reads a part's media type, as its header interface does
// not expose it.
func partContentType(h gomsgmail.PartHeader) (string, map[string]string) {
	if ct, ok := h.(interface {
		ContentType() (string, map[string]string, error)
	}); ok {
		t, params, _ := ct.ContentType()
		return t, params
	}
	return "application/octet-stream", nil
}

func firstAddress(h gomsgmail.Header, key string) Address {
	list := addresses(h, key)
	if len(list) == 0 {
		return Address{}
	}
	return list[0]
}

func addresses(h gomsgmail.Header, key string) []Address {
	list, err := h.AddressList(key)
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// readCapped reads at most limit bytes and reports whether more remained.
func readCapped(r io.Reader, limit int64) (string, bool) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return string(data), false
	}
	if int64(len(data)) > limit {
		return string(data[:limit]), true
	}
	return string(data), false
}

// normalizeText turns CRLF into LF and trims trailing spaces.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// preview makes the one-line list preview.
func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const max = 180
	if len([]rune(text)) > max {
		text = string([]rune(text)[:max]) + "…"
	}
	return text
}

// HTMLToText extracts readable plain text from HTML, skipping scripts,
// styles and the head.
func HTMLToText(source string) string {
	doc, err := htmlnode.Parse(strings.NewReader(source))
	if err != nil {
		return strings.TrimSpace(stripTags(source))
	}
	var b strings.Builder
	var walk func(*htmlnode.Node)
	block := map[string]bool{
		"p": true, "div": true, "br": true, "li": true, "tr": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"blockquote": true, "pre": true, "table": true, "section": true, "article": true,
	}
	var skip func(*htmlnode.Node) bool
	skip = func(n *htmlnode.Node) bool {
		if n.Type == htmlnode.ElementNode {
			switch n.Data {
			case "script", "style", "head", "title", "noscript":
				return true
			}
		}
		return false
	}
	walk = func(n *htmlnode.Node) {
		if skip(n) {
			return
		}
		if n.Type == htmlnode.TextNode {
			if strings.TrimSpace(n.Data) != "" {
				b.WriteString(n.Data)
			}
		}
		if n.Type == htmlnode.ElementNode && (block[n.Data] || n.Data == "td" || n.Data == "th") {
			if n.Data == "td" || n.Data == "th" {
				b.WriteString(" ")
			} else {
				b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == htmlnode.ElementNode && block[n.Data] {
			b.WriteString("\n")
		}
	}
	walk(doc)
	text := html.UnescapeString(b.String())
	var lines []string
	blank := false
	for _, line := range strings.Split(normalizeText(text), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if blank {
				continue
			}
			blank = true
			continue
		}
		blank = false
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// stripTags is the fallback when the HTML does not parse.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

// ParseAddressList parses the recipients typed in a compose window.
func ParseAddressList(values []string) ([]Address, error) {
	var out []Address
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		list, err := mail.ParseAddressList(value)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			out = append(out, Address{Name: a.Name, Email: a.Address})
		}
	}
	return out, nil
}

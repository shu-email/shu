package mail

import (
	"bytes"
	"fmt"
	"net/mail"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// syncHeaders are the headers a sync keeps with every message it reads:
// the threading headers the envelope lacks, which has no References or
// Thread-Index and parses In-Reply-To strictly, and what tells the
// sender's authentication and how the message's list is left (see Trust
// and Security), so that a cache read of an unopened message has them.
var syncHeaders = slices.Concat(
	[]string{"References", "In-Reply-To", "Thread-Index"},
	securityHeaders, unsubscribeHeaders,
)

// threadSection is the part of the header a sync reads besides the
// envelope (see syncHeaders). PEEK, as reading must never mark mail read.
func threadSection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: syncHeaders,
		Peek:         true,
	}
}

// fetchOptions is the header fetch of the list phase.
func fetchOptions(withPreview bool) *imap.FetchOptions {
	opts := &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		InternalDate:  true,
		RFC822Size:    true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{},
		BodySection:   []*imap.FetchItemBodySection{threadSection()},
	}
	if withPreview {
		opts.BodySection = append(opts.BodySection, &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		})
	}
	return opts
}

// threadHeaders reads the threading headers of a fetch into m.
func threadHeaders(m *Message, raw []byte) {
	if len(raw) == 0 {
		return
	}
	// The blank line that ends a header block may be missing.
	block := append(append([]byte(nil), raw...), "\r\n\r\n"...)
	msg, err := mail.ReadMessage(bytes.NewReader(block))
	if err != nil {
		return
	}
	m.References = msgIDs(msg.Header.Get("References"))
	if ids := msgIDs(msg.Header.Get("In-Reply-To")); m.InReplyTo == "" && len(ids) > 0 {
		m.InReplyTo = ids[0]
	}
	m.ThreadIndex = cleanThreadIndex(msg.Header.Get("Thread-Index"))
	m.SetRawHeaders(headerLines(raw, syncHeaders))
}

// fetchSummaries reads headers (and a preview) for a number set.
//
// An update the server sends unasked while the fetch runs, as it may on
// a connection that stays on a folder, is taken by the IMAP library for
// the fetch's answer about that message, and the answer proper is then
// dropped: the message comes with its flags and without its headers. A
// fetch that got such an answer is made once more, when the update is
// out of the way. A message still without its envelope then is kept as
// it is, as it always was: one odd message must not stop a folder from
// syncing, and opening it reads its headers with its body.
func fetchSummaries(c *imapSession, set imap.NumSet, withPreview bool) ([]Message, []FlagUpdate, error) {
	messages, flags, whole, err := fetchSummariesOnce(c, set, withPreview, false)
	if err == nil && !whole {
		messages, flags, _, err = fetchSummariesOnce(c, set, withPreview, true)
	}
	return messages, flags, err
}

// fetchSummariesOnce is one fetch of fetchSummaries. whole is false when
// an answer lacked the UID or the envelope that were asked for. A message
// without a UID is left out; one without an envelope is too, unless
// lenient.
func fetchSummariesOnce(c *imapSession, set imap.NumSet, withPreview, lenient bool) (messages []Message, flags []FlagUpdate, whole bool, err error) {
	opts := fetchOptions(withPreview)
	cmd := c.client.Fetch(set, opts)
	whole = true
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		buf, err := data.Collect()
		if err != nil {
			cmd.Close()
			return nil, nil, false, fmt.Errorf("imap: fetch: %w", err)
		}
		m := summaryFromFetch(buf, withPreview)
		if m.UID == 0 || buf.Envelope == nil {
			whole = false
			if m.UID == 0 || !lenient {
				continue
			}
		}
		messages = append(messages, m)
		flags = append(flags, FlagUpdate{UID: m.UID, Seen: m.Seen, Flagged: m.Flagged, Keywords: m.Keywords})
	}
	if err := cmd.Close(); err != nil {
		return nil, nil, false, fmt.Errorf("imap: fetch: %w", err)
	}
	return messages, flags, whole, nil
}

func summaryFromFetch(buf *imapclient.FetchMessageBuffer, withPreview bool) Message {
	var m Message
	m.UID = UID(buf.UID)
	if buf.Envelope != nil {
		m.Subject = buf.Envelope.Subject
		m.MessageID = buf.Envelope.MessageID
		if len(buf.Envelope.InReplyTo) > 0 {
			m.InReplyTo = buf.Envelope.InReplyTo[0]
		}
		m.From = envelopeAddress(buf.Envelope.From)
		if m.From.Email == "" {
			m.From = envelopeAddress(buf.Envelope.Sender)
		}
		m.To = envelopeAddresses(buf.Envelope.To)
		m.Cc = envelopeAddresses(buf.Envelope.Cc)
		m.ReplyTo = envelopeAddresses(buf.Envelope.ReplyTo)
		m.Date = buf.Envelope.Date
	}
	threadHeaders(&m, buf.FindBodySection(threadSection()))
	if m.Date.IsZero() {
		m.Date = buf.InternalDate
	}
	m.Size = buf.RFC822Size
	m.Seen, m.Flagged, m.Keywords = readFlags(buf.Flags)
	if withPreview {
		section := &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		}
		if body := buf.FindBodySection(section); len(body) > 0 {
			m.Preview = preview(previewText(body))
		}
	}
	m.HasAttachments = attachmentsInStructure(buf.BodyStructure)
	return m
}

// readFlags reads the flags a fetch gave a message: whether it is read
// and flagged, and its keywords, which are never nil.
func readFlags(flags []imap.Flag) (seen, flagged bool, keywords []string) {
	keywords = []string{}
	for _, f := range flags {
		switch {
		case f == imap.FlagSeen:
			seen = true
		case f == imap.FlagFlagged:
			flagged = true
		case !strings.HasPrefix(string(f), `\`):
			keywords = append(keywords, string(f))
		}
	}
	return seen, flagged, keywords
}

// previewText turns a partial BODY[TEXT] into readable text. A multipart
// body starts at its first boundary, so the first text part is read
// directly instead of handing a truncated message to the MIME parser.
func previewText(raw []byte) string {
	s := string(raw)
	trimmed := strings.TrimLeft(s, "\r\n \t")
	if strings.HasPrefix(trimmed, "--") {
		type part struct {
			ctype string
			body  string
		}
		var (
			parts     []part
			cur       *part
			inHeaders bool
		)
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "--") {
				if cur != nil {
					parts = append(parts, *cur)
					cur = nil
				}
				inHeaders = true
				continue
			}
			if cur == nil {
				cur = &part{}
				inHeaders = true
			}
			if inHeaders {
				if strings.TrimSpace(line) == "" {
					inHeaders = false
					continue
				}
				if strings.HasPrefix(strings.ToLower(line), "content-type:") {
					cur.ctype = strings.ToLower(strings.TrimSpace(line[len("content-type:"):]))
				}
				continue
			}
			cur.body += line + "\n"
		}
		if cur != nil {
			parts = append(parts, *cur)
		}
		for _, p := range parts {
			if strings.HasPrefix(p.ctype, "text/plain") {
				return normalizeText(p.body)
			}
		}
		for _, p := range parts {
			if strings.HasPrefix(p.ctype, "text/html") {
				return HTMLToText(p.body)
			}
		}
		return ""
	}
	if strings.HasPrefix(trimmed, "<") {
		return HTMLToText(s)
	}
	return normalizeText(s)
}

func envelopeAddress(list []imap.Address) Address {
	if len(list) == 0 {
		return Address{}
	}
	a := list[0]
	return Address{Name: a.Name, Email: a.Addr()}
}

func envelopeAddresses(list []imap.Address) []Address {
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Addr()})
	}
	return out
}

// attachmentsInStructure reports whether a body structure has a part
// that is not text and not a multipart container.
func attachmentsInStructure(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			if strings.HasPrefix(single.MediaType(), "text/") {
				return true
			}
			if d := single.Disposition(); d != nil && strings.EqualFold(d.Value, "inline") &&
				strings.HasPrefix(single.MediaType(), "image/") {
				return true
			}
			found = true
		}
		return true
	})
	return found
}

// recentSeqSet selects the newest limit messages by sequence number.
func recentSeqSet(total uint32, limit int) imap.NumSet {
	var set imap.SeqSet
	if total == 0 {
		return set
	}
	start := uint32(1)
	if limit > 0 && int(total) > limit {
		start = total - uint32(limit) + 1
	}
	set.AddRange(start, 0) // 0 is *: the last message
	return set
}

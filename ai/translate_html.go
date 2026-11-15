package ai

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	htmlnode "golang.org/x/net/html"
)

// HTMLMessage is a message's HTML body as the window has it, with what
// names it to the model. Only the visible text of it is sent: never an
// attribute, a link's address, an image or an attachment, and only as the
// blocks the request names.
type HTMLMessage struct {
	// Scope ties a cached answer to the mail account it came from. It is
	// never sent.
	Scope     string
	Subject   string
	FromName  string
	FromEmail string
	Date      time.Time
	HTML      string
}

// htmlTranslateTokens is the answer budget of a translation of HTML
// mail: the segments come back as one JSON object, so it asks for more
// room than a translation of plain text does.
const htmlTranslateTokens = 8192

// htmlBlocks are the elements whose text is translated as one block: a
// paragraph, a list item, a table cell, a heading. Text is grouped by the
// block it is in, so a translation keeps the order and the shape of the
// message, and a `<td>` never merges into its neighbour.
var htmlBlocks = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"body": true, "caption": true, "dd": true, "div": true, "dl": true,
	"dt": true, "figcaption": true, "figure": true, "footer": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "li": true, "main": true, "nav": true, "ol": true,
	"p": true, "pre": true, "section": true, "summary": true,
	"table": true, "td": true, "th": true, "tr": true, "ul": true,
}

// htmlSkipped is never read, let alone sent.
var htmlSkipped = map[string]bool{
	"head": true, "noscript": true, "script": true, "style": true,
	"template": true, "title": true,
}

// htmlMarker numbers one run of text inside a block, so that a translation
// can be put back next to the markup that surrounded it.
var htmlMarker = regexp.MustCompile(`\{(\d+)\}`)

// htmlPart is one block of visible text: the text nodes it is made of, and
// their text with a marker before each, which is what the model reads.
type htmlPart struct {
	nodes []*htmlnode.Node
	text  string
}

// HTMLTranslation is a translated message with its markup kept, and the
// same text without it.
type HTMLTranslation struct {
	Translation
	// HTML is the message's HTML with its visible text translated: links,
	// tables, emphasis and pictures stay where they were.
	HTML string
	// Blocks is how many blocks were translated, Total how many the
	// message has.
	Blocks, Total int
}

// skipHTML reports whether an element's text is never read.
func skipHTML(n *htmlnode.Node) bool {
	if n.Type != htmlnode.ElementNode {
		return false
	}
	if htmlSkipped[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "translate":
			if strings.EqualFold(strings.TrimSpace(a.Val), "no") {
				return true
			}
		case "class":
			for _, class := range strings.Fields(a.Val) {
				if class == "notranslate" {
					return true
				}
			}
		}
	}
	return false
}

// hasLetter reports whether a run of text says anything a translation can
// change: punctuation, emoji and numbers are left where they are.
func hasLetter(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}

// planHTML parses a message's HTML and groups its translatable text by the
// block it is in. A block's text runs are one part, so a sentence that
// spans `<b>` or `<a>` is translated whole.
func planHTML(source string) (*htmlnode.Node, []htmlPart) {
	doc, err := htmlnode.Parse(strings.NewReader(source))
	if err != nil {
		return nil, nil
	}
	var parts []htmlPart
	var cur *htmlPart
	flush := func() {
		if cur != nil && len(cur.nodes) > 0 {
			parts = append(parts, *cur)
		}
		cur = nil
	}
	var walk func(*htmlnode.Node)
	walk = func(n *htmlnode.Node) {
		if skipHTML(n) {
			return
		}
		switch {
		case n.Type == htmlnode.ElementNode && (n.Data == "br" || n.Data == "hr"):
			// A line break ends the run: the text either side of it is
			// not one sentence.
			flush()
			return
		case n.Type == htmlnode.TextNode:
			if !hasLetter(n.Data) {
				// The space between two runs of inline text is not
				// translated, but the model needs to see it, or words
				// either side of a `<b>` would fuse.
				if cur != nil && strings.TrimSpace(n.Data) == "" && n.Data != "" {
					cur.text += " "
				}
				return
			}
			if cur == nil {
				cur = &htmlPart{}
			}
			i := len(cur.nodes)
			cur.nodes = append(cur.nodes, n)
			cur.text += "{" + strconv.Itoa(i) + "}" + n.Data
			return
		case n.Type == htmlnode.ElementNode && htmlBlocks[n.Data]:
			flush()
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			flush()
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	flush()
	return doc, parts
}

// stripHTMLMarkers is a part's text without the markers that place it.
func stripHTMLMarkers(s string) string { return htmlMarker.ReplaceAllString(s, "") }

// keepSpacing puts back the whitespace a text run had, which the spaces
// around inline markup are made of.
func keepSpacing(original, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(original)
	if len(runes) > 0 && unicode.IsSpace(runes[0]) {
		text = " " + text
	}
	if len(runes) > 0 && unicode.IsSpace(runes[len(runes)-1]) {
		text += " "
	}
	return text
}

// splitHTMLMarkers reads one block's translation into the runs it was made
// of. It fails when a marker is missing, repeated or out of order, which
// is why the caller has a fallback.
func splitHTMLMarkers(s string, n int) ([]string, bool) {
	matches := htmlMarker.FindAllStringSubmatchIndex(s, -1)
	if len(matches) != n {
		return nil, false
	}
	out := make([]string, 0, n)
	for i, m := range matches {
		if at, err := strconv.Atoi(s[m[2]:m[3]]); err != nil || at != i {
			return nil, false
		}
		start := m[1]
		if i == 0 {
			// A model that wrote a preamble kept it with the first run
			// rather than lose it.
			start = 0
		}
		end := len(s)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		out = append(out, s[start:end])
	}
	return out, true
}

// applyHTMLPart writes a block's translation back into the text runs it
// came from. A translation whose markers did not survive goes where the
// block starts, so the words keep their order even when the markup around
// them cannot be kept.
func applyHTMLPart(nodes []*htmlnode.Node, translated string) {
	pieces, ok := splitHTMLMarkers(translated, len(nodes))
	if !ok {
		for i, n := range nodes {
			if i == 0 {
				n.Data = keepSpacing(n.Data, stripHTMLMarkers(translated))
			} else {
				n.Data = ""
			}
		}
		return
	}
	for i, n := range nodes {
		n.Data = keepSpacing(n.Data, pieces[i])
	}
}

// htmlSegment is one block as the request lists it.
type htmlSegment struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// htmlRequest prepares the translation of the blocks that fit the limit,
// and reports what the excerpt left out. The model reads the blocks as a
// JSON array, which keeps them apart better than blank lines do.
func htmlRequest(m HTMLMessage, target string) (request, Excerpt, int, int) {
	_, parts := planHTML(m.HTML)
	if len(parts) == 0 {
		return request{feature: FeatureTranslate}, Excerpt{}, 0, 0
	}
	head := header(Message{Subject: m.Subject, FromName: m.FromName, FromEmail: m.FromEmail, Date: m.Date}) + "\n"
	// The JSON around the blocks and the markers inside them are part of
	// what is sent, so the limit is spent on the whole text, not on the
	// visible words alone.
	room := TranslateLimit - len([]rune(head))
	var (
		segments []htmlSegment
		body     []byte
	)
	for _, p := range parts {
		next := append(segments, htmlSegment{N: len(segments) + 1, Text: p.text})
		raw, err := json.Marshal(next)
		if err != nil || len([]rune(string(raw))) > room {
			break
		}
		segments, body = next, raw
	}
	excerpt := Excerpt{Truncated: len(segments) < len(parts)}
	if len(segments) == 0 {
		return request{feature: FeatureTranslate}, excerpt, 0, len(parts)
	}
	req := request{
		feature:   FeatureTranslate,
		system:    "You translate the blocks of one email's HTML into " + target + ". Each block is a numbered JSON string; inside one, a marker such as {0} or {1} stands for the inline formatting around a run of text, so keep every marker exactly as it is, in the same order and in the same place. Keep names, addresses, numbers, dates and code as they are. If the email is already in " + target + ", copy the blocks unchanged. Answer with one JSON object and nothing else: {\"source\": \"<the language the email is written in>\", \"segments\": [{\"n\": 1, \"text\": \"...\"}]}, one segment for each block you were given, in the same order." + untrusted,
		user:      head + string(body),
		maxTokens: htmlTranslateTokens,
	}
	return req, excerpt, len(segments), len(parts)
}

// TranslateHTMLPayload shows what TranslateHTML would send, without
// sending it.
func (s *Service) TranslateHTMLPayload(ctx context.Context, m HTMLMessage, target string) (Payload, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return Payload{}, err
	}
	req, e, sent, _ := htmlRequest(m, LanguageName(target))
	if req.user == "" {
		return Payload{}, &Error{Kind: KindEmpty}
	}
	return payload(cfg, req, TranslateLimit, sent, e), nil
}

// htmlAnswer is the object the instruction asks for.
type htmlAnswer struct {
	Source   string        `json:"source"`
	Segments []htmlSegment `json:"segments"`
}

// parseHTMLAnswer reads a model's answer, tolerating prose or code fences
// around the object, and keeps only blocks it was given.
func parseHTMLAnswer(text string, n int) (string, map[int]string, bool) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", nil, false
	}
	var a htmlAnswer
	if err := json.Unmarshal([]byte(text[start:end+1]), &a); err != nil {
		return "", nil, false
	}
	out := map[int]string{}
	for _, s := range a.Segments {
		if s.N < 1 || s.N > n || strings.TrimSpace(s.Text) == "" {
			continue
		}
		out[s.N] = s.Text
	}
	if len(out) == 0 {
		return "", nil, false
	}
	return strings.TrimSpace(a.Source), out, true
}

// spliceHTML writes the translated blocks back into the message and reads
// out of it the whole text without markup.
func spliceHTML(source string, translated map[int]string) (string, string) {
	doc, parts := planHTML(source)
	if doc == nil {
		return "", ""
	}
	var texts []string
	for i, p := range parts {
		tr, ok := translated[i+1]
		if ok {
			applyHTMLPart(p.nodes, tr)
		} else {
			// A block the limit left out stays in the message, so the
			// text of it stays whole too.
			tr = p.text
		}
		if body := strings.TrimSpace(stripHTMLMarkers(tr)); body != "" {
			texts = append(texts, body)
		}
	}
	var buf strings.Builder
	if err := htmlnode.Render(&buf, doc); err != nil {
		return "", strings.Join(texts, "\n\n")
	}
	return buf.String(), strings.Join(texts, "\n\n")
}

// TranslateHTML writes one message's HTML in another language without
// losing its markup: the visible text is translated block by block and
// put back where it was. The answer is cached by its input, as a
// translation of plain text is, and nothing in the mailbox changes.
func (s *Service) TranslateHTML(ctx context.Context, m HTMLMessage, target string) (HTMLTranslation, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return HTMLTranslation{}, err
	}
	if _, err := s.ready(cfg, FeatureTranslate); err != nil {
		return HTMLTranslation{}, err
	}
	name := LanguageName(target)
	if name == "" {
		return HTMLTranslation{}, &Error{Kind: KindBadConfig}
	}
	req, _, sent, total := htmlRequest(m, name)
	if req.user == "" {
		return HTMLTranslation{}, &Error{Kind: KindEmpty}
	}
	result, err := s.cachedAnswer(ctx, cfg, req, m.Scope)
	if err != nil {
		return HTMLTranslation{}, err
	}
	source, translated, ok := parseHTMLAnswer(result.Text, sent)
	if !ok {
		return HTMLTranslation{}, &Error{Kind: KindBadResponse}
	}
	htmlText, plain := spliceHTML(m.HTML, translated)
	if strings.TrimSpace(plain) == "" {
		return HTMLTranslation{}, &Error{Kind: KindBadResponse}
	}
	result.Text = plain
	return HTMLTranslation{
		Translation: Translation{Result: result, Source: source},
		HTML:        htmlText,
		Blocks:      len(translated),
		Total:       total,
	}, nil
}

package ai

import (
	"net/url"
	"regexp"
	"strings"
)

// The limits on what leaves the device. They are deliberately small: a
// summary, a triage or a draft needs the gist of a message, not all of it.
const (
	// BodyLimit is the most body text one summary or draft request sends,
	// in characters.
	BodyLimit = 4000
	// ConversationLimit is the most text one summary of a conversation
	// sends, in characters, all its messages together.
	ConversationLimit = 8000
	// PreviewLimit is the most preview text sent per triaged message.
	PreviewLimit = 160
	// TriageLimit is the most messages one triage request lists.
	TriageLimit = 50
	// GuidanceLimit is the most of the user's own guidance a draft sends.
	GuidanceLimit = 500
	// payloadLimit is the hard cap the egress enforces whatever built the
	// request, so no caller can send more than the features intend.
	payloadLimit = 24000
)

// Excerpt is the part of a message body that may be sent to a model, and
// what was left out to make it.
type Excerpt struct {
	Text string
	// Quoted counts the lines of quoted history removed.
	Quoted int
	// Signature reports that a signature block was removed.
	Signature bool
	// Links counts the links whose address was replaced by its host.
	Links int
	// Truncated reports that the text was cut at the limit.
	Truncated bool
}

var (
	linkPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'\x60\]\)]+`)

	// Lines that start the quoted copy of an earlier message in a reply.
	wrotePattern    = regexp.MustCompile(`(?i)^on\s.+\swrote:$`)
	wroteCJK        = regexp.MustCompile(`^.{0,200}写道[:：]$`)
	originalPattern = regexp.MustCompile(`(?i)^-{2,}\s*(original message|原始邮件)\s*-{2,}$`)
	// Lines that introduce a forwarded message, whose text is the point of
	// the mail rather than history.
	forwardPattern = regexp.MustCompile(`(?i)(forwarded message|begin forwarded message|转发的邮件|转发邮件)`)
	// The header block mail clients put above a quoted message.
	quotedFrom    = regexp.MustCompile(`(?i)^(from|发件人)\s*[:：]`)
	quotedHeaders = regexp.MustCompile(`(?i)^(sent|date|to|subject|发送时间|日期|收件人|主题)\s*[:：]`)
	// The one-line signatures phones add.
	mobileSignature = regexp.MustCompile(`(?i)^(sent from my |get outlook for |发自我的)`)
)

// MakeExcerpt reduces a plain text body to what a model needs: it drops
// the quoted history of a reply, the signature and the addresses of links
// (where tracking tokens live), and caps the length at limit characters.
// Attachments never reach it: only the body text is passed in.
func MakeExcerpt(body string, limit int) Excerpt {
	var e Excerpt
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	lines := strings.Split(body, "\n")

	kept := make([]string, 0, len(lines))
	forwarded := 0
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trimmed := strings.TrimSpace(line)
		if forwardPattern.MatchString(trimmed) {
			// The headers under a forward marker belong to the forwarded
			// message, not to a quoted reply.
			forwarded = 8
			kept = append(kept, line)
			continue
		}
		if forwarded > 0 {
			forwarded--
		}
		if startsHistory(lines, i, forwarded > 0) {
			for _, rest := range lines[i:] {
				if strings.TrimSpace(rest) != "" {
					e.Quoted++
				}
			}
			break
		}
		if strings.HasPrefix(trimmed, ">") {
			e.Quoted++
			continue
		}
		// "-- " is the signature delimiter of RFC 3676; clients that trim
		// trailing spaces leave "--", which is what line holds either way.
		if line == "--" || mobileSignature.MatchString(trimmed) {
			e.Signature = true
			// Everything below a signature is dropped with it. History
			// quoted under it is still counted, so the user is told the
			// truth about what was left out.
			for j := i + 1; j < len(lines); j++ {
				if startsHistory(lines, j, false) || strings.HasPrefix(strings.TrimSpace(lines[j]), ">") {
					for _, rest := range lines[j:] {
						if strings.TrimSpace(rest) != "" {
							e.Quoted++
						}
					}
					break
				}
			}
			break
		}
		kept = append(kept, line)
	}

	text := strings.Join(kept, "\n")
	text = linkPattern.ReplaceAllStringFunc(text, func(raw string) string {
		e.Links++
		// Punctuation that ends a sentence is not part of the link.
		tail := ""
		for len(raw) > 0 && strings.ContainsRune(".,;:!?", rune(raw[len(raw)-1])) {
			tail = string(raw[len(raw)-1]) + tail
			raw = raw[:len(raw)-1]
		}
		host := ""
		if u, err := url.Parse(raw); err == nil {
			host = u.Hostname()
		}
		if host == "" {
			return "[link]" + tail
		}
		return "[link: " + host + "]" + tail
	})
	text = collapseBlankLines(text)

	if limit > 0 {
		if runes := []rune(text); len(runes) > limit {
			text = strings.TrimSpace(string(runes[:limit])) + "…"
			e.Truncated = true
		}
	}
	e.Text = text
	return e
}

// startsHistory reports whether line i begins the quoted copy of an
// earlier message.
func startsHistory(lines []string, i int, forwarded bool) bool {
	line := strings.TrimSpace(lines[i])
	if line == "" {
		return false
	}
	if originalPattern.MatchString(line) || wrotePattern.MatchString(line) || wroteCJK.MatchString(line) {
		return true
	}
	// "On <date>, <name> <address>" wraps before "wrote:" in many clients.
	if strings.HasPrefix(strings.ToLower(line), "on ") && i+1 < len(lines) {
		next := strings.ToLower(strings.TrimSpace(lines[i+1]))
		if next == "wrote:" || (strings.HasSuffix(next, "wrote:") && len(next) < 120) {
			return true
		}
	}
	if forwarded || !quotedFrom.MatchString(line) {
		return false
	}
	// A "From:" line is history only as the top of a header block.
	headers := 0
	for j := i + 1; j < len(lines) && j <= i+5; j++ {
		if quotedHeaders.MatchString(strings.TrimSpace(lines[j])) {
			headers++
		}
	}
	return headers >= 2
}

// collapseBlankLines trims the text and leaves at most one blank line
// between paragraphs.
func collapseBlankLines(s string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// clip caps a single field, such as a subject, at limit characters.
func clip(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return s
}

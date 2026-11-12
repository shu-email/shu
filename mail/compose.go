package mail

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ComposeFormat is the format messages are written and sent in: plain
// text, the only one. Rich text is not offered.
const ComposeFormat = "text/plain"

// SendDelays are the choices of how long a message waits before it
// leaves, in seconds, which is the time there is to take it back; 0
// sends it at once. DefaultSendDelay is the one in use until the user
// picks.
var SendDelays = []int{0, 5, 10, 20, 30}

// DefaultSendDelay is the send delay of a new install, in seconds.
const DefaultSendDelay = 10

// ReplyMode is what the Reply action answers by default: the sender, or
// everyone the message went to.
type ReplyMode string

// The reply modes.
const (
	ReplySender ReplyMode = "reply"
	ReplyAll    ReplyMode = "reply-all"
)

// QuoteStyle is how a reply quotes the message it answers.
type QuoteStyle string

// The quote styles.
const (
	// QuotePrefix introduces the message with a line naming its sender
	// and puts "> " before each of its lines.
	QuotePrefix QuoteStyle = "prefix"
	// QuoteHeader puts the message as it was under a block of its headers,
	// as an "original message".
	QuoteHeader QuoteStyle = "header"
)

// ReplyPosition is where a reply is written: above the quote, or below
// it.
type ReplyPosition string

// The reply positions.
const (
	ReplyAbove ReplyPosition = "above"
	ReplyBelow ReplyPosition = "below"
)

// ComposeSettings are how messages are written and sent, the same for
// every account. A value nobody offered is not obeyed: the default stands
// in for it.
type ComposeSettings struct {
	// SendDelay is one of SendDelays: how many seconds a message waits
	// before it leaves, with the time to take it back.
	SendDelay int
	// Reply is what the Reply action does.
	Reply ReplyMode
	// Quote is how a reply quotes, and Position where it is written.
	Quote    QuoteStyle
	Position ReplyPosition
	// ForwardAttachments has a forward carry the attachments of the
	// message it forwards. A reply never does.
	ForwardAttachments bool
}

// DefaultComposeSettings are the settings of a new install: a short delay
// to take a message back, a reply to the sender written above a quote
// prefixed with "> ", and forwards with their attachments.
func DefaultComposeSettings() ComposeSettings {
	return ComposeSettings{
		SendDelay: DefaultSendDelay, Reply: ReplySender,
		Quote: QuotePrefix, Position: ReplyAbove, ForwardAttachments: true,
	}
}

// Delay is the send delay as a duration.
func (c ComposeSettings) Delay() time.Duration { return time.Duration(c.SendDelay) * time.Second }

// checked returns the settings with the default in place of each value
// that is none of the choices.
func (c ComposeSettings) checked() ComposeSettings {
	def := DefaultComposeSettings()
	if !slices.Contains(SendDelays, c.SendDelay) {
		c.SendDelay = def.SendDelay
	}
	if c.Reply != ReplySender && c.Reply != ReplyAll {
		c.Reply = def.Reply
	}
	if c.Quote != QuotePrefix && c.Quote != QuoteHeader {
		c.Quote = def.Quote
	}
	if c.Position != ReplyAbove && c.Position != ReplyBelow {
		c.Position = def.Position
	}
	return c
}

// The keys of the compose settings in the store.
const (
	settingSendDelay          = "compose.send_delay"
	settingReplyMode          = "compose.reply"
	settingQuoteStyle         = "compose.quote"
	settingReplyPosition      = "compose.reply_position"
	settingForwardAttachments = "compose.forward_attachments"
)

// ComposeSettings reads the compose settings, with the default for each
// that was never set or reads as none of its choices.
func (s *Service) ComposeSettings(ctx context.Context) (ComposeSettings, error) {
	out := DefaultComposeSettings()
	for _, read := range []struct {
		key  string
		into func(string)
	}{
		{settingSendDelay, func(v string) {
			if n, err := strconv.Atoi(v); err == nil {
				out.SendDelay = n
			}
		}},
		{settingReplyMode, func(v string) { out.Reply = ReplyMode(v) }},
		{settingQuoteStyle, func(v string) { out.Quote = QuoteStyle(v) }},
		{settingReplyPosition, func(v string) { out.Position = ReplyPosition(v) }},
		{settingForwardAttachments, func(v string) { out.ForwardAttachments = v != "0" }},
	} {
		raw, ok, err := s.store.Setting(ctx, read.key)
		if err != nil {
			return DefaultComposeSettings(), err
		}
		if ok {
			read.into(raw)
		}
	}
	return out.checked(), nil
}

// SaveComposeSettings writes the compose settings, each that is none of
// its choices as its default.
func (s *Service) SaveComposeSettings(ctx context.Context, set ComposeSettings) error {
	set = set.checked()
	forward := "0"
	if set.ForwardAttachments {
		forward = "1"
	}
	for _, kv := range [][2]string{
		{settingSendDelay, strconv.Itoa(set.SendDelay)},
		{settingReplyMode, string(set.Reply)},
		{settingQuoteStyle, string(set.Quote)},
		{settingReplyPosition, string(set.Position)},
		{settingForwardAttachments, forward},
	} {
		if err := s.store.SetSetting(ctx, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// Signature is what an account signs its mail with, in plain text, and
// the messages it goes into. One without text adds nothing.
type Signature struct {
	Text    string `json:"text"`
	New     bool   `json:"new"`
	Reply   bool   `json:"reply"`
	Forward bool   `json:"forward"`
}

// DefaultSignature is the signature of a new account: no text yet, and
// once there is, in new mail, replies and forwards alike.
func DefaultSignature() Signature { return Signature{New: true, Reply: true, Forward: true} }

// SignatureKey is the key of an account's signature among the settings.
// The store removes it with the account.
func SignatureKey(accountID string) string { return "compose.signature:" + accountID }

// Signature reads an account's signature, the default when it was never
// set or does not read.
func (s *Service) Signature(ctx context.Context, accountID string) (Signature, error) {
	raw, ok, err := s.store.Setting(ctx, SignatureKey(accountID))
	if err != nil || !ok {
		return DefaultSignature(), err
	}
	// What the stored value leaves out keeps its default.
	sig := DefaultSignature()
	if err := json.Unmarshal([]byte(raw), &sig); err != nil {
		return DefaultSignature(), nil
	}
	return sig, nil
}

// SaveSignature writes an account's signature.
func (s *Service) SaveSignature(ctx context.Context, accountID string, sig Signature) error {
	// A row for an account removed meanwhile would never be deleted.
	if _, ok, err := s.store.Account(ctx, accountID); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	raw, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, SignatureKey(accountID), string(raw))
}

// Body is the text a message starts with, and where in it the user
// writes: Caret is an offset in runes, as text inputs take one.
type Body struct {
	Text  string
	Caret int
}

// QuoteWords are the words a quote is written with, in the user's
// language. The engine has no catalog: the window passes its own.
type QuoteWords struct {
	// Wrote is the line that introduces a quote prefixed with "> ", as
	// "On {date}, {name} wrote:".
	Wrote func(date, name string) string
	// Original heads the block of headers of a reply quoted in that
	// style, as "-------- Original Message --------", and Forwarded the
	// one of a forward.
	Original  string
	Forwarded string
	// The names of the headers in such a block.
	From, To, Cc, Date, Subject string
	// FormatDate writes the date of the message quoted. Without it, the
	// date is written as RFC 1123 has it, with the zone's offset.
	FormatDate func(time.Time) string
}

// date writes a date as the words have dates written.
func (w QuoteWords) date(t time.Time) string {
	switch {
	case t.IsZero():
		return ""
	case w.FormatDate != nil:
		return w.FormatDate(t)
	}
	return t.Format(time.RFC1123Z)
}

// SignatureBlock is a signature as a body carries it: the line "-- ",
// which marks a signature in plain text (RFC 3676), then its text. A
// signature without text has none. A marker the user typed is the one.
func SignatureBlock(text string) string {
	lines := strings.Split(lineBreaks(text), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.TrimRight(lines[0], " ") == "--" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return "-- \n" + strings.Join(lines, "\n")
}

// NewBody is the body a new message starts with: room to write, and the
// signature below it where the account signs new mail.
func NewBody(sig Signature) Body {
	return layout(nil, signatureFor(sig, sig.New))
}

// ReplyBody is the body a reply to m starts with: the quote of its text,
// in the settings' style, with room to write above it or below it as
// they say, and the account's signature where it signs replies: above
// the quote for a reply written above it, and at the end for one written
// below. A message without text is not quoted.
func ReplyBody(set ComposeSettings, sig Signature, m Summary, text string, words QuoteWords) Body {
	set = set.checked()
	quote := ""
	if text = strings.TrimRight(lineBreaks(text), "\n"); strings.TrimSpace(text) != "" {
		if set.Quote == QuoteHeader {
			quote = headerBlock(words.Original, m, text, words)
		} else {
			quote = prefixQuote(m, text, words)
		}
	}
	signature := signatureFor(sig, sig.Reply)
	if set.Position == ReplyBelow {
		return layout([]string{quote}, signature)
	}
	return layout(nil, signature, quote)
}

// ForwardBody is the body a forward of m starts with: room for a note,
// the account's signature below it where it signs forwards, and the
// message as it was under a block of its headers. A forward does not
// quote as a reply does: it passes the message on.
func ForwardBody(sig Signature, m Summary, text string, words QuoteWords) Body {
	text = strings.TrimRight(lineBreaks(text), "\n")
	return layout(nil, signatureFor(sig, sig.Forward), headerBlock(words.Forwarded, m, text, words))
}

// signatureFor is the signature's block for a kind of message it goes
// into or not.
func signatureFor(sig Signature, into bool) string {
	if !into {
		return ""
	}
	return SignatureBlock(sig.Text)
}

// layout puts a body's parts one blank line apart, those that are empty
// left out: the parts above the room the user writes in, which is where
// the caret goes, and those below it.
func layout(above []string, below ...string) Body {
	var b strings.Builder
	for _, part := range above {
		if part != "" {
			b.WriteString(part)
			b.WriteString("\n\n")
		}
	}
	caret := utf8.RuneCountInString(b.String())
	for _, part := range below {
		if part != "" {
			b.WriteString("\n\n")
			b.WriteString(part)
		}
	}
	return Body{Text: b.String(), Caret: caret}
}

// prefixQuote quotes text line by line after a line saying who wrote it
// and when. An empty line is quoted without the space, which would end
// it in blank.
func prefixQuote(m Summary, text string, words QuoteWords) string {
	var b strings.Builder
	if words.Wrote != nil {
		name := m.From.Name
		if name == "" {
			name = m.From.Email
		}
		b.WriteString(words.Wrote(words.date(m.Date), name))
		b.WriteString("\n")
	}
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		if line == "" {
			b.WriteString(">")
		} else {
			b.WriteString("> " + line)
		}
	}
	return b.String()
}

// headerBlock puts text, as it is, under a title and the headers of the
// message it is from.
func headerBlock(title string, m Summary, text string, words QuoteWords) string {
	var b strings.Builder
	b.WriteString(title)
	for _, field := range [][2]string{
		{words.From, m.From.String()},
		{words.Date, words.date(m.Date)},
		{words.Subject, m.Subject},
		{words.To, addressesText(m.To)},
		{words.Cc, addressesText(m.Cc)},
	} {
		if field[1] != "" {
			b.WriteString("\n" + field[0] + ": " + field[1])
		}
	}
	if text != "" {
		b.WriteString("\n\n" + text)
	}
	return b.String()
}

// lineBreaks writes every line break as "\n".
func lineBreaks(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

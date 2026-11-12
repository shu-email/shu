package mail

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Search is what a search of the cache asks of a message. Its parts
// combine: a message is found when it matches every part that is set. A
// term is found in a part of a message that contains it, in any case:
// through the full-text index from three characters on, by comparing the
// text below that.
type Search struct {
	// Text are terms each to be found in the subject, the sender, the
	// recipients or the body of a message.
	Text []string `json:"text,omitempty"`
	// From, To, Subject and Body are terms each to be found in that part.
	From    []string `json:"from,omitempty"`
	To      []string `json:"to,omitempty"`
	Subject []string `json:"subject,omitempty"`
	Body    []string `json:"body,omitempty"`
	// Since and Before bound the date: from Since on, and before Before.
	// A zero time leaves that end open.
	Since  time.Time `json:"since,omitempty"`
	Before time.Time `json:"before,omitempty"`
	// Unread, Starred and Attachment keep the unread messages, the
	// flagged ones and those with attachments.
	Unread     bool `json:"unread,omitempty"`
	Starred    bool `json:"starred,omitempty"`
	Attachment bool `json:"attachment,omitempty"`
	// In keeps the messages of the folders a typed name means (see
	// Folder.NamedBy), in each account of the scope.
	In string `json:"in,omitempty"`
}

// IsZero reports whether the search asks nothing, which every message
// matches.
func (s Search) IsZero() bool {
	return len(s.Text) == 0 && len(s.From) == 0 && len(s.To) == 0 && len(s.Subject) == 0 &&
		len(s.Body) == 0 && s.Since.IsZero() && s.Before.IsZero() &&
		!s.Unread && !s.Starred && !s.Attachment && strings.TrimSpace(s.In) == ""
}

// Field is a set of the parts of a message a search looks in.
type Field uint8

const (
	FieldSubject Field = 1 << iota
	FieldFrom
	// FieldTo is the recipients: To and Cc.
	FieldTo
	// FieldBody is the body, or the preview of a body not downloaded.
	FieldBody
)

// FieldAny is every part free text is looked for in.
const FieldAny = FieldSubject | FieldFrom | FieldTo | FieldBody

// Has reports whether f holds every part of g.
func (f Field) Has(g Field) bool { return g != 0 && f&g == g }

// Match is a message a search found, and the parts of it its terms were
// found in. Fields is empty for a search of flags, dates or folders
// alone.
type Match struct {
	MessageRef
	Fields Field `json:"fields,omitempty"`
}

// NamedBy reports whether a name the user typed means the folder: its own
// name in any case, or the common name of its special use, as "sent"
// means the Sent folder whatever the server calls it.
func (f Folder) NamedBy(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.EqualFold(f.Name, name) {
		return true
	}
	special := specialUseByName(name)
	return special != "" && special == f.SpecialUse
}

// ParseSearch reads a search as it is typed. Words are free text, each to
// be found somewhere in a message, and a "quoted phrase" is one term.
// These narrow it:
//
//	from:maya          the sender
//	to:maya            a recipient, To or Cc
//	subject:budget     the subject
//	body:invoice       the body
//	has:attachment     messages with attachments
//	is:unread          unread messages
//	is:starred         flagged messages
//	after:2026-03-01   on that day or later
//	before:2026-03-01  before that day
//	in:sent            a folder, by its name or its special use
//
// A value may be quoted, as in from:"Maya Lin". Operators are read in any
// case, and their colon may be the full-width one Chinese input types.
// Days are whole days in loc, as long as they last there. An operator
// that is not one of these, or whose value is not one it takes, is free
// text; one without a value narrows nothing.
func ParseSearch(text string, loc *time.Location) Search {
	if loc == nil {
		loc = time.Local
	}
	var s Search
	for _, tok := range searchTokens(text) {
		if tok.key == "" || !s.apply(tok, loc) {
			if term := strings.TrimSpace(tok.text); term != "" {
				s.Text = append(s.Text, term)
			}
		}
	}
	return s
}

// searchToken is a free-text term, or an operator with its value; text is
// what to search for when it is no operator.
type searchToken struct {
	key, value, text string
}

// apply narrows the search by an operator, and reports false when the
// operator or its value is not one it takes.
func (s *Search) apply(tok searchToken, loc *time.Location) bool {
	value := strings.TrimSpace(tok.value)
	if value == "" {
		return true
	}
	switch tok.key {
	case "from":
		s.From = append(s.From, value)
	case "to":
		s.To = append(s.To, value)
	case "subject":
		s.Subject = append(s.Subject, value)
	case "body":
		s.Body = append(s.Body, value)
	case "in":
		s.In = value
	case "has":
		if !strings.EqualFold(value, "attachment") {
			return false
		}
		s.Attachment = true
	case "is":
		switch strings.ToLower(value) {
		case "unread":
			s.Unread = true
		case "starred":
			s.Starred = true
		default:
			return false
		}
	case "after", "before":
		day, ok := parseDay(value, loc)
		if !ok {
			return false
		}
		if tok.key == "after" && day.After(s.Since) {
			s.Since = day
		}
		if tok.key == "before" && (s.Before.IsZero() || day.Before(s.Before)) {
			s.Before = day
		}
	default:
		return false
	}
	return true
}

// parseDay reads a day, as 2026-03-01 or 2026/03/01, and returns when it
// begins in loc.
func parseDay(value string, loc *time.Location) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006/01/02"} {
		if day, err := time.ParseInLocation(layout, value, loc); err == nil {
			return day, true
		}
	}
	return time.Time{}, false
}

// searchOperators are the operators ParseSearch knows.
var searchOperators = map[string]bool{
	"from": true, "to": true, "subject": true, "body": true, "in": true,
	"has": true, "is": true, "after": true, "before": true,
}

// isQuote reports whether r opens or closes a phrase: the ASCII quote
// and the curly ones that smart punctuation and Chinese input type.
func isQuote(r rune) bool { return r == '"' || r == '“' || r == '”' }

// searchTokens splits typed text into terms and operators. A quote that
// is never closed runs to the end.
func searchTokens(text string) []searchToken {
	runes := []rune(text)
	// quoted reads the phrase opened at i and returns where it ends.
	quoted := func(i int) (string, int) {
		end := i + 1
		for end < len(runes) && !isQuote(runes[end]) {
			end++
		}
		phrase := string(runes[i+1 : end])
		if end < len(runes) {
			end++
		}
		return phrase, end
	}
	var out []searchToken
	for i := 0; i < len(runes); {
		switch r := runes[i]; {
		case unicode.IsSpace(r):
			i++
			continue
		case isQuote(r):
			phrase, end := quoted(i)
			out = append(out, searchToken{text: phrase})
			i = end
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) && !isQuote(runes[i]) {
			i++
		}
		word := string(runes[start:i])
		colon := strings.IndexAny(word, ":：")
		if colon < 0 || !searchOperators[strings.ToLower(word[:colon])] {
			out = append(out, searchToken{text: word})
			continue
		}
		key := strings.ToLower(word[:colon])
		_, size := utf8.DecodeRuneInString(word[colon:])
		value := word[colon+size:]
		if value == "" && i < len(runes) && isQuote(runes[i]) {
			value, i = quoted(i)
		}
		out = append(out, searchToken{key: key, value: value, text: string(runes[start:i])})
	}
	return out
}

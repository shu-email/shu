package mail

import (
	"context"
	"net/mail"
	"strings"
)

// RecipientEntry is one entry of a recipient field: the text between two
// of the commas that part its entries, and where it is in the field.
type RecipientEntry struct {
	// Text is the entry without the spaces around it.
	Text string `json:"text"`
	// Start and End are the byte offsets in the field of all that lies
	// between the commas, its spaces included.
	Start int `json:"start"`
	End   int `json:"end"`
	// Valid reports that the entry is an address, or a group of them, as
	// net/mail reads it, and Addresses are the addresses it is.
	Valid     bool      `json:"valid,omitempty"`
	Addresses []Address `json:"addresses,omitempty"`
}

// RecipientEntries splits a recipient field into its entries, at the
// commas that are not in quotes, in a comment, in angle brackets or in a
// group, and leaves out the empty ones.
func RecipientEntries(field string) []RecipientEntry {
	var out []RecipientEntry
	add := func(start, end int) {
		text := strings.TrimSpace(field[start:end])
		if text == "" {
			return
		}
		e := RecipientEntry{Text: text, Start: start, End: end}
		if list, err := mail.ParseAddressList(text); err == nil {
			e.Valid = true
			for _, a := range list {
				e.Addresses = append(e.Addresses, Address{Name: a.Name, Email: a.Address})
			}
		}
		out = append(out, e)
	}
	start, comment := 0, 0
	quoted, escaped, angle, group := false, false, false, false
	for i, r := range field {
		switch {
		case escaped:
			escaped = false
		case quoted:
			switch r {
			case '\\':
				escaped = true
			case '"':
				quoted = false
			}
		case comment > 0:
			switch r {
			case '\\':
				escaped = true
			case '(':
				comment++
			case ')':
				comment--
			}
		case r == '"':
			quoted = true
		case r == '(':
			comment = 1
		case angle:
			angle = r != '>'
		case r == '<':
			angle = true
		case r == ':':
			group = true
		case r == ';':
			group = false
		case r == ',' && !group:
			add(start, i)
			start = i + 1
		}
	}
	add(start, len(field))
	return out
}

// RecipientKind is what an entry of a recipient field that is no address
// is.
type RecipientKind int

// The entries of a recipient field that cannot be sent to.
const (
	// RecipientInvalid is a broken address, as "bob@" or
	// "<bob@example.com".
	RecipientInvalid RecipientKind = iota + 1
	// RecipientUnknown is a bare name or word no contact answers to.
	RecipientUnknown
	// RecipientAmbiguous is a bare name or word several contacts answer
	// to.
	RecipientAmbiguous
	// RecipientMatch is a bare name or word one contact answers to: the
	// one to offer in its place.
	RecipientMatch
)

// RecipientProblem is an entry of a recipient field that is no address.
type RecipientProblem struct {
	RecipientEntry
	Kind RecipientKind `json:"kind"`
	// Matches are the contacts a bare name or word may mean, as Suggest
	// finds them, best first, at most DefaultSuggestions.
	Matches []Contact `json:"matches,omitempty"`
}

// RecipientCheck is what CheckRecipients read in a recipient field.
type RecipientCheck struct {
	// Addresses are the addresses of the field, in order.
	Addresses []Address `json:"addresses,omitempty"`
	// Problems are its entries that are no address, in order. The field
	// can be sent to only when there are none.
	Problems []RecipientProblem `json:"problems,omitempty"`
}

// OK reports whether the field can be sent to as it is.
func (c RecipientCheck) OK() bool { return len(c.Problems) == 0 }

// CheckRecipients reads a recipient field as a message to send takes it.
// A field net/mail reads whole is the addresses it reads, so that nothing
// counts as an address that did not before. Otherwise each entry that is
// no address is a problem, of its kind: a broken address, or a bare name
// or word, which the address book is asked about as Suggest asks it, so
// that the window can say which and offer the contact it means when one
// alone answers to it. It reads the address book for those entries alone.
func (s *Service) CheckRecipients(ctx context.Context, field string) (RecipientCheck, error) {
	if strings.TrimSpace(field) == "" {
		return RecipientCheck{}, nil
	}
	if list, err := ParseAddressList([]string{field}); err == nil {
		return RecipientCheck{Addresses: list}, nil
	}
	var check RecipientCheck
	for _, e := range RecipientEntries(field) {
		if e.Valid {
			check.Addresses = append(check.Addresses, e.Addresses...)
			continue
		}
		p := RecipientProblem{RecipientEntry: e, Kind: RecipientInvalid}
		if words := ContactWords(e.Text); len(words) > 0 && !strings.ContainsAny(e.Text, `@<>":;`) {
			matches, err := s.store.Contacts(ctx, ContactQuery{Words: words, Limit: DefaultSuggestions})
			if err != nil {
				return RecipientCheck{}, err
			}
			p.Matches = matches
			switch len(matches) {
			case 0:
				p.Kind = RecipientUnknown
			case 1:
				p.Kind = RecipientMatch
			default:
				p.Kind = RecipientAmbiguous
			}
		}
		check.Problems = append(check.Problems, p)
	}
	if len(check.Problems) == 0 {
		// Each entry reads alone, and the field does not: it is the field
		// as a whole that is wrong.
		check.Problems = []RecipientProblem{{
			RecipientEntry: RecipientEntry{Text: strings.TrimSpace(field), End: len(field)},
			Kind:           RecipientInvalid,
		}}
	}
	return check, nil
}

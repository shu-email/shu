package mail

import "strings"

// What a message's headers say of its sender and of the list it came
// from, read once and kept with the cached message (see Security), so
// that the reading pane has it after a restart and without the headers.
// Nothing here looks anything up on the network.

// The headers that say how a message's list is left. The reading pane
// offers to leave it; nothing here does.
const (
	listUnsubscribeHeader     = "List-Unsubscribe"
	listUnsubscribePostHeader = "List-Unsubscribe-Post"
)

// unsubscribeHeaders are kept in RawHeaders besides the security ones
// (see securityHeaders).
var unsubscribeHeaders = []string{listUnsubscribeHeader, listUnsubscribePostHeader}

// Security is what a message's headers said of it when it was cached:
// whether its sender is authentic, and how to leave the list it came
// from. A message whose headers said none of it has no Security, and
// stays unknown rather than safe.
type Security struct {
	Trust Trust `json:"trust"`
	// ListUnsubscribe is the List-Unsubscribe header's values, one per
	// line, and ListUnsubscribePost the List-Unsubscribe-Post header's,
	// as they came. Empty when the message has none.
	ListUnsubscribe     string `json:"listUnsubscribe,omitempty"`
	ListUnsubscribePost string `json:"listUnsubscribePost,omitempty"`
}

// Unsubscribe reads how the message's list is left: the List-Unsubscribe
// values, one per line, and the List-Unsubscribe-Post that says whether
// the list takes a one-click request. Both are empty when the message is
// not from a list.
func (m Message) Unsubscribe() (list, post string) {
	if m.Security != nil {
		return m.Security.ListUnsubscribe, m.Security.ListUnsubscribePost
	}
	return strings.Join(headerValues(m.RawHeaders, listUnsubscribeHeader), "\n"),
		strings.Join(headerValues(m.RawHeaders, listUnsubscribePostHeader), "\n")
}

// SetRawHeaders keeps the header lines of a message, and reads from them
// what they say of it.
func (m *Message) SetRawHeaders(raw string) {
	m.RawHeaders = raw
	m.Security = ParseSecurity(raw)
}

// ParseSecurity reads what the security headers a message's RawHeaders
// keep say of it. It returns nil when they keep none: a message cached
// before they were, or one whose server said nothing of it.
func ParseSecurity(rawHeaders string) *Security {
	s := &Security{Trust: parseTrust(rawHeaders)}
	if values := headerValues(rawHeaders, listUnsubscribeHeader); len(values) > 0 {
		s.ListUnsubscribe = strings.Join(values, "\n")
	}
	if values := headerValues(rawHeaders, listUnsubscribePostHeader); len(values) > 0 {
		s.ListUnsubscribePost = strings.Join(values, "\n")
	}
	if len(s.Trust.Checks) == 0 && len(s.Trust.Evidence) == 0 &&
		s.ListUnsubscribe == "" && s.ListUnsubscribePost == "" {
		return nil
	}
	return s
}

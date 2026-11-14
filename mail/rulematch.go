package mail

import (
	"mime"
	"slices"
	"strings"

	"github.com/emersion/go-message/charset"
)

// foldText is what the conditions compare: text in which two characters
// that differ in case alone are one, as label names are.
func foldText(s string) string { return LabelNameKey(s) }

// containsFold reports whether text contains what a condition looks for,
// in any case.
func containsFold(text, value string) bool {
	return value != "" && strings.Contains(foldText(text), foldText(value))
}

// headerValues returns the values of a header among a message's RawHeaders,
// their encoded words decoded.
func headerValues(raw, name string) []string {
	decoder := &mime.WordDecoder{CharsetReader: charset.Reader}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		value = strings.TrimSpace(value)
		if decoded, err := decoder.DecodeHeader(value); err == nil {
			value = decoded
		}
		out = append(out, value)
	}
	return out
}

// holds reports whether a message meets the condition. has reports
// whether it has a label.
func (c Condition) holds(m Message, has func(LabelID) bool) bool {
	switch c.Field {
	case CondSender:
		return containsFold(m.From.Name, c.Value) || containsFold(m.From.Email, c.Value)
	case CondRecipients:
		return slices.ContainsFunc(append(slices.Clone(m.To), m.Cc...), func(a Address) bool {
			return containsFold(a.Name, c.Value) || containsFold(a.Email, c.Value)
		})
	case CondSubject:
		return containsFold(m.Subject, c.Value)
	case CondHeader:
		return slices.ContainsFunc(headerValues(m.RawHeaders, c.Header), func(v string) bool { return containsFold(v, c.Value) })
	case CondAttachment:
		return m.HasAttachments
	case CondAccount:
		return m.Account == c.Account
	case CondFolder:
		return m.Account == c.Account && m.Folder == c.Folder
	case CondLabel:
		return m.Account == c.Account && has(c.Label)
	}
	return false
}

// matches reports whether a message meets the rule's conditions, as its
// mode combines them. The message has its Account, Folder and Labels as a
// read of the cache gives them; has reports the labels it has as the
// rules before this one left it. A rule that names an account matches the
// messages of no other.
func (r Rule) matches(m Message, has func(LabelID) bool) bool {
	if a := r.Account(); (a != "" && m.Account != a) || len(r.Conditions) == 0 {
		return false
	}
	for _, c := range r.Conditions {
		held := c.holds(m, has)
		if r.Match == MatchAny && held {
			return true
		}
		if r.Match != MatchAny && !held {
			return false
		}
	}
	return r.Match != MatchAny
}

// sortMessage runs rules, in order, over a message that arrived in folder,
// and returns those that matched it: each enabled rule that watches the
// folder and whose conditions the message meets, up to the first that
// moves it or stops. A label a rule puts on or takes off counts for the
// conditions of the rules after it.
func sortMessage(rules []Rule, m Message, folder Folder) []Rule {
	labels := map[LabelID]bool{}
	for _, l := range m.Labels {
		labels[l.ID] = true
	}
	has := func(id LabelID) bool { return labels[id] }
	var out []Rule
	for _, r := range rules {
		if r.Paused || !r.watches(m.Account, folder) || !r.matches(m, has) {
			continue
		}
		out = append(out, r)
		for _, a := range r.Actions {
			switch a.Kind {
			case ActionAddLabel:
				labels[a.Label] = true
			case ActionRemoveLabel:
				delete(labels, a.Label)
			}
		}
		if r.stops() {
			break
		}
	}
	return out
}

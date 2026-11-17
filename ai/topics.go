package ai

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// TopicItem is one message as the topic grouping reads it: its identity in
// the window, and the subject, which is all that leaves the device.
type TopicItem struct {
	ID      string
	Subject string
}

// Topic is one group of the listed mail: the name the model gave it, and
// the messages it took.
type Topic struct {
	Name string
	IDs  []string
}

const (
	// TopicLimit is the most messages one grouping request lists. A list
	// on screen reads a page at a time, so this is a page.
	TopicLimit = 60
	// TopicChars is the most of the listed subjects one request sends.
	TopicChars = 6000
)

// A line of the answer: the number of the message, then its topic.
var topicLine = regexp.MustCompile(`^\s*(\d+)\s*[.:)\-]\s*(.+?)\s*$`)

// topicsRequest lists the subjects, numbered, and asks for one short topic
// for each. Subjects alone go: no body, no address, no attachment.
func topicsRequest(items []TopicItem, language string) (request, Excerpt, []TopicItem) {
	if len(items) > TopicLimit {
		items = items[:TopicLimit]
	}
	var b strings.Builder
	sent := 0
	for i, it := range items {
		subject := strings.Join(strings.Fields(clip(it.Subject, 200)), " ")
		if subject == "" {
			subject = "(no subject)"
		}
		line := fmt.Sprintf("%d. %s\n", i+1, subject)
		if b.Len()+len(line) > TopicChars {
			break
		}
		b.WriteString(line)
		sent++
	}
	items = items[:sent]
	lang := " Write each topic in the language of its subjects."
	if strings.TrimSpace(language) != "" {
		lang = " Write each topic in " + strings.TrimSpace(language) + "."
	}
	return request{
		feature: FeatureTopics,
		system: "You sort a mailbox into topics by what each subject is about. " +
			"Give each numbered subject one short topic of one to three words, named the way its sender would name it " +
			"(an invoice, a standup, a trip, a person). Subjects that are about the same thing take the same topic word for word. " +
			"Answer with one line per subject and nothing else: \"1: Topic\"." + lang + untrusted,
		user: strings.TrimRight(b.String(), "\n"),
	}, Excerpt{Truncated: sent < len(items)}, items
}

// TopicsPayload shows what Topics would send, without sending it.
func (s *Service) TopicsPayload(ctx context.Context, items []TopicItem, language string) (Payload, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return Payload{}, err
	}
	req, e, sent := topicsRequest(items, language)
	if req.user == "" {
		return Payload{}, &Error{Kind: KindEmpty}
	}
	return payload(cfg, req, TopicChars, len(sent), e), nil
}

// Topics groups the subjects by what they are about. It sends the subjects
// and nothing else, and returns the groups in the order the mail first
// names them.
func (s *Service) Topics(ctx context.Context, items []TopicItem, language string) ([]Topic, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ready(cfg, FeatureTopics); err != nil {
		return nil, err
	}
	req, _, sent := topicsRequest(items, language)
	if req.user == "" {
		return nil, &Error{Kind: KindEmpty}
	}
	answer, err := s.cachedAnswer(ctx, cfg, req, "")
	if err != nil {
		return nil, err
	}
	return parseTopics(answer.Text, sent), nil
}

// parseTopics reads the numbering the model was asked for. A line that does
// not name a listed message is left out, and a subject with no line keeps
// no topic: a miss leaves a card where it is rather than moving it to a
// group the user cannot explain.
func parseTopics(text string, items []TopicItem) []Topic {
	var groups []Topic
	at := make(map[string]int, len(items))
	for _, line := range strings.Split(text, "\n") {
		m := topicLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
			if n > len(items) {
				break
			}
		}
		if n < 1 || n > len(items) {
			continue
		}
		name := strings.Trim(strings.TrimSpace(m[2]), "\"'`*.")
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		i, ok := at[key]
		if !ok {
			i = len(groups)
			at[key] = i
			groups = append(groups, Topic{Name: name})
		}
		groups[i].IDs = append(groups[i].IDs, items[n-1].ID)
	}
	return groups
}

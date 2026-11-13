package ai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testConversation is three messages, each reply quoting the one before,
// with a tracking link and a signature.
func testConversation() Conversation {
	day := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	return Conversation{
		Scope:   "a1",
		Subject: "Venue for the offsite",
		Messages: []Message{
			{FromName: "Lin Wei", FromEmail: "lin.wei@example.com", Date: day,
				Body: "Hi Ann, could you pick one of the two venues? FIRST-ASK\n" +
					"Options: https://venues.example.com/list?ref=TRACKME\n"},
			{FromEmail: "ann@example.com", Date: day.Add(time.Hour),
				Body: "The one by the lake has the bigger room. Let's book it. SECOND-ANSWER\n\n" +
					"On Mon, Nov 2, 2026 at 9:14 AM Lin Wei <lin.wei@example.com> wrote:\n" +
					"> Hi Ann, could you pick one of the two venues? FIRST-ASK\n"},
			{FromName: "Lin Wei", FromEmail: "lin.wei@example.com", Date: day.Add(26 * time.Hour),
				Body: "Booked the lakeside room for Thursday. THIRD-NEWS\n\n-- \nLin Wei\nOffice manager\n\n" +
					"On Mon, Nov 2, 2026 at 10:02 AM Ann <ann@example.com> wrote:\n" +
					"> The one by the lake has the bigger room. Let's book it. SECOND-ANSWER\n"},
		},
	}
}

// What the review shows of a conversation is what is sent: each message
// once, in order, under the subject said once, without the history the
// replies quote, the signature or the addresses of links.
func TestConversationPayloadIsWhatIsSent(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	c := testConversation()
	p, err := s.ConversationPayload(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if provider.count() != 0 {
		t.Fatal("inspecting the payload sent a request")
	}
	for text, want := range map[string]int{
		"Subject: Venue for the offsite": 1, "FIRST-ASK": 1, "SECOND-ANSWER": 1, "THIRD-NEWS": 1,
		"From: Lin Wei": 2, "From: ann@example.com": 1, "[link: venues.example.com]": 1,
		"TRACKME": 0, "Office manager": 0, "lin.wei@example.com": 0, "wrote:": 0, "left out": 0,
	} {
		if got := strings.Count(p.Text, text); got != want {
			t.Errorf("%q appears %d times, want %d:\n%s", text, got, want, p.Text)
		}
	}
	first, second, third := strings.Index(p.Text, "FIRST-ASK"), strings.Index(p.Text, "SECOND-ANSWER"), strings.Index(p.Text, "THIRD-NEWS")
	if first >= second || second >= third {
		t.Errorf("messages out of order:\n%s", p.Text)
	}
	if !strings.Contains(p.Text, "Date: 2026-11-03") {
		t.Errorf("the last message's date is missing:\n%s", p.Text)
	}
	if p.Messages != 3 || p.Omitted != 0 || p.Quoted != 4 || !p.Signature || p.Links != 1 || p.Truncated {
		t.Errorf("payload = %d messages, %d omitted, quoted %d, signature %v, links %d, truncated %v",
			p.Messages, p.Omitted, p.Quoted, p.Signature, p.Links, p.Truncated)
	}
	if p.Limit != ConversationLimit || p.Chars != len([]rune(p.Text)) || p.Feature != FeatureSummary {
		t.Errorf("payload size = %d of %d, feature %q", p.Chars, p.Limit, p.Feature)
	}
	if !strings.Contains(p.Instruction, "conversation") {
		t.Errorf("instruction = %q", p.Instruction)
	}

	result, err := s.SummarizeConversation(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "- A summary" || result.Cached {
		t.Errorf("result = %+v", result)
	}
	req := provider.last(t)
	if req.user() != p.Text {
		t.Errorf("sent text differs from the reviewed payload:\nsent %q\nshown %q", req.user(), p.Text)
	}
	if !strings.Contains(req.raw, "summarize an email conversation") {
		t.Errorf("instruction missing from the request: %s", req.raw)
	}
}

// A conversation longer than the limit loses its oldest messages first,
// and says how many; the newest always goes.
func TestConversationCutsTheOldestFirst(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	day := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	c := Conversation{Scope: "a1", Subject: "A long thread"}
	for i := range 12 {
		c.Messages = append(c.Messages, Message{
			FromName: fmt.Sprintf("Sender %d", i), Date: day.Add(time.Duration(i) * time.Hour),
			Body: fmt.Sprintf("MESSAGE-%02d ", i) + strings.Repeat("words and more words ", 60),
		})
	}
	p, err := s.ConversationPayload(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Chars > ConversationLimit {
		t.Errorf("payload = %d characters, limit %d", p.Chars, ConversationLimit)
	}
	if p.Omitted == 0 || p.Messages+p.Omitted != 12 {
		t.Fatalf("sent %d, left out %d of 12", p.Messages, p.Omitted)
	}
	for i := range 12 {
		if got, want := strings.Contains(p.Text, fmt.Sprintf("MESSAGE-%02d", i)), i >= p.Omitted; got != want {
			t.Errorf("message %d sent = %v, want %v", i, got, want)
		}
	}
	if note := fmt.Sprintf("The first %d messages of the conversation are left out.", p.Omitted); !strings.Contains(p.Text, note) {
		t.Errorf("the text does not say what was left out:\n%.300s", p.Text)
	}
	if _, err := s.SummarizeConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	if provider.last(t).user() != p.Text {
		t.Error("the cut text sent differs from the one reviewed")
	}

	// One long newest message still goes, cut at the body limit.
	c.Messages = append(c.Messages, Message{FromName: "Sender 12", Date: day.Add(13 * time.Hour),
		Body: "NEWEST " + strings.Repeat("a very long reply ", 900)})
	p, err = s.ConversationPayload(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "NEWEST") || !p.Truncated || p.Chars > ConversationLimit {
		t.Errorf("long newest message: truncated %v, %d characters", p.Truncated, p.Chars)
	}
}

// A conversation's summary is cached by what it sends: the same
// conversation again makes no request, one more message makes one.
func TestConversationSummaryIsCachedPerInput(t *testing.T) {
	ctx := context.Background()
	s, provider, store, _ := newTestService(t)
	c := testConversation()
	if _, err := s.SummarizeConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	again, err := s.SummarizeConversation(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cached || provider.count() != 1 {
		t.Fatalf("second summary cached %v after %d requests", again.Cached, provider.count())
	}
	for _, e := range store.cache {
		if e.Scope != "a1" || e.Feature != FeatureSummary {
			t.Errorf("cache entry = %+v", e)
		}
	}
	c.Messages = append(c.Messages, Message{FromName: "Ann", Date: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), Body: "Thanks!"})
	if result, err := s.SummarizeConversation(ctx, c); err != nil || result.Cached {
		t.Fatalf("a longer conversation = %+v, %v", result, err)
	}
	// A message of the conversation alone is another input.
	one := c.Messages[0]
	one.Subject, one.Scope = c.Subject, c.Scope
	if result, err := s.Summarize(ctx, one); err != nil || result.Cached {
		t.Fatalf("one message = %+v, %v", result, err)
	}
	if provider.count() != 3 {
		t.Errorf("requests = %d, want 3", provider.count())
	}
}

// With AI off, or summaries switched off, nothing of a conversation is
// sent; a conversation with no text of its own sends nothing either.
func TestConversationSummaryNeedsAIOn(t *testing.T) {
	ctx := context.Background()
	store, secrets, provider := newMemStore(), newMemSecrets(), &fakeProvider{}
	s := NewService(store, secrets)
	s.SetHTTPClient(&http.Client{Transport: provider})
	if _, err := s.SummarizeConversation(ctx, testConversation()); KindOf(err) != KindDisabled {
		t.Errorf("AI off: err = %v, want disabled", err)
	}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.SetFeature(FeatureSummary, false)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey(ctx, ProviderOpenAI, testKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SummarizeConversation(ctx, testConversation()); KindOf(err) != KindFeatureOff {
		t.Errorf("summaries off: err = %v, want feature off", err)
	}
	cfg.SetFeature(FeatureSummary, true)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	quotes := Conversation{Scope: "a1", Subject: "Re: x", Messages: []Message{{FromName: "Bo", Body: "> only a quote\n> of an earlier message\n"}}}
	if _, err := s.ConversationPayload(ctx, quotes); KindOf(err) != KindEmpty {
		t.Errorf("payload of quotes alone: err = %v, want empty", err)
	}
	if _, err := s.SummarizeConversation(ctx, quotes); KindOf(err) != KindEmpty {
		t.Errorf("summary of quotes alone: err = %v, want empty", err)
	}
	if n := provider.count(); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

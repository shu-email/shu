package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// system returns the instruction of a request, whichever wire format
// carried it.
func (r sent) system() string {
	if text, ok := r.body["system"].(string); ok {
		return text
	}
	messages, _ := r.body["messages"].([]any)
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] == "system" {
			text, _ := msg["content"].(string)
			return text
		}
	}
	return ""
}

// replyWith makes a fake provider answer every request with text.
func replyWith(text string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})
		return reply(http.StatusOK, string(body)), nil
	}
}

// A translation sends the excerpt of the message, and only that, and its
// answer is cached by message, language and model.
func TestTranslateSendsTheExcerptAndCachesIt(t *testing.T) {
	ctx := context.Background()
	s, provider, store, _ := newTestService(t)
	provider.respond = replyWith("Source: German\n\nDie Besprechung ist am Donnerstag.")

	first, err := s.Translate(ctx, testMessage(), "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || first.Source != "German" || first.Text != "Die Besprechung ist am Donnerstag." {
		t.Errorf("first = %+v", first)
	}
	if first.Model != "gpt-4o-mini" {
		t.Errorf("model = %q", first.Model)
	}

	req := provider.last(t)
	if system := req.system(); !strings.Contains(system, "Simplified Chinese") {
		t.Errorf("the instruction does not name the target: %q", system)
	}
	user := req.user()
	for _, want := range []string{"Subject: Design review moved to Thursday", "From: Maya Chen", "The review moved to Thursday"} {
		if !strings.Contains(user, want) {
			t.Errorf("sent text lacks %q: %q", want, user)
		}
	}
	// What the excerpt leaves out never leaves the device.
	for _, secret := range []string{"SECRET-HISTORY", "Head of Design", "utm_source"} {
		if strings.Contains(user, secret) {
			t.Errorf("sent text holds %q: %q", secret, user)
		}
	}

	for _, e := range store.cache {
		if e.Feature != FeatureTranslate || e.Scope != "a1" {
			t.Errorf("cache entry = %+v", e)
		}
	}

	// The same message into the same language is answered from the cache.
	second, err := s.Translate(ctx, testMessage(), "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached || second.Text != first.Text || second.Source != first.Source {
		t.Errorf("second = %+v", second)
	}
	if provider.count() != 1 {
		t.Fatalf("requests = %d, want 1", provider.count())
	}

	// Another language is another input, and another request.
	provider.respond = replyWith("Source: German\n\nLa réunion est jeudi.")
	if _, err := s.Translate(ctx, testMessage(), "fr"); err != nil {
		t.Fatal(err)
	}
	if provider.count() != 2 {
		t.Errorf("requests = %d, want 2", provider.count())
	}
	if system := provider.last(t).system(); !strings.Contains(system, "French") {
		t.Errorf("the instruction does not name French: %q", system)
	}
}

// A translation asks for room for the whole message, which a provider's
// own wire format bounds with max_tokens.
func TestTranslateAsksForRoom(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	provider.respond = func(*http.Request) (*http.Response, error) {
		return reply(http.StatusOK, `{"content":[{"type":"text","text":"Source: German\n\nHallo."}]}`), nil
	}
	cfg, _ := s.Config(ctx)
	cfg.UseProvider(ProviderAnthropic)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey(ctx, ProviderAnthropic, "sk-ant-KEY"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Translate(ctx, testMessage(), "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "German" || result.Text != "Hallo." {
		t.Errorf("result = %+v", result)
	}
	if got := provider.last(t).body["max_tokens"]; got != float64(translateTokens) {
		t.Errorf("max_tokens = %v, want %d", got, translateTokens)
	}
}

// With the capability off, nothing is sent, as with any other.
func TestTranslateRespectsItsSwitch(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	cfg, err := s.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetFeature(FeatureTranslate, false)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Translate(ctx, testMessage(), "zh-Hans"); KindOf(err) != KindFeatureOff {
		t.Errorf("err = %v, want feature off", err)
	}
	if err := s.Check(ctx, FeatureTranslate); KindOf(err) != KindFeatureOff {
		t.Errorf("check = %v, want feature off", err)
	}
	if provider.count() != 0 {
		t.Fatalf("%d requests were sent with the capability off", provider.count())
	}
}

// A message with nothing left after the excerpt is not sent.
func TestTranslateEmptyMessage(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	empty := Message{Subject: "Only a signature", Body: "-- \nMaya"}
	if _, err := s.Translate(ctx, empty, "zh-Hans"); KindOf(err) != KindEmpty {
		t.Errorf("err = %v, want empty", err)
	}
	if _, err := s.TranslatePayload(ctx, empty, "zh-Hans"); KindOf(err) != KindEmpty {
		t.Errorf("payload err = %v, want empty", err)
	}
	if provider.count() != 0 {
		t.Fatalf("%d requests were sent for an empty message", provider.count())
	}
}

// The payload the review shows is the one that is sent, targets included.
func TestTranslatePayload(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	p, err := s.TranslatePayload(ctx, testMessage(), "ja")
	if err != nil {
		t.Fatal(err)
	}
	if p.Feature != FeatureTranslate || p.Model != "gpt-4o-mini" || p.Limit != TranslateLimit {
		t.Errorf("payload = %+v", p)
	}
	if !strings.Contains(p.Instruction, "Japanese") {
		t.Errorf("instruction = %q", p.Instruction)
	}
	if !strings.Contains(p.Text, "Subject: Design review moved to Thursday") {
		t.Errorf("text = %q", p.Text)
	}
	// The signature and the quoted history are counted, not sent.
	if !p.Signature || p.Quoted == 0 {
		t.Errorf("left out: signature %v, quoted %d", p.Signature, p.Quoted)
	}
	if p.Chars == 0 || p.Chars > TranslateLimit {
		t.Errorf("chars = %d", p.Chars)
	}
}

// The source line is read when it is there, and the whole answer is the
// translation when it is not.
func TestSplitTranslation(t *testing.T) {
	cases := []struct {
		name, text, source, body string
	}{
		{"with a source", "Source: German\n\nHallo,\n\nbis Donnerstag.", "German", "Hallo,\n\nbis Donnerstag."},
		{"no blank line", "source: French\nBonjour.", "French", "Bonjour."},
		{"the source alone", "Source: German", "", "Source: German"},
		{"no source line", "Hallo, bis Donnerstag.", "", "Hallo, bis Donnerstag."},
		{"a line that is not a source", "Bonjour: tout le monde\n\nSalut.", "", "Bonjour: tout le monde\n\nSalut."},
	}
	for _, tc := range cases {
		source, body := splitTranslation(tc.text)
		if source != tc.source || body != tc.body {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, source, body, tc.source, tc.body)
		}
	}
}

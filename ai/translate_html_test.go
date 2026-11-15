package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// answerJSON is a model's answer as the HTML instruction asks for it.
func answerJSON(source string, segments map[int]string) string {
	type row struct {
		N    int    `json:"n"`
		Text string `json:"text"`
	}
	var rows []row
	for n := 1; n <= len(segments); n++ {
		rows = append(rows, row{N: n, Text: segments[n]})
	}
	b, _ := json.Marshal(map[string]any{"source": source, "segments": rows})
	return string(b)
}

const testHTML = `<html><head><style>p{color:red}</style><title>Ignore me</title></head><body>
<p>Hello <b>dear</b> <a href="https://example.com/report?token=TRACKME">reader</a>.</p>
<table><tr><td>Name</td><td>Ann</td></tr></table>
<script>alert("never sent")</script>
</body></html>`

// The blocks of a message are its paragraphs, list items and table cells:
// a sentence that spans inline markup stays one block, and a table keeps
// one block a cell.
func TestPlanHTMLGroupsTextByBlock(t *testing.T) {
	_, parts := planHTML(testHTML)
	var texts []string
	for _, p := range parts {
		texts = append(texts, p.text)
	}
	want := []string{
		"{0}Hello {1}dear {2}reader",
		"{0}Name",
		"{0}Ann",
	}
	if len(texts) != len(want) {
		t.Fatalf("parts = %q, want %q", texts, want)
	}
	for i, text := range texts {
		if strings.TrimSpace(text) != strings.TrimSpace(want[i]) {
			t.Errorf("part %d = %q, want %q", i, text, want[i])
		}
	}
	for _, p := range parts {
		for _, n := range p.nodes {
			if strings.Contains(n.Data, "never sent") || strings.Contains(n.Data, "Ignore me") || strings.Contains(n.Data, "color") {
				t.Errorf("a skipped part was read: %q", n.Data)
			}
		}
	}
	// The markers number the runs of a block, not the document.
	if strings.Count(parts[0].text, "{0}") != 1 {
		t.Errorf("markers restarted: %q", parts[0].text)
	}
}

// A translation of a block goes back into the runs it came from, so the
// links, the emphasis and the spaces around them survive.
func TestSpliceHTMLKeepsMarkup(t *testing.T) {
	translated := map[int]string{
		1: "{0}你好 {1}亲爱的 {2}读者",
		2: "姓名",
		3: "安",
	}
	htmlText, plain := spliceHTML(testHTML, translated)
	for _, want := range []string{
		`<a href="https://example.com/report?token=TRACKME">读者</a>`,
		"<b>亲爱的</b>",
		"<td>姓名</td>",
		"<td>安</td>",
	} {
		if !strings.Contains(htmlText, want) {
			t.Errorf("translated HTML lacks %q: %s", want, htmlText)
		}
	}
	if strings.Contains(htmlText, "reader") || strings.Contains(htmlText, ">Ann<") {
		t.Errorf("the original text survived: %s", htmlText)
	}
	if plain != "你好 亲爱的 读者\n\n姓名\n\n安" {
		t.Errorf("plain = %q", plain)
	}
}

// A model that drops or moves the markers still gives a usable answer: the
// whole block goes where it starts, and the runs after it are emptied.
func TestSpliceHTMLWithoutMarkers(t *testing.T) {
	htmlText, plain := spliceHTML(testHTML, map[int]string{1: "你好，亲爱的读者。"})
	if !strings.Contains(htmlText, "你好，亲爱的读者。") {
		t.Errorf("the translation is missing: %s", htmlText)
	}
	if strings.Contains(htmlText, "reader") || strings.Contains(htmlText, "dear") {
		t.Errorf("the original words survived: %s", htmlText)
	}
	if !strings.Contains(plain, "你好，亲爱的读者。") {
		t.Errorf("plain = %q", plain)
	}
}

// Blocks the request never sent keep their own text.
func TestSpliceHTMLKeepsUntranslatedBlocks(t *testing.T) {
	htmlText, plain := spliceHTML(testHTML, map[int]string{1: "{0}你好 {1}亲爱的 {2}读者"})
	if !strings.Contains(htmlText, "<td>Name</td>") || !strings.Contains(htmlText, "<td>Ann</td>") {
		t.Errorf("an untranslated block was lost: %s", htmlText)
	}
	if !strings.Contains(plain, "Name") || !strings.Contains(plain, "Ann") {
		t.Errorf("plain lacks the untranslated blocks: %q", plain)
	}
}

// Text marked not to be translated, and text that says nothing, never
// reaches the request.
func TestPlanHTMLSkipsWhatItShould(t *testing.T) {
	_, parts := planHTML(`<body><p translate="no">Shu Mail</p><p class="x notranslate">v0.2</p><p>Bonjour</p></body>`)
	if len(parts) != 1 || parts[0].text != "{0}Bonjour" {
		t.Fatalf("parts = %+v", parts)
	}
	// A line break ends a run, so the two lines are translated apart.
	_, parts = planHTML(`<body><p>Line one<br>Line two</p></body>`)
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
}

// A translation of HTML mail asks for the blocks, puts the answer back and
// is cached by what it sent.
func TestTranslateHTMLSendsBlocksAndKeepsMarkup(t *testing.T) {
	ctx := context.Background()
	s, provider, store, _ := newTestService(t)
	provider.respond = replyWith(answerJSON("German", map[int]string{
		1: "{0}Hallo {1}liebe {2}Leserin",
		2: "Name",
		3: "Ann",
	}))
	message := HTMLMessage{Scope: "a1", Subject: "A report", FromName: "Maya", FromEmail: "maya@example.com", HTML: testHTML}
	result, err := s.TranslateHTML(ctx, message, "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "German" {
		t.Errorf("source = %q", result.Source)
	}
	if result.Blocks != 3 || result.Total != 3 {
		t.Errorf("blocks = %d of %d", result.Blocks, result.Total)
	}
	if !strings.Contains(result.HTML, `href="https://example.com/report?token=TRACKME"`) {
		t.Errorf("the link was lost: %s", result.HTML)
	}
	if !strings.Contains(result.Text, "Hallo liebe Leserin") {
		t.Errorf("text = %q", result.Text)
	}

	req := provider.last(t)
	if !strings.Contains(req.system(), "Simplified Chinese") {
		t.Errorf("instruction = %q", req.system())
	}
	user := req.user()
	if !strings.Contains(user, "Subject: A report") {
		t.Errorf("the message is not named: %q", user)
	}
	for _, want := range []string{`"n":1`, `{0}Hello {1}dear {2}reader`, `"n":2`} {
		if !strings.Contains(user, want) {
			t.Errorf("sent text lacks %q: %q", want, user)
		}
	}
	// No attribute, address or script leaves with the blocks.
	for _, secret := range []string{"TRACKME", "alert(", "color:red", "Ignore me"} {
		if strings.Contains(user, secret) {
			t.Errorf("sent text holds %q: %q", secret, user)
		}
	}
	if got := req.body["max_tokens"]; got != nil {
		// OpenAI-compatible providers take no budget here; the Anthropic
		// path is tested below.
		t.Logf("max_tokens = %v", got)
	}
	for _, e := range store.cache {
		if e.Feature != FeatureTranslate || e.Scope != "a1" {
			t.Errorf("cache entry = %+v", e)
		}
	}

	// The same message, in the same language, is answered from the cache.
	second, err := s.TranslateHTML(ctx, message, "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached || second.HTML != result.HTML || provider.count() != 1 {
		t.Errorf("second = cached %v, requests %d", second.Cached, provider.count())
	}
}

// A message longer than the limit is translated as far as it goes, with
// the rest left as it was and said to be left out.
func TestTranslateHTMLStopsAtTheLimit(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	var blocks strings.Builder
	blocks.WriteString("<body>")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&blocks, "<p>Paragraph %d says something long enough to matter, with words in it.</p>", i)
	}
	blocks.WriteString("</body>")
	provider.respond = func(*http.Request) (*http.Response, error) {
		return reply(http.StatusOK, `{"choices":[{"message":{"content":"not json"}}]}`), nil
	}
	p, err := s.TranslateHTMLPayload(ctx, HTMLMessage{Subject: "Long", HTML: blocks.String()}, "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated {
		t.Error("the payload does not say the message was cut")
	}
	if p.Messages == 0 || p.Chars > TranslateLimit {
		t.Errorf("payload = %d messages, %d chars", p.Messages, p.Chars)
	}
	// An answer that is not the object asked for is refused, not spliced.
	if _, err := s.TranslateHTML(ctx, HTMLMessage{HTML: testHTML}, "zh-Hans"); KindOf(err) != KindBadResponse {
		t.Errorf("err = %v, want bad response", err)
	}
}

// A translation of HTML mail asks a provider's own wire format for room
// for the whole answer.
func TestTranslateHTMLAsksForRoom(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	provider.respond = func(*http.Request) (*http.Response, error) {
		answer := answerJSON("English", map[int]string{1: "{0}Hello {1}dear {2}reader", 2: "Name", 3: "Ann"})
		raw, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": answer}}})
		return reply(http.StatusOK, string(raw)), nil
	}
	cfg, _ := s.Config(ctx)
	cfg.UseProvider(ProviderAnthropic)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey(ctx, ProviderAnthropic, "sk-ant-KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TranslateHTML(ctx, HTMLMessage{HTML: testHTML}, "ja"); err != nil {
		t.Fatal(err)
	}
	if got := provider.last(t).body["max_tokens"]; got != float64(htmlTranslateTokens) {
		t.Errorf("max_tokens = %v, want %d", got, htmlTranslateTokens)
	}
}

// A message whose HTML holds no text is not sent.
func TestTranslateHTMLEmpty(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	if _, err := s.TranslateHTML(ctx, HTMLMessage{HTML: `<p><img src="cid:1"></p>`}, "zh-Hans"); KindOf(err) != KindEmpty {
		t.Errorf("err = %v, want empty", err)
	}
	if provider.count() != 0 {
		t.Fatalf("%d requests were sent for an empty message", provider.count())
	}
}

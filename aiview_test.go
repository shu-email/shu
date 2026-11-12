package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
)

// aiStore is the settings store and cache of the UI tests' AI service.
type aiStore struct {
	settings map[string]string
	cache    map[string]ai.CacheEntry
}

func (s *aiStore) Setting(ctx context.Context, key string) (string, bool, error) {
	v, ok := s.settings[key]
	return v, ok, nil
}

func (s *aiStore) SetSetting(ctx context.Context, key, value string) error {
	s.settings[key] = value
	return nil
}

func (s *aiStore) AICached(ctx context.Context, key string) (ai.CacheEntry, bool, error) {
	e, ok := s.cache[key]
	return e, ok, nil
}

func (s *aiStore) SaveAICache(ctx context.Context, e ai.CacheEntry) error {
	s.cache[e.Key] = e
	return nil
}

// aiSecrets is the UI tests' keychain.
type aiSecrets map[string][]byte

func (s aiSecrets) Set(key string, secret []byte) error {
	s[key] = append([]byte(nil), secret...)
	return nil
}

func (s aiSecrets) Get(key string) ([]byte, error) {
	v, ok := s[key]
	if !ok {
		return nil, ai.ErrSecretNotFound
	}
	return v, nil
}

func (s aiSecrets) Delete(key string) error {
	delete(s, key)
	return nil
}

// aiProvider stands in for a model provider. It opens no socket: the UI
// tests run the real AI service against it, so a count of zero here means
// nothing left the app.
type aiProvider struct {
	texts  []string
	status int
	// during runs while a request is in flight, as a click on Cancel
	// would.
	during func()
}

const (
	testSummary       = "- The review moved to Thursday at 10."
	testThreadSummary = "- The lakeside venue is booked for Thursday."
	testDraft         = "Thursday at 10 works for me."
)

func (p *aiProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	system, user := "", ""
	for _, m := range body.Messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			user = m.Content
		}
	}
	p.texts = append(p.texts, user)
	if p.during != nil {
		p.during()
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
	}
	if p.status != 0 {
		return aiReply(p.status, `{"error":{"message":"`+user+`"}}`), nil
	}
	answer := "pong"
	switch {
	case strings.Contains(system, "triage an inbox"):
		var rows []map[string]any
		for i, line := range strings.Split(user, "\n") {
			group, reason := "can_wait", "Worth a read later"
			switch {
			case strings.Contains(line, "GitHub"):
				group, reason = "can_archive", "Automated notification"
			case strings.Contains(line, "Design review"):
				group, reason = "needs_you", "Asks you to confirm the time"
			}
			rows = append(rows, map[string]any{"n": i + 1, "group": group, "reason": reason})
		}
		b, _ := json.Marshal(rows)
		answer = string(b)
	case strings.Contains(system, "draft a reply"):
		answer = testDraft
	case strings.Contains(system, "summarize one email"):
		answer = testSummary
	case strings.Contains(system, "summarize an email conversation"):
		answer = testThreadSummary
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	return aiReply(http.StatusOK, string(b)), nil
}

func aiReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

const testAIKey = "sk-ui-test-KEY"

// aiApp is the sample app with the real AI service on a fake provider.
// With on, AI is enabled and a key is stored.
func aiApp(t *testing.T, on bool) (*app, *fakeService, *aiProvider, aiSecrets) {
	t.Helper()
	return aiAppOf(t, on, sampleApp)
}

// aiMessageApp is aiApp with a row for each message, the view the tests
// written before conversations are about.
func aiMessageApp(t *testing.T, on bool) (*app, *fakeService, *aiProvider, aiSecrets) {
	t.Helper()
	return aiAppOf(t, on, sampleMessageApp)
}

func aiAppOf(t *testing.T, on bool, sample func(*testing.T) (*app, *fakeService)) (*app, *fakeService, *aiProvider, aiSecrets) {
	t.Helper()
	a, fs := sample(t)
	provider, secrets := &aiProvider{}, aiSecrets{}
	svc := ai.NewService(&aiStore{settings: map[string]string{}, cache: map[string]ai.CacheEntry{}}, secrets)
	svc.SetHTTPClient(&http.Client{Transport: provider})
	a.ai = svc
	if on {
		cfg := ai.DefaultConfig()
		cfg.Enabled = true
		if err := svc.SaveConfig(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if err := svc.SetKey(ai.ProviderOpenAI, testAIKey); err != nil {
			t.Fatal(err)
		}
	}
	return a, fs, provider, secrets
}

func click(t *testing.T, tt *ui.Tester, target string) {
	t.Helper()
	if err := tt.Click(target); err != nil {
		t.Fatalf("click %q: %v\ntexts: %q", target, err, tt.Texts())
	}
}

// With AI off, the button explains how to turn it on and nothing is sent.
func TestAIOffExplainsAndSendsNothing(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, false)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.Summarize())
	if !tt.HasText(locales.AiOff()) {
		t.Fatalf("no explanation: %q", tt.Texts())
	}
	click(t, tt, locales.TriageInbox())
	if len(provider.texts) != 0 {
		t.Fatalf("requests were sent with AI off: %q", provider.texts)
	}
	if a.review != nil {
		t.Error("a payload was offered for sending with AI off")
	}
	click(t, tt, locales.AiOpenSettings())
	if !a.showSettings || a.settingsTab != settingsTabAI {
		t.Fatalf("settings = %v, tab %d", a.showSettings, a.settingsTab)
	}
	if !tt.HasText(locales.AiTitle()) || !tt.HasText(locales.AiIntro()) {
		t.Errorf("AI settings missing: %q", tt.Texts())
	}

	// On, but without a key: still nothing is sent, and the reason is said.
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.showSettings = false
	tt.Frame()
	click(t, tt, locales.Summarize())
	if !tt.HasText(locales.AiNoKey()) {
		t.Errorf("no-key explanation missing: %q", tt.Texts())
	}
	if len(provider.texts) != 0 {
		t.Fatalf("requests were sent without a key: %q", provider.texts)
	}
}

// The first summary shows exactly what will be sent and waits; later ones
// run at once, come from the cache, and name the model.
func TestSummaryReviewThenCache(t *testing.T) {
	a, fs, provider, _ := aiMessageApp(t, true)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.Summarize())
	if !tt.HasText(locales.AiReviewTitle()) {
		t.Fatalf("no review before the first use: %q", tt.Texts())
	}
	if len(provider.texts) != 0 {
		t.Fatal("the request went out before the user approved it")
	}
	if !tt.HasText(locales.AiReviewTarget("OpenAI", "gpt-4o-mini", "api.openai.com")) {
		t.Errorf("review does not say where the text goes: %q", tt.Texts())
	}
	shown := a.review.payload.Text
	if !strings.Contains(shown, "Body of Design review") || !strings.Contains(shown, "From: Maya Chen") {
		t.Errorf("payload = %q", shown)
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 || provider.texts[0] != shown {
		t.Fatalf("sent %q, shown %q", provider.texts, shown)
	}
	if !tt.HasText(testSummary) || !tt.HasText("gpt-4o-mini") {
		t.Fatalf("summary or its model missing: %q", tt.Texts())
	}
	if tt.HasText(locales.AiReviewTitle()) {
		t.Error("the review stays open after sending")
	}

	// Copy puts the summary on the clipboard.
	click(t, tt, locales.AiCopy())
	if got := tt.Clipboard(); got != testSummary {
		t.Errorf("clipboard = %q", got)
	}
	// What was sent stays inspectable, without a Send button.
	click(t, tt, locales.AiWhatWasSent())
	if !tt.HasText(locales.AiReviewTitle()) || tt.HasText(locales.AiReviewSend("gpt-4o-mini")) {
		t.Errorf("inspection view: %q", tt.Texts())
	}
	click(t, tt, locales.Close())

	// Summarizing again asks nothing and sends nothing: it is cached.
	click(t, tt, locales.AiDismissSummary())
	if tt.HasText(testSummary) {
		t.Fatal("dismiss left the summary")
	}
	click(t, tt, locales.Summarize())
	if a.review != nil {
		t.Error("the review came back for an approved capability")
	}
	if len(provider.texts) != 1 {
		t.Fatalf("a cached summary made a request: %d", len(provider.texts))
	}
	if !tt.HasText(testSummary) || !tt.HasText(locales.AiCachedNote()) {
		t.Errorf("cached summary missing: %q", tt.Texts())
	}
	// A summary changes nothing in the mailbox.
	if len(fs.moved) != 0 || len(fs.sent) != 0 {
		t.Errorf("mailbox changed: moved %v sent %v", fs.moved, fs.sent)
	}
}

// A provider failure is explained in the user's words, without the mail,
// and can be retried.
func TestSummaryFailureAndRetry(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, true)
	if err := a.ai.MarkReviewed(context.Background(), ai.FeatureSummary); err != nil {
		t.Fatal(err)
	}
	provider.status = http.StatusTooManyRequests
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.Summarize())
	if !tt.HasText(locales.AiErrRateLimit()) {
		t.Fatalf("rate limit not explained: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, testAIKey) || strings.Contains(text, "From: Maya Chen") {
			t.Errorf("the failure shows mail or the key: %q", text)
		}
	}
	provider.status = 0
	click(t, tt, locales.Retry())
	if !tt.HasText(testSummary) {
		t.Errorf("retry did not summarize: %q", tt.Texts())
	}
}

// Cancelling a summary in flight stops it and shows nothing.
func TestSummaryCancel(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, true)
	if err := a.ai.MarkReviewed(context.Background(), ai.FeatureSummary); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	key := a.selectedKey()
	wasBusy := false
	provider.during = func() {
		job := a.summaries[key]
		wasBusy = job != nil && job.busy
		if job != nil {
			job.cancel()
		}
	}
	click(t, tt, locales.Summarize())
	if !wasBusy {
		t.Error("the summary did not show as running while in flight")
	}
	if a.summaries[key] != nil {
		t.Errorf("a cancelled summary left %+v", a.summaries[key])
	}
	if tt.HasText(testSummary) || tt.HasText(locales.AiErrOther()) {
		t.Errorf("a cancelled summary shows: %q", tt.Texts())
	}
}

// Triage suggests groups with reasons, and moves nothing: the user acts.
func TestTriageSuggestsOnly(t *testing.T) {
	a, fs, provider, _ := aiMessageApp(t, true)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.TriageInbox())
	if !tt.HasText(locales.AiReviewTitle()) || len(provider.texts) != 0 {
		t.Fatalf("triage did not wait for approval: %q", tt.Texts())
	}
	if !tt.HasText(locales.AiReviewTriageSize(3, a.review.payload.Chars)) {
		t.Errorf("triage payload size missing: %q", tt.Texts())
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 {
		t.Fatalf("requests = %d", len(provider.texts))
	}
	if strings.Contains(provider.texts[0], "INBOX/") {
		t.Errorf("triage sent internal message keys: %q", provider.texts[0])
	}
	for _, text := range []string{
		locales.AiSuggestions(), locales.AiTriageCounts(1, 1, 1),
		locales.AiNeedsYou(), "Asks you to confirm the time",
		locales.AiCanArchive(), "Automated notification", locales.AiCanWait(),
	} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if len(fs.moved) != 0 || len(fs.flagCalls) != 0 {
		t.Fatalf("triage changed the mailbox: moved %v flags %v", fs.moved, fs.flagCalls)
	}
	// Selecting a group only selects.
	click(t, tt, locales.AiSelectArchive())
	if a.sel.Len() != 1 || !a.sel.Has("INBOX/2") {
		t.Fatalf("selection = %d", a.sel.Len())
	}
	if len(fs.moved) != 0 {
		t.Fatal("selecting the suggestions moved mail")
	}
	// Archiving is the user's own click.
	click(t, tt, locales.ArchiveMessage())
	if len(fs.moved) != 1 || fs.moved[0] != "INBOX->Archive" {
		t.Errorf("moves = %v", fs.moved)
	}
	click(t, tt, locales.AiDismissSuggestions())
	if a.triage != nil || tt.HasText(locales.AiSuggestions()) {
		t.Error("the suggestions stay after dismissing them")
	}
}

// A draft lands in the compose window above the quote, editable, and is
// never sent on its own.
func TestDraftReplyIsOnlyADraft(t *testing.T) {
	a, fs, provider, _ := aiApp(t, true)
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, "Design review — Thursday at 10")
	click(t, main, locales.Reply())
	if len(a.composes) != 1 {
		t.Fatalf("composes = %d", len(a.composes))
	}
	cs := a.composes[0]
	if cs.source == nil {
		t.Fatal("the reply has no source message for a draft")
	}
	quoted := cs.body
	tt := ui.NewTester(cs.view, 720, 620)
	click(t, tt, locales.AiGuidance())
	tt.Type("accept the new time")
	click(t, tt, locales.AiDraft())
	if !tt.HasText(locales.AiReviewTitle()) || len(provider.texts) != 0 {
		t.Fatalf("the draft did not wait for approval: %q", tt.Texts())
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 || !strings.Contains(provider.texts[0], "accept the new time") {
		t.Fatalf("sent = %q", provider.texts)
	}
	if !strings.HasPrefix(cs.body, testDraft+"\n\n") || !strings.Contains(cs.body, strings.TrimLeft(quoted, "\n")) {
		t.Errorf("body = %q", cs.body)
	}
	if !tt.HasText(locales.AiDrafted("gpt-4o-mini")) {
		t.Errorf("draft note missing: %q", tt.Texts())
	}
	if len(fs.sent) != 0 || cs.sending || cs.sent {
		t.Fatalf("the draft was sent: %+v", fs.sent)
	}
	// The recipients are the user's, untouched by the model.
	if !strings.Contains(cs.to, "maya@example.com") {
		t.Errorf("to = %q", cs.to)
	}
	// A new message has no email to answer, so no drafting row.
	a.openCompose(nil)
	blank := ui.NewTester(a.composes[1].view, 720, 620)
	if blank.HasText(locales.AiDraft()) {
		t.Error("a new message offers to draft a reply")
	}
}

// The AI settings store the key in the keychain, never show it, and test
// the connection without mail.
func TestAISettings(t *testing.T) {
	a, _, provider, secrets := aiApp(t, false)
	a.openSettings(settingsTabAI)
	tt := ui.NewTester(a.view, 1220, 900)
	if a.aiForm.cfg.Enabled {
		t.Fatal("AI is on in a new install")
	}
	click(t, tt, locales.AiEnable())
	cfg, _ := a.ai.Config(context.Background())
	if !cfg.Enabled {
		t.Fatal("the switch did not save")
	}
	if !tt.HasText(locales.AiKeyMissing()) {
		t.Errorf("missing-key note absent: %q", tt.Texts())
	}

	click(t, tt, locales.AiKey())
	tt.Type(testAIKey)
	click(t, tt, locales.AiKeySave())
	if got := string(secrets[ai.KeyName(ai.ProviderOpenAI)]); got != testAIKey {
		t.Fatalf("keychain = %q", got)
	}
	if a.aiForm.key != "" {
		t.Error("the typed key stays in the window's state")
	}
	if !tt.HasText(locales.AiKeyStored()) {
		t.Errorf("stored note missing: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, testAIKey) {
			t.Errorf("the key is shown: %q", text)
		}
	}

	click(t, tt, locales.AiTest())
	if len(provider.texts) != 1 || provider.texts[0] != "ping" {
		t.Fatalf("test sent %q", provider.texts)
	}
	if !tt.HasText(locales.AiTestOK("gpt-4o-mini")) {
		t.Errorf("test result missing: %q", tt.Texts())
	}
	provider.status = http.StatusUnauthorized
	click(t, tt, locales.AiTest())
	if !tt.HasText(locales.AiErrInvalidKey()) {
		t.Errorf("invalid key not explained: %q", tt.Texts())
	}
	provider.status = 0

	// Each capability has a switch of its own.
	click(t, tt, locales.AiTriage())
	cfg, _ = a.ai.Config(context.Background())
	if cfg.FeatureOn(ai.FeatureTriage) || !cfg.FeatureOn(ai.FeatureSummary) {
		t.Errorf("capability switches = %v", cfg.Off)
	}

	click(t, tt, locales.AiKeyDelete())
	if len(secrets) != 0 {
		t.Errorf("keychain after delete = %v", secrets)
	}
	if !tt.HasText(locales.AiKeyMissing()) {
		t.Errorf("deleted key still reported: %q", tt.Texts())
	}

	// A remote server over plain HTTP shows the warning and its opt-in.
	a.aiForm.provider = locales.AiProviderCustom()
	a.chooseAIProvider()
	a.aiForm.cfg.BaseURL = "http://models.example.com/v1"
	tt.Frame()
	if !tt.HasText(locales.AiHTTPWarning()) {
		t.Fatalf("no plain HTTP warning: %q", tt.Texts())
	}
	click(t, tt, locales.AiAllowHTTP())
	if !a.aiForm.cfg.AllowHTTP {
		t.Error("the opt-in switch did not turn on")
	}
	// A local server needs neither the warning nor a key.
	a.aiForm.provider = locales.AiProviderLocal()
	a.chooseAIProvider()
	tt.Frame()
	if tt.HasText(locales.AiHTTPWarning()) || !tt.HasText(locales.AiKeyOptional()) {
		t.Errorf("local provider view: %q", tt.Texts())
	}
	cfg, _ = a.ai.Config(context.Background())
	if cfg.Provider != ai.ProviderLocal || cfg.AllowHTTP {
		t.Errorf("saved config = %+v", cfg)
	}
}

// At the window's minimum size, in both appearances, the AI controls are
// on screen rather than in the toolbar's overflow menu.
func TestAIControlsAtMinimumSize(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, _, provider, _ := aiMessageApp(t, true)
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		click(t, tt, locales.Summarize())
		click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
		if !tt.HasText(testSummary) {
			t.Errorf("dark=%v: no summary at the minimum size: %q", dark, tt.Texts())
		}
		click(t, tt, locales.TriageInbox())
		click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
		if !tt.HasText(locales.AiSuggestions()) || len(provider.texts) != 2 {
			t.Errorf("dark=%v: no triage at the minimum size: %q", dark, tt.Texts())
		}
	}
}

// The AI strings exist in Simplified Chinese too.
func TestAIStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"aiOff":         zh.AiOff(),
		"aiReviewTitle": zh.AiReviewTitle(),
		"summarize":     zh.Summarize(),
		"aiErrTimeout":  zh.AiErrTimeout(),
	} {
		if text == "" || text == name || !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.AiReviewQuoted(3); !strings.Contains(got, "3") {
		t.Errorf("plural = %q", got)
	}
}

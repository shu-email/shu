package app

import (
	"context"
	"encoding/json"
	"fmt"
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
	texts []string
	// systems is the instruction of each request, in order, so a test can
	// answer one kind of request differently from another.
	systems []string
	// respond, when set, answers instead of the text below.
	respond func(*http.Request) (*http.Response, error)
	status  int
	// during runs while a request is in flight, as a click on Cancel
	// would.
	during func()
}

const (
	testSummary       = "- The review moved to Thursday at 10."
	testThreadSummary = "- The lakeside venue is booked for Thursday."
	testDraft         = "Thursday at 10 works for me."
	testTranslation   = "Source: German\n\nDie Besprechung ist am Donnerstag."
	testTranslated    = "Die Besprechung ist am Donnerstag."
	// An HTML translation answers with the blocks it was given, each
	// keeping the markers that place it.
	testHTMLTranslation = `{"source":"German","segments":[{"n":1,"text":"{0}Bericht über {1}Design-Review"}]}`
	testHTMLTranslated  = "Bericht über Design-Review"
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
	p.systems = append(p.systems, system)
	if p.during != nil {
		p.during()
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
	}
	if p.respond != nil {
		return p.respond(r)
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
	case strings.Contains(system, "sort a mailbox into topics"):
		var lines []string
		for i, line := range strings.Split(user, "\n") {
			topic := "Other"
			switch {
			case strings.Contains(line, "Design review"), strings.Contains(line, "Contract"):
				topic = "Design"
			case strings.Contains(line, "Pull request"):
				topic = "Admin"
			}
			lines = append(lines, fmt.Sprintf("%d: %s", i+1, topic))
		}
		answer = strings.Join(lines, "\n")
	case strings.Contains(system, "draft a reply"):
		answer = testDraft
	case strings.Contains(system, "summarize one email"):
		answer = testSummary
	case strings.Contains(system, "summarize an email conversation"):
		answer = testThreadSummary
	case strings.Contains(system, "the blocks of one email's HTML"):
		answer = testHTMLTranslation
	case strings.Contains(system, "You translate one email"):
		answer = testTranslation
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
		if err := svc.SetKey(context.Background(), ai.ProviderOpenAI, testAIKey); err != nil {
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

// clickMore opens the sidebar's menu and chooses one of the app's screens
// from it, as the user reaches contacts, rules and settings.
func clickMore(t *testing.T, tt *ui.Tester, item string) {
	t.Helper()
	click(t, tt, locales.More())
	click(t, tt, item)
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
	if a.sel.Len() != 1 || !a.sel.Has("a1/INBOX/2") {
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
	conn := addAIConnection(t, a, ai.ProviderOpenAI, "", "")
	a.openProviders()
	tt.Frame()
	if !tt.HasText(locales.AiKeyMissing()) {
		t.Errorf("missing-key note absent: %q", tt.Texts())
	}
	click(t, tt, locales.AiKey())
	tt.Type(testAIKey)
	click(t, tt, locales.AiKeySave())
	if got := string(secrets[ai.KeyName(ai.ConnectionKeyID(conn))]); got != testAIKey {
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
	a.providers.open = false
	tt.Frame()

	// Each capability has a switch of its own.
	click(t, tt, locales.AiTriage())
	cfg, _ = a.ai.Config(context.Background())
	if cfg.FeatureOn(ai.FeatureTriage) || !cfg.FeatureOn(ai.FeatureSummary) {
		t.Errorf("capability switches = %v", cfg.Off)
	}

	a.openProviders()
	tt.Frame()
	click(t, tt, locales.AiKeyDelete())
	a.loadProviderStates()
	tt.Frame()
	if len(secrets) != 0 {
		t.Errorf("keychain after delete = %v", secrets)
	}
	if !tt.HasText(locales.AiKeyMissing()) {
		t.Errorf("deleted key still reported: %q", tt.Texts())
	}

	// A remote server over plain HTTP shows the warning and its opt-in.
	addAIConnection(t, a, ai.ProviderCustom, "http://models.example.com/v1", "")
	a.openProviders()
	tt.Frame()
	tt.Frame()
	if !tt.HasText(locales.AiAllowHTTP()) {
		t.Fatalf("no plain HTTP opt-in: %q", tt.Texts())
	}
	click(t, tt, locales.AiAllowHTTP())
	if !a.aiForm.cfg.AllowHTTP {
		t.Error("the opt-in switch did not turn on")
	}
	// A local server needs neither the warning nor a key.
	addAIConnection(t, a, ai.ProviderLocal, "", "")
	a.openProviders()
	tt.Frame()
	if tt.HasText(locales.AiAllowHTTP()) || !tt.HasText(locales.AiKeyOptional()) {
		t.Errorf("local provider view: %q", tt.Texts())
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
		// A translation replaces the body under the summary's card, and
		// the pane holds both.
		click(t, tt, locales.TranslateMessage())
		click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
		if !tt.HasText(testHTMLTranslated) || !tt.HasText(locales.ShowOriginal()) || len(provider.texts) != 3 {
			t.Errorf("dark=%v: no translation at the minimum size: %q", dark, tt.Texts())
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
		"translate":     zh.TranslateMessage(),
		"aiTranslate":   zh.AiTranslate(),
		"showOriginal":  zh.ShowOriginal(),
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

// addAIConnection adds an API connection as the add overlay does and makes
// it the active one.
func addAIConnection(t *testing.T, a *app, preset ai.ProviderID, baseURL, key string) ai.AIConnection {
	t.Helper()
	a.startAddConnection()
	for i, p := range apiPresets() {
		if p.ID == preset {
			a.providers.form.preset = i
		}
	}
	a.providers.form.baseURL = baseURL
	a.providers.form.key = key
	a.saveConnection()
	if conn, ok := a.aiForm.cfg.ActiveConnection(); ok {
		return conn
	}
	t.Fatal("the connection was not saved")
	return ai.AIConnection{}
}

// A provider the user switched off, and a key saved for another server,
// are said as what to do, not as a bare failure.
func TestAIErrorTextNamesTheProviderAndTheKey(t *testing.T) {
	for _, tc := range []struct {
		kind ai.Kind
		want string
	}{
		{ai.KindProviderOff, locales.AiErrProviderOff()},
		{ai.KindKeyEndpoint, locales.AiErrKeyEndpoint()},
	} {
		text, setup := aiErrorText(&ai.Error{Kind: tc.kind})
		if text != tc.want || !setup {
			t.Errorf("kind %v = %q, setup %v; want %q and a setup step", tc.kind, text, setup, tc.want)
		}
	}
}

// A local tool's failures say what to do with the tool rather than that
// the request failed, and the ones the user fixes in the settings lead
// there.
func TestAIErrorTextExplainsTheLocalTool(t *testing.T) {
	for _, tc := range []struct {
		kind  ai.Kind
		want  string
		setup bool
	}{
		{ai.KindCLINotInstalled, locales.AiErrToolNotInstalled(), true},
		{ai.KindCLIBadPath, locales.AiErrToolBadPath(), true},
		{ai.KindCLISignIn, locales.AiErrToolSignIn(), true},
		{ai.KindCLIOutdated, locales.AiErrToolOutdated(), true},
		{ai.KindCLIFailed, locales.AiErrToolFailed(), false},
		{ai.KindCLIOutput, locales.AiErrToolOutput(), false},
	} {
		text, setup := aiErrorText(&ai.Error{Kind: tc.kind})
		if text != tc.want || setup != tc.setup {
			t.Errorf("kind %v = %q, setup %v; want %q and setup %v", tc.kind, text, setup, tc.want, tc.setup)
		}
		if text == locales.AiErrOther() {
			t.Errorf("kind %v falls through to the generic failure", tc.kind)
		}
	}
	// Each kind says something of its own: a catalog entry copied from
	// another kind would otherwise pass.
	seen := map[string]ai.Kind{}
	for _, kind := range []ai.Kind{ai.KindCLINotInstalled, ai.KindCLIBadPath, ai.KindCLISignIn, ai.KindCLIOutdated, ai.KindCLIFailed, ai.KindCLIOutput} {
		text, _ := aiErrorText(&ai.Error{Kind: kind})
		if other, ok := seen[text]; ok {
			t.Errorf("kind %v says what kind %v says: %q", kind, other, text)
		}
		seen[text] = kind
	}
}

// A sign-in failure keeps a retry even though the settings also fix it:
// the check that read the tool may have been the one that failed.
func TestASignInFailureOffersARetry(t *testing.T) {
	if j := aiFailed(&ai.Error{Kind: ai.KindCLISignIn}); !j.setup || !j.canRetry {
		t.Errorf("sign-in = %+v, want a setup step and a retry", j)
	}
	if j := aiFailed(&ai.Error{Kind: ai.KindCLINotInstalled}); !j.setup || j.canRetry {
		t.Errorf("not installed = %+v, want a setup step alone", j)
	}
}

// reviewed approves a capability, as a first use does, so a test about
// something else needs no review.
func reviewed(t *testing.T, a *app, f ai.Feature) {
	t.Helper()
	if err := a.ai.MarkReviewed(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

// The first translation shows what will be sent and waits; its text then
// takes the place of the message, the original stays a click away, and a
// second translation comes from the cache.
func TestTranslationReviewThenCache(t *testing.T) {
	a, fs, provider, _ := aiMessageApp(t, true)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	const body = "Body of Design review — Thursday at 10"
	if !tt.HasText(body) {
		t.Fatalf("body missing: %q", tt.Texts())
	}
	click(t, tt, locales.TranslateMessage())
	if !tt.HasText(locales.AiReviewTitle()) {
		t.Fatalf("no review before the first translation: %q", tt.Texts())
	}
	if len(provider.texts) != 0 {
		t.Fatal("the request went out before the user approved it")
	}
	// The instruction names the language the app shows in English, and
	// the text sent is the blocks of the message, not its markup.
	if !strings.Contains(a.review.payload.Instruction, "English") {
		t.Errorf("instruction = %q", a.review.payload.Instruction)
	}
	if !strings.Contains(a.review.payload.Text, "From: Maya Chen") || !strings.Contains(a.review.payload.Text, `"n":1`) {
		t.Errorf("text = %q", a.review.payload.Text)
	}
	if strings.Contains(a.review.payload.Text, "<script") || strings.Contains(a.review.payload.Text, "<b>") {
		t.Errorf("markup was sent: %q", a.review.payload.Text)
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 {
		t.Fatalf("requests = %d", len(provider.texts))
	}
	if !tt.HasText(testHTMLTranslated) {
		t.Fatalf("the translation is not shown: %q", tt.Texts())
	}
	if tt.HasText(body) {
		t.Error("the original is still drawn under the translation")
	}
	if !tt.HasText(locales.TranslatedFrom("German")) || !tt.HasText("gpt-4o-mini") {
		t.Errorf("the translation does not say what it is: %q", tt.Texts())
	}

	// The original comes back, and the translation again.
	click(t, tt, locales.ShowOriginal())
	if !tt.HasText(body) || tt.HasText(testHTMLTranslated) {
		t.Errorf("showing the original: %q", tt.Texts())
	}
	click(t, tt, locales.ShowTranslation())
	if !tt.HasText(testHTMLTranslated) || tt.HasText(body) {
		t.Errorf("showing the translation again: %q", tt.Texts())
	}

	// The translation can be copied, and what was sent inspected.
	click(t, tt, locales.AiCopy())
	if got := tt.Clipboard(); got != testHTMLTranslated {
		t.Errorf("clipboard = %q", got)
	}
	click(t, tt, locales.AiWhatWasSent())
	if !tt.HasText(locales.AiReviewTitle()) || tt.HasText(locales.AiReviewSend("gpt-4o-mini")) {
		t.Errorf("inspection view: %q", tt.Texts())
	}
	click(t, tt, locales.Close())

	// Dismissing puts the message back, and asking again sends nothing.
	click(t, tt, locales.AiDismissTranslation())
	if !tt.HasText(body) || tt.HasText(testHTMLTranslated) {
		t.Errorf("after dismissing: %q", tt.Texts())
	}
	click(t, tt, locales.TranslateMessage())
	if a.review != nil {
		t.Error("the review came back for an approved capability")
	}
	if len(provider.texts) != 1 {
		t.Fatalf("a cached translation made a request: %d", len(provider.texts))
	}
	if !tt.HasText(testHTMLTranslated) || !tt.HasText(locales.AiCachedNote()) {
		t.Errorf("cached translation missing: %q", tt.Texts())
	}
	// Translating changes nothing in the mailbox.
	if len(fs.moved) != 0 || len(fs.sent) != 0 || len(fs.flagCalls) != 0 {
		t.Errorf("mailbox changed: moved %v sent %v flags %v", fs.moved, fs.sent, fs.flagCalls)
	}
}

// A failure is explained without the mail, and retried.
func TestTranslationFailureAndRetry(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	provider.status = http.StatusTooManyRequests
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.TranslateMessage())
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
	if !tt.HasText(testHTMLTranslated) {
		t.Errorf("retry did not translate: %q", tt.Texts())
	}
}

// HTML mail shows the translation as plain text; the HTML view is the
// message as it was sent, so choosing it takes the pane back.
func TestTranslationAndHTML(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.TranslateMessage())
	if !tt.HasText(testHTMLTranslated) {
		t.Fatalf("translation missing: %q", tt.Texts())
	}
	if a.htmlOn {
		t.Error("the HTML view stayed on under a plain-text translation")
	}
	click(t, tt, locales.ShowHTML())
	if !tt.HasText("Body of Design review — Thursday at 10") {
		t.Errorf("the HTML view did not show the message: %q", tt.Texts())
	}
	if a.translations[summaryKey(a.all[0])] != nil && !a.translations[summaryKey(a.all[0])].original {
		t.Error("the translation is still what the pane draws")
	}
}

// A message of a conversation is translated on its own, from its own
// actions.
func TestTranslateAMessageOfAConversation(t *testing.T) {
	a, _, provider, _ := aiApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.ForMessage(locales.MoreActions(), 1, 1))
	click(t, tt, locales.TranslateThisMessage())
	if len(provider.texts) != 1 {
		t.Fatalf("requests = %d", len(provider.texts))
	}
	if !tt.HasText(testHTMLTranslated) || !tt.HasText(locales.TranslatedFrom("German")) {
		t.Fatalf("the translation is not shown: %q", tt.Texts())
	}
	if tt.HasText("Body of Design review — Thursday at 10") {
		t.Error("the original is still drawn under the translation")
	}
	// The conversation's own summary is untouched: no request was made
	// for one.
	if a.summaries[threadKey(a.conv.account, a.conv.thread)] != nil {
		t.Error("translating a message wrote a summary")
	}
}

// The translation switch and the language it writes are saved with the
// rest of the AI settings.
func TestTranslationSettings(t *testing.T) {
	a, _, _, _ := aiApp(t, false)
	a.openSettings(settingsTabAI)
	tt := ui.NewTester(a.view, 1220, 900)
	click(t, tt, locales.AiEnable())
	if !tt.HasText(locales.AiTranslate()) || !tt.HasText(locales.AiTranslateTo()) {
		t.Fatalf("translation settings missing: %q", tt.Texts())
	}
	// The language choice starts as the app's own.
	if !tt.HasText(locales.AiTranslateFollowApp()) {
		t.Errorf("the default target is not the app's language: %q", tt.Texts())
	}
	click(t, tt, locales.AiTranslateFollowApp())
	click(t, tt, "简体中文")
	cfg, err := a.ai.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TranslateTo != "zh-Hans" {
		t.Errorf("saved target = %q", cfg.TranslateTo)
	}
	if cfg.TranslateTarget("en") != "zh-Hans" {
		t.Errorf("target = %q", cfg.TranslateTarget("en"))
	}
	// Switching the capability off takes the language choice away too.
	click(t, tt, locales.AiTranslate())
	cfg, _ = a.ai.Config(context.Background())
	if cfg.FeatureOn(ai.FeatureTranslate) {
		t.Error("the translation switch did not save")
	}
	if a.aiForm.features[3] {
		t.Error("the form kept the capability on")
	}
}

// The translation of HTML mail goes back into the markup: a link keeps its
// address and its word for word place.
func TestTranslationKeepsHTMLMarkup(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	key := summaryKey(a.all[0])
	body := a.bodies[key]
	body.Message.BodyHTML = `<p>Body of <a href="https://example.com/agenda">Design review</a></p>`
	a.bodies[key] = body
	tt.Frame()
	click(t, tt, locales.TranslateMessage())
	tr := a.translations[key]
	if tr == nil || tr.html == "" {
		t.Fatalf("no translated HTML: %+v", tr)
	}
	for _, want := range []string{`href="https://example.com/agenda"`, ">Design-Review</a>", "Bericht über"} {
		if !strings.Contains(tr.html, want) {
			t.Errorf("translated HTML lacks %q: %s", want, tr.html)
		}
	}
	if strings.Contains(tr.html, ">Design review</a>") {
		t.Errorf("the link still holds the original words: %s", tr.html)
	}
	// The text without markup is what a conversation shows while another
	// message holds the web view, and what Copy copies.
	if tr.text != testHTMLTranslated {
		t.Errorf("text = %q", tr.text)
	}
}

// A model that will not keep the shape of an HTML request still
// translates: the window asks again for the plain text, and says so when
// asked what was sent.
func TestTranslationFallsBackToPlainText(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	provider.respond = func(*http.Request) (*http.Response, error) {
		answer := testTranslation
		if n := len(provider.systems); n > 0 && strings.Contains(provider.systems[n-1], "the blocks of one email's HTML") {
			// Not the object the instruction asked for.
			answer = "Here is the translation."
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
		return aiReply(http.StatusOK, string(b)), nil
	}
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.TranslateMessage())
	if len(provider.texts) != 2 {
		t.Fatalf("requests = %d, want the block request and the plain one", len(provider.texts))
	}
	tr := a.translations[summaryKey(a.all[0])]
	if tr == nil || tr.html != "" || tr.text != testTranslated {
		t.Fatalf("translation = %+v", tr)
	}
	if !tt.HasText(testTranslated) {
		t.Errorf("the plain translation is not shown: %q", tt.Texts())
	}
	click(t, tt, locales.AiWhatWasSent())
	if !tt.HasText(locales.AiReviewTitle()) || !strings.Contains(a.review.payload.Text, "From: Maya Chen") {
		t.Errorf("what was sent is not the plain request: %q", tt.Texts())
	}
	if strings.Contains(a.review.payload.Text, `"n":1`) {
		t.Error("the failed block request is shown as what was sent")
	}
}

// A message without HTML is translated as text, with no change in how it
// is shown.
func TestTranslationOfPlainMail(t *testing.T) {
	a, _, provider, _ := aiMessageApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	key := summaryKey(a.all[0])
	body := a.bodies[key]
	body.Message.BodyHTML = ""
	a.bodies[key] = body
	tt.Frame()
	click(t, tt, locales.TranslateMessage())
	if len(provider.texts) != 1 {
		t.Fatalf("requests = %d", len(provider.texts))
	}
	tr := a.translations[key]
	if tr == nil || tr.html != "" || tr.text != testTranslated {
		t.Fatalf("translation = %+v", tr)
	}
}

// A conversation has one web view: a translated message claims it, and
// asking for the original gives it back.
func TestTranslationClaimsTheWebView(t *testing.T) {
	a, _, _, _ := aiApp(t, true)
	reviewed(t, a, ai.FeatureTranslate)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.TranslateNewest())
	key := summaryKey(a.conv.msgs[0])
	if a.translationHTMLKey != key {
		t.Fatalf("the translation did not claim the web view: %q", a.translationHTMLKey)
	}
	click(t, tt, locales.ShowOriginal())
	if a.translationHTMLKey != "" || !a.translations[key].original {
		t.Errorf("the original did not take the web view back: %q", a.translationHTMLKey)
	}
	click(t, tt, locales.ShowTranslation())
	if a.translationHTMLKey != key {
		t.Errorf("the translation did not claim the web view again: %q", a.translationHTMLKey)
	}
}

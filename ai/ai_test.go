package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is the in-memory settings store and cache of the tests.
type memStore struct {
	mu       sync.Mutex
	settings map[string]string
	cache    map[string]CacheEntry
}

func newMemStore() *memStore {
	return &memStore{settings: map[string]string{}, cache: map[string]CacheEntry{}}
}

func (s *memStore) Setting(ctx context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.settings[key]
	return v, ok, nil
}

func (s *memStore) SetSetting(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
	return nil
}

func (s *memStore) AICached(ctx context.Context, key string) (CacheEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	return e, ok, nil
}

func (s *memStore) SaveAICache(ctx context.Context, e CacheEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[e.Key] = e
	return nil
}

// memSecrets is an in-memory keychain.
type memSecrets struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemSecrets() *memSecrets { return &memSecrets{data: map[string][]byte{}} }

func (s *memSecrets) Set(key string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), secret...)
	return nil
}

func (s *memSecrets) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return v, nil
}

func (s *memSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// sent is one request the fake provider received.
type sent struct {
	url     string
	headers http.Header
	body    map[string]any
	raw     string
}

// user returns the mail-derived text of a request, whichever wire format
// carried it.
func (r sent) user() string {
	messages, _ := r.body["messages"].([]any)
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] == "user" {
			text, _ := msg["content"].(string)
			return text
		}
	}
	return ""
}

// fakeProvider is an http.RoundTripper standing in for a model provider:
// no socket is opened, and every request is recorded.
type fakeProvider struct {
	mu      sync.Mutex
	calls   []sent
	respond func(r *http.Request) (*http.Response, error)
}

func (f *fakeProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, sent{url: r.URL.String(), headers: r.Header.Clone(), body: body, raw: string(raw)})
	respond := f.respond
	f.mu.Unlock()
	if respond != nil {
		return respond(r)
	}
	return reply(http.StatusOK, `{"choices":[{"message":{"content":"- A summary"}}]}`), nil
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeProvider) last(t *testing.T) sent {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no request reached the provider")
	}
	return f.calls[len(f.calls)-1]
}

func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const testKey = "sk-test-SECRET-0123456789"

// newTestService returns a service with AI on, OpenAI chosen and a key
// stored, talking to a fake provider.
func newTestService(t *testing.T) (*Service, *fakeProvider, *memStore, *memSecrets) {
	t.Helper()
	store, secrets, provider := newMemStore(), newMemSecrets(), &fakeProvider{}
	s := NewService(store, secrets)
	s.SetHTTPClient(&http.Client{
		Transport:     provider,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	cfg := DefaultConfig()
	cfg.Enabled = true
	if err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey(context.Background(), ProviderOpenAI, testKey); err != nil {
		t.Fatal(err)
	}
	return s, provider, store, secrets
}

func testMessage() Message {
	return Message{
		Scope:     "a1",
		Subject:   "Design review moved to Thursday",
		FromName:  "Maya Chen",
		FromEmail: "maya@example.com",
		Date:      time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		Body: "Hi,\n\nThe review moved to Thursday at 10. Please confirm by Wednesday.\n" +
			"Agenda: https://docs.example.com/agenda?utm_source=mail&token=TRACKME\n\n" +
			"-- \nMaya Chen\nHead of Design\n\n" +
			"On Mon, Nov 9, 2026 at 3:00 PM Me <me@example.com> wrote:\n> SECRET-HISTORY can we move it?\n",
	}
}

// With AI off, a capability off or no key, nothing reaches the network.
func TestNothingIsSentUntilAIIsSetUp(t *testing.T) {
	ctx := context.Background()
	store, secrets, provider := newMemStore(), newMemSecrets(), &fakeProvider{}
	s := NewService(store, secrets)
	s.SetHTTPClient(&http.Client{Transport: provider})

	// A new install: AI is off.
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindDisabled {
		t.Errorf("default config: err = %v, want disabled", err)
	}
	if err := s.Check(ctx, FeatureSummary); KindOf(err) != KindDisabled {
		t.Errorf("check = %v, want disabled", err)
	}

	// On, but the provider needs a key and none is stored.
	cfg := DefaultConfig()
	cfg.Enabled = true
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindNoKey {
		t.Errorf("no key: err = %v, want no key", err)
	}
	if _, err := s.Draft(ctx, testMessage(), ""); KindOf(err) != KindNoKey {
		t.Errorf("draft without key: err = %v", err)
	}
	if _, err := s.Triage(ctx, []TriageItem{{ID: "1", Subject: "x"}}, ""); KindOf(err) != KindNoKey {
		t.Errorf("triage without key: err = %v", err)
	}

	// A key, but the capability's own switch is off.
	if err := s.SetKey(context.Background(), ProviderOpenAI, testKey); err != nil {
		t.Fatal(err)
	}
	cfg.SetFeature(FeatureTriage, false)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Triage(ctx, []TriageItem{{ID: "1", Subject: "x"}}, ""); KindOf(err) != KindFeatureOff {
		t.Errorf("triage off: err = %v, want feature off", err)
	}
	// No model chosen.
	cfg.Model = ""
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindNoModel {
		t.Errorf("no model: err = %v, want no model", err)
	}

	if n := provider.count(); n != 0 {
		t.Fatalf("%d requests were sent before AI was set up", n)
	}
	// Summaries are on while triage is off: the switches are independent.
	cfg.Model = "gpt-4o-mini"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); err != nil {
		t.Fatalf("summary with triage off: %v", err)
	}
	if provider.count() != 1 {
		t.Errorf("requests = %d, want 1", provider.count())
	}
}

// A local model works with no key and no internet: the only connection is
// to this machine.
func TestLocalProviderNeedsNoKeyOrInternet(t *testing.T) {
	ctx := context.Background()
	var got sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = sent{url: r.URL.Path, headers: r.Header.Clone(), raw: string(raw)}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"- Local summary"}}]}`)
	}))
	defer server.Close()

	s := NewService(newMemStore(), newMemSecrets())
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ProviderLocal)
	cfg.BaseURL = server.URL + "/v1"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := s.Summarize(ctx, testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "- Local summary" || result.Model != "llama3.2" || result.Provider != ProviderLocal {
		t.Errorf("result = %+v", result)
	}
	if got.url != "/v1/chat/completions" {
		t.Errorf("path = %q", got.url)
	}
	if auth := got.headers.Get("Authorization"); auth != "" {
		t.Errorf("a keyless local request sent Authorization %q", auth)
	}
	if !strings.HasPrefix(server.URL, "http://127.0.0.1") {
		t.Fatalf("the fake is not on loopback: %s", server.URL)
	}
}

// What the user inspects is exactly what is sent, and it leaves out the
// quoted history, the signature and link addresses.
func TestPayloadIsInspectableAndLimited(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	m := testMessage()
	p, err := s.SummaryPayload(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if provider.count() != 0 {
		t.Fatal("inspecting the payload sent a request")
	}
	if p.Model != "gpt-4o-mini" || p.Host != "api.openai.com" || p.Provider != ProviderOpenAI {
		t.Errorf("payload target = %+v", p)
	}
	for _, leaked := range []string{"SECRET-HISTORY", "Head of Design", "TRACKME", "utm_source", "me@example.com", "maya@example.com"} {
		if strings.Contains(p.Text, leaked) {
			t.Errorf("payload contains %q:\n%s", leaked, p.Text)
		}
	}
	for _, want := range []string{"Subject: Design review moved to Thursday", "From: Maya Chen", "Please confirm by Wednesday.", "[link: docs.example.com]"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("payload lacks %q:\n%s", want, p.Text)
		}
	}
	if p.Quoted == 0 || !p.Signature || p.Links != 1 || p.Truncated {
		t.Errorf("excluded = quoted %d signature %v links %d truncated %v", p.Quoted, p.Signature, p.Links, p.Truncated)
	}
	if p.Chars != len([]rune(p.Text)) || p.Limit != BodyLimit {
		t.Errorf("size = %d of %d", p.Chars, p.Limit)
	}

	if _, err := s.Summarize(ctx, m); err != nil {
		t.Fatal(err)
	}
	req := provider.last(t)
	if req.user() != p.Text {
		t.Errorf("sent text differs from the inspected payload:\nsent %q\nshown %q", req.user(), p.Text)
	}
	if !strings.Contains(req.raw, p.Instruction) && !strings.Contains(req.raw, "summarize one email") {
		t.Errorf("instruction missing from the request: %s", req.raw)
	}
	if got := req.headers.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("authorization = %q", got)
	}
	if req.url != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("url = %q", req.url)
	}

	// A long body is cut at the limit.
	m.Body = strings.Repeat("All work and no play. ", 1000)
	p, err = s.SummaryPayload(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || p.Chars > BodyLimit+400 {
		t.Errorf("long body: truncated %v, %d chars", p.Truncated, p.Chars)
	}
}

// A summary is cached by its input and says which model wrote it.
func TestSummaryIsCachedPerInput(t *testing.T) {
	ctx := context.Background()
	s, provider, store, _ := newTestService(t)
	first, err := s.Summarize(ctx, testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached || first.Model != "gpt-4o-mini" || first.Text != "- A summary" {
		t.Errorf("first = %+v", first)
	}
	second, err := s.Summarize(ctx, testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached || second.Text != first.Text || second.Model != first.Model {
		t.Errorf("second = %+v", second)
	}
	if provider.count() != 1 {
		t.Fatalf("requests = %d, want 1", provider.count())
	}
	for _, e := range store.cache {
		if e.Scope != "a1" || e.Feature != FeatureSummary {
			t.Errorf("cache entry = %+v", e)
		}
	}
	// Another message, or another model, is another input.
	other := testMessage()
	other.Body = "Something else entirely."
	if _, err := s.Summarize(ctx, other); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.Config(ctx)
	cfg.SetModelFor(FeatureSummary, "gpt-other")
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := s.Summarize(ctx, testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if result.Cached || result.Model != "gpt-other" {
		t.Errorf("per-feature model result = %+v", result)
	}
	if provider.count() != 3 {
		t.Errorf("requests = %d, want 3", provider.count())
	}
	if got := provider.last(t).body["model"]; got != "gpt-other" {
		t.Errorf("model sent = %v", got)
	}
}

// Failures are told apart, and none of them quotes the key, the prompt or
// the mail.
func TestErrorsAreClassifiedAndCarryNoContent(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		respond func(r *http.Request) (*http.Response, error)
		want    Kind
	}{
		{"invalid key", func(*http.Request) (*http.Response, error) {
			return reply(401, `{"error":{"message":"Incorrect API key provided: `+testKey+`"}}`), nil
		}, KindInvalidKey},
		{"forbidden", func(*http.Request) (*http.Response, error) { return reply(403, `{}`), nil }, KindInvalidKey},
		{"rate limit", func(*http.Request) (*http.Response, error) {
			return reply(429, `{"error":{"message":"slow down"}}`), nil
		}, KindRateLimit},
		{"unknown model", func(*http.Request) (*http.Response, error) {
			return reply(404, `{"error":{"message":"The model does not exist","code":"model_not_found"}}`), nil
		}, KindUnsupportedModel},
		{"model in a 400", func(*http.Request) (*http.Response, error) {
			return reply(400, `{"error":{"message":"Model Not Exist","type":"invalid_request_error"}}`), nil
		}, KindUnsupportedModel},
		{"other 400", func(*http.Request) (*http.Response, error) {
			return reply(400, `{"error":{"message":"Design review moved to Thursday is too long"}}`), nil
		}, KindServer},
		{"server error", func(*http.Request) (*http.Response, error) { return reply(503, `oops`), nil }, KindServer},
		{"gateway timeout", func(*http.Request) (*http.Response, error) { return reply(504, ``), nil }, KindTimeout},
		{"unreachable", func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: connection refused while sending " + testKey)
		}, KindUnreachable},
		{"redirect", func(*http.Request) (*http.Response, error) {
			resp := reply(302, ``)
			resp.Header.Set("Location", "https://elsewhere.example.com/")
			return resp, nil
		}, KindServer},
		{"unreadable answer", func(*http.Request) (*http.Response, error) { return reply(200, `<html>hello</html>`), nil }, KindBadResponse},
		{"empty answer", func(*http.Request) (*http.Response, error) {
			return reply(200, `{"choices":[{"message":{"content":"  "}}]}`), nil
		}, KindBadResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, provider, _, _ := newTestService(t)
			provider.respond = tc.respond
			_, err := s.Summarize(ctx, testMessage())
			if KindOf(err) != tc.want {
				t.Fatalf("err = %v, want kind %d", err, tc.want)
			}
			text := err.Error()
			for _, leaked := range []string{testKey, "Design review", "Maya", "Thursday", "example.com", "elsewhere"} {
				if strings.Contains(text, leaked) {
					t.Errorf("error %q quotes %q", text, leaked)
				}
			}
			if provider.count() != 1 {
				t.Errorf("requests = %d: a redirect or a retry was followed", provider.count())
			}
		})
	}
}

// A slow provider times out, and a cancelled request stops at once.
func TestTimeoutAndCancel(t *testing.T) {
	block := func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	s, provider, _, _ := newTestService(t)
	provider.respond = block
	s.SetTimeout(20 * time.Millisecond)
	if _, err := s.Summarize(context.Background(), testMessage()); KindOf(err) != KindTimeout {
		t.Errorf("slow provider: err = %v, want timeout", err)
	}

	s, provider, _, _ = newTestService(t)
	provider.respond = block
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Summarize(ctx, testMessage())
		done <- err
	}()
	// Cancel once the request is in flight.
	for provider.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if KindOf(err) != KindCancelled {
			t.Errorf("cancelled: err = %v, want cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request did not return")
	}
}

// Plain HTTP to another machine is refused until the user opts in; plain
// HTTP to this machine is always fine.
func TestPlainHTTPNeedsOptIn(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	cfg, _ := s.Config(ctx)
	cfg.UseProvider(ProviderCustom)
	cfg.BaseURL = "http://models.example.com/v1"
	cfg.Model = "any"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !NeedsHTTPOptIn(cfg) {
		t.Error("a remote http endpoint does not ask for the opt-in")
	}
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindInsecure {
		t.Fatalf("err = %v, want insecure", err)
	}
	if err := s.Test(ctx, cfg, ""); KindOf(err) != KindInsecure {
		t.Errorf("test connection: err = %v, want insecure", err)
	}
	if provider.count() != 0 {
		t.Fatal("mail text went out over plain HTTP without the opt-in")
	}
	cfg.AllowHTTP = true
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); err != nil {
		t.Fatalf("after the opt-in: %v", err)
	}
	if got := provider.last(t).url; got != "http://models.example.com/v1/chat/completions" {
		t.Errorf("url = %q", got)
	}

	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true, "ollama.localhost": true,
		"example.com": false, "192.168.1.20": false, "": false,
	} {
		if got := IsLoopback(host); got != want {
			t.Errorf("IsLoopback(%q) = %v", host, got)
		}
	}
	local := DefaultConfig()
	local.UseProvider(ProviderLocal)
	if NeedsHTTPOptIn(local) {
		t.Error("the local provider asks for the plain HTTP opt-in")
	}
	// Anything that is not HTTP is not an endpoint.
	local.BaseURL = "file:///etc/passwd"
	if _, err := checkEndpoint(local); KindOf(err) != KindBadConfig {
		t.Errorf("file endpoint: err = %v", err)
	}
}

// Anthropic speaks its own wire format.
func TestAnthropicWireFormat(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	provider.respond = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"content":[{"type":"text","text":"- From Claude"}]}`), nil
	}
	cfg, _ := s.Config(ctx)
	cfg.UseProvider(ProviderAnthropic)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// The OpenAI key is not Anthropic's.
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindNoKey {
		t.Fatalf("err = %v, want no key for a provider without one", err)
	}
	if err := s.SetKey(context.Background(), ProviderAnthropic, "sk-ant-KEY"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Summarize(ctx, testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "- From Claude" || result.Provider != ProviderAnthropic {
		t.Errorf("result = %+v", result)
	}
	req := provider.last(t)
	if req.url != "https://api.anthropic.com/v1/messages" {
		t.Errorf("url = %q", req.url)
	}
	if req.headers.Get("x-api-key") != "sk-ant-KEY" || req.headers.Get("anthropic-version") == "" {
		t.Errorf("headers = %v", req.headers)
	}
	if req.headers.Get("Authorization") != "" {
		t.Error("the Anthropic request carries a bearer token")
	}
	if system, _ := req.body["system"].(string); !strings.Contains(system, "summarize one email") {
		t.Errorf("system = %q", system)
	}
	if req.body["max_tokens"] == nil {
		t.Error("max_tokens is missing")
	}
}

// A key can be stored, checked for and deleted, but never read back, and
// it never reaches the settings.
func TestKeysStayInTheKeychain(t *testing.T) {
	ctx := context.Background()
	s, _, store, secrets := newTestService(t)
	if ok, err := s.KeyStored(ProviderOpenAI); err != nil || !ok {
		t.Fatalf("stored = %v, %v", ok, err)
	}
	if ok, _ := s.KeyStored(ProviderDeepSeek); ok {
		t.Error("a key is reported for a provider that has none")
	}
	if got := string(secrets.data[KeyName(ProviderOpenAI)]); got != testKey {
		t.Errorf("keychain holds %q", got)
	}
	cfg, _ := s.Config(ctx)
	cfg.MarkReviewed(FeatureSummary)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for k, v := range store.settings {
		if strings.Contains(v, testKey) || strings.Contains(v, "SECRET") {
			t.Errorf("setting %s holds the key: %s", k, v)
		}
	}
	// Replacing a key keeps one entry.
	if err := s.SetKey(context.Background(), ProviderOpenAI, "  sk-new  "); err != nil {
		t.Fatal(err)
	}
	if got := string(secrets.data[KeyName(ProviderOpenAI)]); got != "sk-new" {
		t.Errorf("replaced key = %q", got)
	}
	if err := s.SetKey(context.Background(), ProviderOpenAI, "   "); KindOf(err) != KindNoKey {
		t.Errorf("empty key: err = %v", err)
	}
	if err := s.DeleteKey(ProviderOpenAI); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.KeyStored(ProviderOpenAI); ok {
		t.Error("the key outlived its deletion")
	}
	if err := s.DeleteKey(ProviderOpenAI); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindNoKey {
		t.Errorf("after deleting the key: err = %v", err)
	}
}

// The connection test sends a fixed word, never mail, and works before AI
// is switched on.
func TestConnectionTest(t *testing.T) {
	ctx := context.Background()
	store, secrets, provider := newMemStore(), newMemSecrets(), &fakeProvider{}
	s := NewService(store, secrets)
	s.SetHTTPClient(&http.Client{Transport: provider})
	cfg := DefaultConfig()
	if err := s.Test(ctx, cfg, ""); KindOf(err) != KindNoKey {
		t.Fatalf("without a key: err = %v", err)
	}
	if provider.count() != 0 {
		t.Fatal("a test without a key reached the network")
	}
	if err := s.Test(ctx, cfg, "sk-typed"); err != nil {
		t.Fatal(err)
	}
	req := provider.last(t)
	if req.user() != "ping" {
		t.Errorf("test sent %q", req.user())
	}
	if req.headers.Get("Authorization") != "Bearer sk-typed" {
		t.Errorf("authorization = %q", req.headers.Get("Authorization"))
	}
	// A typed key is used for the test, not stored by it.
	if len(secrets.data) != 0 {
		t.Errorf("the test stored a key: %v", secrets.data)
	}
	provider.respond = func(*http.Request) (*http.Response, error) { return reply(401, `{}`), nil }
	if err := s.Test(ctx, cfg, "sk-wrong"); KindOf(err) != KindInvalidKey {
		t.Errorf("wrong key: err = %v", err)
	}
}

// Triage keeps only answers about listed messages and known groups.
func TestTriageValidatesTheAnswer(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	provider.respond = func(*http.Request) (*http.Response, error) {
		answer := "Here you go:\n```json\n" + `[
			{"n": 1, "group": "needs_you", "reason": "Asks you to confirm by Wednesday"},
			{"n": 2, "group": "Can Archive", "reason": "Automated notification"},
			{"n": 3, "group": "delete_everything", "reason": "bad group"},
			{"n": 9, "group": "can_wait", "reason": "no such message"},
			{"n": 1, "group": "can_archive", "reason": "duplicate"}
		]` + "\n```"
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
		return reply(200, string(body)), nil
	}
	items := []TriageItem{
		{ID: "INBOX/1", FromName: "Maya Chen", FromEmail: "maya@example.com", Subject: "Design review", Preview: "Please confirm https://t.example.com/c?id=TRACK", Unread: true},
		{ID: "INBOX/2", FromName: "GitHub", FromEmail: "notifications@github.com", Subject: "PR #128", Preview: "requested your review"},
		{ID: "INBOX/3", FromEmail: "lena@example.com", Subject: "Contract", Preview: "Signed copy attached"},
	}
	p, err := s.TriagePayload(ctx, items, "English")
	if err != nil {
		t.Fatal(err)
	}
	if p.Messages != 3 || p.Links != 1 || strings.Contains(p.Text, "TRACK") || strings.Contains(p.Text, "INBOX/") {
		t.Errorf("triage payload = %+v", p)
	}
	if !strings.Contains(p.Text, "1. From: Maya Chen <maya@example.com> | Subject: Design review | unread") {
		t.Errorf("triage text:\n%s", p.Text)
	}
	result, err := s.Triage(ctx, items, "English")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v", result.Suggestions)
	}
	if got := result.Suggestions[0]; got.ID != "INBOX/1" || got.Group != GroupNeedsYou || got.Reason == "" {
		t.Errorf("first = %+v", got)
	}
	if got := result.Suggestions[1]; got.ID != "INBOX/2" || got.Group != GroupCanArchive {
		t.Errorf("second = %+v", got)
	}
	if result.Model != "gpt-4o-mini" {
		t.Errorf("model = %q", result.Model)
	}
	if provider.last(t).user() != p.Text {
		t.Error("sent triage text differs from the inspected payload")
	}

	// Prose without a list is not an answer.
	provider.respond = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"choices":[{"message":{"content":"I cannot help with that."}}]}`), nil
	}
	if _, err := s.Triage(ctx, items, ""); KindOf(err) != KindBadResponse {
		t.Errorf("prose answer: err = %v", err)
	}
	// No more than the limit is listed.
	many := make([]TriageItem, TriageLimit+20)
	for i := range many {
		many[i] = TriageItem{ID: "x", Subject: "s"}
	}
	if p, _ := s.TriagePayload(ctx, many, ""); p.Messages != TriageLimit {
		t.Errorf("listed %d messages, limit %d", p.Messages, TriageLimit)
	}
}

// A draft carries the user's guidance and comes back as plain text.
func TestDraftReply(t *testing.T) {
	ctx := context.Background()
	s, provider, _, _ := newTestService(t)
	provider.respond = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"choices":[{"message":{"content":"Thursday at 10 works for me."}}]}`), nil
	}
	m := testMessage()
	m.Language = "English"
	p, err := s.DraftPayload(ctx, m, "accept, and ask for the agenda")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "Guidance from the person replying: accept, and ask for the agenda") {
		t.Errorf("draft payload:\n%s", p.Text)
	}
	if !strings.Contains(p.Instruction, "Write in English.") || !strings.Contains(p.Instruction, "untrusted") {
		t.Errorf("instruction = %q", p.Instruction)
	}
	result, err := s.Draft(ctx, m, "accept, and ask for the agenda")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Thursday at 10 works for me." || result.Cached {
		t.Errorf("draft = %+v", result)
	}
	if provider.last(t).user() != p.Text {
		t.Error("sent draft text differs from the inspected payload")
	}
	// A message with nothing but quoted history has nothing to send.
	m.Body = "> only a quote\n> and more"
	if _, err := s.Draft(ctx, m, ""); KindOf(err) != KindEmpty {
		t.Errorf("empty excerpt: err = %v", err)
	}
	if provider.count() != 1 {
		t.Errorf("requests = %d, want 1", provider.count())
	}
}

// The user's approval of a payload is remembered per capability.
func TestReviewIsRememberedPerFeature(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	cfg, _ := s.Config(ctx)
	if cfg.HasReviewed(FeatureSummary) {
		t.Fatal("a new install has already approved summaries")
	}
	if err := s.MarkReviewed(ctx, FeatureSummary); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Config(ctx)
	if !cfg.HasReviewed(FeatureSummary) || cfg.HasReviewed(FeatureDraft) {
		t.Errorf("reviewed = %v", cfg.Reviewed)
	}
	// Another model on the same server keeps the approval; another server
	// does not, as the text would now go somewhere else.
	cfg.Model = "gpt-other"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = s.Config(ctx); !cfg.HasReviewed(FeatureSummary) {
		t.Error("changing the model dropped the approval")
	}
	cfg.UseProvider(ProviderLocal)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = s.Config(ctx); cfg.HasReviewed(FeatureSummary) {
		t.Error("the approval survived a change of provider")
	}
	if err := s.MarkReviewed(ctx, FeatureSummary); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Config(ctx)
	cfg.BaseURL = "http://127.0.0.1:1234/v1"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = s.Config(ctx); cfg.HasReviewed(FeatureSummary) {
		t.Error("the approval survived a change of server")
	}
	// Settings that do not parse fall back to AI off.
	if err := s.store.SetSetting(ctx, configKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.Config(ctx); cfg.Enabled {
		t.Error("corrupt settings left AI on")
	}
}

func TestExcerpt(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		miss []string
	}{
		{"plain", "Hello\n\n\n\nWorld  ", "Hello\n\nWorld", nil},
		{"quoted lines", "Sounds good.\n> earlier text\n>> older text\nThanks", "Sounds good.\nThanks", []string{"earlier", "older"}},
		{"reply header", "Yes.\n\nOn Tue, Nov 10, 2026, Bob <bob@example.com> wrote:\nplain history without markers", "Yes.", []string{"history", "bob@example.com"}},
		{"wrapped reply header", "Yes.\n\nOn Tue, Nov 10, 2026 at 9:00 AM Bob Example <bob@example.com>\nwrote:\n> hi", "Yes.", []string{"Bob Example"}},
		{"chinese reply header", "好的。\n\n在 2026年11月10日，张三 <zhang@example.com> 写道：\n> 之前的内容", "好的。", []string{"之前的内容", "zhang@example.com"}},
		{"outlook block", "Fine by me.\n\nFrom: Bob <bob@example.com>\nSent: Tuesday\nTo: Me\nSubject: Re: plan\n\nold body", "Fine by me.", []string{"old body"}},
		{"original message", "OK\n-----Original Message-----\nFrom: x\nold", "OK", []string{"old"}},
		{"signature", "See you then.\n-- \nBob\n+1 555 0100", "See you then.", []string{"555"}},
		{"mobile signature", "On my way\n\nSent from my iPhone", "On my way", []string{"iPhone"}},
		{"forward keeps its text", "FYI\n\n---------- Forwarded message ----------\nFrom: Carol <carol@example.com>\nDate: Monday\nSubject: Budget\n\nThe budget is approved.", "The budget is approved.", nil},
		{"a From line in prose", "From: the desk of nobody\nplain text", "From: the desk of nobody\nplain text", nil},
		{"link", "Read https://example.com/a/b?utm=1&id=SECRET, then reply.", "Read [link: example.com], then reply.", []string{"SECRET", "utm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := MakeExcerpt(tc.body, BodyLimit)
			if !strings.Contains(e.Text, tc.want) {
				t.Errorf("excerpt %q lacks %q", e.Text, tc.want)
			}
			for _, miss := range tc.miss {
				if strings.Contains(e.Text, miss) {
					t.Errorf("excerpt %q still has %q", e.Text, miss)
				}
			}
		})
	}
	if e := MakeExcerpt(strings.Repeat("字", 50), 10); !e.Truncated || len([]rune(e.Text)) != 11 {
		t.Errorf("truncation = %+v", e)
	}
	if e := MakeExcerpt("> only\n> quotes", BodyLimit); e.Text != "" || e.Quoted != 2 {
		t.Errorf("all quoted = %+v", e)
	}
}

// A provider switched off refuses a request before any network call, and
// its settings and key stay for when it is switched back on.
func TestOffProviderRefusesBeforeSending(t *testing.T) {
	ctx := context.Background()
	s, provider, store, secrets := newTestService(t)
	cfg, err := s.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderOff(ProviderOpenAI) {
		t.Fatal("a provider is off by default")
	}
	cfg.SetProviderOff(ProviderOpenAI, true)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); KindOf(err) != KindProviderOff {
		t.Errorf("summarize: err = %v, want provider off", err)
	}
	if provider.count() != 0 {
		t.Errorf("%d requests reached a provider that is off", provider.count())
	}
	if got := string(secrets.data[KeyName(ProviderOpenAI)]); got != testKey {
		t.Errorf("the key went away with the provider: %q", got)
	}
	// Switching it back on needs nothing else.
	cfg, _ = s.Config(ctx)
	cfg.SetProviderOff(ProviderOpenAI, false)
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); err != nil {
		t.Fatalf("after switching it on: %v", err)
	}
	if provider.count() != 1 {
		t.Errorf("%d requests, want 1", provider.count())
	}
	for k, v := range store.settings {
		if strings.Contains(v, testKey) {
			t.Errorf("setting %s holds the key", k)
		}
	}
}

// A key goes only to the endpoint it was saved for: moving an editable
// provider to another server makes the stored key unusable until it is
// saved again.
func TestKeyStaysWithItsEndpoint(t *testing.T) {
	ctx := context.Background()
	store, secrets, provider := newMemStore(), newMemSecrets(), &fakeProvider{}
	s := NewService(store, secrets)
	s.SetHTTPClient(&http.Client{Transport: provider})
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ProviderCustom)
	cfg.BaseURL = "https://one.example.com/v1"
	cfg.Model = "m"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKey(ctx, ProviderCustom, testKey); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.Config(ctx)
	if got := saved.KeyEndpoint(ProviderCustom); got != "https://one.example.com/v1" {
		t.Errorf("endpoint = %q", got)
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BaseURL = "https://two.example.com/v1"
	if err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	_, err = s.Summarize(ctx, testMessage())
	if KindOf(err) != KindKeyEndpoint {
		t.Fatalf("err = %v, want a key saved for another server", err)
	}
	if provider.count() != 0 {
		t.Errorf("the key was sent to the new server")
	}
	// Saving it again for this endpoint makes it usable there.
	if err := s.SetKey(ctx, ProviderCustom, testKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Summarize(ctx, testMessage()); err != nil {
		t.Fatalf("after saving the key again: %v", err)
	}
	if got := string(secrets.data[KeyName(ProviderCustom)]); got != testKey {
		t.Errorf("keychain holds %q", got)
	}
}

// The reasoning effort reaches the tool as the option it takes.
func TestEffortReachesTheTool(t *testing.T) {
	args, _ := cliLaunch("claude", "sonnet", ProviderClaudeCLI, "high")
	if !hasPair(args, "--effort", "high") {
		t.Errorf("claude args = %q", args)
	}
	args, _ = cliLaunch("codex", "gpt-5-codex", ProviderCodexCLI, "low")
	if !hasPair(args, "-c", "model_reasoning_effort=low") {
		t.Errorf("codex args = %q", args)
	}
	for _, eff := range []string{"", "extreme"} {
		args, _ = cliLaunch("claude", "sonnet", ProviderClaudeCLI, eff)
		if strings.Contains(strings.Join(args, " "), "--effort") {
			t.Errorf("effort %q was passed: %q", eff, args)
		}
	}
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

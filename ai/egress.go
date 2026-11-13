package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Service is the AI layer's public surface. Every request to a model
// provider leaves through its send method and nowhere else.
type Service struct {
	store   Store
	secrets Secrets
	client  *http.Client
	// clientMu guards the client, which the window may replace when the
	// network settings change while a request is in flight.
	clientMu sync.RWMutex
	timeout  time.Duration
	now      func() time.Time
}

// NewService wires the settings store and the keychain.
func NewService(store Store, secrets Secrets) *Service {
	return &Service{
		store:   store,
		secrets: secrets,
		timeout: 90 * time.Second,
		now:     time.Now,
		client: &http.Client{
			// A redirect would carry the mail text to a host the user did
			// not choose, so none is followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// SetHTTPClient replaces the HTTP client, for tests that count requests.
func (s *Service) SetHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	s.clientMu.Lock()
	s.client = c
	s.clientMu.Unlock()
}

// SetTimeout bounds one request, for tests.
func (s *Service) SetTimeout(d time.Duration) {
	if d > 0 {
		s.timeout = d
	}
}

// request is everything one model call sends: an instruction written by
// Shu and the user text built from the excerpt.
type request struct {
	feature Feature
	system  string
	user    string
}

// Payload is a request as the user can inspect it before it is sent: the
// exact text, where it goes and what was left out.
type Payload struct {
	Feature  Feature
	Provider ProviderID
	Model    string
	// Host is the server the text would go to.
	Host string
	// Instruction is Shu's own prompt, and Text the mail-derived text sent
	// with it. Together they are the whole payload.
	Instruction string
	Text        string
	// Chars is the size of Text and Limit the most it may be.
	Chars int
	Limit int
	// What the excerpt left out.
	Quoted    int
	Signature bool
	Links     int
	Truncated bool
	// Messages is how many messages the payload describes, and Omitted
	// how many earlier ones of a conversation were left out to keep it
	// within its limit.
	Messages int
	Omitted  int
}

// ready checks, without any network call, that a capability may be used:
// AI is on, the capability is on, a model is chosen, the endpoint is
// acceptable and the key the provider needs is stored.
func (s *Service) ready(cfg Config, f Feature) (key string, err error) {
	if !cfg.Enabled {
		return "", &Error{Kind: KindDisabled}
	}
	if !cfg.FeatureOn(f) {
		return "", &Error{Kind: KindFeatureOff}
	}
	return s.usable(cfg, cfg.ModelFor(f), "")
}

// usable checks the parts of ready that a connection test shares. A typed
// key takes the place of the stored one.
func (s *Service) usable(cfg Config, model, typedKey string) (string, error) {
	p, ok := ProviderByID(cfg.Provider)
	if !ok {
		return "", &Error{Kind: KindBadConfig}
	}
	if cfg.ProviderOff(cfg.Provider) {
		return "", &Error{Kind: KindProviderOff}
	}
	if strings.TrimSpace(model) == "" {
		return "", &Error{Kind: KindNoModel}
	}
	if p.CLI != "" {
		// A local tool signs in itself; there is no key, endpoint or
		// transport policy to check.
		return "", nil
	}
	if _, err := checkEndpoint(cfg); err != nil {
		return "", err
	}
	key := strings.TrimSpace(typedKey)
	if key == "" {
		var err error
		if key, err = s.key(cfg.Provider); err != nil {
			return "", err
		}
	}
	if key == "" && p.NeedsKey {
		return "", &Error{Kind: KindNoKey}
	}
	// A stored key goes only to the server it was given to. One typed now
	// is bound to this endpoint as it is saved.
	if key != "" && strings.TrimSpace(typedKey) == "" && cfg.KeyEndpointMismatch(cfg.Provider) {
		return "", &Error{Kind: KindKeyEndpoint}
	}
	return key, nil
}

// Check reports why a capability cannot be used right now, or nil. It
// makes no network call, so the UI can explain how to enable AI.
func (s *Service) Check(ctx context.Context, f Feature) error {
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if _, err := s.ready(cfg, f); err != nil {
		return err
	}
	if CLIProvider(cfg.Provider) {
		status := s.CLIStatus(ctx, cfg.Provider, cfg.CLIPath(cfg.Provider))
		switch {
		case !status.Installed:
			return &Error{Kind: KindCLINotInstalled}
		case !status.SignedIn:
			return &Error{Kind: KindCLISignIn}
		}
	}
	return nil
}

// checkEndpoint parses the endpoint and applies the transport policy:
// HTTPS always, plain HTTP to this machine, and plain HTTP to another
// machine only after the user's explicit opt-in.
func checkEndpoint(cfg Config) (*url.URL, error) {
	u, err := url.Parse(cfg.Endpoint())
	if err != nil || u.Host == "" {
		return nil, &Error{Kind: KindBadConfig}
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !IsLoopback(u.Hostname()) && !cfg.AllowHTTP {
			return nil, &Error{Kind: KindInsecure}
		}
	default:
		return nil, &Error{Kind: KindBadConfig}
	}
	return u, nil
}

// NeedsHTTPOptIn reports whether the endpoint is a remote server over
// plain HTTP, which the settings must warn about.
func NeedsHTTPOptIn(cfg Config) bool {
	u, err := url.Parse(cfg.Endpoint())
	if err != nil || u.Scheme != "http" {
		return false
	}
	return !IsLoopback(u.Hostname())
}

// IsLoopback reports whether a host names this machine.
func IsLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// send is the single egress point: the only function that puts text on
// the network. It re-checks that the capability may be used, enforces the
// payload cap, and returns errors that never quote what was sent.
func (s *Service) send(ctx context.Context, cfg Config, req request) (string, error) {
	key, err := s.ready(cfg, req.feature)
	if err != nil {
		return "", err
	}
	return s.post(ctx, cfg, cfg.ModelFor(req.feature), key, req)
}

// post performs the request. Test, which sends a fixed word instead of
// mail, is its only other caller.
func (s *Service) post(ctx context.Context, cfg Config, model, key string, req request) (string, error) {
	if strings.TrimSpace(req.user) == "" {
		return "", &Error{Kind: KindEmpty}
	}
	if len([]rune(req.user)) > payloadLimit {
		req.user = string([]rune(req.user)[:payloadLimit])
	}
	p, _ := ProviderByID(cfg.Provider)
	if p.CLI != "" {
		return s.runCLI(ctx, cfg, model, req)
	}
	endpoint, err := checkEndpoint(cfg)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(endpoint.String(), "/")

	var (
		target  string
		body    any
		headers = map[string]string{"Content-Type": "application/json"}
	)
	if p.Messages {
		target = base + "/v1/messages"
		if strings.HasSuffix(base, "/v1") {
			target = base + "/messages"
		}
		headers["anthropic-version"] = "2023-06-01"
		if key != "" {
			headers["x-api-key"] = key
		}
		body = map[string]any{
			"model":      model,
			"max_tokens": 1024,
			"system":     req.system,
			"messages":   []map[string]string{{"role": "user", "content": req.user}},
		}
	} else {
		target = base + "/chat/completions"
		if strings.HasSuffix(base, "/chat/completions") {
			target = base
		}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
		// Only what every OpenAI-compatible server accepts: sampling and
		// length parameters differ between providers and model families.
		body = map[string]any{
			"model":  model,
			"stream": false,
			"messages": []map[string]string{
				{"role": "system", "content": req.system},
				{"role": "user", "content": req.user},
			},
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", &Error{Kind: KindBadConfig}
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return "", &Error{Kind: KindBadConfig}
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	s.clientMu.RLock()
	client := s.client
	s.clientMu.RUnlock()
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", transportError(ctx, err)
	}
	defer resp.Body.Close()
	// A megabyte is far more than any answer these features ask for.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", statusError(resp.StatusCode, data)
	}
	text, ok := answerText(p.Messages, data)
	if !ok || strings.TrimSpace(text) == "" {
		return "", &Error{Kind: KindBadResponse}
	}
	return strings.TrimSpace(text), nil
}

// transportError classifies a failure to complete the HTTP exchange. The
// underlying error is dropped: it can quote the URL, and with it a host
// the user may not want in a log.
func transportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		return &Error{Kind: KindCancelled}
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Kind: KindTimeout}
	}
	return &Error{Kind: KindUnreachable}
}

// statusError classifies an error status. The provider's message is read
// only to tell a model problem from others; it is never returned, as it
// can echo the prompt or part of the key.
func statusError(status int, data []byte) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Kind: KindInvalidKey, Status: status}
	case http.StatusTooManyRequests:
		return &Error{Kind: KindRateLimit, Status: status}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return &Error{Kind: KindTimeout, Status: status}
	case http.StatusNotFound:
		return &Error{Kind: KindUnsupportedModel, Status: status}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		if mentionsModel(data) {
			return &Error{Kind: KindUnsupportedModel, Status: status}
		}
	}
	return &Error{Kind: KindServer, Status: status}
}

// mentionsModel reports whether a provider's error is about the model.
func mentionsModel(data []byte) bool {
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &parsed) != nil || len(parsed.Error) == 0 {
		return false
	}
	var detail struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Param   string `json:"param"`
	}
	if json.Unmarshal(parsed.Error, &detail) != nil {
		var message string
		if json.Unmarshal(parsed.Error, &message) != nil {
			return false
		}
		detail.Message = message
	}
	code, _ := detail.Code.(string)
	text := strings.ToLower(detail.Message + " " + code + " " + detail.Type + " " + detail.Param)
	return strings.Contains(text, "model")
}

// answerText reads the model's text out of a successful response.
func answerText(messages bool, data []byte) (string, bool) {
	if messages {
		var parsed struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(data, &parsed) != nil {
			return "", false
		}
		var b strings.Builder
		for _, part := range parsed.Content {
			if part.Type == "" || part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
		return b.String(), b.Len() > 0
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &parsed) != nil || len(parsed.Choices) == 0 {
		return "", false
	}
	return parsed.Choices[0].Message.Content, true
}

// Test checks the provider settings with one tiny request that carries no
// mail: the word "ping". It runs only when the user presses the button,
// and works before AI is switched on, so a setup can be verified first.
// A typed key is used without being stored.
func (s *Service) Test(ctx context.Context, cfg Config, typedKey string) error {
	if CLIProvider(cfg.Provider) {
		// A local tool is checked by its own status commands: no prompt,
		// and so no model quota.
		status := s.CLIStatus(ctx, cfg.Provider, cfg.CLIPath(cfg.Provider))
		switch {
		case !status.Installed:
			return &Error{Kind: KindCLINotInstalled}
		case !status.SignedIn:
			return &Error{Kind: KindCLISignIn}
		}
		return nil
	}
	key, err := s.usable(cfg, cfg.Model, typedKey)
	if err != nil {
		return err
	}
	_, err = s.post(ctx, cfg, strings.TrimSpace(cfg.Model), key, request{
		system: "Reply with the single word: pong",
		user:   "ping",
	})
	return err
}

// payload describes a request for inspection.
func payload(cfg Config, req request, limit, messages int, e Excerpt) Payload {
	host := ""
	if CLIProvider(cfg.Provider) {
		host = "This computer"
	} else if u, err := url.Parse(cfg.Endpoint()); err == nil {
		host = u.Host
	}
	return Payload{
		Feature:     req.feature,
		Provider:    cfg.Provider,
		Model:       cfg.ModelFor(req.feature),
		Host:        host,
		Instruction: req.system,
		Text:        req.user,
		Chars:       len([]rune(req.user)),
		Limit:       limit,
		Quoted:      e.Quoted,
		Signature:   e.Signature,
		Links:       e.Links,
		Truncated:   e.Truncated,
		Messages:    messages,
	}
}

package mail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

// testClock is a test's time, which moves only when the test moves it.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// logRecorder keeps what is logged during a test, by the standard logger
// and by the fake servers, for the test to look for tokens in.
type logRecorder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logRecorder) Printf(format string, args ...any) { fmt.Fprintf(l, format+"\n", args...) }
func (l *logRecorder) Println(args ...any)               { fmt.Fprintln(l, args...) }

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureLogs has the standard logger write to a recorder until the test
// ends.
func captureLogs(t *testing.T) *logRecorder {
	l := &logRecorder{}
	out := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(out) })
	return l
}

// fakeOAuth is a provider's authorization and token endpoints, over TLS
// on 127.0.0.1. It checks what a provider checks: the client, and its
// secret where the provider has one; a redirect to a loopback port; the
// PKCE verifier of the challenge; a code used once. The fake mail servers
// take its access tokens while they are valid.
type fakeOAuth struct {
	server   *httptest.Server
	clientID string
	// secret is the client's secret, "" for a public client, which must
	// then send none, as with Microsoft.
	secret string
	now    func() time.Time

	mu sync.Mutex
	// user is the address the browser is signed in as, which the ID
	// token names; deny has the user decline.
	user string
	deny bool
	// rotate has each refresh replace the refresh token, as Microsoft's
	// do. life is how long an access token lasts.
	rotate bool
	life   time.Duration
	// scope is what a sign-in is granted, "" for what it asked for.
	scope string
	// claims edits the claims of each ID token.
	claims func(map[string]any)
	// answer, when set, is the token endpoint's answer to any request.
	answer func(form url.Values) (status int, body string)
	// beforeRefresh runs, unlocked, as a refresh request arrives.
	beforeRefresh func()

	codes   map[string]fakeCode
	access  map[string]fakeAccess
	refresh map[string]string
	// issued is every code and token handed out, asked the query of each
	// authorization request, and problems what requests did wrong.
	issued    []string
	asked     []url.Values
	problems  []string
	exchanges int
	refreshes int
}

type fakeCode struct{ challenge, redirect, user, scope string }

type fakeAccess struct {
	user    string
	expires time.Time
}

func startFakeOAuth(t *testing.T, provider string, now func() time.Time, logs *logRecorder) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{
		clientID: "shu-test." + provider, now: now, user: "ann@example.com", life: time.Hour,
		codes: map[string]fakeCode{}, access: map[string]fakeAccess{}, refresh: map[string]string{},
	}
	if provider == OAuthGoogle {
		f.secret = "secret-" + rand.Text()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	f.server = httptest.NewUnstartedServer(mux)
	f.server.Config.ErrorLog = log.New(logs, "fake provider: ", 0)
	f.server.StartTLS()
	t.Cleanup(func() {
		f.server.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range f.problems {
			t.Errorf("fake provider: %s", p)
		}
	})
	return f
}

func (f *fakeOAuth) endpoints() OAuthEndpoints {
	return OAuthEndpoints{
		AuthURL: f.server.URL + "/authorize", TokenURL: f.server.URL + "/token",
		ClientID: f.clientID, ClientSecret: f.secret,
	}
}

// set changes the fake under its lock.
func (f *fakeOAuth) set(change func(f *fakeOAuth)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// issue makes a code or a token, and remembers it for the tests that look
// for leaks. f.mu is held.
func (f *fakeOAuth) issue(kind string) string {
	s := kind + "-" + rand.Text() + rand.Text()
	f.issued = append(f.issued, s)
	return s
}

func (f *fakeOAuth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Scheme != "http" || back.Hostname() != "127.0.0.1" || back.Port() == "" ||
		back.Path != "" || back.RawQuery != "" || q.Get("client_id") != f.clientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("state") == "" {
		f.problems = append(f.problems, fmt.Sprintf("a bad authorization request: %v", q))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	answer := url.Values{"state": {q.Get("state")}}
	if f.deny {
		answer.Set("error", "access_denied")
		answer.Set("error_description", "The user declined.")
	} else {
		code := f.issue("code")
		f.codes[code] = fakeCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), user: f.user, scope: q.Get("scope")}
		answer.Set("code", code)
	}
	back.RawQuery = answer.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	if form.Get("grant_type") == "refresh_token" {
		f.mu.Lock()
		before := f.beforeRefresh
		f.mu.Unlock()
		if before != nil {
			before()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answer != nil {
		status, body := f.answer(form)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}
	_, hasSecret := form["client_secret"]
	if form.Get("client_id") != f.clientID || form.Get("client_secret") != f.secret || (f.secret == "" && hasSecret) {
		f.problems = append(f.problems, "a token request from the wrong client")
		f.refuse(w, http.StatusUnauthorized, "invalid_client", "Unknown client.")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		f.exchanges++
		code, ok := f.codes[form.Get("code")]
		delete(f.codes, form.Get("code"))
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || code.redirect != form.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			f.problems = append(f.problems, "a code redeemed without its redirect or verifier, or twice")
			f.refuse(w, http.StatusBadRequest, "invalid_grant", "Bad code.")
			return
		}
		scope := code.scope
		if f.scope != "" {
			scope = f.scope
		}
		refresh := f.issue("refresh")
		f.refresh[refresh] = code.user
		f.grant(w, code.user, refresh, scope)
	case "refresh_token":
		f.refreshes++
		old := form.Get("refresh_token")
		user, ok := f.refresh[old]
		if !ok {
			f.refuse(w, http.StatusBadRequest, "invalid_grant", "Token has been expired or revoked.")
			return
		}
		next := ""
		if f.rotate {
			delete(f.refresh, old)
			next = f.issue("refresh")
			f.refresh[next] = user
		}
		f.grant(w, user, next, "")
	default:
		f.refuse(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (f *fakeOAuth) refuse(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

// grant answers with a new access token and an ID token, and refresh when
// it is set. f.mu is held.
func (f *fakeOAuth) grant(w http.ResponseWriter, user, refresh, scope string) {
	access := f.issue("access")
	f.access[access] = fakeAccess{user: user, expires: f.now().Add(f.life)}
	claims := map[string]any{"iss": f.server.URL, "aud": f.clientID, "sub": "1234", "email": user, "email_verified": true}
	if f.claims != nil {
		f.claims(claims)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	answer := map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(f.life / time.Second),
		"id_token": "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".c2lnbmF0dXJl",
	}
	if refresh != "" {
		answer["refresh_token"] = refresh
	}
	if scope != "" {
		answer["scope"] = scope
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// valid reports whether token is an access token of user's that has not
// expired.
func (f *fakeOAuth) valid(user, token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.access[token]
	return ok && strings.EqualFold(a.user, user) && f.now().Before(a.expires)
}

// revoke withdraws the user's grant, as the user may at the provider:
// none of their tokens works any more.
func (f *fakeOAuth) revoke(user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for token, u := range f.refresh {
		if strings.EqualFold(u, user) {
			delete(f.refresh, token)
		}
	}
	for token, a := range f.access {
		if strings.EqualFold(a.user, user) {
			delete(f.access, token)
		}
	}
}

// dropAccess has an access token stop working before it expires, as a
// provider's may.
func (f *fakeOAuth) dropAccess(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.access, token)
}

// lastAccess is the access token handed out last.
func (f *fakeOAuth) lastAccess() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.issued) - 1; i >= 0; i-- {
		if strings.HasPrefix(f.issued[i], "access-") {
			return f.issued[i]
		}
	}
	return ""
}

// liveRefresh reports whether a refresh token still works.
func (f *fakeOAuth) liveRefresh(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.refresh[token]
	return ok
}

// secrets is every code and token handed out, and the client's secret.
func (f *fakeOAuth) secrets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.issued...)
	if f.secret != "" {
		out = append(out, f.secret)
	}
	return out
}

// counts is how many codes and refresh tokens the token endpoint was
// given.
func (f *fakeOAuth) counts() (exchanges, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, f.refreshes
}

// lastAsked is the query of the last authorization request.
func (f *fakeOAuth) lastAsked() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asked) == 0 {
		return nil
	}
	return f.asked[len(f.asked)-1]
}

// visit is the browser's trip to the provider and back.
type visit struct {
	url    string
	status int
	page   string
	err    error
}

// prompt is the window's part of a sign-in, with the test for the
// browser: it opens the sign-in page at once, where the user is signed in
// already, and follows the provider back to the loopback listener.
func (f *fakeOAuth) prompt(done string) (OAuthPrompt, *visit) {
	v := &visit{}
	return OAuthPrompt{Done: done, Open: func(u string) {
		v.url = u
		v.status, v.page, v.err = f.browse(u)
	}}, v
}

// browse requests a page, follows its redirects and returns the page it
// ends on.
func (f *fakeOAuth) browse(u string) (int, string, error) {
	resp, err := f.server.Client().Get(u)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// xoauth2Server is the server side of XOAUTH2, as Gmail has it: a token
// it does not take gets a challenge that says why, and the exchange ends
// with refusal once the client has answered that with an empty response.
type xoauth2Server struct {
	valid   func(user, token string) bool
	login   func() error
	refusal error
	failed  bool
}

func (x *xoauth2Server) Next(response []byte) ([]byte, bool, error) {
	switch {
	case x.failed:
		if len(response) != 0 {
			return nil, true, errors.New("xoauth2: the answer to the error must be empty")
		}
		return nil, true, x.refusal
	case response == nil:
		return []byte{}, false, nil
	}
	user, token, ok := parseXOAUTH2(response)
	if !ok {
		return nil, true, x.refusal
	}
	if !x.valid(user, token) {
		x.failed = true
		return []byte(`{"status":"400","schemes":"Bearer","scope":"https://mail.google.com/"}`), false, nil
	}
	return nil, true, x.login()
}

// parseXOAUTH2 reads the user and the token of an XOAUTH2 message.
func parseXOAUTH2(b []byte) (user, token string, ok bool) {
	s, ok := strings.CutSuffix(string(b), "\x01\x01")
	if !ok {
		return "", "", false
	}
	for _, field := range strings.Split(s, "\x01") {
		if v, found := strings.CutPrefix(field, "user="); found {
			user = v
		}
		if v, found := strings.CutPrefix(field, "auth=Bearer "); found {
			token = v
		}
	}
	return user, token, user != "" && token != ""
}

// xoauthSession is a session of an IMAP server that takes XOAUTH2 and no
// password, as Outlook's.
type xoauthSession struct{ *fakeSession }

func (s *xoauthSession) Login(string, string) error { return imapserver.ErrAuthFailed }

func (s *xoauthSession) AuthenticateMechanisms() []string { return []string{xoauth2Mechanism} }

func (s *xoauthSession) Authenticate(mech string) (sasl.Server, error) {
	if mech != xoauth2Mechanism {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Unsupported mechanism"}
	}
	s.f.xoauthTries.Add(1)
	return &xoauth2Server{
		valid: s.f.xoauth,
		login: func() error { return s.fakeSession.SessionIMAP4rev2.Login("ann", "secret") },
		refusal: &imap.Error{
			Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed,
			Text: "Invalid credentials (Failure)",
		},
	}, nil
}

// startXOAUTH2SMTP starts a fake SMTP server that signs in by XOAUTH2 with
// the tokens valid holds valid, and takes no password, as Outlook's. The
// counter counts the exchanges.
func startXOAUTH2SMTP(t *testing.T, valid func(user, token string) bool, logs *logRecorder) (*fakeSMTP, *atomic.Int32) {
	t.Helper()
	tries := &atomic.Int32{}
	f := startFakeSMTPWith(t, func(srv *smtp.Server) {
		srv.Backend = xoauthSMTPBackend{inner: srv.Backend, valid: valid, tries: tries}
		srv.ErrorLog = logs
	})
	return f, tries
}

type xoauthSMTPBackend struct {
	inner smtp.Backend
	valid func(user, token string) bool
	tries *atomic.Int32
}

func (b xoauthSMTPBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	s, err := b.inner.NewSession(c)
	if err != nil {
		return nil, err
	}
	return &xoauthSMTPSession{smtpSession: s.(*smtpSession), valid: b.valid, tries: b.tries}, nil
}

type xoauthSMTPSession struct {
	*smtpSession
	valid func(user, token string) bool
	tries *atomic.Int32
}

func (s *xoauthSMTPSession) AuthMechanisms() []string { return []string{xoauth2Mechanism} }

func (s *xoauthSMTPSession) Auth(mech string) (sasl.Server, error) {
	if mech != xoauth2Mechanism {
		return nil, fmt.Errorf("unsupported mechanism %q", mech)
	}
	s.tries.Add(1)
	return &xoauth2Server{
		valid: s.valid,
		login: func() error { s.auth = true; return nil },
		refusal: &smtp.SMTPError{
			Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8},
			Message: "Username and Password not accepted",
		},
	}, nil
}

// oauthRig is a service with an OAuth provider and IMAP and SMTP servers,
// all fake, the mail servers signing in by XOAUTH2 with the provider's
// access tokens alone.
type oauthRig struct {
	t         *testing.T
	ctx       context.Context
	provider  string
	svc       *Service
	store     Store
	secrets   *memSecrets
	clock     *testClock
	oauth     *fakeOAuth
	imap      *fakeIMAP
	smtp      *fakeSMTP
	smtpTries *atomic.Int32
	logs      *logRecorder
	// imapRefuses and smtpRefuses have a server refuse every token.
	imapRefuses, smtpRefuses atomic.Bool

	mu sync.Mutex
	// seen is the errors the service returned, which must name no token.
	seen []string
}

func newOAuthRig(t *testing.T, provider string) (*oauthRig, *memStore) {
	t.Helper()
	store := newMemStore()
	return newOAuthRigOn(t, provider, store), store
}

// newOAuthRigOn makes a rig whose service caches in store.
func newOAuthRigOn(t *testing.T, provider string, store Store) *oauthRig {
	t.Helper()
	r := &oauthRig{t: t, ctx: context.Background(), provider: provider, store: store, clock: newTestClock()}
	r.logs = captureLogs(t)
	r.oauth = startFakeOAuth(t, provider, r.clock.Now, r.logs)
	r.imap = startFakeIMAPWith(t, allCaps(), func(user, token string) bool {
		return !r.imapRefuses.Load() && r.oauth.valid(user, token)
	})
	r.imap.create(t, "INBOX", "Sent")
	r.smtp, r.smtpTries = startXOAUTH2SMTP(t, func(user, token string) bool {
		return !r.smtpRefuses.Load() && r.oauth.valid(user, token)
	}, r.logs)
	r.secrets = newMemSecrets()
	r.svc = NewService(store, r.secrets, t.TempDir())
	r.svc.SetTLSConfig(insecureTLS())
	r.svc.SetClock(r.clock.Now)
	r.svc.SetOAuth(provider, r.oauth.endpoints(), r.oauth.server.Client())
	t.Cleanup(r.checkSecrets)
	return r
}

// signIn adds the account the provider's user signs in as, and points it
// at the fake mail servers: the provider's presets name the real ones.
func (r *oauthRig) signIn(name string) Account {
	r.t.Helper()
	prompt, v := r.oauth.prompt("Return to Shu")
	a, err := r.svc.AddOAuthAccount(r.ctx, r.provider, name, prompt)
	if err != nil {
		r.t.Fatalf("sign in: %v", r.saw(err))
	}
	if v.err != nil || v.status != http.StatusOK || !strings.Contains(v.page, "Return to Shu") {
		r.t.Fatalf("the browser came back to %d %q, %v", v.status, v.page, v.err)
	}
	return r.pointAtFakes(a)
}

// pointAtFakes has an account reach the fake mail servers.
func (r *oauthRig) pointAtFakes(a Account) Account {
	r.t.Helper()
	host, port := r.imap.hostPort(r.t)
	a.IMAP = Server{Host: host, Port: port, TLS: TLSStartTLS}
	host, smtpPort, err := net.SplitHostPort(r.smtp.addr)
	if err != nil {
		r.t.Fatal(err)
	}
	a.SMTP = Server{Host: host, Port: mustPort(r.t, smtpPort), TLS: TLSStartTLS}
	if err := r.store.SaveAccount(r.ctx, a); err != nil {
		r.t.Fatal(err)
	}
	return a
}

// saw notes an error of the service's, which must name no token, and
// returns it.
func (r *oauthRig) saw(err error) error {
	if err != nil {
		r.mu.Lock()
		r.seen = append(r.seen, err.Error())
		r.mu.Unlock()
	}
	return err
}

// checkSecrets fails the test if a code or a token of the provider, or
// its client secret, is in anything the test saw: an error the service
// returned, what was logged, and the cache when it is a memStore.
func (r *oauthRig) checkSecrets() {
	r.mu.Lock()
	texts := map[string]string{
		"an error":              strings.Join(r.seen, "\n"),
		"the log":               r.logs.String(),
		"the IMAP server's log": r.imap.log.text(),
	}
	r.mu.Unlock()
	if m, ok := r.store.(*memStore); ok {
		texts["the cache"] = m.dump()
	}
	for _, secret := range r.oauth.secrets() {
		for where, text := range texts {
			if strings.Contains(text, secret) {
				kind, _, _ := strings.Cut(secret, "-")
				r.t.Errorf("%s holds a %s of the provider's", where, kind)
			}
		}
	}
}

// refreshToken is what the keychain holds for an account, "" for nothing.
func (r *oauthRig) refreshToken(accountID string) string {
	b, err := r.secrets.Get(tokensKey(accountID))
	if err != nil {
		return ""
	}
	return string(b)
}

// dump is everything the cache holds, as text.
func (s *memStore) dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "%+v\n%+v\n%+v\n%+v\n%+v\n", s.accounts, s.folders, s.messages, s.drafts, s.kv)
	for _, p := range s.outbox {
		fmt.Fprintf(&b, "%s %+v %s %s\n", p.ID, p.Envelope, p.Raw, p.LastError)
	}
	return b.String()
}

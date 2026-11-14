package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-sasl"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// localOAuth is a provider's sign-in page and token endpoint, as
// Google's, over TLS on 127.0.0.1. It checks what a provider checks: the
// client and its secret, a redirect to a loopback port, the PKCE
// verifier of the challenge, and a code used once. The local IMAP server
// takes the access tokens it holds valid.
type localOAuth struct {
	server           *httptest.Server
	clientID, secret string

	mu sync.Mutex
	// user is the address the browser is signed in as.
	user    string
	codes   map[string]localCode
	access  map[string]string
	refresh map[string]string
	// issued is every code and token handed out, asked the query of each
	// sign-in page, and problems what requests did wrong.
	issued   []string
	asked    []url.Values
	problems []string
}

type localCode struct{ challenge, redirect, user, scope string }

func startLocalOAuth(t *testing.T) *localOAuth {
	t.Helper()
	o := &localOAuth{
		clientID: "shu-test.apps.example", secret: "secret-" + rand.Text(), user: "ann@gmail.com",
		codes: map[string]localCode{}, access: map[string]string{}, refresh: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", o.authorize)
	mux.HandleFunc("POST /token", o.token)
	o.server = httptest.NewUnstartedServer(mux)
	o.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	o.server.StartTLS()
	t.Cleanup(func() {
		o.server.Close()
		o.mu.Lock()
		defer o.mu.Unlock()
		for _, p := range o.problems {
			t.Errorf("provider: %s", p)
		}
	})
	return o
}

// endpoints are the provider's, for SetOAuth.
func (o *localOAuth) endpoints() mail.OAuthEndpoints {
	return mail.OAuthEndpoints{
		AuthURL: o.server.URL + "/authorize", TokenURL: o.server.URL + "/token",
		ClientID: o.clientID, ClientSecret: o.secret,
	}
}

// issue makes a code or a token, and remembers it. o.mu is held.
func (o *localOAuth) issue(kind string) string {
	s := kind + "-" + rand.Text() + rand.Text()
	o.issued = append(o.issued, s)
	return s
}

func (o *localOAuth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.asked = append(o.asked, q)
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Scheme != "http" || back.Hostname() != "127.0.0.1" || back.Port() == "" ||
		q.Get("client_id") != o.clientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		o.problems = append(o.problems, fmt.Sprintf("a bad sign-in request: %v", q))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := o.issue("code")
	o.codes[code] = localCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), user: o.user, scope: q.Get("scope")}
	back.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (o *localOAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	o.mu.Lock()
	defer o.mu.Unlock()
	if form.Get("client_id") != o.clientID || form.Get("client_secret") != o.secret {
		o.problems = append(o.problems, "a token request from another client")
		o.refuse(w, "invalid_client", "Unknown client.")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		code, ok := o.codes[form.Get("code")]
		delete(o.codes, form.Get("code"))
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || code.redirect != form.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			o.problems = append(o.problems, "a code redeemed without its redirect or verifier, or twice")
			o.refuse(w, "invalid_grant", "Bad code.")
			return
		}
		refresh := o.issue("refresh")
		o.refresh[refresh] = code.user
		o.grant(w, code.user, refresh, code.scope)
	case "refresh_token":
		user, ok := o.refresh[form.Get("refresh_token")]
		if !ok {
			o.refuse(w, "invalid_grant", "Token has been expired or revoked.")
			return
		}
		o.grant(w, user, "", "")
	default:
		o.refuse(w, "unsupported_grant_type", "")
	}
}

// refuse answers a token request with an OAuth error. o.mu is held.
func (o *localOAuth) refuse(w http.ResponseWriter, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

// grant answers with a new access token and an ID token naming user,
// and refresh when it is set. o.mu is held.
func (o *localOAuth) grant(w http.ResponseWriter, user, refresh, scope string) {
	access := o.issue("access")
	o.access[access] = user
	claims, err := json.Marshal(map[string]any{"iss": o.server.URL, "aud": o.clientID, "sub": "1", "email": user, "email_verified": true})
	if err != nil {
		panic(err)
	}
	answer := map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": 3600,
		"id_token": "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".c2lnbmF0dXJl",
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

// valid reports whether token is an access token of user's.
func (o *localOAuth) valid(user, token string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	owner, ok := o.access[token]
	return ok && strings.EqualFold(owner, user)
}

// revoke withdraws the user's grant, as the user may at the provider:
// none of their tokens works any more.
func (o *localOAuth) revoke(user string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for token, owner := range o.refresh {
		if strings.EqualFold(owner, user) {
			delete(o.refresh, token)
		}
	}
	for token, owner := range o.access {
		if strings.EqualFold(owner, user) {
			delete(o.access, token)
		}
	}
}

// secrets are every code and token handed out, and the client's secret.
func (o *localOAuth) secrets() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append(slices.Clone(o.issued), o.secret)
}

// lastAsked is the query of the last sign-in page.
func (o *localOAuth) lastAsked() url.Values {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.asked) == 0 {
		return nil
	}
	return o.asked[len(o.asked)-1]
}

// visit is the browser's trip to the provider and back to Shu.
type visit struct {
	status int
	page   string
	err    error
}

// browse is the browser, where the user is signed in at the provider
// already: it opens the sign-in page and follows the provider back to
// Shu's listener. A trip that fails tells the sign-in, as a provider
// tells it of an error, so that it ends rather than wait out its time.
func (o *localOAuth) browse(page string) visit {
	var v visit
	resp, err := o.server.Client().Get(page)
	if err == nil {
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		v.status, v.page, v.err = resp.StatusCode, string(body), readErr
	} else {
		v.err = err
	}
	if v.err != nil || v.status != http.StatusOK {
		if u, err := url.Parse(page); err == nil {
			back, err := url.Parse(u.Query().Get("redirect_uri"))
			if err == nil {
				back.RawQuery = url.Values{"error": {"server_error"}, "state": {u.Query().Get("state")}}.Encode()
				if resp, err := http.Get(back.String()); err == nil {
					resp.Body.Close()
				}
			}
		}
	}
	return v
}

// xoauthSession is a session of the local IMAP server that signs in by
// XOAUTH2 with the access tokens valid holds valid, as Gmail's, and
// takes no password, as Outlook's.
type xoauthSession struct {
	promptSession
	valid func(user, token string) bool
}

func (s xoauthSession) Login(string, string) error { return imapserver.ErrAuthFailed }

func (s xoauthSession) AuthenticateMechanisms() []string { return []string{"XOAUTH2"} }

func (s xoauthSession) Authenticate(mech string) (sasl.Server, error) {
	if mech != "XOAUTH2" {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Unsupported mechanism"}
	}
	return &xoauth2Login{valid: s.valid, login: func() error { return s.SessionIMAP4rev2.Login("ann", "secret") }}, nil
}

// xoauth2Login is the server's side of XOAUTH2, as Gmail has it: a token
// it does not take gets a challenge saying why, and the exchange ends in
// refusal once the client has answered that.
type xoauth2Login struct {
	valid  func(user, token string) bool
	login  func() error
	failed bool
}

func (x *xoauth2Login) Next(response []byte) ([]byte, bool, error) {
	refusal := &imap.Error{
		Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: "Invalid credentials (Failure)",
	}
	switch {
	case x.failed:
		return nil, true, refusal
	case response == nil:
		return []byte{}, false, nil
	}
	user, token, ok := readXOAUTH2(response)
	if !ok {
		return nil, true, refusal
	}
	if !x.valid(user, token) {
		x.failed = true
		return []byte(`{"status":"400","schemes":"Bearer","scope":"https://mail.google.com/"}`), false, nil
	}
	return nil, true, x.login()
}

// readXOAUTH2 reads the user and the token of an XOAUTH2 message.
func readXOAUTH2(b []byte) (user, token string, ok bool) {
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

// usePreset has a provider's preset name the local servers for the
// test: an account that signs in with OAuth takes its provider's
// servers, which must never be the real ones in a test.
func usePreset(t *testing.T, id, imapAddr, smtpAddr string) {
	t.Helper()
	saved := mail.Providers
	t.Cleanup(func() { mail.Providers = saved })
	mail.Providers = slices.Clone(saved)
	for i := range mail.Providers {
		if mail.Providers[i].ID != id {
			continue
		}
		imapHost, imapPort := hostPort(t, imapAddr)
		smtpHost, smtpPort := hostPort(t, smtpAddr)
		mail.Providers[i].IMAP = mail.Server{Host: imapHost, Port: imapPort, TLS: mail.TLSStartTLS}
		mail.Providers[i].SMTP = mail.Server{Host: smtpHost, Port: smtpPort, TLS: mail.TLSStartTLS}
		return
	}
	t.Fatalf("no preset %q", id)
}

// lockedBuffer is a log the servers' goroutines may write to.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The acceptance criteria of signing in with a provider end to end: the
// window on the real engine and a database on disk, against a provider
// and an IMAP server that takes its tokens and no password, all on
// 127.0.0.1. The provider's button adds the account and syncs its inbox;
// the provider revoking the sign-in leaves the account needing one, with
// its cache whole and readable, also after a restart; signing in again
// brings the mail that came meanwhile. No code or token shows in the
// window, nor goes into the database or the log.
func TestOAuthEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	logs := &lockedBuffer{}
	stdLog := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(stdLog) })

	provider := startLocalOAuth(t)
	imapServer := startLocalIMAPWith(t, provider.valid, "INBOX", "Sent", "Trash")
	smtpServer := startLocalSMTP(t)
	usePreset(t, "gmail", imapServer.addr, smtpServer.addr)
	base := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	delivered := 0
	deliver := func(subject string) {
		t.Helper()
		delivered++
		raw := "From: Bo <bo@example.com>\r\nTo: ann@gmail.com\r\nSubject: " + subject +
			"\r\nDate: " + base.Add(time.Duration(delivered)*time.Minute).Format(time.RFC1123Z) +
			"\r\nMessage-ID: <" + strings.ReplaceAll(subject, " ", "-") + "@example.com>\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\nThe text of " + subject + ".\r\n"
		if _, err := imapServer.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	deliver("Welcome to your inbox")

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := memKeychain{}
	svc := mail.NewService(db, keys, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	svc.SetOAuth(mail.OAuthGoogle, provider.endpoints(), provider.server.Client())
	listing := mail.DefaultSettings()
	listing.Conversations = false
	if err := svc.SaveSettings(ctx, listing); err != nil {
		t.Fatal(err)
	}

	// The browser runs beside the sign-in, which waits for it.
	visits := make(chan visit, 4)
	var opened []string
	launch := func() (*app, *ui.Tester) {
		a := newTestApp(svc)
		a.setLocale = func(string) {}
		a.openBrowser = func(page string) error {
			opened = append(opened, page)
			go func() { visits <- provider.browse(page) }()
			return nil
		}
		a.start()
		return a, ui.NewTester(a.view, 1220, 900)
	}
	came := func(what string) {
		t.Helper()
		select {
		case v := <-visits:
			if v.err != nil || v.status != http.StatusOK || !strings.Contains(v.page, html.EscapeString(locales.SignInDonePage())) {
				t.Fatalf("%s: the browser came back to %d %q, %v", what, v.status, v.page, v.err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: the browser never came back", what)
		}
	}
	// seen is everything the window said, which must hold no secret.
	var seen []string
	look := func(a *app, tt *ui.Tester) {
		tt.Frame()
		seen = append(seen, tt.Texts()...)
		seen = append(seen, a.err, a.accountErr, a.accountNote, a.bodyErr)
		if f := a.signInErr; f != nil {
			seen = append(seen, f.text, f.detail)
		}
	}

	// A build with Google's client offers Google, and not Microsoft.
	a, tt := launch()
	if !tt.HasText(locales.SignInWithGoogle()) || tt.HasText(locales.SignInWithMicrosoft()) {
		t.Fatalf("the add view: %q", tt.Texts())
	}
	typeIn(t, tt, locales.OauthName(), "Ann Lee")
	click(t, tt, locales.SignInWithGoogle())
	came("adding the account")
	look(a, tt)
	accounts, active, err := svc.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	acc := accounts[0]
	if acc.OAuth != mail.OAuthGoogle || acc.Email != "ann@gmail.com" || acc.Name != "Ann Lee" || active != acc.ID {
		t.Fatalf("added %+v, active %q", acc, active)
	}
	if q := provider.lastAsked(); q.Get("login_hint") != "" || !strings.Contains(q.Get("prompt"), "consent") || len(opened) != 1 {
		t.Errorf("the sign-in page asked %v, opened %d times", q, len(opened))
	}
	if a.active != acc.ID || a.showAccounts || a.err != "" {
		t.Fatalf("after adding: active %q, accounts view %v, error %q", a.active, a.showAccounts, a.err)
	}
	if !tt.HasText("Welcome to your inbox") || !tt.HasText("The text of Welcome to your inbox.") {
		t.Fatalf("the inbox did not sync: %q", tt.Texts())
	}

	// Mail that comes is synced with the tokens.
	deliver("Second message")
	click(t, tt, locales.Sync())
	look(a, tt)
	if !tt.HasText("Second message") || a.err != "" {
		t.Fatalf("after a sync: %q, %q", a.err, tt.Texts())
	}

	// The provider revokes the sign-in: the next sync finds it out, and
	// the account needs a sign-in, its cache as it was.
	provider.revoke("ann@gmail.com")
	deliver("Arrived while signed out")
	click(t, tt, locales.Sync())
	look(a, tt)
	for _, text := range []string{locales.SignInNeeded("Ann Lee"), locales.SignInAgain(), "Welcome to your inbox", "Second message"} {
		if !tt.HasText(text) {
			t.Errorf("after the revocation the window lacks %q: %q", text, tt.Texts())
		}
	}
	if tt.HasText("Arrived while signed out") || a.err != "" {
		t.Errorf("after the revocation: %q, %q", a.err, tt.Texts())
	}
	noRawTexts(t, tt, "after the revocation")
	if need, err := svc.NeedsSignIn(ctx, acc.ID); !need || err != nil {
		t.Errorf("needs sign-in = %v, %v", need, err)
	}
	if len(keys) != 0 {
		t.Errorf("the keychain keeps %d entries of a revoked sign-in", len(keys))
	}
	if got, _, _ := svc.Accounts(ctx); len(got) != 1 || got[0] != acc {
		t.Errorf("the account changed: %+v", got)
	}

	// The app starts again: the account says it needs a sign-in, and its
	// cached mail reads from the database.
	a, tt = launch()
	look(a, tt)
	if !tt.HasText(locales.SignInNeeded("Ann Lee")) || a.err != "" {
		t.Fatalf("after a restart: %q, %q", a.err, tt.Texts())
	}
	click(t, tt, "Welcome to your inbox")
	look(a, tt)
	if !tt.HasText("The text of Welcome to your inbox.") {
		t.Errorf("a cached message cannot be read: %q", tt.Texts())
	}

	// Signing in again, as the account's address, brings the mail that
	// came meanwhile.
	click(t, tt, locales.SignInAgainTo("Ann Lee"))
	came("signing in again")
	look(a, tt)
	if q := provider.lastAsked(); q.Get("login_hint") != "ann@gmail.com" {
		t.Errorf("signing in again asked %v", q)
	}
	if a.needSignIn[acc.ID] || tt.HasText(locales.SignInNeeded("Ann Lee")) || a.err != "" {
		t.Fatalf("signed in again: needs %v, error %q: %q", a.needSignIn, a.err, tt.Texts())
	}
	if !tt.HasText("Arrived while signed out") {
		t.Errorf("the mail that came meanwhile is not there: %q", tt.Texts())
	}
	if need, err := svc.NeedsSignIn(ctx, acc.ID); need || err != nil || len(keys) != 1 {
		t.Errorf("needs sign-in = %v, %v; keychain entries %d", need, err, len(keys))
	}

	// No code or token anywhere: in what the window said, in the log, in
	// the database.
	files, err := filepath.Glob(filepath.Join(dir, "shu.db*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("database files %v, %v", files, err)
	}
	where := map[string]string{"the window": strings.Join(seen, "\n"), "the log": logs.String()}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		where[filepath.Base(f)] = string(b)
	}
	secrets := provider.secrets()
	if len(secrets) < 6 {
		t.Fatalf("the provider handed out %d secrets", len(secrets))
	}
	for _, secret := range secrets {
		for place, text := range where {
			if strings.Contains(text, secret) {
				kind, _, _ := strings.Cut(secret, "-")
				t.Errorf("%s holds a %s of the provider's", place, kind)
			}
		}
	}
}

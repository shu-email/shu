package mail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// The providers an account can sign in with through OAuth, by the names
// Account.OAuth keeps.
const (
	OAuthGoogle    = "google"
	OAuthMicrosoft = "microsoft"
)

// The clients Shu is registered as with each provider (docs/oauth.md).
// They are empty in the repository, and a build that has them sets them:
//
//	MYGO_LDFLAGS='-X shu/mail.GoogleClientID=… -X shu/mail.GoogleClientSecret=… -X shu/mail.MicrosoftClientID=…' go tool mygo build
//
// A provider whose client a build lacks is not offered
// (Service.OAuthConfigured). None of them is a secret: an app installed
// on the user's computer cannot keep one (RFC 8252, section 8.5). Google
// issues its desktop clients a "secret" all the same, and wants it with
// every token request.
var (
	GoogleClientID     string
	GoogleClientSecret string
	MicrosoftClientID  string
)

// The errors of signing in that the window tells apart, with errors.Is.
var (
	// ErrSignInRequired is an account that signs in with OAuth and has to
	// sign in again (Service.SignInAgain) before it reaches its servers:
	// the provider revoked its sign-in or let it expire, or the user
	// signed it out. Its cache and settings are as they were, and its
	// cached mail can be read.
	ErrSignInRequired = errors.New("mail: the account has to sign in again")
	// ErrSignInDenied is the user declining, at the provider, to let Shu
	// at their mail.
	ErrSignInDenied = errors.New("mail: the sign-in was declined")
	// ErrSignInTimeout is a sign-in the browser did not come back from
	// in time.
	ErrSignInTimeout = errors.New("mail: the sign-in was not finished in time")
	// ErrAddressMismatch is signing an account in again as another
	// address than its own.
	ErrAddressMismatch = errors.New("mail: the sign-in is of another address than the account's")
	// ErrOAuthNotConfigured is a provider this build has no client for.
	ErrOAuthNotConfigured = errors.New("mail: this build cannot sign in with the provider")
)

// OAuthError is a provider refusing a sign-in or a token, in its own
// terms: the error code of OAuth (RFC 6749, sections 4.1.2.1 and 5.2),
// as "invalid_grant", and the description it gave. It holds no request
// or answer whole, and its description none of the secrets of the
// request it answers.
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description == "" {
		return "oauth: " + e.Code
	}
	return "oauth: " + e.Code + ": " + e.Description
}

// Is makes the user's refusal at the provider an ErrSignInDenied.
func (e *OAuthError) Is(target error) bool {
	return target == ErrSignInDenied && e.Code == "access_denied"
}

// final reports whether a refresh failed for good: the grant is revoked
// or expired, or Microsoft wants the user to take a step (a second
// factor, a consent) that only signing in again can.
func (e *OAuthError) final() bool {
	switch e.Code {
	case "invalid_grant", "interaction_required", "consent_required":
		return true
	}
	return false
}

// OAuthPrompt is how a sign-in reaches the user, which only the window
// can do. Open opens the provider's page in the system browser; it is
// called once, before the sign-in waits, from the goroutine signing in,
// and must not block. Done is what the page the browser comes back to
// says, in the user's language: that they can return to Shu.
type OAuthPrompt struct {
	Open func(url string)
	Done string
}

// OAuthEndpoints is where a provider signs accounts in, and the client
// Shu is there: what SetOAuth points a provider at in a test.
type OAuthEndpoints struct {
	AuthURL, TokenURL      string
	ClientID, ClientSecret string
}

const (
	// signInTimeout is how long a sign-in waits for the browser to come
	// back: time to type a password and find the phone of a second step.
	signInTimeout = 5 * time.Minute
	// tokenMargin is how long before it expires an access token is
	// renewed, so that no connection signs in with one about to expire.
	tokenMargin = 5 * time.Minute
	// defaultTokenLife is the life of an access token the provider gave
	// none for: the hour Google's and Microsoft's last.
	defaultTokenLife = time.Hour
)

// oauthProvider is how Shu signs in with a provider: where, as which
// client, for what, and the mail provider whose servers its accounts use.
type oauthProvider struct {
	authURL, tokenURL      string
	clientID, clientSecret string
	// needsSecret is set for a provider that refuses a client without
	// its secret.
	needsSecret bool
	scopes      []string
	// mailScope is the scope a sign-in is of no use without, for a
	// provider that lets the user grant some of the scopes asked for.
	mailScope string
	// params go with every authorization request; prompt with that of a
	// new account and againPrompt with that of a sign-in again.
	params              map[string]string
	prompt, againPrompt string
	// preset is the mail provider (Providers) whose IMAP and SMTP
	// servers the accounts use.
	preset string
}

// defaultOAuthProviders are the providers as a build has them.
func defaultOAuthProviders() map[string]oauthProvider {
	return map[string]oauthProvider{
		OAuthGoogle: {
			authURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			tokenURL: "https://oauth2.googleapis.com/token",
			clientID: GoogleClientID, clientSecret: GoogleClientSecret, needsSecret: true,
			// Gmail's IMAP and SMTP take no narrower scope than the whole
			// mailbox; openid and email name the address.
			scopes: []string{"https://mail.google.com/", "openid", "email"},
			// Google's consent screen lets the user untick the mail.
			mailScope: "https://mail.google.com/",
			// Google gives a refresh token only to an offline request,
			// and only when it asked the user to consent. Shu keeps no
			// grant across signing out or removing an account, so it asks
			// every time rather than get no refresh token the second.
			params:      map[string]string{"access_type": "offline"},
			prompt:      "select_account consent",
			againPrompt: "consent",
			preset:      "gmail",
		},
		OAuthMicrosoft: {
			authURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			tokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			clientID: MicrosoftClientID,
			// IMAP and SMTP of Outlook.com and Microsoft 365, a refresh
			// token (offline_access), and the address.
			scopes: []string{
				"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send",
				"offline_access", "openid", "email",
			},
			// A new account is picked, not taken from the browser's
			// session.
			prompt: "select_account",
			preset: "outlook",
		},
	}
}

// configured reports whether a build has the client the provider needs.
func (p oauthProvider) configured() bool {
	return p.clientID != "" && (!p.needsSecret || p.clientSecret != "")
}

// check refuses an endpoint that is not https: the code, the tokens and
// the client travel to them.
func (p oauthProvider) check() error {
	for _, endpoint := range []string{p.authURL, p.tokenURL} {
		if u, err := url.Parse(endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("oauth: the endpoint %q is not https", endpoint)
		}
	}
	return nil
}

// OAuthConfigured reports whether this build can sign in with a
// provider: whether it was built with Shu's client there. The window
// offers the others no button.
func (s *Service) OAuthConfigured(provider string) bool {
	p, ok := s.oauthProviders[provider]
	return ok && p.configured()
}

// SetOAuth points a provider at other endpoints and another client,
// which client reaches, for tests against a fake provider. The endpoints
// must still be https.
func (s *Service) SetOAuth(provider string, e OAuthEndpoints, client *http.Client) {
	p, ok := s.oauthProviders[provider]
	if !ok {
		return
	}
	p.authURL, p.tokenURL, p.clientID, p.clientSecret = e.AuthURL, e.TokenURL, e.ClientID, e.ClientSecret
	s.oauthProviders[provider] = p
	if client != nil {
		s.httpClient = client
	}
}

// newOAuthClient is the HTTP client of the token endpoints. It follows no
// redirect: a request that carries a code or a refresh token goes where
// it was sent or nowhere.
func newOAuthClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// oauthGrant is what a token endpoint gave for a code or a refresh token.
type oauthGrant struct {
	access, refresh, idToken, scope string
	// renewAt is when the access token is to be renewed.
	renewAt time.Time
}

// authorize signs a user in with a provider in the system browser, by the
// authorization code flow for native apps of RFC 8252: a listener on the
// loopback address, at a port the system picks, takes the browser's way
// back from the provider, and with it a code that is worth something only
// with the PKCE verifier (RFC 7636), which never leaves Shu. Of the
// requests it gets, one counts: the first that carries the sign-in's
// state. It returns what the code was redeemed for, and the address the
// ID token names. hint is the address to sign in as, "" for a new
// account.
func (s *Service) authorize(ctx context.Context, provider, hint string, prompt OAuthPrompt) (oauthGrant, string, error) {
	p, ok := s.oauthProviders[provider]
	switch {
	case !ok:
		return oauthGrant{}, "", fmt.Errorf("mail: signing in with %q is not supported", provider)
	case !p.configured():
		return oauthGrant{}, "", ErrOAuthNotConfigured
	case prompt.Open == nil:
		return oauthGrant{}, "", errors.New("mail: a sign-in needs a way to open the browser")
	}
	if err := p.check(); err != nil {
		return oauthGrant{}, "", err
	}
	if err := ctx.Err(); err != nil {
		return oauthGrant{}, "", err
	}
	// rand.Text is 26 characters of base32, which PKCE allows; a
	// verifier takes at least 43.
	verifier, state := rand.Text()+rand.Text(), rand.Text()
	// The loopback address itself, not "localhost", which a system may
	// resolve elsewhere (RFC 8252, section 8.3), and never every address.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return oauthGrant{}, "", fmt.Errorf("oauth: listen for the browser: %w", err)
	}
	redirect := "http://" + ln.Addr().String()
	answers := make(chan url.Values, 1)
	srv := &http.Server{
		Handler:           callbackHandler(state, donePage(prompt.Done), answers),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(ln) }()
	defer closeServer(srv, ln)

	page, _ := url.Parse(p.authURL)
	q := page.Query()
	for k, v := range p.params {
		q.Set(k, v)
	}
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	ask := p.prompt
	if hint != "" {
		ask = p.againPrompt
		q.Set("login_hint", hint)
	}
	if ask != "" {
		q.Set("prompt", ask)
	}
	page.RawQuery = q.Encode()
	prompt.Open(page.String())

	timer, stop := s.newTimer(signInTimeout)
	defer stop()
	var answer url.Values
	select {
	case answer = <-answers:
	case <-timer:
		return oauthGrant{}, "", ErrSignInTimeout
	case <-ctx.Done():
		return oauthGrant{}, "", ctx.Err()
	}
	if code := answer.Get("error"); code != "" {
		return oauthGrant{}, "", &OAuthError{Code: cleanText(code, 64), Description: cleanText(answer.Get("error_description"), 300)}
	}
	code := answer.Get("code")
	if code == "" {
		return oauthGrant{}, "", errors.New("oauth: the provider sent the browser back without a code")
	}
	g, err := s.redeem(ctx, p, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
	if err != nil {
		return oauthGrant{}, "", err
	}
	if g.refresh == "" {
		return oauthGrant{}, "", errors.New("oauth: the provider gave no refresh token")
	}
	if p.mailScope != "" && g.scope != "" && !slices.Contains(strings.Fields(g.scope), p.mailScope) {
		return oauthGrant{}, "", fmt.Errorf("%w: the mail was not granted", ErrSignInDenied)
	}
	claims, err := parseIDToken(g.idToken)
	if err != nil {
		return oauthGrant{}, "", err
	}
	if !slices.Contains(claims.Audience, p.clientID) {
		return oauthGrant{}, "", errors.New("oauth: the ID token is not for Shu")
	}
	address, err := claims.address(provider)
	if err != nil {
		return oauthGrant{}, "", err
	}
	return g, address, nil
}

// callbackHandler answers the browser's way back from the provider. The
// first request with the sign-in's state is the answer; any other gets a
// bare refusal, so that nothing else on the computer can end a sign-in
// or answer it twice. The page is on its way to the browser before the
// answer is handed on, as the sign-in closes the listener once it ends.
func callbackHandler(state string, page []byte, answers chan<- url.Values) http.Handler {
	var taken atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/" ||
			subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || !taken.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(len(page)))
		// The address of this page holds the code: nothing it loads may
		// be told it, and nothing may keep it.
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Connection", "close")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		answers <- q
	})
}

// donePage is the page the browser comes back to.
func donePage(text string) []byte {
	if strings.TrimSpace(text) == "" {
		text = "You can close this page and return to Shu."
	}
	return []byte(`<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width"><title>Shu</title></head>` +
		`<body style="font:16px/1.5 system-ui,sans-serif;margin:4em auto;max-width:32em;padding:0 1em;text-align:center">` +
		`<p>` + html.EscapeString(text) + `</p></body></html>`)
}

// closeServer ends a sign-in's listener: at once for new connections, and
// for the page still on its way after a moment at most. It closes ln
// itself as well: Shutdown closes only a listener Serve has begun to
// serve, and a sign-in cancelled at once can end before the goroutine
// running Serve has.
func closeServer(srv *http.Server, ln net.Listener) {
	_ = ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if srv.Shutdown(ctx) != nil {
		srv.Close()
	}
}

// pkceChallenge is the S256 challenge of a PKCE verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// tokenAnswer is a token endpoint's answer (RFC 6749, sections 5.1 and
// 5.2), with an ID token (OpenID Connect).
type tokenAnswer struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	Scope        string          `json:"scope"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Error        string          `json:"error"`
	Description  string          `json:"error_description"`
}

// redeem asks a provider's token endpoint for tokens, for a code (RFC
// 6749, section 4.1.3) or a refresh token (section 6). Its errors hold an
// OAuth error's code and description, or the status of an answer that is
// none, and never a request or the body of an answer: those hold the
// secrets.
func (s *Service) redeem(ctx context.Context, p oauthProvider, form url.Values) (oauthGrant, error) {
	if err := p.check(); err != nil {
		return oauthGrant{}, err
	}
	secrets := []string{form.Get("code"), form.Get("code_verifier"), form.Get("refresh_token"), p.clientSecret}
	form.Set("client_id", p.clientID)
	if p.clientSecret != "" {
		form.Set("client_secret", p.clientSecret)
	}
	start := s.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthGrant{}, fmt.Errorf("oauth: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return oauthGrant{}, fmt.Errorf("oauth: reach the token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauthGrant{}, fmt.Errorf("oauth: read the token endpoint's answer: %w", err)
	}
	var answer tokenAnswer
	if json.Unmarshal(body, &answer) != nil {
		answer = tokenAnswer{}
	}
	switch {
	case answer.Error != "":
		return oauthGrant{}, &OAuthError{
			Code:        cleanText(answer.Error, 64, secrets...),
			Description: cleanText(answer.Description, 300, secrets...),
		}
	case resp.StatusCode != http.StatusOK:
		return oauthGrant{}, fmt.Errorf("oauth: the token endpoint answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	case !isToken(answer.AccessToken) || (answer.RefreshToken != "" && !isToken(answer.RefreshToken)):
		return oauthGrant{}, errors.New("oauth: the token endpoint gave no usable token")
	}
	life := tokenLife(answer.ExpiresIn)
	return oauthGrant{
		access: answer.AccessToken, refresh: answer.RefreshToken,
		idToken: answer.IDToken, scope: answer.Scope,
		renewAt: start.Add(life - min(tokenMargin, life/2)),
	}, nil
}

// isToken reports whether s can be a token: printable ASCII without
// spaces, as bearer tokens are, and as XOAUTH2 and HTTP need.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// tokenLife reads expires_in, which some providers send as a string.
func tokenLife(raw json.RawMessage) time.Duration {
	n, err := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 64)
	if err != nil || n <= 0 {
		return defaultTokenLife
	}
	return time.Duration(min(n, 7*24*3600)) * time.Second
}

// cleanText makes a provider's words fit an error message: one line of
// printable text, of at most limit runes, without any of secrets.
func cleanText(s string, limit int, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "…")
		}
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
	}
	return s
}

// idClaims are the claims of an ID token that a sign-in reads.
type idClaims struct {
	Audience          audience        `json:"aud"`
	Email             string          `json:"email"`
	EmailVerified     json.RawMessage `json:"email_verified"`
	PreferredUsername string          `json:"preferred_username"`
}

// audience is the aud claim, one client or several.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// parseIDToken reads the claims of an ID token without checking its
// signature. It came straight from the provider's token endpoint over
// TLS, which OpenID Connect Core 1.0 (section 3.1.3.7) accepts in place
// of the signature as proof of who issued it.
func parseIDToken(token string) (idClaims, error) {
	bad := errors.New("oauth: the provider gave no valid ID token")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return idClaims{}, bad
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return idClaims{}, bad
	}
	var c idClaims
	if json.Unmarshal(payload, &c) != nil {
		return idClaims{}, bad
	}
	return c, nil
}

// address is the account's address an ID token names: its email, unless
// the provider says that is not verified, or for Microsoft the name the
// user signs in with, which is their address where a work account's
// token has no email.
func (c idClaims) address(provider string) (string, error) {
	addr := c.Email
	if strings.Trim(string(c.EmailVerified), `"`) == "false" {
		addr = ""
	}
	if addr == "" && provider == OAuthMicrosoft {
		addr = c.PreferredUsername
	}
	if parsed, err := mail.ParseAddress(addr); addr == "" || err != nil || parsed.Address != addr {
		return "", errors.New("oauth: the provider did not give the account's address")
	}
	return addr, nil
}

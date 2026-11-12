package mail

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// The challenge of RFC 7636, appendix B.
func TestPKCEChallenge(t *testing.T) {
	if got := pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge = %q", got)
	}
}

// A sign-in asks each provider for what it needs, in the browser, and
// redeems the code with the verifier and the redirect it was given for,
// which the fake provider checks; the address comes from the ID token.
func TestSignInAsksEachProviderForWhatItNeeds(t *testing.T) {
	for _, tc := range []struct {
		provider string
		scope    []string
		params   map[string]string
		preset   string
	}{
		{OAuthGoogle, []string{"https://mail.google.com/", "openid", "email"},
			map[string]string{"access_type": "offline", "prompt": "select_account consent"}, "gmail"},
		{OAuthMicrosoft, []string{"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send", "offline_access", "openid", "email"},
			map[string]string{"prompt": "select_account"}, "outlook"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			r, store := newOAuthRig(t, tc.provider)
			r.oauth.set(func(f *fakeOAuth) { f.user = "Ann.Lee@example.com" })
			prompt, v := r.oauth.prompt("Return to Shu")
			a, err := r.svc.AddOAuthAccount(r.ctx, tc.provider, " Ann Lee ", prompt)
			if err != nil {
				t.Fatal(r.saw(err))
			}
			q := r.oauth.lastAsked()
			if got := strings.Fields(q.Get("scope")); !slices.Equal(got, tc.scope) {
				t.Errorf("scope = %q, want %q", got, tc.scope)
			}
			for k, want := range tc.params {
				if got := q.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if q.Has("login_hint") {
				t.Errorf("a new account came with a login hint %q", q.Get("login_hint"))
			}
			redirect, _ := url.Parse(q.Get("redirect_uri"))
			if redirect == nil || redirect.Hostname() != "127.0.0.1" {
				t.Errorf("redirect = %q, want the loopback address", q.Get("redirect_uri"))
			}
			if v.status != http.StatusOK || !strings.Contains(v.page, "Return to Shu") {
				t.Errorf("the browser came back to %d %q", v.status, v.page)
			}
			preset, _ := ProviderByID(tc.preset)
			want := Account{
				ID: a.ID, Name: "Ann Lee", Email: "Ann.Lee@example.com", Username: "Ann.Lee@example.com",
				IMAP: preset.IMAP, SMTP: preset.SMTP, OAuth: tc.provider,
			}
			if a != want || a.ID == "" {
				t.Errorf("account = %+v, want %+v", a, want)
			}
			if stored, ok, _ := store.Account(r.ctx, a.ID); !ok || stored != want {
				t.Errorf("stored account = %+v, %v", stored, ok)
			}
			if active, _ := store.ActiveAccount(r.ctx); active != a.ID {
				t.Errorf("active account = %q", active)
			}
			if exchanges, refreshes := r.oauth.counts(); exchanges != 1 || refreshes != 0 {
				t.Errorf("token requests: %d codes, %d refreshes", exchanges, refreshes)
			}
			// The keychain has the refresh token and no password; the
			// listener is gone.
			if r.refreshToken(a.ID) == "" {
				t.Error("no refresh token in the keychain")
			}
			if _, err := r.secrets.Get(PasswordKey(a.ID)); !errors.Is(err, ErrNotFound) {
				t.Errorf("password entry: %v", err)
			}
			if conn, err := net.Dial("tcp", redirect.Host); err == nil {
				conn.Close()
				t.Error("the loopback listener is still open")
			}
		})
	}
}

// Declining at the provider is ErrSignInDenied, in the provider's words
// too, and leaves no account and nothing in the keychain.
func TestSignInDenied(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	r.oauth.set(func(f *fakeOAuth) { f.deny = true })
	prompt, v := r.oauth.prompt("Return to Shu")
	_, err := r.svc.AddOAuthAccount(r.ctx, OAuthGoogle, "", prompt)
	r.saw(err)
	if !errors.Is(err, ErrSignInDenied) {
		t.Fatalf("err = %v, want ErrSignInDenied", err)
	}
	var refusal *OAuthError
	if !errors.As(err, &refusal) || refusal.Code != "access_denied" || refusal.Description != "The user declined." {
		t.Errorf("refusal = %+v", refusal)
	}
	if v.status != http.StatusOK {
		t.Errorf("the browser came back to %d", v.status)
	}
	if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 {
		t.Errorf("accounts = %+v", accounts)
	}
	if len(r.secrets.data) != 0 {
		t.Errorf("the keychain holds %d entries", len(r.secrets.data))
	}
	if exchanges, _ := r.oauth.counts(); exchanges != 0 {
		t.Errorf("%d codes redeemed", exchanges)
	}
}

// Google's consent screen lets the user untick the mail: a sign-in
// without it is no use, and is refused as declined.
func TestSignInWithoutMailIsDenied(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	r.oauth.set(func(f *fakeOAuth) { f.scope = "openid https://www.googleapis.com/auth/userinfo.email" })
	prompt, _ := r.oauth.prompt("")
	if _, err := r.svc.AddOAuthAccount(r.ctx, OAuthGoogle, "", prompt); !errors.Is(r.saw(err), ErrSignInDenied) {
		t.Fatalf("err = %v, want ErrSignInDenied", err)
	}
	if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 || len(r.secrets.data) != 0 {
		t.Errorf("accounts = %+v, keychain = %d entries", accounts, len(r.secrets.data))
	}
}

// loopbackAt is the listener a sign-in page sends the browser back to,
// and the state it carries.
func loopbackAt(t *testing.T, authURL string) (redirect, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("redirect_uri"), u.Query().Get("state")
}

// get requests a page of the loopback listener as a browser would.
func get(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The listener takes the first request with the sign-in's state and no
// other: one with another state, at another path or of another method,
// or a second one, is refused, and the sign-in goes on with the answer it
// had.
func TestSignInTakesOneAnswerWithItsState(t *testing.T) {
	r, store := newOAuthRig(t, OAuthMicrosoft)
	var codes []int
	prompt := OAuthPrompt{Open: func(authURL string) {
		redirect, state := loopbackAt(t, authURL)
		codes = append(codes,
			get(t, redirect+"/?"+url.Values{"state": {"forged"}, "code": {"code-forged"}}.Encode()),
			get(t, redirect+"/other?"+url.Values{"state": {state}, "code": {"code-forged"}}.Encode()))
		resp, err := http.Post(redirect+"/?"+url.Values{"state": {state}, "code": {"code-forged"}}.Encode(), "text/plain", nil)
		if err == nil {
			resp.Body.Close()
			codes = append(codes, resp.StatusCode)
		}
		status, _, err := r.oauth.browse(authURL)
		if err != nil || status != http.StatusOK {
			t.Errorf("the sign-in came back to %d, %v", status, err)
		}
		codes = append(codes, get(t, redirect+"/?"+url.Values{"state": {state}, "code": {"code-forged"}}.Encode()))
	}}
	a, err := r.svc.AddOAuthAccount(r.ctx, OAuthMicrosoft, "", prompt)
	if err != nil {
		t.Fatal(r.saw(err))
	}
	if !slices.Equal(codes, []int{400, 400, 400, 400}) {
		t.Errorf("the other requests were answered %v", codes)
	}
	if got, ok, _ := store.Account(r.ctx, a.ID); !ok || got.Email != "ann@example.com" {
		t.Errorf("account = %+v, %v", got, ok)
	}
}

// A provider that sends the browser back with the state but neither a
// code nor an error ends the sign-in with an error.
func TestSignInWithoutCode(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	prompt := OAuthPrompt{Open: func(authURL string) {
		redirect, state := loopbackAt(t, authURL)
		if code := get(t, redirect+"/?"+url.Values{"state": {state}}.Encode()); code != http.StatusOK {
			t.Errorf("the page answered %d", code)
		}
	}}
	_, err := r.svc.AddOAuthAccount(r.ctx, OAuthGoogle, "", prompt)
	if err == nil || !strings.Contains(r.saw(err).Error(), "without a code") {
		t.Fatalf("err = %v", err)
	}
	if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 || len(r.secrets.data) != 0 {
		t.Errorf("accounts = %+v, keychain = %d entries", accounts, len(r.secrets.data))
	}
}

// A browser that never comes back ends the sign-in after a while, and a
// window that gives up ends it at once; either way the listener closes.
func TestSignInEndsOnTimeoutAndCancel(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	var redirect string
	var waited []time.Duration
	r.svc.newTimer = func(d time.Duration) (<-chan time.Time, func()) {
		waited = append(waited, d)
		fired := make(chan time.Time, 1)
		fired <- time.Time{}
		return fired, func() {}
	}
	prompt := OAuthPrompt{Open: func(authURL string) { redirect, _ = loopbackAt(t, authURL) }}
	if _, err := r.svc.AddOAuthAccount(r.ctx, OAuthGoogle, "", prompt); !errors.Is(r.saw(err), ErrSignInTimeout) {
		t.Fatalf("err = %v, want ErrSignInTimeout", err)
	}
	if !slices.Equal(waited, []time.Duration{signInTimeout}) {
		t.Errorf("waited %v", waited)
	}
	closed := func(when string) {
		t.Helper()
		u, _ := url.Parse(redirect)
		if conn, err := net.Dial("tcp", u.Host); err == nil {
			conn.Close()
			t.Errorf("the listener is open after %s", when)
		}
	}
	closed("the timeout")

	r.svc.newTimer = realTimer
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	prompt = OAuthPrompt{Open: func(authURL string) {
		redirect, _ = loopbackAt(t, authURL)
		cancel()
	}}
	if _, err := r.svc.AddOAuthAccount(ctx, OAuthGoogle, "", prompt); !errors.Is(r.saw(err), context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	closed("the cancel")
	if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 || len(r.secrets.data) != 0 {
		t.Errorf("accounts = %+v, keychain = %d entries", accounts, len(r.secrets.data))
	}
	if exchanges, _ := r.oauth.counts(); exchanges != 0 {
		t.Errorf("%d codes redeemed", exchanges)
	}
}

// A build without Shu's client at a provider does not offer it, and a
// sign-in there fails before the browser opens. So does one with an
// endpoint that is not https.
func TestSignInNeedsAClientAndHTTPS(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService()
	opened := false
	prompt := OAuthPrompt{Open: func(string) { opened = true }}
	for _, provider := range []string{OAuthGoogle, OAuthMicrosoft} {
		if svc.OAuthConfigured(provider) {
			t.Errorf("%s is configured in a build without its client", provider)
		}
		if _, err := svc.AddOAuthAccount(ctx, provider, "", prompt); !errors.Is(err, ErrOAuthNotConfigured) {
			t.Errorf("%s: err = %v, want ErrOAuthNotConfigured", provider, err)
		}
	}
	// Google wants its secret.
	svc.SetOAuth(OAuthGoogle, OAuthEndpoints{AuthURL: "https://a.example/auth", TokenURL: "https://a.example/token", ClientID: "id"}, nil)
	if svc.OAuthConfigured(OAuthGoogle) {
		t.Error("Google is configured without its client secret")
	}
	svc.SetOAuth(OAuthMicrosoft, OAuthEndpoints{AuthURL: "http://a.example/auth", TokenURL: "https://a.example/token", ClientID: "id"}, nil)
	if !svc.OAuthConfigured(OAuthMicrosoft) {
		t.Error("Microsoft is not configured with its client")
	}
	if _, err := svc.AddOAuthAccount(ctx, OAuthMicrosoft, "", prompt); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("err = %v, want an endpoint that is not https", err)
	}
	if _, err := svc.AddOAuthAccount(ctx, "yahoo", "", prompt); err == nil {
		t.Error("a sign-in with an unknown provider began")
	}
	if opened {
		t.Error("the browser opened")
	}
}

// The token endpoint's errors carry its code and description, without
// the secrets of the request even when the provider repeats them, and an
// answer that is no OAuth error is told by its status alone.
func TestTokenEndpointErrorsHoldNoSecrets(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthGoogle)
	a := r.signIn("")
	refresh := r.refreshToken(a.ID)
	r.clock.advance(2 * time.Hour)
	r.oauth.set(func(f *fakeOAuth) {
		f.answer = func(form url.Values) (int, string) {
			return http.StatusBadRequest, `{"error":"invalid_request","error_description":"refresh token ` +
				form.Get("refresh_token") + ` of client ` + form.Get("client_secret") + "\\n is malformed\"}"
		}
	})
	err := r.saw(r.svc.TestConnection(r.ctx, a, ""))
	var refusal *OAuthError
	if !errors.As(err, &refusal) || refusal.Code != "invalid_request" ||
		refusal.Description != "refresh token … of client … is malformed" {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrSignInRequired) {
		t.Error("a malformed request took the sign-in away")
	}
	r.oauth.set(func(f *fakeOAuth) {
		f.answer = func(form url.Values) (int, string) {
			return http.StatusBadGateway, "<html>upstream sent " + form.Get("refresh_token") + "</html>"
		}
	})
	err = r.saw(r.svc.TestConnection(r.ctx, a, ""))
	if err == nil || err.Error() != "oauth: the token endpoint answered 502 Bad Gateway" {
		t.Fatalf("err = %v", err)
	}
	if r.refreshToken(a.ID) != refresh {
		t.Error("the refresh token changed")
	}
}

// The ID token names the address: its audience must be Shu, Google's
// email must not be unverified, and Microsoft's work accounts, whose
// token may have no email, are named by the name they sign in with.
func TestSignInReadsTheAddressOfTheIDToken(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		claims         func(map[string]any)
		want           string
	}{
		{"other audience", OAuthGoogle, func(c map[string]any) { c["aud"] = []string{"someone-else"} }, ""},
		{"several audiences", OAuthGoogle, func(c map[string]any) { c["aud"] = []string{"x", "shu-test.google"} }, "ann@example.com"},
		{"unverified", OAuthGoogle, func(c map[string]any) { c["email_verified"] = false }, ""},
		{"unverified as text", OAuthGoogle, func(c map[string]any) { c["email_verified"] = "false" }, ""},
		{"no email", OAuthGoogle, func(c map[string]any) { delete(c, "email"); c["preferred_username"] = "ann@example.com" }, ""},
		{"work account", OAuthMicrosoft, func(c map[string]any) { delete(c, "email"); c["preferred_username"] = "ann@contoso.com" }, "ann@contoso.com"},
		{"phone number", OAuthMicrosoft, func(c map[string]any) { delete(c, "email"); c["preferred_username"] = "+15551234567" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, store := newOAuthRig(t, tc.provider)
			r.oauth.set(func(f *fakeOAuth) { f.claims = tc.claims })
			prompt, _ := r.oauth.prompt("")
			a, err := r.svc.AddOAuthAccount(r.ctx, tc.provider, "", prompt)
			r.saw(err)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("added %+v", a)
				}
				if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 || len(r.secrets.data) != 0 {
					t.Errorf("accounts = %+v, keychain = %d entries", accounts, len(r.secrets.data))
				}
				return
			}
			if err != nil || a.Email != tc.want || a.Username != tc.want {
				t.Fatalf("account = %+v, %v; want %s", a, err, tc.want)
			}
		})
	}
}

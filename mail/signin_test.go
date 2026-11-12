package mail

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncInbox syncs the account's folders and inbox.
func (r *oauthRig) syncInbox(accountID string) error {
	if _, err := r.svc.SyncAccount(r.ctx, accountID); err != nil {
		return r.saw(err)
	}
	return r.saw(r.svc.SyncFolder(r.ctx, accountID, "INBOX", 50))
}

// send sends a short message from the account.
func (r *oauthRig) send(accountID, subject string) (SendResult, error) {
	result, err := r.svc.Send(r.ctx, accountID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: subject, Text: "Hello"})
	return result, r.saw(err)
}

// An account added by signing in, with no host, port or password typed,
// syncs over IMAP and sends over SMTP, both signing in by XOAUTH2 with
// the access token of the sign-in, which needs no renewal yet.
func TestOAuthAccountSyncsAndSends(t *testing.T) {
	for _, provider := range []string{OAuthGoogle, OAuthMicrosoft} {
		t.Run(provider, func(t *testing.T) {
			r, store := newOAuthRig(t, provider)
			r.imap.append(t, "INBOX", readFixture(t, "plain.eml"))
			a := r.signIn("Ann")
			if err := r.syncInbox(a.ID); err != nil {
				t.Fatal(err)
			}
			msgs, _ := store.ListMessages(r.ctx, a.ID, "INBOX", "", 50)
			if len(msgs) != 1 {
				t.Fatalf("inbox = %+v", msgs)
			}
			if _, err := r.svc.FetchMessage(r.ctx, a.ID, "INBOX", msgs[0].UID, false); err != nil {
				t.Fatal(r.saw(err))
			}
			result, err := r.send(a.ID, "From Shu")
			if err != nil {
				t.Fatal(err)
			}
			if !result.Copied {
				t.Errorf("the sent copy was not saved: %v", result.Warning)
			}
			if got := r.smtp.received(); len(got) != 1 || !strings.Contains(got[0], "Subject: From Shu") {
				t.Errorf("SMTP received %q", got)
			}
			if r.imap.xoauthTries.Load() == 0 || r.smtpTries.Load() != 1 {
				t.Errorf("XOAUTH2 exchanges: IMAP %d, SMTP %d", r.imap.xoauthTries.Load(), r.smtpTries.Load())
			}
			if err := r.saw(r.svc.TestConnection(r.ctx, a, "")); err != nil {
				t.Errorf("test connection: %v", err)
			}
			if exchanges, refreshes := r.oauth.counts(); exchanges != 1 || refreshes != 0 {
				t.Errorf("token requests: %d codes, %d refreshes", exchanges, refreshes)
			}
			if need, err := r.svc.NeedsSignIn(r.ctx, a.ID); need || err != nil {
				t.Errorf("needs sign-in = %v, %v", need, err)
			}
		})
	}
}

// An access token about to expire is renewed before a connection signs
// in with it, without a word to the user, and the refresh token Microsoft
// gives in its place is the one kept.
func TestExpiredAccessTokenIsRenewed(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthMicrosoft)
	r.oauth.set(func(f *fakeOAuth) { f.rotate = true })
	a := r.signIn("Ann")
	first := r.refreshToken(a.ID)
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	// Within the margin before it expires, the token is renewed.
	r.clock.advance(time.Hour - tokenMargin + time.Second)
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 1 {
		t.Fatalf("%d refreshes", refreshes)
	}
	second := r.refreshToken(a.ID)
	if second == "" || second == first || !r.oauth.liveRefresh(second) || r.oauth.liveRefresh(first) {
		t.Errorf("the keychain kept %q, not the refresh token given in place of the first", second)
	}
	// And long after, again, with the token the last renewal left.
	r.clock.advance(3 * time.Hour)
	if _, err := r.send(a.ID, "Later"); err != nil {
		t.Fatal(err)
	}
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 2 {
		t.Errorf("%d refreshes", refreshes)
	}
	if third := r.refreshToken(a.ID); third == second || !r.oauth.liveRefresh(third) {
		t.Error("the second renewal's refresh token was not kept")
	}
}

// A server that refuses a token that has not expired, as one the
// provider revoked early, gets a renewed one, once: a renewed one it
// refuses too is the error, with no third try and no second renewal.
func TestRefusedTokenIsRenewedOnce(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthGoogle)
	a := r.signIn("Ann")
	r.oauth.dropAccess(r.oauth.lastAccess())
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 1 {
		t.Errorf("IMAP: %d refreshes", refreshes)
	}
	if tries := r.imap.xoauthTries.Load(); tries != 3 {
		t.Errorf("IMAP: %d XOAUTH2 exchanges, want a refused one and one for each of two connections", tries)
	}

	r.oauth.dropAccess(r.oauth.lastAccess())
	if _, err := r.send(a.ID, "Again"); err != nil {
		t.Fatal(err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 2 {
		t.Errorf("SMTP: %d refreshes", refreshes)
	}
	if tries := r.smtpTries.Load(); tries != 2 {
		t.Errorf("SMTP: %d XOAUTH2 exchanges", tries)
	}

	r.imapRefuses.Store(true)
	before := r.imap.xoauthTries.Load()
	_, err := r.svc.SyncAccount(r.ctx, a.ID)
	var refused *loginError
	if !errors.As(r.saw(err), &refused) || failureOf(err) != WatchFailAuth {
		t.Fatalf("err = %v, want the server's refusal", err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 3 {
		t.Errorf("refused twice: %d refreshes", refreshes)
	}
	if tries := r.imap.xoauthTries.Load() - before; tries != 2 {
		t.Errorf("refused twice: %d XOAUTH2 exchanges", tries)
	}
	if need, _ := r.svc.NeedsSignIn(r.ctx, a.ID); need {
		t.Error("a server's refusal took the sign-in away")
	}

	r.imapRefuses.Store(false)
	r.smtpRefuses.Store(true)
	if _, err := r.send(a.ID, "Refused"); !smtpRefused(err) {
		t.Fatalf("err = %v, want the SMTP server's refusal", err)
	}
}

// A sign-in the provider revoked leaves the account needing a sign-in:
// the sync says so, nothing tries again, and the cache, its mail and the
// account's settings stay as they were. Signing in again as the account
// brings it back; as another address is refused.
func TestRevokedSignInNeedsSignIn(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	r.oauth.set(func(f *fakeOAuth) { f.user = "ann@gmail.com" })
	r.imap.append(t, "INBOX", readFixture(t, "plain.eml"))
	a := r.signIn("Ann")
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	msgs, _ := store.ListMessages(r.ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 {
		t.Fatalf("inbox = %+v", msgs)
	}
	uid := msgs[0].UID
	if _, err := r.svc.FetchMessage(r.ctx, a.ID, "INBOX", uid, false); err != nil {
		t.Fatal(r.saw(err))
	}
	set := DefaultSettings()
	set.Muted = map[string]bool{a.ID: true}
	if err := r.svc.SaveSettings(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	folders, _ := store.Folders(r.ctx, a.ID)

	r.oauth.revoke("ann@gmail.com")
	err := r.svc.SyncFolder(r.ctx, a.ID, "INBOX", 50)
	if !errors.Is(r.saw(err), ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired", err)
	}
	var refusal *OAuthError
	if !errors.As(err, &refusal) || refusal.Code != "invalid_grant" {
		t.Errorf("err = %v, want the provider's invalid_grant", err)
	}
	if failureOf(err) != WatchFailSignIn {
		t.Errorf("failure = %v", failureOf(err))
	}
	if need, err := r.svc.NeedsSignIn(r.ctx, a.ID); !need || err != nil {
		t.Errorf("needs sign-in = %v, %v", need, err)
	}
	if r.refreshToken(a.ID) != "" {
		t.Error("the revoked refresh token stays in the keychain")
	}
	// Nothing tries again: no request to the provider, no connection.
	_, refreshes := r.oauth.counts()
	sessions := r.imap.sessions.Load()
	smtpTries := r.smtpTries.Load()
	for i := 0; i < 3; i++ {
		if err := r.syncInbox(a.ID); !errors.Is(err, ErrSignInRequired) {
			t.Fatalf("sync %d: %v", i, err)
		}
		if _, err := r.svc.FetchMessage(r.ctx, a.ID, "INBOX", uid, true); !errors.Is(r.saw(err), ErrSignInRequired) {
			t.Fatalf("fetch %d: %v", i, err)
		}
		if _, err := r.send(a.ID, "Lost"); !errors.Is(err, ErrSignInRequired) {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, after := r.oauth.counts(); after != refreshes || r.imap.sessions.Load() != sessions || r.smtpTries.Load() != smtpTries {
		t.Errorf("signed out, the account asked the provider %d times and the servers %d and %d times",
			after-refreshes, r.imap.sessions.Load()-sessions, r.smtpTries.Load()-smtpTries)
	}
	// The cache is whole, and its mail can be read.
	if got, ok, _ := store.Account(r.ctx, a.ID); !ok || got != a {
		t.Errorf("account = %+v, want %+v", got, a)
	}
	if got, _ := store.Folders(r.ctx, a.ID); len(got) != len(folders) {
		t.Errorf("folders = %+v, want %+v", got, folders)
	}
	if m, ok, err := r.svc.CachedMessage(r.ctx, a.ID, "INBOX", uid); err != nil || !ok || m.Message.BodyText == "" {
		t.Errorf("cached message = %+v, %v, %v", m.Message, ok, err)
	}
	if got, err := r.svc.Settings(r.ctx); err != nil || !got.Muted[a.ID] {
		t.Errorf("settings = %+v, %v", got, err)
	}
	if pending, _ := store.PendingSends(r.ctx, a.ID); len(pending) != 0 {
		t.Errorf("sends of an account that has to sign in were queued: %+v", pending)
	}

	// Another mailbox's tokens are refused, and nothing changes.
	r.oauth.set(func(f *fakeOAuth) { f.user = "eve@gmail.com" })
	prompt, _ := r.oauth.prompt("")
	err = r.svc.SignInAgain(r.ctx, a.ID, prompt)
	if !errors.Is(r.saw(err), ErrAddressMismatch) {
		t.Fatalf("err = %v, want ErrAddressMismatch", err)
	}
	if need, _ := r.svc.NeedsSignIn(r.ctx, a.ID); !need || r.refreshToken(a.ID) != "" {
		t.Error("another address's sign-in was taken")
	}
	if err := r.syncInbox(a.ID); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("sync after the refused sign-in: %v", err)
	}

	// The account's own address, in other letters, is the account.
	r.oauth.set(func(f *fakeOAuth) { f.user = "Ann@Gmail.com" })
	prompt, v := r.oauth.prompt("")
	if err := r.svc.SignInAgain(r.ctx, a.ID, prompt); err != nil {
		t.Fatal(r.saw(err))
	}
	if q := r.oauth.lastAsked(); q.Get("login_hint") != "ann@gmail.com" || q.Get("prompt") != "consent" {
		t.Errorf("the sign-in again asked with %v", q)
	}
	if v.status != 200 {
		t.Errorf("the browser came back to %d", v.status)
	}
	if need, err := r.svc.NeedsSignIn(r.ctx, a.ID); need || err != nil {
		t.Errorf("needs sign-in = %v, %v", need, err)
	}
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ListMessages(r.ctx, a.ID, "INBOX", "", 50); len(got) != 1 || got[0].UID != uid {
		t.Errorf("inbox after signing in again = %+v", got)
	}
	if got, _, _ := store.Account(r.ctx, a.ID); got != a {
		t.Errorf("account = %+v, want %+v", got, a)
	}
}

// Connections that find the token expiring together renew it once, so
// that none spends a refresh token Microsoft has already replaced.
func TestConcurrentDialsRenewOnce(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthMicrosoft)
	r.oauth.set(func(f *fakeOAuth) { f.rotate = true })
	a := r.signIn("Ann")
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(2 * time.Hour)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i%4 == 3 {
				_, errs[i] = r.send(a.ID, "Together")
				return
			}
			_, errs[i] = r.svc.SyncAccount(r.ctx, a.ID)
			r.saw(errs[i])
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("connection %d: %v", i, err)
		}
	}
	if _, refreshes := r.oauth.counts(); refreshes != 1 {
		t.Errorf("%d refreshes", refreshes)
	}
	if !r.oauth.liveRefresh(r.refreshToken(a.ID)) {
		t.Error("the keychain holds a refresh token the provider replaced")
	}
}

// Signing out takes the tokens out of the keychain and leaves the
// account, its cache and its settings; removing the account takes its
// tokens with it.
func TestSignOutAndRemoveDeleteTheTokens(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	r.imap.append(t, "INBOX", readFixture(t, "plain.eml"))
	a := r.signIn("Ann")
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SignOut(r.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.secrets.data) != 0 {
		t.Errorf("the keychain holds %d entries after signing out", len(r.secrets.data))
	}
	if need, _ := r.svc.NeedsSignIn(r.ctx, a.ID); !need {
		t.Error("a signed-out account does not need a sign-in")
	}
	// The access token in memory went too.
	if err := r.syncInbox(a.ID); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("sync after signing out: %v", err)
	}
	if err := r.saw(r.svc.TestConnection(r.ctx, a, "")); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("test connection after signing out: %v", err)
	}
	if got, ok, _ := store.Account(r.ctx, a.ID); !ok || got != a {
		t.Errorf("account = %+v, %v", got, ok)
	}
	if got, _ := store.ListMessages(r.ctx, a.ID, "INBOX", "", 50); len(got) != 1 {
		t.Errorf("inbox = %+v", got)
	}

	prompt, _ := r.oauth.prompt("")
	if err := r.svc.SignInAgain(r.ctx, a.ID, prompt); err != nil {
		t.Fatal(r.saw(err))
	}
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RemoveAccount(r.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.secrets.data) != 0 {
		t.Errorf("the keychain holds %d entries after removing the account", len(r.secrets.data))
	}
	if _, ok, _ := store.Account(r.ctx, a.ID); ok {
		t.Error("the account remains")
	}
	r.tokenMuCheck(a.ID)
	if _, err := r.svc.NeedsSignIn(r.ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("needs sign-in of a removed account: %v", err)
	}
	// Signing in again, or out, is for OAuth accounts that are there.
	if err := r.svc.SignInAgain(r.ctx, a.ID, prompt); !errors.Is(err, ErrNotFound) {
		t.Errorf("signing a removed account in again: %v", err)
	}
	if err := r.svc.AddAccount(r.ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SignOut(r.ctx, "a1"); err == nil {
		t.Error("an account that signs in with a password signed out")
	}
	if err := r.svc.SignInAgain(r.ctx, "a1", prompt); err == nil {
		t.Error("an account that signs in with a password signed in with OAuth")
	}
	if need, err := r.svc.NeedsSignIn(r.ctx, "a1"); need || err != nil {
		t.Errorf("a password account needs a sign-in: %v, %v", need, err)
	}
}

// tokenMuCheck fails if the service still keeps an access token for a
// removed account.
func (r *oauthRig) tokenMuCheck(accountID string) {
	r.t.Helper()
	r.svc.tokenMu.Lock()
	defer r.svc.tokenMu.Unlock()
	if _, ok := r.svc.tokens[accountID]; ok {
		r.t.Error("the service keeps the access token of a removed account")
	}
}

// Of an account that signs in with OAuth only the name changes, even when
// a form built for passwords sends the rest; it has no password, and is
// only added by signing in.
func TestOAuthAccountUpdates(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	a := r.signIn("Ann")
	form := Account{
		ID: a.ID, Name: "Ann Lee", Email: "someone@example.com", Username: "someone",
		IMAP: Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit},
		SMTP: Server{Host: "smtp.example.com", Port: 465, TLS: TLSImplicit},
	}
	if err := r.svc.UpdateAccount(r.ctx, form, ""); err != nil {
		t.Fatal(err)
	}
	want := a
	want.Name = "Ann Lee"
	if got, _, _ := store.Account(r.ctx, a.ID); got != want {
		t.Errorf("account = %+v, want %+v", got, want)
	}
	if err := r.svc.UpdateAccount(r.ctx, form, "hunter2"); err == nil {
		t.Error("an OAuth account took a password")
	}
	if _, err := r.secrets.Get(PasswordKey(a.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("password entry: %v", err)
	}
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}

	added := testAccount()
	added.OAuth = OAuthGoogle
	if err := r.svc.AddAccount(r.ctx, added, "secret"); err == nil {
		t.Error("an OAuth account was added with a password")
	}
	if err := r.svc.AddAccount(r.ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.UpdateAccount(r.ctx, added, ""); err == nil {
		t.Error("a password account changed to OAuth")
	}
	bad := testAccount()
	bad.OAuth = "yahoo"
	if err := bad.Validate(); err == nil {
		t.Error("an unknown OAuth provider is valid")
	}
}

// watchEvents is a watcher's reports.
type watchEvents chan WatchEvent

// next waits for a report that matches, passing over the others.
func (ch watchEvents) next(t *testing.T, what string, match func(WatchEvent) bool) WatchEvent {
	t.Helper()
	guard := time.After(watchGuard)
	for {
		select {
		case ev := <-ch:
			if match(ev) {
				return ev
			}
		case <-guard:
			t.Fatalf("the watcher did not report %s", what)
		}
	}
}

// watchOAuth starts watching the rig's account, its timers on the test's
// clock, after it has signed in.
func (r *oauthRig) watchOAuth(a Account) (watchEvents, *watchClock) {
	clock := newWatchClock()
	r.svc.newTimer = clock.newTimer
	events := make(watchEvents, 1024)
	r.svc.Watch(a.ID, func(ev WatchEvent) { events <- ev })
	r.t.Cleanup(r.svc.StopWatching)
	events.next(r.t, "a check", func(ev WatchEvent) bool { return ev.Folders != nil })
	// It idles on the inbox.
	select {
	case <-clock.armed:
	case <-time.After(watchGuard):
		r.t.Fatal("the watcher did not idle")
	}
	return events, clock
}

// The watcher of an account signed out hangs up and says the account has
// to sign in, and waits for that without a timer: a nudge changes
// nothing. Signing in again has it check at once.
func TestWatcherWaitsForSignIn(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthGoogle)
	a := r.signIn("Ann")
	events, clock := r.watchOAuth(a)
	if err := r.svc.SignOut(r.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	ev := events.next(t, "the sign-in", func(ev WatchEvent) bool { return ev.Status.State != WatchConnecting })
	if ev.Status.State != WatchSignIn || ev.Status.Failure != WatchFailSignIn {
		t.Fatalf("status = %+v", ev.Status)
	}
	if n := len(clock.armed); n != 0 {
		t.Errorf("the watcher armed %d timers to try again", n)
	}
	r.svc.Nudge()
	_, refreshes := r.oauth.counts()
	prompt, _ := r.oauth.prompt("")
	if err := r.svc.SignInAgain(r.ctx, a.ID, prompt); err != nil {
		t.Fatal(r.saw(err))
	}
	events.next(t, "a check", func(ev WatchEvent) bool {
		if ev.Status.State == WatchSignIn || ev.Status.State == WatchRetrying {
			t.Fatalf("after the sign-in the watcher reported %+v", ev.Status)
		}
		return ev.Folders != nil
	})
	if _, after := r.oauth.counts(); after != refreshes {
		t.Errorf("the watcher asked the provider for %d tokens", after-refreshes)
	}
}

// A sign-in the provider revoked, as a sync finds, has the watcher hang
// up on its connection, which still works, and wait for the account to
// sign in again.
func TestWatcherHearsOfARevokedSignIn(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthMicrosoft)
	a := r.signIn("Ann")
	events, _ := r.watchOAuth(a)
	r.oauth.revoke("ann@example.com")
	if err := r.syncInbox(a.ID); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("sync: %v", err)
	}
	ev := events.next(t, "the sign-in", func(ev WatchEvent) bool { return ev.Status.State != WatchConnecting })
	if ev.Status.State != WatchSignIn {
		t.Fatalf("status = %+v", ev.Status)
	}
	guard := time.After(watchGuard)
	for r.imap.open.Load() != 0 {
		select {
		case <-r.imap.ended:
		case <-guard:
			t.Fatalf("%d connections are still open", r.imap.open.Load())
		}
	}
}

// A keychain that will not keep the refresh token leaves no account
// behind, and a server that does not take OAuth tokens is an error of its
// own, which no renewal answers.
func TestSignInFailuresLeaveNothing(t *testing.T) {
	r, store := newOAuthRig(t, OAuthGoogle)
	r.secrets.fail = true
	prompt, _ := r.oauth.prompt("")
	if _, err := r.svc.AddOAuthAccount(r.ctx, OAuthGoogle, "", prompt); err == nil || !strings.Contains(r.saw(err).Error(), "keychain") {
		t.Fatalf("err = %v, want the keychain's", err)
	}
	if accounts, _ := store.Accounts(r.ctx); len(accounts) != 0 {
		t.Errorf("accounts = %+v", accounts)
	}
	r.secrets.fail = false

	a := r.signIn("Ann")
	plain := startFakeIMAP(t, allCaps())
	host, port := plain.hostPort(t)
	a.IMAP = Server{Host: host, Port: port, TLS: TLSStartTLS}
	if err := store.SaveAccount(r.ctx, a); err != nil {
		t.Fatal(err)
	}
	_, err := r.svc.SyncAccount(r.ctx, a.ID)
	if err == nil || !strings.Contains(r.saw(err).Error(), "XOAUTH2") || failureOf(err) != WatchFailOther {
		t.Fatalf("err = %v, want a server without XOAUTH2", err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 0 {
		t.Errorf("%d refreshes", refreshes)
	}
}

// A renewal that fails for want of the network, or that the provider
// cannot answer for now, keeps the sign-in: the next try renews it.
func TestRenewalThatFailsKeepsTheSignIn(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthMicrosoft)
	a := r.signIn("Ann")
	refresh := r.refreshToken(a.ID)
	r.clock.advance(2 * time.Hour)
	r.oauth.set(func(f *fakeOAuth) {
		f.answer = func(url.Values) (int, string) {
			return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable","error_description":"Try again."}`
		}
	})
	_, err := r.svc.SyncAccount(r.ctx, a.ID)
	if errors.Is(r.saw(err), ErrSignInRequired) || failureOf(err) != WatchFailOther {
		t.Fatalf("err = %v, want the provider's", err)
	}
	r.oauth.server.Close()
	_, err = r.svc.SyncAccount(r.ctx, a.ID)
	if errors.Is(r.saw(err), ErrSignInRequired) || failureOf(err) != WatchFailNetwork {
		t.Fatalf("err = %v, want the network's", err)
	}
	if need, _ := r.svc.NeedsSignIn(r.ctx, a.ID); need || r.refreshToken(a.ID) != refresh {
		t.Fatal("a renewal that could not be made took the sign-in away")
	}
}

// A renewal Microsoft says needs the user, for a second factor, needs a
// sign-in, as a revoked one does.
func TestRenewalThatNeedsTheUserNeedsSignIn(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthMicrosoft)
	a := r.signIn("Ann")
	r.clock.advance(2 * time.Hour)
	r.oauth.set(func(f *fakeOAuth) {
		f.answer = func(url.Values) (int, string) {
			return http.StatusBadRequest, `{"error":"interaction_required","error_description":"AADSTS50076: multi-factor authentication is required."}`
		}
	})
	if _, err := r.svc.SyncAccount(r.ctx, a.ID); !errors.Is(r.saw(err), ErrSignInRequired) {
		t.Fatalf("err = %v, want ErrSignInRequired", err)
	}
	if need, _ := r.svc.NeedsSignIn(r.ctx, a.ID); !need {
		t.Error("the account does not need a sign-in")
	}
}

// With the context of its job cancelled, a dial waits no longer for the
// renewal another connection has under way.
func TestRenewalWaitEndsWithTheContext(t *testing.T) {
	r, _ := newOAuthRig(t, OAuthGoogle)
	a := r.signIn("Ann")
	r.clock.advance(2 * time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.oauth.set(func(f *fakeOAuth) {
		f.beforeRefresh = func() {
			once.Do(func() { close(entered) })
			<-release
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := r.svc.SyncAccount(r.ctx, a.ID)
		done <- r.saw(err)
	}()
	<-entered
	ctx, cancel := context.WithCancel(r.ctx)
	cancel()
	if _, err := r.svc.SyncAccount(ctx, a.ID); !errors.Is(r.saw(err), context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("the renewal: %v", err)
	}
	if _, refreshes := r.oauth.counts(); refreshes != 1 {
		t.Errorf("%d refreshes", refreshes)
	}
}

package mail

import (
	"errors"
	"testing"
	"time"
)

// OAuthScenario lives an OAuth account's life on store, with a fake
// provider and fake mail servers: it signs in, syncs, reads, sends, has
// its token renewed and its refresh token replaced, fails a send, is
// revoked and signs in again. It returns every code and token the
// provider handed out, and its client secret. It is for the test of the
// database file (oauth_sqlite_test.go), which needs the SQLite store and
// so cannot be in this package.
func OAuthScenario(t *testing.T, store Store) []string {
	t.Helper()
	r := newOAuthRigOn(t, OAuthGoogle, store)
	r.oauth.set(func(f *fakeOAuth) { f.rotate = true })
	r.imap.append(t, "INBOX", readFixture(t, "plain.eml"))
	r.imap.append(t, "INBOX", readFixture(t, "attachment.eml"))
	a := r.signIn("Ann")
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.ListMessages(r.ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("inbox = %+v, %v", msgs, err)
	}
	for _, m := range msgs {
		if _, err := r.svc.FetchMessage(r.ctx, a.ID, "INBOX", m.UID, true); err != nil {
			t.Fatal(r.saw(err))
		}
	}
	if _, err := r.send(a.ID, "Sent with a token"); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(2 * time.Hour)
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	r.smtpRefuses.Store(true)
	if _, err := r.send(a.ID, "Refused"); !smtpRefused(err) {
		t.Fatalf("send to a server that refuses the token: %v", err)
	}
	r.smtpRefuses.Store(false)
	r.oauth.revoke("ann@example.com")
	if err := r.syncInbox(a.ID); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("sync after the revocation: %v", err)
	}
	prompt, _ := r.oauth.prompt("")
	if err := r.svc.SignInAgain(r.ctx, a.ID, prompt); err != nil {
		t.Fatal(r.saw(err))
	}
	if err := r.syncInbox(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.send(a.ID, "Signed in again"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingSends(r.ctx, a.ID); err != nil || len(pending) != 1 || pending[0].LastError == "" {
		t.Fatalf("the refused send is not in the outbox: %+v, %v", pending, err)
	}
	return r.oauth.secrets()
}

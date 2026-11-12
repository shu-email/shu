package mail

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// ScheduleScenario sends with a delay on store, as the app does on its
// database: a draft from an alias, with a blind copy and a file, is
// scheduled and taken back, then scheduled again and sent as its delay
// ends; another message waits while the app is closed and leaves after
// the next start, once; one that had begun to leave when the app stopped
// is reported unknown and never sent again. It is for the test on the
// SQLite store (schedule_sqlite_test.go).
func ScheduleScenario(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent", "Drafts")
	smtpServer := startFakeSMTP(t)
	secrets := newMemSecrets()
	clock := newSendClock()
	dataDir, files := t.TempDir(), t.TempDir()
	start := func() *Service {
		svc := NewService(store, secrets, "")
		svc.SetTLSConfig(insecureTLS())
		svc.SetDataDir(dataDir)
		svc.SetClock(clock.Now)
		svc.newTimer = clock.newTimer
		return svc
	}
	svc := start()
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	alias, err := svc.AddIdentity(ctx, a.ID, "Ann (Lab)", "ann@lab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	env := &attachEnv{dataDir: dataDir, files: files}
	d, att, err := svc.AttachFile(ctx, Draft{
		AccountID: a.ID, To: "bob@example.com", Bcc: "Erin <erin@example.com>", Subject: "Results", Body: "Attached.",
		Identity: alias.ID,
	}, env.file(t, "results.csv", []byte("a,b\n1,2\n")))
	if err != nil {
		t.Fatal(err)
	}
	out := d.outgoing()
	out.DraftID = d.ID

	p, err := svc.Schedule(ctx, a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := sendInBackground(ctx, svc, p.ID)
	clock.timer(t)
	back, err := svc.Unsend(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != d.ID || back.Identity != alias.ID || back.Bcc != d.Bcc || !sameFiles(back.Attachments, att) {
		t.Fatalf("draft given back = %+v", back)
	}
	if o := outcome(t, done); !errors.Is(o.err, ErrUnsent) || smtpServer.sessions.Load() != 0 {
		t.Fatalf("taken back, the send ended with %v after %d connections", o.err, smtpServer.sessions.Load())
	}

	p, err = svc.Schedule(ctx, a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done = sendInBackground(ctx, svc, p.ID)
	clock.timer(t).fire()
	if o := outcome(t, done); o.err != nil || !o.result.Copied {
		t.Fatalf("send after the delay = %+v, %v", o.result, o.err)
	}
	if got := smtpServer.senders(); !slices.Equal(got, []string{"ann@lab.example.org"}) {
		t.Errorf("envelope sender = %v", got)
	}
	if got := smtpServer.recipients(); !slices.Equal(got, []string{"bob@example.com", "erin@example.com"}) {
		t.Errorf("recipients = %v", got)
	}
	received := smtpServer.received()
	if len(received) != 1 || strings.Contains(received[0], "erin@example.com") {
		t.Fatalf("delivered %d messages, naming the blind copy: %v", len(received), len(received) == 1)
	}
	sent := imapServer.messages(t, "Sent")
	if bcc, _ := draftHeaders([]byte(sent[0].Raw)); len(sent) != 1 || len(bcc) != 1 || bcc[0].Email != "erin@example.com" ||
		sent[0].From != alias.Address() {
		t.Errorf("the Sent copy names %+v as Bcc, from %+v", bcc, sent[0].From)
	}
	if _, ok, _ := svc.Draft(ctx, d.ID); ok {
		t.Error("the draft outlived its message")
	}

	waiting, err := svc.Schedule(ctx, a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "While closed"}, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stuck, err := svc.Schedule(ctx, a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Stuck"}, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if won, err := store.ClaimOutbox(ctx, stuck.ID, StateScheduled, StateSending); err != nil || !won {
		t.Fatal(won, err)
	}
	clock.advance(time.Hour)
	restarted := start()
	results, err := restarted.DeliverScheduled(ctx)
	if err != nil || len(results) != 1 || results[0].Outbox.ID != waiting.ID || results[0].Err != nil {
		t.Fatalf("after the restart: %+v, %v", results, err)
	}
	if again, err := start().DeliverScheduled(ctx); err != nil || len(again) != 0 {
		t.Errorf("another restart sent %+v, %v", again, err)
	}
	if got := len(smtpServer.received()); got != 2 {
		t.Errorf("delivered %d messages in all, want 2", got)
	}
	pending, err := restarted.PendingSends(ctx, a.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != stuck.ID || pending[0].State != StateUnknown {
		t.Errorf("outbox = %+v, %v", pending, err)
	}
}

package main

import (
	"context"
	"net"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
	"shu/store"
)

// memKeychain is the keychain of the tests that run the real engine.
type memKeychain map[string][]byte

func (k memKeychain) Set(key string, secret []byte) error {
	k[key] = append([]byte(nil), secret...)
	return nil
}

func (k memKeychain) Get(key string) ([]byte, error) {
	v, ok := k[key]
	if !ok {
		return nil, mail.ErrNotFound
	}
	return v, nil
}

func (k memKeychain) Delete(key string) error {
	delete(k, key)
	return nil
}

// The whole path of the first acceptance criterion, on the real engine
// and the real database, with a mail server that cannot be reached: type
// a message, quit without closing the window, launch again, and the
// draft is in Drafts and opens exactly as it was left.
func TestDraftRestoredFromDiskAfterRestart(t *testing.T) {
	if err := sqlite.Load(); err != nil {
		t.Skipf("sqlite native module is not ready (%v); run go tool mygo generate", err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	keys := memKeychain{}

	// The account's servers are a local port nothing listens on: every
	// connection is refused at once, as offline.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: "127.0.0.1", Port: port, TLS: mail.TLSImplicit},
		SMTP: mail.Server{Host: "127.0.0.1", Port: port, TLS: mail.TLSImplicit},
	}

	// launch opens the database and starts the app on it, as main does.
	launch := func() (*app, *store.Store) {
		t.Helper()
		db, err := store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		a := newTestApp(mail.NewService(db, keys, t.TempDir()))
		a.setLocale = func(string) {}
		a.start()
		return a, db
	}

	first, db := launch()
	if err := first.svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	first.afterAccountSaved(account.ID)
	if first.active != "a1" {
		t.Fatalf("active account = %q", first.active)
	}
	const (
		to      = "Bob <bob@example.com>, carol@exa"
		cc      = "dave@"
		subject = "Quarterly plan"
		body    = "First thoughts, unfinished"
	)
	cs := first.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), to)
	typeIn(t, tt, locales.Cc(), cc)
	typeIn(t, tt, locales.Subject(), subject)
	typeIn(t, tt, locales.Body(), body)
	if !tt.HasText(locales.DraftSaved()) {
		t.Fatalf("status = %q", tt.Texts())
	}
	// The upload cannot reach the server. The draft says so and stays.
	first.syncDrafts("a1")
	tt.Frame()
	if !tt.HasText(locales.DraftOffline()) {
		t.Fatalf("status offline = %q", tt.Texts())
	}
	// Quit: the window is never closed, the database just goes away.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	second, db := launch()
	main := ui.NewTester(second.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(second.all) != 1 || second.all[0].DraftID != cs.draftID || !main.HasText(subject) || !main.HasText(body) {
		t.Fatalf("drafts after the restart = %+v, texts %q", second.all, main.Texts())
	}
	click(t, main, locales.EditDraft())
	if len(second.composes) != 1 {
		t.Fatalf("composes = %d", len(second.composes))
	}
	got := second.composes[0]
	if got.to != to || got.cc != cc || got.subject != subject || got.body != body || got.accountID != "a1" {
		t.Fatalf("restored window = to %q cc %q subject %q body %q", got.to, got.cc, got.subject, got.body)
	}
	rt := ui.NewTester(got.view, 720, 620)
	values := fieldValues(rt)
	for _, want := range []string{to, cc, subject, body} {
		if !slices.Contains(values, want) {
			t.Errorf("restored window does not show %q: %q", want, values)
		}
	}
	// An edit after the restart is saved to the same draft.
	typeIn(t, rt, locales.Body(), ", continued")
	d, ok, err := second.svc.Draft(ctx, cs.draftID)
	if err != nil || !ok || d.Body != body+", continued" || d.To != to || !d.Dirty() {
		t.Fatalf("draft on disk = %+v, %v, %v", d, ok, err)
	}

	// Discarding it, confirmed, removes it for the next launch too. The
	// server never had it, so nothing waits for a connection.
	second.confirmDiscard = func(*mygo.Window, int) bool { return true }
	click(t, rt, locales.DiscardDraft())
	if !got.closed {
		t.Error("the window stays open after the discard")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	third, db := launch()
	defer db.Close()
	main = ui.NewTester(third.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(third.all) != 0 || !main.HasText(locales.EmptyFolder()) {
		t.Errorf("drafts after the discard and a restart = %+v", third.all)
	}
}

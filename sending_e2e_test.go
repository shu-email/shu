package main

import (
	"context"
	"crypto/tls"
	netmail "net/mail"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// The acceptance criteria of sending end to end: the window, the real
// engine, a database on disk and local IMAP and SMTP servers. A message
// written from an alias the account editor added, with a blind copy,
// waits out the delay the settings chose and then leaves: the server is
// given it from the alias and for the blind copy too, whom the message
// itself does not name, and the copy in Sent names both. Another, taken
// back with Undo before its time, reaches no server and opens again as
// it was written.
func TestSendDelayEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Drafts", "Sent", "Trash")
	smtpServer := startLocalSMTP(t)
	imapHost, imapPort := hostPort(t, imapServer.addr)
	smtpHost, smtpPort := hostPort(t, smtpServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: imapHost, Port: imapPort, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: smtpHost, Port: smtpPort, TLS: mail.TLSStartTLS},
	}
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	main := ui.NewTester(a.view, 1220, 800)
	// The engine keeps the window's time: the delay the window counts
	// down is the one the engine waits, and the test moves both on.
	svc.SetClock(main.Now)

	// The shortest delay, chosen in the settings over the engine's own.
	a.openSettings(settingsTabGeneral)
	main.SetSize(1220, 1800)
	if info, _ := main.AccessByRole(ui.RoleRadio, locales.SendDelaySeconds(10)); !info.Checked {
		t.Fatalf("the engine's delay is not the one chosen: %+v", info)
	}
	click(t, main, locales.SendDelaySeconds(5))
	click(t, main, locales.Done())
	main.SetSize(1220, 800)

	// The alias, added in the account editor.
	a.openAccountForm(a.account("a1"))
	main.Frame()
	reveal(t, main, locales.Provider(), locales.IdentitiesSection())
	click(t, main, locales.AddIdentity())
	main.Type("Ann (Lab)")
	typeIn(t, main, locales.IdentityEmail(), "ann@lab.example.org")
	click(t, main, locales.IdentitySave())
	if ids, err := svc.Identities(ctx, "a1"); err != nil || len(ids) != 2 || ids[1].Email != "ann@lab.example.org" {
		t.Fatalf("identities = %+v, %v", ids, err)
	}
	click(t, main, locales.Cancel())

	// held runs the jobs queued so far, and holds those they queue.
	q := queueJobs(a)
	held := func() {
		jobs := q.jobs
		q.jobs = nil
		for _, job := range jobs {
			job()
		}
	}
	a.run = func(f func()) { f() }

	// Written from the alias, with a blind copy, and sent: it waits.
	cs := a.openCompose(nil)
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.To(), "Bob <bob@example.com>")
	click(t, ct, locales.ShowBcc())
	ct.Type("Erin <erin@example.com>")
	typeIn(t, ct, locales.Subject(), "Lab results")
	typeIn(t, ct, locales.Body(), "The results are in.")
	click(t, ct, "Ann <ann@example.com>")
	click(t, ct, "Ann (Lab) <ann@lab.example.org>")
	a.run = func(f func()) { q.jobs = append(q.jobs, f) }
	click(t, ct, locales.Send())
	held()
	main.Frame()
	if !cs.closed || !main.HasText(locales.SendingIn(5)) || smtpServer.connections() != 0 {
		t.Fatalf("waiting: closed %v, %d connections: %q", cs.closed, smtpServer.connections(), main.Texts())
	}
	// Its time comes, and it leaves.
	main.Advance(6 * time.Second)
	held()
	q.drain(a)
	main.Frame()
	if len(a.waiting) != 0 || main.HasText(locales.Sending()) || a.sendNote != "" {
		t.Fatalf("after its time: waiting %d, note %q: %q", len(a.waiting), a.sendNote, main.Texts())
	}
	got, envelopes := smtpServer.received(), smtpServer.envelopesGot()
	if len(got) != 1 || len(envelopes) != 1 {
		t.Fatalf("the server was given %d messages", len(got))
	}
	if env := envelopes[0]; env.from != "ann@lab.example.org" ||
		!slices.Equal(env.to, []string{"bob@example.com", "erin@example.com"}) {
		t.Errorf("envelope = %+v", env)
	}
	delivered := readMessage(t, got[0])
	if from := addressesOf(t, delivered, "From"); len(from) != 1 || from[0].Address != "ann@lab.example.org" || from[0].Name != "Ann (Lab)" {
		t.Errorf("delivered from %+v", from)
	}
	if delivered.Header.Get("Bcc") != "" || strings.Contains(got[0], "erin@example.com") {
		t.Errorf("the message delivered names the blind copy:\n%s", got[0])
	}
	sent := imapServer.messages(t, "Sent")
	if len(sent) != 1 {
		t.Fatalf("the Sent folder has %d messages", len(sent))
	}
	copyOf := readMessage(t, sent[0].Raw)
	if bcc := addressesOf(t, copyOf, "Bcc"); len(bcc) != 1 || bcc[0].Address != "erin@example.com" {
		t.Errorf("the copy in Sent names %+v as Bcc", bcc)
	}
	if from := addressesOf(t, copyOf, "From"); len(from) != 1 || from[0].Address != "ann@lab.example.org" {
		t.Errorf("the copy in Sent is from %+v", from)
	}
	if drafts, err := db.Drafts(ctx, "a1"); err != nil || len(drafts) != 0 {
		t.Errorf("drafts left after the send = %+v, %v", drafts, err)
	}

	// Another, taken back before its time.
	connections := smtpServer.connections()
	a.run = func(f func()) { f() }
	back := a.openCompose(nil)
	bt := ui.NewTester(back.view, 720, 620)
	typeIn(t, bt, locales.To(), "Carol <carol@example.com>")
	typeIn(t, bt, locales.Subject(), "Not yet")
	typeIn(t, bt, locales.Body(), "Hold on.")
	written := back.snapshot()
	a.run = func(f func()) { q.jobs = append(q.jobs, f) }
	click(t, bt, locales.Send())
	held()
	main.Frame()
	if !back.closed || !main.HasText(locales.SendingIn(5)) {
		t.Fatalf("the second message is not waiting: %q", main.Texts())
	}
	waiting := q.jobs
	q.jobs = nil
	a.run = func(f func()) { f() }
	click(t, main, locales.UndoSendOf("Not yet"))
	for _, job := range waiting {
		job()
	}
	main.Frame()
	if smtpServer.connections() != connections || len(smtpServer.received()) != 1 || len(a.waiting) != 0 || a.sendNote != "" {
		t.Fatalf("taken back: %d connections, %d messages, waiting %d, note %q",
			smtpServer.connections()-connections, len(smtpServer.received()), len(a.waiting), a.sendNote)
	}
	open := openComposes(a)
	if len(open) != 1 || open[0].to != written.To || open[0].subject != written.Subject || open[0].body != written.Body ||
		open[0].draftID != written.ID {
		t.Fatalf("opened again: %+v", open)
	}
	if d, ok, err := svc.Draft(ctx, written.ID); err != nil || !ok || d.Body != written.Body {
		t.Errorf("the draft taken back = %+v, %v, %v", d, ok, err)
	}
}

// readMessage parses a message as it was given or kept.
func readMessage(t *testing.T, raw string) *netmail.Message {
	t.Helper()
	m, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// addressesOf reads a header of addresses.
func addressesOf(t *testing.T, m *netmail.Message, name string) []*netmail.Address {
	t.Helper()
	if m.Header.Get(name) == "" {
		return nil
	}
	list, err := m.Header.AddressList(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return list
}

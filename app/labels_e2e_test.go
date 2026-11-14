package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"

	"shu/internal/fakemail"
	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// serverMessage is a message of a mailbox as another mail client sees it:
// its UID, its size, its system flags and its keywords, sorted.
type serverMessage struct {
	uid             imap.UID
	size            int64
	flags, keywords []string
}

// mailOf reads a mailbox's messages by subject, as another mail client
// sees them.
func (s *localIMAP) mailOf(t *testing.T, mailbox string) map[string]serverMessage {
	t.Helper()
	c, selected := s.open(t, mailbox)
	defer c.Close()
	out := map[string]serverMessage{}
	if selected.NumMessages == 0 {
		return out
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	fetched, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, Envelope: true, RFC822Size: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range fetched {
		msg := serverMessage{uid: m.UID, size: m.RFC822Size, flags: []string{}, keywords: []string{}}
		for _, f := range m.Flags {
			if strings.HasPrefix(string(f), `\`) {
				msg.flags = append(msg.flags, string(f))
			} else {
				msg.keywords = append(msg.keywords, string(f))
			}
		}
		slices.Sort(msg.flags)
		slices.Sort(msg.keywords)
		out[m.Envelope.Subject] = msg
	}
	return out
}

// keywordsOf reads the keywords of a mailbox's messages by subject, in
// lower case, as IMAP compares them: the server may keep another case.
func (s *localIMAP) keywordsOf(t *testing.T, mailbox string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for subject, m := range s.mailOf(t, mailbox) {
		out[subject] = []string{}
		for _, k := range m.keywords {
			out[subject] = append(out[subject], strings.ToLower(k))
		}
		slices.Sort(out[subject])
	}
	return out
}

// withoutKeywords is mail as deleting a label never changes it: all but
// the keywords.
func withoutKeywords(mail map[string]serverMessage) map[string]serverMessage {
	out := map[string]serverMessage{}
	for subject, m := range mail {
		m.keywords = nil
		out[subject] = m
	}
	return out
}

// putLabelMail puts three messages of three senders in a server's inbox.
func putLabelMail(t *testing.T, s *localIMAP) {
	t.Helper()
	for i, subject := range []string{"Venue for the offsite", "Quarterly numbers", "Lunch on Friday"} {
		raw := fmt.Sprintf("From: Sender %d <sender%d@example.com>\r\nTo: Ann <ann@example.com>\r\nSubject: %s\r\n"+
			"Date: %s\r\nMessage-ID: <labels-%d@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAbout %s.\r\n",
			i, i, subject, time.Date(2026, 3, 1+i, 9, 0, 0, 0, time.UTC).Format(time.RFC1123Z), i, strings.ToLower(subject))
		if _, err := s.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
}

// labelEngine is the engine on a database file, as the app opens it as it
// starts, with the keychain of the account's password.
type labelEngine struct {
	t    *testing.T
	path string
	keys memKeychain
	db   *store.Store
}

// open opens the database, closing the one open, and returns a new
// window on it, started.
func (e *labelEngine) open() (*app, *mail.Service) {
	e.t.Helper()
	ctx := context.Background()
	if e.db != nil {
		if err := e.db.Close(); err != nil {
			e.t.Fatal(err)
		}
	}
	db, err := store.Open(ctx, e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.db = db
	svc := mail.NewService(db, e.keys, e.t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a local test server
	return newTestApp(svc), svc
}

func newLabelEngine(t *testing.T) *labelEngine {
	e := &labelEngine{t: t, path: filepath.Join(t.TempDir(), "shu.db"), keys: memKeychain{}}
	t.Cleanup(func() {
		if e.db != nil {
			e.db.Close()
		}
	})
	return e
}

// startEngineApp starts a window on the engine, as the user sees it.
func startEngineApp(t *testing.T, a *app) *ui.Tester {
	t.Helper()
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	return ui.NewTester(a.view, 1220, 800)
}

// The acceptance criteria of labels end to end: the window on the real
// engine and a database on disk, against a local IMAP server that keeps
// keywords. Two labels are made, and both put on one message from the
// toolbar's checklist: the message is under each label's list, and the
// server has both keywords on it. A new app on the same database, as
// after a restart, shows them again, and so does it after a sync. A label
// deleted once the user said so comes off the message on the server, and
// every message there is as it was but for that keyword; label: finds the
// message by the other.
func TestLabelsEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Archive", "Trash")
	putLabelMail(t, imapServer)
	host, port := hostPort(t, imapServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
	}
	engine := newLabelEngine(t)
	a, svc := engine.open()
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	tt := startEngineApp(t, a)
	if len(a.convs) != 3 {
		t.Fatalf("rows = %v", rowSubjects(a))
	}

	// Two labels, made in the dialog, and both put on the numbers.
	for _, name := range []string{"Work", "To Do"} {
		click(t, tt, locales.NewLabel())
		focusName(t, tt, name)
		click(t, tt, locales.CreateLabel())
		if a.labels.form.open {
			t.Fatalf("making %s: %q", name, tt.Texts())
		}
	}
	click(t, tt, "Quarterly numbers")
	openPicker(t, a, tt)
	clickRole(t, tt, ui.RoleCheckBox, "Work")
	clickRole(t, tt, ui.RoleCheckBox, "To Do")
	if a.err != "" || a.labels.note != "" {
		t.Fatalf("labelling: %q, note %q", a.err, a.labels.note)
	}
	tt.Key(0, ui.KeyEscape)
	underEach := func(tt *ui.Tester, a *app, when string) {
		t.Helper()
		for _, name := range []string{"Work", "To Do"} {
			openLabel(t, tt, name)
			if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Quarterly numbers"}) {
				t.Errorf("%s: %s lists %v", when, name, got)
			}
		}
		click(t, tt, locales.Inbox())
		row := slices.IndexFunc(a.all, func(m mail.Summary) bool { return m.Subject == "Quarterly numbers" })
		if got := a.labelNames("a1", a.rowLabelsOf(row)); got != "Work, To Do" {
			t.Errorf("%s: the row's labels = %q", when, got)
		}
	}
	underEach(tt, a, "labelled")
	want := map[string][]string{"Venue for the offsite": {}, "Quarterly numbers": {"to_do", "work"}, "Lunch on Friday": {}}
	if got := imapServer.keywordsOf(t, "INBOX"); !reflect.DeepEqual(got, want) {
		t.Fatalf("the server's keywords = %v, want %v", got, want)
	}

	// A restart, and a sync.
	a, _ = engine.open()
	tt = startEngineApp(t, a)
	underEach(tt, a, "after a restart")
	a.refresh()
	tt.Frame()
	if a.err != "" {
		t.Fatalf("sync: %s", a.err)
	}
	underEach(tt, a, "after a sync")

	// Deleting Work, once confirmed.
	before := imapServer.mailOf(t, "INBOX")
	askDelete(t, tt, "Work")
	click(t, tt, locales.DeleteLabelButton())
	if a.err != "" || a.labels.del.partly {
		t.Fatalf("deleting: %q, partly %v", a.err, a.labels.del.partly)
	}
	want["Quarterly numbers"] = []string{"to_do"}
	if got := imapServer.keywordsOf(t, "INBOX"); !reflect.DeepEqual(got, want) {
		t.Errorf("after deleting Work, the server's keywords = %v", got)
	}
	if after := imapServer.mailOf(t, "INBOX"); !reflect.DeepEqual(withoutKeywords(after), withoutKeywords(before)) || len(after) != 3 {
		t.Errorf("deleting a label changed mail on the server:\n got %+v\nwas %+v", after, before)
	}
	for _, mailbox := range []string{"Sent", "Archive", "Trash"} {
		if got := imapServer.mailOf(t, mailbox); len(got) != 0 {
			t.Errorf("%s has %v", mailbox, got)
		}
	}
	if tt.HasText("Work") || a.folder != "INBOX" {
		t.Errorf("after deleting: folder %q, %q", a.folder, tt.Texts())
	}

	// label: finds the numbers by the label left, its name in any case.
	click(t, tt, "Search mail")
	tt.Type(`label:"to do"`)
	if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Quarterly numbers"}) {
		t.Errorf("label: found %v", got)
	}
	if !tt.HasText(locales.FilterLabelValue("To Do")) {
		t.Errorf("no chip: %q", tt.Texts())
	}
}

// An account whose server keeps no keywords, as Outlook's: its label is
// kept on this device only, which the sidebar and the note as it goes on
// say, and which no server hears of; it stays on the message across a
// restart.
func TestLabelsKeptOnDeviceEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	server, err := fakemail.StartIMAP(fakemail.IMAPOptions{
		Cert: localCert(t), Username: "ann", Password: "secret", Logger: serverLog{t},
		Mailboxes: []string{"INBOX", "Sent", "Trash"}, KeepsNoKeywords: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	imapServer := &localIMAP{addr: server.Addr, user: server.User}
	putLabelMail(t, imapServer)
	host, port := hostPort(t, imapServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
	}
	engine := newLabelEngine(t)
	a, svc := engine.open()
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	set := mail.DefaultSettings()
	set.Conversations = false
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	tt := startEngineApp(t, a)

	click(t, tt, locales.NewLabel())
	focusName(t, tt, "Receipts")
	click(t, tt, locales.CreateLabel())
	if item := sidebarLabel(t, tt, "Receipts"); item.Name != "Receipts "+locales.LabelLocal() || item.Description != locales.LabelLocalDetail("Receipts") {
		t.Errorf("the label's entry = %q, %q", item.Name, item.Description)
	}
	click(t, tt, "Lunch on Friday")
	openPicker(t, a, tt)
	clickRole(t, tt, ui.RoleCheckBox, "Receipts")
	if a.err != "" || a.labels.note != locales.LabelKeptHereNote("Receipts", 1) {
		t.Fatalf("labelling: %q, note %q", a.err, a.labels.note)
	}
	for subject, keywords := range imapServer.keywordsOf(t, "INBOX") {
		if len(keywords) > 0 {
			t.Errorf("a label kept here reached the server: %s has %v", subject, keywords)
		}
	}
	check := func(a *app, tt *ui.Tester, when string) {
		t.Helper()
		openLabel(t, tt, "Receipts")
		if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Lunch on Friday"}) {
			t.Errorf("%s: Receipts lists %v", when, got)
		}
		if !hasLabel(tt, locales.LabelOnDevice("Receipts")) {
			t.Errorf("%s: no chip says it is on this device only: %q", when, tt.Texts())
		}
	}
	check(a, tt, "labelled")
	a, _ = engine.open()
	tt = startEngineApp(t, a)
	check(a, tt, "after a restart")
}

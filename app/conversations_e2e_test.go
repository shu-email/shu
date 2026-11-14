package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// flagsOf reads the subjects of a mailbox's messages with whether they
// are read, as another mail client sees them.
func (s *localIMAP) flagsOf(t *testing.T, mailbox string) map[string]bool {
	t.Helper()
	c, selected := s.open(t, mailbox)
	defer c.Close()
	out := map[string]bool{}
	if selected.NumMessages == 0 {
		return out
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	fetched, err := c.Fetch(set, &imap.FetchOptions{Flags: true, Envelope: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range fetched {
		out[m.Envelope.Subject+" "+m.Envelope.Date.Format(time.TimeOnly)] = slices.Contains(m.Flags, imap.FlagSeen)
	}
	return out
}

// The acceptance criteria of moving a conversation end to end: the
// window on the real engine and a database on disk, against a local IMAP
// server holding a Gmail conversation, its question and the answer to it
// in the inbox and the user's reply in Sent. The inbox shows it as one
// row that opens on all three; archiving it moves exactly its two inbox
// messages, and Undo brings them back.
func TestConversationsEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Archive", "Trash")
	for _, put := range [][2]string{{"INBOX", "gmail-1.eml"}, {"Sent", "gmail-2.eml"}, {"INBOX", "gmail-3.eml"}} {
		raw, err := os.ReadFile(filepath.Join("..", "testdata", "threads", put[1]))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := imapServer.user.Append(put[0], appendBody{bytes.NewReader(raw)}, &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	host, port := hostPort(t, imapServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
	}
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a local test server
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	tt := ui.NewTester(a.view, 1220, 800)

	// One row, of the two inbox messages, which the account's sync
	// threaded with the reply in Sent.
	if len(a.convs) != 1 {
		t.Fatalf("rows = %+v", a.convs)
	}
	row := a.convs[0]
	if row.Count != 2 || !row.HasAttachments || row.Latest.Subject != "Re: Venue for the offsite" {
		t.Errorf("row = %+v", row)
	}
	if a.conv == nil || len(a.conv.msgs) != 3 {
		t.Fatalf("open conversation = %+v", a.conv)
	}
	var folders []string
	for _, m := range a.conv.msgs {
		folders = append(folders, m.Folder)
	}
	if !slices.Equal(folders, []string{"INBOX", "Sent", "INBOX"}) {
		t.Errorf("shown from %v", folders)
	}
	if !tt.HasText(locales.ConversationScope(2, locales.Inbox())+" · "+locales.ConversationElsewhere(1, locales.Sent())) ||
		!tt.HasText("Booked the lakeside room for Thursday.") {
		t.Errorf("the conversation is not on screen: %q", tt.Texts())
	}
	// Opening it marked its unread inbox messages read on the server; the
	// reply, collapsed, was not opened.
	seen := imapServer.flagsOf(t, "INBOX")
	if len(seen) != 2 {
		t.Fatalf("the server's inbox = %v", seen)
	}
	for subject, read := range seen {
		if !read {
			t.Errorf("%q is unread on the server", subject)
		}
	}

	click(t, tt, locales.ArchiveConversations(1))
	if a.err != "" {
		t.Fatalf("archive: %s", a.err)
	}
	if inbox, archive, sent := len(imapServer.messages(t, "INBOX")), len(imapServer.messages(t, "Archive")), len(imapServer.messages(t, "Sent")); inbox != 0 || archive != 2 || sent != 1 {
		t.Fatalf("after archiving: inbox %d, archive %d, sent %d", inbox, archive, sent)
	}
	if len(a.convs) != 0 || !tt.HasText(locales.ConversationsArchived(1)) {
		t.Fatalf("rows %d: %q", len(a.convs), tt.Texts())
	}

	click(t, tt, locales.Undo())
	if a.err != "" {
		t.Fatalf("undo: %s", a.err)
	}
	if inbox, archive := len(imapServer.messages(t, "INBOX")), len(imapServer.messages(t, "Archive")); inbox != 2 || archive != 0 {
		t.Fatalf("after the undo: inbox %d, archive %d", inbox, archive)
	}
	if len(a.convs) != 1 || a.convs[0].Count != 2 || !strings.HasPrefix(a.convs[0].Latest.Subject, "Re: Venue") {
		t.Fatalf("rows after the undo = %+v", a.convs)
	}
	if a.conv == nil || len(a.conv.msgs) != 3 {
		t.Errorf("the conversation after the undo = %+v", a.conv)
	}
}

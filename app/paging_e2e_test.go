package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"maps"
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

// serverFlags reads the flags of a mailbox's messages by UID, as another
// mail client sees them.
func (s *localIMAP) serverFlags(t *testing.T, mailbox string) map[imap.UID]string {
	t.Helper()
	c, selected := s.open(t, mailbox)
	defer c.Close()
	out := map[imap.UID]string{}
	if selected.NumMessages == 0 {
		return out
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	fetched, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range fetched {
		var flags []string
		for _, f := range m.Flags {
			flags = append(flags, string(f))
		}
		slices.Sort(flags)
		out[m.UID] = strings.Join(flags, " ")
	}
	return out
}

// The acceptance criteria of browsing older mail end to end: the window
// on the real engine and a database on disk, against a local IMAP server
// whose inbox holds more mail than the first sync keeps. The list pages
// to the end of what the cache holds, says so, and fetches the older
// mail from the server only when asked, until the folder is all on this
// device; the server's flags stay as they were.
func TestOlderMailEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	const total, window = 130, 50
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Archive", "Trash")
	for i := 1; i <= total; i++ {
		raw := fmt.Sprintf("From: Sender %d <sender%d@example.com>\r\nTo: Ann <ann@example.com>\r\nSubject: Message %d\r\n"+
			"Date: %s\r\nMessage-ID: <n%d@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nThe body of message %d.\r\n",
			i%7, i%7, i, time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC).Format(time.RFC1123Z), i, i)
		var flags []imap.Flag
		if i%3 == 0 {
			flags = append(flags, imap.FlagSeen)
		}
		if i%11 == 0 {
			flags = append(flags, imap.FlagFlagged)
		}
		if _, err := imapServer.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now(), Flags: flags}); err != nil {
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
	set := mail.DefaultSettings()
	set.Conversations, set.SyncWindow = false, window
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	tt := ui.NewTester(a.view, 1220, 800)
	// Opening the folder opened, and marked read, its newest message:
	// from here on the server's flags are what they are.
	flags := imapServer.serverFlags(t, "INBOX")
	if len(flags) != total {
		t.Fatalf("the server's inbox has %d messages", len(flags))
	}

	// The first sync kept the window, which the list reads to its end.
	if len(a.all) != window || !tt.HasText(locales.OnDeviceSummary(window, total, a.folderUnread("INBOX"))) {
		t.Fatalf("%d rows: %q", len(a.all), tt.Texts())
	}
	scrollToLast(t, a, tt)
	if !tt.HasText(locales.NoMoreHere()) || !tt.HasText(locales.LoadOlder()) {
		t.Fatalf("the end of the cache: %q", tt.Texts())
	}

	// Fetched on demand, a window at a time, until the folder is here.
	for fetches := 1; ; fetches++ {
		if fetches > 3 {
			t.Fatalf("still not all here: %d rows: %q", len(a.all), tt.Texts())
		}
		click(t, tt, locales.LoadOlder())
		if a.err != "" || a.olderAt("a1", "INBOX").err != "" {
			t.Fatalf("fetch %d: %s %s", fetches, a.err, a.olderAt("a1", "INBOX").err)
		}
		scrollToLast(t, a, tt)
		if want := min(total, (fetches+1)*window); len(a.all) != want {
			t.Fatalf("after fetch %d: %d rows, want %d", fetches, len(a.all), want)
		}
		if tt.HasText(locales.AllOnDevice()) {
			break
		}
	}
	for i, m := range a.all {
		if want := fmt.Sprintf("Message %d", total-i); m.Subject != want || m.Account != "a1" {
			t.Fatalf("row %d is %q of %q, want %q", i, m.Subject, m.Account, want)
		}
	}
	if !tt.HasText(locales.MailboxSummary(total, a.folderUnread("INBOX"))) || tt.HasText(locales.LoadOlder()) {
		t.Errorf("all here: %q", tt.Texts())
	}
	if cache, err := svc.FolderCache(ctx, "a1", "INBOX"); err != nil || !cache.Complete || cache.Cached != total {
		t.Errorf("the cache of the inbox = %+v, %v", cache, err)
	}
	// Paging and fetching changed no flag on the server.
	if after := imapServer.serverFlags(t, "INBOX"); !maps.Equal(after, flags) {
		for uid, f := range after {
			if flags[uid] != f {
				t.Errorf("UID %d: flags %q, were %q", uid, f, flags[uid])
			}
		}
	}
}

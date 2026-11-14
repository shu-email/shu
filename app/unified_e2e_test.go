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

// subjectsOf are the rows of the list as account and subject.
func subjectsOf(a *app) []string {
	out := make([]string, len(a.all))
	for i, m := range a.all {
		out[i] = m.Account + " " + m.Subject
	}
	return out
}

// The acceptance criteria of the unified inbox and of searching every
// account end to end: the window on the real engine and a database on
// disk, against two local IMAP servers, one for each account. The unified
// inbox lists both inboxes, the second account's never opened before;
// starring and archiving a row of the second account changes its server
// alone, and so does taking the archive back; a search of every account
// finds a message on the second server and says why.
func TestUnifiedEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	// Home's folders of a special use have other names than Work's, which
	// the engine knows them by: mail of Home's moved by Work's folders
	// would go to a folder Home does not have.
	work := startLocalIMAP(t, "INBOX", "Sent", "Archive", "Junk", "Trash")
	home := startLocalIMAP(t, "INBOX", "Sent", "All Mail", "Spam", "Deleted Items")
	base := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	put := func(s *localIMAP, from, subject, body string, hours int, flags ...imap.Flag) {
		t.Helper()
		raw := fmt.Sprintf("From: %s\r\nTo: Ann <ann@example.com>\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@example.com>\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, subject,
			base.Add(-time.Duration(hours)*time.Hour).Format(time.RFC1123Z), strings.ReplaceAll(strings.ToLower(subject), " ", "-"), body)
		if _, err := s.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now(), Flags: flags}); err != nil {
			t.Fatal(err)
		}
	}
	put(work, "Maya Chen <maya@example.com>", "Quarterly budget review", "The forecast for the third quarter is in.", 1)
	// Read already: it is the row that opens in the place of the one
	// archived, as in any list, which changes nothing on Work's server.
	put(work, "Priya Nair <priya@example.com>", "Lunch on Friday", "Shall we try the new place by the river?", 5, imap.FlagSeen)
	put(home, "Sam Lee <sam@example.com>", "Dinner on Saturday", "We could cook at ours this time.", 2)
	put(home, "Oak and Sons <quotes@oak.example>", "Garden pergola quote", "Cedar, as we discussed on site.", 8, imap.FlagSeen)

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	for _, acc := range []struct {
		id, name string
		server   *localIMAP
	}{{"a1", "Work", work}, {"a2", "Home", home}} {
		host, port := hostPort(t, acc.server.addr)
		account := mail.Account{
			ID: acc.id, Name: acc.name, Email: "ann@" + strings.ToLower(acc.name) + ".example", Username: "ann",
			IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
			SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		}
		if err := svc.AddAccount(ctx, account, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	set := mail.DefaultSettings()
	set.Conversations = false
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" || a.active != "a1" {
		t.Fatalf("start: %q, account %q", a.err, a.active)
	}
	tt := ui.NewTester(a.view, 1220, 800)
	// Work's inbox opened on its newest message, read from here on.
	workFlags := work.serverFlags(t, "INBOX")

	click(t, tt, locales.AllInboxes())
	if a.err != "" {
		t.Fatalf("the unified inbox: %s", a.err)
	}
	want := []string{"a1 Quarterly budget review", "a2 Dinner on Saturday", "a1 Lunch on Friday", "a2 Garden pergola quote"}
	if got := subjectsOf(a); !slices.Equal(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	if !tt.HasText(locales.AccountUnread("Work", 0)+" · "+locales.AccountUnread("Home", 1)) || countText(tt, locales.InAccount("Home")) != 2 {
		t.Errorf("the unified inbox: %q", tt.Texts())
	}
	homeFlags := home.serverFlags(t, "INBOX")

	// Home's row, starred and archived.
	click(t, tt, "Dinner on Saturday")
	click(t, tt, locales.StarMessage())
	click(t, tt, locales.ArchiveMessage())
	if a.err != "" {
		t.Fatalf("star and archive: %s", a.err)
	}
	if after := work.serverFlags(t, "INBOX"); !maps.Equal(after, workFlags) || len(work.messages(t, "Archive")) != 0 {
		t.Errorf("Work's server changed: inbox %v, was %v; archive %d", after, workFlags, len(work.messages(t, "Archive")))
	}
	archived := home.serverFlags(t, "All Mail")
	if len(archived) != 1 || len(home.messages(t, "INBOX")) != 1 {
		t.Fatalf("Home's server: All Mail %v, inbox %d", archived, len(home.messages(t, "INBOX")))
	}
	for _, flags := range archived {
		if flags != `\Flagged \Seen` {
			t.Errorf("the archived message's flags on Home's server: %q", flags)
		}
	}
	if got := subjectsOf(a); slices.Contains(got, "a2 Dinner on Saturday") || len(got) != 3 {
		t.Errorf("rows after archiving: %v", got)
	}

	click(t, tt, locales.Undo())
	if a.err != "" {
		t.Fatalf("undo: %s", a.err)
	}
	if len(home.messages(t, "INBOX")) != 2 || len(home.messages(t, "All Mail")) != 0 {
		t.Errorf("Home's server after the undo: inbox %d, All Mail %d", len(home.messages(t, "INBOX")), len(home.messages(t, "All Mail")))
	}
	if after := work.serverFlags(t, "INBOX"); !maps.Equal(after, workFlags) {
		t.Errorf("Work's server changed with the undo: %v, was %v", after, workFlags)
	}
	if got := subjectsOf(a); !slices.Contains(got, "a2 Dinner on Saturday") {
		t.Errorf("rows after the undo: %v", got)
	}
	// Only Home's message changed on Home's server, as starred and read.
	for uid, flags := range home.serverFlags(t, "INBOX") {
		if was, ok := homeFlags[uid]; ok && was != flags {
			t.Errorf("Home's message %d: %q, was %q", uid, flags, was)
		}
	}
	// Deleted, it goes to Home's Deleted Items, and comes back.
	click(t, tt, "Dinner on Saturday")
	click(t, tt, locales.DeleteMessage())
	if a.err != "" || len(home.messages(t, "Deleted Items")) != 1 || len(work.messages(t, "Trash")) != 0 {
		t.Fatalf("delete: %q, Home's Deleted Items %d, Work's Trash %d", a.err, len(home.messages(t, "Deleted Items")), len(work.messages(t, "Trash")))
	}
	click(t, tt, locales.Undo())
	if a.err != "" || len(home.messages(t, "Deleted Items")) != 0 || len(home.messages(t, "INBOX")) != 2 {
		t.Fatalf("delete undone: %q, Home's Deleted Items %d", a.err, len(home.messages(t, "Deleted Items")))
	}

	// From Work's inbox, a search of Work finds nothing; of every account,
	// the quote on Home's server, which says where the word was.
	click(t, tt, locales.Inbox())
	click(t, tt, "Search mail")
	tt.Type("pergola")
	if len(a.all) != 0 || !tt.HasText(locales.SearchedWhere(locales.WhereAccount("Work"))) {
		t.Fatalf("Work alone: %v: %q", subjectsOf(a), tt.Texts())
	}
	click(t, tt, locales.SearchAllAccounts())
	if got := subjectsOf(a); !slices.Equal(got, []string{"a2 Garden pergola quote"}) {
		t.Fatalf("every account: %v", got)
	}
	if m := a.all[0]; !m.Matched.Has(mail.FieldSubject) || !tt.HasText(matchedLine(m.Matched)) || !tt.HasText(locales.InAccount("Home")) {
		t.Errorf("the reason: %v: %q", m.Matched, tt.Texts())
	}
	click(t, tt, "Garden pergola quote")
	if !tt.HasText(locales.InAccountFolder("Home", locales.Inbox())) || !tt.HasText("Cedar, as we discussed on site.") {
		t.Errorf("Home's quote open: %q", tt.Texts())
	}

	// From Work's inbox opened again, with its own scope, account: names
	// the account to search: the engine reads the rest, and the scope
	// widens to cover that account.
	search(a, tt, "")
	click(t, tt, locales.Sent())
	click(t, tt, locales.Inbox())
	search(a, tt, "account:home pergola")
	if got := subjectsOf(a); !slices.Equal(got, []string{"a2 Garden pergola quote"}) || scopeChosen(tt) != locales.AllAccounts() || a.err != "" {
		t.Fatalf("account:home: %v, scope %q, %q", got, scopeChosen(tt), a.err)
	}
	if !tt.HasText(locales.FilterAccountValue("home")) || !tt.HasText(locales.MessageCount(1)+" · "+locales.WhereAccount("Home")) {
		t.Errorf("account:home says: %q", tt.Texts())
	}
	search(a, tt, "account:work pergola")
	if got := subjectsOf(a); len(got) != 0 || scopeChosen(tt) != locales.AllFolders() ||
		!tt.HasText(locales.SearchedWhere(locales.WhereAccount("Work"))) || a.err != "" {
		t.Errorf("account:work: %v, scope %q, %q: %q", got, scopeChosen(tt), a.err, tt.Texts())
	}
}

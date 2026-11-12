package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
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

// onServer lists the subjects of a mailbox, sorted, as another mail client
// sees them.
func (s *localIMAP) onServer(t *testing.T, mailbox string) []string {
	t.Helper()
	out := []string{}
	for subject := range s.mailOf(t, mailbox) {
		out = append(out, subject)
	}
	slices.Sort(out)
	return out
}

// The acceptance criteria of rules end to end: the window on the real
// engine and a database on disk, against a local IMAP server whose mail
// the account's watcher receives. An old newsletter is in the inbox. A
// rule that moves the newsletter's sender to a "Newsletters" folder is
// written in the window: its preview lists the old one, and saving moves
// nothing. A newsletter that arrives is moved on the server and not
// announced, while other new mail is, and the old one stays; Undo in the
// log brings it back, and the watcher does not sort it away again. "Pause
// and undo everything" leaves the rule paused, the mail where it was, and
// a newsletter that arrives then stays. Turned on again, the rule is
// applied once from its preview, which moves the old one, and that
// entry's undo brings it back.
func TestRulesEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Newsletters", "Sent", "Trash")
	minute := 0
	deliver := func(subject, from string) {
		t.Helper()
		id := strings.ReplaceAll(strings.ToLower(subject), " ", ".") + "@example.org"
		raw := fmt.Sprintf("From: %s\r\nTo: Ann <ann@example.com>\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s>\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n\r\nThe text of %s.\r\n",
			from, subject, time.Date(2026, 3, 2, 9, minute, 0, 0, time.UTC).Format(time.RFC1123Z), id, subject)
		minute++
		if _, err := imapServer.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	const shop = "Shop News <news@shop.example>"
	deliver("Weekly news 1", shop)
	deliver("Lunch on Friday", "Bob <bob@example.org>")
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
	// The watchers and the rules' goroutines end before the database
	// closes.
	defer svc.StopWatching()
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	set := mail.DefaultSettings()
	set.Conversations, set.NotifyDetails = false, true
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(svc)
	a.watches = true
	a.setLocale = func(string) {}
	var notes []note
	a.notify = func(n note) error {
		notes = append(notes, n)
		return nil
	}
	a.focused = func() bool { return false }
	// The watcher and the rules report from goroutines of the engine:
	// what they and the jobs post is run here, in order.
	queue := make(uiQueue, 512)
	a.update = func(f func()) { queue <- f }
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	queue.until(t, "the first check of the inbox", func() bool {
		tt.Frame()
		return a.watch["a1"].State == mail.WatchUpToDate && len(a.all) == 2 && a.folderExists("Newsletters")
	})
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	logOf := func() []mail.RuleActivity {
		t.Helper()
		page, err := svc.RuleActivity(ctx, mail.ActivityQuery{})
		if err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	entryOf := func(subject string, manual bool) (mail.RuleActivity, bool) {
		for _, e := range logOf() {
			if e.Subject == subject && e.Manual == manual {
				return e, true
			}
		}
		return mail.RuleActivity{}, false
	}
	// checked has the inbox synced after a move into it, as the window
	// syncs it, and then checked twice by the watcher: the second check
	// began after the first ended, so after the sync, and whatever the
	// move brought has been sorted, or not, and announced, or not.
	checked := func(what string) {
		t.Helper()
		if err := svc.SyncFolder(ctx, "a1", "INBOX", 0); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for range 2 {
			was := a.watch["a1"].CheckedAt
			svc.Nudge()
			queue.until(t, what, func() bool { return a.watch["a1"].CheckedAt.After(was) })
		}
	}
	// arrive delivers mail and has the watcher check, whether or not the
	// server has pushed it yet.
	arrive := func(msgs ...[2]string) {
		t.Helper()
		for _, m := range msgs {
			deliver(m[0], m[1])
		}
		svc.Nudge()
	}

	// The rule, written in the window: its preview lists the old one.
	click(t, tt, locales.Rules())
	click(t, tt, locales.NewRule())
	tt.Type("Newsletters")
	typeIn(t, tt, locales.ValueOf(1), "news@shop.example")
	choose(t, tt, locales.ActionFolderOf(1), "Newsletters")
	queue.until(t, "the preview", func() bool {
		tt.Frame()
		return tt.HasText(locales.RulePreviewCount(1)) && tt.HasText("Weekly news 1")
	})
	if !tt.HasText(locales.RuleNewMailOnly()) {
		t.Errorf("the preview does not say saving changes nothing: %q", tt.Texts())
	}
	click(t, tt, locales.SaveRule())
	queue.until(t, "the rule saved", func() bool {
		tt.Frame()
		return tt.HasText(locales.RuleSaved()) && tt.HasText(locales.ApplyRuleNow(1))
	})
	rules, err := svc.Rules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Name != "Newsletters" || rules[0].Account() != "a1" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, []string{"Lunch on Friday", "Weekly news 1"}) {
		t.Fatalf("saving changed the inbox: %v", got)
	}
	if got := imapServer.onServer(t, "Newsletters"); len(got) != 0 || len(logOf()) != 0 {
		t.Fatalf("saving moved %v, logged %+v", got, logOf())
	}
	click(t, tt, locales.CloseRuleEditor())
	click(t, tt, locales.Done())

	// A newsletter arrives with other mail: the newsletter is moved and
	// not announced, the other is announced, the old one stays.
	arrive([2]string{"Weekly news 2", shop}, [2]string{"Minutes", "Carol <carol@example.org>"})
	queue.until(t, "the newsletter moved and the minutes announced", func() bool {
		tt.Frame()
		e, ok := entryOf("Weekly news 2", false)
		return ok && e.State == mail.ActivityApplied && len(notes) > 0 &&
			slices.Contains(rowSubjects(a), "Minutes") && !slices.Contains(rowSubjects(a), "Weekly news 2")
	})
	checked("a check after the newsletter moved")
	if got := imapServer.onServer(t, "Newsletters"); !slices.Equal(got, []string{"Weekly news 2"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, []string{"Lunch on Friday", "Minutes", "Weekly news 1"}) {
		t.Errorf("the inbox holds %v", got)
	}
	if len(notes) != 1 || notes[0].Body != "Minutes" {
		t.Errorf("notifications = %+v, want one of the minutes", notes)
	}
	for _, n := range notes {
		if strings.Contains(n.Body, "Weekly news") {
			t.Errorf("a newsletter was announced: %+v", n)
		}
	}

	// Undo in the log brings it back, and it stays.
	click(t, tt, locales.Rules())
	click(t, tt, locales.ActivityTab())
	queue.until(t, "the log", func() bool {
		tt.Frame()
		return tt.HasText("Weekly news 2") && tt.HasText(locales.StateApplied())
	})
	click(t, tt, locales.UndoEntry("Weekly news 2"))
	queue.until(t, "the undo", func() bool {
		tt.Frame()
		e, _ := entryOf("Weekly news 2", false)
		return e.State == mail.ActivityUndone && tt.HasText(locales.UndoEntryDone("Weekly news 2"))
	})
	checked("a check after the newsletter came back")
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, []string{"Lunch on Friday", "Minutes", "Weekly news 1", "Weekly news 2"}) {
		t.Errorf("after the undo the inbox holds %v", got)
	}
	if got := imapServer.onServer(t, "Newsletters"); len(got) != 0 {
		t.Errorf("after the undo Newsletters holds %v", got)
	}
	if n := len(logOf()); n != 1 {
		t.Errorf("the newsletter that came back was sorted again: %+v", logOf())
	}
	if len(notes) != 1 {
		t.Errorf("the newsletter that came back was announced: %+v", notes)
	}

	// "Pause and undo everything": another newsletter arrives and is moved,
	// then the rule is paused and it comes back.
	arrive([2]string{"Weekly news 3", shop})
	queue.until(t, "the third newsletter moved", func() bool {
		tt.Frame()
		e, ok := entryOf("Weekly news 3", false)
		return ok && e.State == mail.ActivityApplied && tt.HasText("Weekly news 3")
	})
	click(t, tt, locales.EntryActions("Weekly news 3"))
	if err := tt.ChooseMenuItem(locales.PauseAndUndoNamed("Newsletters")); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(locales.PauseUndoTitle("Newsletters")) {
		t.Fatalf("no question before pausing and undoing: %q", tt.Texts())
	}
	click(t, tt, locales.PauseUndoButton())
	queue.until(t, "the rule paused and undone", func() bool {
		tt.Frame()
		return tt.HasText(locales.PauseUndoDone("Newsletters", 1))
	})
	checked("a check after the third came back")
	if len(notes) != 1 {
		t.Errorf("a newsletter that came back was announced: %+v", notes)
	}
	if rules, err := svc.Rules(ctx); err != nil || len(rules) != 1 || !rules[0].Paused {
		t.Errorf("the rule after pausing = %+v, %v", rules, err)
	}
	inbox := []string{"Lunch on Friday", "Minutes", "Weekly news 1", "Weekly news 2", "Weekly news 3"}
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, inbox) {
		t.Errorf("after pausing and undoing the inbox holds %v", got)
	}
	for _, e := range logOf() {
		if e.State != mail.ActivityUndone {
			t.Errorf("%s is %s after the rule's undo", e.Subject, e.State)
		}
	}
	// Paused, it leaves the mail that arrives alone, which is announced.
	notesBefore, logged := len(notes), len(logOf())
	arrive([2]string{"Weekly news 4", shop})
	queue.until(t, "the fourth newsletter announced", func() bool { return len(notes) > notesBefore })
	if got := imapServer.onServer(t, "INBOX"); !slices.Contains(got, "Weekly news 4") || len(logOf()) != logged {
		t.Errorf("a paused rule sorted: the inbox holds %v, the log %d entries, was %d", got, len(logOf()), logged)
	}

	// Turned on again and applied once from its preview: the old one, and
	// the others in the inbox, move; that entry's undo brings it back.
	click(t, tt, locales.RulesTab())
	clickRole(t, tt, ui.RoleSwitch, locales.UseRule("Newsletters"))
	queue.until(t, "the rule on", func() bool {
		tt.Frame()
		rules, err := svc.Rules(ctx)
		return err == nil && len(rules) == 1 && !rules[0].Paused && !tt.HasText(locales.RulePaused())
	})
	click(t, tt, locales.EditRule("Newsletters"))
	queue.until(t, "the preview of the rule saved", func() bool {
		tt.Frame()
		return tt.HasText(locales.ApplyRuleNow(4))
	})
	click(t, tt, locales.ApplyRuleNow(4))
	click(t, tt, locales.ApplyRuleButton(4))
	queue.until(t, "the run", func() bool {
		tt.Frame()
		return tt.HasText(locales.RuleRanDone(4))
	})
	if got := imapServer.onServer(t, "Newsletters"); !slices.Equal(got, []string{"Weekly news 1", "Weekly news 2", "Weekly news 3", "Weekly news 4"}) {
		t.Errorf("after the run Newsletters holds %v", got)
	}
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, []string{"Lunch on Friday", "Minutes"}) {
		t.Errorf("after the run the inbox holds %v", got)
	}
	click(t, tt, locales.ShowRuleActivity())
	queue.until(t, "the run in the log", func() bool {
		tt.Frame()
		return tt.HasText(locales.EntryManual(locales.ActMoveText("Newsletters"))) && tt.HasText("Weekly news 1")
	})
	entries, announced := len(logOf()), len(notes)
	click(t, tt, locales.UndoEntry("Weekly news 1"))
	queue.until(t, "the run's undo", func() bool {
		tt.Frame()
		e, _ := entryOf("Weekly news 1", true)
		return e.State == mail.ActivityUndone && tt.HasText(locales.UndoEntryDone("Weekly news 1"))
	})
	checked("a check after the old one came back")
	if len(notes) != announced {
		t.Errorf("the old one that came back was announced: %+v", notes[announced:])
	}
	if got := imapServer.onServer(t, "INBOX"); !slices.Equal(got, []string{"Lunch on Friday", "Minutes", "Weekly news 1"}) {
		t.Errorf("after the run's undo the inbox holds %v", got)
	}
	if got := imapServer.onServer(t, "Newsletters"); slices.Contains(got, "Weekly news 1") {
		t.Errorf("after the run's undo Newsletters holds %v", got)
	}
	if n := len(logOf()); n != entries {
		t.Errorf("the old one that came back was sorted again: %d entries, were %d", n, entries)
	}
}

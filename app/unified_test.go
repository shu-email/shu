package app

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// twoBase is the time the two accounts' mail is dated from.
var twoBase = time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

func hoursAgo(h int) time.Time { return twoBase.Add(-time.Duration(h) * time.Hour) }

var (
	workMaya = mail.Address{Name: "Maya Chen", Email: "maya@example.com"}
	annWork  = mail.Address{Name: "Ann", Email: "ann@work.example"}
	annHome  = mail.Address{Name: "Ann", Email: "ann@home.example"}
)

// homeFolders are Home's folders, whose special uses have other names
// than Work's: mail of Home sent to a folder of Work's would go to one
// Home does not have.
func homeFolders() []mail.Folder {
	return []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "All Mail", SpecialUse: mail.SpecialArchive},
		{Name: "Spam", SpecialUse: mail.SpecialJunk},
		{Name: "Deleted Items", SpecialUse: mail.SpecialTrash},
	}
}

// twoAccountsService holds two accounts, Work and Home, whose inboxes both
// have a message 1. Work's budget conversation has its question in the
// inbox and the user's answer in Sent; Home keeps a quote about a pergola
// in its All Mail, its archive, as Work's Sent mentions one.
func twoAccountsService(conversations bool) *fakeService {
	fs := newFakeService()
	fs.settings.Conversations = conversations
	fs.addAccount(testAccount("a1", "Work", annWork.Email), "secret", testFolders(), []mail.Summary{
		{UID: 1, Folder: "INBOX", Thread: 7, MessageID: "<budget-1@example.com>", Subject: "Quarterly budget review",
			Preview: "The forecast for the third quarter is in.", From: workMaya, To: []mail.Address{annWork}, Date: hoursAgo(1)},
		{UID: 2, Folder: "INBOX", Subject: "Lunch on Friday?", Preview: "Shall we try the new place by the river?",
			From: mail.Address{Name: "Priya Nair", Email: "priya.nair@example.com"}, To: []mail.Address{annWork}, Date: hoursAgo(5)},
		{UID: 3, Folder: "Sent", Subject: "Re: Offsite venue", Preview: "The one with the pergola, please.",
			From: annWork, To: []mail.Address{{Name: "Lin Wei", Email: "lin@example.com"}}, Date: hoursAgo(26), Seen: true},
		{UID: 6, Folder: "Sent", Thread: 7, MessageID: "<budget-2@example.com>", Subject: "Re: Quarterly budget review",
			Preview: "Thanks, the forecast looks right to me.", From: annWork, To: []mail.Address{workMaya}, Date: hoursAgo(30), Seen: true},
		{UID: 4, Folder: "Archive", Subject: "Invoice for March", Preview: "Your invoice is attached.",
			From: mail.Address{Name: "Billing", Email: "billing@vendor.example"}, Date: hoursAgo(24 * 20), Seen: true, HasAttachments: true},
	})
	fs.addAccount(testAccount("a2", "Home", annHome.Email), "secret", homeFolders(), []mail.Summary{
		{UID: 1, Folder: "INBOX", Subject: "Dinner on Saturday", Preview: "We could cook at ours this time.",
			From: mail.Address{Name: "Sam Lee", Email: "sam@example.com"}, To: []mail.Address{annHome}, Date: hoursAgo(2)},
		{UID: 2, Folder: "INBOX", Subject: "Your flight confirmation", Preview: "Booking reference K7Q2, seat 14A.",
			From: mail.Address{Name: "Skyways", Email: "noreply@skyways.example"}, To: []mail.Address{annHome},
			Date: hoursAgo(8), Seen: true, Flagged: true, HasAttachments: true},
		{UID: 5, Folder: "All Mail", Subject: "Garden pergola quote", Preview: "Cedar, as we discussed on site.",
			From: mail.Address{Name: "Oak & Sons", Email: "quotes@oak.example"}, To: []mail.Address{annHome},
			Date: hoursAgo(24 * 3), Seen: true},
	})
	for _, id := range []string{"a1", "a2"} {
		for _, f := range fs.folders[id] {
			fs.recount(id, f.Name)
		}
	}
	return fs
}

// startTwo is a window on the two accounts, Work on screen.
func startTwo(t *testing.T, conversations bool) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	fs := twoAccountsService(conversations)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, fs, ui.NewTester(a.view, 1220, 800)
}

// rowKeys are the keys of the list's rows, in order.
func rowKeys(a *app) []string {
	out := make([]string, len(a.all))
	for i := range a.all {
		out[i] = a.rowKey(i)
	}
	return out
}

// actsLike are the calls that reached an account, of those whose names
// begin with prefix.
func actsLike(fs *fakeService, accountID, prefix string) []string {
	return slices.DeleteFunc(fs.actsOf(accountID), func(s string) bool { return !strings.HasPrefix(s, prefix) })
}

// countText counts the texts on screen that are exactly s.
func countText(tt *ui.Tester, s string) int {
	n := 0
	for _, text := range tt.Texts() {
		if text == s {
			n++
		}
	}
	return n
}

// The unified inbox is an entry of its own with two accounts, and none
// with one. It lists both inboxes, the two message 1s apart, newest
// first, each row with its account's name; its badge adds up the
// accounts' unread counts, which the line under its title gives one by
// one. A row of the second account opens as that account's.
func TestUnifiedInboxListsEveryAccount(t *testing.T) {
	one, _ := sampleApp(t)
	if tt := ui.NewTester(one.view, 1220, 800); tt.HasText(locales.AllInboxes()) {
		t.Error("one account has a unified inbox")
	}

	for _, conversations := range []bool{false, true} {
		a, fs, tt := startTwo(t, conversations)
		// Work's inbox opened on its newest message, which is read now:
		// each account has one unread message, and the entry's badge both.
		if a.unifiedUnread() != 2 || !slices.Contains(tt.Texts(), "2") || !tt.HasText(locales.AllInboxes()) {
			t.Fatalf("conversations=%v: the entry, want a badge of 2 of %v: %q", conversations, a.inboxes, tt.Texts())
		}
		click(t, tt, locales.AllInboxes())
		want := []string{"a1/INBOX/1", "a2/INBOX/1", "a1/INBOX/2", "a2/INBOX/2"}
		if conversations {
			want = []string{threadKey("a1", 7), threadKey("a2", a.convs[1].Thread), threadKey("a1", a.convs[2].Thread), threadKey("a2", a.convs[3].Thread)}
		}
		if got := rowKeys(a); !reflect.DeepEqual(got, want) {
			t.Fatalf("conversations=%v: rows %v, want %v", conversations, got, want)
		}
		for i, account := range []string{"Work", "Home", "Work", "Home"} {
			if got, folder := a.rowPlace(a.all[i]); got != account || folder != "" {
				t.Errorf("conversations=%v: row %d says %q %q", conversations, i, got, folder)
			}
		}
		// Each row names its account in words, which assistive technology
		// reads with the row.
		if countText(tt, locales.InAccount("Home")) != 2 || countText(tt, locales.InAccount("Work")) != 2 {
			t.Errorf("conversations=%v: account names %q", conversations, tt.Texts())
		}
		if label := a.list.Label(1); !strings.Contains(label, locales.InAccount("Home")) {
			t.Errorf("conversations=%v: row 1 reads %q", conversations, label)
		}
		counts := locales.AccountUnread("Work", 1) + " · " + locales.AccountUnread("Home", 1)
		if !tt.HasText(counts) || a.unifiedUnread() != 2 {
			t.Errorf("conversations=%v: counts %d: %q", conversations, a.unifiedUnread(), tt.Texts())
		}
		click(t, tt, "Dinner on Saturday")
		if !tt.HasText(locales.InAccountFolder("Home", locales.Inbox())) && !tt.HasText("Home · "+locales.ConversationScope(1, locales.Inbox())) {
			t.Errorf("conversations=%v: the pane does not say Home: %q", conversations, tt.Texts())
		}
		if !tt.HasText(locales.ToMe()) || !tt.HasText("Body of Dinner on Saturday") {
			t.Errorf("conversations=%v: Home's message: %q", conversations, tt.Texts())
		}
		if got := actsLike(fs, "a2", "Fetch"); len(got) != 1 || !strings.HasPrefix(got[0], "FetchMessage INBOX/1") {
			t.Errorf("conversations=%v: Home's bodies read: %v", conversations, got)
		}
		if got := actsLike(fs, "a1", "Fetch"); len(got) != 1 || !strings.HasPrefix(got[0], "FetchMessage INBOX/1") {
			t.Errorf("conversations=%v: Work's bodies read: %v", conversations, got)
		}
		if a.inboxes["a2"] != 0 || a.inboxes["a1"] != 1 || !tt.HasText(locales.AccountUnread("Work", 1)+" · "+locales.AccountUnread("Home", 0)) {
			t.Errorf("conversations=%v: counts after reading Home's: %v", conversations, a.inboxes)
		}
	}
}

// Starring, archiving and taking the archive back on a row of the unified
// inbox reach the row's account and no other: the message goes to that
// account's Archive and returns from it, and only that account's counts
// change.
func TestUnifiedActionsReachTheirAccount(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, fs, tt := startTwo(t, conversations)
		click(t, tt, locales.AllInboxes())
		click(t, tt, "Your flight confirmation")
		fs.acts = nil
		star, archive, unread := locales.UnstarMessage(), locales.ArchiveMessage(), locales.MarkUnread()
		if conversations {
			star, archive, unread = locales.UnstarConversations(1), locales.ArchiveConversations(1), locales.MarkConversationsUnread(1)
		}
		// The flight is starred; the button takes the star off, in Home.
		click(t, tt, star)
		if m, _ := fs.find("a2", "INBOX", 2); m.Flagged || len(fs.actsOf("a1")) != 0 || len(actsLike(fs, "a2", "SetFlag")) != 1 {
			t.Errorf("conversations=%v: unstar: flagged %v, calls %v", conversations, m.Flagged, fs.acts)
		}
		click(t, tt, unread)
		if a.inboxes["a2"] != 2 || a.inboxes["a1"] != 1 || len(fs.actsOf("a1")) != 0 {
			t.Errorf("conversations=%v: unread counts %v, calls %v", conversations, a.inboxes, fs.acts)
		}
		fs.acts = nil
		click(t, tt, archive)
		if _, ok := fs.find("a2", "All Mail", 1002); !ok || len(fs.messages[listKey("a1", "Archive")]) != 1 {
			t.Fatalf("conversations=%v: archived %v: %v", conversations, fs.acts, a.err)
		}
		if got := actsLike(fs, "a2", "Move"); len(got) != 1 || !strings.HasSuffix(got[0], "->All Mail") {
			t.Errorf("conversations=%v: archive calls %v", conversations, fs.acts)
		}
		// The row that took its place opens, as in any list: Work's lunch,
		// whose reading is all that reaches Work.
		if m := a.chosenMessage(); m.Account != "a1" || m.UID != 2 {
			t.Errorf("conversations=%v: opened after archiving: %+v", conversations, m)
		}
		for _, call := range fs.actsOf("a1") {
			if !strings.HasPrefix(call, "FetchMessage INBOX/2 ") && call != `SetFlags INBOX/2 \Seen true` {
				t.Errorf("conversations=%v: archiving Home's row reached Work: %q", conversations, call)
			}
		}
		if slices.Contains(rowKeys(a), "a2/INBOX/2") || a.inboxes["a2"] != 1 {
			t.Errorf("conversations=%v: after archiving: rows %v, counts %v", conversations, rowKeys(a), a.inboxes)
		}
		fs.acts = nil
		click(t, tt, locales.Undo())
		if _, ok := fs.find("a2", "INBOX", 2); !ok || len(fs.actsOf("a1")) != 0 || len(actsLike(fs, "a2", "MoveBack")) != 1 {
			t.Errorf("conversations=%v: undo calls %v", conversations, fs.acts)
		}
		if !slices.Contains(fs.syncCalls, "a2/INBOX/0") || len(a.all) != 4 || a.err != "" {
			t.Errorf("conversations=%v: after the undo: syncs %v, %d rows, %q", conversations, fs.syncCalls, len(a.all), a.err)
		}
		// Junk and Delete take it to Home's folders of those uses, by
		// Home's names for them, and the undo brings it back each time.
		junk, del := locales.JunkMessage(), locales.DeleteMessage()
		if conversations {
			junk, del = locales.JunkConversations(1), locales.DeleteConversations(1)
		}
		for _, c := range []struct{ label, dest string }{{junk, "Spam"}, {del, "Deleted Items"}} {
			click(t, tt, "Your flight confirmation")
			fs.acts = nil
			click(t, tt, c.label)
			got := actsLike(fs, "a2", "Move")
			if _, ok := fs.find("a2", c.dest, 1002); !ok || len(got) != 1 || !strings.HasSuffix(got[0], "->"+c.dest) || a.err != "" {
				t.Errorf("conversations=%v: %s: calls %v, error %q", conversations, c.label, fs.acts, a.err)
			}
			if got := actsLike(fs, "a1", "Move"); len(got) != 0 {
				t.Errorf("conversations=%v: %s reached Work: %v", conversations, c.label, got)
			}
			click(t, tt, locales.Undo())
			if _, ok := fs.find("a2", "INBOX", 2); !ok || a.err != "" {
				t.Errorf("conversations=%v: %s taken back: %v, %q", conversations, c.label, fs.acts, a.err)
			}
		}
	}
}

// Rows of both accounts chosen together are each acted on in their own
// account: one star and one archive for each, to its own Archive.
func TestUnifiedMultiSelection(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, fs, tt := startTwo(t, conversations)
		click(t, tt, locales.AllInboxes())
		if err := tt.ClickWith(ui.Cmd, "Dinner on Saturday"); err != nil {
			t.Fatal(err)
		}
		if a.sel.Len() != 2 {
			t.Fatalf("conversations=%v: chosen %v", conversations, chosenKeys(a))
		}
		fs.acts = nil
		star, archive := locales.StarMessage(), locales.ArchiveMessage()
		if conversations {
			star, archive = locales.StarConversations(2), locales.ArchiveConversations(2)
		}
		click(t, tt, star)
		for _, id := range []string{"a1", "a2"} {
			if m, _ := fs.find(id, "INBOX", 1); !m.Flagged || len(actsLike(fs, id, "SetFlag")) != 1 {
				t.Errorf("conversations=%v: %s's star: %v", conversations, id, fs.actsOf(id))
			}
		}
		click(t, tt, archive)
		for id, dest := range map[string]string{"a1": "Archive", "a2": "All Mail"} {
			got := actsLike(fs, id, "Move")
			if _, ok := fs.find(id, dest, 1001); !ok || len(got) != 1 || !strings.HasSuffix(got[0], "->"+dest) {
				t.Errorf("conversations=%v: %s's archive: %v: %v", conversations, id, fs.actsOf(id), a.err)
			}
		}
		if got := rowKeys(a); len(got) != 2 {
			t.Errorf("conversations=%v: rows after archiving: %v", conversations, got)
		}
	}
}

// A reply to a row of the unified inbox is written from the row's
// account, as is a forward.
func TestUnifiedReplyUsesItsAccount(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, _, tt := startTwo(t, conversations)
		click(t, tt, locales.AllInboxes())
		click(t, tt, "Dinner on Saturday")
		click(t, tt, locales.Reply())
		if cs := a.composes[len(a.composes)-1]; cs.accountID != "a2" || cs.to != "Sam Lee <sam@example.com>" {
			t.Errorf("conversations=%v: reply from %q to %q", conversations, cs.accountID, cs.to)
		}
		click(t, tt, locales.Forward())
		if cs := a.composes[len(a.composes)-1]; cs.accountID != "a2" {
			t.Errorf("conversations=%v: forward from %q", conversations, cs.accountID)
		}
	}
}

// A notification clicked while the unified inbox shows opens its message
// there, of whichever account, rather than the account's own inbox.
func TestUnifiedNotificationOpensThere(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, _, tt := startTwo(t, conversations)
		click(t, tt, locales.AllInboxes())
		a.showTarget(noteTarget{account: "a2", folder: "INBOX", validity: 7, uid: 2, message: true})
		tt.Frame()
		if a.folder != allInboxes || a.active != "a1" || a.aim != nil {
			t.Fatalf("conversations=%v: folder %q, account %q, wish %+v", conversations, a.folder, a.active, a.aim)
		}
		if m := a.chosenMessage(); m.Account != "a2" || m.UID != 2 || !tt.HasText("Body of Your flight confirmation") {
			t.Errorf("conversations=%v: opened %+v: %q", conversations, m, tt.Texts())
		}
	}
}

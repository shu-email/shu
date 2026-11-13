package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// watchedApp is a window whose accounts are watched, with what it asked
// of the system recorded.
type watchedApp struct {
	a  *app
	fs *fakeService
	// notes are the notifications shown, badge the Dock's count, and
	// raised how often the window was brought to the front.
	notes  []note
	badge  int
	raised int
	// focused is whether the window has the focus, and noteErr what the
	// system answers to a notification.
	focused bool
	noteErr error
}

// watchFolders is an account's folders with an inbox the watcher can
// name messages of.
func watchFolders(unread, total int) []mail.Folder {
	folders := testFolders()
	folders[0].UIDValidity = 7
	folders[0].Unread, folders[0].Total = unread, total
	return folders
}

// newWatchedService holds two accounts: Work, with the sample mail, and
// Home, with one read message.
func newWatchedService() *fakeService {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Work", "work@example.com"), "secret", watchFolders(2, 3), testMessages("a1"))
	fs.addAccount(testAccount("a2", "Home", "home@example.com"), "secret", watchFolders(0, 1), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Welcome home", Seen: true,
		From: mail.Address{Name: "Sam", Email: "sam@example.com"}, Date: time.Now().Add(-48 * time.Hour),
	}})
	return fs
}

// watchApp opens the window on a service, watching.
func watchApp(t *testing.T, fs *fakeService) *watchedApp {
	t.Helper()
	w := &watchedApp{fs: fs, badge: -1}
	w.a = newTestApp(fs)
	w.a.watches = true
	w.a.setLocale = func(string) {}
	w.a.setBadge = func(n int) { w.badge = n }
	w.a.notify = func(n note) error {
		if w.noteErr != nil {
			return w.noteErr
		}
		w.notes = append(w.notes, n)
		return nil
	}
	w.a.focused = func() bool { return w.focused }
	w.a.showWindow = func() { w.raised++ }
	return w
}

// startWatchApp is watchApp started on the two accounts.
func startWatchApp(t *testing.T) *watchedApp {
	t.Helper()
	w := watchApp(t, newWatchedService())
	w.a.start()
	return w
}

// startMessageWatchApp is startWatchApp with a row for each message, the
// view the tests written before conversations are about.
func startMessageWatchApp(t *testing.T) *watchedApp {
	t.Helper()
	w := watchApp(t, inMessageView(newWatchedService()))
	w.a.start()
	return w
}

// newMail is an unread message for an inbox.
func newMail(uid mail.UID, sender, subject string) mail.Summary {
	return mail.Summary{
		UID: uid, Folder: "INBOX", Subject: subject, Preview: "Preview of " + subject,
		From: mail.Address{Name: sender, Email: strings.ToLower(sender) + "@example.com"},
		Date: time.Now().Add(time.Duration(uid) * time.Second),
	}
}

// checked is the report of a check that worked, as the engine's watcher
// makes it: the folders as the service has them and what arrived, newest
// first.
func (f *fakeService) checked(accountID string, at time.Time, arrived ...mail.Summary) mail.WatchEvent {
	fresh := append([]mail.Summary(nil), arrived...)
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].UID > fresh[j].UID })
	return mail.WatchEvent{
		AccountID: accountID,
		Status:    mail.WatchStatus{State: mail.WatchUpToDate, CheckedAt: at},
		Folders:   append([]mail.Folder(nil), f.folders[accountID]...),
		Folder:    "INBOX",
		New:       fresh,
	}
}

// arrive delivers mail to an account's inbox and has its watcher report
// it. It returns the report.
func (w *watchedApp) arrive(t *testing.T, accountID string, arrived ...mail.Summary) mail.WatchEvent {
	t.Helper()
	key := listKey(accountID, "INBOX")
	w.fs.messages[key] = append(w.fs.messages[key], arrived...)
	sortSummaries(w.fs.messages[key])
	w.fs.recount(accountID, "INBOX")
	for i := range w.fs.folders[accountID] {
		if w.fs.folders[accountID][i].Name == "INBOX" {
			w.fs.folders[accountID][i].Total = len(w.fs.messages[key])
		}
	}
	ev := w.fs.checked(accountID, time.Now(), arrived...)
	w.report(t, ev)
	return ev
}

// report hands the window a watcher's report.
func (w *watchedApp) report(t *testing.T, ev mail.WatchEvent) {
	t.Helper()
	report := w.fs.watching[ev.AccountID]
	if report == nil {
		t.Fatalf("account %s is not watched", ev.AccountID)
	}
	report(ev)
}

// say hands the window a watcher's state.
func (w *watchedApp) say(t *testing.T, accountID string, status mail.WatchStatus) {
	t.Helper()
	w.report(t, mail.WatchEvent{AccountID: accountID, Status: status})
}

func inboxOf(fs *fakeService, accountID string) mail.Folder {
	for _, f := range fs.folders[accountID] {
		if f.IsInbox() {
			return f
		}
	}
	return mail.Folder{}
}

// The accounts are watched from the start, and mail that arrives for the
// folder on screen appears in the list without moving the choice, being
// opened or being marked read.
func TestNewMailAppearsInTheList(t *testing.T) {
	w := startWatchApp(t)
	a := w.a
	if want := []string{"watch a1", "watch a2"}; !reflect.DeepEqual(w.fs.watchLog, want) {
		t.Fatalf("watchers asked for = %v, want %v", w.fs.watchLog, want)
	}
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "[shu-email/shu] Pull request #128")
	chosen := a.selectedKey()
	flagged := len(w.fs.flagCalls)
	unread := a.folderUnread("INBOX")

	w.arrive(t, "a1", newMail(10, "Noor", "Lunch on Friday?"))
	tt.Frame()
	if !tt.HasText("Lunch on Friday?") {
		t.Fatalf("the new message is not in the list: %q", tt.Texts())
	}
	if a.all[0].UID != 10 || a.all[0].Seen {
		t.Errorf("the first row = %+v, want the new message, unread", a.all[0])
	}
	if got := a.selectedKey(); got != chosen {
		t.Errorf("the choice moved from %q to %q", chosen, got)
	}
	if a.sel.Len() != 1 || !a.sel.Has(chosen) {
		t.Errorf("the highlighted rows changed")
	}
	if len(w.fs.flagCalls) != flagged {
		t.Errorf("new mail changed flags: %v", w.fs.flagCalls[flagged:])
	}
	if got := a.folderUnread("INBOX"); got != unread+1 {
		t.Errorf("inbox unread = %d, want %d", got, unread+1)
	}
	if !tt.HasText(fmt.Sprint(unread + 1)) {
		t.Errorf("the sidebar does not show the count: %q", tt.Texts())
	}
	if len(w.fs.moved) != 0 {
		t.Errorf("new mail moved messages: %v", w.fs.moved)
	}
}

// With nothing chosen, new mail leaves nothing chosen; new mail pushing
// the rows down keeps the last one read, and chosen; and a chosen row
// that leaves the list is not replaced by its neighbour, which would open
// that message and mark it read.
func TestNewMailOpensNothing(t *testing.T) {
	fs := newFakeService()
	var msgs []mail.Summary
	for uid := mail.UID(1); uid <= listPage; uid++ {
		m := newMail(uid, "Sender", fmt.Sprintf("Message %d", uid))
		msgs = append(msgs, m)
	}
	fs.addAccount(testAccount("a1", "Work", "work@example.com"), "secret", watchFolders(listPage, listPage), msgs)
	w := watchApp(t, inMessageView(fs))
	a := w.a
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	// The oldest message, at the end of the page the list holds.
	a.chooseRow(len(a.all) - 1)
	a.reloadBody()
	tt.Frame()
	if a.selectedKey() != "a1/INBOX/1" {
		t.Fatalf("chosen = %q", a.selectedKey())
	}
	flagged := len(fs.flagCalls)

	w.arrive(t, "a1", newMail(200, "Noor", "One more"))
	tt.Frame()
	if len(a.all) != listPage+1 || a.selectedKey() != "a1/INBOX/1" || a.sel.Len() != 1 {
		t.Errorf("new mail pushed the chosen row out: %d rows, %q chosen", len(a.all), a.selectedKey())
	}

	// Another mail client deletes it.
	key := listKey("a1", "INBOX")
	fs.messages[key] = slices.DeleteFunc(fs.messages[key], func(m mail.Summary) bool { return m.UID == 1 })
	w.report(t, fs.checked("a1", time.Now()))
	tt.Frame()
	if a.selected != -1 || a.sel.Len() != 0 {
		t.Errorf("after the chosen row left, row %d is chosen (%d highlighted)", a.selected, a.sel.Len())
	}
	if len(fs.flagCalls) != flagged {
		t.Errorf("new mail marked a message read: %v", fs.flagCalls[flagged:])
	}

	// Nothing chosen stays nothing chosen.
	w.arrive(t, "a1", newMail(201, "Noor", "And another"))
	tt.Frame()
	if a.selected != -1 || len(fs.flagCalls) != flagged {
		t.Errorf("new mail chose row %d; flags %v", a.selected, fs.flagCalls[flagged:])
	}
	if !tt.HasText("And another") {
		t.Errorf("the list did not follow: %q", tt.Texts())
	}
}

// The Dock badge is the unread mail of every account's inbox, and the
// account menu says how much of it is in the accounts not on screen.
func TestBadgeAndMenuCountEveryAccount(t *testing.T) {
	fs := newWatchedService()
	key := listKey("a2", "INBOX")
	for uid := mail.UID(2); uid <= 5; uid++ {
		fs.messages[key] = append(fs.messages[key], newMail(uid, "Sam", fmt.Sprintf("Home mail %d", uid)))
	}
	sortSummaries(fs.messages[key])
	fs.recount("a2", "INBOX")
	w := watchApp(t, fs)
	a := w.a
	a.start()
	// Work shows its first message, which reading marked read: 1 of its
	// 2 unread is left, with Home's 4.
	if w.badge != 5 {
		t.Fatalf("badge = %d at the start, want 1 + 4", w.badge)
	}
	w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
	if w.badge != 6 {
		t.Errorf("badge = %d after mail for the other account, want 6", w.badge)
	}
	w.arrive(t, "a1", newMail(10, "Noor", "Lunch"))
	if w.badge != 7 {
		t.Errorf("badge = %d after mail for the account on screen, want 7", w.badge)
	}

	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.AccountsMenu())
	if !menuShows(tt, "Home · home@example.com", "5") {
		t.Errorf("the menu does not say Home has 5 unread: %q", tt.Texts())
	}
	// The active account's count is the sidebar's to show.
	if menuShows(tt, "Work · work@example.com", "2") {
		t.Errorf("the menu repeats the active account's count")
	}
	if err := tt.ChooseMenuItem("Home · home@example.com"); err != nil {
		t.Fatalf("menu = %v: %v", tt.Menu(), err)
	}
	// Opening the account shows its newest message, which reads it.
	if a.active != "a2" || w.badge != 6 {
		t.Fatalf("after switching: active %q, badge %d, want 6", a.active, w.badge)
	}
	// Reading on the other account lowers the sum.
	click(t, tt, "Home mail 5")
	if w.badge != 5 {
		t.Errorf("badge = %d after reading a message of Home, want 5", w.badge)
	}
	click(t, tt, locales.AccountsMenu())
	if !menuShows(tt, "Work · work@example.com", "2") {
		t.Errorf("the menu does not say Work has 2 unread: %q", tt.Texts())
	}
}

// menuShows reports whether the open menu has an item with a count at
// its end.
func menuShows(tt *ui.Tester, label, count string) bool {
	item, ok := tt.Find(label)
	if !ok {
		return false
	}
	for _, info := range tt.AccessAll() {
		if info.Name == label && strings.Contains(info.Description+" "+info.Value, count) {
			return true
		}
	}
	// Drawn on the item's line, to the right of its label.
	at, ok := tt.Find(count)
	return ok && at.X > item.X && at.Y+at.H > item.Y && at.Y < item.Y+item.H
}

// One message makes one notification, which by default says the account
// and a count and nothing of the mail.
func TestNotificationIsPrivateByDefault(t *testing.T) {
	w := startWatchApp(t)
	w.arrive(t, "a2", newMail(20, "Sam Rivera", "Your test results"))
	if len(w.notes) != 1 {
		t.Fatalf("notifications = %+v, want one", w.notes)
	}
	n := w.notes[0]
	if n.Title != "Home" || n.Body != "1 new message" || n.Subtitle != "" {
		t.Errorf("notification = %+v", n)
	}
	for _, text := range []string{n.Title, n.Subtitle, n.Body} {
		if strings.Contains(text, "Sam") || strings.Contains(text, "results") {
			t.Errorf("the notification says %q", text)
		}
	}
	if target, ok := parseNoteID(n.ID); !ok || !target.message || target.account != "a2" || target.folder != "INBOX" || target.uid != 20 || target.validity != 7 {
		t.Errorf("the notification's ID %q opens %+v", n.ID, target)
	}
	if n.Group != "a2" {
		t.Errorf("group = %q", n.Group)
	}

	// Several at once are one count, which opens the inbox.
	w.arrive(t, "a2", newMail(21, "Sam", "One"), newMail(22, "Kim", "Two"))
	if len(w.notes) != 2 {
		t.Fatalf("notifications = %+v, want one more", w.notes)
	}
	n = w.notes[1]
	if n.Title != "Home" || n.Body != "2 new messages" {
		t.Errorf("notification = %+v", n)
	}
	if target, ok := parseNoteID(n.ID); !ok || target.message || target.uid != 22 {
		t.Errorf("the count's ID %q opens %+v", n.ID, target)
	}
}

// Asked to, a notification names the sender and the subject. More than
// a few messages at once still arrive as one count.
func TestNotificationDetailsAreOptIn(t *testing.T) {
	fs := newWatchedService()
	fs.settings.NotifyDetails = true
	w := watchApp(t, fs)
	w.a.start()
	w.arrive(t, "a2", newMail(20, "Sam Rivera", "Your test\r\n results"))
	if len(w.notes) != 1 {
		t.Fatalf("notifications = %+v, want one", w.notes)
	}
	if n := w.notes[0]; n.Title != "Sam Rivera" || n.Body != "Your test results" || n.Subtitle != "Home" {
		t.Errorf("notification = %+v", n)
	}

	// A sender without a name shows by address, a message without a
	// subject says so, and the newest is shown last, to end on top.
	bare := newMail(22, "", "")
	bare.From = mail.Address{Email: "noreply@example.com"}
	w.notes = nil
	w.arrive(t, "a2", newMail(21, "Kim", "First"), bare, newMail(23, "Lee", "Third"))
	if len(w.notes) != 3 {
		t.Fatalf("notifications = %+v, want three", w.notes)
	}
	if got := []string{w.notes[0].Body, w.notes[1].Body, w.notes[2].Body}; !reflect.DeepEqual(got, []string{"First", locales.NoSubject(), "Third"}) {
		t.Errorf("bodies = %q", got)
	}
	if w.notes[1].Title != "noreply@example.com" {
		t.Errorf("title of a nameless sender = %q", w.notes[1].Title)
	}
	for i, n := range w.notes {
		if target, ok := parseNoteID(n.ID); !ok || !target.message || target.uid != mail.UID(21+i) {
			t.Errorf("notification %d opens %+v", i, target)
		}
	}

	w.notes = nil
	var many []mail.Summary
	for uid := mail.UID(30); uid < 30+noteBatchMax+1; uid++ {
		many = append(many, newMail(uid, "Sender", fmt.Sprintf("Message %d", uid)))
	}
	w.arrive(t, "a2", many...)
	if len(w.notes) != 1 {
		t.Fatalf("%d messages made %d notifications, want one count", len(many), len(w.notes))
	}
	if n := w.notes[0]; n.Title != "Home" || n.Body != "4 new messages" || n.Subtitle != "" {
		t.Errorf("count = %+v", n)
	}
	if target, ok := parseNoteID(w.notes[0].ID); !ok || target.message {
		t.Errorf("the count opens %+v", target)
	}
}

// A report that reaches the window twice notifies once, also when it
// overlaps one that came before.
func TestSameReportNotifiesOnce(t *testing.T) {
	w := startWatchApp(t)
	ev := w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
	w.report(t, ev)
	w.report(t, ev)
	if len(w.notes) != 1 {
		t.Fatalf("the same report notified %d times", len(w.notes))
	}
	if w.badge != 2 {
		t.Errorf("badge = %d", w.badge)
	}
	// A report holding that message and a newer one is news of the
	// newer one only.
	more := newMail(21, "Kim", "Breakfast")
	key := listKey("a2", "INBOX")
	w.fs.messages[key] = append(w.fs.messages[key], more)
	w.fs.recount("a2", "INBOX")
	w.report(t, w.fs.checked("a2", time.Now(), ev.New[0], more))
	if len(w.notes) != 2 || w.notes[1].Body != "1 new message" {
		t.Fatalf("notifications = %+v", w.notes)
	}
	if target, _ := parseNoteID(w.notes[1].ID); target.uid != 21 {
		t.Errorf("the second notification opens %+v", target)
	}
}

// With notifications off, for everything or for an account, nothing is
// notified, and the counts follow all the same.
func TestNotificationsOff(t *testing.T) {
	t.Run("everywhere", func(t *testing.T) {
		fs := newWatchedService()
		fs.settings.Notify = false
		w := watchApp(t, fs)
		w.a.start()
		w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
		w.arrive(t, "a1", newMail(10, "Noor", "Lunch"))
		if len(w.notes) != 0 {
			t.Errorf("notified with notifications off: %+v", w.notes)
		}
		if w.badge != 3 || w.a.inboxes["a2"] != 1 || w.a.folderUnread("INBOX") != 2 {
			t.Errorf("counts: badge %d, Home %d, Work %d", w.badge, w.a.inboxes["a2"], w.a.folderUnread("INBOX"))
		}
		tt := ui.NewTester(w.a.view, 1220, 800)
		if !tt.HasText("Lunch") {
			t.Errorf("the list did not follow: %q", tt.Texts())
		}
		// Turning them on later does not notify of what came before.
		w.a.mailSet.Notify = true
		w.report(t, w.fs.checked("a2", time.Now(), newMail(20, "Sam", "Dinner")))
		if len(w.notes) != 0 {
			t.Errorf("old mail was notified once notifications were on: %+v", w.notes)
		}
	})
	t.Run("for one account", func(t *testing.T) {
		fs := newWatchedService()
		fs.settings.Muted["a2"] = true
		w := watchApp(t, fs)
		w.a.start()
		w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
		if len(w.notes) != 0 {
			t.Errorf("a muted account notified: %+v", w.notes)
		}
		if w.badge != 2 {
			t.Errorf("badge = %d, want 2", w.badge)
		}
		w.arrive(t, "a1", newMail(10, "Noor", "Lunch"))
		if len(w.notes) != 1 || w.notes[0].Title != "Work" {
			t.Errorf("the other account's notifications = %+v", w.notes)
		}
	})
	t.Run("without a way to notify", func(t *testing.T) {
		w := startWatchApp(t)
		w.a.notify = nil
		w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
		if w.badge != 2 {
			t.Errorf("badge = %d, want 2", w.badge)
		}
	})
}

// Mail arriving in the list the user is looking at is not notified: it
// appears before their eyes. Looking elsewhere, it is.
func TestNoNotificationForTheListOnScreen(t *testing.T) {
	w := startWatchApp(t)
	a := w.a
	tt := ui.NewTester(a.view, 1220, 800)
	w.focused = true
	w.arrive(t, "a1", newMail(10, "Noor", "Seen arriving"))
	if len(w.notes) != 0 {
		t.Fatalf("notified of mail in the list on screen: %+v", w.notes)
	}
	// It is not announced later either, when the window is in the back.
	w.focused = false
	w.report(t, w.fs.checked("a1", time.Now(), newMail(10, "Noor", "Seen arriving")))
	if len(w.notes) != 0 {
		t.Fatalf("mail seen arriving was notified afterwards: %+v", w.notes)
	}

	// The window in the back.
	w.arrive(t, "a1", newMail(11, "Noor", "Window in the back"))
	if len(w.notes) != 1 {
		t.Fatalf("no notification with the window in the back: %+v", w.notes)
	}
	// Another account's mail.
	w.focused = true
	w.arrive(t, "a2", newMail(20, "Sam", "Other account"))
	if len(w.notes) != 2 {
		t.Fatalf("no notification for the account not on screen: %+v", w.notes)
	}
	// Another folder on screen.
	click(t, tt, "Sent")
	w.arrive(t, "a1", newMail(12, "Noor", "Other folder"))
	if len(w.notes) != 3 {
		t.Fatalf("no notification with another folder on screen: %+v", w.notes)
	}
	// A search hides the list's new mail.
	click(t, tt, locales.Inbox())
	a.query = "contract"
	a.search()
	w.arrive(t, "a1", newMail(13, "Noor", "While searching"))
	if len(w.notes) != 4 {
		t.Fatalf("no notification while a search filters the list: %+v", w.notes)
	}
	// The settings cover the list.
	a.query = ""
	a.search()
	a.openSettings(settingsTabGeneral)
	w.arrive(t, "a1", newMail(14, "Noor", "Behind the settings"))
	if len(w.notes) != 5 {
		t.Fatalf("no notification with the settings open: %+v", w.notes)
	}
}

// Clicking a notification opens its account, folder and message, and
// brings the window to the front.
func TestNotificationClickOpensTheMessage(t *testing.T) {
	w := startMessageWatchApp(t)
	a := w.a
	tt := ui.NewTester(a.view, 1220, 800)
	w.arrive(t, "a2", newMail(20, "Sam", "Dinner on Friday"))
	w.arrive(t, "a2", newMail(21, "Kim", "Breakfast on Monday"))
	if len(w.notes) != 2 {
		t.Fatalf("notifications = %+v", w.notes)
	}
	// From the settings, of another account.
	a.openSettings(settingsTabGeneral)
	a.openNotification(w.notes[0].ID)
	tt.Frame()
	if a.active != "a2" || a.folder != "INBOX" || a.selectedKey() != "a2/INBOX/20" {
		t.Fatalf("opened account %q, folder %q, message %q", a.active, a.folder, a.selectedKey())
	}
	if a.showSettings || w.raised != 1 {
		t.Errorf("settings open %v, window raised %d times", a.showSettings, w.raised)
	}
	if !tt.HasText("Body of Dinner on Friday") {
		t.Errorf("the message is not open: %q", tt.Texts())
	}
	if a.aim != nil {
		t.Errorf("the wish of the click is still pending: %+v", a.aim)
	}
	// The newer message, which a plain opening of the folder would have
	// shown first, was not opened on the way.
	if m, _ := w.fs.find("a2", "INBOX", 21); m.Seen {
		t.Error("the click marked another message read")
	}

	// In the account and folder on screen, from another message.
	a.openNotification(w.notes[1].ID)
	tt.Frame()
	if a.selectedKey() != "a2/INBOX/21" || w.raised != 2 {
		t.Fatalf("opened %q; raised %d", a.selectedKey(), w.raised)
	}

	// From another folder of the same account, with a search typed.
	click(t, tt, "Sent")
	a.query = "nothing matches"
	a.search()
	a.openNotification(w.notes[0].ID)
	tt.Frame()
	if a.folder != "INBOX" || a.selectedKey() != "a2/INBOX/20" || a.query != "" {
		t.Fatalf("opened folder %q, message %q, search %q", a.folder, a.selectedKey(), a.query)
	}

	// A count opens the inbox of its account.
	w.arrive(t, "a1", newMail(10, "Noor", "One"), newMail(11, "Noor", "Two"))
	count := w.notes[len(w.notes)-1]
	if target, _ := parseNoteID(count.ID); target.message {
		t.Fatalf("the count's ID = %q", count.ID)
	}
	click(t, tt, "Sent")
	a.openNotification(count.ID)
	tt.Frame()
	if a.active != "a1" || a.folder != "INBOX" {
		t.Fatalf("the count opened account %q, folder %q", a.active, a.folder)
	}
	if !tt.HasText("Two") {
		t.Errorf("the inbox is not on screen: %q", tt.Texts())
	}
}

// The click that launched the app comes before the accounts are read: it
// is held, and opens its message once they are.
func TestNotificationClickAtLaunch(t *testing.T) {
	fs := newWatchedService()
	key := listKey("a2", "INBOX")
	fs.messages[key] = append(fs.messages[key], newMail(20, "Sam", "Dinner on Friday"), newMail(21, "Kim", "Later mail"))
	sortSummaries(fs.messages[key])
	fs.recount("a2", "INBOX")
	id := noteTarget{account: "a2", folder: "INBOX", validity: 7, uid: 20, message: true}.id()

	w := watchApp(t, inMessageView(fs))
	a := w.a
	a.openNotification(id)
	if a.active != "" || w.raised != 0 {
		t.Fatalf("the click acted before the accounts were read")
	}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	if a.active != "a2" || a.folder != "INBOX" || a.selectedKey() != "a2/INBOX/20" {
		t.Fatalf("opened account %q, folder %q, message %q", a.active, a.folder, a.selectedKey())
	}
	if !tt.HasText("Body of Dinner on Friday") || w.raised != 1 {
		t.Errorf("message open: %v; raised %d", tt.HasText("Body of Dinner on Friday"), w.raised)
	}
	if fs.active != "a2" {
		t.Errorf("the account opened is not remembered: %q", fs.active)
	}
}

// A message the click asks for that the cache does not hold yet is
// chosen once the folder's sync brought it, and given up after.
func TestNotificationClickWaitsForTheSync(t *testing.T) {
	w := startWatchApp(t)
	a := w.a
	id := noteTarget{account: "a1", folder: "INBOX", validity: 7, uid: 99, message: true}.id()
	a.openNotification(id)
	if a.selected != -1 && a.selectedKey() == "a1/INBOX/99" {
		t.Fatal("a message that is not there was chosen")
	}
	if a.aim != nil {
		t.Errorf("the wish outlived the folder's sync: %+v", a.aim)
	}
	// Under another UIDVALIDITY the UID names another message: the
	// folder opens, and no message is looked for.
	stale := noteTarget{account: "a2", folder: "INBOX", validity: 6, uid: 1, message: true}.id()
	a.openNotification(stale)
	if a.active != "a2" || a.folder != "INBOX" || a.aim != nil {
		t.Errorf("a stale ID opened account %q, folder %q, wish %+v", a.active, a.folder, a.aim)
	}
}

// A notification ID is input from outside. One the app did not write, or
// about an account or folder it does not have, does nothing.
func TestNotificationClickIgnoresBadIDs(t *testing.T) {
	good := noteTarget{account: "a2", folder: "INBOX", validity: 7, uid: 20, message: true}.id()
	if good != "mail/m/a2/INBOX/7/20" {
		t.Fatalf("the ID of a message = %q", good)
	}
	odd := noteTarget{account: "a/2", folder: "Work/Reports 2026", validity: 7, uid: 20}
	if back, ok := parseNoteID(odd.id()); !ok || back != odd {
		t.Fatalf("%q read back as %+v", odd.id(), back)
	}
	bad := []string{
		"",
		"mail",
		"mail/m/a2/INBOX/7",
		"mail/m/a2/INBOX/7/20/",
		"mail/m/a2/INBOX/7/20/extra",
		"mail/x/a2/INBOX/7/20",
		"Mail/m/a2/INBOX/7/20",
		"mail/m//INBOX/7/20",
		"mail/m/a2//7/20",
		"mail/m/a2/INBOX/0/20",
		"mail/m/a2/INBOX/7/0",
		"mail/m/a2/INBOX/7/-20",
		"mail/m/a2/INBOX/7/+20",
		"mail/m/a2/INBOX/7/020",
		"mail/m/a2/INBOX/07/20",
		"mail/m/a2/INBOX/7/20 ",
		"mail/m/a2/INBOX/7/4294967296",
		"mail/m/a2/INBOX/4294967296/20",
		"mail/m/a2/INBOX/7/2e1",
		"mail/m/a2/INB%4fX/7/20",
		"mail/m/a2/INBOX%2/7/20",
		"mail/m/a2/INBOX/7/20\x00",
		"mail/m/a2/../7/20/..",
		"file:///etc/passwd",
		"mail/m/a2/" + strings.Repeat("x", 2000) + "/7/20",
	}
	for _, id := range bad {
		if target, ok := parseNoteID(id); ok {
			t.Errorf("parseNoteID(%q) = %+v", id, target)
		}
	}

	w := startWatchApp(t)
	a := w.a
	a.openSettings(settingsTabGeneral)
	before := []any{a.active, a.folder, a.selectedKey(), a.gen}
	unknown := []string{
		"mail/m/nobody/INBOX/7/20",
		"mail/m/a2/No%20such%20folder/7/20",
		"mail/s/a2/Nope/7/20",
	}
	for _, id := range append(bad, unknown...) {
		a.openNotification(id)
		if after := []any{a.active, a.folder, a.selectedKey(), a.gen}; !reflect.DeepEqual(before, after) {
			t.Fatalf("%q changed the window: %v to %v", id, before, after)
		}
		if w.raised != 0 || !a.showSettings || a.aim != nil {
			t.Fatalf("%q acted: raised %d, settings %v, wish %+v", id, w.raised, a.showSettings, a.aim)
		}
	}
	if len(w.fs.flagCalls) != 0 && a.selectedKey() != "a1/INBOX/1" {
		t.Errorf("flags changed: %v", w.fs.flagCalls)
	}
}

// The sidebar's footer says how receiving mail is going, in words of
// its own: never the text of an error.
func TestFooterShowsWatchState(t *testing.T) {
	w := startWatchApp(t)
	a := w.a
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.WatchConnecting()) {
		t.Fatalf("before the first check the footer does not say it is checking: %q", tt.Texts())
	}
	// Checked a few minutes ago, by the window's clock.
	at := tt.Now().Add(-3 * time.Minute)
	w.report(t, w.fs.checked("a1", at))
	tt.Frame()
	when := i18n.FormatDate("en", at, i18n.DateOptions{Time: i18n.ShortStyle})
	// The engine may keep time in another zone than the window's.
	at = at.In(time.FixedZone("elsewhere", -7*3600))
	w.report(t, w.fs.checked("a1", at))
	tt.Frame()
	if want := locales.WatchCheckedAt(when); !tt.HasText(want) {
		t.Errorf("the footer does not say %q: %q", want, tt.Texts())
	}
	if !tt.HasText(locales.SidebarSummary(1, 3)) {
		t.Errorf("the totals left the footer: %q", tt.Texts())
	}

	states := []struct {
		status mail.WatchStatus
		want   string
	}{
		{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailNetwork, CheckedAt: at}, locales.WatchRetryNetwork()},
		{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailAuth, CheckedAt: at}, locales.WatchRetryAuth()},
		{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailOther, CheckedAt: at}, locales.WatchRetryOther()},
		{mail.WatchStatus{State: mail.WatchOffline, Failure: mail.WatchFailNetwork, CheckedAt: at}, locales.WatchOffline()},
		{mail.WatchStatus{State: mail.WatchConnecting, CheckedAt: at}, locales.WatchConnecting()},
		{mail.WatchStatus{State: mail.WatchUpToDate}, locales.WatchUpToDate()},
	}
	for _, s := range states {
		w.say(t, "a1", s.status)
		tt.Frame()
		if !tt.HasText(s.want) {
			t.Errorf("state %+v: the footer does not say %q: %q", s.status, s.want, tt.Texts())
		}
		// Trouble is a line of the footer, not a banner: the mail that
		// is cached stays there to read.
		if a.err != "" || !tt.HasText("Design review — Thursday at 10") || !tt.HasText(locales.SidebarSummary(1, 3)) {
			t.Errorf("state %+v: error %q, or the list or totals are gone: %q", s.status, a.err, tt.Texts())
		}
		click(t, tt, "Re: Contract for Q4 campaign")
		if !tt.HasText("Body of Re: Contract for Q4 campaign") {
			t.Errorf("state %+v: cached mail cannot be read", s.status)
		}
	}

	// The footer is the active account's: another account's trouble is
	// not shown as this one's.
	w.say(t, "a2", mail.WatchStatus{State: mail.WatchOffline})
	tt.Frame()
	if tt.HasText(locales.WatchOffline()) {
		t.Errorf("the footer shows another account's state")
	}
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem("Home · home@example.com"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(locales.WatchOffline()) {
		t.Errorf("the footer does not follow the account: %q", tt.Texts())
	}
}

// The footer says what the account's queued actions are doing: those the
// server has not made yet, and those it refused, under the watcher's own
// line.
func TestFooterShowsTheQueue(t *testing.T) {
	w := startWatchApp(t)
	tt := ui.NewTester(w.a.view, 1220, 800)
	w.report(t, mail.WatchEvent{AccountID: "a1", Queue: &mail.QueueStatus{Pending: 2}})
	tt.Frame()
	if want := locales.QueuePending(2); !tt.HasText(want) {
		t.Errorf("the footer does not say %q: %q", want, tt.Texts())
	}
	if !tt.HasText(locales.WatchConnecting()) {
		t.Errorf("the queue took the watcher's line: %q", tt.Texts())
	}

	// A refusal is the queue's line, with the reason the header holds.
	w.report(t, mail.WatchEvent{AccountID: "a1", Queue: &mail.QueueStatus{
		Pending: 1, Failed: 1, Reason: "mail: folder Archive is gone",
	}})
	tt.Frame()
	if want := locales.QueueFailed(1); !tt.HasText(want) {
		t.Errorf("the footer does not say %q: %q", want, tt.Texts())
	}
	// A refusal waits for the user, who may fix its cause and ask again.
	click(t, tt, locales.QueueFailed(1))
	if !slices.Contains(w.fs.watchLog, "retry a1") {
		t.Errorf("a press on the refused changes did not ask for a retry: %v", w.fs.watchLog)
	}

	// Another account's queue is not this account's.
	w.report(t, mail.WatchEvent{AccountID: "a2", Queue: &mail.QueueStatus{Pending: 4}})
	tt.Frame()
	if tt.HasText(locales.QueuePending(4)) {
		t.Error("the footer shows another account's queue")
	}

	// A queue the server caught up with takes its line away.
	w.report(t, mail.WatchEvent{AccountID: "a1", Queue: &mail.QueueStatus{}})
	tt.Frame()
	if tt.HasText(locales.QueueFailed(1)) || tt.HasText(locales.QueuePending(1)) {
		t.Errorf("the footer keeps the queue: %q", tt.Texts())
	}
	if !tt.HasText(locales.WatchConnecting()) {
		t.Errorf("the watcher's line went with the queue: %q", tt.Texts())
	}
}

// Without watchers the footer says nothing of them.
func TestFooterWithoutWatchers(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	for _, text := range []string{locales.WatchConnecting(), locales.WatchUpToDate()} {
		if tt.HasText(text) {
			t.Errorf("the footer says %q with nothing watched", text)
		}
	}
}

// The settings of receiving mail save as they are changed, and are
// there again when the app starts anew.
func TestMailSettingsSaveAndPersist(t *testing.T) {
	w := startWatchApp(t)
	a, fs := w.a, w.fs
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 900)
	for _, text := range []string{
		locales.CheckEvery(), locales.CheckMinutes(1), locales.CheckMinutes(5), locales.CheckMinutes(15),
		locales.CheckMinutes(30), locales.NotifyNewMail(), locales.NotifyNewMailDetail(), locales.NotifyDetails(),
		locales.NotifyAccounts(), "Work", "home@example.com",
	} {
		if !tt.HasText(text) {
			t.Fatalf("the settings lack %q: %q", text, tt.Texts())
		}
	}
	if a.checkChoice != 1 {
		t.Errorf("the interval shown = choice %d, want the default of 5 minutes", a.checkChoice)
	}

	click(t, tt, locales.CheckMinutes(15))
	if fs.settings.CheckMinutes != 15 {
		t.Errorf("saved interval = %d", fs.settings.CheckMinutes)
	}
	click(t, tt, locales.NotifyDetails())
	if !fs.settings.NotifyDetails {
		t.Error("showing the sender and subject was not saved")
	}
	click(t, tt, "Home")
	if !fs.settings.Muted["a2"] || fs.settings.Muted["a1"] {
		t.Errorf("muted = %v, want Home only", fs.settings.Muted)
	}
	// The setting applies at once.
	w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
	w.arrive(t, "a1", newMail(10, "Noor Haddad", "Lunch"))
	if len(w.notes) != 1 || w.notes[0].Title != "Noor Haddad" {
		t.Errorf("notifications after the change = %+v", w.notes)
	}
	click(t, tt, locales.NotifyNewMail())
	if fs.settings.Notify {
		t.Error("turning notifications off was not saved")
	}
	if fs.settings.CheckMinutes != 15 || !fs.settings.NotifyDetails || !fs.settings.Muted["a2"] {
		t.Errorf("a later save lost earlier choices: %+v", fs.settings)
	}

	// The app starts again on what was saved.
	again := watchApp(t, fs)
	again.a.start()
	again.a.openSettings(settingsTabGeneral)
	set := again.a.mailSet
	if again.a.checkChoice != 2 || set.Notify || !set.NotifyDetails || !set.Muted["a2"] || *again.a.notifyOn["a2"] || !*again.a.notifyOn["a1"] {
		t.Errorf("settings after a restart: choice %d, %+v", again.a.checkChoice, set)
	}
	tt = ui.NewTester(again.a.view, 1220, 900)
	click(t, tt, locales.NotifyNewMail())
	click(t, tt, locales.CheckMinutes(30))
	if !fs.settings.Notify || fs.settings.CheckMinutes != 30 {
		t.Errorf("saved after the restart: %+v", fs.settings)
	}
}

// A save that fails, and a system that refuses notifications, are said
// in the settings.
func TestMailSettingsSayWhatIsWrong(t *testing.T) {
	w := startWatchApp(t)
	a := w.a
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 900)
	w.fs.settingsErr = errFake
	click(t, tt, locales.CheckMinutes(1))
	if !tt.HasText(locales.SettingsSaveFailed()) {
		t.Errorf("a failed save is not said: %q", tt.Texts())
	}
	if tt.HasText(errFake.Error()) {
		t.Errorf("the raw error shows: %q", tt.Texts())
	}
	w.fs.settingsErr = nil
	click(t, tt, locales.CheckMinutes(5))
	if tt.HasText(locales.SettingsSaveFailed()) {
		t.Errorf("the warning stays after a save that worked")
	}

	w.noteErr = mygo.ErrNotificationsDenied
	w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
	tt.Frame()
	if !tt.HasText(locales.NotifyDenied()) {
		t.Errorf("refused notifications are not said: %q", tt.Texts())
	}
	// The counts follow all the same.
	if w.badge != 2 {
		t.Errorf("badge = %d", w.badge)
	}
	w.noteErr = nil
	w.arrive(t, "a2", newMail(21, "Sam", "Supper"))
	tt.Frame()
	if tt.HasText(locales.NotifyDenied()) || len(w.notes) != 1 {
		t.Errorf("after notifications were allowed: %+v", w.notes)
	}
	// Another failure of the system is not the user's to fix.
	w.noteErr = errors.New("notification centre is busy")
	w.arrive(t, "a2", newMail(22, "Sam", "Snack"))
	tt.Frame()
	if tt.HasText(locales.NotifyDenied()) || tt.HasText("busy") {
		t.Errorf("an unrelated failure shows: %q", tt.Texts())
	}
}

// One account has no switch of its own: it would repeat the one above.
func TestMailSettingsWithOneAccount(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Work", "work@example.com"), "secret", watchFolders(2, 3), testMessages("a1"))
	w := watchApp(t, fs)
	w.a.start()
	w.a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(w.a.view, 1220, 900)
	if tt.HasText(locales.NotifyAccounts()) {
		t.Errorf("one account has a switch of its own: %q", tt.Texts())
	}
	if !tt.HasText(locales.NotifyNewMail()) {
		t.Errorf("no notification settings: %q", tt.Texts())
	}
}

// The watchers are woken by the Sync button, and by what the window's
// hooks call when the app comes to the front, the computer wakes and
// the network changes. Manual sync works whatever state the watcher is in.
func TestNudges(t *testing.T) {
	w := startWatchApp(t)
	a, fs := w.a, w.fs
	tt := ui.NewTester(a.view, 1220, 800)
	fs.watchLog = nil
	a.nudge()
	a.setOnline(false)
	a.setOnline(true)
	if want := []string{"nudge", "offline", "online"}; !reflect.DeepEqual(fs.watchLog, want) {
		t.Fatalf("watchers were told %v, want %v", fs.watchLog, want)
	}

	for _, status := range []mail.WatchStatus{
		{State: mail.WatchUpToDate},
		{State: mail.WatchRetrying, Failure: mail.WatchFailNetwork},
		{State: mail.WatchOffline},
		{State: mail.WatchConnecting},
	} {
		w.say(t, "a1", status)
		fs.watchLog = nil
		// Mail the server has and the cache shows only after a sync.
		uid := mail.UID(40 + int(status.State))
		key := listKey("a1", "INBOX")
		subject := fmt.Sprintf("Synced by hand %d", status.State)
		click(t, tt, locales.Sync())
		if !reflect.DeepEqual(fs.watchLog, []string{"nudge"}) {
			t.Errorf("state %v: the Sync button told the watchers %v", status.State, fs.watchLog)
		}
		fs.messages[key] = append(fs.messages[key], newMail(uid, "Noor", subject))
		sortSummaries(fs.messages[key])
		click(t, tt, locales.Sync())
		if !tt.HasText(subject) {
			t.Errorf("state %v: a manual sync did not show the mail: %q", status.State, tt.Texts())
		}
	}

	// A window that watches nothing wakes nothing.
	plain, pfs := sampleApp(t)
	plain.nudge()
	plain.setOnline(false)
	plain.refresh()
	if len(pfs.watchLog) != 0 {
		t.Errorf("a window without watchers told them %v", pfs.watchLog)
	}
}

// Watchers follow the accounts: a new account is watched, an edited one
// is watched anew, and a removed one is not watched and no longer heard.
func TestWatchersFollowAccounts(t *testing.T) {
	w := startWatchApp(t)
	a, fs := w.a, w.fs
	tt := ui.NewTester(a.view, 1220, 1200)

	// Saving an account restarts its watcher: the password may be new.
	fs.watchLog = nil
	a.openAccountForm(a.account("a2"))
	a.accountForm.Password = "another secret"
	tt.Frame()
	click(t, tt, "Test and save")
	if want := []string{"watch a2"}; !reflect.DeepEqual(fs.watchLog, want) {
		t.Fatalf("after editing an account the watchers were told %v, want %v", fs.watchLog, want)
	}
	if got := a.watch["a2"].State; got != mail.WatchConnecting {
		t.Errorf("the edited account's state = %v, want connecting", got)
	}

	// A new account.
	fs.watchLog = nil
	a.openAccountForm(nil)
	a.accountForm.Name = "Third"
	a.accountForm.Email = "third@example.com"
	a.accountForm.Password = "secret"
	a.accountForm.Host = "imap.example.com"
	a.accountForm.Port = "993"
	a.accountForm.SMTPHost = "smtp.example.com"
	a.accountForm.SMTPPort = "465"
	tt.Frame()
	click(t, tt, "Test and save")
	if len(fs.accounts) != 3 {
		t.Fatalf("accounts = %+v", fs.accounts)
	}
	third := fs.accounts[2].ID
	if want := []string{"watch " + third}; !reflect.DeepEqual(fs.watchLog, want) {
		t.Fatalf("after adding an account the watchers were told %v, want %v", fs.watchLog, want)
	}
	if on := a.notifyOn[third]; on == nil || !*on {
		t.Error("the new account has no notification switch, or it is off")
	}

	// An account removed, as the delete button does after its question.
	fs.watchLog = nil
	gone := fs.watching["a1"]
	if err := fs.RemoveAccount(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	a.accounts, _, _ = fs.Accounts(context.Background())
	a.accountsChanged()
	if want := []string{"unwatch a1"}; !reflect.DeepEqual(fs.watchLog, want) {
		t.Fatalf("after removing an account the watchers were told %v, want %v", fs.watchLog, want)
	}
	if _, ok := a.watch["a1"]; ok {
		t.Error("the removed account still has a state")
	}
	// A report of it that was on its way is dropped.
	badge := w.badge
	ev := fs.checked("a1", time.Now(), newMail(50, "Noor", "Too late"))
	ev.Folders = watchFolders(9, 9)
	gone(ev)
	if len(w.notes) != 0 || w.badge != badge {
		t.Errorf("a removed account's report was heard: %+v, badge %d", w.notes, w.badge)
	}
	if w.badge != 0 {
		t.Errorf("badge = %d, want the other accounts' 0", w.badge)
	}
}

// The calls that start and stop watchers run one at a time, in the
// order they were asked for, however the jobs are scheduled.
func TestWatchCallsKeepTheirOrder(t *testing.T) {
	fs := newWatchedService()
	w := watchApp(t, fs)
	a := w.a
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	var got []string
	for _, name := range []string{"first", "second", "third"} {
		a.watchOp(func() { got = append(got, name) })
	}
	// The last job asked for runs first.
	for i := len(jobs) - 1; i >= 0; i-- {
		jobs[i]()
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls ran as %v, want %v", got, want)
	}
}

// The footer and the settings hold at the smallest window, in the dark,
// and in Simplified Chinese.
func TestWatchLayoutAtMinimumSize(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			name := fmt.Sprintf("%s dark=%v", locale, dark)
			i18n.SetLocaleFunc(func() string { return locale })
			w := startWatchApp(t)
			a := w.a
			tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
			tt.SetLocale(locale)
			at := tt.Now().Add(-time.Minute)
			when := i18n.FormatDate(locale, at, i18n.DateOptions{Time: i18n.ShortStyle})
			lines := []struct {
				status mail.WatchStatus
				text   string
			}{
				{mail.WatchStatus{State: mail.WatchUpToDate, CheckedAt: at}, locales.WatchCheckedAt(when)},
				{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailNetwork}, locales.WatchRetryNetwork()},
				{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailAuth}, locales.WatchRetryAuth()},
				{mail.WatchStatus{State: mail.WatchRetrying, Failure: mail.WatchFailOther}, locales.WatchRetryOther()},
				{mail.WatchStatus{State: mail.WatchOffline}, locales.WatchOffline()},
				{mail.WatchStatus{State: mail.WatchConnecting}, locales.WatchConnecting()},
			}
			for _, line := range lines {
				w.say(t, "a1", line.status)
				tt.Frame()
				at, ok := tt.Find(line.text)
				if !ok {
					t.Errorf("%s: the footer lacks %q: %q", name, line.text, tt.Texts())
					continue
				}
				// Inside the sidebar, and inside the window.
				if at.X < 0 || at.X+at.W > 222 || at.Y+at.H > 640 {
					t.Errorf("%s: %q is at %+v, outside the sidebar", name, line.text, at)
				}
				for _, text := range []string{locales.Inbox(), locales.Trash(), locales.SidebarSummary(1, 3), locales.Compose()} {
					if !tt.HasText(text) {
						t.Errorf("%s: with %q the window lacks %q", name, line.text, text)
					}
				}
			}

			a.openSettings(settingsTabGeneral)
			tt.Frame()
			for _, text := range []string{
				locales.CheckEvery(), locales.CheckMinutes(30), locales.CheckEveryDetail(), locales.NotifyNewMail(),
				locales.NotifyNewMailDetail(), locales.NotifyDetails(), locales.NotifyDetailsDetail(),
			} {
				at, ok := tt.Find(text)
				if !ok {
					t.Errorf("%s: the settings lack %q: %q", name, text, tt.Texts())
					continue
				}
				if at.X < 0 || at.X+at.W > 1000 {
					t.Errorf("%s: %q is at %+v, outside the window", name, text, at)
				}
			}
			click(t, tt, locales.CheckMinutes(30))
			click(t, tt, locales.NotifyDetails())
			if w.fs.settings.CheckMinutes != 30 || !w.fs.settings.NotifyDetails {
				t.Errorf("%s: the controls do not work at the smallest size: %+v", name, w.fs.settings)
			}
		}
	}
	i18n.SetLocaleFunc(func() string { return "en" })
}

// The strings of receiving mail exist in Simplified Chinese, and a
// notification is written in the app's language.
func TestWatchStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"newMailCount":        zh.NewMailCount(3),
		"watchUpToDate":       zh.WatchUpToDate(),
		"watchCheckedAt":      zh.WatchCheckedAt("09:41"),
		"watchConnecting":     zh.WatchConnecting(),
		"watchRetryNetwork":   zh.WatchRetryNetwork(),
		"watchRetryAuth":      zh.WatchRetryAuth(),
		"watchRetryOther":     zh.WatchRetryOther(),
		"watchOffline":        zh.WatchOffline(),
		"checkMail":           zh.CheckMail(),
		"checkEvery":          zh.CheckEvery(),
		"checkMinutes":        zh.CheckMinutes(5),
		"checkEveryDetail":    zh.CheckEveryDetail(),
		"notifications":       zh.Notifications(),
		"notifyNewMail":       zh.NotifyNewMail(),
		"notifyNewMailDetail": zh.NotifyNewMailDetail(),
		"notifyDetails":       zh.NotifyDetails(),
		"notifyDetailsDetail": zh.NotifyDetailsDetail(),
		"notifyAccounts":      zh.NotifyAccounts(),
		"notifyDenied":        zh.NotifyDenied(),
		"settingsSaveFailed":  zh.SettingsSaveFailed(),
	} {
		if text == "" || text == name || !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.NewMailCount(1); got != "1 封新邮件" {
		t.Errorf("one new message in zh-Hans = %q", got)
	}
	if got := zh.WatchCheckedAt("09:41"); !strings.Contains(got, "09:41") {
		t.Errorf("checked at in zh-Hans = %q", got)
	}

	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	w := startWatchApp(t)
	w.arrive(t, "a2", newMail(20, "Sam", "Dinner"))
	w.arrive(t, "a2", newMail(21, "Sam", "One"), newMail(22, "Kim", "Two"))
	if len(w.notes) != 2 || w.notes[0].Title != "Home" || w.notes[0].Body != "1 封新邮件" || w.notes[1].Body != "2 封新邮件" {
		t.Errorf("notifications in zh-Hans = %+v", w.notes)
	}
	w.a.mailSet.NotifyDetails = true
	blank := newMail(23, "Sam", "")
	w.arrive(t, "a2", blank)
	if n := w.notes[len(w.notes)-1]; n.Title != "Sam" || n.Body != "（无主题）" && n.Body != zh.NoSubject() {
		t.Errorf("a message without a subject in zh-Hans = %+v", n)
	}
}

// uiQueue runs what the engine's goroutines post on the test's own
// goroutine, as the window's UI thread does.
type uiQueue chan func()

// until runs posted updates until done reports true.
func (q uiQueue) until(t *testing.T, what string, done func() bool) {
	t.Helper()
	guard := time.After(20 * time.Second)
	for !done() {
		select {
		case f := <-q:
			f()
		case <-guard:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// The first acceptance criterion end to end: the window on the real
// engine and a database on disk, against a local IMAP server that
// pushes. A message another mail client delivers while the app does
// nothing appears in the list and is notified, once, and stays unread on
// the server; a click on the notification opens it.
func TestBackgroundMailEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Trash")
	host, port := hostPort(t, imapServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
	}
	deliver := func(subject string) {
		t.Helper()
		raw := "From: Bo <bo@example.com>\r\nTo: ann@example.com\r\nSubject: " + subject +
			"\r\nDate: Mon, 02 Mar 2026 09:00:00 +0000\r\nMessage-ID: <" + subject + "@example.com>\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\nThe text of " + subject + ".\r\n"
		literal := appendBody{bytes.NewReader([]byte(raw))}
		if _, err := imapServer.user.Append("INBOX", literal, &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	deliver("Old mail")

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a local test server
	// The watchers end before the database closes.
	defer svc.StopWatching()
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	// The list of messages, as before conversations: those have their own
	// test end to end.
	listing := mail.DefaultSettings()
	listing.Conversations = false
	if err := svc.SaveSettings(ctx, listing); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(svc)
	a.watches = true
	a.setLocale = func(string) {}
	queue := make(uiQueue, 256)
	var notes []note
	a.notify = func(n note) error {
		notes = append(notes, n)
		return nil
	}
	a.focused = func() bool { return false }
	badge := -1
	a.setBadge = func(n int) { badge = n }
	// The watcher reports from its goroutine: what it and the jobs post
	// is run here, in order.
	a.update = func(f func()) { queue <- f }
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)

	// Opening the folder shows its first message, which marks it read.
	queue.until(t, "the first check and the inbox", func() bool {
		tt.Frame()
		return a.watch["a1"].State == mail.WatchUpToDate && a.selectedKey() == "a1/INBOX/1" &&
			tt.HasText("The text of Old mail.")
	})
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	if len(notes) != 0 {
		t.Fatalf("the mail that was there was notified: %+v", notes)
	}

	deliver("Pushed mail")
	queue.until(t, "the notification and the new row", func() bool {
		tt.Frame()
		return len(notes) == 1 && tt.HasText("Pushed mail")
	})
	if n := notes[0]; n.Title != "Ann" || n.Body != "1 new message" {
		t.Errorf("notification = %+v", n)
	}
	if !tt.HasText("Pushed mail") {
		t.Errorf("the new mail is not in the list: %q", tt.Texts())
	}
	if got := a.folderUnread("INBOX"); got != 1 || badge != 1 {
		t.Errorf("inbox unread = %d, badge = %d, want 1 and 1", got, badge)
	}
	if a.selectedKey() != "a1/INBOX/1" {
		t.Errorf("the choice moved to %q", a.selectedKey())
	}
	// On the server the new message is unread still: watching reads only.
	seen := map[imap.UID]bool{}
	c := imapServer.connect(t, "INBOX")
	var set imap.SeqSet
	set.AddRange(1, 0)
	fetched, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range fetched {
		for _, flag := range m.Flags {
			if flag == imap.FlagSeen {
				seen[m.UID] = true
			}
		}
	}
	if !seen[1] || seen[2] {
		t.Errorf("read on the server: %v, want the opened message only", seen)
	}

	// More checks do not notify again.
	svc.Nudge()
	checked := a.watch["a1"].CheckedAt
	queue.until(t, "another check", func() bool { return a.watch["a1"].CheckedAt.After(checked) })
	if len(notes) != 1 {
		t.Errorf("a later check notified again: %+v", notes)
	}

	// The click opens the message.
	a.openNotification(notes[0].ID)
	queue.until(t, "the message to open", func() bool {
		tt.Frame()
		return a.selectedKey() == "a1/INBOX/2" && tt.HasText("The text of Pushed mail.")
	})
}

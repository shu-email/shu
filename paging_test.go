package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// pagedMail is n messages of an inbox, the newest numbered n, a minute
// apart and read. Each is a conversation of its own unless thread gives
// it one.
func pagedMail(n int, thread func(uid mail.UID) mail.ThreadID) []mail.Summary {
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	out := make([]mail.Summary, 0, n)
	for i := 1; i <= n; i++ {
		uid := mail.UID(i)
		m := mail.Summary{
			UID: uid, Folder: "INBOX", Subject: fmt.Sprintf("Message %d", i), Preview: fmt.Sprintf("Preview %d", i),
			From: mail.Address{Name: "Sender", Email: "sender@example.com"},
			Date: base.Add(time.Duration(i) * time.Minute), Seen: true,
		}
		if thread != nil {
			m.Thread = thread(uid)
		}
		out = append(out, m)
	}
	return out
}

// pagedService holds an account whose inbox has msgs, in conversation
// view or not.
func pagedService(msgs []mail.Summary, conversations bool) *fakeService {
	fs := newFakeService()
	fs.settings.Conversations = conversations
	folders := testFolders()
	folders[0].Unread, folders[0].Total, folders[0].UIDValidity = 0, len(msgs), 7
	fs.addAccount(testAccount("a1", "Work", "work@example.com"), "secret", folders, msgs)
	return fs
}

func startPaged(t *testing.T, fs *fakeService) (*app, *ui.Tester) {
	t.Helper()
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, ui.NewTester(a.view, 1220, 800)
}

// scrollList scrolls the list by dy DIPs, as a touchpad does: far enough
// reaches either end. A page read inline as the list is laid out changes
// the rows after the frame was built; the window's update would build
// another, which the Tester does when asked.
func scrollList(tt *ui.Tester, dy float32) {
	tt.Scroll(400, 400, 0, dy)
	tt.Frame()
}

const (
	toEnd = 1e7
	toTop = -1e7
)

// scrollToLast scrolls the list to its end until the cache has no more.
func scrollToLast(t *testing.T, a *app, tt *ui.Tester) {
	t.Helper()
	for i := 0; a.pager.more; i++ {
		if i > 1000 {
			t.Fatalf("the list never ends: %d rows", len(a.all))
		}
		scrollList(tt, toEnd)
	}
	scrollList(tt, toEnd)
}

// uids are the UIDs of the rows, in order.
func uids(list []mail.Summary) []mail.UID {
	out := make([]mail.UID, len(list))
	for i, m := range list {
		out[i] = m.UID
	}
	return out
}

// countdown is n, n-1, … 1.
func countdown(n int) []mail.UID {
	out := make([]mail.UID, n)
	for i := range out {
		out[i] = mail.UID(n - i)
	}
	return out
}

func chosenKeys(a *app) []string {
	return slices.Sorted(a.sel.All())
}

// Two accounts each have a message 1 in their INBOX. As rows of one list,
// which a list across accounts will be, they have keys, bodies and a
// choice of their own, and each action reaches the message's own
// account.
func TestAccountsNeverShareAKey(t *testing.T) {
	one := mail.Summary{Account: "a1", Folder: "INBOX", UID: 1}
	two := mail.Summary{Account: "a2", Folder: "INBOX", UID: 1}
	if summaryKey(one) == summaryKey(two) || threadKey("a1", 7) == threadKey("a2", 7) {
		t.Fatalf("keys %q %q, %q %q", summaryKey(one), summaryKey(two), threadKey("a1", 7), threadKey("a2", 7))
	}
	// A slash in an account's ID does not make it another's message.
	if messageKey("a/b", "c", 1) == messageKey("a", "b/c", 1) {
		t.Fatal("a slash in the account made two messages one")
	}

	fs := newFakeService()
	at := time.Now().Add(-time.Hour)
	fs.addAccount(testAccount("a1", "Work", "work@example.com"), "secret", testFolders(), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Alpha", From: mail.Address{Name: "Ann", Email: "ann@example.com"}, Date: at,
	}})
	fs.addAccount(testAccount("a2", "Home", "home@example.com"), "secret", testFolders(), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Beta", From: mail.Address{Name: "Bo", Email: "bo@example.com"},
		To: []mail.Address{{Email: "home@example.com"}}, Date: at.Add(time.Minute),
	}})
	a, tt := startPaged(t, inMessageView(fs))
	both, err := fs.MessagePage(context.Background(), mail.ListQuery{Scope: mail.Scope{Folder: "INBOX"}})
	if err != nil || len(both.Messages) != 2 {
		t.Fatalf("both inboxes = %+v, %v", both, err)
	}
	a.setRows(a.listQuery(), listRows{msgs: both.Messages})
	a.list, a.selected = ui.ListState{}, -1
	a.sel.Clear()
	tt.Frame()
	if a.rowKey(0) == a.rowKey(1) {
		t.Fatalf("both rows are %q", a.rowKey(0))
	}

	click(t, tt, "Alpha")
	click(t, tt, "Beta")
	alpha, beta := a.bodies[summaryKey(one)], a.bodies[summaryKey(two)]
	if alpha.Message.BodyText != "Body of Alpha" || beta.Message.BodyText != "Body of Beta" {
		t.Fatalf("bodies: a1 %q, a2 %q", alpha.Message.BodyText, beta.Message.BodyText)
	}
	if !tt.HasText("Body of Beta") || tt.HasText("Body of Alpha") || !tt.HasText(locales.ToMe()) {
		t.Errorf("the pane does not show Beta to its account: %q", tt.Texts())
	}
	if err := tt.ClickWith(ui.Cmd, "Alpha"); err != nil {
		t.Fatal(err)
	}
	if got, want := chosenKeys(a), []string{summaryKey(one), summaryKey(two)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chosen %v, want %v", got, want)
	}

	// Starring both stars each in its own account.
	click(t, tt, locales.StarMessage())
	for _, id := range []string{"a1", "a2"} {
		if m, _ := fs.find(id, "INBOX", 1); !m.Flagged {
			t.Errorf("%s's message 1 is not starred", id)
		}
	}
	// A reply is written from the account of the message it answers, and
	// the AI reads the message as that account's.
	a.chooseRow(a.rowWithKey(summaryKey(two)))
	click(t, tt, locales.Reply())
	if cs := a.composes[len(a.composes)-1]; cs.accountID != "a2" || cs.source == nil || cs.source.Scope != "a2" {
		t.Errorf("the reply to a2's message is from %q, read as %+v", cs.accountID, cs.source)
	}
	// Archiving both moves a1's to a1's Archive, and leaves a2's, whose
	// folders the window does not hold, where it is.
	a.sel.Clear()
	a.sel.Add(summaryKey(one))
	a.sel.Add(summaryKey(two))
	tt.Frame()
	click(t, tt, locales.ArchiveMessage())
	if !reflect.DeepEqual(fs.moved, []string{"INBOX->Archive"}) {
		t.Errorf("moved = %v", fs.moved)
	}
	if _, ok := fs.find("a1", "Archive", 1001); !ok {
		t.Error("a1's message is not in a1's Archive")
	}
	if _, ok := fs.find("a2", "INBOX", 1); !ok || len(fs.messages[listKey("a2", "Archive")]) != 0 {
		t.Error("a2's message moved")
	}

	// A notification of a2's message 1 aims at it, not at a1's.
	q := queueJobs(a)
	a.showTarget(noteTarget{account: "a2", folder: "INBOX", validity: 7, uid: 1, message: true})
	if a.aim == nil || a.aim.key != summaryKey(two) {
		t.Errorf("the notification aims at %+v", a.aim)
	}
	q.drain(a)
	tt.Frame()
	if a.active != "a2" || a.selectedKey() != summaryKey(two) || !tt.HasText("Body of Beta") {
		t.Errorf("the notification opened %q of %q", a.selectedKey(), a.active)
	}
}

// Scrolling near the end of the rows read reads the next page, as the
// keys moving there do. The page comes in below: the rows in view, the
// one open and those chosen stay as they were, and the end of the list
// says what is happening, until the cache has no more.
func TestPagingKeepsPlaceAndChoice(t *testing.T) {
	fs := pagedService(pagedMail(250, nil), false)
	a, tt := startPaged(t, fs)
	if len(a.all) != listPage || !a.pager.more {
		t.Fatalf("first page: %d rows, more %v", len(a.all), a.pager.more)
	}
	if !tt.HasText(locales.MessageCount(250) + " · 0 unread") {
		// The cache holds the whole folder: no "of" in the line.
		t.Errorf("header: %q", tt.Texts())
	}
	click(t, tt, "Message 245")
	if err := tt.ClickWith(ui.Cmd, "Message 243"); err != nil {
		t.Fatal(err)
	}
	chosen, keys := a.selectedKey(), chosenKeys(a)
	if len(keys) != 2 {
		t.Fatalf("chosen %v", keys)
	}
	reads := fs.pageReads

	// The next page is held back, to see the list wait for it.
	q := queueJobs(a)
	scrollList(tt, toEnd)
	if !tt.HasText(locales.LoadingMore()) || len(q.jobs) != 1 {
		t.Fatalf("no row says a page is coming (%d jobs): %q", len(q.jobs), tt.Texts())
	}
	first, _ := a.list.Visible()
	q.drain(a)
	tt.Frame()
	if len(a.all) != 2*listPage {
		t.Fatalf("after the second page: %d rows", len(a.all))
	}
	if now, _ := a.list.Visible(); now != first {
		t.Errorf("the list moved from row %d to row %d", first, now)
	}
	if a.selectedKey() != chosen || !reflect.DeepEqual(chosenKeys(a), keys) {
		t.Errorf("the choice changed: %q %v, want %q %v", a.selectedKey(), chosenKeys(a), chosen, keys)
	}
	if !tt.HasText("Body of Message 243") {
		t.Errorf("the message open changed: %q", tt.Texts())
	}

	// The keys reach the end too: End chooses the last row read, and the
	// page after it comes.
	a.list.Handle.Focus()
	tt.Frame()
	tt.Key(0, ui.KeyEnd)
	if len(a.all) != 250 || a.pager.more || a.selectedKey() != "a1/INBOX/51" {
		t.Fatalf("End: %d rows, more %v, chosen %q", len(a.all), a.pager.more, a.selectedKey())
	}
	tt.Frame()
	tt.Key(0, ui.KeyEnd)
	if a.selectedKey() != "a1/INBOX/1" {
		t.Errorf("End again chose %q", a.selectedKey())
	}
	if !reflect.DeepEqual(uids(a.all), countdown(250)) {
		t.Errorf("rows = %v", uids(a.all))
	}
	if !tt.HasText(locales.AllOnDevice()) || tt.HasText(locales.LoadOlder()) {
		t.Errorf("the end does not say all the mail is here: %q", tt.Texts())
	}
	if fs.pageReads != reads+2 || len(fs.olderCalls) != 0 {
		t.Errorf("pages read %d, older fetched %v", fs.pageReads-reads, fs.olderCalls)
	}
}

// In conversation view a page is of conversations, and the one open
// stays open, as it was, while pages come below it.
func TestPagingConversations(t *testing.T) {
	// 150 conversations of two messages each.
	fs := pagedService(pagedMail(300, func(uid mail.UID) mail.ThreadID { return mail.ThreadID((uid + 1) / 2) }), true)
	a, tt := startPaged(t, fs)
	if len(a.convs) != listPage || len(a.all) != listPage || a.convs[0].Count != 2 {
		t.Fatalf("first page: %d conversations", len(a.convs))
	}
	if !tt.HasText(locales.ConversationCount(listPage)) && !tt.HasText(locales.MessageCount(300)+" · 0 unread") {
		t.Errorf("header: %q", tt.Texts())
	}
	click(t, tt, "Message 296")
	cv, chosen := a.conv, a.selectedKey()
	if cv == nil || chosen != "thread/a1/148" {
		t.Fatalf("open %+v, chosen %q", cv, chosen)
	}
	cv.open["a1/INBOX/295"] = true
	scrollList(tt, toEnd)
	scrollList(tt, toEnd)
	if len(a.convs) != 150 || a.pager.more {
		t.Fatalf("after paging: %d conversations, more %v", len(a.convs), a.pager.more)
	}
	if a.conv != cv || !cv.open["a1/INBOX/295"] || a.selectedKey() != chosen {
		t.Errorf("the conversation open changed: %+v, chosen %q", a.conv, a.selectedKey())
	}
	seen := map[mail.ThreadID]bool{}
	for i, c := range a.convs {
		if seen[c.Thread] || c.Thread != mail.ThreadID(150-i) || summaryKey(a.all[i]) != summaryKey(c.Latest) {
			t.Fatalf("row %d is conversation %d", i, c.Thread)
		}
		seen[c.Thread] = true
	}
}

// Starred mail and a search page as a folder does, and their ends say
// that there is no more.
func TestPagingStarredAndSearch(t *testing.T) {
	msgs := pagedMail(260, nil)
	for i := range msgs {
		uid := msgs[i].UID
		msgs[i].Flagged = uid%2 == 0
		if uid%5 == 0 {
			msgs[i].Folder = "Archive"
		}
		if uid > 100 {
			msgs[i].Subject += " invoice"
		}
	}
	fs := pagedService(msgs, false)
	a, tt := startPaged(t, fs)

	click(t, tt, locales.Starred())
	if len(a.all) != listPage || !tt.HasText(locales.MessageCountMore(listPage)) {
		t.Fatalf("starred: %d rows: %q", len(a.all), tt.Texts())
	}
	// The page comes below the rows in view; the end is further down.
	scrollList(tt, toEnd)
	scrollList(tt, toEnd)
	var want []mail.UID
	for uid := mail.UID(260); uid >= 2; uid -= 2 {
		want = append(want, uid)
	}
	if !reflect.DeepEqual(uids(a.all), want) {
		t.Fatalf("starred rows = %v", uids(a.all))
	}
	if !tt.HasText(locales.NoMoreMail()) || !tt.HasText(locales.MessageCount(130)) || tt.HasText(locales.LoadOlder()) {
		t.Errorf("the end of starred mail: %q", tt.Texts())
	}

	click(t, tt, locales.Inbox())
	click(t, tt, "Search mail")
	tt.Type("invoice")
	inbox := 0
	for uid := 260; uid > 100; uid-- {
		if uid%5 != 0 {
			inbox++
		}
	}
	if len(a.all) != listPage || !tt.HasText(fmt.Sprintf("%d+", listPage)) {
		t.Fatalf("search: %d rows: %q", len(a.all), tt.Texts())
	}
	click(t, tt, "Message 259 invoice")
	chosen := a.selectedKey()
	scrollList(tt, toEnd)
	scrollList(tt, toEnd)
	if len(a.all) != inbox || a.pager.more || a.selectedKey() != chosen || a.query != "invoice" {
		t.Fatalf("search after paging: %d rows of %d, chosen %q, query %q", len(a.all), inbox, a.selectedKey(), a.query)
	}
	if !tt.HasText(locales.NoMoreResults()) || !tt.HasText(fmt.Sprint(inbox)) {
		t.Errorf("the end of the search: %q", tt.Texts())
	}
}

// pagedWatch is a window on fs, watched, with the rows read past the
// first page and the list at their end.
func pagedWatch(t *testing.T, fs *fakeService) (*watchedApp, *ui.Tester) {
	t.Helper()
	w := watchApp(t, fs)
	w.a.start()
	tt := ui.NewTester(w.a.view, 1220, 800)
	scrollList(tt, toEnd)
	if len(w.a.all) != 2*listPage {
		t.Fatalf("rows read: %d", len(w.a.all))
	}
	return w, tt
}

// Mail that arrives while pages were read is read with as many rows as
// the list had, and the rest: none of those rows leaves it, the one in
// view stays in view and the one chosen stays chosen.
func TestNewMailKeepsThePagesRead(t *testing.T) {
	w, tt := pagedWatch(t, pagedService(pagedMail(350, nil), false))
	a := w.a
	// To the end of the two pages, where a third comes below.
	scrollList(tt, toEnd)
	if len(a.all) != 3*listPage {
		t.Fatalf("rows read: %d", len(a.all))
	}
	click(t, tt, "Message 155")
	first, _ := a.list.Visible()
	inView := a.rowKey(first)
	reads := w.fs.pageReads

	w.arrive(t, "a1", newMail(400, "Noor", "Lunch?"), newMail(401, "Noor", "Lunch!"))
	tt.Frame()
	if len(a.all) != 3*listPage+2 || a.all[0].Subject != "Lunch!" {
		t.Fatalf("after new mail: %d rows, first %q", len(a.all), a.all[0].Subject)
	}
	if a.selectedKey() != "a1/INBOX/155" || !tt.HasText("Body of Message 155") {
		t.Errorf("chosen %q", a.selectedKey())
	}
	if now, _ := a.list.Visible(); a.rowKey(now) != inView {
		t.Errorf("the row in view moved from %q to %q", inView, a.rowKey(now))
	}
	// The rows the list had, and the two pushed down, in two reads: the
	// first as long as the list was, the second as long as what came.
	if got := w.fs.pageReads - reads; got != 2 {
		t.Errorf("read %d pages", got)
	}
	// The next page goes on from there.
	scrollList(tt, toEnd)
	scrollList(tt, toEnd)
	if !reflect.DeepEqual(uids(a.all), append([]mail.UID{401, 400}, countdown(350)...)) {
		t.Errorf("rows = %v", uids(a.all))
	}
}

// A conversation a new message moves above the pages read is read with
// the first, once, and none is lost; one that merges into another goes,
// with the choice following to the conversation it is now part of.
func TestRefreshFollowsConversationsMoving(t *testing.T) {
	msgs := pagedMail(250, func(uid mail.UID) mail.ThreadID { return mail.ThreadID(uid) })
	w, tt := pagedWatch(t, pagedService(msgs, true))
	a := w.a
	before := map[string]bool{}
	for i := range a.all {
		before[a.rowKey(i)] = true
	}

	// A reply in the conversation of row 150, message 100.
	reply := newMail(400, "Noor", "Re: Message 100")
	reply.Thread = 100
	w.arrive(t, "a1", reply)
	tt.Frame()
	if len(a.convs) != 2*listPage || a.convs[0].Thread != 100 || a.convs[0].Count != 2 {
		t.Fatalf("after the reply: %d rows, first %+v", len(a.convs), a.convs[0])
	}
	seen := map[string]bool{}
	for i := range a.all {
		key := a.rowKey(i)
		if seen[key] || !before[key] {
			t.Fatalf("row %d %q twice or new", i, key)
		}
		seen[key] = true
	}

	// The conversation of a row in view, past the first page, merges into
	// the one of row 10, and is the one chosen. The row is scrolled to,
	// as which rows a window holds depends on the fonts of the platform.
	a.list.ScrollTo(a.rowWithKey("thread/a1/160"), ui.Center)
	tt.Frame()
	click(t, tt, "Message 160")
	if a.selectedKey() != "thread/a1/160" {
		t.Fatalf("chosen %q", a.selectedKey())
	}
	editFake(w.fs, "INBOX", 160, func(m *mail.Summary) { m.Thread = 241 })
	w.report(t, w.fs.checked("a1", time.Now()))
	tt.Frame()
	// As many rows as before: the next conversation takes the place.
	if len(a.convs) != 2*listPage || a.rowWithKey("thread/a1/160") >= 0 || a.convs[len(a.convs)-1].Thread != 50 {
		t.Fatalf("after the merge: %d rows", len(a.convs))
	}
	if a.selectedKey() != "thread/a1/241" || a.conv == nil || a.conv.thread != 241 {
		t.Errorf("the choice did not follow the merge: %q", a.selectedKey())
	}
}

// A page read for rows that are no longer on screen is dropped: the
// folder, the search or the rows changed while it was read.
func TestStalePagesAreDropped(t *testing.T) {
	fs := pagedService(pagedMail(450, nil), false)
	fs.messages[listKey("a1", "Sent")] = []mail.Summary{{UID: 9, Folder: "Sent", Subject: "Sent mail", Date: time.Now()}}
	a, tt := startPaged(t, fs)

	// Another folder.
	q := queueJobs(a)
	scrollList(tt, toEnd)
	if len(q.jobs) != 1 {
		t.Fatalf("jobs = %d", len(q.jobs))
	}
	click(t, tt, locales.Sent())
	q.drain(a)
	tt.Frame()
	if len(a.all) != 1 || a.all[0].Subject != "Sent mail" {
		t.Fatalf("the inbox's page came into Sent: %d rows", len(a.all))
	}

	// Another search, read before the page.
	click(t, tt, locales.Inbox())
	q = queueJobs(a)
	scrollList(tt, toEnd)
	click(t, tt, "Search mail")
	tt.Type("Message 1")
	page := q.jobs[0]
	q.jobs = q.jobs[1:]
	q.drain(a)
	page()
	tt.Frame()
	for _, m := range a.all {
		if m.Subject[:9] != "Message 1" {
			t.Fatalf("a page of the inbox came into the search: %q", m.Subject)
		}
	}

	// The list read again from the top, before the page.
	click(t, tt, "Clear")
	if a.query != "" || len(a.all) != listPage {
		t.Fatalf("search %q, %d rows", a.query, len(a.all))
	}
	q = queueJobs(a)
	scrollList(tt, toEnd)
	page = q.jobs[0]
	q.jobs = q.jobs[1:]
	a.readList(a.gen, false, false)
	q.drain(a)
	page()
	tt.Frame()
	scrollToLast(t, a, tt)
	if !reflect.DeepEqual(uids(a.all), countdown(450)) {
		t.Fatalf("%d rows = %v", len(a.all), uids(a.all))
	}

	// A page added while the list is read again from the top: that read
	// is read again, as far as the page went.
	click(t, tt, locales.Sent())
	click(t, tt, locales.Inbox())
	q = queueJobs(a)
	a.readList(a.gen, false, false)
	scrollList(tt, toEnd)
	top, page := q.jobs[0], q.jobs[1]
	q.jobs = q.jobs[2:]
	page()
	top()
	q.drain(a)
	tt.Frame()
	if !reflect.DeepEqual(uids(a.all), countdown(450)[:2*listPage]) {
		t.Fatalf("rows after the late read = %v", uids(a.all))
	}
}

// olderService holds an inbox of which the cache has the newest cached
// messages and the server older ones besides.
func olderService(cached, older int) *fakeService {
	all := pagedMail(cached+older, nil)
	fs := pagedService(all[older:], false)
	for i := older - 1; i >= 0; i-- {
		all[i].Account = "a1"
		fs.older[listKey("a1", "INBOX")] = append(fs.older[listKey("a1", "INBOX")], all[i])
	}
	return fs
}

// At the end of what the cache holds of a folder, the list offers the
// older mail on the server and fetches it only when asked: it shows the
// fetch under way, its failure with a retry, and goes on with what came,
// until the folder is all here. The header says how much is.
func TestFetchOlderFromTheEnd(t *testing.T) {
	fs := olderService(120, 150)
	a, tt := startPaged(t, fs)
	if !tt.HasText(locales.OnDeviceSummary(120, 270, 0)) {
		t.Errorf("header: %q", tt.Texts())
	}
	click(t, tt, "Message 268")
	chosen := a.selectedKey()
	scrollToLast(t, a, tt)
	if len(a.all) != 120 || !tt.HasText(locales.NoMoreHere()) || !tt.HasText(locales.LoadOlder()) {
		t.Fatalf("the end of the cache, %d rows: %q", len(a.all), tt.Texts())
	}
	if len(fs.olderCalls) != 0 {
		t.Fatalf("scrolling fetched from the server: %v", fs.olderCalls)
	}

	// It fails, and says so.
	fs.olderErr = errFake
	click(t, tt, locales.LoadOlder())
	if !tt.HasText(locales.FetchOlderFailed(errFake.Error())) || !tt.HasText(locales.Retry()) {
		t.Fatalf("no failure: %q", tt.Texts())
	}
	// Retried, it works: the fetch shows while it runs.
	fs.olderErr = nil
	q := queueJobs(a)
	click(t, tt, locales.RetryFetchOlder())
	if !tt.HasText(locales.FetchingOlder()) || tt.HasText(locales.LoadOlder()) {
		t.Fatalf("no progress: %q", tt.Texts())
	}
	q.drain(a)
	tt.Frame()
	if want := []string{"a1/INBOX/0", "a1/INBOX/0"}; !reflect.DeepEqual(fs.olderCalls, want) {
		t.Errorf("fetches = %v, want %v", fs.olderCalls, want)
	}
	// The settings' window came, 200 of the 150: all of the folder.
	scrollToLast(t, a, tt)
	if !reflect.DeepEqual(uids(a.all), countdown(270)) {
		t.Fatalf("rows = %v", uids(a.all))
	}
	if a.selectedKey() != chosen {
		t.Errorf("the choice moved to %q", a.selectedKey())
	}
	if !tt.HasText(locales.AllOnDevice()) || tt.HasText(locales.LoadOlder()) {
		t.Errorf("the end of a folder all here: %q", tt.Texts())
	}
	if !tt.HasText(locales.MailboxSummary(270, 0)) {
		t.Errorf("header: %q", tt.Texts())
	}
	if len(fs.olderCalls) != 2 {
		t.Errorf("fetches = %v", fs.olderCalls)
	}
}

// The keys reach the older mail: End goes to the last row, Tab to the
// button after it, and Space presses it.
func TestFetchOlderFromTheKeyboard(t *testing.T) {
	fs := olderService(60, 20)
	a, tt := startPaged(t, fs)
	a.list.Handle.Focus()
	tt.Frame()
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	if a.selectedKey() != "a1/INBOX/21" || !tt.HasText(locales.LoadOlder()) {
		t.Fatalf("End chose %q: %q", a.selectedKey(), tt.Texts())
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeySpace)
	if !reflect.DeepEqual(fs.olderCalls, []string{"a1/INBOX/0"}) {
		t.Fatalf("fetches = %v", fs.olderCalls)
	}
	scrollToLast(t, a, tt)
	if len(a.all) != 80 || !tt.HasText(locales.AllOnDevice()) {
		t.Errorf("%d rows: %q", len(a.all), tt.Texts())
	}
}

// A fetch that finds the folder renumbered, or never synced, reads the
// list from its top: a row chosen before may be another message now.
func TestFetchOlderReset(t *testing.T) {
	fs := olderService(150, 50)
	fs.olderReset = true
	a, tt := startPaged(t, fs)
	scrollToLast(t, a, tt)
	click(t, tt, "Message 52")
	if a.selectedKey() != "a1/INBOX/52" {
		t.Fatalf("chosen %q", a.selectedKey())
	}
	click(t, tt, locales.LoadOlder())
	if len(a.all) != listPage || a.selectedKey() == "a1/INBOX/52" || a.bodies[messageKey("a1", "INBOX", 52)].Message.Subject != "" {
		t.Fatalf("after the reset: %d rows, chosen %q", len(a.all), a.selectedKey())
	}
	if first, _ := a.list.Visible(); first != 0 {
		t.Errorf("the list shows row %d first", first)
	}
}

// A page that could not be read says so at the end, and is read again
// when the user asks, not before.
func TestLoadMoreFailure(t *testing.T) {
	fs := pagedService(pagedMail(150, nil), false)
	a, tt := startPaged(t, fs)
	fs.pageErr = errFake
	scrollList(tt, toEnd)
	if !tt.HasText(locales.LoadMoreFailed(errFake.Error())) {
		t.Fatalf("no failure at the end: %q", tt.Texts())
	}
	reads := fs.pageReads
	scrollList(tt, toTop)
	scrollList(tt, toEnd)
	if fs.pageReads != reads {
		t.Errorf("a failed page was read again unasked")
	}
	fs.pageErr = nil
	click(t, tt, locales.RetryLoadMore())
	tt.Frame()
	if len(a.all) != 150 || a.pager.failed != "" {
		t.Errorf("after the retry: %d rows, %q", len(a.all), a.pager.failed)
	}
}

// An empty folder and a search without results say so; a folder whose
// mail is all on the server offers it, as a search does there.
func TestEmptyListStates(t *testing.T) {
	fs := pagedService(nil, false)
	a, tt := startPaged(t, fs)
	if !tt.HasText(locales.EmptyFolder()) || tt.HasText(locales.LoadOlder()) {
		t.Fatalf("an empty folder: %q", tt.Texts())
	}
	click(t, tt, "Search mail")
	tt.Type("lunch")
	if !tt.HasText(locales.SearchNoResults("lunch")) || tt.HasText(locales.LoadOlder()) {
		t.Fatalf("no results: %q", tt.Texts())
	}
	click(t, tt, locales.SearchClear())

	// Every message cached was archived; older mail is on the server.
	fs = olderService(0, 30)
	fs.folders["a1"][0].Total = 30
	a, tt = startPaged(t, fs)
	if !tt.HasText(locales.NoneHere()) || !tt.HasText(locales.OnDeviceSummary(0, 30, 0)) {
		t.Fatalf("an empty cache: %q", tt.Texts())
	}
	click(t, tt, "Search mail")
	tt.Type("lunch")
	if !tt.HasText(locales.SearchedHere()) || !tt.HasText(locales.LoadOlder()) {
		t.Fatalf("no results here: %q", tt.Texts())
	}
	click(t, tt, locales.SearchClear())
	click(t, tt, locales.LoadOlder())
	if len(a.all) != 30 || !tt.HasText("Message 30") {
		t.Errorf("after the fetch: %d rows: %q", len(a.all), tt.Texts())
	}
}

// The first-sync window is a setting the engine keeps, and the window's
// syncs leave it to the engine.
func TestSyncWindowSetting(t *testing.T) {
	fs := pagedService(pagedMail(3, nil), false)
	a, tt := startPaged(t, fs)
	if !slices.Contains(fs.syncCalls, "a1/INBOX/0") {
		t.Errorf("syncs = %v", fs.syncCalls)
	}
	for _, call := range fs.syncCalls {
		if call[len(call)-2:] != "/0" {
			t.Errorf("a sync with a limit of its own: %v", fs.syncCalls)
		}
	}
	a.openSettings(settingsTabGeneral)
	tt.Frame()
	for _, text := range []string{locales.MailOnDevice(), locales.SyncWindow(), locales.SyncWindowDetail(), "1,000"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	click(t, tt, "1,000")
	if len(fs.saved) == 0 || fs.saved[len(fs.saved)-1].SyncWindow != 1000 {
		t.Fatalf("saved %+v", fs.saved)
	}
	// Read back by the next run.
	b := newTestApp(fs)
	b.setLocale = func(string) {}
	b.start()
	if b.mailSet.SyncWindow != 1000 || mail.SyncWindows[b.windowChoice] != 1000 {
		t.Errorf("read back %d (choice %d)", b.mailSet.SyncWindow, b.windowChoice)
	}
}

// A folder of tens of thousands of messages pages from its newest to its
// oldest and back: every row once and in order, the choice kept on every
// page, no page read outside a job, and a frame with every row read
// building only the rows in view.
func TestLargeFolderStaysResponsive(t *testing.T) {
	const n = 20000
	fs := pagedService(pagedMail(n, nil), false)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	// Jobs and their results still run inline, but each says where it
	// runs: a page read that is not in a job blocks the window.
	inJob, outside := false, 0
	a.run = func(f func()) {
		was := inJob
		inJob = true
		f()
		inJob = was
	}
	a.update = func(f func()) {
		was := inJob
		inJob = false
		f()
		inJob = was
	}
	fs.onPage = func(mail.ListQuery) {
		if !inJob {
			outside++
		}
	}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, fmt.Sprintf("Message %d", n-2))
	if err := tt.ClickWith(ui.Cmd, fmt.Sprintf("Message %d", n-4)); err != nil {
		t.Fatal(err)
	}
	chosen, keys := a.selectedKey(), chosenKeys(a)

	begin := time.Now()
	pages := 1
	for a.pager.more {
		read := len(a.all)
		scrollList(tt, toEnd)
		if len(a.all) != read+listPage {
			t.Fatalf("page %d: %d rows after %d", pages+1, len(a.all), read)
		}
		pages++
		if a.selectedKey() != chosen || !slices.Equal(chosenKeys(a), keys) {
			t.Fatalf("page %d: chosen %q %v, want %q %v", pages, a.selectedKey(), chosenKeys(a), chosen, keys)
		}
	}
	t.Logf("%d pages to the oldest message in %v", pages, time.Since(begin))
	if !reflect.DeepEqual(uids(a.all), countdown(n)) {
		t.Fatal("the rows are not every message once, newest first")
	}
	scrollList(tt, toEnd)
	if !tt.HasText("Message 1") || !tt.HasText(locales.AllOnDevice()) {
		t.Errorf("the end: %q", tt.Texts())
	}

	// A frame with every row read builds the rows in view, not all.
	start := time.Now()
	tt.Frame()
	took := time.Since(start)
	t.Logf("a frame with %d rows read took %v", len(a.all), took)
	if took > 300*time.Millisecond {
		t.Errorf("a frame with %d rows read took %v", len(a.all), took)
	}
	built := 0
	for _, text := range tt.Texts() {
		if len(text) > 8 && text[:8] == "Message " {
			built++
		}
	}
	if built == 0 || built > 60 {
		t.Errorf("%d rows built of %d", built, len(a.all))
	}

	// And back to the newest, with the choice where it was.
	scrollList(tt, toTop)
	if first, _ := a.list.Visible(); first != 0 || !tt.HasText(fmt.Sprintf("Message %d", n)) {
		t.Errorf("back at the top, row %d first", first)
	}
	if a.selectedKey() != chosen || !slices.Equal(chosenKeys(a), keys) || len(a.all) != n {
		t.Errorf("back at the top: chosen %q %v, %d rows", a.selectedKey(), chosenKeys(a), len(a.all))
	}
	if outside != 0 {
		t.Errorf("%d page reads ran outside a job", outside)
	}
}

// At the window's smallest, light and dark, the end of the list and its
// button, the header's line and the setting fit, and the button works.
func TestPagingAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		fs := olderService(120, 30)
		a, _ := startPaged(t, fs)
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		scrollToLast(t, a, tt)
		for _, label := range []string{locales.LoadOlder(), locales.NoMoreHere(), locales.OnDeviceSummary(120, 150, 0)} {
			r, ok := tt.Find(label)
			if !ok || r.W == 0 || r.X < 223 || r.X+r.W > 607 || r.Y+r.H > 640 {
				t.Errorf("dark=%v: %q is not in the list: %+v", dark, label, r)
			}
		}
		click(t, tt, locales.LoadOlder())
		if len(fs.olderCalls) != 1 {
			t.Errorf("dark=%v: fetches %v", dark, fs.olderCalls)
		}
		a.openSettings(settingsTabGeneral)
		tt.Frame()
		for _, label := range []string{locales.SyncWindow(), "1,000"} {
			if r, ok := tt.Find(label); !ok || r.X+r.W > 1000 {
				t.Errorf("dark=%v: %q is off screen: %+v", dark, label, r)
			}
		}
	}
}

// The states of the list's end read in Simplified Chinese.
func TestPagingStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	fs := olderService(120, 30)
	a, tt := startPaged(t, fs)
	tt.SetLocale("zh-Hans")
	scrollToLast(t, a, tt)
	for _, text := range []string{"本设备上没有更多邮件了。", "从服务器加载更早的邮件", "本设备上有 120 封，共 150 封 · 0 封未读"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	fs.olderErr = errFake
	click(t, tt, "从服务器加载更早的邮件")
	if !tt.HasText("无法获取更早的邮件：" + errFake.Error()) {
		t.Errorf("the failure: %q", tt.Texts())
	}
	fs.olderErr = nil
	click(t, tt, "重新获取更早的邮件")
	scrollToLast(t, a, tt)
	if !tt.HasText("此文件夹的全部邮件都已在本设备上。") {
		t.Errorf("the end: %q", tt.Texts())
	}
	if got := locales.MessageCountMore(100); got != "100+ 封邮件" {
		t.Errorf("more messages = %q", got)
	}
}

// rowWithKey is the row whose key is key, or -1.
func (a *app) rowWithKey(key string) int {
	for i := range a.all {
		if a.rowKey(i) == key {
			return i
		}
	}
	return -1
}

// The badge beside a folder's title is the folder's unread count, the one
// the sidebar shows, not the unread rows of the pages read so far. A list
// of Shu's own, as Starred, counts its rows and says when there are more.
func TestUnreadBadgeCountsTheFolder(t *testing.T) {
	msgs := pagedMail(250, nil)
	for i := range msgs {
		msgs[i].Seen, msgs[i].Flagged = false, true
	}
	fs := pagedService(msgs, false)
	fs.folders["a1"][0].Unread = 250
	a, tt := startPaged(t, fs)
	if len(a.all) != listPage || !a.pager.more {
		t.Fatalf("first page: %d rows, more %v", len(a.all), a.pager.more)
	}
	unread := a.folderUnread("INBOX")
	if unread <= listPage {
		t.Fatalf("the folder's unread count is %d", unread)
	}
	// The sidebar shows the same count: with it hidden, the badge by the
	// title is the only one.
	a.sidebarOpen = false
	tt.Frame()
	loaded := a.unread()
	texts := tt.Texts()
	if !slices.Contains(texts, fmt.Sprint(unread)) ||
		slices.Contains(texts, fmt.Sprint(loaded)) || slices.Contains(texts, fmt.Sprintf("%d+", loaded)) {
		t.Errorf("inbox badge, want %d, not the %d unread rows read: %q", unread, loaded, texts)
	}

	a.sidebarOpen = true
	tt.Frame()
	click(t, tt, locales.Starred())
	if !a.pager.more {
		t.Fatalf("starred: %d rows and no more", len(a.all))
	}
	a.sidebarOpen = false
	tt.Frame()
	want := fmt.Sprintf("%d+", a.unread())
	if !slices.Contains(tt.Texts(), want) {
		t.Errorf("starred badge, want %q: %q", want, tt.Texts())
	}
}

// A fetch that timed out says so in the user's words, however the engine
// wrapped it; other failures keep their own text.
func TestOlderErrorWording(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, fmt.Errorf("imap: fetch: %w", context.DeadlineExceeded)} {
		if got := olderError(err); got != locales.FetchOlderFailed(locales.ServerTooSlow()) {
			t.Errorf("olderError(%v) = %q", err, got)
		}
	}
	if got := olderError(errors.New("imap: select INBOX: NO no such mailbox")); !strings.Contains(got, "no such mailbox") {
		t.Errorf("other error = %q", got)
	}
}

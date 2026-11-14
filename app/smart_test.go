package app

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// saveSearch saves the search on screen as a smart mailbox of a name,
// from the sidebar's button, and returns it as the fake keeps it.
func saveSearch(t *testing.T, a *app, tt *ui.Tester, query, name string) mail.SmartMailbox {
	t.Helper()
	search(a, tt, query)
	click(t, tt, locales.SaveSearchEllipsis())
	if !a.smarts.form.open {
		t.Fatalf("save search opened nothing: %q", tt.Texts())
	}
	click(t, tt, locales.SmartMailboxNameCaption())
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(name)
	click(t, tt, locales.SaveSmartMailbox())
	list, _ := a.svc.SmartMailboxes(context.Background(), "a1")
	for _, mb := range list {
		if mb.Name == name {
			return mb
		}
	}
	t.Fatalf("no smart mailbox %q was saved: %q", name, tt.Texts())
	return mail.SmartMailbox{}
}

// openSmart opens a smart mailbox from the sidebar.
func openSmart(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	b := sidebarLabel(t, tt, name).Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
}

// smartMenu opens a smart mailbox's menu from its sidebar entry.
func smartMenu(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	b := sidebarLabel(t, tt, name).Bounds
	tt.RightClickAt(b.X+b.W/2, b.Y+b.H/2)
}

// Saving the search on screen makes a named entry that opens the same
// results, with paging, and its badge counts the unread mail it finds.
func TestSmartMailboxSavedSearch(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	// An older, unread message of the same search: opening a row marks it
	// read, so the badge counts this one.
	fs.messages["a1/INBOX"] = append(fs.messages["a1/INBOX"], mail.Summary{UID: 9, Folder: "INBOX",
		Subject: "Budget notes", Preview: "Older notes on the budget.", From: workMaya,
		To: []mail.Address{annWork}, Date: hoursAgo(40)})
	search(a, tt, "budget")
	want := []string{"a1/INBOX/1", "a1/Sent/6", "a1/INBOX/9"}
	if got := rowKeys(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("the search lists %v", got)
	}
	click(t, tt, locales.SaveSearchEllipsis())
	f := a.smarts.form
	if !f.open || f.query != "budget" || f.folder != "INBOX" || f.scope != mail.SmartScopeAccount {
		t.Fatalf("the dialog = %+v", f)
	}
	click(t, tt, locales.SmartMailboxNameCaption())
	tt.Type("Budget")
	click(t, tt, locales.SaveSmartMailbox())
	if len(fs.smarts["a1"]) != 1 {
		t.Fatalf("the saved search = %+v", fs.smarts)
	}
	mb := fs.smarts["a1"][0]
	if mb.Name != "Budget" || mb.Query != "budget" || mb.Folder != "INBOX" || mb.Scope != mail.SmartScopeAccount {
		t.Errorf("saved %+v", mb)
	}
	if a.folder != smartEntry(mb.ID) || a.query != "budget" || !tt.HasText("Budget") {
		t.Errorf("after saving, the list is %q with %q: %q", a.folder, a.query, tt.Texts())
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, want) {
		t.Errorf("the mailbox lists %v, the search did %v", got, want)
	}
	// Its badge is the unread mail it finds: the inbox's question, not the
	// answer in Sent.
	if n, err := a.svc.CountUnread(context.Background(), a.smartListQuery(mb).read("", 1)); err != nil || n != 1 {
		t.Logf("a count of the mailbox's list = %d, %v", n, err)
	}
	if len(a.smarts.list) != 1 || a.smarts.list[0].Unread != 1 {
		t.Errorf("the mailboxes = %+v", a.smarts.list)
	}
	// Another folder, then the entry: the search field and the rows are
	// the saved ones again.
	click(t, tt, locales.Inbox())
	if got := rowKeys(a); slices.Contains(got, "a1/Sent/6") {
		t.Fatalf("the inbox lists %v", got)
	}
	openSmart(t, tt, "Budget")
	if a.query != "budget" || a.folder != smartEntry(mb.ID) {
		t.Errorf("reopened at %q with %q", a.folder, a.query)
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, want) {
		t.Errorf("reopened, the mailbox lists %v", got)
	}
	// Opening the unread row marks it read, and the badge follows the
	// mail rather than waiting for a sync.
	click(t, tt, "Budget notes")
	if len(a.smarts.list) != 1 || a.smarts.list[0].Unread != 0 {
		t.Errorf("after reading it, the mailboxes = %+v", a.smarts.list)
	}
}

// A smart mailbox's list reads the cache a page at a time, as the search
// did.
func TestSmartMailboxPages(t *testing.T) {
	fs := twoAccountsService(false)
	for i := 0; i < 120; i++ {
		fs.messages["a1/INBOX"] = append(fs.messages["a1/INBOX"], mail.Summary{UID: mail.UID(100 + i),
			Folder: "INBOX", Subject: fmt.Sprintf("Weekly update %d", i), Date: hoursAgo(40 + i)})
	}
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	saveSearch(t, a, tt, "weekly", "Weekly")
	if len(a.all) != listPage || !a.pager.more {
		t.Fatalf("the first page has %d rows, more %v", len(a.all), a.pager.more)
	}
	a.loadMore()
	if len(a.all) != 120 || a.pager.more {
		t.Errorf("after the next page, %d rows, more %v", len(a.all), a.pager.more)
	}
}

// A smart mailbox changes its name and search in its dialog, the list it
// was saved from kept, and asks before it is deleted; a name the account
// has is refused in the dialog.
func TestSmartMailboxEditAndDelete(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	mb := saveSearch(t, a, tt, "budget", "Budget")
	// A name another mailbox has, in any case, keeps the dialog open.
	click(t, tt, locales.SaveSearchEllipsis())
	click(t, tt, locales.SmartMailboxNameCaption())
	tt.Type("budget")
	click(t, tt, locales.SaveSmartMailbox())
	if !a.smarts.form.open || a.smarts.form.err != locales.SmartMailboxExists("budget") {
		t.Fatalf("the dialog = %+v", a.smarts.form)
	}
	click(t, tt, locales.Cancel())
	if a.smarts.form.open {
		t.Errorf("Cancel left the dialog open")
	}
	// Its menu in the sidebar changes it.
	smartMenu(t, tt, "Budget")
	if err := tt.ChooseMenuItem(locales.EditSmartMailboxEllipsis()); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	if f := a.smarts.form; !f.open || f.editing != mb.ID || f.name != "Budget" || f.query != "budget" {
		t.Fatalf("the dialog = %+v", f)
	}
	click(t, tt, locales.SmartMailboxNameCaption())
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Finance")
	click(t, tt, locales.SmartSearchCaption())
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("invoice")
	click(t, tt, locales.Save())
	if len(fs.smarts["a1"]) != 1 {
		t.Fatalf("the mailboxes = %+v", fs.smarts)
	}
	after := fs.smarts["a1"][0]
	if after.ID != mb.ID || after.Name != "Finance" || after.Query != "invoice" || after.Folder != "INBOX" {
		t.Errorf("changed to %+v, was %+v", after, mb)
	}
	// The list shows its new search at once, and the sidebar its new name.
	if got := rowKeys(a); !reflect.DeepEqual(got, []string{"a1/Archive/4"}) {
		t.Errorf("the changed mailbox lists %v", got)
	}
	if !tt.HasText("Finance") || tt.HasText("Budget") {
		t.Errorf("the sidebar: %q", tt.Texts())
	}
	// Deleting it asks first; Cancel keeps it, and the confirmation drops
	// it, leaving the mail alone.
	smartMenu(t, tt, "Finance")
	if err := tt.ChooseMenuItem(locales.DeleteSmartMailboxEllipsis()); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	click(t, tt, locales.Cancel())
	if len(fs.smarts["a1"]) != 1 || a.smarts.del.asking {
		t.Fatalf("Cancel deleted it: %+v", fs.smarts)
	}
	smartMenu(t, tt, "Finance")
	if err := tt.ChooseMenuItem(locales.DeleteSmartMailboxEllipsis()); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	click(t, tt, locales.DeleteSmartMailboxButton())
	if len(fs.smarts["a1"]) != 0 {
		t.Fatalf("after deleting, %+v", fs.smarts)
	}
	if a.folder != "INBOX" || tt.HasText("Finance") {
		t.Errorf("after deleting, the list is %q: %q", a.folder, tt.Texts())
	}
	if len(fs.messages["a1/INBOX"]) != 2 || len(fs.messages["a1/Sent"]) != 2 {
		t.Errorf("the mail changed: %+v", fs.messages)
	}
}

// A smart mailbox saved from the drafts reads the drafts, and one saved
// with no typed text is its folder's list alone, as the list on screen is.
func TestSmartMailboxDraftsAndEmptySearch(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	if _, err := fs.SaveDraft(context.Background(), mail.Draft{ID: "d1", AccountID: "a1",
		To: "leo@work.example", Subject: "Offsite notes", Body: "The draft body."}); err != nil {
		t.Fatal(err)
	}
	click(t, tt, locales.Drafts())
	drafts := rowKeys(a)
	if len(drafts) == 0 {
		t.Fatalf("the drafts list nothing: %q", tt.Texts())
	}
	mb := saveSearch(t, a, tt, "", "My drafts")
	if mb.Folder != draftsFolder || mb.Scope != mail.SmartScopeHere {
		t.Errorf("saved %+v", mb)
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, drafts) {
		t.Errorf("the mailbox lists %v, the drafts did %v", got, drafts)
	}
	// An empty search of a folder does not widen to its account, as a
	// typed one would: saving it keeps the folder's own list.
	click(t, tt, locales.Inbox())
	inbox := rowKeys(a)
	if mb := saveSearch(t, a, tt, "", "Inbox only"); mb.Folder != "INBOX" || mb.Scope != mail.SmartScopeHere {
		t.Errorf("saved %+v", mb)
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, inbox) {
		t.Errorf("the mailbox lists %v, the inbox did %v", got, inbox)
	}
}

// Saved from the unified inbox, a smart mailbox is the unified inbox as
// it was: every account's inbox, which the window knows it shows.
func TestSmartMailboxFromAllInboxes(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, locales.AllInboxes())
	all := rowKeys(a)
	if mb := saveSearch(t, a, tt, "", "Every inbox"); mb.Folder != allInboxes || mb.Scope != mail.SmartScopeHere {
		t.Errorf("saved %+v", mb)
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, all) {
		t.Errorf("the mailbox lists %v, the inboxes did %v", got, all)
	}
	if !a.isUnified() {
		t.Error("the mailbox is not the unified inbox")
	}
	click(t, tt, locales.Inbox())
	openSmart(t, tt, "Every inbox")
	if !a.isUnified() {
		t.Error("reopened, the mailbox is not the unified inbox")
	}
	if got := rowKeys(a); !reflect.DeepEqual(got, all) {
		t.Errorf("reopened, the mailbox lists %v", got)
	}
}

// A saved search whose account: part keeps no account lists nothing and
// counts nothing, rather than widening to every account.
func TestSmartMailboxNoAccount(t *testing.T) {
	a, _, tt := startTwo(t, false)
	saveSearch(t, a, tt, "account:nobody budget", "Nobody")
	if got := rowKeys(a); len(got) != 0 {
		t.Errorf("the mailbox lists %v", got)
	}
	if len(a.smarts.list) != 1 || a.smarts.list[0].Unread != 0 {
		t.Errorf("the mailboxes = %+v", a.smarts.list)
	}
}

// Switching accounts leaves none of the other account's smart mailboxes
// in the sidebar, and an entry left from before the switch reads no list
// rather than the account's every folder.
func TestSmartMailboxOtherAccount(t *testing.T) {
	a, _, tt := startTwo(t, false)
	mb := saveSearch(t, a, tt, "budget", "Budget")
	a.switchAccount("a2")
	a.loadSmarts(a.gen)
	tt.Frame()
	if len(a.smarts.list) != 0 || tt.HasText("Budget") {
		t.Errorf("the other account's sidebar: %q", tt.Texts())
	}
	a.folder = smartEntry(mb.ID)
	a.readList(a.gen, false, false)
	tt.Frame()
	if len(a.all) != 0 {
		t.Errorf("the stale entry lists %v", rowKeys(a))
	}
}

// A smart mailbox saved from a folder that is gone says so, and lists
// nothing: the search that was saved has no list to read.
func TestSmartMailboxMissingFolder(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	fs.smarts["a1"] = []mail.SmartMailbox{{ID: 9, Account: "a1", Name: "Invoices", Folder: "Archive",
		Query: "invoice", Scope: mail.SmartScopeHere}}
	a.loadSmarts(a.gen)
	tt.Frame()
	openSmart(t, tt, "Invoices")
	if got := rowKeys(a); !reflect.DeepEqual(got, []string{"a1/Archive/4"}) {
		t.Fatalf("the mailbox lists %v", got)
	}
	fs.folders["a1"] = slices.DeleteFunc(fs.folders["a1"], func(f mail.Folder) bool { return f.Name == "Archive" })
	a.setFolders(fs.folders["a1"])
	a.readList(a.gen, false, false)
	tt.Frame()
	if len(a.all) != 0 || !tt.HasText(locales.SmartFolderGone("Invoices")) {
		t.Errorf("with the folder gone: %d rows: %q", len(a.all), tt.Texts())
	}
}

// The smart mailboxes' names read in Simplified Chinese.
func TestSmartMailboxStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	_, _, tt := startTwo(t, false)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	for _, text := range []string{"智能邮箱", "保存搜索…", "还没有智能邮箱。"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if got := locales.SmartMailboxExists("每周报告"); got != "已存在名为“每周报告”的智能邮箱。" {
		t.Errorf("the refusal = %q", got)
	}
}

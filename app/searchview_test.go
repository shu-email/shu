package app

import (
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

// clickRole clicks the middle of the element of a role and a name, where
// another element shows the same words, as a filter's chip and its check
// box do.
func clickRole(t *testing.T, tt *ui.Tester, role ui.Role, name string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, name)
	if !ok {
		t.Fatalf("no %v named %q: %q", role, name, tt.Texts())
	}
	b := info.Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
}

// scopeChosen is the segment of the scope control that is chosen.
func scopeChosen(tt *ui.Tester) string {
	for _, name := range []string{locales.ThisFolder(), locales.ScopeInboxes(), locales.AllFolders(), locales.AllAccounts()} {
		if info, ok := tt.AccessByRole(ui.RoleRadio, name); ok && info.Checked {
			return name
		}
	}
	return ""
}

// search types a search into the field, in place of the one there.
func search(a *app, tt *ui.Tester, query string) {
	a.query = query
	a.search()
	tt.Frame()
}

// From Work's inbox a search covers Work's every folder, and finds the
// reply in Sent; the scope shows above the results, and its choice of
// every account finds Home's quote in Home's Archive, the text kept and
// the rows read again. Back to the folder alone, nothing is found, which
// the list says with where it looked, and offers to look wider.
func TestSearchScope(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("pergola")
	if got := rowKeys(a); !reflect.DeepEqual(got, []string{"a1/Sent/3"}) {
		t.Fatalf("Work's folders: %v", got)
	}
	if got := scopeChosen(tt); got != locales.AllFolders() || !tt.HasText(locales.ThisFolder()) || !tt.HasText(locales.AllAccounts()) {
		t.Errorf("scope %q: %q", got, tt.Texts())
	}
	if _, folder := a.rowPlace(a.all[0]); folder != locales.Sent() || !tt.HasText(locales.MessageCount(1)+" · "+locales.WhereAccount("Work")) {
		t.Errorf("the reply's folder %q: %q", folder, tt.Texts())
	}
	reads := fs.pageReads
	click(t, tt, locales.AllAccounts())
	if got := rowKeys(a); !reflect.DeepEqual(got, []string{"a1/Sent/3", "a2/All Mail/5"}) || a.query != "pergola" || fs.pageReads == reads {
		t.Fatalf("every account: %v, query %q, reads %d", got, a.query, fs.pageReads-reads)
	}
	if account, folder := a.rowPlace(a.all[1]); account != "Home" || folder != locales.Archive() || scopeChosen(tt) != locales.AllAccounts() {
		t.Errorf("Home's quote says %q %q, scope %q", account, folder, scopeChosen(tt))
	}
	if !tt.HasText(locales.MessageCount(2) + " · " + locales.WhereAllAccounts()) {
		t.Errorf("header: %q", tt.Texts())
	}
	click(t, tt, "Garden pergola quote")
	if !tt.HasText(locales.InAccountFolder("Home", locales.Archive())) || !tt.HasText("Body of Garden pergola quote") {
		t.Errorf("Home's quote open: %q", tt.Texts())
	}

	click(t, tt, locales.ThisFolder())
	if len(a.all) != 0 || a.query != "pergola" || !tt.HasText(locales.SearchNoResults("pergola")) ||
		!tt.HasText(locales.SearchedWhere(locales.WhereFolderOf(locales.Inbox(), "Work"))) || !tt.HasText(locales.SearchedHere()) {
		t.Fatalf("the inbox alone: %d rows: %q", len(a.all), tt.Texts())
	}
	click(t, tt, locales.SearchAllFolders())
	if got := rowKeys(a); !reflect.DeepEqual(got, []string{"a1/Sent/3"}) || scopeChosen(tt) != locales.AllFolders() {
		t.Errorf("widened: %v, scope %q", got, scopeChosen(tt))
	}
	// With one account there is no scope of every account.
	one, _ := sampleApp(t)
	one.query = "review"
	one.search()
	if tt := ui.NewTester(one.view, 1220, 800); tt.HasText(locales.AllAccounts()) || !tt.HasText(locales.AllFolders()) {
		t.Errorf("one account: %q", tt.Texts())
	}
}

// A new scope reads the results from their top again: the pages read of
// the scope before are not continued.
func TestScopeChangeResetsPaging(t *testing.T) {
	msgs := pagedMail(250, nil)
	for i := range msgs {
		if msgs[i].UID%2 == 0 {
			msgs[i].Folder = "Archive"
		}
	}
	a, tt := startPaged(t, pagedService(msgs, false))
	click(t, tt, "Search mail")
	tt.Type("Preview")
	scrollList(tt, toEnd)
	if len(a.all) != 2*listPage || scopeChosen(tt) != locales.AllFolders() {
		t.Fatalf("every folder: %d rows, scope %q", len(a.all), scopeChosen(tt))
	}
	click(t, tt, locales.ThisFolder())
	if len(a.all) != listPage || !a.pager.more || a.all[0].UID != 249 || a.query != "Preview" {
		t.Fatalf("the inbox: %d rows from %d, more %v", len(a.all), a.all[0].UID, a.pager.more)
	}
	if first, _ := a.list.Visible(); first != 0 {
		t.Errorf("the list starts at row %d", first)
	}
	scrollToLast(t, a, tt)
	if len(a.all) != 125 || !tt.HasText(locales.NoMoreResults()) {
		t.Errorf("the inbox to its end: %d rows: %q", len(a.all), tt.Texts())
	}
}

// What is typed is read as the engine reads a search: each part narrows
// the results, the parts combine, and each result says where its words
// were found.
func TestSearchSyntax(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("from:maya")
	for _, c := range []struct {
		query, scope string
		want         []string
		reason       string
	}{
		{"from:maya", "", []string{"a1/INBOX/1"}, "Matched: from"},
		{"to:maya", "", []string{"a1/Sent/6"}, "Matched: to"},
		{"subject:budget", "", []string{"a1/INBOX/1", "a1/Sent/6"}, "Matched: subject"},
		{"body:forecast", "", []string{"a1/INBOX/1", "a1/Sent/6"}, "Matched: body"},
		{"is:unread", "", []string{"a1/INBOX/2"}, ""},
		{"has:attachment", "", []string{"a1/Archive/4"}, ""},
		{"before:2026-03-01", "", []string{"a1/Archive/4"}, ""},
		{"after:2026-03-01 forecast", "", []string{"a1/INBOX/1", "a1/Sent/6"}, "Matched: body"},
		{"after:2026-02-01 before:2026-03-01 has:attachment", "", []string{"a1/Archive/4"}, ""},
		{"in:sent", "", []string{"a1/Sent/3", "a1/Sent/6"}, ""},
		{"from:priya lunch", "", []string{"a1/INBOX/2"}, "Matched: from, subject"},
		{`"forecast for"`, "", []string{"a1/INBOX/1"}, "Matched: body"},
		{"budget from:ann in:sent", "", []string{"a1/Sent/6"}, "Matched: from, subject"},
		{"is:starred", "", nil, ""},
		{"is:starred has:attachment", locales.AllAccounts(), []string{"a2/INBOX/2"}, ""},
		{"is:unread in:inbox", locales.AllAccounts(), []string{"a2/INBOX/1", "a1/INBOX/2"}, ""},
		{"from:sam is:unread before:2026-03-01", locales.AllAccounts(), nil, ""},
	} {
		search(a, tt, c.query)
		if c.scope != "" && scopeChosen(tt) != c.scope {
			click(t, tt, c.scope)
		}
		got := rowKeys(a)
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v, want %v", c.query, got, c.want)
			continue
		}
		if c.reason != "" && countText(tt, c.reason) != len(c.want) {
			t.Errorf("%q: %d rows say %q: %q", c.query, countText(tt, c.reason), c.reason, tt.Texts())
		}
		if c.reason == "" && strings.Contains(strings.Join(tt.Texts(), "\n"), "Matched") {
			t.Errorf("%q: a search of flags, days or folders says what matched: %q", c.query, tt.Texts())
		}
	}
}

// The filter control writes what is chosen in it into the search field,
// and shows what is typed there: the field is the search, both ways. Its
// filters show beside the results, each of which can be taken off.
func TestFilterControlWritesTheQuery(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("budget")
	click(t, tt, locales.SearchFilters())
	click(t, tt, locales.FilterFrom())
	tt.Type("maya")
	if a.query != "budget from:maya" || !reflectRows(a, "a1/INBOX/1") {
		t.Fatalf("From: %q: %v", a.query, rowKeys(a))
	}
	if !tt.HasText(locales.FilterFromValue("maya")) {
		t.Errorf("no chip of the filter: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleCheckBox, locales.FilterUnread())
	if a.query != "budget from:maya is:unread" || len(a.all) != 0 {
		t.Fatalf("Unread only: %q: %v", a.query, rowKeys(a))
	}
	clickRole(t, tt, ui.RoleCheckBox, locales.FilterUnread())
	if a.query != "budget from:maya" || !reflectRows(a, "a1/INBOX/1") {
		t.Fatalf("Unread only again: %q", a.query)
	}

	// A folder from the menu. The inbox's own scope cannot hold another
	// folder: the search covers the account, as the control shows.
	click(t, tt, locales.ThisFolder())
	click(t, tt, locales.SearchFilters())
	click(t, tt, locales.AnyFolder())
	if err := tt.ChooseMenuItem(locales.Sent()); err != nil {
		t.Fatal(err)
	}
	if a.query != "budget from:maya in:sent" || len(a.all) != 0 || scopeChosen(tt) != locales.AllFolders() {
		t.Fatalf("in Sent: %q: %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
	if !tt.HasText(locales.FilterInFolder(locales.Sent())) {
		t.Errorf("no chip of the folder: %q", tt.Texts())
	}

	// A day, as the box beside it turns it on.
	now := tt.Now()
	y, m, d := now.In(searchZone).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, searchZone)
	clickRole(t, tt, ui.RoleCheckBox, locales.FilterAfter())
	if want := "budget from:maya in:sent " + dayOperator("after", today); a.query != want {
		t.Fatalf("On or after: %q, want %q", a.query, want)
	}
	if !tt.HasText(locales.FilterAfterDay(i18n.FormatDate("en", today, i18n.DateOptions{Skeleton: "yMMMd"}))) {
		t.Errorf("no chip of the day: %q", tt.Texts())
	}

	// What is typed shows in the control.
	search(a, tt, "budget is:starred to:lin")
	if info, ok := tt.AccessByRole(ui.RoleCheckBox, locales.FilterStarred()); !ok || !info.Checked {
		t.Errorf("Starred only, typed: %+v", info)
	}
	if info, ok := tt.AccessByRole(ui.RoleCheckBox, locales.FilterUnread()); !ok || info.Checked {
		t.Errorf("Unread only, not typed: %+v", info)
	}
	click(t, tt, locales.ClearFilters())
	if a.query != "budget" {
		t.Errorf("cleared: %q", a.query)
	}

	// A chip takes its filter out of the text, and only it.
	click(t, tt, locales.SearchFilters())
	if a.filters.open {
		t.Fatal("the filter control's button does not close it")
	}
	search(a, tt, `"quarterly budget" from:maya is:unread`)
	click(t, tt, locales.RemoveFilter(locales.FilterFromValue("maya")))
	if a.query != `"quarterly budget" is:unread` {
		t.Errorf("after removing From: %q", a.query)
	}
}

// reflectRows reports whether the list's rows are those keys.
func reflectRows(a *app, keys ...string) bool { return reflect.DeepEqual(rowKeys(a), keys) }

// An in: of another folder cannot be met by the list's own folder: the
// search covers the account, or in the unified inbox every account, and
// choosing the folder's scope again takes the in: out of the text.
func TestInWidensTheScope(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("budget")
	click(t, tt, locales.ThisFolder())
	if !reflectRows(a, "a1/INBOX/1") {
		t.Fatalf("the inbox: %v", rowKeys(a))
	}
	search(a, tt, "budget in:sent")
	if !reflectRows(a, "a1/Sent/6") || scopeChosen(tt) != locales.AllFolders() {
		t.Fatalf("in Sent: %v, scope %q", rowKeys(a), scopeChosen(tt))
	}
	click(t, tt, locales.ThisFolder())
	if a.query != "budget" || !reflectRows(a, "a1/INBOX/1") || scopeChosen(tt) != locales.ThisFolder() {
		t.Fatalf("back to the inbox: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}

	click(t, tt, locales.AllInboxes())
	search(a, tt, "in:archive")
	if !reflectRows(a, "a2/All Mail/5", "a1/Archive/4") || scopeChosen(tt) != locales.AllAccounts() {
		t.Errorf("the unified inbox, in Archive: %v, scope %q", rowKeys(a), scopeChosen(tt))
	}
	search(a, tt, "dinner")
	if !reflectRows(a, "a2/INBOX/1") || scopeChosen(tt) != locales.ScopeInboxes() || tt.HasText(locales.AllFolders()) {
		t.Errorf("the unified inbox's search: %v, scope %q: %q", rowKeys(a), scopeChosen(tt), tt.Texts())
	}
}

// A conversation found says in how many of its messages, and where; open,
// it shows those messages expanded, each saying where it matched.
func TestConversationMatchReasons(t *testing.T) {
	a, _, tt := startTwo(t, true)
	click(t, tt, "Search mail")
	tt.Type("forecast")
	if len(a.convs) != 1 || a.convs[0].Thread != 7 || len(a.convs[0].Found) != 2 {
		t.Fatalf("results: %+v", a.convs)
	}
	if !tt.HasText(locales.MatchedInMessages(2, locales.FieldBody())) {
		t.Errorf("the row: %q", tt.Texts())
	}
	click(t, tt, "Quarterly budget review")
	if got := openKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/Sent/6", "a1/INBOX/1"}) {
		t.Errorf("expanded %v", got)
	}
	if countText(tt, locales.MatchedFields(locales.FieldBody())) != 2 {
		t.Errorf("the messages found: %q", tt.Texts())
	}
	// A search of the sender names the sender, in the one message.
	search(a, tt, "from:maya")
	if !tt.HasText(locales.MatchedInMessages(1, locales.FieldFrom())) {
		t.Errorf("from Maya: %q", tt.Texts())
	}
}

// An empty search says what it looked for, where, with which filters, and
// that only this device's mail was; it offers to look wider, and its
// results then say so. A list read to its end says that there is no more
// of it on this device, and where older mail comes from.
func TestSearchStates(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("nothing from:zed")
	for _, text := range []string{
		locales.SearchNoResults("nothing"), locales.SearchedWhere(locales.WhereAccount("Work")),
		locales.WithFilters(locales.FilterFromValue("zed")), locales.SearchedHere(), locales.SearchAllAccounts(),
	} {
		if !tt.HasText(text) {
			t.Errorf("no results: missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "is:starred")
	if !tt.HasText(locales.NoFilterResults()) {
		t.Errorf("filters alone: %q", tt.Texts())
	}
	click(t, tt, locales.SearchAllAccounts())
	if !reflectRows(a, "a2/INBOX/2") || tt.HasText(locales.SearchAllAccounts()) {
		t.Errorf("every account: %v: %q", rowKeys(a), tt.Texts())
	}
	if !tt.HasText(locales.NoMoreResultsHere()) || !tt.HasText(locales.OlderInFolderHint()) {
		t.Errorf("the end of the results: %q", tt.Texts())
	}

	// The unified inbox loads, then ends.
	search(a, tt, "")
	q := queueJobs(a)
	click(t, tt, locales.AllInboxes())
	if !tt.HasText(locales.Loading()) {
		t.Errorf("loading: %q", tt.Texts())
	}
	q.drain(a)
	tt.Frame()
	if len(a.all) != 4 || !tt.HasText(locales.NoMoreHere()) || !tt.HasText(locales.OlderInFolderHint()) {
		t.Errorf("the end of the unified inbox: %d rows: %q", len(a.all), tt.Texts())
	}
}

// The filter control is the stop of Tab after the search field, and the
// scope control one of its own. Typing in the filters' fields types: the
// list's one-key shortcuts do not take the keys.
func TestSearchKeyboard(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("budget")
	tt.Key(0, ui.KeyTab)
	if !tt.Focused(locales.SearchFilters()) {
		t.Errorf("Tab from the search field went elsewhere")
	}
	reached := false
	for i := 0; i < 60 && !reached; i++ {
		tt.Key(0, ui.KeyTab)
		reached = tt.Focused(locales.AllFolders())
	}
	if !reached {
		t.Fatal("Tab never reaches the scope control")
	}
	tt.Key(0, ui.KeyRight)
	if scopeChosen(tt) != locales.AllAccounts() || len(a.all) != 2 {
		t.Errorf("the arrow on the scope control: %q, %v", scopeChosen(tt), rowKeys(a))
	}

	click(t, tt, "Quarterly budget review")
	chosen := a.selectedKey()
	fs.acts = nil
	click(t, tt, locales.SearchFilters())
	click(t, tt, locales.FilterFrom())
	tt.Type("sejrfuk")
	if a.query != "budget from:sejrfuk" {
		t.Errorf("typed into From: %q", a.query)
	}
	for _, call := range fs.acts {
		if !strings.Contains(call, "Fetch") {
			t.Errorf("a key acted on the mail: %q", call)
		}
	}
	if a.selectedKey() != chosen && a.selectedKey() != "" {
		t.Errorf("a key moved the choice to %q", a.selectedKey())
	}
	if len(a.composes) != 0 {
		t.Error("a key opened a reply")
	}
}

// The search's controls and the unified inbox's labels fit the list at
// the window's smallest, light and dark, and the filters fit the window.
func TestSearchAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		fs := twoAccountsService(false)
		a := newTestApp(fs)
		a.setLocale = func(string) {}
		a.start()
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		inList := func(label string) {
			t.Helper()
			r, ok := tt.Find(label)
			if !ok || r.W == 0 || r.X < 223 || r.X+r.W > 607 || r.Y+r.H > 640 {
				t.Errorf("dark=%v: %q is not in the list: %+v", dark, label, r)
			}
		}
		click(t, tt, locales.AllInboxes())
		inList(locales.AccountUnread("Work", 1) + " · " + locales.AccountUnread("Home", 1))
		inList(locales.InAccount("Home"))
		search(a, tt, `"budget review" from:maya is:unread has:attachment after:2026-03-01`)
		click(t, tt, locales.AllAccounts())
		for _, label := range []string{
			locales.ScopeInboxes(), locales.AllAccounts(), locales.FilterFromValue("maya"), locales.FilterAttachment(),
			locales.RemoveFilter(locales.FilterAfterDay("Mar 1, 2026")),
		} {
			inList(label)
		}
		click(t, tt, locales.SearchFilters())
		for _, label := range []string{locales.FilterFrom(), locales.ClearFilters(), locales.AnyFolder(), locales.AnyAccount(), locales.FilterSyntaxHint()} {
			if r, ok := tt.Find(label); !ok || r.W == 0 || r.X+r.W > 1000 || r.Y+r.H > 640 {
				t.Errorf("dark=%v: %q is off the window: %+v", dark, label, r)
			}
		}
		clickRole(t, tt, ui.RoleCheckBox, locales.FilterStarred())
		if !strings.Contains(a.query, "is:starred") {
			t.Errorf("dark=%v: Starred only at the smallest size: %q", dark, a.query)
		}
	}
}

// The unified inbox and the search read in Simplified Chinese.
func TestSearchStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, _, tt := startTwo(t, true)
	tt.SetLocale("zh-Hans")
	click(t, tt, "所有收件箱")
	for _, text := range []string{"Work 1 · Home 1", "账户：Home"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "forecast")
	click(t, tt, "所有账户")
	for _, text := range []string{"仅收件箱", "所有账户", "2 封邮件匹配：正文", "1 个会话 · 所有账户的所有文件夹", "本设备上的邮件中没有更多结果了。"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "")
	click(t, tt, "收件箱")
	search(a, tt, "nothing from:zed")
	for _, text := range []string{"此文件夹", "所有文件夹", "已搜索：Work的所有文件夹。", "筛选条件：发件人：zed", "搜索所有账户", "没有匹配“nothing”的邮件。"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "pergola account:school")
	for _, text := range []string{"账户：school（无此账户）", "没有名称或地址包含“school”的账户。", "没有会话 · 无账户"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "pergola account:home")
	click(t, tt, "搜索筛选条件")
	for _, text := range []string{"账户：home", "账户", "也可以直接输入 from:、to:、subject:、body:、after:、before:、is:unread、is:starred、has:attachment、in:、label: 和 account:。"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	search(a, tt, "pergola")
	if !tt.HasText("任意账户") {
		t.Errorf("missing 任意账户 in %q", tt.Texts())
	}
	click(t, tt, "所有收件箱")
	search(a, tt, "account:home")
	if !tt.HasText("2 个会话 · Home的收件箱") {
		t.Errorf("the unified inbox of Home: %q", tt.Texts())
	}
	if got := matchedLine(mail.FieldFrom | mail.FieldBody); got != "匹配：发件人、正文" {
		t.Errorf("reasons = %q", got)
	}
	if got := fmt.Sprint(locales.MatchedInMessages(1, locales.FieldSubject())); got != "1 封邮件匹配：主题" {
		t.Errorf("a conversation's reason = %q", got)
	}
}

// A result of another account deleted from a search of every account goes
// to that account's Trash, and Undo brings it back to the folder it was
// in, which is synced for its row to return.
func TestUndoInASearchOfEveryAccount(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("pergola")
	click(t, tt, locales.AllAccounts())
	click(t, tt, "Garden pergola quote")
	fs.acts = nil
	click(t, tt, locales.DeleteMessage())
	// Work's result that took the row's place opens, which is no move.
	if _, ok := fs.find("a2", "Deleted Items", 1005); !ok || len(actsLike(fs, "a1", "Move")) != 0 {
		t.Fatalf("deleted: %v: %q", fs.acts, a.err)
	}
	click(t, tt, locales.Undo())
	if _, ok := fs.find("a2", "All Mail", 5); !ok || !reflect.DeepEqual(actsLike(fs, "a2", "MoveBack"), []string{"MoveBack Deleted Items/1005->All Mail"}) {
		t.Fatalf("undone: %v", fs.acts)
	}
	if !slices.Contains(fs.syncCalls, "a2/All Mail/0") || !slices.Contains(rowKeys(a), "a2/All Mail/5") {
		t.Errorf("after the undo: syncs %v, rows %v", fs.syncCalls, rowKeys(a))
	}
}

// A typed account: keeps the accounts whose name or address has its
// value, in any case, and combines with the rest of the search, which the
// engine reads without it. One that leaves out the account on screen
// widens the search to every account; one that keeps it searches it alone.
func TestAccountSearch(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("pergola")
	for _, c := range []struct {
		query, scope string
		want         []string
	}{
		{"account:home pergola", locales.AllAccounts(), []string{"a2/All Mail/5"}},
		{"account:HOME", locales.AllAccounts(), []string{"a2/INBOX/1", "a2/INBOX/2", "a2/All Mail/5"}},
		{`account:"ann@home"`, locales.AllAccounts(), []string{"a2/INBOX/1", "a2/INBOX/2", "a2/All Mail/5"}},
		{"account：Home from:sam", locales.AllAccounts(), []string{"a2/INBOX/1"}},
		{"account:home in:archive", locales.AllAccounts(), []string{"a2/All Mail/5"}},
		{"is:unread account:home in:inbox", locales.AllAccounts(), []string{"a2/INBOX/1"}},
		{"account:work pergola", locales.AllFolders(), []string{"a1/Sent/3"}},
		{"account:WORK in:sent budget", locales.AllFolders(), []string{"a1/Sent/6"}},
		// Both accounts' addresses have it: the account on screen's
		// folders are the scope's, and it keeps them.
		{"account:example pergola", locales.AllFolders(), []string{"a1/Sent/3"}},
	} {
		search(a, tt, c.query)
		got := rowKeys(a)
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) || scopeChosen(tt) != c.scope || a.err != "" {
			t.Errorf("%q: %v in %q, want %v in %q (%q)", c.query, got, scopeChosen(tt), c.want, c.scope, a.err)
		}
	}
	search(a, tt, "account:home pergola")
	if !tt.HasText(locales.MessageCount(1)+" · "+locales.WhereAccount("Home")) || !tt.HasText(locales.FilterAccountValue("home")) {
		t.Errorf("Home's search says: %q", tt.Texts())
	}
	// Every account chosen, the value keeps both.
	search(a, tt, "account:example pergola")
	click(t, tt, locales.AllAccounts())
	if !reflectRows(a, "a1/Sent/3", "a2/All Mail/5") || a.query != "account:example pergola" {
		t.Errorf("every account: %v, %q", rowKeys(a), a.query)
	}
	if !tt.HasText(locales.MessageCount(2) + " · " + locales.WhereAllAccounts()) {
		t.Errorf("both accounts kept: %q", tt.Texts())
	}
}

// The scope control and account: say the same: choosing the folder's or
// the account's scope takes out the account: parts that leave the account
// on screen out, and keeps the others. Every account keeps them all. A
// chip takes its account: out like any filter's.
func TestAccountScope(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("budget")
	click(t, tt, locales.ThisFolder())
	search(a, tt, "budget account:home")
	if len(a.all) != 0 || scopeChosen(tt) != locales.AllAccounts() {
		t.Fatalf("widened: %v, scope %q", rowKeys(a), scopeChosen(tt))
	}
	click(t, tt, locales.ThisFolder())
	if a.query != "budget" || !reflectRows(a, "a1/INBOX/1") || scopeChosen(tt) != locales.ThisFolder() {
		t.Fatalf("this folder: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
	search(a, tt, "budget account:home")
	click(t, tt, locales.AllFolders())
	if a.query != "budget" || !reflectRows(a, "a1/INBOX/1", "a1/Sent/6") || scopeChosen(tt) != locales.AllFolders() {
		t.Fatalf("all folders: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
	search(a, tt, "budget account:work")
	click(t, tt, locales.ThisFolder())
	if a.query != "budget account:work" || !reflectRows(a, "a1/INBOX/1") {
		t.Errorf("an account: of Work stays: %q %v", a.query, rowKeys(a))
	}
	search(a, tt, "budget account:home account:work")
	if len(a.all) != 0 || !tt.HasText(locales.NoAccountMatchesAll()) {
		t.Errorf("two accounts at once: %v: %q", rowKeys(a), tt.Texts())
	}
	click(t, tt, locales.AllFolders())
	if a.query != "budget account:work" || !reflectRows(a, "a1/INBOX/1", "a1/Sent/6") {
		t.Errorf("Home's part taken out: %q %v", a.query, rowKeys(a))
	}
	search(a, tt, "budget account:home")
	click(t, tt, locales.AllAccounts())
	if a.query != "budget account:home" || scopeChosen(tt) != locales.AllAccounts() {
		t.Errorf("every account keeps it: %q, scope %q", a.query, scopeChosen(tt))
	}

	// Its chip takes it out: the scope chosen is the one shown again.
	click(t, tt, locales.ThisFolder())
	search(a, tt, "budget account:home")
	click(t, tt, locales.RemoveFilter(locales.FilterAccountValue("home")))
	if a.query != "budget" || !reflectRows(a, "a1/INBOX/1") || scopeChosen(tt) != locales.ThisFolder() {
		t.Errorf("chip removed: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
}

// An empty search never offers a scope that covers no more than its own:
// not every account when account: keeps only the one on screen, and none
// when it keeps no account. A value in no account's name or address finds
// nothing without reading the cache, and the chip and the list name it.
func TestAccountSearchFindsNothing(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("nothing account:work")
	if scopeChosen(tt) != locales.AllFolders() || tt.HasText(locales.SearchAllAccounts()) ||
		!tt.HasText(locales.SearchedWhere(locales.WhereAccount("Work"))) {
		t.Errorf("Work's folders: scope %q: %q", scopeChosen(tt), tt.Texts())
	}
	click(t, tt, locales.ThisFolder())
	if !tt.HasText(locales.SearchAllFolders()) || tt.HasText(locales.SearchAllAccounts()) {
		t.Errorf("Work's inbox: %q", tt.Texts())
	}
	click(t, tt, locales.AllAccounts())
	if !tt.HasText(locales.SearchedWhere(locales.WhereAccount("Work"))) || tt.HasText(locales.SearchAllAccounts()) {
		t.Errorf("every account, kept to Work: %q", tt.Texts())
	}
	// Without account:, the widest scope is still offered.
	click(t, tt, locales.AllFolders())
	search(a, tt, "nothing")
	if !tt.HasText(locales.SearchAllAccounts()) {
		t.Errorf("no account: %q", tt.Texts())
	}

	reads := fs.pageReads
	search(a, tt, "pergola account:school")
	if len(a.all) != 0 || fs.pageReads != reads || a.err != "" {
		t.Errorf("no such account: %v, %d reads, %q", rowKeys(a), fs.pageReads-reads, a.err)
	}
	for _, text := range []string{
		locales.FilterNoAccount("school"), locales.NoAccountNamed("school"),
		locales.WithFilters(locales.FilterNoAccount("school")), locales.MessageCount(0) + " · " + locales.WhereNoAccount(),
	} {
		if !tt.HasText(text) {
			t.Errorf("no such account: missing %q in %q", text, tt.Texts())
		}
	}
	for _, text := range []string{locales.SearchAllAccounts(), locales.SearchAllFolders(), locales.SearchedWhere(locales.WhereAllAccounts())} {
		if tt.HasText(text) {
			t.Errorf("no such account: %q offered: %q", text, tt.Texts())
		}
	}
	// In conversation view, too.
	a, fs, tt = startTwo(t, true)
	reads = fs.pageReads
	search(a, tt, "account:school")
	if len(a.all) != 0 || len(a.convs) != 0 || fs.pageReads != reads || !tt.HasText(locales.NoAccountNamed("school")) {
		t.Errorf("conversations: %v, %d reads: %q", rowKeys(a), fs.pageReads-reads, tt.Texts())
	}
}

// The filter control's Account menu writes account: with the account's
// name, and its address where two accounts have one name; Clear filters
// takes it out. With one account there is no such menu.
func TestAccountFilterControl(t *testing.T) {
	a, _, tt := startTwo(t, false)
	click(t, tt, "Search mail")
	tt.Type("pergola")
	click(t, tt, locales.SearchFilters())
	if !strings.Contains(locales.FilterSyntaxHint(), "account:") || !tt.HasText(locales.FilterSyntaxHint()) {
		t.Errorf("the hint: %q", locales.FilterSyntaxHint())
	}
	clickRole(t, tt, ui.RoleMenuButton, locales.FilterAccount())
	if err := tt.ChooseMenuItem("Home"); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	if a.query != "pergola account:Home" || !reflectRows(a, "a2/All Mail/5") || scopeChosen(tt) != locales.AllAccounts() {
		t.Fatalf("Home chosen: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
	if filterCount(a.query) != 1 || !tt.HasText(locales.FilterAccountValue("Home")) {
		t.Errorf("Home's chip: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleMenuButton, locales.FilterAccount())
	if err := tt.ChooseMenuItem("Work"); err != nil {
		t.Fatal(err)
	}
	if a.query != "pergola account:Work" || !reflectRows(a, "a1/Sent/3") || scopeChosen(tt) != locales.AllFolders() {
		t.Fatalf("Work chosen: %q %v, scope %q", a.query, rowKeys(a), scopeChosen(tt))
	}
	clickRole(t, tt, ui.RoleMenuButton, locales.FilterAccount())
	if err := tt.ChooseMenuItem(locales.AnyAccount()); err != nil {
		t.Fatal(err)
	}
	if a.query != "pergola" {
		t.Errorf("any account: %q", a.query)
	}
	clickRole(t, tt, ui.RoleMenuButton, locales.FilterAccount())
	if err := tt.ChooseMenuItem("Home"); err != nil {
		t.Fatal(err)
	}
	click(t, tt, locales.ClearFilters())
	if a.query != "pergola" {
		t.Errorf("cleared: %q", a.query)
	}

	// Two accounts of one name: the menu tells them apart by address, and
	// writes the address.
	a.accounts[0].Name = "Home"
	tt.Frame()
	clickRole(t, tt, ui.RoleMenuButton, locales.FilterAccount())
	if err := tt.ChooseMenuItem("Home · " + annHome.Email); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	if a.query != "pergola account:"+annHome.Email || !reflectRows(a, "a2/All Mail/5") {
		t.Errorf("Home by address: %q %v", a.query, rowKeys(a))
	}

	one, _ := sampleApp(t)
	one.query = "review"
	one.search()
	ot := ui.NewTester(one.view, 1220, 800)
	click(t, ot, locales.SearchFilters())
	if _, ok := ot.AccessByRole(ui.RoleMenuButton, locales.FilterAccount()); ok || ot.HasText(locales.AnyAccount()) {
		t.Errorf("one account has an Account menu: %q", ot.Texts())
	}
}

// In the unified inbox account: keeps those accounts' inboxes, the scope
// still the inboxes'.
func TestAccountSearchInTheUnifiedInbox(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, _, tt := startTwo(t, conversations)
		click(t, tt, locales.AllInboxes())
		search(a, tt, "account:home")
		accounts := map[string]bool{}
		for _, m := range a.all {
			accounts[m.Account+" "+m.Folder] = true
		}
		if len(a.all) != 2 || !reflect.DeepEqual(accounts, map[string]bool{"a2 INBOX": true}) || scopeChosen(tt) != locales.ScopeInboxes() {
			t.Errorf("conversations=%v: Home's inbox: %v, scope %q", conversations, rowKeys(a), scopeChosen(tt))
		}
		if !tt.HasText(locales.WhereInboxesOf("Home")) && !tt.HasText(locales.MessageCount(2)+" · "+locales.WhereInboxesOf("Home")) &&
			!tt.HasText(locales.ConversationCount(2)+" · "+locales.WhereInboxesOf("Home")) {
			t.Errorf("conversations=%v: where: %q", conversations, tt.Texts())
		}
		search(a, tt, "account:work dinner")
		if len(a.all) != 0 || !tt.HasText(locales.SearchAllAccounts()) {
			t.Errorf("conversations=%v: Work's inbox has no dinner: %v: %q", conversations, rowKeys(a), tt.Texts())
		}
		click(t, tt, locales.ScopeInboxes())
		if a.query != "account:work dinner" {
			t.Errorf("conversations=%v: the inboxes' scope took account: out: %q", conversations, a.query)
		}
		// No account: no wider scope finds more.
		search(a, tt, "account:school dinner")
		if len(a.all) != 0 || !tt.HasText(locales.NoAccountNamed("school")) || tt.HasText(locales.SearchAllAccounts()) {
			t.Errorf("conversations=%v: no such account: %v: %q", conversations, rowKeys(a), tt.Texts())
		}
	}
}

// A move never targets the folder a message is in already. In a search of
// every folder, Delete on a row of Trash and one of the inbox moves the
// inbox's alone, and the Trash row stays; with only the Trash row chosen
// Delete is off and its key does nothing. In a folder of a special use
// its own action is off, by message and by conversation.
func TestMoveLeavesWhatIsThere(t *testing.T) {
	a, fs, tt := startTwo(t, false)
	key := listKey("a1", "Trash")
	fs.messages[key] = append(fs.messages[key], mail.Summary{Account: "a1", Folder: "Trash", UID: 8, Subject: "Old budget notes",
		Preview: "Superseded.", From: annWork, To: []mail.Address{workMaya}, Date: hoursAgo(40), Seen: true})
	fs.recount("a1", "Trash")
	click(t, tt, "Search mail")
	tt.Type("budget")
	if !reflectRows(a, "a1/INBOX/1", "a1/Sent/6", "a1/Trash/8") {
		t.Fatalf("results: %v", rowKeys(a))
	}
	click(t, tt, "Old budget notes")
	if info, ok := tt.Access(locales.DeleteMessage()); !ok || !info.Disabled {
		t.Errorf("Delete on a row of Trash: found %v, disabled %v", ok, info.Disabled)
	}
	if info, ok := tt.Access(locales.ArchiveMessage()); !ok || info.Disabled {
		t.Errorf("Archive on a row of Trash: found %v, disabled %v", ok, info.Disabled)
	}
	fs.acts = nil
	tt.Key(0, ui.KeyDelete)
	if got := actsLike(fs, "a1", "Move"); len(got) != 0 || !reflectRows(a, "a1/INBOX/1", "a1/Sent/6", "a1/Trash/8") {
		t.Errorf("the Delete key on a row of Trash: %v, rows %v", got, rowKeys(a))
	}
	if err := tt.ClickWith(ui.Cmd, "Quarterly budget review"); err != nil {
		t.Fatal(err)
	}
	if a.sel.Len() != 2 {
		t.Fatalf("chosen %v", chosenKeys(a))
	}
	if info, ok := tt.Access(locales.DeleteMessage()); !ok || info.Disabled {
		t.Errorf("Delete on rows of Trash and the inbox: found %v, disabled %v", ok, info.Disabled)
	}
	click(t, tt, locales.DeleteMessage())
	if got := actsLike(fs, "a1", "Move"); !reflect.DeepEqual(got, []string{"Move INBOX/1->Trash"}) || a.err != "" {
		t.Fatalf("moves %v, %q", got, a.err)
	}
	if keys := rowKeys(a); !slices.Contains(keys, "a1/Trash/8") || slices.Contains(keys, "a1/INBOX/1") {
		t.Errorf("rows after Delete: %v", keys)
	}
	fs.acts = nil
	click(t, tt, locales.Undo())
	if got := actsLike(fs, "a1", "MoveBack"); !reflect.DeepEqual(got, []string{"MoveBack Trash/1001->INBOX"}) {
		t.Errorf("the undo: %v", got)
	}
	if _, ok := fs.find("a1", "Trash", 8); !ok {
		t.Error("the Trash row's message left Trash")
	}

	for _, conversations := range []bool{false, true} {
		a, fs, tt := startTwo(t, conversations)
		fs.messages[key] = append(fs.messages[key], mail.Summary{Account: "a1", Folder: "Trash", UID: 8, Subject: "Old budget notes",
			Preview: "Superseded.", From: annWork, Date: hoursAgo(40), Seen: true})
		fs.recount("a1", "Trash")
		archive, del := locales.ArchiveMessage(), locales.DeleteMessage()
		if conversations {
			archive, del = locales.ArchiveConversations(1), locales.DeleteConversations(1)
		}
		for _, c := range []struct {
			folder, subject, off, on string
			key                      ui.Key
		}{
			{locales.Archive(), "Invoice for March", archive, del, ui.KeyE},
			{locales.Trash(), "Old budget notes", del, archive, ui.KeyDelete},
		} {
			click(t, tt, c.folder)
			click(t, tt, c.subject)
			if info, ok := tt.Access(c.off); !ok || !info.Disabled {
				t.Errorf("conversations=%v: %q in %s: found %v, disabled %v", conversations, c.off, c.folder, ok, info.Disabled)
			}
			if info, ok := tt.Access(c.on); !ok || info.Disabled {
				t.Errorf("conversations=%v: %q in %s: found %v, disabled %v", conversations, c.on, c.folder, ok, info.Disabled)
			}
			fs.acts = nil
			tt.Key(0, c.key)
			if got := actsLike(fs, "a1", "Move"); len(got) != 0 || len(a.all) != 1 {
				t.Errorf("conversations=%v: the key in %s: %v, %d rows", conversations, c.folder, got, len(a.all))
			}
		}
	}
}

// searchTimers arms real timers that fire only when the test says so,
// and tells a timer the app stopped from one it did not.
type searchTimers struct {
	armed []*time.Timer
	fns   []func()
}

func (s *searchTimers) after(d time.Duration, fn func()) *time.Timer {
	t := time.AfterFunc(time.Hour, func() {})
	s.armed = append(s.armed, t)
	s.fns = append(s.fns, fn)
	return t
}

// fire runs the handlers of the timers the app did not stop.
func (s *searchTimers) fire() {
	armed, fns := s.armed, s.fns
	s.armed, s.fns = nil, nil
	for i, t := range armed {
		if !t.Stop() {
			continue
		}
		fns[i]()
	}
}

// A word typed a letter at a time asks the cache once, when the typing
// pauses: each keystroke retires the search the one before it armed.
func TestSearchWaitsForTheTypingToPause(t *testing.T) {
	a, _, tt := startTwo(t, false)
	ti := &searchTimers{}
	a.after = ti.after
	click(t, tt, "Search mail")
	reads := a.pager.reads
	tt.TypeKey(0, ui.KeyP, "p")
	tt.TypeKey(0, ui.KeyI, "i")
	if len(ti.armed) != 2 {
		t.Fatalf("two keystrokes armed %d searches", len(ti.armed))
	}
	if a.pager.reads != reads {
		t.Errorf("the list read before the typing paused: %d reads", a.pager.reads-reads)
	}
	ti.fire()
	if got := a.pager.reads - reads; got != 1 {
		t.Errorf("reads = %d, want one", got)
	}
	if a.query != "pi" {
		t.Errorf("query = %q", a.query)
	}
}

// A search the user asks for takes the place of one the typing armed: a
// filter chip taken off searches once, not again when the pause comes.
func TestASearchRetiresTheTypingTimer(t *testing.T) {
	a, _, tt := startTwo(t, false)
	ti := &searchTimers{}
	a.after = ti.after
	click(t, tt, "Search mail")
	tt.TypeKey(0, ui.KeyP, "p")
	reads := a.pager.reads
	a.search()
	if got := a.pager.reads - reads; got != 1 {
		t.Fatalf("the search read %d times, want once", got)
	}
	ti.fire()
	if got := a.pager.reads - reads; got != 1 {
		t.Errorf("reads = %d: the retired search read again", got)
	}
}

// A list opened while the typing paused is not searched for: the text the
// timer was armed with is no longer the one on screen.
func TestSearchOfAnotherListIsNotSearchedFor(t *testing.T) {
	a, _, tt := startTwo(t, false)
	ti := &searchTimers{}
	a.after = ti.after
	click(t, tt, "Search mail")
	tt.TypeKey(0, ui.KeyP, "p")
	reads := a.pager.reads
	a.query = ""
	ti.fire()
	if a.pager.reads != reads {
		t.Errorf("the timer searched a list the user opened: %d reads", a.pager.reads-reads)
	}
}

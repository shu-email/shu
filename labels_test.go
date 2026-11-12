package main

import (
	"context"
	"fmt"
	"math"
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

// keepsAny are the PERMANENTFLAGS of a folder that keeps any keyword.
var keepsAny = []string{`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, `\Draft`, `\*`}

// labelsService is the two accounts' mail with labels. Work's folders
// keep any keyword: its Projects is on the budget review, in the inbox
// and in Sent, its Finance on the lunch and on the invoice in Archive, and
// Receipts on nothing. Home's keep none, as Outlook's: its Family, on the
// dinner, and Trips, on the flight, are kept on this device only.
func labelsService(conversations bool) *fakeService {
	fs := twoAccountsService(conversations)
	for i := range fs.folders["a1"] {
		fs.folders["a1"][i].PermanentFlags = keepsAny
	}
	fs.keepNoKeywords("a2")
	projects := fs.addLabel("a1", "Projects", mail.LabelBlue)
	finance := fs.addLabel("a1", "Finance", mail.LabelGreen)
	fs.addLabel("a1", "Receipts", mail.LabelOrange)
	family := fs.addLabel("a2", "Family", mail.LabelPink)
	trips := fs.addLabel("a2", "Trips", mail.LabelTeal)
	ctx := context.Background()
	for _, set := range []struct {
		account string
		id      mail.LabelID
		refs    []mail.MessageRef
	}{
		{"a1", projects.ID, []mail.MessageRef{{Folder: "INBOX", UID: 1}, {Folder: "Sent", UID: 6}}},
		{"a1", finance.ID, []mail.MessageRef{{Folder: "INBOX", UID: 2}, {Folder: "Archive", UID: 4}}},
		{"a2", family.ID, []mail.MessageRef{{Folder: "INBOX", UID: 1}}},
		{"a2", trips.ID, []mail.MessageRef{{Folder: "INBOX", UID: 2}}},
	} {
		if err := fs.SetLabel(ctx, set.account, set.refs, set.id, true); err != nil {
			panic(err)
		}
	}
	fs.acts = nil
	return fs
}

// startLabels is a window on labelsService, Work on screen.
func startLabels(t *testing.T, conversations bool) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	fs := labelsService(conversations)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, fs, ui.NewTester(a.view, 1220, 800)
}

// labelNamed is an account's label of a name, as the fake keeps it.
func labelNamed(t *testing.T, fs *fakeService, account, name string) mail.Label {
	t.Helper()
	for _, l := range fs.labels[account] {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("%s has no label %q: %+v", account, name, fs.labels[account])
	return mail.Label{}
}

// labelsOn are the names of the labels a message of the fake has, those
// kept on this device marked with a star.
func labelsOn(fs *fakeService, account, folder string, uid mail.UID) []string {
	m, ok := fs.find(account, folder, uid)
	if !ok {
		return nil
	}
	var out []string
	for _, ml := range m.Labels {
		for _, l := range fs.labels[account] {
			if l.ID == ml.ID {
				name := l.Name
				if ml.Local {
					name += "*"
				}
				out = append(out, name)
			}
		}
	}
	return out
}

// sidebarLabel is what assistive technology has of a label's entry in the
// sidebar, which is named, as a folder's, by what it shows: the label's
// name, then its mark and its unread count.
func sidebarLabel(t *testing.T, tt *ui.Tester, name string) ui.AccessInfo {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Role == ui.RoleTreeItem && (info.Name == name || strings.HasPrefix(info.Name, name+" ")) {
			return info
		}
	}
	t.Fatalf("no entry of the label %q in the sidebar: %q", name, tt.Texts())
	return ui.AccessInfo{}
}

// openLabel opens a label's list from the sidebar.
func openLabel(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	b := sidebarLabel(t, tt, name).Bounds
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
}

// openPicker opens the toolbar's checklist of labels.
func openPicker(t *testing.T, a *app, tt *ui.Tester) {
	t.Helper()
	click(t, tt, locales.ChangeLabels())
	if !a.labels.picker {
		t.Fatalf("the checklist did not open: %q", tt.Texts())
	}
}

// labelBox is what assistive technology has of a label's check box in the
// checklist.
func labelBox(t *testing.T, tt *ui.Tester, name string) ui.AccessInfo {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleCheckBox, name)
	if !ok {
		t.Fatalf("no check box %q: %q", name, tt.Texts())
	}
	return info
}

func TestLabelEntries(t *testing.T) {
	for _, id := range []mail.LabelID{1, 42, math.MaxInt64} {
		if got := labelOf(labelEntry(id)); got != id {
			t.Errorf("labelOf(labelEntry(%d)) = %d", id, got)
		}
	}
	for _, entry := range []string{"", "INBOX", "starred", draftsFolder, allInboxes, "label/3", "\x00label/", "\x00label/x",
		"\x00label/-2", "\x00label/0", "Labels/\x00label/3"} {
		if got := labelOf(entry); got != 0 {
			t.Errorf("labelOf(%q) = %d, want none", entry, got)
		}
	}
}

// The sidebar has a section of the account's labels, under its folders:
// each with its name and unread count, and a mark on those kept on this
// device only, whose reason the pointer and screen readers find; the
// other account's labels show once it is on screen.
func TestLabelsSidebar(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	for _, text := range []string{locales.Labels(), "Projects", "Finance", "Receipts", locales.NewLabel()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if tt.HasText("Family") {
		t.Errorf("Home's labels show with Work on screen: %q", tt.Texts())
	}
	// Opening the inbox read its first message, Projects', so the one
	// unread message with a label is the lunch, which Finance has.
	if got := unreadOf(a, "a1"); !reflect.DeepEqual(got, map[string]int{"Projects": 0, "Finance": 1, "Receipts": 0}) {
		t.Errorf("unread = %v", got)
	}
	if item := sidebarLabel(t, tt, "Finance"); item.Name != "Finance 1" || item.Description != "" {
		t.Errorf("Finance's entry = %q, %q", item.Name, item.Description)
	}
	if tt.HasText(locales.LabelLocal()) {
		t.Error("a label kept on the server is marked as on this device only")
	}

	click(t, tt, "Accounts")
	if err := tt.ChooseMenuItem("Home · " + annHome.Email); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Family", "Trips"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if tt.HasText("Projects") {
		t.Errorf("Work's labels show with Home on screen: %q", tt.Texts())
	}
	family := labelNamed(t, fs, "a2", "Family")
	if !a.findLabel("a2", family.ID).Local {
		t.Error("Family is not kept on this device only")
	}
	// Screen readers hear the mark with the name, and why as its
	// description; the pointer finds why on the mark.
	item := sidebarLabel(t, tt, "Family")
	if item.Name != "Family "+locales.LabelLocal() || item.Description != locales.LabelLocalDetail("Family") {
		t.Errorf("the entry of a label kept here = %q, %q", item.Name, item.Description)
	}
	b, ok := tt.Find(locales.LabelLocal())
	if !ok {
		t.Fatalf("no mark of a label kept on this device: %q", tt.Texts())
	}
	tt.Move(b.X+b.W/2, b.Y+b.H/2)
	tt.Advance(time.Second)
	if !tt.HasText(locales.LabelLocalDetail("Family")) {
		t.Errorf("no tooltip on the mark: %q", tt.Texts())
	}
}

// unreadOf are the unread counts of an account's labels as the window
// shows them.
func unreadOf(a *app, account string) map[string]int {
	out := map[string]int{}
	for _, l := range a.labels.of[account] {
		out[l.Name] = l.Unread
	}
	return out
}

// rowSubjects are the subjects of the list's rows.
func rowSubjects(a *app) []string {
	out := make([]string, len(a.all))
	for i, m := range a.all {
		out[i] = m.Subject
	}
	return out
}

// A label's entry lists its mail, as a folder's does, from every folder
// but Junk, Trash and Drafts: by message and by conversation, with its
// unread count as the header's badge, its folder on each row, an empty
// state, and actions that act on its rows.
func TestLabelList(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, fs, tt := startLabels(t, conversations)
		finance := labelNamed(t, fs, "a1", "Finance")
		openLabel(t, tt, "Finance")
		if a.folder != labelEntry(finance.ID) {
			t.Fatalf("conversations=%v: folder %q", conversations, a.folder)
		}
		if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Lunch on Friday?", "Invoice for March"}) {
			t.Fatalf("conversations=%v: rows %v", conversations, got)
		}
		if got := a.listScope(); !reflect.DeepEqual(got, mail.LabelScope("a1", finance.ID)) {
			t.Errorf("conversations=%v: scope %+v", conversations, got)
		}
		// The lunch opened with the list: none of it is unread now.
		if !tt.HasText("Finance") || a.shownLabel() == nil || a.shownLabel().Unread != 0 {
			t.Errorf("conversations=%v: header: %q", conversations, tt.Texts())
		}
		if place, folder := a.rowPlace(a.all[1]); place != "" || folder != locales.Archive() {
			t.Errorf("conversations=%v: the invoice's place = %q %q", conversations, place, folder)
		}
		// Deleting the invoice takes it out of the label's list, which
		// leaves Trash out.
		click(t, tt, "Invoice for March")
		if conversations {
			click(t, tt, locales.DeleteConversations(1))
		} else {
			click(t, tt, locales.DeleteMessage())
		}
		if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Lunch on Friday?"}) {
			t.Errorf("conversations=%v: after deleting, rows %v", conversations, got)
		}
		if got := labelsOn(fs, "a1", "Trash", 1004); !reflect.DeepEqual(got, []string{"Finance"}) {
			t.Errorf("conversations=%v: the invoice in Trash has %v", conversations, got)
		}
		// Receipts has nothing yet.
		openLabel(t, tt, "Receipts")
		if len(a.all) != 0 || !tt.HasText(locales.LabelEmpty()) {
			t.Errorf("conversations=%v: Receipts' list: %q", conversations, tt.Texts())
		}
	}
}

// A label's list reads a page at a time, as a folder's does.
func TestLabelListPages(t *testing.T) {
	msgs := pagedMail(130, nil)
	fs := pagedService(msgs, true)
	for i := range fs.folders["a1"] {
		fs.folders["a1"][i].PermanentFlags = keepsAny
	}
	l := fs.addLabel("a1", "Read later", mail.LabelPurple)
	for i := 1; i <= 125; i++ {
		fs.putLabel("a1", "INBOX", l.ID, mail.UID(i))
	}
	a, tt := startPaged(t, fs)
	openLabel(t, tt, "Read later")
	if len(a.all) != listPage || !a.pager.more {
		t.Fatalf("first page: %d rows, more %v", len(a.all), a.pager.more)
	}
	scrollToLast(t, a, tt)
	if len(a.all) != 125 {
		t.Fatalf("rows = %d, want 125", len(a.all))
	}
	if a.all[0].Subject != "Message 125" || a.all[124].Subject != "Message 1" {
		t.Errorf("rows from %q to %q", a.all[0].Subject, a.all[124].Subject)
	}
	if !tt.HasText(locales.NoMoreHere()) || !tt.HasText(locales.OlderInFolderHint()) {
		t.Errorf("the end of the label's list: %q", tt.Texts())
	}
}

// The checklist puts a label on the message chosen and takes it off, the
// row and the reading pane showing it at once, and the engine told with
// the message's place.
func TestLabelOneMessage(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	click(t, tt, "Lunch on Friday?")
	openPicker(t, a, tt)
	if box := labelBox(t, tt, "Finance"); !box.Checked || box.Mixed {
		t.Errorf("Finance on the lunch = %+v", box)
	}
	if box := labelBox(t, tt, "Projects"); box.Checked || box.Mixed {
		t.Errorf("Projects on the lunch = %+v", box)
	}
	clickRole(t, tt, ui.RoleCheckBox, "Projects")
	projects := labelNamed(t, fs, "a1", "Projects")
	if got := actsLike(fs, "a1", "SetLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("SetLabel INBOX/2 %d true", projects.ID)}) {
		t.Fatalf("calls = %v", got)
	}
	if got := labelsOn(fs, "a1", "INBOX", 2); !reflect.DeepEqual(got, []string{"Projects", "Finance"}) {
		t.Errorf("the lunch has %v", got)
	}
	if box := labelBox(t, tt, "Projects"); !box.Checked {
		t.Errorf("Projects after a click = %+v", box)
	}
	if !a.labels.picker {
		t.Error("the checklist closed after a click")
	}
	// The row and the reading pane show both, the pane with a button to
	// take each off.
	row := slices.IndexFunc(a.all, func(m mail.Summary) bool { return m.Subject == "Lunch on Friday?" })
	if got := a.labelNames("a1", a.rowLabelsOf(row)); got != "Projects, Finance" {
		t.Errorf("the row's labels = %q", got)
	}
	if !tt.HasText(locales.RemoveLabel("Projects")) && !hasLabel(tt, locales.RemoveLabel("Projects")) {
		t.Errorf("no way to take Projects off in the pane: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if a.labels.picker {
		t.Fatal("Escape left the checklist open")
	}
	click(t, tt, locales.RemoveLabel("Projects"))
	if got := labelsOn(fs, "a1", "INBOX", 2); !reflect.DeepEqual(got, []string{"Finance"}) {
		t.Errorf("after taking Projects off, the lunch has %v", got)
	}
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 2 || got[1] != fmt.Sprintf("SetLabel INBOX/2 %d false", projects.ID) {
		t.Errorf("calls = %v", got)
	}
}

// hasLabel reports whether an element is named s for assistive
// technology.
func hasLabel(tt *ui.Tester, s string) bool {
	_, ok := tt.Access(s)
	return ok
}

// A label on some of the messages chosen is mixed in the checklist: a
// click puts it on all of them, another takes it off all.
func TestLabelMixedSelection(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	click(t, tt, "Quarterly budget review")
	if err := tt.ClickWith(ui.Cmd, "Lunch on Friday?"); err != nil {
		t.Fatal(err)
	}
	openPicker(t, a, tt)
	for name, want := range map[string][2]bool{"Projects": {false, true}, "Finance": {false, true}, "Receipts": {false, false}} {
		if box := labelBox(t, tt, name); box.Checked != want[0] || box.Mixed != want[1] {
			t.Errorf("%s = checked %v mixed %v, want %v", name, box.Checked, box.Mixed, want)
		}
	}
	clickRole(t, tt, ui.RoleCheckBox, "Projects")
	projects := labelNamed(t, fs, "a1", "Projects")
	want := fmt.Sprintf("SetLabel INBOX/1,INBOX/2 %d true", projects.ID)
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %v, want %q", got, want)
	}
	if box := labelBox(t, tt, "Projects"); !box.Checked || box.Mixed {
		t.Errorf("Projects on both = %+v", box)
	}
	clickRole(t, tt, ui.RoleCheckBox, "Projects")
	for _, uid := range []mail.UID{1, 2} {
		if slices.Contains(labelsOn(fs, "a1", "INBOX", uid), "Projects") {
			t.Errorf("INBOX/%d keeps Projects", uid)
		}
	}
	if box := labelBox(t, tt, "Projects"); box.Checked || box.Mixed {
		t.Errorf("Projects after taking it off = %+v", box)
	}
}

// A conversation's row has the labels of its messages in the list; a
// label put on it goes on exactly those messages, as the conversation's
// other actions act on them, and not on its messages elsewhere.
func TestLabelConversations(t *testing.T) {
	fs := convService()
	for i := range fs.folders["a1"] {
		fs.folders["a1"][i].PermanentFlags = keepsAny
	}
	urgent := fs.addLabel("a1", "Urgent", mail.LabelRed)
	fs.putLabel("a1", "Sent", urgent.ID, 2)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	if a.conv == nil || a.convs[0].Thread != 7 {
		t.Fatalf("open = %+v", a.conv)
	}
	// Ann's answer in Sent has Urgent, which the inbox's row leaves out.
	if len(a.convs[0].Labels) != 0 {
		t.Errorf("the offsite's row has %v", a.convs[0].Labels)
	}
	openPicker(t, a, tt)
	if box := labelBox(t, tt, "Urgent"); box.Checked || box.Mixed {
		t.Errorf("Urgent on the offsite = %+v", box)
	}
	clickRole(t, tt, ui.RoleCheckBox, "Urgent")
	want := fmt.Sprintf("SetLabel INBOX/1,INBOX/3 %d true", urgent.ID)
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %v, want %q", got, want)
	}
	for _, place := range []struct {
		folder string
		uid    mail.UID
		want   bool
	}{{"INBOX", 1, true}, {"INBOX", 3, true}, {"Archive", 4, false}, {"Trash", 5, false}, {"Drafts", 6, false}} {
		if got := slices.Contains(labelsOn(fs, "a1", place.folder, place.uid), "Urgent"); got != place.want {
			t.Errorf("%s/%d has Urgent: %v", place.folder, place.uid, got)
		}
	}
	if got := a.convs[0].Labels; !reflect.DeepEqual(got, []mail.LabelID{urgent.ID}) {
		t.Errorf("the row's labels = %v", got)
	}
	if !hasLabel(tt, locales.ConversationLabels()) {
		t.Errorf("the pane does not show the conversation's labels: %q", tt.Texts())
	}
	// A message's own menu puts a label on it alone.
	tt.Key(0, ui.KeyEscape)
	click(t, tt, locales.ForMessage(locales.MoreActions(), 3, 3))
	if err := tt.ChooseMenuItem(locales.Labels(), "Urgent"); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	want = fmt.Sprintf("SetLabel INBOX/3 %d false", urgent.ID)
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 2 || got[1] != want {
		t.Fatalf("calls = %v, want %q last", got, want)
	}
	if got := a.convs[0].Labels; !reflect.DeepEqual(got, []mail.LabelID{urgent.ID}) {
		t.Errorf("the row's labels after one message lost it = %v", got)
	}
}

// focusName types into the field a caption names, from its start.
func focusName(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	click(t, tt, locales.LabelNameCaption())
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(text)
}

// A label is made in the dialog the sidebar opens, of a name and a color
// of the palette, and shows in the sidebar. A name the account has, in
// any case, is said in the dialog, which stays open; an empty one is not
// saved.
func TestCreateLabel(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	click(t, tt, locales.NewLabel())
	if !a.labels.form.open || a.labels.form.account != "a1" {
		t.Fatalf("the dialog = %+v", a.labels.form)
	}
	if create, ok := tt.AccessByRole(ui.RoleButton, locales.CreateLabel()); !ok || !create.Disabled {
		t.Errorf("Create with no name = %+v", create)
	}
	focusName(t, tt, "projects")
	click(t, tt, locales.CreateLabel())
	if !tt.HasText(locales.LabelExists("projects")) || !a.labels.form.open {
		t.Fatalf("a name taken: %q", tt.Texts())
	}
	if len(fs.labels["a1"]) != 3 {
		t.Fatalf("labels = %+v", fs.labels["a1"])
	}
	focusName(t, tt, "  Clients  ")
	click(t, tt, locales.ColorTeal())
	if !tt.HasText(locales.LabelPreview()) {
		t.Errorf("no preview: %q", tt.Texts())
	}
	click(t, tt, locales.CreateLabel())
	if a.labels.form.open {
		t.Fatalf("the dialog stays: %q", tt.Texts())
	}
	l := labelNamed(t, fs, "a1", "Clients")
	if l.Color != mail.LabelTeal || l.Keyword != "Clients" {
		t.Errorf("made %+v", l)
	}
	sidebarLabel(t, tt, "Clients")
	// Cancel makes none.
	click(t, tt, locales.NewLabel())
	focusName(t, tt, "Later")
	click(t, tt, locales.CancelLabelForm())
	if a.labels.form.open || len(fs.labels["a1"]) != 4 {
		t.Errorf("after Cancel: open %v, labels %+v", a.labels.form.open, fs.labels["a1"])
	}
}

// The first acceptance criterion, as the user does it: two labels made in
// the dialog, both put on one message from the checklist, and the message
// under each label's list, in either view.
func TestTwoLabelsOnOneMessage(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		fs := twoAccountsService(conversations)
		for i := range fs.folders["a1"] {
			fs.folders["a1"][i].PermanentFlags = keepsAny
		}
		a := newTestApp(fs)
		a.setLocale = func(string) {}
		a.start()
		tt := ui.NewTester(a.view, 1220, 800)
		for _, name := range []string{"Budget", "Follow up"} {
			click(t, tt, locales.NewLabel())
			focusName(t, tt, name)
			click(t, tt, locales.CreateLabel())
		}
		click(t, tt, "Lunch on Friday?")
		openPicker(t, a, tt)
		clickRole(t, tt, ui.RoleCheckBox, "Budget")
		clickRole(t, tt, ui.RoleCheckBox, "Follow up")
		if got := labelsOn(fs, "a1", "INBOX", 2); !reflect.DeepEqual(got, []string{"Budget", "Follow up"}) {
			t.Fatalf("conversations=%v: the lunch has %v", conversations, got)
		}
		tt.Key(0, ui.KeyEscape)
		for _, name := range []string{"Budget", "Follow up"} {
			openLabel(t, tt, name)
			if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Lunch on Friday?"}) {
				t.Errorf("conversations=%v: %s lists %v", conversations, name, got)
			}
		}
	}
}

// A label's menu in the sidebar renames and recolors it, as the menu of
// its list's header does, which the keyboard reaches: its name and color
// are this device's, its keyword stays, and the rows say the new name.
func TestEditLabel(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	before := labelNamed(t, fs, "a1", "Finance")
	if err := tt.RightClick("Finance"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.EditLabelEllipsis()); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
	f := a.labels.form
	if !f.open || f.editing != before.ID || f.name != "Finance" || f.color != mail.LabelGreen {
		t.Fatalf("the dialog = %+v", f)
	}
	if !tt.HasText(locales.LabelRenameNote()) {
		t.Errorf("no word that other mail apps keep the name: %q", tt.Texts())
	}
	focusName(t, tt, "Money")
	click(t, tt, locales.ColorBlue())
	click(t, tt, locales.Save())
	after := labelNamed(t, fs, "a1", "Money")
	if after.ID != before.ID || after.Color != mail.LabelBlue || after.Keyword != before.Keyword {
		t.Errorf("edited %+v, was %+v", after, before)
	}
	if a.labels.form.open || tt.HasText("Finance") {
		t.Errorf("after saving: %q", tt.Texts())
	}
	row := slices.IndexFunc(a.all, func(m mail.Summary) bool { return m.Subject == "Lunch on Friday?" })
	if got := a.labelNames("a1", a.rowLabelsOf(row)); got != "Money" {
		t.Errorf("the lunch's labels = %q", got)
	}
	openLabel(t, tt, "Money")
	click(t, tt, locales.LabelActions("Money"))
	if err := tt.ChooseMenuItem(locales.EditLabelEllipsis()); err != nil {
		t.Fatalf("header menu %v: %v", tt.Menu(), err)
	}
	if !a.labels.form.open || a.labels.form.editing != before.ID {
		t.Errorf("the header's menu did not edit the label: %+v", a.labels.form)
	}
}

// strip is a message without its labels, which deleting a label alone
// may change.
func strip(m mail.Summary) mail.Summary {
	m.Labels = nil
	return m
}

// mailOf is every message of the fake, without its labels, by account
// and folder.
func mailOf(fs *fakeService) map[string][]mail.Summary {
	out := map[string][]mail.Summary{}
	for key, list := range fs.messages {
		for _, m := range list {
			out[key] = append(out[key], strip(m))
		}
	}
	return out
}

// askDelete asks to delete a label from its list's header.
func askDelete(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	openLabel(t, tt, name)
	click(t, tt, locales.LabelActions(name))
	if err := tt.ChooseMenuItem(locales.DeleteLabelEllipsis()); err != nil {
		t.Fatalf("menu %v: %v", tt.Menu(), err)
	}
}

// Deleting a label asks first, saying its messages keep everything but
// the label; Cancel keeps it. Confirmed, it comes off every message, all
// of which are still there as they were, and its list gives way to the
// inbox.
func TestDeleteLabelKeepsMail(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	before := mailOf(fs)
	projects := labelNamed(t, fs, "a1", "Projects")
	askDelete(t, tt, "Projects")
	for _, text := range []string{locales.DeleteLabelTitle("Projects"), locales.DeleteLabelMessage(), locales.DeleteLabelButton()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in the question: %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Cancel())
	if got := actsLike(fs, "a1", "DeleteLabel"); len(got) != 0 || a.labels.del.asking {
		t.Fatalf("Cancel deleted: %v", got)
	}
	askDelete(t, tt, "Projects")
	click(t, tt, locales.DeleteLabelButton())
	if got := actsLike(fs, "a1", "DeleteLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("DeleteLabel %d", projects.ID)}) {
		t.Fatalf("calls = %v", got)
	}
	if after := mailOf(fs); !reflect.DeepEqual(after, before) {
		t.Errorf("deleting a label changed mail:\n got %+v\nwant %+v", after, before)
	}
	for _, place := range []struct {
		folder string
		uid    mail.UID
	}{{"INBOX", 1}, {"Sent", 6}} {
		if got := labelsOn(fs, "a1", place.folder, place.uid); slices.Contains(got, "Projects") {
			t.Errorf("%s/%d keeps it: %v", place.folder, place.uid, got)
		}
	}
	if a.folder != "INBOX" || a.findLabel("a1", projects.ID) != nil || tt.HasText("Projects") {
		t.Errorf("after deleting: folder %q, %q", a.folder, tt.Texts())
	}
	if got := rowSubjects(a); len(got) != 2 {
		t.Errorf("the inbox shows %v", got)
	}
}

// A deletion that took the label off some messages only says so and
// offers to finish, which deleting again does; a deletion that failed
// keeps the label and says why. No message is changed either way but for
// the label.
func TestDeleteLabelPartly(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	before := mailOf(fs)
	fs.deleteLabelErr = fmt.Errorf("%w: %w", mail.ErrLabelPartlyRemoved, errFake)
	askDelete(t, tt, "Projects")
	click(t, tt, locales.DeleteLabelButton())
	for _, text := range []string{locales.LabelPartlyTitle("Projects"), locales.LabelPartlyMessage(), locales.FinishDeleting()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q: %q", text, tt.Texts())
		}
	}
	if got := labelsOn(fs, "a1", "Sent", 6); !reflect.DeepEqual(got, []string{"Projects"}) {
		t.Errorf("Sent/6 has %v", got)
	}
	if a.findLabel("a1", labelNamed(t, fs, "a1", "Projects").ID) == nil {
		t.Error("the label went from the sidebar")
	}
	fs.deleteLabelErr = nil
	click(t, tt, locales.FinishDeleting())
	if tt.HasText("Projects") || len(actsLike(fs, "a1", "DeleteLabel")) != 2 {
		t.Errorf("after finishing: %v, %q", actsLike(fs, "a1", "DeleteLabel"), tt.Texts())
	}
	if after := mailOf(fs); !reflect.DeepEqual(after, before) {
		t.Errorf("deleting a label changed mail:\n got %+v\nwant %+v", after, before)
	}

	fs.deleteLabelErr = errFake
	askDelete(t, tt, "Finance")
	click(t, tt, locales.DeleteLabelButton())
	if a.err != locales.LabelDeleteFailed("Finance", errFake.Error()) || a.labels.del.partly {
		t.Errorf("a failure says %q, partly %v", a.err, a.labels.del.partly)
	}
	if got := labelsOn(fs, "a1", "INBOX", 2); !reflect.DeepEqual(got, []string{"Finance"}) {
		t.Errorf("a failed deletion took the label off: %v", got)
	}
	sidebarLabel(t, tt, "Finance")
}

// Home's server keeps no labels, as Outlook's: its labels say they are kept
// on this device only in the header of their list, on the rows' chips and
// in the question before deleting one, and a label put on its mail says
// so as it goes on, which no server hears of.
func TestLabelsKeptOnDevice(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	a.switchAccount("a2")
	tt.Frame()
	openLabel(t, tt, "Family")
	if !tt.HasText(locales.MessageCount(1) + " · " + locales.LabelLocal()) {
		t.Errorf("the header does not say it: %q", tt.Texts())
	}
	if !hasLabel(tt, locales.LabelOnDevice("Family")) {
		t.Errorf("no chip says it: %q", tt.Texts())
	}
	click(t, tt, locales.LabelActions("Family"))
	if err := tt.ChooseMenuItem(locales.DeleteLabelEllipsis()); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(locales.DeleteLabelMessageLocal()) || tt.HasText(locales.DeleteLabelMessage()) {
		t.Errorf("the question about a label kept here: %q", tt.Texts())
	}
	click(t, tt, locales.Cancel())

	click(t, tt, locales.NewLabel())
	focusName(t, tt, "Receipts")
	click(t, tt, locales.CreateLabel())
	receipts := labelNamed(t, fs, "a2", "Receipts")
	if !a.findLabel("a2", receipts.ID).Local {
		t.Errorf("a label of Home is synced: %+v", a.findLabel("a2", receipts.ID))
	}
	click(t, tt, locales.Inbox())
	click(t, tt, "Your flight confirmation")
	openPicker(t, a, tt)
	if box := labelBox(t, tt, "Receipts"); box.Description != locales.LabelLocal() {
		t.Errorf("the checklist does not mark it: %+v", box)
	}
	if !tt.HasText(locales.LabelsKeptHereHint()) {
		t.Errorf("the checklist does not say why: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleCheckBox, "Receipts")
	if a.labels.note != locales.LabelKeptHereNote("Receipts", 1) || !tt.HasText(a.labels.note) {
		t.Errorf("note = %q: %q", a.labels.note, tt.Texts())
	}
	if got := labelsOn(fs, "a2", "INBOX", 2); !reflect.DeepEqual(got, []string{"Trips*", "Receipts*"}) {
		t.Errorf("the flight has %v", got)
	}
	click(t, tt, locales.DismissLabelNote())
	if a.labels.note != "" {
		t.Error("the note stays")
	}
}

// label: finds the messages with a label, by its name in any case: the
// search's chip names the label as it is written, and each row says its
// labels found it. The filters' Label menu writes label: and takes it
// out, quoting a name with a space.
func TestLabelSearch(t *testing.T) {
	for _, conversations := range []bool{false, true} {
		a, fs, tt := startLabels(t, conversations)
		click(t, tt, "Search mail")
		tt.Type("label:finance")
		if got := rowSubjects(a); !reflect.DeepEqual(got, []string{"Lunch on Friday?", "Invoice for March"}) {
			t.Fatalf("conversations=%v: found %v", conversations, got)
		}
		if !tt.HasText(locales.FilterLabelValue("Finance")) {
			t.Errorf("conversations=%v: no chip: %q", conversations, tt.Texts())
		}
		reason := locales.MatchedFields(locales.FieldLabel())
		if conversations {
			reason = locales.MatchedInMessages(1, locales.FieldLabel())
		}
		if got := a.rowReason(0); got != reason {
			t.Errorf("conversations=%v: reason %q, want %q", conversations, got, reason)
		}
		if conversations {
			continue
		}
		fs.addLabel("a1", "To Do", mail.LabelRed)
		fs.putLabel("a1", "Sent", labelNamed(t, fs, "a1", "To Do").ID, 3)
		a.loadLabels("a1")
		search(a, tt, "offsite")
		click(t, tt, locales.SearchFilters())
		clickRole(t, tt, ui.RoleMenuButton, locales.FilterLabel())
		if err := tt.ChooseMenuItem("To Do"); err != nil {
			t.Fatalf("menu %v: %v", tt.Menu(), err)
		}
		if a.query != `offsite label:"To Do"` || !reflect.DeepEqual(rowSubjects(a), []string{"Re: Offsite venue"}) {
			t.Errorf("query %q found %v", a.query, rowSubjects(a))
		}
		clickRole(t, tt, ui.RoleMenuButton, locales.FilterLabel())
		if err := tt.ChooseMenuItem(locales.AnyLabel()); err != nil {
			t.Fatal(err)
		}
		if a.query != "offsite" {
			t.Errorf("query %q after Any label", a.query)
		}
	}
}

// In the unified inbox a row of one account takes that account's labels,
// and rows of two accounts take none together, which the checklist and
// the rows' menu say.
func TestLabelsSeveralAccounts(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	click(t, tt, locales.AllInboxes())
	click(t, tt, "Dinner on Saturday")
	openPicker(t, a, tt)
	if _, ok := tt.AccessByRole(ui.RoleCheckBox, "Projects"); ok {
		t.Error("Work's labels are offered for Home's mail")
	}
	clickRole(t, tt, ui.RoleCheckBox, "Trips")
	trips := labelNamed(t, fs, "a2", "Trips")
	if got := actsLike(fs, "a2", "SetLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("SetLabel INBOX/1 %d true", trips.ID)}) {
		t.Errorf("Home's calls = %v", got)
	}
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 0 {
		t.Errorf("Work's calls = %v", got)
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.ClickWith(ui.Cmd, "Quarterly budget review"); err != nil {
		t.Fatal(err)
	}
	openPicker(t, a, tt)
	if !tt.HasText(locales.LabelsSeveralAccounts()) {
		t.Errorf("the checklist does not say why: %q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleCheckBox, ""); ok {
		t.Error("the checklist offers labels for two accounts' mail")
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.RightClick("Dinner on Saturday"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Labels(), locales.LabelsSeveralAccounts()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("the menu's item: %v, menu %v", err, tt.Menu())
	}
}

// The list's menu puts a label on the rows chosen or takes it off, as the
// checklist does: a right-click on a row not chosen chooses it first, and
// a label some of the rows have goes on all of them.
func TestLabelRowMenu(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	projects, finance := labelNamed(t, fs, "a1", "Projects"), labelNamed(t, fs, "a1", "Finance")
	if err := tt.RightClick("Lunch on Friday?"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); !reflect.DeepEqual(got, []string{locales.Labels(), "-", locales.MakeRuleEllipsis()}) {
		t.Fatalf("menu = %v", got)
	}
	if m, _ := a.current(); m == nil || m.Subject != "Lunch on Friday?" {
		t.Fatalf("the right-click chose %+v", m)
	}
	if err := tt.ChooseMenuItem(locales.Labels(), "Projects"); err != nil {
		t.Fatal(err)
	}
	if got := actsLike(fs, "a1", "SetLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("SetLabel INBOX/2 %d true", projects.ID)}) {
		t.Fatalf("calls = %v", got)
	}
	if err := tt.ClickWith(ui.Cmd, "Quarterly budget review"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("Quarterly budget review"); err != nil {
		t.Fatal(err)
	}
	if a.sel.Len() != 2 {
		t.Fatalf("a right-click on a row chosen chose %d", a.sel.Len())
	}
	if err := tt.ChooseMenuItem(locales.Labels(), "Finance"); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("SetLabel INBOX/1,INBOX/2 %d true", finance.ID)
	if got := actsLike(fs, "a1", "SetLabel"); len(got) != 2 || got[1] != want {
		t.Errorf("calls = %v, want %q last", got, want)
	}
	// New label… makes a label for the rows chosen and puts it on them.
	if err := tt.RightClick("Quarterly budget review"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.Labels(), locales.NewLabelEllipsis()); err != nil {
		t.Fatal(err)
	}
	focusName(t, tt, "Q3")
	click(t, tt, locales.CreateLabel())
	q3 := labelNamed(t, fs, "a1", "Q3")
	for _, uid := range []mail.UID{1, 2} {
		if !slices.Contains(labelsOn(fs, "a1", "INBOX", uid), "Q3") {
			t.Errorf("INBOX/%d lacks the new label: %v", uid, labelsOn(fs, "a1", "INBOX", uid))
		}
	}
	if got := actsLike(fs, "a1", "SetLabel"); got[len(got)-1] != fmt.Sprintf("SetLabel INBOX/1,INBOX/2 %d true", q3.ID) {
		t.Errorf("calls = %v", got)
	}
}

// The checklist's New label… makes a label and puts it on the rows that
// were chosen as it opened.
func TestNewLabelFromChecklist(t *testing.T) {
	a, fs, tt := startLabels(t, true)
	openPicker(t, a, tt)
	click(t, tt, locales.NewLabelEllipsis())
	if a.labels.picker || !a.labels.form.open || a.labels.form.apply == nil {
		t.Fatalf("picker %v, dialog %+v", a.labels.picker, a.labels.form)
	}
	focusName(t, tt, "Urgent")
	click(t, tt, locales.CreateLabel())
	urgent := labelNamed(t, fs, "a1", "Urgent")
	if got := actsLike(fs, "a1", "SetLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("SetLabel INBOX/1 %d true", urgent.ID)}) {
		t.Errorf("calls = %v", got)
	}
	if !slices.Contains(a.convs[0].Labels, urgent.ID) {
		t.Errorf("the row's labels = %v", a.convs[0].Labels)
	}
}

// L opens the checklist with the keyboard on its first label, whose box
// Space checks or unchecks, and Escape closes it; typed in the search
// field, L is a letter.
func TestLabelKeyboard(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	tt.Key(0, ui.KeyL)
	if !a.labels.picker || !tt.Focused("Projects") {
		t.Fatalf("picker %v, focus on Projects %v", a.labels.picker, tt.Focused("Projects"))
	}
	tt.Key(0, ui.KeySpace)
	projects := labelNamed(t, fs, "a1", "Projects")
	if got := actsLike(fs, "a1", "SetLabel"); !reflect.DeepEqual(got, []string{fmt.Sprintf("SetLabel INBOX/1 %d false", projects.ID)}) {
		t.Errorf("calls = %v", got)
	}
	tt.Key(0, ui.KeyEscape)
	if a.labels.picker {
		t.Error("Escape left it open")
	}
	click(t, tt, "Search mail")
	tt.Type("l")
	if a.labels.picker || a.query != "l" {
		t.Errorf("typing L: picker %v, query %q", a.labels.picker, a.query)
	}
}

// A label the server refuses comes off the rows again, and the window
// says why.
func TestLabelRefused(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	fs.setLabelErr = errFake
	click(t, tt, "Lunch on Friday?")
	openPicker(t, a, tt)
	clickRole(t, tt, ui.RoleCheckBox, "Projects")
	if a.err != locales.LabelChangeFailed(errFake.Error()) {
		t.Errorf("err = %q", a.err)
	}
	row := slices.IndexFunc(a.all, func(m mail.Summary) bool { return m.Subject == "Lunch on Friday?" })
	if got := a.labelNames("a1", a.rowLabelsOf(row)); got != "Finance" {
		t.Errorf("the row's labels = %q", got)
	}
	if box := labelBox(t, tt, "Projects"); box.Checked {
		t.Error("Projects stays checked")
	}
}

// A label's unread count follows the messages read and marked unread in
// any list, as the folders' counts do.
func TestLabelUnreadFollowsReading(t *testing.T) {
	a, _, tt := startLabels(t, false)
	if got := unreadOf(a, "a1")["Finance"]; got != 1 {
		t.Fatalf("Finance unread = %d", got)
	}
	click(t, tt, "Lunch on Friday?")
	if got := unreadOf(a, "a1")["Finance"]; got != 0 || sidebarLabel(t, tt, "Finance").Name != "Finance" {
		t.Errorf("after reading the lunch: %d, %q", got, sidebarLabel(t, tt, "Finance").Name)
	}
	tt.Key(0, ui.KeyU)
	if got := unreadOf(a, "a1")["Finance"]; got != 1 || sidebarLabel(t, tt, "Finance").Name != "Finance 1" {
		t.Errorf("after marking it unread: %d", got)
	}
}

// The toolbar's button and the checklist hold at the window's minimum
// size, light and dark, beside the AI's button, as do the sidebar's labels.
func TestLabelsAtMinimumSize(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, fs, _, _ := aiMessageApp(t, true)
		for i := range fs.folders["a1"] {
			fs.folders["a1"][i].PermanentFlags = keepsAny
		}
		fs.addLabel("a1", "Projects", mail.LabelBlue)
		a.loadLabels("a1")
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		for _, name := range []string{locales.Summarize(), locales.ChangeLabels()} {
			if b, ok := tt.Find(name); !ok || b.W == 0 || b.X+b.W > 1000 {
				t.Errorf("dark=%v: %q at %v", dark, name, b)
			}
		}
		openPicker(t, a, tt)
		box := labelBox(t, tt, "Projects").Bounds
		if box.X < 0 || box.Y < 0 || box.X+box.W > 1000 || box.Y+box.H > 640 {
			t.Errorf("dark=%v: the checklist's box at %v", dark, box)
		}
		clickRole(t, tt, ui.RoleCheckBox, "Projects")
		if got := actsLike(fs, "a1", "SetLabel"); len(got) != 1 {
			t.Errorf("dark=%v: calls = %v", dark, got)
		}
		if b := sidebarLabel(t, tt, "Projects").Bounds; b.Y+b.H > 640 {
			t.Errorf("dark=%v: the sidebar's label at %v", dark, b)
		}
	}
}

// luminance is a color's relative luminance, as WCAG reckons it.
func luminance(c ui.Color) float64 {
	channel := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

// contrast is the contrast ratio of two opaque colors.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// Each color of the palette has its shades in both appearances, and a
// label's name reads on its chip over the window's background as normal
// text should (WCAG's 4.5:1), its dot apart from the background.
func TestLabelInksReadable(t *testing.T) {
	for _, theme := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		seen := map[ui.Color]bool{}
		for _, color := range mail.LabelColors {
			if _, ok := labelInks[color]; !ok {
				t.Errorf("%s has no shades", color)
				continue
			}
			s := inkOf(color, theme.Dark)
			if seen[s.dot] {
				t.Errorf("dark=%v: %s's dot is another color's", theme.Dark, color)
			}
			seen[s.dot] = true
			chip := chipTint(s).Over(theme.Background)
			if r := contrast(s.ink, chip); r < 4.5 {
				t.Errorf("dark=%v: %s's name on its chip: %.2f:1", theme.Dark, color, r)
			}
			if r := contrast(s.dot, theme.Background); r < 1.5 {
				t.Errorf("dark=%v: %s's dot on the background: %.2f:1", theme.Dark, color, r)
			}
		}
	}
	if colorName(mail.LabelTeal) != locales.ColorTeal() || inkOf("plum", false) != noInk[0] {
		t.Error("a color the palette lacks")
	}
}

// A row shows its labels at the end of its subject's line, at most two
// chips then how many more, which stay in the row; assistive technology
// reads them all with the row. A conversation's row has those of its
// messages in the list.
func TestLabelChipsOnRows(t *testing.T) {
	fs := labelsService(false)
	fs.putLabel("a1", "INBOX", labelNamed(t, fs, "a1", "Finance").ID, 1)
	fs.putLabel("a1", "INBOX", labelNamed(t, fs, "a1", "Receipts").ID, 1)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	row, ok := tt.AccessByRole(ui.RoleListItem, "Maya Chen Quarterly budget review, "+locales.RowLabels("Projects, Finance, Receipts"))
	if !ok {
		t.Fatalf("the row as read: %+v", tt.AccessAll())
	}
	more, ok := tt.Access(locales.MoreLabels(1))
	if !ok || countText(tt, locales.MoreLabels(1)) != 1 {
		t.Fatalf("no +1: %q", tt.Texts())
	}
	in := func(b ui.Rect) bool {
		return b.X >= row.Bounds.X && b.X+b.W <= row.Bounds.X+row.Bounds.W && b.Y >= row.Bounds.Y && b.Y+b.H <= row.Bounds.Y+row.Bounds.H
	}
	if !in(more.Bounds) {
		t.Errorf("+1 at %v, out of its row %v", more.Bounds, row.Bounds)
	}
	for _, name := range []string{"Projects", "Finance"} {
		chip, ok := tt.AccessByRole(ui.RoleGroup, name)
		if !ok || !in(chip.Bounds) || math.Abs(float64(chip.Bounds.Y+chip.Bounds.H/2-(more.Bounds.Y+more.Bounds.H/2))) > 2 {
			t.Errorf("chip %s at %v, +1 at %v", name, chip.Bounds, more.Bounds)
		}
	}

	// The budget review's conversation in the inbox has the labels of its
	// message there; searched in every folder, those of its answer in
	// Sent too.
	fs = labelsService(true)
	fs.addLabel("a1", "Answered", mail.LabelYellow)
	fs.putLabel("a1", "Sent", labelNamed(t, fs, "a1", "Answered").ID, 6)
	a = newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt = ui.NewTester(a.view, 1220, 800)
	if got := a.labelNames("a1", a.rowLabelsOf(0)); got != "Projects" {
		t.Errorf("the inbox's row = %q", got)
	}
	search(a, tt, "budget")
	if got := a.labelNames("a1", a.rowLabelsOf(0)); got != "Projects, Answered" {
		t.Errorf("the row of every folder = %q", got)
	}
}

// The labels' strings exist in Simplified Chinese, and the checklist says
// them.
func TestLabelStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"newLabel": zh.NewLabel(), "changeLabels": zh.ChangeLabels(), "labelLocal": zh.LabelLocal(),
		"labelLocalDetail": zh.LabelLocalDetail("工作"), "deleteLabelMessage": zh.DeleteLabelMessage(),
		"labelPartlyMessage": zh.LabelPartlyMessage(), "labelsSeveralAccounts": zh.LabelsSeveralAccounts(),
		"labelKeptHereNote": zh.LabelKeptHereNote("工作", 2), "colorTeal": zh.ColorTeal(), "fieldLabel": zh.FieldLabel(),
	} {
		if text == "" || !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.LabelKeptHereNote("工作", 2); !strings.Contains(got, "2") {
		t.Errorf("plural = %q", got)
	}
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, _, tt := startLabels(t, false)
	tt.SetLocale("zh-Hans")
	click(t, tt, "Lunch on Friday?")
	click(t, tt, zh.ChangeLabels())
	for _, text := range []string{zh.Labels(), zh.NewLabelEllipsis(), zh.NewLabel()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if !a.labels.picker {
		t.Error("the checklist did not open")
	}
}

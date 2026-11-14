package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// bookApp starts the window on a service and opens the address book from
// the mail window's sidebar.
func bookApp(t *testing.T, fs *fakeService) (*app, *ui.Tester) {
	t.Helper()
	a := openApp(fs)
	tt := ui.NewTester(a.view, 1220, 800)
	clickMore(t, tt, locales.Contacts())
	if !a.book.open || !tt.HasText(locales.ContactsDetail()) {
		t.Fatalf("the address book did not open: %q", tt.Texts())
	}
	return a, tt
}

// rows are the contacts the address book lists, as assistive technology
// names their rows.
func rows(tt *ui.Tester) []string {
	var out []string
	for _, info := range tt.AccessAll() {
		if info.Role == ui.RoleListItem {
			out = append(out, info.Name)
		}
	}
	return out
}

// bookOrder is contactsService's book, best first: those Ann writes to,
// by how much and how lately, then those who only wrote to her.
var bookOrder = []string{
	"Maya Chen maya@example.com", "Lena Lee lena.lee@example.com", "王小明 wang@example.cn",
	"Bob Lee bob.lee@example.com", "Marta Díaz marta@example.org",
	"Mark Stone mark.stone@example.net", "Raj Patel raj@example.com",
}

// suggestedFirst types in a new message's To field and returns the
// contacts it offers.
func suggestedFirst(t *testing.T, a *app, typed string) []string {
	t.Helper()
	cs := a.openCompose(nil)
	defer cs.close()
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.To(), typed)
	names, _ := offers(ct)
	return names
}

// The address book opens from the mail window and lists the contacts in
// the engine's order, never the user's own addresses; Done and Escape go
// back to the mail. It says that the contacts stay on this device.
func TestAddressBookOpensFromTheMailWindow(t *testing.T) {
	a, tt := bookApp(t, contactsService())
	if got := rows(tt); !slices.Equal(got, bookOrder) {
		t.Fatalf("rows = %q\nwant %q", got, bookOrder)
	}
	if !tt.Focused(locales.SearchContacts()) {
		t.Error("the search field does not take the keyboard")
	}
	click(t, tt, locales.Done())
	if a.book.open || !tt.HasText(locales.Inbox()) {
		t.Fatalf("Done: %q", tt.Texts())
	}
	clickMore(t, tt, locales.Contacts())
	tt.Key(0, ui.KeyEscape)
	if a.book.open {
		t.Error("Escape left the address book open")
	}
}

// The search finds contacts by the beginning of a word of their name or
// address, and says when it finds none.
func TestAddressBookSearch(t *testing.T) {
	a, tt := bookApp(t, contactsService())
	tt.Type("lee")
	if got := rows(tt); !slices.Equal(got, []string{"Lena Lee lena.lee@example.com", "Bob Lee bob.lee@example.com"}) {
		t.Fatalf("“lee” finds %q", got)
	}
	tt.Type("zz")
	if got := rows(tt); len(got) != 0 || !tt.HasText(locales.ContactsNoMatch("leezz")) {
		t.Fatalf("“leezz” finds %q: %q", got, tt.Texts())
	}
	a.book.search = ""
	a.searchContacts()
	tt.Frame()
	if got := rows(tt); !slices.Equal(got, bookOrder) {
		t.Errorf("the search cleared lists %q", got)
	}

	// A book with nobody in it says how people come into it.
	_, et := bookApp(t, composeService())
	if !et.HasText(locales.ContactsEmpty()) || len(rows(et)) != 0 {
		t.Errorf("an empty book: %q", et.Texts())
	}
}

// A word typed a letter at a time in the search field asks the book once,
// when the typing pauses: each keystroke retires the search before it.
func TestAddressBookSearchWaitsForTheTypingToPause(t *testing.T) {
	fs := contactsService()
	a, tt := bookApp(t, fs)
	ti := &searchTimers{}
	a.after = ti.after
	reads := fs.contactReads
	tt.TypeKey(0, ui.KeyL, "l")
	tt.TypeKey(0, ui.KeyE, "e")
	if len(ti.armed) != 2 {
		t.Fatalf("two keystrokes armed %d searches", len(ti.armed))
	}
	if fs.contactReads != reads {
		t.Errorf("the book read before the typing paused: %d reads", fs.contactReads-reads)
	}
	ti.fire()
	if got := fs.contactReads - reads; got != 1 {
		t.Errorf("reads = %d, want one", got)
	}
	tt.Frame()
	if got := rows(tt); !slices.Equal(got, []string{"Lena Lee lena.lee@example.com", "Bob Lee bob.lee@example.com"}) {
		t.Errorf("“le” finds %q", got)
	}
}

// Closing the address book while a search waits for the pause reads
// nothing: the book the user left is not read again.
func TestAddressBookClosedWhileItWaitsReadsNothing(t *testing.T) {
	fs := contactsService()
	a, tt := bookApp(t, fs)
	ti := &searchTimers{}
	a.after = ti.after
	tt.TypeKey(0, ui.KeyL, "l")
	reads := fs.contactReads
	a.closeContacts()
	ti.fire()
	if fs.contactReads != reads {
		t.Errorf("the closed book read again: %d reads", fs.contactReads-reads)
	}
}

// A long book is read a page at a time, the next one as the list nears
// the end of those read, each contact once; an edit reads it again for
// as many as it shows, so that the list keeps its length.
func TestAddressBookPaging(t *testing.T) {
	fs := composeService()
	const n = 2*mail.DefaultPageSize + 30
	var order []string
	for i := range n {
		name, email := fmt.Sprintf("Person %03d", i), fmt.Sprintf("person%03d@example.org", i)
		fs.wrote(name, email, bookDay(time.March, 1).Add(-time.Duration(i)*time.Hour))
		order = append(order, email)
	}
	a, tt := bookApp(t, fs)
	emails := func() []string {
		var out []string
		for _, ct := range a.book.contacts {
			out = append(out, ct.Email)
		}
		return out
	}
	if got := emails(); !slices.Equal(got, order[:mail.DefaultPageSize]) || !a.book.more {
		t.Fatalf("the first page has %d contacts, more %v", len(got), a.book.more)
	}
	for _, want := range []int{2 * mail.DefaultPageSize, n} {
		a.book.list.ScrollTo(len(a.book.contacts)-1, ui.End)
		tt.Frame()
		if got := emails(); !slices.Equal(got, order[:want]) {
			t.Fatalf("after scrolling down: %d contacts, want %d", len(got), want)
		}
	}
	if a.book.more {
		t.Error("more follow the last page")
	}
	// The last made a favorite: it comes first, and the list keeps all.
	a.book.list.ScrollTo(n-1, ui.Center)
	tt.Frame()
	click(t, tt, locales.FavoriteContact("Person 129"))
	if got := emails(); len(got) != n || got[0] != "person129@example.org" || !slices.Equal(got[1:], order[:n-1]) {
		t.Errorf("after the favorite: %d contacts, first %q", len(got), got[0])
	}
}

// A contact added by hand: the engine's refusals are said in the user's
// words, the address field marked invalid; then it is in the book and in
// the next suggestion. Escape and Cancel close the form, not the book.
func TestAddContactByHand(t *testing.T) {
	a, tt := bookApp(t, contactsService())
	click(t, tt, locales.AddContact())
	if !tt.Focused(locales.ContactName()) {
		t.Fatal("the form does not take the keyboard")
	}
	tt.Type("Zoe  Park")
	typeIn(t, tt, locales.ContactEmail(), "zoe")
	tt.Key(0, ui.KeyEnter)
	if !tt.HasText(locales.ContactNotAddress()) {
		t.Fatalf("not an address: %q", tt.Texts())
	}
	if info, _ := tt.AccessByRole(ui.RoleTextField, locales.ContactEmail()); !info.Invalid ||
		!slices.Equal(info.ErrorMessage, []string{locales.ContactNotAddress()}) {
		t.Errorf("the address field = %+v", info)
	}
	for _, c := range []struct{ email, want string }{
		{"maya@example.com", locales.ContactExists("maya@example.com")},
		{"ANN@example.com", locales.ContactOwn("ANN@example.com")},
		{"ann@lab.example.org", locales.ContactOwn("ann@lab.example.org")},
	} {
		a.book.email = c.email
		tt.Frame()
		click(t, tt, locales.ContactAdd())
		if !tt.HasText(c.want) || !a.book.adding {
			t.Errorf("%s: %q", c.email, tt.Texts())
		}
	}
	a.book.email = "zoe@example.org"
	tt.Frame()
	click(t, tt, locales.ContactAdd())
	if a.book.adding || tt.HasText(locales.ContactOwn("ann@lab.example.org")) || !tt.Focused(locales.AddContact()) {
		t.Fatalf("after adding: %q", tt.Texts())
	}
	if !slices.Contains(rows(tt), "Zoe Park zoe@example.org") {
		t.Fatalf("Zoe is not listed: %q", rows(tt))
	}
	if got := suggestedFirst(t, a, "zo"); !slices.Equal(got, []string{"Zoe Park zoe@example.org"}) {
		t.Errorf("“zo” offers %q", got)
	}

	// Escape in the form closes it; Cancel too.
	click(t, tt, locales.AddContact())
	tt.Type("Nobody")
	tt.Key(0, ui.KeyEscape)
	if a.book.adding || !a.book.open || !tt.Focused(locales.AddContact()) {
		t.Errorf("Escape: adding %v, open %v", a.book.adding, a.book.open)
	}
	click(t, tt, locales.AddContact())
	click(t, tt, locales.ContactAddCancel())
	if a.book.adding || !a.book.open {
		t.Errorf("Cancel: adding %v, open %v", a.book.adding, a.book.open)
	}
}

// A contact renamed shows its new name in the book and in the next
// suggestion; Escape leaves the name as it was; an empty name hands the
// naming back to the mail. The keyboard goes back to the rename button.
func TestRenameContact(t *testing.T) {
	a, tt := bookApp(t, contactsService())
	click(t, tt, locales.RenameContact("Maya Chen"))
	if info, _ := tt.AccessByRole(ui.RoleTextField, locales.ContactNewName("maya@example.com")); info.Value != "Maya Chen" ||
		!tt.Focused(locales.ContactNewName("maya@example.com")) {
		t.Fatalf("the name field = %+v", info)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Maya Q. Chen")
	tt.Key(0, ui.KeyEnter)
	if got := rows(tt); got[0] != "Maya Q. Chen maya@example.com" || !tt.Focused(locales.RenameContact("Maya Q. Chen")) {
		t.Fatalf("renamed: %q, rename focused %v", got, tt.Focused(locales.RenameContact("Maya Q. Chen")))
	}
	if got := suggestedFirst(t, a, "ma"); len(got) == 0 || got[0] != "Maya Q. Chen maya@example.com" {
		t.Errorf("“ma” offers %q", got)
	}
	if got := suggestedFirst(t, a, "q."); len(got) != 1 {
		t.Errorf("“q.” offers %q", got)
	}

	// Escape leaves the name.
	click(t, tt, locales.RenameContact("Maya Q. Chen"))
	tt.Type(" the Second")
	tt.Key(0, ui.KeyEscape)
	if !a.book.open || a.book.renaming != "" || rows(tt)[0] != "Maya Q. Chen maya@example.com" {
		t.Fatalf("Escape: %q", rows(tt))
	}

	// Emptied, the name is the mail's again: none until mail names her.
	click(t, tt, locales.RenameContact("Maya Q. Chen"))
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	click(t, tt, locales.ContactRenameSave())
	if got := rows(tt); got[0] != "maya@example.com" {
		t.Errorf("emptied: %q", got)
	}
	if got := suggestedFirst(t, a, "maya"); len(got) == 0 || got[0] != "maya@example.com" {
		t.Errorf("“maya” offers %q", got)
	}
}

// A favorite comes first, in the book and in the suggestions, and the
// button says so; it keeps the keyboard as its row moves.
func TestFavoriteContact(t *testing.T) {
	a, tt := bookApp(t, contactsService())
	click(t, tt, locales.FavoriteContact("Mark Stone"))
	if got := rows(tt); got[0] != "Mark Stone mark.stone@example.net" || !tt.Focused(locales.UnfavoriteContact("Mark Stone")) {
		t.Fatalf("Mark made a favorite: %q", got)
	}
	if got := suggestedFirst(t, a, "ma"); !slices.Equal(got, []string{markOffer, mayaOffer, martaOffer}) {
		t.Errorf("“ma” offers %q", got)
	}
	click(t, tt, locales.UnfavoriteContact("Mark Stone"))
	if got := rows(tt); !slices.Equal(got, bookOrder) {
		t.Errorf("Mark no favorite: %q", got)
	}
	if got := suggestedFirst(t, a, "ma"); !slices.Equal(got, []string{mayaOffer, martaOffer, markOffer}) {
		t.Errorf("“ma” offers %q", got)
	}
}

// Deleting a contact asks first, saying that writing to it again brings
// it back; once deleted it is in neither the book nor the suggestions,
// and the keyboard goes to the contact after it. Written to again, it is
// back.
func TestDeleteContactAfterAsking(t *testing.T) {
	fs := contactsService()
	fs.clock = func() time.Time { return bookDay(time.March, 10) }
	a, tt := bookApp(t, fs)
	click(t, tt, locales.DeleteContact("Marta Díaz"))
	for _, text := range []string{locales.DeleteContactTitle("Marta Díaz"), locales.DeleteContactMessage("marta@example.org")} {
		if !tt.HasText(text) {
			t.Fatalf("the question lacks %q: %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Cancel())
	if !slices.Contains(rows(tt), "Marta Díaz marta@example.org") {
		t.Fatalf("cancelled, Marta is gone: %q", rows(tt))
	}
	click(t, tt, locales.DeleteContact("Marta Díaz"))
	click(t, tt, locales.DeleteContactButton())
	if got := rows(tt); slices.Contains(got, "Marta Díaz marta@example.org") || len(got) != len(bookOrder)-1 {
		t.Fatalf("deleted, the book lists %q", got)
	}
	if !tt.Focused(locales.DeleteContact("Mark Stone")) {
		t.Error("the keyboard is not on the next contact")
	}
	if got := suggestedFirst(t, a, "ma"); !slices.Equal(got, []string{mayaOffer, markOffer}) {
		t.Errorf("“ma” offers %q", got)
	}

	// Written to again.
	cs := a.openCompose(nil)
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.To(), "Marta Díaz <marta@example.org>")
	typeIn(t, ct, locales.Subject(), "Hello again")
	click(t, ct, locales.Send())
	if got := suggestedFirst(t, a, "ma"); !slices.Contains(got, martaOffer) {
		t.Errorf("written to again, Marta is not offered: %q", got)
	}
}

// Every action of the book is reached with the keyboard: Tab goes from
// the search to the add button and the buttons of each row, Enter
// renames, makes a favorite and deletes, with the question's default
// button.
func TestAddressBookByKeyboard(t *testing.T) {
	fs := contactsService()
	fs.clock = func() time.Time { return bookDay(time.March, 10) }
	_, tt := bookApp(t, fs)
	for _, want := range []string{locales.AddContact(), locales.FavoriteContact("Maya Chen"), locales.RenameContact("Maya Chen"),
		locales.DeleteContact("Maya Chen"), locales.FavoriteContact("Lena Lee")} {
		tt.Key(0, ui.KeyTab)
		if !tt.Focused(want) {
			t.Fatalf("Tab did not reach %q", want)
		}
	}
	tt.Key(0, ui.KeyEnter)
	if got := rows(tt); got[0] != "Lena Lee lena.lee@example.com" {
		t.Fatalf("Enter on the star: %q", got)
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	if !tt.Focused(locales.ContactNewName("lena.lee@example.com")) {
		t.Fatal("Enter on rename does not edit the name")
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Lena")
	tt.Key(0, ui.KeyEnter)
	if got := rows(tt); got[0] != "Lena lena.lee@example.com" || !tt.Focused(locales.RenameContact("Lena")) {
		t.Fatalf("renamed by keyboard: %q", got)
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	if got := rows(tt); slices.Contains(got, "Lena lena.lee@example.com") || !tt.Focused(locales.DeleteContact("Maya Chen")) {
		t.Errorf("deleted by keyboard: %q", got)
	}
}

// An address book the store cannot read, or edit, says so.
func TestAddressBookFailures(t *testing.T) {
	fs := contactsService()
	fs.contactErr = errFake
	a, tt := bookApp(t, fs)
	if !tt.HasText(locales.ContactsReadFailed(errFake.Error())) || tt.HasText(locales.ContactsEmpty()) {
		t.Fatalf("a book that cannot be read: %q", tt.Texts())
	}
	fs.contactErr = nil
	a.book.search = "m"
	a.searchContacts()
	tt.Frame()
	fs.contactErr = errFake
	click(t, tt, locales.FavoriteContact("Maya Chen"))
	if !tt.HasText(locales.ContactEditFailed(errFake.Error())) {
		t.Errorf("an edit that failed: %q", tt.Texts())
	}
	click(t, tt, locales.AddContact())
	tt.Type("Zoe")
	typeIn(t, tt, locales.ContactEmail(), "zoe@example.org")
	tt.Key(0, ui.KeyEnter)
	if !tt.HasText(locales.ContactAddFailed(errFake.Error())) {
		t.Errorf("an add that failed: %q", tt.Texts())
	}
}

// The words of the address book are in Simplified Chinese too.
func TestContactsStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"contacts": zh.Contacts(), "contactsDetail": zh.ContactsDetail(), "searchContacts": zh.SearchContacts(),
		"addContact": zh.AddContact(), "contactName": zh.ContactName(), "contactNamePlaceholder": zh.ContactNamePlaceholder(),
		"contactEmail": zh.ContactEmail(), "contactAdd": zh.ContactAdd(), "contactAddCancel": zh.ContactAddCancel(),
		"contactNotAddress": zh.ContactNotAddress(), "contactExists": zh.ContactExists("a@b.c"), "contactOwn": zh.ContactOwn("a@b.c"),
		"contactAddFailed": zh.ContactAddFailed("x"), "contactEditFailed": zh.ContactEditFailed("x"),
		"contactsReadFailed": zh.ContactsReadFailed("x"), "contactsEmpty": zh.ContactsEmpty(), "contactsNoMatch": zh.ContactsNoMatch("x"),
		"favoriteContact": zh.FavoriteContact("Maya"), "unfavoriteContact": zh.UnfavoriteContact("Maya"),
		"renameContact": zh.RenameContact("Maya"), "contactNewName": zh.ContactNewName("a@b.c"),
		"contactNameFromMail": zh.ContactNameFromMail(), "contactRenameSave": zh.ContactRenameSave(),
		"contactRenameCancel": zh.ContactRenameCancel("Maya"), "deleteContact": zh.DeleteContact("Maya"),
		"deleteContactTitle": zh.DeleteContactTitle("Maya"), "deleteContactMessage": zh.DeleteContactMessage("a@b.c"),
		"deleteContactButton": zh.DeleteContactButton(),
	} {
		if !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
}

// At the main window's smallest size, in both appearances, the sidebar's
// menu is in the title bar and opens the book, whose search, add button
// and each row's buttons are inside the window.
func TestAddressBookAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := openApp(contactsService())
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		clickMore(t, tt, locales.Contacts())
		if !a.book.open {
			t.Fatalf("dark=%v: the contacts button at the smallest size opens nothing", dark)
		}
		click(t, tt, locales.RenameContact("Lena Lee"))
		for _, label := range []string{locales.SearchContacts(), locales.AddContact(), locales.FavoriteContact("Maya Chen"),
			locales.RenameContact("Maya Chen"), locales.DeleteContact("Maya Chen"), locales.ContactRenameSave(), locales.Done()} {
			info, ok := tt.Access(label)
			box := info.Bounds
			if !ok || box.W <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > 1000 || box.Y+box.H > 640 {
				t.Errorf("dark=%v: %q is at %+v (found %v)", dark, label, box, ok)
			}
		}
	}
}

// The address book opens in a window of its own, with the book's title and
// room for the list; closing it goes back to the mail.
func TestAddressBookOpensItsOwnWindow(t *testing.T) {
	a := openApp(contactsService())
	var got mygo.WindowOptions
	calls := 0
	a.newWindow = func(o mygo.WindowOptions) *mygo.Window {
		calls++
		got = o
		return nil
	}
	a.openContacts()
	if calls != 1 {
		t.Fatalf("windows = %d", calls)
	}
	if got.Title != locales.Contacts() || got.Width < 460 || got.Height < 420 || got.Content == nil {
		t.Errorf("window = %+v", got)
	}
	if !a.book.open {
		t.Error("the book did not open")
	}
	a.closeContacts()
	if a.book.open {
		t.Error("closeContacts left the book open")
	}
}

// Without a window, the book shows in the main view, which is how the
// tests without a window read it.
func TestAddressBookWithoutAWindow(t *testing.T) {
	a := openApp(contactsService())
	a.newWindow = nil
	a.openContacts()
	if !a.book.open {
		t.Fatal("the book did not open")
	}
	if a.contactsWin != nil {
		t.Error("a window appeared without newWindow")
	}
}

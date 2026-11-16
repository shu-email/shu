package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// streamApp is a message window on an account whose first message is a
// code, as mail often is.
func streamApp(t *testing.T, previews ...string) (*app, *ui.Tester) {
	t.Helper()
	fs := inMessageView(newFakeService())
	msgs := testMessages("a1")
	for i := range msgs {
		if i < len(previews) {
			msgs[i].Preview = previews[i]
			msgs[i].Seen = false
		}
	}
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", testFolders(), msgs)
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, ui.NewTester(a.view, 1220, 800)
}

// The stream shows what mail asks of the user without the reading pane:
// a code is a chip that copies it, and the band says what the rows read
// come to.
func TestStreamShowsACodeToCopy(t *testing.T) {
	_, tt := streamApp(t, "Your verification code is 482913. It expires in 10 minutes.")
	click(t, tt, locales.Stream())
	tt.Frame()

	if !tt.HasText("482913") || !tt.HasText(locales.ExtractCode("482913")) {
		t.Fatalf("the code is not on the row: %v", tt.Texts())
	}
	if !tt.HasText(locales.StreamCodes(1)) {
		t.Errorf("the band does not count the code: %q", tt.Texts())
	}
	click(t, tt, "482913")
	if got := tt.Clipboard(); got != "482913" {
		t.Errorf("the clipboard = %q, want the code", got)
	}
	if !tt.HasText(locales.CopiedExtract()) {
		t.Errorf("the row does not say it was copied: %q", tt.Texts())
	}
}

// The band counts what the rows read ask of the user, and only the
// stream has one.
func TestStreamBandCounts(t *testing.T) {
	a, tt := streamApp(t, "Lunch on Friday?", "Lunch on Friday?", "Lunch on Friday?")
	if tt.HasText(locales.StreamNeeds(len(a.all))) {
		t.Fatal("a folder's list has a stream band")
	}
	click(t, tt, locales.Stream())
	tt.Frame()
	if !tt.HasText(locales.StreamNeeds(len(a.all))) {
		t.Errorf("the band does not count what needs the user: %q", tt.Texts())
	}
}

// A row's amount and due date are shown as its code is, and nothing is
// read out of a message that says neither.
func TestStreamReadsAmountsAndDates(t *testing.T) {
	_, tt := streamApp(t, "Invoice 1280 due before 2026-03-14.", "Order 482913 has shipped.", "")
	click(t, tt, locales.Stream())
	tt.Frame()
	if !tt.HasText(locales.ExtractAmount("1280")) && !tt.HasText("1280") {
		t.Errorf("the amount is not on the row: %q", tt.Texts())
	}
	if !tt.HasText("2026-03-14") {
		t.Errorf("the date is not on the row: %q", tt.Texts())
	}
	// A number with nothing near it saying it is a code is not one.
	if got := rowExtracts(mail.Summary{Preview: "Order 482913 has shipped."}); len(got) > 0 && got[0].Kind == mail.KindCode {
		t.Errorf("a bare number was read as a code: %v", got)
	}
}

// The sidebar lists the ways into the mail and no folder of the account:
// a folder is where mail sits on a server, not where the user files it.
func TestSidebarListsNoFolders(t *testing.T) {
	_, _, tt := folderApp(t)
	if !tt.HasText(locales.Stream()) || !tt.HasText(locales.Starred()) || !tt.HasText(locales.Archive()) {
		t.Fatalf("the ways into the mail: %q", tt.Texts())
	}
	if tt.HasText("Projects") {
		t.Errorf("the sidebar lists the account's folder: %q", tt.Texts())
	}
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.NewFolder()); err == nil {
		t.Error("the accounts menu still makes folders")
	}
	tt.CloseMenu()
}

// A label is still a mark the user keeps and browses: the sidebar lists
// the account's labels under the ways into the mail.
func TestLabelsStayUnderTheWaysIn(t *testing.T) {
	_, _, tt := folderApp(t)
	if !tt.HasText(locales.Labels()) || !tt.HasText(locales.Pinned()) {
		t.Fatalf("the sidebar's sections: %q", tt.Texts())
	}
}

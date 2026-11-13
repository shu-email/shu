package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// folderApp is a message window whose account has a Projects folder
// besides the system ones.
func folderApp(t *testing.T) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	fs := newFakeService()
	folders := append(testFolders(), mail.Folder{Name: "Projects", Delimiter: "/", Subscribed: true, Total: 2})
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", folders, testMessages("a1"))
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, fs, ui.NewTester(a.view, 1220, 800)
}

// folderNamed finds a folder of the fake account.
func folderNamed(fs *fakeService, name string) (mail.Folder, bool) {
	for _, f := range fs.folders["a1"] {
		if f.Name == name {
			return f, true
		}
	}
	return mail.Folder{}, false
}

func TestFolderLeavesAndErrors(t *testing.T) {
	for _, c := range []struct {
		folder mail.Folder
		want   string
	}{
		{mail.Folder{Name: "Projects", Delimiter: "/"}, "Projects"},
		{mail.Folder{Name: "Projects/Q4", Delimiter: "/"}, "  Q4"},
		{mail.Folder{Name: "Projects/Q4/Launch", Delimiter: "/"}, "    Launch"},
		{mail.Folder{Name: "A.B", Delimiter: "."}, "  B"},
		{mail.Folder{Name: "No delimiter"}, "No delimiter"},
	} {
		if got := folderLeaf(c.folder); got != c.want {
			t.Errorf("%q: %q, want %q", c.folder.Name, got, c.want)
		}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{mail.ErrFolderExists, locales.FolderExists()},
		{mail.ErrFolderName, locales.FolderBadName()},
		{mail.ErrFolderSystem, locales.FolderSystem()},
		{mail.ErrNotFound, locales.FolderGone()},
	} {
		if got := folderErrorText(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// A folder made from the account menu appears in the sidebar.
func TestCreateFolderFromTheMenu(t *testing.T) {
	a, fs, tt := folderApp(t)
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.NewFolder()); err != nil {
		t.Fatal(err)
	}
	if !a.folderForm.open || a.folderForm.mode != "create" {
		t.Fatalf("form = %+v", a.folderForm)
	}
	focusField(t, tt, locales.FolderNameLabel(), "Clients")
	click(t, tt, locales.CreateFolder())
	if _, ok := folderNamed(fs, "Clients"); !ok {
		t.Fatalf("folders = %+v", fs.folders["a1"])
	}
	if !tt.HasText("Clients") {
		t.Errorf("the sidebar does not show the new folder: %q", tt.Texts())
	}
	if a.folderNote != locales.FolderCreated("Clients") {
		t.Errorf("note = %q", a.folderNote)
	}
}

// A name the server refuses stays in the dialog, which can be corrected.
func TestCreateFolderFailureStaysOpen(t *testing.T) {
	a, fs, tt := folderApp(t)
	fs.folderErr = mail.ErrFolderName
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.NewFolder()); err != nil {
		t.Fatal(err)
	}
	focusField(t, tt, locales.FolderNameLabel(), "bad/")
	click(t, tt, locales.CreateFolder())
	if !a.folderForm.open || a.folderForm.err != locales.FolderBadName() {
		t.Fatalf("form = %+v", a.folderForm)
	}
	if !tt.HasText(locales.FolderBadName()) {
		t.Errorf("the dialog does not say why: %q", tt.Texts())
	}
}

func TestRenameFolderFromItsMenu(t *testing.T) {
	a, fs, tt := folderApp(t)
	if err := tt.RightClick("Projects"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.RenameFolder()); err != nil {
		t.Fatal(err)
	}
	focusField(t, tt, locales.FolderNameLabel(), "Work")
	click(t, tt, locales.Save())
	if _, ok := folderNamed(fs, "Work"); !ok {
		t.Fatalf("folders = %+v", fs.folders["a1"])
	}
	if _, ok := folderNamed(fs, "Projects"); ok {
		t.Error("the old name is still there")
	}
	if a.folderNote != locales.FolderRenamed("Projects", "Work") {
		t.Errorf("note = %q", a.folderNote)
	}
	if !tt.HasText("Work") {
		t.Errorf("the sidebar does not show the new name: %q", tt.Texts())
	}
}

// Deleting a folder asks first, names the mail that goes with it, and
// leaves the list alone if the user says no.
func TestDeleteFolderAsksFirst(t *testing.T) {
	a, fs, tt := folderApp(t)
	if err := tt.RightClick("Projects"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.DeleteFolder()); err != nil {
		t.Fatal(err)
	}
	if !a.folderForm.confirming || !tt.HasText(locales.DeleteFolderTitle("Projects")) {
		t.Fatalf("no confirmation: %+v, %q", a.folderForm, tt.Texts())
	}
	if !strings.Contains(locales.DeleteFolderConfirm("Projects", 2), "2 messages") {
		t.Fatalf("the confirmation does not count the mail: %q", locales.DeleteFolderConfirm("Projects", 2))
	}
	click(t, tt, locales.Cancel())
	if _, ok := folderNamed(fs, "Projects"); !ok {
		t.Error("Cancel deleted the folder")
	}
	if a.folderForm.target != "" {
		t.Errorf("target after Cancel = %q", a.folderForm.target)
	}

	if err := tt.RightClick("Projects"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.DeleteFolder()); err != nil {
		t.Fatal(err)
	}
	click(t, tt, locales.DeleteFolder())
	if _, ok := folderNamed(fs, "Projects"); ok {
		t.Error("the folder is still in the account")
	}
	if !slices.Contains(fs.folderCalls, "delete Projects") {
		t.Errorf("calls = %v", fs.folderCalls)
	}
	if a.folderNote != locales.FolderDeleted("Projects") {
		t.Errorf("note = %q", a.folderNote)
	}
}

// Hiding a folder takes it out of the sidebar and keeps its mail; the
// account menu brings it back.
func TestHideAndShowFolder(t *testing.T) {
	a, fs, tt := folderApp(t)
	if err := tt.RightClick("Projects"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.HideFromSidebar()); err != nil {
		t.Fatal(err)
	}
	if f, _ := folderNamed(fs, "Projects"); f.Subscribed {
		t.Error("the folder stayed subscribed")
	}
	// The note names the folder too; the sidebar does not.
	a.folderNote = ""
	tt.Frame()
	if tt.HasText("Projects") {
		t.Errorf("the hidden folder is still in the sidebar: %q", tt.Texts())
	}
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.HiddenFolders(), "Projects"); err != nil {
		t.Fatal(err)
	}
	if f, _ := folderNamed(fs, "Projects"); !f.Subscribed {
		t.Error("the folder stayed hidden")
	}
	a.folderNote = ""
	tt.Frame()
	if !tt.HasText("Projects") {
		t.Errorf("the folder is not in the sidebar again: %q", tt.Texts())
	}
}

// The list's menu moves the chosen message to a folder by name, with the
// same undo as the toolbar's moves.
func TestMoveToFolderFromTheRowMenu(t *testing.T) {
	a, fs, tt := folderApp(t)
	if err := tt.RightClick("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.MoveToFolder(), "Projects"); err != nil {
		t.Fatal(err)
	}
	if len(fs.movedRefs) != 1 || !strings.Contains(fs.movedRefs[0], "Projects") {
		t.Fatalf("moves = %v", fs.movedRefs)
	}
	if a.undo == nil || a.undo.label != locales.MovedToFolder("Projects") {
		t.Errorf("undo = %+v", a.undo)
	}
}

// A move the server could not be made for yet is offered for undo too:
// taking it back drops the queued action, and the message never left.
func TestUndoOfAMoveWaitingForTheServer(t *testing.T) {
	a, fs, tt := folderApp(t)
	fs.movePending = true
	if len(a.all) == 0 {
		t.Fatal("no rows")
	}
	m := a.all[0]
	a.moveMessagesToFolder([]mail.Summary{m}, "Projects")
	if a.undo == nil || len(a.undo.moved["a1"]) != 1 || a.undo.moved["a1"][0].Pending == 0 {
		t.Fatalf("undo = %+v", a.undo)
	}
	a.undoLastMove()
	tt.Frame()
	if !slices.Contains(fs.moveBacks, "Projects->"+m.Folder) {
		t.Errorf("the undo did not take the queued move back: %v", fs.moveBacks)
	}
	if a.undo != nil {
		t.Errorf("the undo is still offered: %+v", a.undo)
	}
}

// focusField types into the field a caption names, from its start.
func focusField(t *testing.T, tt *ui.Tester, caption, text string) {
	t.Helper()
	click(t, tt, caption)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(text)
}

// Dragging rows carries the messages they show, or the conversations,
// and dropping them on a folder moves them.
func TestDragRowsOntoAFolder(t *testing.T) {
	a, fs, _ := folderApp(t)
	d, ok := a.dragRows([]int{0}).(*folderDrop)
	if !ok || len(d.convs) != 1 || d.account != "a1" {
		t.Fatalf("drag = %#v", a.dragRows([]int{0}))
	}
	a.dropOnFolder(d, "Projects")
	if len(fs.movedRefs) == 0 || !strings.Contains(fs.movedRefs[0], "Projects") {
		t.Fatalf("moves = %v", fs.movedRefs)
	}

	// A row of another account is not for this account's folders.
	other := a.dragRows(nil)
	if other != nil {
		t.Errorf("an empty drag = %#v", other)
	}
	if d := (&folderDrop{account: "a2", msgs: []mail.Summary{{Account: "a2"}}}); true {
		before := len(fs.movedRefs)
		a.dropOnFolder(d, "Projects")
		if len(fs.movedRefs) != before {
			t.Error("a drop from another account moved mail")
		}
	}
}

func TestMoveMessagesToFolderSkipsWhatIsThere(t *testing.T) {
	a, fs, _ := folderApp(t)
	a.moveMessagesToFolder([]mail.Summary{
		{Account: "a1", Folder: "Projects", UID: 1, Subject: "Already there"},
		{Account: "a2", Folder: "INBOX", UID: 2, Subject: "Another account"},
	}, "Projects")
	if len(fs.moved) != 0 {
		t.Errorf("moved = %v", fs.moved)
	}
}

package app

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
	a.undoLastAction()
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

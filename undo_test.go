package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Starring offers to take it back in the list's one banner, and the undo
// asks the engine to put the star back where it was.
func TestUndoAStar(t *testing.T) {
	a, fs, tt := folderApp(t)
	click(t, tt, "Design review — Thursday at 10")
	tt.Key(0, ui.KeyS)
	tt.Frame()
	if a.undo == nil || a.undo.label != locales.Starred() || a.undo.flag != mail.FlagFlagged || !a.undo.on {
		t.Fatalf("undo = %+v", a.undo)
	}
	click(t, tt, locales.Undo())
	tt.Frame()
	if !slices.Contains(fs.undoneChanges, fmt.Sprintf("flag %s true", mail.FlagFlagged)) {
		t.Errorf("undone = %v", fs.undoneChanges)
	}
	if a.undo != nil {
		t.Errorf("the undo is still offered: %+v", a.undo)
	}
}

// A star of several messages offers to take back only the stars it
// added: one that was starred before it stays starred.
func TestUndoAStarOfSeveral(t *testing.T) {
	a, fs, tt := folderApp(t)
	click(t, tt, "Design review — Thursday at 10")
	tt.Key(0, ui.KeyS)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyS)
	tt.Frame()
	if a.undo == nil || len(a.undo.refs["a1"]) == 0 {
		t.Fatalf("undo = %+v", a.undo)
	}
	for _, ref := range a.undo.refs["a1"] {
		if ref.UID == 1 {
			t.Fatalf("the undo would unstar one starred before: %+v", a.undo.refs["a1"])
		}
	}
	click(t, tt, locales.Undo())
	tt.Frame()
	if m, _ := fs.find("a1", "INBOX", 1); !m.Flagged {
		t.Error("the undo took a star it did not add")
	}
	if m, _ := fs.find("a1", "INBOX", 2); m.Flagged {
		t.Error("the star of the second message stayed")
	}
}

// Marking messages read or unread offers the undo too, and the undo says
// which way the change went.
func TestUndoMarkingRead(t *testing.T) {
	a, fs, tt := folderApp(t)
	click(t, tt, "Design review — Thursday at 10")
	unread := a.folderUnread("INBOX")
	tt.Key(0, ui.KeyU)
	tt.Frame()
	if a.undo == nil || a.undo.flag != mail.FlagSeen {
		t.Fatalf("undo = %+v", a.undo)
	}
	want, on := locales.MarkedRead(), a.undo.on
	if !on {
		want = locales.MarkedUnread()
	}
	if a.undo.label != want {
		t.Fatalf("label = %q, want %q", a.undo.label, want)
	}
	click(t, tt, locales.Undo())
	tt.Frame()
	if !slices.Contains(fs.undoneChanges, fmt.Sprintf("flag %s %v", mail.FlagSeen, on)) {
		t.Errorf("undone = %v, want the read change", fs.undoneChanges)
	}
	if got := a.folderUnread("INBOX"); got != unread {
		t.Errorf("inbox unread after the undo = %d, want %d", got, unread)
	}
}

// A label put on a message is offered for undo as the moves are, and the
// undo takes it off again.
func TestUndoingALabel(t *testing.T) {
	a, fs, tt := startLabels(t, false)
	click(t, tt, "Quarterly budget review")
	openPicker(t, a, tt)
	clickRole(t, tt, ui.RoleCheckBox, "Receipts")
	tt.Frame()
	receipts := labelNamed(t, fs, "a1", "Receipts")
	if a.undo == nil || a.undo.label != locales.LabelAdded("Receipts") || a.undo.labelID != receipts.ID || !a.undo.on {
		t.Fatalf("undo = %+v", a.undo)
	}
	click(t, tt, locales.Undo())
	tt.Frame()
	if !slices.Contains(fs.undoneChanges, fmt.Sprintf("label %d true", receipts.ID)) {
		t.Errorf("undone = %v", fs.undoneChanges)
	}
	if slices.Contains(labelsOn(fs, "a1", "INBOX", 1), "Receipts") {
		t.Error("the label stayed on the message")
	}
}

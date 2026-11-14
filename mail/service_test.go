package mail

import (
	"reflect"
	"testing"
)

// A move or a flag change asks the server once for each message of a
// folder, however many references name it, and leaves the folder it skips
// out.
func TestByFolderAsksEachUIDOnce(t *testing.T) {
	got := byFolder([]MessageRef{
		{Folder: "INBOX", UID: 1},
		{Folder: "INBOX", UID: 1},
		{Folder: "INBOX", UID: 2},
		{Folder: "Sent", UID: 1},
		{Folder: "Trash", UID: 9},
	}, "Trash")
	want := []uidGroup{
		{folder: "INBOX", uids: []UID{1, 2}},
		{folder: "Sent", uids: []UID{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("byFolder = %+v, want %+v", got, want)
	}
}

package mail

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func folderService(t *testing.T) (*Service, *memStore, *fakeIMAP, Account) {
	t.Helper()
	f := startFakeIMAP(t, imap.CapSet{
		imap.CapIMAP4rev1:    {},
		imap.CapListExtended: {},
	})
	f.create(t, "INBOX", "Sent", "Drafts", "Trash", "Junk", "Archive")
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	return svc, store, f, a
}

func TestCreateFolder(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := folderService(t)

	folder, err := svc.CreateFolder(ctx, a.ID, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if folder.Name != "Projects" || !folder.Subscribed {
		t.Errorf("created %+v", folder)
	}
	if _, err := f.user.Status("Projects", &imap.StatusOptions{}); err != nil {
		t.Errorf("not on the server: %v", err)
	}
	if cached, ok, _ := store.Folder(ctx, a.ID, "Projects"); !ok || !cached.Subscribed {
		t.Errorf("cached = %+v, %v", cached, ok)
	}

	// A child of an existing folder uses the server's delimiter.
	if _, err := svc.CreateFolder(ctx, a.ID, "Projects/Q4"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Folder(ctx, a.ID, "Projects/Q4"); !ok {
		t.Error("the child is not cached")
	}

	if _, err := svc.CreateFolder(ctx, a.ID, "Projects"); !errors.Is(err, ErrFolderExists) {
		t.Errorf("a duplicate: %v", err)
	}
	for _, name := range []string{"", " ", "/leading", "trailing/", "con\x01trol"} {
		if _, err := svc.CreateFolder(ctx, a.ID, name); !errors.Is(err, ErrFolderName) {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestRenameFolderKeepsItsMail(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := folderService(t)
	if _, err := svc.CreateFolder(ctx, a.ID, "Projects"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Projects", 50); err != nil {
		t.Fatal(err)
	}
	if err := svc.RenameFolder(ctx, a.ID, "Projects", "Work"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.user.Status("Work", &imap.StatusOptions{}); err != nil {
		t.Errorf("not renamed on the server: %v", err)
	}
	if _, ok, _ := store.Folder(ctx, a.ID, "Work"); !ok {
		t.Error("the new name is not cached")
	}
	if _, ok, _ := store.Folder(ctx, a.ID, "Projects"); ok {
		t.Error("the old name is still cached")
	}
	if cached, ok, _ := store.Folder(ctx, a.ID, "Work"); ok && cached.IsSystem() {
		t.Error("a renamed folder became a system one")
	}

	if err := svc.RenameFolder(ctx, a.ID, "INBOX", "Everything"); !errors.Is(err, ErrFolderSystem) {
		t.Errorf("renaming the inbox: %v", err)
	}
	if err := svc.RenameFolder(ctx, a.ID, "Work", "Sent"); !errors.Is(err, ErrFolderExists) {
		t.Errorf("renaming onto a folder there is: %v", err)
	}
	if err := svc.RenameFolder(ctx, a.ID, "missing", "Other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming a folder there is not: %v", err)
	}
}

func TestDeleteFolderRemovesItsMail(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := folderService(t)
	if _, err := svc.CreateFolder(ctx, a.ID, "Old"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Old", 50); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFolder(ctx, a.ID, "Old"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.user.Status("Old", &imap.StatusOptions{}); err == nil {
		t.Error("the folder is still on the server")
	}
	if _, ok, _ := store.Folder(ctx, a.ID, "Old"); ok {
		t.Error("the folder is still cached")
	}

	for _, name := range []string{"INBOX", "Sent", "Drafts", "Trash", "Junk", "Archive"} {
		if err := svc.DeleteFolder(ctx, a.ID, name); !errors.Is(err, ErrFolderSystem) {
			t.Errorf("deleting %s: %v", name, err)
		}
	}
	if err := svc.DeleteFolder(ctx, a.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a folder there is not: %v", err)
	}
}

func TestFolderSubscriptions(t *testing.T) {
	ctx := context.Background()
	svc, store, _, a := folderService(t)

	if err := svc.SetFolderSubscribed(ctx, a.ID, "Archive", false); err != nil {
		t.Fatal(err)
	}
	if cached, _, _ := store.Folder(ctx, a.ID, "Archive"); cached.Subscribed {
		t.Error("the folder stayed subscribed")
	}
	// A LIST that returns subscriptions says the same to the next sync.
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if cached, _, _ := store.Folder(ctx, a.ID, "Archive"); cached.Subscribed {
		t.Error("a sync subscribed the folder again")
	}
	if err := svc.SetFolderSubscribed(ctx, a.ID, "Archive", true); err != nil {
		t.Fatal(err)
	}
	if cached, _, _ := store.Folder(ctx, a.ID, "Archive"); !cached.Subscribed {
		t.Error("the folder stayed hidden")
	}
	if err := svc.SetFolderSubscribed(ctx, a.ID, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("subscribing a folder there is not: %v", err)
	}
}

func TestSystemFolders(t *testing.T) {
	for _, c := range []struct {
		special string
		system  bool
	}{
		{SpecialInbox, true},
		{SpecialSent, true},
		{SpecialDrafts, true},
		{SpecialTrash, true},
		{SpecialJunk, true},
		{SpecialArchive, true},
		{SpecialFlagged, true},
		{"", false},
	} {
		if got := (Folder{SpecialUse: c.special}).IsSystem(); got != c.system {
			t.Errorf("%q: %v, want %v", c.special, got, c.system)
		}
	}
}

// A server is asked only for the LIST return options it advertised:
// counts (LIST-STATUS), special uses (SPECIAL-USE), both or neither.
// Every combination lists the folders and their mail as before.
func TestFolderListAsksOnlyForWhatTheServerAdvertised(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		caps imap.CapSet
	}{
		{"plain", imap.CapSet{imap.CapIMAP4rev1: {}}},
		{"list-status", imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapListStatus: {}}},
		{"special-use", imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapSpecialUse: {}}},
		{"both", imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapListStatus: {}, imap.CapSpecialUse: {}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := startFakeIMAP(t, c.caps)
			f.create(t, "INBOX", "Sent")
			f.append(t, "INBOX", readFixture(t, "plain.eml"))
			svc, store, a := newIMAPService(t, f, "secret")
			if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
			folders, err := store.Folders(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(folders))
			for _, folder := range folders {
				names = append(names, folder.Name)
			}
			if !slices.Contains(names, "INBOX") || !slices.Contains(names, "Sent") {
				t.Errorf("folders = %q, want INBOX and Sent", names)
			}
			var inbox Folder
			for _, folder := range folders {
				if folder.Name == "INBOX" {
					inbox = folder
				}
			}
			if inbox.Total != 1 {
				t.Errorf("inbox = %+v, want one message", inbox)
			}
			f.mu.Lock()
			lists := slices.Clone(f.lists)
			f.mu.Unlock()
			if len(lists) == 0 {
				t.Fatal("no LIST was asked")
			}
			list := lists[0]
			if want := c.caps.Has(imap.CapListStatus); list.status != want {
				t.Errorf("STATUS return = %v, want %v", list.status, want)
			}
			if want := c.caps.Has(imap.CapSpecialUse); list.specialUse != want {
				t.Errorf("SPECIAL-USE return = %v, want %v", list.specialUse, want)
			}
		})
	}
}

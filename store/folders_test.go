package store

import (
	"context"
	"errors"
	"testing"

	"shu/mail"
)

func TestFolderSubscriptionsPersist(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", Delimiter: "/", SpecialUse: mail.SpecialInbox, Subscribed: true},
		{Name: "News", Delimiter: "/", Subscribed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderSubscribed(ctx, "a1", "News", false); err != nil {
		t.Fatal(err)
	}
	news, ok, err := s.Folder(ctx, "a1", "News")
	if err != nil || !ok || news.Subscribed {
		t.Fatalf("News = %+v, %v, %v", news, ok, err)
	}
	// A LIST keeps what the cache knows when it says nothing new.
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "News", Delimiter: "/", Subscribed: false}}); err != nil {
		t.Fatal(err)
	}
	if news, _, _ := s.Folder(ctx, "a1", "News"); news.Subscribed {
		t.Error("a sync resubscribed the folder")
	}
	if err := s.SetFolderSubscribed(ctx, "a1", "missing", true); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestRenameFolderMovesChildren(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "Projects", Delimiter: "/"},
		{Name: "Projects/Q4", Delimiter: "/"},
		{Name: "Projects/Q4/Launch", Delimiter: "/"},
		{Name: "Personal", Delimiter: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameFolder(ctx, "a1", "Projects", "Work", "/"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Work", "Work/Q4", "Work/Q4/Launch", "Personal"} {
		if _, ok, _ := s.Folder(ctx, "a1", name); !ok {
			t.Errorf("%s is missing after the rename", name)
		}
	}
	for _, name := range []string{"Projects", "Projects/Q4"} {
		if _, ok, _ := s.Folder(ctx, "a1", name); ok {
			t.Errorf("%s is still there after the rename", name)
		}
	}
	if err := s.RenameFolder(ctx, "a1", "missing", "Other", "/"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestDeleteFolderRemovesItsMessages(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "Old", Delimiter: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(ctx, "a1", "Old", mail.Message{Summary: mail.Summary{
		UID: 1, Folder: "Old", Subject: "Gone", MessageID: "<gone@example.com>",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(ctx, "a1", "Old"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Folder(ctx, "a1", "Old"); ok {
		t.Error("the folder is still there")
	}
	if _, ok, _ := s.GetMessage(ctx, "a1", "Old", 1); ok {
		t.Error("the message is still there")
	}
	if err := s.DeleteFolder(ctx, "a1", "missing"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

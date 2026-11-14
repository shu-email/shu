package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

func TestTombstoneAndDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 2, Subject: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, true); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 0 {
		t.Errorf("tombstoned message is visible: %+v", msgs)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, false); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 1 {
		t.Errorf("restored message is hidden: %+v", msgs)
	}
	if err := s.DeleteMessage(ctx, "a1", "INBOX", 2); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetMessage(ctx, "a1", "INBOX", 2); ok {
		t.Error("deleted message remains")
	}
}

func TestSetFlagAdjustsUnread(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "INBOX", Unread: 1, Total: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3, Unread: 1, Total: 1},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 3, Subject: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 3, mail.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	f, _, _ := s.Folder(ctx, "a1", "INBOX")
	if f.Unread != 0 {
		t.Errorf("unread = %d after marking read", f.Unread)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 3, mail.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	m, _, _ := s.GetMessage(ctx, "a1", "INBOX", 3)
	if !m.Flagged || !m.Seen {
		t.Errorf("flags = %+v", m.Summary)
	}
}

func TestMessageBodyAndAttachments(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	now := time.Unix(1_700_000_000, 0)
	msg := mail.Message{
		Summary: mail.Summary{UID: 7, Subject: "Body", Date: now, Seen: true, HasAttachments: true,
			To: []mail.Address{{Email: "me@example.com"}}, References: []string{"<a@b>"}},
		BodyText: "plain body", BodyHTML: "<p>html</p>", CachedAt: now, RawHeaders: "Subject: Body\r\n",
		Attachments: []mail.Attachment{{Part: "2", Filename: "a.pdf", ContentType: "application/pdf", Size: 3, ContentHash: "h"}},
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", msg); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetMessage(ctx, "a1", "INBOX", 7)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.BodyText != "plain body" || got.BodyHTML != "<p>html</p>" || !got.BodyCached {
		t.Errorf("body = %+v", got)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Filename != "a.pdf" {
		t.Errorf("attachments = %+v", got.Attachments)
	}
	if err := s.SetAttachmentCache(ctx, "a1", "INBOX", 7, "2", "hash", "/tmp/a"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetMessage(ctx, "a1", "INBOX", 7)
	if got.Attachments[0].CachePath != "/tmp/a" || got.Attachments[0].ContentHash != "hash" {
		t.Errorf("cache path = %+v", got.Attachments[0])
	}
	// Saving the header again keeps the body.
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 7},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 7, Subject: "Body", Seen: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetMessage(ctx, "a1", "INBOX", 7)
	if got.BodyText != "plain body" || !got.BodyCached {
		t.Errorf("sync dropped body: %+v", got)
	}
}

func TestHeaderOnlyMessageHasNoBody(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 2, Subject: "header only"}}},
	}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.GetMessage(ctx, "a1", "INBOX", 2)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if m.BodyCached {
		t.Error("a header-only message reports a cached body")
	}
}

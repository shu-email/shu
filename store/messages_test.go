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

// A flag change on messages of a folder moves its unread count by the
// messages whose flag it changed, and one the cache no longer holds
// changes nothing.
func TestSetFlagsMovesTheUnreadCountByWhatChanged(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3, Unread: 2, Total: 3},
		Messages: []mail.Message{
			{Summary: mail.Summary{UID: 1, Subject: "read", Seen: true}},
			{Summary: mail.Summary{UID: 2, Subject: "unread"}},
			{Summary: mail.Summary{UID: 3, Subject: "also unread"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	unread := func() int64 {
		f, _, err := s.Folder(ctx, "a1", "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		return int64(f.Unread)
	}
	if got := unread(); got != 2 {
		t.Fatalf("unread = %d, want 2", got)
	}
	// One already read, one unread, one the cache does not hold: the
	// count moves by the one.
	if err := s.SetFlags(ctx, "a1", "INBOX", []mail.UID{1, 2, 9}, mail.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 1 {
		t.Errorf("unread = %d after marking two read, want 1", got)
	}
	// Unread again only where it was not: 3 stayed unread through the
	// change above, so only 2 moves the count back.
	if err := s.SetFlags(ctx, "a1", "INBOX", []mail.UID{2, 3}, mail.FlagSeen, false); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 2 {
		t.Errorf("unread = %d after marking two unread, want 2", got)
	}
	// A star moves no count.
	if err := s.SetFlags(ctx, "a1", "INBOX", []mail.UID{1, 2, 3}, mail.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 2 {
		t.Errorf("unread = %d after starring, want 2", got)
	}
	for _, uid := range []mail.UID{1, 2, 3} {
		m, _, err := s.GetMessage(ctx, "a1", "INBOX", uid)
		if err != nil {
			t.Fatal(err)
		}
		if !m.Flagged || m.Seen != (uid == 1) {
			t.Errorf("message %d flags = seen %v, flagged %v", uid, m.Seen, m.Flagged)
		}
	}
}

// Removing the messages a move took moves the folder's counts by what
// they were, and a message the cache no longer holds counts for nothing.
func TestDeleteMessagesMovesTheCountsByWhatGone(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3, Unread: 2, Total: 3},
		Messages: []mail.Message{
			{Summary: mail.Summary{UID: 1, Subject: "stays"}},
			{Summary: mail.Summary{UID: 2, Subject: "unread"}},
			{Summary: mail.Summary{UID: 3, Subject: "read", Seen: true}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMessages(ctx, "a1", "INBOX", []mail.UID{2, 3, 9}); err != nil {
		t.Fatal(err)
	}
	f, _, err := s.Folder(ctx, "a1", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if f.Total != 1 || f.Unread != 1 {
		t.Errorf("counts = %d total, %d unread; want 1 and 1", f.Total, f.Unread)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 1 || msgs[0].UID != 1 {
		t.Errorf("messages left = %+v", msgs)
	}
}

// A count already at zero stays there, and a message the cache does not
// hold moves nothing, through the single-message calls too.
func TestFlagsAndDeletesClampTheCountsAtZero(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	// The server says no unread mail though one message is unseen: a
	// count the cache cannot make match the rows.
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2, Unread: 0, Total: 2},
		Messages: []mail.Message{
			{Summary: mail.Summary{UID: 1, Subject: "unseen"}},
			{Summary: mail.Summary{UID: 2, Subject: "seen", Seen: true}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMessage(ctx, "a1", "INBOX", 9); err != nil {
		t.Fatalf("delete of a message the cache lacks: %v", err)
	}
	if err := s.SetFlags(ctx, "a1", "INBOX", []mail.UID{9}, mail.FlagSeen, true); err != nil {
		t.Fatalf("flags of a message the cache lacks: %v", err)
	}
	if err := s.DeleteMessages(ctx, "a1", "INBOX", []mail.UID{1, 2}); err != nil {
		t.Fatal(err)
	}
	f, _, err := s.Folder(ctx, "a1", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if f.Total != 0 || f.Unread != 0 {
		t.Errorf("counts = %d total, %d unread; want 0 and 0", f.Total, f.Unread)
	}
}

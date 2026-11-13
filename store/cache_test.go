package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

// A cache version change drops what a sync can fetch again, bodies and
// attachment rows, and keeps what the server cannot give: drafts, sent
// messages, contacts and settings. The same version runs nothing.
func TestPrepareCacheVersionsTheParsedCache(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.SaveMessage(ctx, "a1", "INBOX", mail.Message{
		Summary:  mail.Summary{UID: 1, MessageID: "one@example.com", Subject: "Plan", Preview: "the body", Date: time.Unix(1_700_000_000, 0)},
		BodyText: "the body", BodyHTML: "<p>the body</p>",
		CachedAt:    time.Unix(1_700_000_100, 0),
		Attachments: []mail.Attachment{{Part: "1", Filename: "a.pdf", ContentHash: "hash", CachePath: "/cache/attachments/hash"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "keep me", UpdatedAt: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "notifications", "on"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddContact(ctx, "Bob", "bob@example.org"); err != nil {
		t.Fatal(err)
	}
	refs, err := s.AttachmentFileRefs(ctx)
	if err != nil || len(refs) != 1 || refs[0] != "/cache/attachments/hash" {
		t.Fatalf("attachment files = %v, %v", refs, err)
	}

	cleared, err := s.PrepareCache(ctx, 2)
	if err != nil || !cleared {
		t.Fatalf("PrepareCache = %v, %v; want it to clear", cleared, err)
	}
	rows, err := s.query(ctx, `SELECT COALESCE(body_text, ''), COALESCE(body_html, ''), body_truncated,
			cached_at, subject FROM messages WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if r := rows.Values[0]; text(r[0]) != "" || text(r[1]) != "" || truth(r[2]) || r[3] != nil || text(r[4]) != "Plan" {
		t.Errorf("message after the cache change = %v, want its body fields cleared and its header kept", r)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM attachments`); got != 0 {
		t.Errorf("attachments after the cache change = %d, want none", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM drafts WHERE id = 'd1' AND subject = 'keep me'`); got != 1 {
		t.Error("the draft did not survive the cache change")
	}
	if v, ok, err := s.Setting(ctx, "notifications"); err != nil || !ok || v != "on" {
		t.Errorf("setting after the cache change = %q, %v, %v", v, ok, err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM contacts WHERE key = 'bob@example.org'`); got != 1 {
		t.Error("the contact did not survive the cache change")
	}
	if refs, err := s.AttachmentFileRefs(ctx); err != nil || len(refs) != 0 {
		t.Errorf("attachment files after the cache change = %v, %v", refs, err)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		t.Errorf("search index after the cache change: %v", err)
	}

	if cleared, err := s.PrepareCache(ctx, 2); err != nil || cleared {
		t.Errorf("the same version cleared again: %v, %v", cleared, err)
	}
	if cleared, err := s.PrepareCache(ctx, 3); err != nil || !cleared {
		t.Errorf("a newer version = %v, %v; want it to clear", cleared, err)
	}
}

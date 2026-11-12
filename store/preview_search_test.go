package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// A sync stores headers and a preview, not the body. Search finds a
// message, and its conversation, by the start of its body all the same:
// a reply in Sent that was never opened is found by what it says.
func TestSearchFindsMailByItsPreview(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Sent")
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{
		UID: 1, MessageID: "root@example.com", Subject: "Plan", Preview: "Here is the plan for March.",
	}})
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{
		UID: 1, MessageID: "reply@example.com", InReplyTo: "root@example.com", Subject: "Re: Plan",
		Preview: "The quarterly budget looks right to me. 下周的预算安排也可以。",
	}})

	for _, q := range []string{"quarterly budget", "预算"} {
		got, err := s.ListMessages(ctx, "a1", "", q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Folder != "Sent" {
			t.Errorf("messages matching %q = %+v", q, got)
		}
		convs, err := s.ListConversations(ctx, "a1", "INBOX", q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(convs) != 1 || convs[0].Latest.MessageID != "root@example.com" {
			t.Errorf("Inbox conversations matching %q = %+v", q, convs)
		}
	}

	// A later sync that changes the preview, and the body a fetch stores,
	// keep the index in step with the row.
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{
		UID: 1, MessageID: "reply@example.com", InReplyTo: "root@example.com", Subject: "Re: Plan",
		Preview: "Something else entirely.",
	}})
	if got, _ := s.ListMessages(ctx, "a1", "", "quarterly", 10); len(got) != 0 {
		t.Errorf("the old preview is still found: %+v", got)
	}
	if got, _ := s.ListMessages(ctx, "a1", "", "else entirely", 10); len(got) != 1 {
		t.Errorf("the new preview is not found: %+v", got)
	}
	if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("full-text index: %v", err)
	}
}

// A cache made before previews were indexed has its index rebuilt by the
// upgrade, so the rows already there are found by their previews too.
func TestMigrationIndexesPreviewsOfAnOlderDatabase(t *testing.T) {
	if err := sqlite.Load(); err != nil {
		t.Skipf("sqlite native module is not ready (%v); run go tool mygo generate", err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 6; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name) VALUES (1, 'a1', 'INBOX')`,
		`INSERT INTO threads (id, account_id) VALUES (1, 'a1'), (2, 'a1')`,
		`INSERT INTO messages (id, folder_id, server_uid, message_id, subject, preview, body_text, thread_id)
			VALUES (1, 1, 1, 'one@example.com', 'Budget', 'The quarterly numbers are attached.', NULL, 1),
			(2, 1, 2, 'two@example.com', 'Lunch', 'Lunch on Friday?', 'Lunch on Friday? We could try the new place.', 2)`,
		"PRAGMA user_version=6",
	} {
		if _, err := db.Execute(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	for q, want := range map[string]mail.UID{"quarterly numbers": 1, "new place": 2, "Lunch": 2} {
		got, err := s.ListMessages(ctx, "a1", "INBOX", q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].UID != want {
			t.Errorf("after the upgrade, %q found %+v, want UID %d", q, got, want)
		}
	}
	if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("full-text index: %v", err)
	}
}

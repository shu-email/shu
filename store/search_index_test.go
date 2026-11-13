package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

// found searches a folder for a text and returns the subjects it found.
func found(t *testing.T, s *Store, folder string, text string) []string {
	t.Helper()
	page, err := s.MessagePage(context.Background(), mail.ListQuery{
		Scope:  mail.AccountScope("a1", folder),
		Search: mail.Search{Text: []string{text}},
	})
	if err != nil {
		t.Fatalf("search %q: %v", text, err)
	}
	out := make([]string, 0, len(page.Messages))
	for _, m := range page.Messages {
		out = append(out, m.Subject)
	}
	return out
}

// The index follows every write: a new message, a body saved later, a
// changed subject, a flag (which does not touch the index), a move and a
// delete. Chinese and two-character terms keep working, and the drift
// check stays quiet.
func TestSearchIndexFollowsWrites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Archive")
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{
		UID: 1, MessageID: "budget@example.com", Subject: "Budget 预算", Preview: "Numbers attached",
		Date: time.Unix(1_700_000_000, 0)}},
	)
	if got := found(t, s, "INBOX", "budget"); len(got) != 1 {
		t.Fatalf("search of the new message = %v", got)
	}
	// Two Chinese characters take the LIKE fallback, not the trigram.
	if got := found(t, s, "INBOX", "预算"); len(got) != 1 {
		t.Fatalf("search of two Chinese characters = %v", got)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}

	// A body fetched later joins the index in the same write.
	m, _, err := s.GetMessage(ctx, "a1", "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	m.BodyText, m.CachedAt = "The quarterly figures", time.Unix(1_700_000_100, 0)
	if err := s.SaveMessage(ctx, "a1", "INBOX", m); err != nil {
		t.Fatal(err)
	}
	if got := found(t, s, "INBOX", "quarterly"); len(got) != 1 {
		t.Fatalf("search of the saved body = %v", got)
	}

	// A subject that changes is searchable by the new text only.
	m.Subject = "Forecast 预算"
	m.CachedAt = time.Unix(1_700_000_200, 0)
	if err := s.SaveMessage(ctx, "a1", "INBOX", m); err != nil {
		t.Fatal(err)
	}
	if got := found(t, s, "INBOX", "Forecast"); len(got) != 1 {
		t.Fatalf("search of the new subject = %v", got)
	}
	if got := found(t, s, "INBOX", "Budget"); len(got) != 0 {
		t.Errorf("search of the old subject = %v", got)
	}

	// A flag is no indexed column: it leaves the index as it was.
	if err := s.SetFlag(ctx, "a1", "INBOX", 1, mail.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}

	// The message goes, and its terms with it.
	if err := s.SetFlag(ctx, "a1", "INBOX", 1, mail.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMessage(ctx, "a1", "INBOX", 1); err != nil {
		t.Fatal(err)
	}
	if got := found(t, s, "INBOX", "quarterly"); len(got) != 0 {
		t.Errorf("search of a deleted message = %v", got)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		t.Fatal(err)
	}
}

// The drift check finds an index that a write skipped: a row missing, a
// row left behind or a row whose columns changed. Rebuilding the index
// mends it.
func TestSearchIndexCheckFindsDrift(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// drift changes messages without the triggers.
		drift string
	}{
		{"a message without an index row", `INSERT INTO messages (folder_id, server_uid, subject, date, thread_id)
			VALUES (1, 2, 'stray@example.com', 1, 1)`},
		{"an index row without a message", `DELETE FROM messages WHERE id = 1`},
		{"a row whose subject changed", `UPDATE messages SET subject = 'changed' WHERE id = 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			addAccount(t, s, "a1")
			addFolders(t, s, "a1", "INBOX")
			syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{
				UID: 1, MessageID: "one@example.com", Subject: "original", Preview: "preview", Date: time.Unix(1_700_000_000, 0)}},
			)
			if err := s.CheckSearchIndex(ctx); err != nil {
				t.Fatalf("a healthy database: %v", err)
			}
			for _, q := range []string{`DROP TRIGGER messages_ai`, `DROP TRIGGER messages_ad`, `DROP TRIGGER messages_au`} {
				if _, err := s.exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.exec(ctx, tc.drift); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckSearchIndex(ctx); err == nil {
				t.Fatal("the check passed with a drifted index")
			}
			if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`); err != nil {
				t.Fatal(err)
			}
			if err := s.CheckSearchIndex(ctx); err != nil {
				t.Fatalf("the rebuilt index: %v", err)
			}
		})
	}
}

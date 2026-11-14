package store

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"shu/mail"
)

// cacheFindings runs the check and returns what it found, by kind.
func cacheFindings(t *testing.T, s *Store) []mail.IntegrityFinding {
	t.Helper()
	findings, err := s.CheckCache(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return findings
}

// integrityFixture is an account with a folder and two messages, one of
// them a conversation of its own.
func integrityFixture(t *testing.T, s *Store) {
	t.Helper()
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Archive")
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{
			UID: 1, MessageID: "one@example.com", Subject: "One", Preview: "first",
			Date: time.Unix(1_700_000_000, 0)}},
		mail.Message{Summary: mail.Summary{
			UID: 2, MessageID: "two@example.com", Subject: "Two", Preview: "second",
			Date: time.Unix(1_700_000_100, 0), Seen: true}},
	)
}

func TestCheckCacheFindsNothingInAHealthyDatabase(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	if got := cacheFindings(t, s); len(got) != 0 {
		t.Fatalf("a healthy database reports %+v", got)
	}
}

// A conversation whose totals are not its messages' is reported, with the
// conversation named.
func TestCheckCacheFindsThreadAggregateDrift(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	ctx := context.Background()
	if _, err := s.exec(ctx, `UPDATE threads SET message_count = 7, unread_count = 7`); err != nil {
		t.Fatal(err)
	}
	findings := cacheFindings(t, s)
	var drift mail.IntegrityFinding
	for _, f := range findings {
		if f.Kind == mail.IntegrityThreads {
			drift = f
		}
	}
	if drift.Count != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	threads, err := s.query(ctx, `SELECT id FROM threads ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, r := range threads.Values {
		want = append(want, text(r[0]))
	}
	if !slices.Equal(drift.IDs, want) {
		t.Errorf("drift names %v, want %v", drift.IDs, want)
	}
}

// A folder's totals that are gone are reported too, as is a row that
// should be there.
func TestCheckCacheFindsThreadFolderDrift(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	ctx := context.Background()
	if _, err := s.exec(ctx, `DELETE FROM thread_folders`); err != nil {
		t.Fatal(err)
	}
	findings := cacheFindings(t, s)
	if len(findings) != 1 || findings[0].Kind != mail.IntegrityThreads || findings[0].Count != 2 {
		t.Fatalf("findings = %+v", findings)
	}
}

// A full-text index a write skipped is reported as the search index.
func TestCheckCacheFindsSearchIndexDrift(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	ctx := context.Background()
	if _, err := s.exec(ctx, `DROP TRIGGER messages_ai`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, `INSERT INTO messages (folder_id, server_uid, subject, date, thread_id)
		VALUES (1, 3, 'unindexed', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	findings := cacheFindings(t, s)
	if len(findings) != 1 || findings[0].Kind != mail.IntegritySearchIndex {
		t.Fatalf("findings = %+v", findings)
	}
}

// A row that points at a row which is not there is reported by table and
// row, and no message content is in the finding.
func TestCheckCacheFindsDanglingReferences(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	ctx := context.Background()
	if _, err := s.exec(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, `INSERT INTO attachments (message_id, part, content_hash)
		VALUES (999, '1', 'hash')`); err != nil {
		t.Fatal(err)
	}
	findings := cacheFindings(t, s)
	if len(findings) != 1 || findings[0].Kind != mail.IntegrityReferences || findings[0].Count != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if len(findings[0].IDs) != 1 || findings[0].IDs[0] != "attachments/1" {
		t.Errorf("the finding names %v", findings[0].IDs)
	}
}

// A table WITHOUT ROWID has no row to name, so its dangling rows are
// reported by table and counted.
func TestCheckCacheNamesRowsWithoutRowidByTable(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	ctx := context.Background()
	if _, err := s.exec(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, `INSERT INTO thread_folders (thread_id, folder_id) VALUES (999, 1)`); err != nil {
		t.Fatal(err)
	}
	var refs mail.IntegrityFinding
	for _, f := range cacheFindings(t, s) {
		if f.Kind == mail.IntegrityReferences {
			refs = f
		}
	}
	if refs.Count != 1 || !slices.Equal(refs.IDs, []string{"thread_folders"}) {
		t.Errorf("the finding is %+v", refs)
	}
}

// A database whose version is not this build's is reported as the schema.
func TestCheckCacheFindsSchemaVersionDrift(t *testing.T) {
	s := openTest(t)
	integrityFixture(t, s)
	if _, err := s.exec(context.Background(), `PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	findings := cacheFindings(t, s)
	if len(findings) != 1 || findings[0].Kind != mail.IntegritySchema || findings[0].Count != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if want := []string{"1", strconv.Itoa(SchemaVersion)}; !slices.Equal(findings[0].IDs, want) {
		t.Errorf("the finding names %v, want %v", findings[0].IDs, want)
	}
}

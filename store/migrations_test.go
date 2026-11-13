package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// baselineMessageID is the Message-ID the fixture of every version carries.
// See docs/schema-migrations.md for the checklist this file proves.
const baselineMessageID = "baseline@example.com"

// createAtVersion writes the database an installation of an older release
// leaves: the schema its migrations produced, one account, one folder and
// one message, with the columns that version has. The migrations are the
// fixtures; they are forward only, so migrations[0:v] is exactly the schema
// of a database at version v, and no real mailbox is checked in.
func createAtVersion(t *testing.T, ctx context.Context, path string, v int) {
	t.Helper()
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < v; i++ {
		for _, q := range migrations[i] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("schema v%d: %v", i+1, err)
			}
		}
	}
	for _, q := range seedAtVersion(v) {
		if _, err := db.Execute(ctx, q); err != nil {
			t.Fatalf("seeding schema v%d: %v", v, err)
		}
	}
	if _, err := db.Execute(ctx, fmt.Sprintf("PRAGMA user_version=%d", v)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedAtVersion is the data an older installation holds, using only the
// columns its version has. It grows with the schema, not with the releases:
// a version gets the rows its tables allow, and the invariants after the
// migration are what the data must still meet.
func seedAtVersion(v int) []string {
	if v == 0 {
		return nil
	}
	stmts := []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann@example.com',
				'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid)
			VALUES (1, 'a1', 'INBOX', '\Inbox', 5, 42)`,
	}
	columns := `id, folder_id, server_uid, message_id, from_name, from_email, subject, preview, date`
	values := `1, 1, 42, 'baseline@example.com', 'Bob', 'bob@example.org',
		'Migration baseline', 'baseline needle', 1700000000`
	if v >= 6 {
		// A message is written with a conversation since v6, and a real
		// database has the thread row too.
		stmts = append(stmts, `INSERT INTO threads (id, account_id) VALUES (1, 'a1')`)
		columns += `, thread_id`
		values += `, 1`
	}
	if v >= 12 {
		// Written by a sync since v12: the key follows the message.
		columns += `, msg_key`
		values += `, 'baseline@example.com'`
	}
	return append(stmts, fmt.Sprintf(`INSERT INTO messages (%s) VALUES (%s)`, columns, values))
}

// schemaOf lists the schema as SQLite keeps it, objects sorted so that two
// databases compare whatever order they were built in.
func schemaOf(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.query(context.Background(), `SELECT type, name, tbl_name, COALESCE(sql, '')
		FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, fmt.Sprintf("%s %s %s %s", text(r[0]), text(r[1]), text(r[2]), text(r[3])))
	}
	return out
}

// sameSchema compares a migrated schema with a fresh one, reporting the
// objects that differ rather than the whole lists.
func sameSchema(t *testing.T, migrated, fresh []string) {
	t.Helper()
	if slices.Equal(migrated, fresh) {
		return
	}
	for _, line := range migrated {
		if !slices.Contains(fresh, line) {
			t.Errorf("migrated schema has %q, a fresh one does not", line)
		}
	}
	for _, line := range fresh {
		if !slices.Contains(migrated, line) {
			t.Errorf("a fresh schema has %q, the migrated one does not", line)
		}
	}
}

// checkUpgradedData proves the fixture survived the upgrade: the account,
// the folder and the message are as they were, the message is threaded and
// keyed, search finds it, and the book counted its sender when the version
// that made the book migrated.
func checkUpgradedData(t *testing.T, s *Store, v int) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.query(ctx, `SELECT server_uid, subject, thread_id, COALESCE(msg_key, '')
		FROM messages WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 {
		t.Fatalf("v%d: %d messages after the migration, want 1", v, len(rows.Values))
	}
	r := rows.Values[0]
	if uid, subject := number(r[0]), text(r[1]); uid != 42 || subject != "Migration baseline" {
		t.Errorf("v%d: message = UID %d %q, want UID 42 %q", v, uid, subject, "Migration baseline")
	}
	if number(r[2]) == 0 {
		t.Errorf("v%d: message has no conversation after the migration", v)
	}
	if key := text(r[3]); key != baselineMessageID {
		t.Errorf("v%d: message key = %q, want %q", v, key, baselineMessageID)
	}
	// The conversation's aggregates were computed from the message.
	rows, err = s.query(ctx, `SELECT t.message_count, t.unread_count, t.snippet,
			(SELECT COUNT(*) FROM thread_folders tf WHERE tf.thread_id = t.id)
		FROM threads t JOIN messages m ON m.thread_id = t.id WHERE m.id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 {
		t.Fatalf("v%d: the message has no conversation aggregate", v)
	}
	if a := rows.Values[0]; number(a[0]) != 1 || number(a[1]) != 1 || text(a[2]) != "baseline needle" || number(a[3]) != 1 {
		t.Errorf("v%d: conversation aggregate = %v, want 1 message, 1 unread, its preview and its folder", v, a)
	}
	if a, ok, err := s.Account(ctx, "a1"); err != nil || !ok || a.Email != "ann@example.com" {
		t.Errorf("v%d: account = %+v, %v, %v", v, a, ok, err)
	}
	page, err := s.MessagePage(ctx, mail.ListQuery{
		Scope:  mail.AccountScope("a1", "INBOX"),
		Search: mail.Search{Text: []string{"baseline"}},
	})
	if err != nil {
		t.Fatalf("v%d: search after the migration: %v", v, err)
	}
	if len(page.Messages) != 1 || page.Messages[0].MessageID != baselineMessageID {
		t.Errorf("v%d: search found %+v, want the fixture", v, page.Messages)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		t.Errorf("v%d: %v", v, err)
	}
	// The address book was made from the mail already cached, once.
	if v < 11 {
		rows, err := s.query(ctx, `SELECT COUNT(*) FROM contacts WHERE key = 'bob@example.org'`)
		if err != nil {
			t.Fatal(err)
		}
		if got := number(rows.Values[0][0]); got != 1 {
			t.Errorf("v%d: %d contacts for the fixture's sender after the migration, want 1", v, got)
		}
	}
}

// Every schema ever shipped upgrades to the current one, with the same
// schema a fresh install has and its data intact. The empty database and
// the fresh install are the v0 case.
func TestMigrationsFromEveryVersion(t *testing.T) {
	sqlitetest.Require(t)
	fresh, err := InMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	want := schemaOf(t, fresh)

	for v := 0; v < SchemaVersion; v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "shu.db")
			createAtVersion(t, ctx, path, v)

			s, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("upgrading v%d: %v", v, err)
			}
			got := schemaOf(t, s)
			rows, err := s.query(ctx, "PRAGMA user_version")
			if err != nil {
				t.Fatal(err)
			}
			if version := int(number(rows.Values[0][0])); version != SchemaVersion {
				t.Errorf("user_version = %d after the migration, want %d", version, SchemaVersion)
			}
			sameSchema(t, got, want)
			if v > 0 {
				checkUpgradedData(t, s, v)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// Opening it again changes nothing: the migrations already ran.
			again, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("opening v%d a second time: %v", v, err)
			}
			defer again.Close()
			sameSchema(t, schemaOf(t, again), got)
			if v > 0 {
				checkUpgradedData(t, again, v)
			}
		})
	}
}

// A migration that fails leaves the database at the version before it, with
// nothing of its statements and no half-written row, and the next Open can
// run it once the cause is gone.
func TestFailedMigrationRollsBack(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	createAtVersion(t, ctx, path, 12)

	// Make the migration to v13 fail on its first statement: the table it
	// creates is already there.
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE rules (id INTEGER)`,
		`INSERT INTO rules (id) VALUES (7)`,
	} {
		if _, err := db.Execute(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if s, err := Open(ctx, path); err == nil {
		s.Close()
		t.Fatal("Open succeeded on a database whose migration cannot run")
	} else if !strings.Contains(err.Error(), "migration to v13") {
		t.Fatalf("Open error = %v, want it to name the migration to v13", err)
	}

	db, err = sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, "PRAGMA user_version")
	if err != nil {
		t.Fatal(err)
	}
	if got := number(rows.Values[0][0]); got != 12 {
		t.Fatalf("user_version = %d after the failed migration, want 12", got)
	}
	rows, err = db.Query(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('rules', 'rule_log', 'expected_arrivals') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for _, r := range rows.Values {
		tables = append(tables, text(r[0]))
	}
	if !slices.Equal(tables, []string{"rules"}) {
		t.Errorf("tables after the failed migration = %v, want only the conflicting rules table", tables)
	}
	rows, err = db.Query(ctx, `SELECT id FROM rules`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 || number(rows.Values[0][0]) != 7 {
		t.Errorf("the conflicting table = %v, want its row kept", rows.Values)
	}
	rows, err = db.Query(ctx, `SELECT COUNT(*) FROM pragma_table_info('folders') WHERE name = 'subscribed'`)
	if err != nil {
		t.Fatal(err)
	}
	if number(rows.Values[0][0]) != 0 {
		t.Error("a later migration ran after the failed one")
	}
	rows, err = db.Query(ctx, `SELECT COUNT(*) FROM accounts WHERE id = 'a1'`)
	if err != nil {
		t.Fatal(err)
	}
	if number(rows.Values[0][0]) != 1 {
		t.Error("the account is gone after the failed migration")
	}

	// Once the conflict is gone, the same database upgrades.
	if _, err := db.Execute(ctx, `DROP TABLE rules`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrading after the cause was removed: %v", err)
	}
	defer s.Close()
	rows, err = s.query(ctx, "PRAGMA user_version")
	if err != nil {
		t.Fatal(err)
	}
	if got := number(rows.Values[0][0]); got != int64(SchemaVersion) {
		t.Errorf("user_version = %d, want %d", got, SchemaVersion)
	}
	checkUpgradedData(t, s, 12)
}

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// baselineMessageID is the Message-ID the fixture of every version carries,
// and baselineHeaders the header lines it kept, which say of its sender and
// its list. See docs/schema-migrations.md for the checklist this file proves.
const (
	baselineMessageID = "baseline@example.com"
	baselineHeaders   = "Authentication-Results: mx.example; spf=pass; dkim=pass; dmarc=pass header.from=example.org\r\n" +
		"List-Unsubscribe: <https://example.org/unsub?id=1>"
)

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
	// The header lines a fetch kept, which migration v22 reads for what
	// they say of the message's sender and its list.
	columns += `, raw_headers`
	values += `, '` + baselineHeaders + `'`
	if v >= 22 {
		// What v22's backfill read of them, as an installation of that
		// version holds it.
		columns += `, security`
		values += `, '` + encodeJSON(mail.ParseSecurity(baselineHeaders)) + `'`
	}
	// An outbox entry sent by an older build, which kept the raw message
	// it delivered with until v25 dropped it; an installation at v25 or
	// later holds it without.
	raw := "'Subject: old'"
	if v >= 25 {
		raw = "''"
	}
	stmts = append(stmts, fmt.Sprintf(`INSERT INTO outbox (id, account_id, raw, envelope_json, state, attempts, created_at)
		VALUES ('o1', 'a1', %s, '{"to":[{"email":"bob@example.org"}]}', 'sent', 1, 1700000000)`, raw))
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
	// What the cached headers said of the message was read once the column
	// that keeps it existed, so a read has it.
	if msg, ok, err := s.GetMessage(ctx, "a1", "INBOX", 42); err != nil || !ok {
		t.Errorf("v%d: the message read = %v, %v", v, ok, err)
	} else if msg.Security == nil {
		t.Errorf("v%d: the headers kept said nothing of the message after the migration", v)
	} else if msg.Security.Trust.Verdict != mail.AuthPassed || msg.Security.ListUnsubscribe == "" {
		t.Errorf("v%d: security = %+v, want a pass and its list", v, msg.Security)
	}
	// An entry that had been sent kept no raw message once v25 ran.
	rows, err = s.query(ctx, `SELECT raw, state FROM outbox WHERE id = 'o1'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 {
		t.Fatalf("v%d: %d outbox entries after the migration, want 1", v, len(rows.Values))
	}
	if raw, state := text(rows.Values[0][0]), text(rows.Values[0][1]); raw != "" || state != "sent" {
		t.Errorf("v%d: sent entry = %q bytes in state %q, want no bytes and its state", v, raw, state)
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

func TestMigrationsFromEmpty(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	rows, err := s.query(ctx, "PRAGMA user_version")
	if err != nil {
		t.Fatal(err)
	}
	if got := number(rows.Values[0][0]); got != int64(SchemaVersion) {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	rows, err = s.query(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'index', 'trigger')`)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, r := range rows.Values {
		have[text(r[0])] = true
	}
	// Each of these was kept on every write of a message or a folder, and
	// no query ever read it (see v26).
	for _, name := range []string{"messages_message_id", "folders_account"} {
		if have[name] {
			t.Errorf("index %q kept though nothing reads it", name)
		}
	}
	for _, name := range []string{
		"accounts", "kv", "folders", "messages", "attachments", "outbox",
		"messages_fts", "messages_list",
		"attachments_cache_path", "outbox_state_due",
		"messages_ai", "messages_ad", "messages_au", "outbox_account",
		"ai_cache", "ai_cache_account",
		"drafts", "drafts_account",
		"draft_attachments",
		"watermarks",
		"threads", "threads_account", "thread_refs", "thread_refs_thread", "messages_thread",
		"messages_thread_insert", "messages_thread_update",
		"messages_date", "messages_flagged",
		"identities", "identities_email",
		"contacts", "contacts_rank", "contact_terms", "contact_terms_key", "contact_messages",
		"labels", "labels_name", "labels_keyword", "message_labels", "message_labels_label",
		"local_labels", "local_labels_key", "messages_key",
		"reminders", "reminders_account", "reminders_due",
		"follow_up_dismissals", "follow_up_dismissals_thread",
		"smart_mailboxes", "smart_mailboxes_account",
		"discoveries",
		"rules", "rules_account", "rule_log", "rule_log_rule", "rule_log_account", "rule_log_message",
		"expected_arrivals", "expected_arrivals_key",
		"thread_folders", "thread_folders_folder",
		"pending_operations", "pending_operations_account",
		"unsubscribes", "blocked_senders",
	} {
		if !have[name] {
			t.Errorf("missing schema object %q", name)
		}
	}
	for _, column := range [][2]string{
		{"accounts", "oauth"}, {"drafts", "identity"}, {"outbox", "due_at"}, {"outbox", "draft_id"},
		{"messages", "msg_key"}, {"folders", "permanent_flags"},
	} {
		if !hasColumn(t, s, column[0], column[1]) {
			t.Errorf("%s has no %s column", column[0], column[1])
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening the file runs no migration and keeps the data.
	path := t.TempDir() + "/shu.db"
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	addAccount(t, db, "a1")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		accounts, err := db.Accounts(ctx)
		if err != nil || len(accounts) != 1 || accounts[0].ID != "a1" {
			t.Fatalf("reopen %d accounts = %v, %v", i, accounts, err)
		}
		db.Close()
	}
}

// A database from before drafts gains the table and keeps its data.
func TestMigrationAddsDraftsToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := t.TempDir() + "/shu.db"
	createAtVersion(t, ctx, path, 2)

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.query(ctx, "PRAGMA user_version")
	if err != nil || number(rows.Values[0][0]) != int64(SchemaVersion) {
		t.Fatalf("user_version = %v, %v", rows.Values, err)
	}
	if accounts, err := s.Accounts(ctx); err != nil || len(accounts) != 1 {
		t.Fatalf("accounts after the migration = %v, %v", accounts, err)
	}
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "x", UpdatedAt: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("draft in a migrated database: %v", err)
	}
}

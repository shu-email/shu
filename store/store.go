// Package store is Shu's SQLite cache. It implements mail.Store with the
// schema documented in docs/core-spec.md and imports no UI code.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// Store is the SQLite connection and the mail cache on it.
type Store struct {
	db *sqlite.DB
	// threadMu makes each write of messages one step with the reads
	// that put them in conversations: the watcher of every account,
	// window syncs and body fetches write at once, and two that read
	// the same keys must not each make a conversation of them.
	threadMu sync.Mutex
}

// stmtType is one statement of a transaction.
type stmtType = sqlite.Statement

// migrations are applied in order, each in a transaction, keyed on
// PRAGMA user_version. They are forward only.
var migrations = [][]string{
	// v1: accounts, settings, folders, messages, attachments, outbox and
	// the full-text index.
	{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, username TEXT NOT NULL,
			imap_host TEXT NOT NULL, imap_port INTEGER NOT NULL, imap_tls TEXT NOT NULL,
			smtp_host TEXT NOT NULL, smtp_port INTEGER NOT NULL, smtp_tls TEXT NOT NULL)`,
		`CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE folders (
			id INTEGER PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			name TEXT NOT NULL, delimiter TEXT, special_use TEXT,
			unread INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0,
			uidvalidity INTEGER NOT NULL DEFAULT 0, last_uid INTEGER NOT NULL DEFAULT 0,
			synced_at INTEGER,
			UNIQUE (account_id, name))`,
		`CREATE INDEX folders_account ON folders (account_id)`,
		`CREATE TABLE messages (
			id INTEGER PRIMARY KEY,
			folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
			server_uid INTEGER NOT NULL,
			message_id TEXT, in_reply_to TEXT, references_json TEXT,
			from_name TEXT, from_email TEXT,
			to_json TEXT, cc_json TEXT, reply_to_json TEXT,
			subject TEXT, preview TEXT, date INTEGER, size INTEGER,
			seen INTEGER NOT NULL DEFAULT 0, flagged INTEGER NOT NULL DEFAULT 0,
			has_attachments INTEGER NOT NULL DEFAULT 0,
			body_text TEXT, body_html TEXT, body_truncated INTEGER NOT NULL DEFAULT 0,
			raw_headers TEXT, cached_at INTEGER,
			deleted INTEGER NOT NULL DEFAULT 0,
			UNIQUE (folder_id, server_uid))`,
		`CREATE INDEX messages_list ON messages (folder_id, date DESC)`,
		`CREATE INDEX messages_message_id ON messages (message_id) WHERE message_id IS NOT NULL`,
		`CREATE TABLE attachments (
			message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			part TEXT NOT NULL,
			filename TEXT, content_type TEXT, size INTEGER,
			content_hash TEXT, cache_path TEXT,
			PRIMARY KEY (message_id, part))`,
		`CREATE TABLE outbox (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			raw TEXT NOT NULL, envelope_json TEXT NOT NULL,
			state TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT,
			created_at INTEGER NOT NULL, sent_at INTEGER)`,
		`CREATE INDEX outbox_account ON outbox (account_id, created_at)`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(
			subject, from_name, from_email, body_text,
			content='messages', content_rowid='id', tokenize='trigram')`,
		`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text);
		END`,
		`CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text);
		END`,
		`CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text);
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text);
		END`,
	},
	// v2: the cache of AI answers. The key is a hash of the request that
	// produced the answer; the row goes when its account does. API keys
	// are not here or anywhere in the database: they stay in the keychain.
	{
		`CREATE TABLE ai_cache (
			key TEXT PRIMARY KEY,
			account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,
			feature TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
			result TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX ai_cache_account ON ai_cache (account_id)`,
	},
	// v3: the drafts being written. The recipient fields are the text as
	// typed. revision counts the saves and synced_revision is the one the
	// server copy holds, at server_folder/server_uid under
	// server_uidvalidity; pending_delete keeps the row of a draft
	// discarded or sent until its server copy is removed.
	{
		`CREATE TABLE drafts (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			to_text TEXT NOT NULL DEFAULT '', cc_text TEXT NOT NULL DEFAULT '',
			bcc_text TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '',
			in_reply_to TEXT NOT NULL DEFAULT '', references_json TEXT NOT NULL DEFAULT '',
			message_id TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			revision INTEGER NOT NULL DEFAULT 1,
			synced_revision INTEGER NOT NULL DEFAULT 0,
			server_folder TEXT NOT NULL DEFAULT '',
			server_uid INTEGER NOT NULL DEFAULT 0,
			server_uidvalidity INTEGER NOT NULL DEFAULT 0,
			pending_delete INTEGER NOT NULL DEFAULT 0,
			conflict INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX drafts_account ON drafts (account_id, updated_at DESC)`,
	},
	// v4: the files attached to a draft, in the order they were added.
	// Their bytes are no part of the database: content_hash names the
	// file that holds them, which several drafts may share.
	{
		`CREATE TABLE draft_attachments (
			draft_id TEXT NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			filename TEXT NOT NULL DEFAULT '', content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0, content_hash TEXT NOT NULL,
			PRIMARY KEY (draft_id, position))`,
	},
	// v5: the watermark of a folder watched for new mail: the highest UID
	// that was announced, under the UIDVALIDITY it belongs to. A message
	// is announced once, so the row is kept apart from the sync cursor,
	// which every sync moves, and goes only with its folder.
	{
		`CREATE TABLE watermarks (
			folder_id INTEGER PRIMARY KEY REFERENCES folders(id) ON DELETE CASCADE,
			uidvalidity INTEGER NOT NULL, uid INTEGER NOT NULL)`,
	},
	// v6: conversations. thread_id is a message's, never null once
	// written. threads hands out their IDs, which only grow, so that an
	// ID is never another conversation's. thread_refs maps each key an
	// account's messages were threaded by (see mail.ThreadKeys) to its
	// conversation, and stays when the messages go: mail that comes
	// later still finds it. The update trigger of the full-text index
	// now fires only for the columns it indexes, so that renumbering a
	// merged conversation, like setting a flag, leaves the index alone.
	// backfillThreads threads the rows already there.
	{
		`ALTER TABLE messages ADD COLUMN thread_id INTEGER`,
		`ALTER TABLE messages ADD COLUMN thread_index TEXT`,
		`CREATE INDEX messages_thread ON messages (thread_id, date)`,
		`CREATE TABLE threads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE)`,
		`CREATE INDEX threads_account ON threads (account_id)`,
		`CREATE TABLE thread_refs (
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			thread_id INTEGER NOT NULL,
			PRIMARY KEY (account_id, key)) WITHOUT ROWID`,
		`CREATE INDEX thread_refs_thread ON thread_refs (thread_id)`,
		`DROP TRIGGER messages_au`,
		`CREATE TRIGGER messages_au AFTER UPDATE OF subject, from_name, from_email, body_text ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text);
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text);
		END`,
		`CREATE TRIGGER messages_thread_insert BEFORE INSERT ON messages WHEN new.thread_id IS NULL BEGIN
			SELECT RAISE(ABORT, 'store: a message without a conversation');
		END`,
		`CREATE TRIGGER messages_thread_update BEFORE UPDATE OF thread_id ON messages WHEN new.thread_id IS NULL BEGIN
			SELECT RAISE(ABORT, 'store: a message without a conversation');
		END`,
	},
	// v7: the full-text index also holds each message's preview, which a
	// sync stores with the headers, so that search finds a message by the
	// start of its body before the body was ever downloaded. The index is
	// made anew with the column and rebuilt from the rows.
	{
		`DROP TRIGGER messages_ai`,
		`DROP TRIGGER messages_ad`,
		`DROP TRIGGER messages_au`,
		`DROP TABLE messages_fts`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(
			subject, from_name, from_email, body_text, preview,
			content='messages', content_rowid='id', tokenize='trigram')`,
		`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text, preview)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text, new.preview);
		END`,
		`CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text, preview)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text, old.preview);
		END`,
		`CREATE TRIGGER messages_au AFTER UPDATE OF subject, from_name, from_email, body_text, preview ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text, preview)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text, old.preview);
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text, preview)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text, new.preview);
		END`,
		`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`,
	},
}

// backfills compute, from the rows of the version before, statements a
// migration runs in its transaction after its own, keyed by the version
// it migrates to.
var backfills = map[int]func(context.Context, *Store) ([]sqlite.Statement, error){
	6: backfillThreads,
}

// SchemaVersion is the version Open migrates to.
var SchemaVersion = len(migrations)

// Open opens (and creates) the database at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := sqlite.Load(); err != nil {
		return nil, fmt.Errorf("store: sqlite native module: %w (run go tool mygo generate)", err)
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{BusyTimeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// InMemory opens a throwaway database for tests.
func InMemory(ctx context.Context) (*Store, error) { return Open(ctx, ":memory:") }

// Close closes the connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init(ctx context.Context) error {
	// WAL keeps readers working while a sync writes; in-memory databases
	// spell it "memory" and cannot change it, which is fine.
	if _, err := s.db.Query(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("store: journal mode: %w", err)
	}
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := s.db.Execute(ctx, pragma); err != nil {
			return fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	rows, err := s.db.Query(ctx, "PRAGMA user_version")
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	var version int
	if len(rows.Values) == 1 && len(rows.Values[0]) == 1 {
		version = int(number(rows.Values[0][0]))
	}
	if version > SchemaVersion {
		return fmt.Errorf("store: database schema v%d is newer than this app's v%d", version, SchemaVersion)
	}
	for v := version; v < len(migrations); v++ {
		stmts := make([]sqlite.Statement, 0, len(migrations[v])+1)
		for _, q := range migrations[v] {
			stmts = append(stmts, sqlite.Statement{SQL: q})
		}
		if backfill := backfills[v+1]; backfill != nil {
			more, err := backfill(ctx, s)
			if err != nil {
				return fmt.Errorf("store: migration to v%d: %w", v+1, err)
			}
			stmts = append(stmts, more...)
		}
		stmts = append(stmts, sqlite.Statement{SQL: fmt.Sprintf("PRAGMA user_version=%d", v+1)})
		if _, err := s.db.Transaction(ctx, stmts); err != nil {
			return fmt.Errorf("store: migration to v%d: %w", v+1, err)
		}
	}
	return nil
}

// exec runs one statement.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sqlite.Result, error) {
	return s.db.Execute(ctx, query, args...)
}

// query runs one statement and returns its rows.
func (s *Store) query(ctx context.Context, query string, args ...any) (sqlite.Rows, error) {
	return s.db.Query(ctx, query, args...)
}

// transaction runs statements atomically.
func (s *Store) transaction(ctx context.Context, stmts []sqlite.Statement) error {
	_, err := s.db.Transaction(ctx, stmts)
	return err
}

// stmt builds one statement.
func stmt(query string, args ...any) sqlite.Statement {
	return sqlite.Statement{SQL: query, Args: args}
}

// number reads an integer column.
func number(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case []byte:
		n := int64(0)
		fmt.Sscan(string(x), &n)
		return n
	case string:
		n := int64(0)
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}

// text reads a string column.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// blob reads a []byte column.
func blob(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	}
	return nil
}

// truth reads a boolean column.
func truth(v any) bool { return number(v) != 0 }

// unix reads a timestamp column, zero when null.
func unix(v any) time.Time {
	n := number(v)
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// unixPtr writes a timestamp column, nil for the zero time.
func unixPtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// notFound reports whether err is a store lookup miss.
func notFound(err error) bool {
	return errors.Is(err, mail.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

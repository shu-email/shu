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
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// Store is the SQLite connection and the mail cache on it.
type Store struct {
	db *sqlite.DB
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

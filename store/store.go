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
	// the same keys must not each make a conversation of them. The
	// writes of labels hold it too, as a sync makes labels of keywords.
	threadMu sync.Mutex
	// labelsGone is when each label DeleteLabel deleted went, by its
	// account and its keyword in lower case, under threadMu: a sync that
	// began before then read the keyword before the deletion took it off
	// the server, and must not make the label again.
	labelsGone map[string]time.Time
	// contactMu makes each write of the address book one step with the
	// reads it is worked out from (see contacts.go). Where both are held,
	// threadMu is taken first.
	contactMu sync.Mutex
	// now is the clock a message's date counts no later than in the
	// address book. Tests replace it.
	now func() time.Time
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
	// v8: pages and searches over every folder and account. The
	// recipients of a message, To and Cc as text, join the full-text
	// index, which is made anew with them and rebuilt. A date a message
	// lacks is 0, never null, so that the order of the pages, newest
	// first and then by row, is total. A folder's list index gains the
	// row that breaks a tie of dates; messages_date reads every folder
	// newest first, for a scope of several, and messages_flagged the
	// starred messages. folders.complete records that the cache lacks
	// none of the folder's messages up to its sync cursor.
	{
		`DROP TRIGGER messages_ai`,
		`DROP TRIGGER messages_ad`,
		`DROP TRIGGER messages_au`,
		`DROP TABLE messages_fts`,
		`ALTER TABLE messages ADD COLUMN recipients TEXT`,
		`UPDATE messages SET recipients = (
			SELECT group_concat(trim(COALESCE(value ->> 'name', '') || ' ' || COALESCE(value ->> 'email', '')), ', ')
			FROM (SELECT value FROM json_each(CASE WHEN json_valid(to_json) THEN to_json ELSE '[]' END)
				UNION ALL
				SELECT value FROM json_each(CASE WHEN json_valid(cc_json) THEN cc_json ELSE '[]' END)))`,
		`UPDATE messages SET date = 0 WHERE date IS NULL`,
		`ALTER TABLE folders ADD COLUMN complete INTEGER NOT NULL DEFAULT 0`,
		`DROP INDEX messages_list`,
		`CREATE INDEX messages_list ON messages (folder_id, date DESC, id DESC)`,
		`CREATE INDEX messages_date ON messages (date DESC, id DESC, folder_id)`,
		`CREATE INDEX messages_flagged ON messages (date DESC, id DESC, folder_id) WHERE flagged = 1`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(
			subject, from_name, from_email, body_text, preview, recipients,
			content='messages', content_rowid='id', tokenize='trigram')`,
		`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text, preview, recipients)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text, new.preview, new.recipients);
		END`,
		`CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text, preview, recipients)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text, old.preview, old.recipients);
		END`,
		`CREATE TRIGGER messages_au AFTER UPDATE OF subject, from_name, from_email, body_text, preview, recipients ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, subject, from_name, from_email, body_text, preview, recipients)
			VALUES ('delete', old.id, old.subject, old.from_name, old.from_email, old.body_text, old.preview, old.recipients);
			INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text, preview, recipients)
			VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text, new.preview, new.recipients);
		END`,
		`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`,
	},
	// v9: how an account signs in: '' with a password, or through OAuth
	// with the provider named. Either way the secret is in the keychain:
	// the password, or the refresh token. The accounts there before sign
	// in with their passwords, as they did.
	{
		`ALTER TABLE accounts ADD COLUMN oauth TEXT NOT NULL DEFAULT ''`,
	},
	// v10: identities and the send delay. An account sends from its own
	// address, which is the account's, and from the identities added
	// here, in the order of position; an address is one identity of an
	// account at most, whatever its case, and the rows go with the
	// account. A draft names the identity it is written from, '' for the
	// account's own. An outbox entry sent with a delay has the time it is
	// due and the draft it was written as, which stays a draft until it
	// leaves; the entries there were go at once and have none.
	{
		`CREATE TABLE identities (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX identities_email ON identities (account_id, email COLLATE NOCASE)`,
		`ALTER TABLE drafts ADD COLUMN identity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE outbox ADD COLUMN due_at INTEGER`,
		`ALTER TABLE outbox ADD COLUMN draft_id TEXT NOT NULL DEFAULT ''`,
	},
	// v11: the address book (see mail.ContactState). A contact is an
	// address, one whatever its case, key being it in lower case, across
	// the accounts: the name kept for it and where that came from, whether
	// the user made it a favorite, added it or deleted it, and the
	// messages the user wrote to it and those it wrote, how many, the
	// newest date, and their weights added up, which rank it. A deleted
	// contact keeps its row, with its address alone, so that its mail
	// does not bring it back. contacts_rank is the order of the book.
	// contact_terms holds the terms each live contact is found by, read by
	// prefix, and contact_messages the key of every message counted, so
	// that none counts twice. backfillContacts makes the book of the mail
	// cached and sent before.
	{
		`CREATE TABLE contacts (
			key TEXT PRIMARY KEY,
			address TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			name_from INTEGER NOT NULL DEFAULT 0,
			name_at INTEGER NOT NULL DEFAULT 0,
			favorite INTEGER NOT NULL DEFAULT 0,
			added INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0,
			forgot_at INTEGER NOT NULL DEFAULT 0,
			sent INTEGER NOT NULL DEFAULT 0,
			sent_at INTEGER NOT NULL DEFAULT 0,
			sent_score REAL NOT NULL DEFAULT 0,
			received INTEGER NOT NULL DEFAULT 0,
			received_at INTEGER NOT NULL DEFAULT 0,
			received_score REAL NOT NULL DEFAULT 0) WITHOUT ROWID`,
		`CREATE INDEX contacts_rank ON contacts (favorite DESC, (sent > 0 OR added) DESC,
			(CASE WHEN sent > 0 OR added THEN sent_score ELSE received_score END) DESC,
			received_score DESC, key) WHERE deleted = 0`,
		`CREATE TABLE contact_terms (
			term TEXT NOT NULL,
			key TEXT NOT NULL,
			PRIMARY KEY (term, key)) WITHOUT ROWID`,
		`CREATE INDEX contact_terms_key ON contact_terms (key, term)`,
		`CREATE TABLE contact_messages (key TEXT PRIMARY KEY) WITHOUT ROWID`,
	},
	// v12: labels (see mail.Label). A label is an account's, with a name
	// that is one whatever its case (name_key, mail.LabelNameKey), a
	// color, and a keyword, its name on the server, also one whatever its
	// case; position orders them, and their IDs are never given again.
	// message_labels holds the labels each cached message has as the
	// server keeps them, which a sync writes from its keywords;
	// local_labels those kept on this device only, by the message's
	// mail.LabelKey, which messages.msg_key holds, so that a label stays
	// on a message wherever it goes. Both index their messages by label,
	// which lists and counts a label's. folders.permanent_flags is what a
	// folder's PERMANENTFLAGS said when it was last opened to change, as
	// JSON, null until then. backfillLabelKeys writes the key of each
	// message already there.
	{
		`ALTER TABLE messages ADD COLUMN msg_key TEXT`,
		`CREATE INDEX messages_key ON messages (msg_key)`,
		`ALTER TABLE folders ADD COLUMN permanent_flags TEXT`,
		`CREATE TABLE labels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			name_key TEXT NOT NULL,
			color TEXT NOT NULL,
			keyword TEXT NOT NULL,
			position INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX labels_name ON labels (account_id, name_key)`,
		`CREATE UNIQUE INDEX labels_keyword ON labels (account_id, keyword COLLATE NOCASE)`,
		`CREATE TABLE message_labels (
			message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
			PRIMARY KEY (message_id, label_id)) WITHOUT ROWID`,
		`CREATE INDEX message_labels_label ON message_labels (label_id, message_id)`,
		`CREATE TABLE local_labels (
			label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
			msg_key TEXT NOT NULL,
			PRIMARY KEY (label_id, msg_key)) WITHOUT ROWID`,
		`CREATE INDEX local_labels_key ON local_labels (msg_key, label_id)`,
	},
	// v13: rules and their log (see mail.Rule, mail.RuleActivity). A rule
	// is in the order of position, its conditions and actions as JSON; its
	// IDs are never given again, and it goes with the account it names,
	// when it names one. rule_log is the rules' matches, each with the
	// message where the rule found it, by its key too (msg_key), what the
	// rule was to do and what it changed, as JSON; an entry goes with its
	// account and outlives its rule, to be undone. expected_arrivals are
	// the messages, by their keys, this app is moving into a folder, which
	// are no mail that arrived there; they go with the folder.
	{
		`CREATE TABLE rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			paused INTEGER NOT NULL DEFAULT 0,
			match_mode TEXT NOT NULL,
			stop INTEGER NOT NULL DEFAULT 0,
			conditions TEXT NOT NULL,
			actions TEXT NOT NULL)`,
		`CREATE INDEX rules_account ON rules (account_id)`,
		`CREATE TABLE rule_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			rule_id INTEGER NOT NULL,
			rule_name TEXT NOT NULL,
			manual INTEGER NOT NULL DEFAULT 0,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			folder TEXT NOT NULL,
			uidvalidity INTEGER NOT NULL,
			uid INTEGER NOT NULL,
			message_id TEXT NOT NULL DEFAULT '',
			msg_key TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			from_name TEXT NOT NULL DEFAULT '',
			from_email TEXT NOT NULL DEFAULT '',
			actions TEXT NOT NULL,
			changes TEXT NOT NULL DEFAULT '{}',
			quiet INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX rule_log_rule ON rule_log (rule_id, id)`,
		`CREATE INDEX rule_log_account ON rule_log (account_id, state, id)`,
		`CREATE INDEX rule_log_message ON rule_log (account_id, folder, uid)`,
		`CREATE TABLE expected_arrivals (
			id INTEGER PRIMARY KEY,
			folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
			msg_key TEXT NOT NULL)`,
		`CREATE INDEX expected_arrivals_key ON expected_arrivals (folder_id, msg_key)`,
	},
	// v14: folder subscriptions, as IMAP keeps them (Folder.Subscribed).
	// A folder is subscribed until the user hides it; servers without
	// LIST-EXTENDED never say, so they keep every folder subscribed.
	{
		`ALTER TABLE folders ADD COLUMN subscribed INTEGER NOT NULL DEFAULT 1`,
	},
	// v15: the invitation a message carries, as JSON (mail.CalendarInvite),
	// so the reading pane shows it from the cache too.
	{
		`ALTER TABLE messages ADD COLUMN calendar TEXT`,
	},
	// v16: the aggregates of a conversation, so that a list reads one row
	// per conversation instead of its messages (see thread_aggregates.go).
	threadAggregateMigration(),
	// v17: the reminders (see mail.Reminder): a conversation of an account
	// the user asked to come back to the inbox, with the time it is due.
	// One per conversation, and it goes with its account or its
	// conversation, a merge taking it to the conversation it joined.
	{
		`CREATE TABLE reminders (
			id INTEGER PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
			subject TEXT NOT NULL DEFAULT '',
			remind_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE (account_id, thread_id))`,
		`CREATE INDEX reminders_account ON reminders (account_id, remind_at)`,
		`CREATE INDEX reminders_due ON reminders (remind_at)`,
	},
	// v18: the follow-ups the user put off (see FollowUpScope): the time
	// they dismissed a conversation, until a newer message of it is
	// cached. One per conversation, and it goes with its account or its
	// conversation, a merge taking it to the conversation it joined.
	{
		`CREATE TABLE follow_up_dismissals (
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
			dismissed_at INTEGER NOT NULL,
			PRIMARY KEY (account_id, thread_id))`,
		`CREATE INDEX follow_up_dismissals_thread ON follow_up_dismissals (thread_id)`,
	},
	// v19: the smart mailboxes (see mail.SmartMailbox): a search the user
	// saved, by the list it was saved from, the typed text and the scope it
	// covered. They are this device's, go with their account, and a name is
	// one per account, whatever its case.
	{
		`CREATE TABLE smart_mailboxes (
			id INTEGER PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			name_key TEXT NOT NULL,
			folder TEXT NOT NULL DEFAULT '',
			query TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT 'here',
			position INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE (account_id, name_key))`,
		`CREATE INDEX smart_mailboxes_account ON smart_mailboxes (account_id, position)`,
	},
	// v20: the servers found or entered for a domain (see
	// Service.Discover). They are this device's, need no account, and a
	// domain has one: a later lookup or entry replaces it.
	{
		`CREATE TABLE discoveries (
			domain TEXT PRIMARY KEY,
			imap_host TEXT NOT NULL,
			imap_port INTEGER NOT NULL,
			imap_tls TEXT NOT NULL,
			smtp_host TEXT NOT NULL,
			smtp_port INTEGER NOT NULL,
			smtp_tls TEXT NOT NULL,
			source TEXT NOT NULL,
			found_at INTEGER NOT NULL)`,
	},
	// v21: the actions the user asked for that the server has not made
	// yet (see mail/queue.go). One row per message and action: it is
	// written before the server is touched, claimed with one state
	// change, and gone once it is done.
	{
		`CREATE TABLE pending_operations (
			id INTEGER PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			folder TEXT NOT NULL,
			uid INTEGER NOT NULL,
			dest TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_try INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			claimed_at INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX pending_operations_account ON pending_operations (account_id, state, id)`,
	},
	// v22: what a message's headers say of its sender and its list (see
	// mail.Security), one JSON column, so that a read has it after a
	// restart and without the headers. Migration v22 fills it from the
	// header lines the cache already kept.
	{
		`ALTER TABLE messages ADD COLUMN security TEXT`,
	},
}

// backfills compute, from the rows of the version before, statements a
// migration runs in its transaction after its own, keyed by the version
// it migrates to.
var backfills = map[int]func(context.Context, *Store) ([]sqlite.Statement, error){
	6:  backfillThreads,
	11: backfillContacts,
	12: backfillLabelKeys,
	22: backfillSecurity,
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
	s := &Store{db: db, now: time.Now, labelsGone: map[string]time.Time{}}
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

// realNumber reads a floating-point column.
func realNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

// unix reads a timestamp column, zero when null.
func unix(v any) time.Time {
	n := number(v)
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// unixUTC is unix in UTC.
func unixUTC(v any) time.Time {
	if t := unix(v); !t.IsZero() {
		return t.UTC()
	}
	return time.Time{}
}

// unixPtr writes a timestamp column, nil for the zero time.
func unixPtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// dateColumn writes a message's date, 0 for none: the pages order by it,
// and a null would fall out of their comparisons.
func dateColumn(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// notFound reports whether err is a store lookup miss.
func notFound(err error) bool {
	return errors.Is(err, mail.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

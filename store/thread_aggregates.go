package store

import (
	"context"
)

// A conversation's aggregates are derived from its messages. threads keeps
// the totals of a conversation across the folders of its account, and
// thread_folders the same totals in one folder, for a list narrowed to a
// folder. Triggers write both in the transaction of every write of a
// message, so a list reads one row per conversation (or per conversation
// and folder) instead of aggregating its messages, and no writer has to
// remember to keep them.
//
// A message hidden while it moves (deleted = 1) counts in neither, and
// thread_refs remains the only record of who a conversation is: the
// aggregates can be recomputed from messages at any time.

// threadAggregateSet is the assignment of the conversation's columns, for
// the thread the UPDATE names. The subqueries correlate on threads.id, so
// the same text serves the whole rebuild and the trigger of one thread.
const threadAggregateSet = `
	last_message_at = COALESCE((SELECT MAX(m.date) FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0), 0),
	message_count = (SELECT COUNT(*) FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0),
	unread_count = (SELECT COUNT(*) FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0 AND m.seen = 0),
	starred_count = (SELECT COUNT(*) FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0 AND m.flagged = 1),
	attachment_count = (SELECT COUNT(*) FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0 AND m.has_attachments = 1),
	snippet = COALESCE((SELECT m.preview FROM messages m
		WHERE m.thread_id = threads.id AND m.deleted = 0
		ORDER BY m.date DESC, m.id DESC LIMIT 1), ''),
	participants_json = COALESCE((SELECT json_group_array(json_object('name', m.from_name, 'email', m.from_email))
		FROM (SELECT DISTINCT x.from_name, x.from_email FROM messages x
			WHERE x.thread_id = threads.id AND x.deleted = 0) m), '[]')`

// threadAggregateRebuild recomputes every aggregate from the messages.
// The migration that adds the columns runs it too, before the triggers
// exist, so that the cache it upgrades is in step at once.
var threadAggregateRebuild = []string{
	`UPDATE threads SET ` + threadAggregateSet,
	`DELETE FROM thread_folders`,
	`INSERT INTO thread_folders
		(thread_id, folder_id, last_message_at,
		 message_count, unread_count, starred_count, attachment_count)
		SELECT m.thread_id, m.folder_id, COALESCE(MAX(m.date), 0),
			COUNT(*), COALESCE(SUM(m.seen = 0), 0),
			COALESCE(SUM(m.flagged), 0), COALESCE(SUM(m.has_attachments), 0)
		FROM messages m
		WHERE m.deleted = 0 AND m.thread_id IS NOT NULL
		GROUP BY m.thread_id, m.folder_id`,
}

// aggregateThreadSQL recomputes one conversation's totals, the one the
// ID expression names.
func aggregateThreadSQL(id string) string {
	return `UPDATE threads SET ` + threadAggregateSet + `
		WHERE threads.id = ` + id
}

// clearThreadFolderSQL drops a folder's aggregate once it holds no
// message of the conversation; insertThreadFolderSQL writes it otherwise.
func clearThreadFolderSQL(thread, folder string) string {
	return `DELETE FROM thread_folders WHERE thread_id = ` + thread + `
		AND folder_id = ` + folder
}

func insertThreadFolderSQL(thread, folder string) string {
	return `INSERT INTO thread_folders
			(thread_id, folder_id, last_message_at,
			 message_count, unread_count, starred_count, attachment_count)
		SELECT ` + thread + `, ` + folder + `, COALESCE(MAX(m.date), 0),
			COUNT(*), COALESCE(SUM(m.seen = 0), 0),
			COALESCE(SUM(m.flagged), 0), COALESCE(SUM(m.has_attachments), 0)
		FROM messages m INDEXED BY messages_thread
		WHERE m.thread_id = ` + thread + ` AND m.folder_id = ` + folder + `
			AND m.deleted = 0
		HAVING COUNT(*) > 0`
}

// threadAggregateMigration adds the columns, the table and the triggers
// that keep them in step with messages.
func threadAggregateMigration() []string {
	write := func(thread, folder string) string {
		return aggregateThreadSQL(thread) + ";\n" +
			clearThreadFolderSQL(thread, folder) + ";\n" +
			insertThreadFolderSQL(thread, folder) + ";"
	}
	trigger := func(name, event, body string) string {
		return `CREATE TRIGGER ` + name + ` ` + event + ` BEGIN ` + body + `
		END`
	}
	out := []string{
		`ALTER TABLE threads ADD COLUMN last_message_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE threads ADD COLUMN message_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE threads ADD COLUMN unread_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE threads ADD COLUMN starred_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE threads ADD COLUMN attachment_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE threads ADD COLUMN snippet TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE threads ADD COLUMN participants_json TEXT NOT NULL DEFAULT '[]'`,
		`CREATE TABLE thread_folders (
			thread_id INTEGER NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
			folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
			last_message_at INTEGER NOT NULL DEFAULT 0,
			message_count INTEGER NOT NULL DEFAULT 0,
			unread_count INTEGER NOT NULL DEFAULT 0,
			starred_count INTEGER NOT NULL DEFAULT 0,
			attachment_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (thread_id, folder_id)) WITHOUT ROWID`,
		`CREATE INDEX thread_folders_folder ON thread_folders (folder_id, thread_id)`,
	}
	out = append(out, threadAggregateRebuild...)
	return append(out,
		trigger("messages_agg_ai", "AFTER INSERT ON messages",
			write("new.thread_id", "new.folder_id")),
		trigger("messages_agg_ad", "AFTER DELETE ON messages",
			write("old.thread_id", "old.folder_id")),
		// Every column the aggregates read is listed, so that a write of
		// any of them leaves them right, and a write of another leaves
		// them alone.
		trigger("messages_agg_au",
			"AFTER UPDATE OF thread_id, folder_id, date, seen, flagged, has_attachments, deleted ON messages",
			write("old.thread_id", "old.folder_id")+write("new.thread_id", "new.folder_id")),
	)
}

// RebuildThreadAggregates recomputes every conversation's aggregates from
// its messages, in one transaction. The store keeps them itself, so this
// is a repair of derived data, not a step a writer calls.
func (s *Store) RebuildThreadAggregates(ctx context.Context) error {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	stmts := make([]stmtType, 0, len(threadAggregateRebuild))
	for _, q := range threadAggregateRebuild {
		stmts = append(stmts, stmt(q))
	}
	return s.transaction(ctx, stmts)
}

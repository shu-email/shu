package store

import (
	"context"
	"fmt"
	"strconv"

	"shu/mail"
)

// CheckCache reads what the database derives and reports what is out of
// step; it writes nothing (see mail.IntegrityFinding). The parts the store
// owns are the schema's version, the references the foreign keys promise,
// the full-text index, and the conversations' aggregates.
func (s *Store) CheckCache(ctx context.Context) ([]mail.IntegrityFinding, error) {
	var out []mail.IntegrityFinding
	version, err := s.userVersion(ctx)
	if err != nil {
		return nil, err
	}
	if version != SchemaVersion {
		// The finding carries both versions, the one the database says and
		// the one this build writes, so that the report says what to do.
		f := mail.IntegrityOf(mail.IntegritySchema, []string{strconv.Itoa(version), strconv.Itoa(SchemaVersion)})
		f.Count = 1
		out = append(out, f)
	}
	refs, broken, err := s.danglingReferences(ctx)
	if err != nil {
		return nil, err
	}
	if broken > 0 {
		f := mail.IntegrityOf(mail.IntegrityReferences, refs)
		f.Count = broken
		out = append(out, f)
	}
	if err := s.CheckSearchIndex(ctx); err != nil {
		// The check writes to the index. A database that no longer takes
		// a statement at all (busy, gone) is a check that could not run,
		// not an index out of step; the read says which.
		if _, probe := s.userVersion(ctx); probe != nil {
			return nil, fmt.Errorf("store: the search index check could not run: %w", err)
		}
		out = append(out, mail.IntegrityOf(mail.IntegritySearchIndex, []string{"messages_fts"}))
	}
	threads, err := s.threadAggregateDrift(ctx)
	if err != nil {
		return nil, err
	}
	if len(threads) > 0 {
		ids := make([]string, 0, len(threads))
		for _, id := range threads {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		out = append(out, mail.IntegrityOf(mail.IntegrityThreads, ids))
	}
	return out, nil
}

// userVersion is the schema version the database says it is.
func (s *Store) userVersion(ctx context.Context) (int, error) {
	rows, err := s.query(ctx, "PRAGMA user_version")
	if err != nil {
		return 0, err
	}
	if len(rows.Values) != 1 || len(rows.Values[0]) != 1 {
		return 0, fmt.Errorf("store: the database gave no schema version")
	}
	return int(number(rows.Values[0][0])), nil
}

// danglingReferences names the rows that point at rows which are not
// there, as "table/rowid", by the foreign keys the schema declares, and
// says how many violations there are: a table WITHOUT ROWID has no rowid
// to name a row by, so its rows are counted but only the table is named,
// and a row that breaks more than one foreign key is named once.
func (s *Store) danglingReferences(ctx context.Context) ([]string, int, error) {
	rows, err := s.query(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]bool, len(rows.Values))
	out := make([]string, 0, len(rows.Values))
	for _, r := range rows.Values {
		if len(r) < 2 {
			continue
		}
		id := text(r[0])
		if r[1] != nil {
			id += "/" + text(r[1])
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, len(rows.Values), nil
}

// threadAggregateDrift lists the conversations whose aggregates are not
// what their messages say, either in threads or in thread_folders. The
// participants are not compared: their JSON has no order the recompute
// promises, and so no drift a check could prove.
func (s *Store) threadAggregateDrift(ctx context.Context) ([]int64, error) {
	queries := []string{threadAggregateDriftSQL, threadFolderDriftSQL}
	seen := map[int64]bool{}
	var out []int64
	for _, query := range queries {
		rows, err := s.query(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			id := number(r[0])
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// threadAggregateDriftSQL selects the conversations whose totals in
// threads are not the ones their messages give.
const threadAggregateDriftSQL = `
	WITH expected AS (
		SELECT m.thread_id AS id,
			COALESCE(MAX(m.date), 0) AS last_message_at,
			COUNT(*) AS message_count,
			COALESCE(SUM(m.seen = 0), 0) AS unread_count,
			COALESCE(SUM(m.flagged), 0) AS starred_count,
			COALESCE(SUM(m.has_attachments), 0) AS attachment_count,
			COALESCE((SELECT x.preview FROM messages x
				WHERE x.thread_id = m.thread_id AND x.deleted = 0
				ORDER BY x.date DESC, x.id DESC LIMIT 1), '') AS snippet
		FROM messages m
		WHERE m.deleted = 0 AND m.thread_id IS NOT NULL
		GROUP BY m.thread_id
	)
	SELECT t.id FROM threads t LEFT JOIN expected e ON e.id = t.id
	WHERE t.last_message_at != COALESCE(e.last_message_at, 0)
		OR t.message_count != COALESCE(e.message_count, 0)
		OR t.unread_count != COALESCE(e.unread_count, 0)
		OR t.starred_count != COALESCE(e.starred_count, 0)
		OR t.attachment_count != COALESCE(e.attachment_count, 0)
		OR t.snippet != COALESCE(e.snippet, '')
	ORDER BY t.id`

// threadFolderDriftSQL selects the conversations whose totals in
// thread_folders are not the ones their messages give in that folder, and
// those that should have a row there and have none.
const threadFolderDriftSQL = `
	WITH expected AS (
		SELECT m.thread_id, m.folder_id,
			COALESCE(MAX(m.date), 0) AS last_message_at,
			COUNT(*) AS message_count,
			COALESCE(SUM(m.seen = 0), 0) AS unread_count,
			COALESCE(SUM(m.flagged), 0) AS starred_count,
			COALESCE(SUM(m.has_attachments), 0) AS attachment_count
		FROM messages m
		WHERE m.deleted = 0 AND m.thread_id IS NOT NULL
		GROUP BY m.thread_id, m.folder_id
	), bad AS (
		SELECT tf.thread_id AS id FROM thread_folders tf
			LEFT JOIN expected e
				ON e.thread_id = tf.thread_id AND e.folder_id = tf.folder_id
		WHERE e.thread_id IS NULL
			OR tf.last_message_at != e.last_message_at
			OR tf.message_count != e.message_count
			OR tf.unread_count != e.unread_count
			OR tf.starred_count != e.starred_count
			OR tf.attachment_count != e.attachment_count
		UNION
		SELECT e.thread_id FROM expected e
			LEFT JOIN thread_folders tf
				ON tf.thread_id = e.thread_id AND tf.folder_id = e.folder_id
		WHERE tf.thread_id IS NULL
	)
	SELECT DISTINCT id FROM bad ORDER BY id`

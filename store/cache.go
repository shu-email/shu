package store

import (
	"context"
	"strconv"
)

// cacheVersionKey is the kv key of the version of the parsed cache that
// wrote the database (mail.CacheVersion).
const cacheVersionKey = "cache_version"

// PrepareCache clears the parsed cache when version is not the one it was
// written with: bodies and attachment rows a sync can fetch again. Drafts,
// the outbox, contacts, labels, rules and settings cannot be made from the
// server and are never touched. It reports whether it cleared anything.
func (s *Store) PrepareCache(ctx context.Context, version int) (bool, error) {
	rows, err := s.query(ctx, `SELECT value FROM kv WHERE key = ?`, cacheVersionKey)
	if err != nil {
		return false, err
	}
	stored := 0
	if len(rows.Values) == 1 {
		// A value that is not a version is from no build: drop the cache
		// rather than trust it.
		stored, _ = strconv.Atoi(text(rows.Values[0][0]))
	}
	if stored == version {
		return false, nil
	}
	cleared := []stmtType{
		stmt(`UPDATE messages SET body_text = '', body_html = '', body_truncated = 0, cached_at = NULL`),
		stmt(`DELETE FROM attachments`),
		stmt(`INSERT INTO kv (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, cacheVersionKey, strconv.Itoa(version)),
	}
	return true, s.transaction(ctx, cleared)
}

// AttachmentFileRefs lists the files the cached attachments' bytes are in,
// which a sweep of the cache directory keeps.
func (s *Store) AttachmentFileRefs(ctx context.Context) ([]string, error) {
	rows, err := s.query(ctx, `SELECT cache_path FROM attachments
		WHERE cache_path IS NOT NULL AND cache_path <> ''`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, text(r[0]))
	}
	return out, nil
}

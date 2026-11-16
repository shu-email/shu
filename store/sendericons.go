package store

import (
	"context"

	"shu/mail"
)

// The icons this device keeps for senders (migrations v27 and v28). A key
// has one row: what the lookup found, or that it found none. The bytes are
// a file of the cache directory the engine writes, and only the hash is
// here.

// SenderIcon reads the icon kept for a key, or reports that none was
// looked for.
func (s *Store) SenderIcon(ctx context.Context, key string) (mail.StoredIcon, bool, error) {
	rows, err := s.query(ctx, `SELECT content_hash, found, checked_at FROM sender_icons WHERE key = ?`, key)
	if err != nil {
		return mail.StoredIcon{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.StoredIcon{}, false, nil
	}
	r := rows.Values[0]
	return mail.StoredIcon{
		Key:         key,
		ContentHash: text(r[0]),
		Found:       truth(r[1]),
		CheckedAt:   unixUTC(r[2]),
	}, true, nil
}

// SaveSenderIcon keeps what a key's lookup found, replacing what it had.
func (s *Store) SaveSenderIcon(ctx context.Context, icon mail.StoredIcon) error {
	_, err := s.exec(ctx, `INSERT INTO sender_icons (key, content_hash, found, checked_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			content_hash = excluded.content_hash, found = excluded.found, checked_at = excluded.checked_at`,
		icon.Key, icon.ContentHash, icon.Found, icon.CheckedAt.Unix())
	return err
}

// SenderIconRefs lists the icon files the rows name, for the sweep that
// keeps them and removes the rest.
func (s *Store) SenderIconRefs(ctx context.Context) ([]string, error) {
	rows, err := s.query(ctx, `SELECT content_hash FROM sender_icons WHERE found = 1 AND content_hash <> ''`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, text(r[0]))
	}
	return out, nil
}

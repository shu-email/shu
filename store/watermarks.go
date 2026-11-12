package store

import (
	"context"

	"shu/mail"
)

// TakeNew returns the unread messages of a folder above its watermark,
// newest first, and raises the watermark over them in the same step, so
// that no message is returned twice: not to a second caller, and not
// after a restart.
//
// A folder without a watermark, or one whose UIDVALIDITY is no longer the
// watermark's, gets its watermark set to the highest UID it has and
// returns nothing: the mail that was there before is not news.
func (s *Store) TakeNew(ctx context.Context, accountID, folder string) ([]mail.Summary, error) {
	rows, err := s.query(ctx, `SELECT f.id, f.uidvalidity, f.last_uid, w.uidvalidity, w.uid,
			(SELECT COALESCE(MAX(m.server_uid), 0) FROM messages m WHERE m.folder_id = f.id)
		FROM folders f LEFT JOIN watermarks w ON w.folder_id = f.id
		WHERE f.account_id = ? AND f.name = ?`, accountID, folder)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, mail.ErrNotFound
	}
	r := rows.Values[0]
	folderID, validity := number(r[0]), number(r[1])
	highest := max(number(r[2]), number(r[5]))
	if validity == 0 {
		// The folder was never synced: there is nothing to compare with.
		return nil, nil
	}
	if r[3] == nil || number(r[3]) != validity {
		_, err := s.exec(ctx, `INSERT INTO watermarks (folder_id, uidvalidity, uid) VALUES (?, ?, ?)
			ON CONFLICT (folder_id) DO UPDATE SET uidvalidity = excluded.uidvalidity, uid = excluded.uid`,
			folderID, validity, highest)
		return nil, err
	}
	mark := number(r[4])
	found, err := s.query(ctx, `SELECT `+summaryColumns+`
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE m.folder_id = ? AND m.server_uid > ? AND m.seen = 0 AND m.deleted = 0
		ORDER BY m.server_uid DESC`, folderID, mark)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Summary, 0, len(found.Values))
	for _, row := range found.Values {
		m := summaryRow(row)
		highest = max(highest, int64(m.UID))
		out = append(out, m)
	}
	if highest <= mark {
		return nil, nil
	}
	// The watermark moves only from the value these rows were read above.
	// A caller that finds it moved was overtaken by another, which has
	// the same rows and announces them.
	res, err := s.exec(ctx, `UPDATE watermarks SET uid = ?
		WHERE folder_id = ? AND uidvalidity = ? AND uid = ?`, highest, folderID, validity, mark)
	if err != nil {
		return nil, err
	}
	if res.Changes != 1 || len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

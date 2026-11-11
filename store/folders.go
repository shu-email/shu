package store

import (
	"context"

	"shu/mail"
)

// folderColumns are the folder fields every query reads.
const folderColumns = `name, COALESCE(delimiter, ''), COALESCE(special_use, ''),
	unread, total, uidvalidity, last_uid, synced_at`

func folderRow(r []any) mail.Folder {
	return mail.Folder{
		Name:        text(r[0]),
		Delimiter:   text(r[1]),
		SpecialUse:  text(r[2]),
		Unread:      int(number(r[3])),
		Total:       int(number(r[4])),
		UIDValidity: uint32(number(r[5])),
		LastUID:     mail.UID(number(r[6])),
		SyncedAt:    unix(r[7]),
	}
}

// Folders returns an account's folders, special-use folders first.
func (s *Store) Folders(ctx context.Context, accountID string) ([]mail.Folder, error) {
	rows, err := s.query(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE account_id = ?
		ORDER BY CASE WHEN special_use IN ('\Inbox') THEN 0
			WHEN special_use IS NOT NULL AND special_use <> '' THEN 1 ELSE 2 END,
			lower(name)`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Folder, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, folderRow(r))
	}
	return out, nil
}

// Folder returns one folder by name.
func (s *Store) Folder(ctx context.Context, accountID, name string) (mail.Folder, bool, error) {
	rows, err := s.query(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE account_id = ? AND name = ?`, accountID, name)
	if err != nil {
		return mail.Folder{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.Folder{}, false, nil
	}
	return folderRow(rows.Values[0]), true, nil
}

// SaveFolders upserts a LIST result, keeping each folder's sync cursor.
func (s *Store) SaveFolders(ctx context.Context, accountID string, folders []mail.Folder) error {
	stmts := make([]stmtType, 0, len(folders)+1)
	for _, f := range folders {
		stmts = append(stmts,
			stmt(`INSERT INTO folders (account_id, name, delimiter, special_use, unread, total)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (account_id, name) DO UPDATE SET
					delimiter=excluded.delimiter, special_use=excluded.special_use,
					unread=excluded.unread, total=excluded.total`,
				accountID, f.Name, f.Delimiter, f.SpecialUse, f.Unread, f.Total),
		)
	}
	return s.transaction(ctx, stmts)
}

// folderID finds a folder's row id.
func (s *Store) folderID(ctx context.Context, accountID, name string) (int64, error) {
	rows, err := s.query(ctx, `SELECT id FROM folders WHERE account_id = ? AND name = ?`, accountID, name)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, mail.ErrNotFound
	}
	return number(rows.Values[0][0]), nil
}

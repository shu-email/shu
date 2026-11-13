package store

import (
	"context"
	"strings"

	"shu/mail"
)

// folderColumns are the folder fields every query reads.
const folderColumns = `name, COALESCE(delimiter, ''), COALESCE(special_use, ''),
	unread, total, uidvalidity, last_uid, synced_at, complete, permanent_flags, subscribed`

func folderRow(r []any) mail.Folder {
	return mail.Folder{
		Name:           text(r[0]),
		Delimiter:      text(r[1]),
		SpecialUse:     text(r[2]),
		Unread:         int(number(r[3])),
		Total:          int(number(r[4])),
		UIDValidity:    uint32(number(r[5])),
		LastUID:        mail.UID(number(r[6])),
		SyncedAt:       unix(r[7]),
		Complete:       truth(r[8]),
		PermanentFlags: decodeFlags(r[9]),
		Subscribed:     truth(r[10]),
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

// SaveFolders upserts a LIST result, keeping each folder's sync cursor
// and what its PERMANENTFLAGS said, of which a LIST says nothing.
func (s *Store) SaveFolders(ctx context.Context, accountID string, folders []mail.Folder) error {
	stmts := make([]stmtType, 0, len(folders)+1)
	for _, f := range folders {
		stmts = append(stmts,
			stmt(`INSERT INTO folders (account_id, name, delimiter, special_use, unread, total, subscribed)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (account_id, name) DO UPDATE SET
					delimiter=excluded.delimiter, special_use=excluded.special_use,
					unread=excluded.unread, total=excluded.total,
					subscribed=excluded.subscribed`,
				accountID, f.Name, f.Delimiter, f.SpecialUse, f.Unread, f.Total, f.Subscribed),
		)
	}
	return s.transaction(ctx, stmts)
}

// FolderCache counts the messages the cache holds of a folder, against
// what the server last said it has.
func (s *Store) FolderCache(ctx context.Context, accountID, folder string) (mail.FolderCache, error) {
	rows, err := s.query(ctx, `SELECT f.total, f.complete,
			(SELECT COUNT(*) FROM messages m WHERE m.folder_id = f.id AND m.deleted = 0)
		FROM folders f WHERE f.account_id = ? AND f.name = ?`, accountID, folder)
	if err != nil {
		return mail.FolderCache{}, err
	}
	if len(rows.Values) == 0 {
		return mail.FolderCache{}, mail.ErrNotFound
	}
	r := rows.Values[0]
	return mail.FolderCache{Total: int(number(r[0])), Complete: truth(r[1]), Cached: int(number(r[2]))}, nil
}

// FolderUIDs lists the UIDs the cache holds in a folder, the tombstones
// of messages being moved away included: those are still there.
func (s *Store) FolderUIDs(ctx context.Context, accountID, folder string) ([]mail.UID, error) {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `SELECT server_uid FROM messages WHERE folder_id = ? ORDER BY server_uid`, folderID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.UID, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.UID(number(r[0])))
	}
	return out, nil
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

// SetFolderSubscribed records whether the user wants a folder in the
// sidebar. The server is told by the engine; a folder keeps its mail and
// its cursor either way.
func (s *Store) SetFolderSubscribed(ctx context.Context, accountID, name string, on bool) error {
	if _, err := s.folderID(ctx, accountID, name); err != nil {
		return err
	}
	return s.transaction(ctx, []stmtType{
		stmt(`UPDATE folders SET subscribed = ? WHERE account_id = ? AND name = ?`,
			on, accountID, name),
	})
}

// RenameFolder moves a folder row and the rows of the folders below it to
// their new names, keeping their messages, cursors and what each keeps.
// The server renames the children with their parent; the engine says
// which prefix it used, delimiter.
func (s *Store) RenameFolder(ctx context.Context, accountID, from, to, delimiter string) error {
	if delimiter == "" {
		delimiter = "/"
	}
	rows, err := s.query(ctx, `SELECT name FROM folders
		WHERE account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\')`,
		accountID, from, escapeLike(from+delimiter)+"%")
	if err != nil {
		return err
	}
	stmts := make([]stmtType, 0, len(rows.Values))
	for _, r := range rows.Values {
		name := text(r[0])
		next := to + strings.TrimPrefix(name, from)
		stmts = append(stmts, stmt(`UPDATE folders SET name = ? WHERE account_id = ? AND name = ?`,
			next, accountID, name))
	}
	if len(stmts) == 0 {
		return mail.ErrNotFound
	}
	return s.transaction(ctx, stmts)
}

// DeleteFolder removes a folder row and, with it, the cached messages in
// it. The engine deletes the folder on the server only after the user
// chose to lose it.
func (s *Store) DeleteFolder(ctx context.Context, accountID, name string) error {
	if _, err := s.folderID(ctx, accountID, name); err != nil {
		return err
	}
	return s.transaction(ctx, []stmtType{
		stmt(`DELETE FROM folders WHERE account_id = ? AND name = ?`, accountID, name),
	})
}

// escapeLike makes a literal match for a SQL LIKE pattern.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

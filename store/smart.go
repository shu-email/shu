package store

import (
	"context"
	"slices"

	"shu/mail"
)

// Smart mailboxes are one table (migration v19): a search the user saved,
// by the folder it was saved from, the typed text and the scope it
// covered. They are this device's, go with their account, and are read by
// the same plan as any search, so that a mailbox lists what the search did.

// SmartMailboxes lists an account's smart mailboxes in their order.
func (s *Store) SmartMailboxes(ctx context.Context, accountID string) ([]mail.SmartMailbox, error) {
	rows, err := s.query(ctx, `SELECT id, name, folder, query, scope FROM smart_mailboxes
		WHERE account_id = ? ORDER BY position, id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.SmartMailbox, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.SmartMailbox{ID: number(r[0]), Account: accountID, Name: text(r[1]),
			Folder: text(r[2]), Query: text(r[3]), Scope: text(r[4])})
	}
	return out, nil
}

// SaveSmartMailbox adds a smart mailbox after the account's others, or
// changes the name, search and scope of one there is. The folder it was
// saved from stays.
func (s *Store) SaveSmartMailbox(ctx context.Context, m mail.SmartMailbox) (mail.SmartMailbox, error) {
	key := mail.SmartNameKey(m.Name)
	if m.ID != 0 {
		taken, err := s.smartNameTaken(ctx, m.Account, key, m.ID)
		if err != nil {
			return mail.SmartMailbox{}, err
		}
		if taken {
			return mail.SmartMailbox{}, mail.ErrSmartExists
		}
		rows, err := s.query(ctx, `UPDATE smart_mailboxes SET name = ?, name_key = ?, query = ?, scope = ?
			WHERE id = ? AND account_id = ? RETURNING folder`, m.Name, key, m.Query, m.Scope, m.ID, m.Account)
		if err != nil {
			return mail.SmartMailbox{}, err
		}
		if len(rows.Values) == 0 {
			return mail.SmartMailbox{}, mail.ErrNotFound
		}
		// The list it was saved from is not the change's to make.
		m.Folder = text(rows.Values[0][0])
		return m, nil
	}
	taken, err := s.smartNameTaken(ctx, m.Account, key, 0)
	if err != nil {
		return mail.SmartMailbox{}, err
	}
	if taken {
		return mail.SmartMailbox{}, mail.ErrSmartExists
	}
	rows, err := s.query(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM smart_mailboxes WHERE account_id = ?`, m.Account)
	if err != nil {
		return mail.SmartMailbox{}, err
	}
	position := int64(0)
	if len(rows.Values) > 0 {
		position = number(rows.Values[0][0])
	}
	rows, err = s.query(ctx, `INSERT INTO smart_mailboxes (account_id, name, name_key, folder, query, scope, position, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		m.Account, m.Name, key, m.Folder, m.Query, m.Scope, position, s.now().Unix())
	if err != nil {
		return mail.SmartMailbox{}, err
	}
	m.ID = number(rows.Values[0][0])
	return m, nil
}

// smartNameTaken reports whether an account has a smart mailbox of a name
// key other than id's.
func (s *Store) smartNameTaken(ctx context.Context, accountID, key string, id int64) (bool, error) {
	rows, err := s.query(ctx, `SELECT 1 FROM smart_mailboxes WHERE account_id = ? AND name_key = ? AND id <> ? LIMIT 1`,
		accountID, key, id)
	if err != nil {
		return false, err
	}
	return len(rows.Values) > 0, nil
}

// DeleteSmartMailbox removes a smart mailbox, and no mail.
func (s *Store) DeleteSmartMailbox(ctx context.Context, accountID string, id int64) error {
	rows, err := s.query(ctx, `DELETE FROM smart_mailboxes WHERE id = ? AND account_id = ? RETURNING id`, id, accountID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return mail.ErrNotFound
	}
	return nil
}

// CountUnread counts the unread messages a list query reads, as
// MessagePage would list them one by one.
func (s *Store) CountUnread(ctx context.Context, q mail.ListQuery) (int, error) {
	p, err := s.plan(ctx, q, searchCriteria(q.Search))
	if err != nil {
		return 0, err
	}
	folders := p.folders
	if p.in != nil {
		folders = slices.DeleteFunc(slices.Clone(folders), func(id int64) bool { return !slices.Contains(p.in, id) })
	}
	if len(folders) == 0 {
		return 0, nil
	}
	// A count has no rows to stop at, so each index is worth driving:
	// the folder's, or the full-text index's.
	where, args := p.inScope("m", folders, true)
	if match, more := p.matchSQL("m", true); match != "" {
		where += ` AND ` + match
		args = append(args, more...)
	}
	if p.follow {
		threads, more := p.followThreadsSQL()
		where += ` AND m.thread_id IN (` + threads + `)`
		args = append(args, more...)
	}
	rows, err := s.query(ctx, `SELECT COUNT(*) FROM messages m WHERE `+where+` AND m.deleted = 0 AND m.seen = 0`, args...)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, nil
	}
	return int(number(rows.Values[0][0])), nil
}

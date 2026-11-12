package store

import (
	"context"

	"shu/mail"
)

// Identities returns the identities added to an account, in the order
// they were. Its own address is the account's, and no row.
func (s *Store) Identities(ctx context.Context, accountID string) ([]mail.Identity, error) {
	rows, err := s.query(ctx, `SELECT id, account_id, name, email FROM identities
		WHERE account_id = ? ORDER BY position, rowid`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Identity, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.Identity{
			ID: text(r[0]), AccountID: text(r[1]), Name: text(r[2]), Email: text(r[3]),
		})
	}
	return out, nil
}

// SaveIdentity adds an identity after those its account has, or changes
// the name and address of one there is; its account and place stay.
func (s *Store) SaveIdentity(ctx context.Context, id mail.Identity) error {
	_, err := s.exec(ctx, `INSERT INTO identities (id, account_id, position, name, email)
		SELECT ?, ?, COALESCE(MAX(position), 0) + 1, ?, ? FROM identities WHERE account_id = ?
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, email = excluded.email`,
		id.ID, id.AccountID, id.Name, id.Email, id.AccountID)
	return err
}

// DeleteIdentity removes an identity, and in the same step has the drafts
// written from it go from their account's own address: an edit, which
// raises their revision so that their server copies follow.
func (s *Store) DeleteIdentity(ctx context.Context, id string) error {
	return s.transaction(ctx, []stmtType{
		stmt(`UPDATE drafts SET identity = '', revision = revision + 1 WHERE identity = ?`, id),
		stmt(`DELETE FROM identities WHERE id = ?`, id),
	})
}

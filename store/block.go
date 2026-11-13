package store

import (
	"context"

	"shu/mail"
)

// BlockedSenders reads an account's blocks, newest first (see
// mail.BlockedSender).
func (s *Store) BlockedSenders(ctx context.Context, accountID string) ([]mail.BlockedSender, error) {
	rows, err := s.query(ctx, `SELECT value, domain, folder, at FROM blocked_senders
		WHERE account_id = ? ORDER BY at DESC, value`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.BlockedSender, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.BlockedSender{
			AccountID: accountID, Value: text(r[0]), Domain: truth(r[1]),
			Folder: text(r[2]), At: unix(r[3]),
		})
	}
	return out, nil
}

// SaveBlockedSender keeps a block, replacing where one of the same sender
// files its mail.
func (s *Store) SaveBlockedSender(ctx context.Context, b mail.BlockedSender) error {
	_, err := s.exec(ctx, `INSERT INTO blocked_senders (account_id, value, domain, folder, at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account_id, value, domain) DO UPDATE SET
			folder = excluded.folder, at = excluded.at`,
		b.AccountID, b.Value, boolInt(b.Domain), b.Folder, unixPtr(b.At))
	return err
}

// RemoveBlockedSender takes a block back.
func (s *Store) RemoveBlockedSender(ctx context.Context, accountID, value string, domain bool) error {
	_, err := s.exec(ctx, `DELETE FROM blocked_senders WHERE account_id = ? AND value = ? AND domain = ?`,
		accountID, value, boolInt(domain))
	return err
}

// UnsubscribeRecord reads what the account kept of leaving one sender's
// list.
func (s *Store) UnsubscribeRecord(ctx context.Context, accountID, email string) (mail.UnsubscribeRecord, bool, error) {
	rows, err := s.query(ctx, `SELECT method, destination, at FROM unsubscribes
		WHERE account_id = ? AND email = ?`, accountID, email)
	if err != nil {
		return mail.UnsubscribeRecord{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.UnsubscribeRecord{}, false, nil
	}
	r := rows.Values[0]
	return mail.UnsubscribeRecord{
		AccountID: accountID, Email: email, Method: mail.UnsubscribeMethod(text(r[0])),
		Destination: text(r[1]), At: unix(r[2]),
	}, true, nil
}

// Unsubscribes reads every list the account left from this device, the
// most recent first.
func (s *Store) Unsubscribes(ctx context.Context, accountID string) ([]mail.UnsubscribeRecord, error) {
	rows, err := s.query(ctx, `SELECT email, method, destination, at FROM unsubscribes
		WHERE account_id = ? ORDER BY at DESC, email`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.UnsubscribeRecord, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.UnsubscribeRecord{
			AccountID: accountID, Email: text(r[0]), Method: mail.UnsubscribeMethod(text(r[1])),
			Destination: text(r[2]), At: unix(r[3]),
		})
	}
	return out, nil
}

// SaveUnsubscribe keeps a list left, one row per sender.
func (s *Store) SaveUnsubscribe(ctx context.Context, r mail.UnsubscribeRecord) error {
	_, err := s.exec(ctx, `INSERT INTO unsubscribes (account_id, email, method, destination, at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account_id, email) DO UPDATE SET
			method = excluded.method, destination = excluded.destination, at = excluded.at`,
		r.AccountID, r.Email, string(r.Method), r.Destination, unixPtr(r.At))
	return err
}

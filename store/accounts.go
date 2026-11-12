package store

import (
	"context"

	"shu/mail"
)

// Accounts returns the configured accounts in the order they were added.
func (s *Store) Accounts(ctx context.Context) ([]mail.Account, error) {
	rows, err := s.query(ctx, `SELECT id, name, email, username,
		imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, oauth
		FROM accounts ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Account, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, accountRow(r))
	}
	return out, nil
}

// Account returns one account.
func (s *Store) Account(ctx context.Context, id string) (mail.Account, bool, error) {
	rows, err := s.query(ctx, `SELECT id, name, email, username,
		imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, oauth
		FROM accounts WHERE id = ?`, id)
	if err != nil {
		return mail.Account{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.Account{}, false, nil
	}
	return accountRow(rows.Values[0]), true, nil
}

func accountRow(r []any) mail.Account {
	return mail.Account{
		ID:       text(r[0]),
		Name:     text(r[1]),
		Email:    text(r[2]),
		Username: text(r[3]),
		IMAP:     mail.Server{Host: text(r[4]), Port: int(number(r[5])), TLS: mail.TLSMode(text(r[6]))},
		SMTP:     mail.Server{Host: text(r[7]), Port: int(number(r[8])), TLS: mail.TLSMode(text(r[9]))},
		OAuth:    text(r[10]),
	}
}

// SaveAccount inserts or updates an account.
func (s *Store) SaveAccount(ctx context.Context, a mail.Account) error {
	_, err := s.exec(ctx, `INSERT INTO accounts
		(id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, oauth)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name=excluded.name, email=excluded.email, username=excluded.username,
			imap_host=excluded.imap_host, imap_port=excluded.imap_port, imap_tls=excluded.imap_tls,
			smtp_host=excluded.smtp_host, smtp_port=excluded.smtp_port, smtp_tls=excluded.smtp_tls,
			oauth=excluded.oauth`,
		a.ID, a.Name, a.Email, a.Username,
		a.IMAP.Host, a.IMAP.Port, string(a.IMAP.TLS),
		a.SMTP.Host, a.SMTP.Port, string(a.SMTP.TLS), a.OAuth)
	return err
}

// DeleteAccount removes an account and, by cascade, its folders, messages,
// watermarks, outbox, drafts and the rows of their attachments. The
// settings kept for the account go with it.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	return s.transaction(ctx, []stmtType{
		stmt(`DELETE FROM accounts WHERE id = ?`, id),
		stmt(`DELETE FROM kv WHERE key = 'active_account' AND value = ?`, id),
		stmt(`DELETE FROM kv WHERE key = ?`, mail.MutedKey(id)),
	})
}

// ActiveAccount returns the account the window showed last.
func (s *Store) ActiveAccount(ctx context.Context) (string, error) {
	rows, err := s.query(ctx, `SELECT value FROM kv WHERE key = 'active_account'`)
	if err != nil {
		return "", err
	}
	if len(rows.Values) == 0 {
		return "", nil
	}
	return text(rows.Values[0][0]), nil
}

// SetActiveAccount remembers the account shown.
func (s *Store) SetActiveAccount(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `INSERT INTO kv (key, value) VALUES ('active_account', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, id)
	return err
}

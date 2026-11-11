package store

import (
	"context"
	"encoding/json"
	"fmt"

	"shu/mail"
)

// EnqueueOutbox writes a message to send and its raw form in one row,
// before any network call.
func (s *Store) EnqueueOutbox(ctx context.Context, p mail.PendingSend) error {
	envelope, err := json.Marshal(p.Envelope)
	if err != nil {
		return err
	}
	if p.State == "" {
		p.State = mail.StatePending
	}
	_, err = s.exec(ctx, `INSERT INTO outbox
		(id, account_id, raw, envelope_json, state, attempts, last_error, created_at, sent_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.AccountID, string(p.Raw), string(envelope), p.State, p.Attempts,
		p.LastError, p.CreatedAt.Unix(), unixPtr(p.SentAt))
	return err
}

// Outbox reads one entry with its raw message.
func (s *Store) Outbox(ctx context.Context, id string) (mail.PendingSend, error) {
	rows, err := s.query(ctx, `SELECT id, account_id, raw, envelope_json, state, attempts,
		COALESCE(last_error, ''), created_at, sent_at FROM outbox WHERE id = ?`, id)
	if err != nil {
		return mail.PendingSend{}, err
	}
	if len(rows.Values) == 0 {
		return mail.PendingSend{}, mail.ErrNotFound
	}
	return outboxRow(rows.Values[0])
}

func outboxRow(r []any) (mail.PendingSend, error) {
	p := mail.PendingSend{
		ID:        text(r[0]),
		AccountID: text(r[1]),
		Raw:       blob(r[2]),
		State:     text(r[4]),
		Attempts:  int(number(r[5])),
		LastError: text(r[6]),
		CreatedAt: unix(r[7]),
		SentAt:    unix(r[8]),
	}
	if err := json.Unmarshal([]byte(text(r[3])), &p.Envelope); err != nil {
		return p, fmt.Errorf("store: outbox envelope %s: %w", p.ID, err)
	}
	return p, nil
}

// PendingSends lists an account's unsent entries, oldest first. Rows
// left "sending" by a crash are reported as unknown.
func (s *Store) PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error) {
	rows, err := s.query(ctx, `SELECT id, account_id, raw, envelope_json, state, attempts,
		COALESCE(last_error, ''), created_at, sent_at
		FROM outbox WHERE account_id = ? AND state <> ? ORDER BY created_at`,
		accountID, mail.StateSent)
	if err != nil {
		return nil, err
	}
	out := make([]mail.PendingSend, 0, len(rows.Values))
	for _, r := range rows.Values {
		p, err := outboxRow(r)
		if err != nil {
			return nil, err
		}
		if p.State == mail.StateSending {
			p.State = mail.StateUnknown
		}
		out = append(out, p)
	}
	return out, nil
}

// SetOutboxState records a state transition.
func (s *Store) SetOutboxState(ctx context.Context, id, state, lastError string) error {
	var err error
	switch state {
	case mail.StateSent:
		_, err = s.exec(ctx, `UPDATE outbox SET state = ?, last_error = NULL, sent_at = unixepoch(), attempts = attempts + 1 WHERE id = ?`, state, id)
	default:
		_, err = s.exec(ctx, `UPDATE outbox SET state = ?, last_error = ? WHERE id = ?`, state, lastError, id)
	}
	return err
}

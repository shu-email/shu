package store

import (
	"context"
	"encoding/json"
	"fmt"

	"shu/mail"
)

// EnqueueOutbox writes a message to send in one row, before any network
// call: its envelope and its raw form. A message with attachments comes
// without a raw form, as that would put the files' bytes in the database;
// its envelope names the files instead.
func (s *Store) EnqueueOutbox(ctx context.Context, p mail.PendingSend) error {
	envelope, err := json.Marshal(p.Envelope)
	if err != nil {
		return err
	}
	if p.State == "" {
		p.State = mail.StatePending
	}
	_, err = s.exec(ctx, `INSERT INTO outbox
		(id, account_id, raw, envelope_json, state, attempts, last_error, created_at, sent_at, due_at, draft_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.AccountID, string(p.Raw), string(envelope), p.State, p.Attempts,
		p.LastError, p.CreatedAt.Unix(), unixPtr(p.SentAt), unixPtr(p.DueAt), p.DraftID)
	return err
}

// outboxColumns are the outbox fields every query reads.
const outboxColumns = `id, account_id, raw, envelope_json, state, attempts,
	COALESCE(last_error, ''), created_at, sent_at, due_at, draft_id`

// Outbox reads one entry with its raw message.
func (s *Store) Outbox(ctx context.Context, id string) (mail.PendingSend, error) {
	rows, err := s.query(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE id = ?`, id)
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
		DueAt:     unix(r[9]),
		DraftID:   text(r[10]),
	}
	if err := json.Unmarshal([]byte(text(r[3])), &p.Envelope); err != nil {
		return p, fmt.Errorf("store: outbox envelope %s: %w", p.ID, err)
	}
	return p, nil
}

// outboxRows reads the entries of a query.
func (s *Store) outboxRows(ctx context.Context, query string, args ...any) ([]mail.PendingSend, error) {
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]mail.PendingSend, 0, len(rows.Values))
	for _, r := range rows.Values {
		p, err := outboxRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// PendingSends lists an account's unsent entries, oldest first. Rows
// left "sending" by a crash are reported as unknown. A send the user
// cancelled before any of it left is not among them: there is nothing of
// it to deliver or to ask about. Nor is one waiting out its send delay,
// which leaves on its own and must not be sent again before.
func (s *Store) PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error) {
	out, err := s.outboxRows(ctx, `SELECT `+outboxColumns+`
		FROM outbox WHERE account_id = ? AND state NOT IN (?, ?, ?) ORDER BY created_at`,
		accountID, mail.StateSent, mail.StateCancelled, mail.StateScheduled)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].State == mail.StateSending {
			out[i].State = mail.StateUnknown
		}
	}
	return out, nil
}

// ScheduledSends lists the entries waiting out their send delay, of every
// account, the soonest due first.
func (s *Store) ScheduledSends(ctx context.Context) ([]mail.PendingSend, error) {
	return s.outboxRows(ctx, `SELECT `+outboxColumns+`
		FROM outbox WHERE state = ? ORDER BY due_at, created_at, rowid`, mail.StateScheduled)
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

// ClaimOutbox moves an entry from one state to another if it is in the
// first, and reports whether it was. It is one statement, so of callers
// that claim an entry from the same state one alone gets true.
func (s *Store) ClaimOutbox(ctx context.Context, id, from, to string) (bool, error) {
	res, err := s.exec(ctx, `UPDATE outbox SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	return res.Changes == 1, nil
}

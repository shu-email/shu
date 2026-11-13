package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"shu/mail"
)

// The actions queued for the server (migration v21, see mail/queue.go).
// One row is one message and one action: it is written before the server
// is touched, claimed by one state change, and deleted once it is done.
// A row left claimed when a run ended is reclaimed by the next one, and a
// row whose action the server refused stays failed, with its reason, for
// the window to show.

// operationPayload is the JSON a row's flag or label rides in, with the
// folder's UIDVALIDITY when it was queued: UIDs name other messages in a
// folder the server reset under the queue.
type operationPayload struct {
	Flag     string `json:"flag,omitempty"`
	On       bool   `json:"on,omitempty"`
	Label    int64  `json:"label,omitempty"`
	Validity uint32 `json:"validity,omitempty"`
}

// operationColumns are the fields every query of the queue reads.
const operationColumns = `id, account_id, kind, folder, uid, dest, payload, state,
	attempts, next_try, created_at, claimed_at, last_error`

// EnqueueOperations writes what the server must still do, each with the
// ID the store gave it.
func (s *Store) EnqueueOperations(ctx context.Context, ops []mail.PendingOperation) ([]int64, error) {
	ids := make([]int64, 0, len(ops))
	for _, op := range ops {
		payload, err := json.Marshal(operationPayload{
			Flag: string(op.Flag), On: op.On, Label: int64(op.Label), Validity: op.Validity,
		})
		if err != nil {
			return ids, err
		}
		state := op.State
		if state == "" {
			state = mail.OperationPending
		}
		created := op.CreatedAt
		if created.IsZero() {
			created = s.now()
		}
		rows, err := s.query(ctx, `INSERT INTO pending_operations
			(account_id, kind, folder, uid, dest, payload, state, attempts, next_try, created_at, claimed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			op.AccountID, string(op.Kind), op.Folder, int64(op.UID), op.Dest, string(payload), string(state),
			op.Attempts, unixOf(op.NextTry), created.Unix(), unixOf(op.ClaimedAt))
		if err != nil {
			return ids, err
		}
		if len(rows.Values) > 0 {
			ids = append(ids, int64(number(rows.Values[0][0])))
		}
	}
	return ids, nil
}

// ClaimOperation takes the oldest action of an account that is due, for
// the one worker that claims it: the state change decides, so two
// workers never run the same one.
func (s *Store) ClaimOperation(ctx context.Context, accountID string, now time.Time) (mail.PendingOperation, bool, error) {
	rows, err := s.query(ctx, `UPDATE pending_operations SET state = ?, claimed_at = ?
		WHERE id = (SELECT id FROM pending_operations
			WHERE account_id = ? AND state = ? AND next_try <= ?
			ORDER BY id LIMIT 1)
		RETURNING `+operationColumns,
		string(mail.OperationClaimed), now.Unix(), accountID, string(mail.OperationPending), now.Unix())
	if err != nil {
		return mail.PendingOperation{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.PendingOperation{}, false, nil
	}
	op, err := operationRow(rows.Values[0])
	return op, err == nil, err
}

// ClaimOperationsByKey claims the actions that can go in one command with
// one just claimed: those of the same account, kind, folder, destination
// and payload.
func (s *Store) ClaimOperationsByKey(ctx context.Context, op mail.PendingOperation, now time.Time) ([]mail.PendingOperation, error) {
	rows, err := s.query(ctx, `UPDATE pending_operations SET state = ?, claimed_at = ?
		WHERE account_id = ? AND state = ? AND kind = ? AND folder = ? AND dest = ? AND payload = ?
		RETURNING `+operationColumns,
		string(mail.OperationClaimed), now.Unix(), op.AccountID, string(mail.OperationPending),
		string(op.Kind), op.Folder, op.Dest, operationPayloadOf(op))
	if err != nil {
		return nil, err
	}
	out := make([]mail.PendingOperation, 0, len(rows.Values))
	for _, r := range rows.Values {
		got, err := operationRow(r)
		if err != nil {
			return out, err
		}
		out = append(out, got)
	}
	return out, nil
}

// operationPayloadOf writes a row's payload as ClaimOperationsByKey
// matches it.
func operationPayloadOf(op mail.PendingOperation) string {
	payload, err := json.Marshal(operationPayload{
		Flag: string(op.Flag), On: op.On, Label: int64(op.Label), Validity: op.Validity,
	})
	if err != nil {
		return ""
	}
	return string(payload)
}

// FinishOperation drops an action the server made.
func (s *Store) FinishOperation(ctx context.Context, id int64) error {
	_, err := s.exec(ctx, `DELETE FROM pending_operations WHERE id = ?`, id)
	return err
}

// RetryOperation puts an action back for a later try, one attempt on.
func (s *Store) RetryOperation(ctx context.Context, id int64, nextTry time.Time, reason string) error {
	_, err := s.exec(ctx, `UPDATE pending_operations
		SET state = ?, claimed_at = 0, attempts = attempts + 1, next_try = ?, last_error = ?
		WHERE id = ?`, string(mail.OperationPending), nextTry.Unix(), reason, id)
	return err
}

// FailOperation keeps an action the server refused, with its reason.
func (s *Store) FailOperation(ctx context.Context, id int64, reason string) error {
	_, err := s.exec(ctx, `UPDATE pending_operations
		SET state = ?, claimed_at = 0, attempts = attempts + 1, last_error = ?
		WHERE id = ?`, string(mail.OperationFailed), reason, id)
	return err
}

// RetryFailedOperations puts the actions the server refused back in the
// queue, due now, and returns how many there were: the user asked for
// them to be tried again.
func (s *Store) RetryFailedOperations(ctx context.Context, accountID string, nextTry time.Time) (int, error) {
	rows, err := s.query(ctx, `UPDATE pending_operations
		SET state = ?, next_try = ?, claimed_at = 0
		WHERE account_id = ? AND state = ? RETURNING id`,
		string(mail.OperationPending), nextTry.Unix(), accountID, string(mail.OperationFailed))
	if err != nil {
		return 0, err
	}
	return len(rows.Values), nil
}

// Operations reads the actions of an account that are not done, oldest
// first.
func (s *Store) Operations(ctx context.Context, accountID string) ([]mail.PendingOperation, error) {
	rows, err := s.query(ctx, `SELECT `+operationColumns+` FROM pending_operations
		WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.PendingOperation, 0, len(rows.Values))
	for _, r := range rows.Values {
		op, err := operationRow(r)
		if err != nil {
			return out, err
		}
		out = append(out, op)
	}
	return out, nil
}

// ReclaimOperations gives back the actions a run left claimed when it
// ended: the next run makes them.
func (s *Store) ReclaimOperations(ctx context.Context, accountID string) error {
	_, err := s.exec(ctx, `UPDATE pending_operations
		SET state = ?, claimed_at = 0
		WHERE account_id = ? AND state = ?`,
		string(mail.OperationPending), accountID, string(mail.OperationClaimed))
	return err
}

// CancelOperations drops actions before the server makes them, as an undo
// of something still queued. What is not there is no failure.
func (s *Store) CancelOperations(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		marks = append(marks, "?")
	}
	_, err := s.exec(ctx, `DELETE FROM pending_operations WHERE id IN (`+strings.Join(marks, ", ")+`)`, args...)
	return err
}

// unixOf is a time as the store keeps it, zero for none.
func unixOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// operationRow reads an action of a query.
func operationRow(r []any) (mail.PendingOperation, error) {
	var payload operationPayload
	if raw := text(r[6]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return mail.PendingOperation{}, fmt.Errorf("store: queued action %v: %w", r[0], err)
		}
	}
	return mail.PendingOperation{
		ID:        int64(number(r[0])),
		AccountID: text(r[1]),
		Kind:      mail.OperationKind(text(r[2])),
		Folder:    text(r[3]),
		UID:       mail.UID(number(r[4])),
		Dest:      text(r[5]),
		Flag:      mail.Flag(payload.Flag),
		On:        payload.On,
		Label:     mail.LabelID(payload.Label),
		Validity:  payload.Validity,
		State:     mail.OperationState(text(r[7])),
		Attempts:  int(number(r[8])),
		NextTry:   unix(r[9]),
		CreatedAt: unix(r[10]),
		ClaimedAt: unix(r[11]),
		LastError: text(r[12]),
	}, nil
}

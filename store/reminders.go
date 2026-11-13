package store

import (
	"context"
	"time"

	"shu/mail"
)

// Reminders are one table: a conversation of an account the user asked to
// come back to the inbox, with the time it is due (mail.Reminder). The
// times are kept as whole seconds since the epoch, as a message's date is.

const reminderColumns = `id, account_id, thread_id, subject, remind_at, created_at`

// reminder reads a row of the reminders table.
func reminder(r []any) mail.Reminder {
	return mail.Reminder{
		ID:        number(r[0]),
		Account:   text(r[1]),
		Thread:    mail.ThreadID(number(r[2])),
		Subject:   text(r[3]),
		RemindAt:  time.Unix(number(r[4]), 0).UTC(),
		CreatedAt: time.Unix(number(r[5]), 0).UTC(),
	}
}

// Reminders lists an account's reminders, the soonest due first.
func (s *Store) Reminders(ctx context.Context, accountID string) ([]mail.Reminder, error) {
	rows, err := s.query(ctx, `SELECT `+reminderColumns+` FROM reminders
		WHERE account_id = ? ORDER BY remind_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Reminder, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, reminder(r))
	}
	return out, nil
}

// DueReminders lists the reminders of every account that are due at a
// time, the soonest due first.
func (s *Store) DueReminders(ctx context.Context, at time.Time) ([]mail.Reminder, error) {
	rows, err := s.query(ctx, `SELECT `+reminderColumns+` FROM reminders
		WHERE remind_at <= ? ORDER BY remind_at, id`, at.Unix())
	if err != nil {
		return nil, err
	}
	out := make([]mail.Reminder, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, reminder(r))
	}
	return out, nil
}

// SaveReminder writes a reminder, or the due time and subject of the
// account's reminder for the conversation when it has one: the ID it had
// stays.
func (s *Store) SaveReminder(ctx context.Context, r mail.Reminder) (mail.Reminder, error) {
	at, created := r.RemindAt.Unix(), r.CreatedAt.Unix()
	rows, err := s.query(ctx, `INSERT INTO reminders (account_id, thread_id, subject, remind_at, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account_id, thread_id) DO UPDATE SET subject = excluded.subject, remind_at = excluded.remind_at
		RETURNING id, remind_at, created_at`,
		r.Account, int64(r.Thread), r.Subject, at, created)
	if err != nil {
		return mail.Reminder{}, err
	}
	r.ID = number(rows.Values[0][0])
	r.RemindAt = time.Unix(number(rows.Values[0][1]), 0).UTC()
	r.CreatedAt = time.Unix(number(rows.Values[0][2]), 0).UTC()
	return r, nil
}

// DeleteReminder takes a reminder off a conversation. One there is not is
// already gone.
func (s *Store) DeleteReminder(ctx context.Context, accountID string, thread mail.ThreadID) error {
	_, err := s.exec(ctx, `DELETE FROM reminders WHERE account_id = ? AND thread_id = ?`, accountID, int64(thread))
	return err
}

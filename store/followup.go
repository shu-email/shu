package store

import (
	"context"
	"time"

	"shu/mail"
)

// CountFollowUps counts the conversations a follow-up scope reads, the
// ones whose newest message in the scope waits for a reply.
func (s *Store) CountFollowUps(ctx context.Context, q mail.ListQuery) (int, error) {
	p, err := s.plan(ctx, q, nil)
	if err != nil {
		return 0, err
	}
	if len(p.folders) == 0 || !p.follow {
		return 0, nil
	}
	where, args := p.inScope("m", p.folders, false)
	newest, newestArgs := p.newestCond("m")
	cond, condArgs := p.followUpCond("m", "f")
	args = append(args, newestArgs...)
	args = append(args, condArgs...)
	rows, err := s.query(ctx, `SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE `+where+` AND m.deleted = 0 AND `+newest+` AND `+cond, args...)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, nil
	}
	return int(number(rows.Values[0][0])), nil
}

// DismissFollowUp notes that the user took a conversation off the
// follow-up list at a time: it stays off until a message newer than it is
// cached. The dismissal is this device's only.
func (s *Store) DismissFollowUp(ctx context.Context, accountID string, thread mail.ThreadID, at time.Time) error {
	_, err := s.exec(ctx, `INSERT INTO follow_up_dismissals (account_id, thread_id, dismissed_at) VALUES (?, ?, ?)
		ON CONFLICT (account_id, thread_id) DO UPDATE SET dismissed_at = excluded.dismissed_at`,
		accountID, int64(thread), at.Unix())
	return err
}

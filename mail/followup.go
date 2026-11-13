package mail

import (
	"context"
	"time"
)

// Follow Up is a list derived from the cache alone: the conversations
// whose newest message that is neither a draft nor junk or trash is one
// the user sent, old enough that an answer should have come and not so
// old that the thread is forgotten. The user can dismiss one; it stays
// out of the list until a newer message of it appears.

// FollowUpMinAge is how long a conversation waits before it is followed
// up: a reply may still be on its way before.
const FollowUpMinAge = 3 * 24 * time.Hour

// FollowUpMaxAge is how old the sent message may be: an older
// conversation is no longer waiting for an answer.
const FollowUpMaxAge = 30 * 24 * time.Hour

// FollowUpAge is how long a conversation has waited at now, in whole
// days, or false when its sent message is outside the ages followed up.
func FollowUpAge(now, sent time.Time) (int, bool) {
	age := now.Sub(sent)
	if age < FollowUpMinAge || age > FollowUpMaxAge {
		return 0, false
	}
	return int(age / (24 * time.Hour)), true
}

// DismissFollowUp takes a conversation off the follow-up list, where it
// stays until a newer message of it is cached. The follow-up list is the
// cache's own; no server hears of the dismissal.
func (s *Service) DismissFollowUp(ctx context.Context, accountID string, thread ThreadID) error {
	return s.store.DismissFollowUp(ctx, accountID, thread, s.now())
}

// FollowUpCount is how many conversations wait for a reply now.
func (s *Service) FollowUpCount(ctx context.Context, accountID string) (int, error) {
	return s.store.CountFollowUps(ctx, ListQuery{Scope: FollowUpScope(accountID, s.now())})
}

package mail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
)

// The queue: what the user asked of the mail that the server has not
// made yet (pending_operations, migration v21, store/pending.go). An
// action is written down before the server is touched, so it outlives a
// crash, a restart or a network that is not there. The call that asked
// makes it at once, on the connection it already needs; what it could
// not make, because the server was out of reach, waits for the
// account's one worker. That worker takes the oldest action with a
// single state change, so two workers never run the same one, makes it,
// and drops it; an action the server refuses stays failed with its
// reason for the window to show. Nothing here sends anything of the
// user's: only the commands the user asked for.

// OperationKind is what a queued action does, from a closed set.
type OperationKind string

const (
	// OpMove moves a message from its folder to Dest: archive, junk,
	// trash or a folder of the user's.
	OpMove OperationKind = "move"
	// OpFlags changes \Seen or \Flagged on a message.
	OpFlags OperationKind = "flags"
	// OpLabel puts a label of the message's account on it, or takes it
	// off.
	OpLabel OperationKind = "label"
)

// OperationState is where a queued action stands.
type OperationState string

const (
	// OperationPending waits for the server, now or after NextTry.
	OperationPending OperationState = "pending"
	// OperationClaimed is one worker's, and no other's.
	OperationClaimed OperationState = "claimed"
	// OperationFailed is one the server refused: it stays, with its
	// reason, until the user does something about it.
	OperationFailed OperationState = "failed"
)

// PendingOperation is one message's queued action.
type PendingOperation struct {
	ID        int64
	AccountID string
	Kind      OperationKind
	Folder    string
	UID       UID
	// Dest is where an OpMove moves the message.
	Dest string
	// Flag and On are what an OpFlags sets; Label and On what an
	// OpLabel adds or takes off.
	Flag  Flag
	On    bool
	Label LabelID
	// Validity is the folder's UIDVALIDITY when the action was queued: a
	// folder the server reset since no longer knows these UIDs.
	Validity  uint32
	State     OperationState
	Attempts  int
	NextTry   time.Time
	CreatedAt time.Time
	ClaimedAt time.Time
	LastError string
}

// QueueStatus is what an account's queued actions are doing, for the
// window to say in its own words. The window hears of it as a
// WatchEvent with Queue set.
type QueueStatus struct {
	// Pending is how many actions wait for the server.
	Pending int
	// Failed is how many the server refused.
	Failed int
	// Reason is what the last refusal said.
	Reason string
}

// The queue's waits: after a failure the worker tries again, and the
// wait doubles with each failure in a row; an empty queue is looked at
// again within the minute.
const (
	queueBackoffMin = 5 * time.Second
	queueBackoffMax = 10 * time.Minute
	queueIdleWait   = time.Minute
)

// queueWorker makes one account's queued actions, oldest first, off the
// UI thread.
type queueWorker struct {
	svc       *Service
	accountID string
	report    func(WatchEvent)

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}

	failures int
	last     QueueStatus
}

// startQueue starts the worker of an account, or replaces the one it
// had. watchMu is held.
func (s *Service) startQueue(accountID string, report func(WatchEvent)) *queueWorker {
	ctx, cancel := context.WithCancel(context.Background())
	q := &queueWorker{
		svc: s, accountID: accountID, report: report,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	return q
}

// run is the worker's goroutine: claim, make, drop, and again, until
// stopped.
func (q *queueWorker) run() {
	defer close(q.done)
	// What a run that ended left claimed is this run's to make.
	_ = q.svc.store.ReclaimOperations(q.ctx, q.accountID)
	q.say()
	for {
		if q.ctx.Err() != nil {
			return
		}
		op, ok, err := q.svc.store.ClaimOperation(q.ctx, q.accountID, q.svc.now())
		if err != nil {
			if !q.sleep(queueBackoffMin) {
				return
			}
			continue
		}
		if !ok {
			// Nothing due; a nudge looks again, and so does the minute.
			q.say()
			if !q.sleep(queueIdleWait) {
				return
			}
			continue
		}
		// The ones that go in one command with this one, which is already
		// this worker's.
		like, err := q.svc.store.ClaimOperationsByKey(q.ctx, op, q.svc.now())
		if err != nil {
			like = nil
		}
		group := append([]PendingOperation{op}, like...)
		err = q.svc.applyOperations(q.ctx, group)
		if q.ctx.Err() != nil {
			// The app is quitting: the action was not made, and stays
			// claimed for the next run to reclaim rather than fail.
			return
		}
		switch {
		case err == nil:
			q.svc.finishOperations(q.ctx, group)
			q.failures = 0
		case retryable(err):
			wait := queueBackoff(q.failures)
			q.failures++
			next := q.svc.now().Add(wait)
			for _, o := range group {
				_ = q.svc.store.RetryOperation(context.WithoutCancel(q.ctx), o.ID, next, err.Error())
			}
			q.say()
			if !q.sleep(wait) {
				return
			}
		default:
			q.svc.failOperations(q.ctx, group, err)
			q.say()
		}
	}
}

// sleep waits for the next look, a nudge, or the worker's end; false
// when it ended.
func (q *queueWorker) sleep(d time.Duration) bool {
	timer, stop := q.svc.queueTimer(d)
	defer stop()
	select {
	case <-q.ctx.Done():
		return false
	case <-q.wake:
		return true
	case <-timer:
		return true
	}
}

// say reports what the queue holds, when it changed.
func (q *queueWorker) say() {
	ops, err := q.svc.store.Operations(context.WithoutCancel(q.ctx), q.accountID)
	if err != nil {
		return
	}
	var st QueueStatus
	for _, op := range ops {
		if op.State == OperationFailed {
			st.Failed++
			if op.LastError != "" {
				st.Reason = op.LastError
			}
			continue
		}
		st.Pending++
	}
	if st == q.last {
		return
	}
	q.last = st
	q.report(WatchEvent{AccountID: q.accountID, Queue: &st})
}

// queueBackoff is the wait after failures in a row.
func queueBackoff(failures int) time.Duration {
	wait := queueBackoffMin
	for i := 0; i < failures && wait < queueBackoffMax; i++ {
		wait *= 2
	}
	if wait > queueBackoffMax {
		wait = queueBackoffMax
	}
	return wait
}

// retryable reports whether an action may work later: the server could
// not be reached, or the account's sign-in is gone.
func retryable(err error) bool {
	switch failureOf(err) {
	case WatchFailNetwork, WatchFailAuth, WatchFailSignIn:
		return true
	}
	return false
}

// applyOperations makes what a worker claimed. The actions are those of
// one folder and one change.
func (s *Service) applyOperations(ctx context.Context, ops []PendingOperation) error {
	if len(ops) == 0 {
		return nil
	}
	first := ops[0]
	folder, ok, err := s.store.Folder(ctx, first.AccountID, first.Folder)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("mail: folder %s is not cached: %w", first.Folder, ErrNotFound)
	}
	if first.Validity != 0 && folder.UIDValidity != first.Validity {
		return fmt.Errorf("mail: the server reset %s since the action was queued", first.Folder)
	}
	uids := uidsOf(ops)
	switch first.Kind {
	case OpMove:
		a, password, err := s.account(ctx, first.AccountID)
		if err != nil {
			return err
		}
		if _, err := s.moveOnServer(ctx, a, password, first.Folder, uids, first.Dest); err != nil {
			// The move may have been made before the app ended, which
			// left the action to be reclaimed: the messages are gone from
			// the folder, and there is nothing left to do.
			if gone, checkErr := s.uidsGone(ctx, a, password, first.Folder, uids); checkErr != nil || !gone {
				return err
			}
		}
		for _, uid := range uids {
			// The move is made: the row goes, and a sync of dest brings
			// the message there.
			if err := s.store.DeleteMessage(ctx, first.AccountID, first.Folder, uid); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return nil
	case OpFlags:
		a, password, err := s.account(ctx, first.AccountID)
		if err != nil {
			return err
		}
		c, err := s.dialIMAP(ctx, a, password)
		if err != nil {
			return err
		}
		defer c.close()
		return storeFlagsIn(c, first.Folder, first.Validity, uids, first.Flag, first.On)
	case OpLabel:
		label, err := s.label(ctx, first.AccountID, first.Label)
		if err != nil {
			return err
		}
		c, err := s.labelSession(ctx, first.AccountID)
		if err != nil {
			return err
		}
		defer c.close()
		opened, err := s.openToChange(ctx, c, first.AccountID, folder)
		if err != nil {
			return err
		}
		if first.On && !opened.KeepsKeyword(label.Keyword) {
			// The folder keeps no such keyword now: the label is this
			// device's, and the syncs read the folder's own flags.
			return nil
		}
		return storeKeyword(c, first.Folder, uids, label.Keyword, first.On)
	}
	return fmt.Errorf("mail: unknown queued action %q", first.Kind)
}

// uidsGone reports whether a folder has none of the messages: one moved
// already, or deleted meanwhile, leaves none of them.
func (s *Service) uidsGone(ctx context.Context, a Account, password, folder string, uids []UID) (bool, error) {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return false, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return false, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	data, err := c.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return false, fmt.Errorf("imap: search %s: %w", folder, err)
	}
	return len(data.AllUIDs()) == 0, nil
}

// enqueueOperations writes actions under the account, claimed by the call
// that is about to make them: no worker takes them first, and a run that
// ends before they are dropped leaves them to be reclaimed.
func (s *Service) enqueueOperations(ctx context.Context, accountID string, ops []PendingOperation) ([]int64, error) {
	now := s.now()
	for i := range ops {
		ops[i].AccountID = accountID
		ops[i].State = OperationClaimed
		ops[i].CreatedAt = now
		ops[i].ClaimedAt = now
	}
	return s.store.EnqueueOperations(ctx, ops)
}

// RetryFailedActions tries the actions an account's server refused once
// more, as the window offers when the user has dealt with the cause, and
// returns how many went back to the queue.
func (s *Service) RetryFailedActions(ctx context.Context, accountID string) (int, error) {
	n, err := s.store.RetryFailedOperations(ctx, accountID, s.now())
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.wakeQueue(accountID)
	}
	return n, nil
}

// requeueOperations leaves actions the server could not be reached for to
// its account's worker, which says so before it tries them again.
func (s *Service) requeueOperations(ctx context.Context, accountID string, ids []int64) {
	bg := context.WithoutCancel(ctx)
	next := s.now().Add(queueBackoffMin)
	for _, id := range ids {
		_ = s.store.RetryOperation(bg, id, next, "")
	}
	s.wakeQueue(accountID)
}

// wakeQueue has an account's worker look at its actions now.
func (s *Service) wakeQueue(accountID string) {
	s.watchMu.Lock()
	q := s.queues[accountID]
	s.watchMu.Unlock()
	if q != nil {
		signal(q.wake)
	}
}

// dropOperations drops actions by ID, once the server made them.
func (s *Service) dropOperations(ctx context.Context, ids []int64) {
	bg := context.WithoutCancel(ctx)
	for _, id := range ids {
		_ = s.store.FinishOperation(bg, id)
	}
}

// allIDs flattens the IDs of several groups.
func allIDs(groups [][]int64) []int64 {
	var out []int64
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// finishOperations drops actions the server made.
func (s *Service) finishOperations(ctx context.Context, ops []PendingOperation) {
	bg := context.WithoutCancel(ctx)
	for _, op := range ops {
		_ = s.store.FinishOperation(bg, op.ID)
	}
}

// failOperations keeps actions the server refused, with the reason, and
// puts back what the cache showed before them.
func (s *Service) failOperations(ctx context.Context, ops []PendingOperation, cause error) {
	bg := context.WithoutCancel(ctx)
	for _, op := range ops {
		_ = s.store.FailOperation(bg, op.ID, cause.Error())
		if op.Kind == OpMove {
			// The message is where it was: its row is no tombstone.
			_ = s.store.MarkDeleted(bg, op.AccountID, op.Folder, op.UID, false)
		}
	}
}

// uidsOf reads the messages of a group of queued actions, each once.
func uidsOf(ops []PendingOperation) []UID {
	out := make([]UID, 0, len(ops))
	seen := map[UID]bool{}
	for _, op := range ops {
		if seen[op.UID] {
			continue
		}
		seen[op.UID] = true
		out = append(out, op.UID)
	}
	return out
}

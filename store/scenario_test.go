package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// The write paths as a matrix. Every case starts from its own store,
// ends with the invariants every write must leave, and is checked from
// the outside: the rows a list shows, the tombstones it hides, the
// folder's counts, the search index and the conversation totals. A case
// that fails names itself.

var scenarioNow = time.Unix(1_700_000_000, 0)

// scenarioSync is a sync of a folder that cached uids, newest last.
func scenarioSync(folder string, validity uint32, uids ...mail.UID) mail.SyncBatch {
	messages := make([]mail.Message, 0, len(uids))
	for i, uid := range uids {
		messages = append(messages, mail.Message{Summary: mail.Summary{
			UID:       uid,
			Subject:   fmt.Sprintf("%s %d", folder, uid),
			From:      mail.Address{Name: "Ann", Email: "ann@example.com"},
			Date:      scenarioNow.Add(time.Duration(i) * time.Minute),
			MessageID: fmt.Sprintf("<%s-%d@example.com>", folder, uid),
		}})
	}
	return mail.SyncBatch{
		Folder: mail.Folder{
			Name: folder, UIDValidity: validity, LastUID: uids[len(uids)-1],
			Total: len(uids), Unread: len(uids), SyncedAt: scenarioNow,
		},
		Messages: messages,
	}
}

// scenarioInvariants proves what every write must leave: no reference to
// a row that is not there, the search index in step, and the
// conversations' totals equal to their messages. It writes nothing.
func scenarioInvariants(t *testing.T, s *Store) {
	t.Helper()
	findings, err := s.CheckCache(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("the cache is out of step: %+v", findings)
	}
}

// visibleAndTombstones counts what a list shows and what a move hides.
func visibleAndTombstones(t *testing.T, s *Store) (visible, tombstones int64) {
	t.Helper()
	return countRows(t, s, `SELECT COUNT(*) FROM messages WHERE deleted = 0`),
		countRows(t, s, `SELECT COUNT(*) FROM messages WHERE deleted = 1`)
}

func scenarioStore(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	return s
}

func TestMessageWriteScenarioMatrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		write      func(t *testing.T, s *Store)
		visible    int64
		tombstones int64
	}{
		{
			name: "sync",
			write: func(t *testing.T, s *Store) {
				if err := s.ApplySync(ctx, "a1", scenarioSync("INBOX", 1, 1, 2)); err != nil {
					t.Fatal(err)
				}
				if f, _, _ := s.Folder(ctx, "a1", "INBOX"); f.Total != 2 || f.Unread != 2 {
					t.Errorf("folder after the sync = %+v", f)
				}
			},
			visible: 2,
		},
		{
			name: "the same sync again",
			write: func(t *testing.T, s *Store) {
				batch := scenarioSync("INBOX", 1, 1, 2)
				for i := 0; i < 2; i++ {
					if err := s.ApplySync(ctx, "a1", batch); err != nil {
						t.Fatal(err)
					}
				}
			},
			visible: 2,
		},
		{
			name: "an unconfirmed move, restored",
			write: func(t *testing.T, s *Store) {
				if err := s.ApplySync(ctx, "a1", scenarioSync("INBOX", 1, 1)); err != nil {
					t.Fatal(err)
				}
				if err := s.MarkDeleted(ctx, "a1", "INBOX", 1, true); err != nil {
					t.Fatal(err)
				}
				// Between the two steps the mail is a tombstone, hidden
				// from the list, which is what an undone move puts back.
				if visible, tombstones := visibleAndTombstones(t, s); visible != 0 || tombstones != 1 {
					t.Fatalf("while the move waits: %d visible, %d tombstones", visible, tombstones)
				}
				if err := s.MarkDeleted(ctx, "a1", "INBOX", 1, false); err != nil {
					t.Fatal(err)
				}
			},
			visible: 1,
		},
		{
			name: "a confirmed move",
			write: func(t *testing.T, s *Store) {
				if err := s.ApplySync(ctx, "a1", scenarioSync("INBOX", 1, 1)); err != nil {
					t.Fatal(err)
				}
				if err := s.MarkDeleted(ctx, "a1", "INBOX", 1, true); err != nil {
					t.Fatal(err)
				}
				if visible, tombstones := visibleAndTombstones(t, s); visible != 0 || tombstones != 1 {
					t.Fatalf("after the delete flag: %d visible, %d tombstones", visible, tombstones)
				}
				if err := s.DeleteMessage(ctx, "a1", "INBOX", 1); err != nil {
					t.Fatal(err)
				}
			},
			tombstones: 0,
		},
		{
			name: "the server reset UIDVALIDITY",
			write: func(t *testing.T, s *Store) {
				if err := s.ApplySync(ctx, "a1", scenarioSync("INBOX", 1, 1)); err != nil {
					t.Fatal(err)
				}
				reset := scenarioSync("INBOX", 2, 5)
				reset.Reset = true
				if err := s.ApplySync(ctx, "a1", reset); err != nil {
					t.Fatal(err)
				}
			},
			visible: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scenarioStore(t)
			c.write(t, s)
			scenarioInvariants(t, s)
			if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE deleted = 0`); got != c.visible {
				t.Errorf("visible messages = %d, want %d", got, c.visible)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE deleted = 1`); got != c.tombstones {
				t.Errorf("tombstones = %d, want %d", got, c.tombstones)
			}
		})
	}
}

// A write to a folder that is not cached fails whole: no message, no
// conversation and no full-text row is left behind.
func TestWriteToAMissingFolderChangesNothing(t *testing.T) {
	ctx := context.Background()
	s := scenarioStore(t)
	err := s.ApplySync(ctx, "a1", scenarioSync("Missing", 1, 1))
	if !errors.Is(err, mail.ErrNotFound) {
		t.Fatalf("sync of a missing folder = %v", err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages`); got != 0 {
		t.Errorf("messages left by a failed sync = %d", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM threads`); got != 0 {
		t.Errorf("conversations left by a failed sync = %d", got)
	}
	if _, ok, _ := s.Folder(ctx, "a1", "Missing"); ok {
		t.Error("the failed sync made the folder")
	}
	scenarioInvariants(t, s)
}

// The cache is the database file: a tombstone, a queued action and the
// folder's cursor are what a restart finds.
func TestWriteScenariosSurviveRestart(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", scenarioSync("INBOX", 7, 1, 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 1, true); err != nil {
		t.Fatal(err)
	}
	ops := queueOps()
	// The queued move is the tombstoned message's own: it waits for the
	// server to make it after the restart.
	ops[0].UID, ops[0].Validity = 1, 7
	if _, err := s.EnqueueOperations(ctx, ops[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scenarioInvariants(t, s)
	msgs, err := s.ListMessages(ctx, "a1", "INBOX", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].UID != 2 {
		t.Errorf("after the restart the list = %+v", msgs)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE deleted = 1`); got != 1 {
		t.Errorf("tombstones after the restart = %d", got)
	}
	queued, err := s.Operations(ctx, "a1")
	if err != nil || len(queued) != 1 || queued[0].State != mail.OperationPending {
		t.Errorf("queue after the restart = %+v, %v", queued, err)
	}
	if f, _, _ := s.Folder(ctx, "a1", "INBOX"); f.UIDValidity != 7 || f.LastUID != 2 {
		t.Errorf("cursor after the restart = %+v", f)
	}
}

// One outbox entry is sent or taken back by one claim: the send and the
// undo race, every worker may try, and exactly one wins.
func TestOutboxClaimHasOneWinner(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
		Envelope: mail.Outgoing{To: []mail.Address{{Email: "a@example.com"}}},
		State:    mail.StateScheduled, DueAt: scenarioNow, CreatedAt: scenarioNow,
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	type win struct {
		won bool
		to  string
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	wins := make(chan win, 8)
	for i := 0; i < 8; i++ {
		to := mail.StateSending
		if i%2 == 1 {
			to = mail.StateCancelled
		}
		wg.Add(1)
		go func(to string) {
			defer wg.Done()
			won, err := s.ClaimOutbox(ctx, "o1", mail.StateScheduled, to)
			if err != nil {
				errs <- err
				return
			}
			wins <- win{won: won, to: to}
		}(to)
	}
	wg.Wait()
	close(errs)
	close(wins)
	for err := range errs {
		t.Fatalf("claim: %v", err)
	}
	var won int
	var winner string
	for w := range wins {
		if w.won {
			won++
			winner = w.to
		}
	}
	if won != 1 {
		t.Errorf("%d claims won, want 1", won)
	}
	if got, err := s.Outbox(ctx, "o1"); err != nil || got.State != winner {
		t.Errorf("outbox after the claims = %+v, %v; want the winner's %q", got, err, winner)
	}
	scenarioInvariants(t, s)
}

// The queue's workers never make one action twice: every action is
// claimed exactly once, whatever the number of workers.
func TestQueueClaimHasOneWinnerPerAction(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps())
	if err != nil || len(ids) != 3 {
		t.Fatalf("enqueue = %v, %v", ids, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := map[int64]int{}
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				op, ok, err := s.ClaimOperation(ctx, "a1", scenarioNow)
				if err != nil {
					errs <- err
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claimed[op.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 3 {
		t.Errorf("claimed %d of 3 actions", len(claimed))
	}
	for id, times := range claimed {
		if times != 1 {
			t.Errorf("action %d was claimed %d times", id, times)
		}
	}
	if left, _ := s.Operations(ctx, "a1"); len(left) != 3 {
		t.Errorf("queue after the claims = %+v", left)
	}
	scenarioInvariants(t, s)
}

// A retry puts the action back with a later time, and it is not claimed
// before that time; a failure stays for the window.
func TestQueueRetryScenarios(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if _, err := s.EnqueueOperations(ctx, queueOps()[:2]); err != nil {
		t.Fatal(err)
	}
	op, ok, err := s.ClaimOperation(ctx, "a1", scenarioNow)
	if err != nil || !ok {
		t.Fatalf("claim = %+v, %v", op, err)
	}
	other, ok, err := s.ClaimOperation(ctx, "a1", scenarioNow)
	if err != nil || !ok {
		t.Fatalf("second claim = %+v, %v", other, err)
	}
	if err := s.FinishOperations(ctx, []int64{other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOperation(ctx, op.ID, scenarioNow.Add(time.Minute), "the server is out of reach"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimOperation(ctx, "a1", scenarioNow); ok {
		t.Error("a retried action was claimed before its time")
	}
	again, ok, err := s.ClaimOperation(ctx, "a1", scenarioNow.Add(time.Minute))
	if err != nil || !ok || again.ID != op.ID || again.Attempts != 1 {
		t.Errorf("claim after the wait = %+v, %v", again, err)
	}
	if err := s.FailOperation(ctx, again.ID, "the server refused it"); err != nil {
		t.Fatal(err)
	}
	left, err := s.Operations(ctx, "a1")
	if err != nil || len(left) != 1 || left[0].State != mail.OperationFailed {
		t.Errorf("queue after the failure = %+v, %v", left, err)
	}
	if err := s.FinishOperations(ctx, []int64{again.ID}); err != nil {
		t.Fatal(err)
	}
	scenarioInvariants(t, s)
}

package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"shu/mail"
)

// queueOps is a few actions of one account, as an offline archive, star
// and label write them.
func queueOps() []mail.PendingOperation {
	return []mail.PendingOperation{
		{AccountID: "a1", Kind: mail.OpMove, Folder: "INBOX", UID: 7, Dest: "Archive", Validity: 42},
		{AccountID: "a1", Kind: mail.OpFlags, Folder: "INBOX", UID: 8, Flag: mail.FlagFlagged, On: true, Validity: 42},
		{AccountID: "a1", Kind: mail.OpLabel, Folder: "INBOX", UID: 9, Label: 3, On: true, Validity: 42},
	}
}

func TestQueueKeepsWhatTheServerMustDo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3", ids)
	}
	got, err := s.Operations(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("operations = %d, want 3", len(got))
	}
	if got[0].State != mail.OperationPending {
		t.Errorf("state = %q, want pending", got[0].State)
	}
	if got[0].Kind != mail.OpMove || got[0].Folder != "INBOX" || got[0].UID != 7 || got[0].Dest != "Archive" {
		t.Errorf("move = %+v", got[0])
	}
	if got[1].Flag != mail.FlagFlagged || !got[1].On {
		t.Errorf("flags = %+v", got[1])
	}
	if got[2].Label != 3 || !got[2].On {
		t.Errorf("label = %+v", got[2])
	}
	if got[0].Validity != 42 || got[2].Validity != 42 {
		t.Errorf("validity = %d, %d, want 42", got[0].Validity, got[2].Validity)
	}
}

func TestQueueClaimsOneActionOnce(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if _, err := s.EnqueueOperations(ctx, queueOps()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	op, ok, err := s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if op.UID != 7 || op.State != mail.OperationClaimed {
		t.Fatalf("claimed = %+v", op)
	}
	// The oldest first, and never the same one twice.
	op, ok, err = s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok {
		t.Fatalf("second claim = %v, %v", ok, err)
	}
	if op.UID != 8 {
		t.Errorf("second = %d, want the next one", op.UID)
	}
	// Another account's actions are not this account's.
	if op, ok, err = s.ClaimOperation(ctx, "a2", now); err != nil || ok {
		t.Errorf("claim for a2 = %+v, %v, %v; want none", op, ok, err)
	}
}

func TestQueueClaimsItsLikeInOneCommand(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if _, err := s.EnqueueOperations(ctx, []mail.PendingOperation{
		{AccountID: "a1", Kind: mail.OpMove, Folder: "INBOX", UID: 1, Dest: "Archive", Validity: 4},
		{AccountID: "a1", Kind: mail.OpMove, Folder: "INBOX", UID: 2, Dest: "Archive", Validity: 4},
		{AccountID: "a1", Kind: mail.OpMove, Folder: "INBOX", UID: 3, Dest: "Trash", Validity: 4},
		{AccountID: "a1", Kind: mail.OpMove, Folder: "Sent", UID: 1, Dest: "Archive", Validity: 4},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	op, ok, err := s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	group, err := s.ClaimOperationsByKey(ctx, op, now)
	if err != nil {
		t.Fatal(err)
	}
	// The claimed action is the caller's, not part of the group.
	if len(group) != 1 || group[0].UID != 2 {
		t.Fatalf("group = %+v, want the other move to Archive", group)
	}
	// What is left is another destination and another folder.
	op, ok, err = s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok || op.UID != 3 {
		t.Fatalf("next = %+v, %v, %v", op, ok, err)
	}
	op, ok, err = s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok || op.Folder != "Sent" {
		t.Fatalf("next = %+v, %v, %v", op, ok, err)
	}
}

func TestQueueRetryWaitsAndKeepsTheReason(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps()[:1])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, _ := s.ClaimOperation(ctx, "a1", now); !ok {
		t.Fatal("nothing claimed")
	}
	later := now.Add(time.Minute)
	if err := s.RetryOperation(ctx, ids[0], later, "imap: connection reset"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimOperation(ctx, "a1", now); err != nil || ok {
		t.Fatalf("claimed again before its time: %v, %v", ok, err)
	}
	op, ok, err := s.ClaimOperation(ctx, "a1", later)
	if err != nil || !ok {
		t.Fatalf("claim at its time = %v, %v", ok, err)
	}
	if op.Attempts != 1 || op.LastError != "imap: connection reset" || op.State != mail.OperationClaimed {
		t.Errorf("retried = %+v", op)
	}
}

func TestQueueFailureStaysForTheWindow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps()[:1])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, _ := s.ClaimOperation(ctx, "a1", now); !ok {
		t.Fatal("nothing claimed")
	}
	if err := s.FailOperation(ctx, ids[0], "mail: folder Archive is gone"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Operations(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != mail.OperationFailed || got[0].LastError != "mail: folder Archive is gone" {
		t.Fatalf("failed = %+v", got)
	}
	// A failed action is no worker's to try again by itself.
	if _, ok, err := s.ClaimOperation(ctx, "a1", now.Add(time.Hour)); err != nil || ok {
		t.Errorf("a failed action was claimed: %v, %v", ok, err)
	}
	// The user asked for another try: it is the worker's again.
	if n, err := s.RetryFailedOperations(ctx, "a1", now); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v; want one", n, err)
	}
	if op, ok, err := s.ClaimOperation(ctx, "a1", now); err != nil || !ok || op.ID != ids[0] {
		t.Errorf("after asking again = %+v, %v, %v", op, ok, err)
	}
	if err := s.FinishOperations(ctx, []int64{ids[0]}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Operations(ctx, "a1"); len(got) != 0 {
		t.Errorf("after finishing = %+v", got)
	}
}

func TestQueueReclaimTakesBackWhatARunLeft(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if _, err := s.EnqueueOperations(ctx, queueOps()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, _ := s.ClaimOperation(ctx, "a1", now); !ok {
		t.Fatal("nothing claimed")
	}
	// A claim younger than the grace is a call's, still being made.
	if err := s.ReclaimOperations(ctx, "a1", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Operations(ctx, "a1"); got[0].State != mail.OperationClaimed {
		t.Fatalf("a live claim was taken back: %+v", got[0])
	}
	if err := s.ReclaimOperations(ctx, "a1", now); err != nil {
		t.Fatal(err)
	}
	op, ok, err := s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok {
		t.Fatalf("after reclaim = %v, %v", ok, err)
	}
	if op.State != mail.OperationClaimed || op.UID != 7 {
		t.Errorf("reclaimed = %+v", op)
	}
}

// An action a call wrote and then does not make goes back to the queue
// at once, and with no attempt on it: nothing failed.
func TestQueueReleaseGivesBackACallsClaim(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, ok, _ := s.ClaimOperation(ctx, "a1", now); !ok {
		t.Fatal("nothing claimed")
	}
	if err := s.ReleaseOperations(ctx, []int64{ids[0]}, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Operations(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != mail.OperationPending || !got[0].ClaimedAt.IsZero() || got[0].Attempts != 0 {
		t.Errorf("released = %+v", got[0])
	}
	// The ones the call never held are as they were.
	for _, op := range got[1:] {
		if op.State != mail.OperationPending || !op.ClaimedAt.IsZero() {
			t.Errorf("release touched another action: %+v", op)
		}
	}
	op, ok, err := s.ClaimOperation(ctx, "a1", now)
	if err != nil || !ok || op.ID != ids[0] {
		t.Errorf("after release = %+v, %v, %v", op, ok, err)
	}
}

func TestQueueCancelDropsWhatIsNotMade(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.CancelOperations(ctx, []int64{ids[0], 9999}); err != nil || !slices.Equal(got, []int64{ids[0]}) {
		t.Fatalf("cancel = %v, %v; want the one action", got, err)
	}
	got, err := s.Operations(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("after cancel = %+v", got)
	}
	if got[0].UID != 8 {
		t.Errorf("first = %d, want the second action", got[0].UID)
	}
	// One a worker is making is no longer the caller's to take back.
	if _, ok, _ := s.ClaimOperation(ctx, "a1", time.Now()); !ok {
		t.Fatal("nothing claimed")
	}
	if gone, err := s.CancelOperations(ctx, []int64{ids[1]}); err != nil || len(gone) != 0 {
		t.Fatalf("cancel of a claim = %v, %v; want none", gone, err)
	}
	if got, _ := s.Operations(ctx, "a1"); len(got) != 2 {
		t.Errorf("a claim was dropped by the undo: %+v", got)
	}
}

// A batch is written in one statement, and each ID it gives back is its
// action's.
func TestQueueWritesABatchInOneGo(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	ids, err := s.EnqueueOperations(ctx, queueOps())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOperations(ctx, []int64{ids[1]}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Operations(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UID != 7 || got[1].UID != 9 {
		t.Fatalf("after dropping the second = %+v", got)
	}
}

func TestQueueGoesWithItsAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if _, err := s.EnqueueOperations(ctx, queueOps()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Operations(ctx, "a1"); err != nil || len(got) != 0 {
		t.Errorf("actions left with no account = %+v, %v", got, err)
	}
}

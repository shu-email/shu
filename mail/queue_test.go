package mail

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// startQueueRun runs an account's queue worker as Watch does, and
// collects its reports.
func startQueueRun(t *testing.T, svc *Service, accountID string) (*queueWorker, chan WatchEvent) {
	t.Helper()
	events := make(chan WatchEvent, 64)
	q := svc.startQueue(accountID, func(ev WatchEvent) {
		select {
		case events <- ev:
		default:
		}
	})
	done := make(chan struct{})
	go func() { q.run(); close(done) }()
	t.Cleanup(func() {
		q.cancel()
		<-done
	})
	return q, events
}

// queued reads an account's queued actions.
func queued(t *testing.T, store *memStore, accountID string) []PendingOperation {
	t.Helper()
	ops, err := store.Operations(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// waitFor waits for what a background worker does, which no clock of a
// test can make happen at once.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// reportedQueue is the last queue status an account's worker reported.
func reportedQueue(events chan WatchEvent) (QueueStatus, bool) {
	var (
		st    QueueStatus
		found bool
	)
	for {
		select {
		case ev := <-events:
			if ev.Queue != nil {
				st, found = *ev.Queue, true
			}
		default:
			return st, found
		}
	}
}

// A star the server cannot be told of waits for it, and the change stays
// in the cache meanwhile. It is made once the server is back, and the
// window hears of it.
func TestAFlagMadeOfflineWaitsForTheServer(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	uid := msgs[0].UID

	f.stop()
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{"INBOX", uid}}, FlagFlagged, true); err != nil {
		t.Fatalf("a star with the server away = %v", err)
	}
	if got, _, _ := store.GetMessage(ctx, a.ID, "INBOX", uid); !got.Flagged {
		t.Error("the cache did not show the star")
	}
	ops := queued(t, store, a.ID)
	if len(ops) != 1 || ops[0].Kind != OpFlags || ops[0].UID != uid || ops[0].State != OperationPending {
		t.Fatalf("queued = %+v", ops)
	}
	if !ops[0].NextTry.After(clock.Now()) {
		t.Errorf("next try = %v, want a wait after %v", ops[0].NextTry, clock.Now())
	}

	// The server is back, and the wait is over.
	f.restart(t)
	clock.advance(time.Minute)
	_, events := startQueueRun(t, svc, a.ID)
	waitFor(t, "the star", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 1 {
		t.Errorf("starred on the server = %q", got)
	}
	if st, ok := reportedQueue(events); !ok || st.Pending != 0 || st.Failed != 0 {
		t.Errorf("last status = %+v, %v; want an empty queue", st, ok)
	}
}

// A move the server cannot be told of is queued the same way, its result
// naming the action so that an undo can drop it.
func TestAMoveMadeOfflineWaitsForTheServer(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	uid := msgs[0].UID
	f.stop()

	moved, err := svc.MoveMessages(ctx, a.ID, []MessageRef{{"INBOX", uid}}, "Archive")
	if err != nil || len(moved) != 1 {
		t.Fatalf("move with the server away = %+v, %v", moved, err)
	}
	if moved[0].Pending == 0 || moved[0].Folder != "Archive" || moved[0].From != "INBOX" {
		t.Fatalf("result = %+v", moved[0])
	}
	if got, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); len(got) != len(msgs)-1 {
		t.Errorf("inbox shows %d messages, want %d", len(got), len(msgs)-1)
	}
	if ops := queued(t, store, a.ID); len(ops) != 1 || ops[0].Kind != OpMove || ops[0].Dest != "Archive" {
		t.Fatalf("queued = %+v", ops)
	}

	f.restart(t)
	clock.advance(time.Minute)
	startQueueRun(t, svc, a.ID)
	waitFor(t, "the move", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := f.messages(t, "Archive"); len(got) != 1 {
		t.Errorf("archive on the server = %d messages, want 1", len(got))
	}
}

// An undo of a move the server never heard of drops its queued action:
// the message stays where it was, on this device and on the server.
func TestUndoOfAQueuedMoveDropsIt(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	svc.SetClock(newTestClock().Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	uid := msgs[0].UID
	f.stop()
	moved, err := svc.MoveMessages(ctx, a.ID, []MessageRef{{"INBOX", uid}}, "Archive")
	if err != nil || len(moved) != 1 || moved[0].Pending == 0 {
		t.Fatalf("move with the server away = %+v, %v", moved, err)
	}
	if err := svc.MoveBackMessages(ctx, a.ID, moved); err != nil {
		t.Fatal(err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 0 {
		t.Fatalf("actions left after the undo = %+v", ops)
	}
	if got, _, _ := store.GetMessage(ctx, a.ID, "INBOX", uid); got.UID != uid {
		t.Error("the undo lost the message's row")
	}
	f.restart(t)
	if got := f.messages(t, "INBOX"); len(got) != len(msgs) {
		t.Errorf("inbox on the server = %d messages, want %d", len(got), len(msgs))
	}
	if got := f.messages(t, "Archive"); len(got) != 0 {
		t.Errorf("archive on the server = %d messages, want none", len(got))
	}
}

// What one run queues another one makes: the actions are in the store,
// not in a running service.
func TestAQueuedActionGoesWithTheNextRun(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	f.stop()
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{"INBOX", msgs[0].UID}}, FlagFlagged, true); err != nil {
		t.Fatalf("a star with the server away = %v", err)
	}

	// The app starts again: another service over the same store.
	next := NewService(store, svc.secrets, "")
	next.SetTLSConfig(insecureTLS())
	next.SetClock(clock.Now)
	f.restart(t)
	clock.advance(time.Minute)
	startQueueRun(t, next, a.ID)
	waitFor(t, "the star of the run before", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 1 {
		t.Errorf("starred on the server = %q", got)
	}
}

// A server that is out of reach is no refusal: the worker tries again
// later, and says what it holds meanwhile.
func TestAQueuedActionWaitsThroughAFailure(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	f.stop()
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{"INBOX", msgs[0].UID}}, FlagFlagged, true); err != nil {
		t.Fatalf("a star with the server away = %v", err)
	}

	clock.advance(time.Minute)
	q, events := startQueueRun(t, svc, a.ID)
	waitFor(t, "a try that failed", func() bool {
		ops := queued(t, store, a.ID)
		return len(ops) == 1 && ops[0].Attempts == 2
	})
	ops := queued(t, store, a.ID)
	if ops[0].State != OperationPending || ops[0].LastError == "" {
		t.Fatalf("after a failed try = %+v", ops[0])
	}
	if st, ok := reportedQueue(events); !ok || st.Pending != 1 || st.Failed != 0 {
		t.Errorf("status = %+v, %v; want one waiting", st, ok)
	}

	// The wait doubles after a failure; the server is back at its end.
	next := ops[0].NextTry
	if !next.After(clock.Now()) {
		t.Fatalf("next try = %v, want a wait after %v", next, clock.Now())
	}
	clock.advance(next.Sub(clock.Now()))
	f.restart(t)
	signal(q.wake)
	waitFor(t, "the star after the failure", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 1 {
		t.Errorf("starred on the server = %q", got)
	}
}

// A move the server made before the app ended is not made again, nor
// reported as a failure: the folder no longer has the message.
func TestAQueuedMoveAlreadyMadeIsDone(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	uid := msgs[0].UID
	// The app wrote the action and made it, and ended before dropping it.
	if _, err := store.EnqueueOperations(ctx, []PendingOperation{{
		AccountID: a.ID, Kind: OpMove, Folder: "INBOX", UID: uid, Dest: "Archive",
		State: OperationClaimed,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.moveOnServer(ctx, a, "secret", "INBOX", []UID{uid}, "Archive"); err != nil {
		t.Fatal(err)
	}

	clock.advance(time.Minute)
	startQueueRun(t, svc, a.ID)
	waitFor(t, "the action left from before", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := f.messages(t, "Archive"); len(got) != 1 {
		t.Errorf("archive on the server = %d messages, want 1", len(got))
	}
	if got, _, _ := store.GetMessage(ctx, a.ID, "INBOX", uid); got.UID != 0 {
		t.Error("the cache kept the row of a message the server moved")
	}
}

// What the server refuses stays, with its reason, for the window to
// show, and the cache goes back to what it was.
func TestAQueuedActionTheServerRefusesIsKept(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	// The archive is gone from the server, though the cache still knows it.
	if err := f.user.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	f.stop()
	moved, err := svc.MoveMessages(ctx, a.ID, []MessageRef{{"INBOX", msgs[0].UID}}, "Archive")
	if err != nil || len(moved) != 1 || moved[0].Pending == 0 {
		t.Fatalf("move with the server away = %+v, %v", moved, err)
	}

	f.restart(t)
	clock.advance(time.Minute)
	q, events := startQueueRun(t, svc, a.ID)
	waitFor(t, "the refusal", func() bool {
		ops := queued(t, store, a.ID)
		return len(ops) == 1 && ops[0].State == OperationFailed
	})
	ops := queued(t, store, a.ID)
	if ops[0].LastError == "" {
		t.Error("the refusal kept no reason")
	}
	if got, _, _ := store.GetMessage(ctx, a.ID, "INBOX", msgs[0].UID); got.UID == 0 {
		t.Error("the cache lost the message the server refused to move")
	}
	if st, ok := reportedQueue(events); !ok || st.Failed != 1 || st.Reason == "" {
		t.Errorf("status = %+v, %v; want the refusal", st, ok)
	}

	// The user puts the folder back and asks for another try.
	f.create(t, "Archive")
	if n, err := svc.RetryFailedActions(ctx, a.ID); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v; want one action", n, err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 1 || ops[0].State != OperationPending {
		t.Fatalf("after asking again = %+v", ops)
	}
	signal(q.wake)
	waitFor(t, "the move asked for again", func() bool { return len(queued(t, store, a.ID)) == 0 })
	if got := f.messages(t, "Archive"); len(got) != 1 {
		t.Errorf("archive on the server = %d messages, want 1", len(got))
	}
}

// A folder the server reset under the queue names other messages now:
// the action fails rather than change one of them.
func TestAQueuedActionUnderANewUIDVALIDITYFails(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	f.stop()
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{"INBOX", msgs[0].UID}}, FlagFlagged, true); err != nil {
		t.Fatalf("a star with the server away = %v", err)
	}

	// The server makes the folder again: its UIDs are other messages.
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "threads/apple-1.eml"))
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	startQueueRun(t, svc, a.ID)
	waitFor(t, "the refusal", func() bool {
		ops := queued(t, store, a.ID)
		return len(ops) == 1 && ops[0].State == OperationFailed
	})
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("starred in the remade inbox = %q", got)
	}
}

// A label made offline is put on the server's keyword once it is back.
func TestALabelMadeOfflineWaitsForTheServer(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml"))
	clock := newTestClock()
	svc.SetClock(clock.Now)
	label, err := svc.CreateLabel(ctx, a.ID, "To Do", "")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	f.stop()
	if err := svc.SetLabel(ctx, a.ID, []MessageRef{{"INBOX", msgs[0].UID}}, label.ID, true); err != nil {
		t.Fatalf("a label with the server away = %v", err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 1 || ops[0].Kind != OpLabel || ops[0].Label != label.ID {
		t.Fatalf("queued = %+v", ops)
	}
	if got, _, _ := store.GetMessage(ctx, a.ID, "INBOX", msgs[0].UID); !slices.ContainsFunc(got.Labels, func(l MessageLabel) bool { return l.ID == label.ID }) {
		t.Error("the cache did not show the label")
	}

	f.restart(t)
	clock.advance(time.Minute)
	startQueueRun(t, svc, a.ID)
	waitFor(t, "the label", func() bool { return len(queued(t, store, a.ID)) == 0 })
	for _, keywords := range keywordsOn(t, f, "INBOX") {
		if len(keywords) != 1 || !strings.EqualFold(keywords[0], label.Keyword) {
			t.Errorf("keywords on the server = %v, want %q", keywords, label.Keyword)
		}
	}
}

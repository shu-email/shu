package mail

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
)

// syncRecorder keeps the events a service reports.
type syncRecorder struct {
	mu     sync.Mutex
	events []SyncEvent
}

func (r *syncRecorder) report(ev SyncEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *syncRecorder) all() []SyncEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SyncEvent(nil), r.events...)
}

func (r *syncRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// A folder's sync says it connects, reads, writes the cache and finishes,
// naming the account and folder each time. A folder synced before is no
// first sync.
func TestSyncFolderReportsItsSteps(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, _, a := newIMAPService(t, f, "secret")
	rec := &syncRecorder{}
	svc.OnSync(rec.report)

	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	events := rec.all()
	var phases []SyncPhase
	for _, ev := range events {
		if ev.AccountID != a.ID || ev.Folder != "INBOX" || ev.Kind != SyncKindFirst {
			t.Errorf("event = %+v, want the account's first sync of INBOX", ev)
		}
		phases = append(phases, ev.Phase)
	}
	want := []SyncPhase{SyncPhaseConnecting, SyncPhaseReading, SyncPhaseWriting, SyncPhaseFinished}
	if len(phases) != len(want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases = %v, want %v", phases, want)
		}
	}

	rec.reset()
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	for _, ev := range rec.all() {
		if ev.Kind != SyncKindFolder {
			t.Errorf("a later sync reports %+v, want SyncKindFolder", ev)
		}
	}
}

// A sync that fails before it reaches the server, as one of an account
// that has to sign in again, still says where it stopped and why.
func TestSyncFolderReportsAFailureBeforeTheServer(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	svc, _, _ := newIMAPService(t, f, "secret")
	rec := &syncRecorder{}
	svc.OnSync(rec.report)
	if err := svc.SyncFolder(context.Background(), "missing", "INBOX", 10); err == nil {
		t.Fatal("a sync of no account worked")
	}
	events := rec.all()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	if last.AccountID != "missing" || last.Folder != "INBOX" ||
		last.Phase != SyncPhaseConnecting || last.Failure == SyncFailNone {
		t.Errorf("the failure = %+v", last)
	}
}

// FetchOlder says how many of how many it has, and a folder it resets is
// a reading too.
func TestFetchOlderReportsProgress(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 120)
	svc, _, a := olderService(t, f, 50)
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	rec := &syncRecorder{}
	svc.OnSync(rec.report)
	if got, err := svc.FetchOlder(ctx, a.ID, "INBOX", 30); err != nil || got.Fetched != 30 {
		t.Fatalf("older mail = %+v, %v", got, err)
	}
	events := rec.all()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	progress := false
	for _, ev := range events {
		if ev.Kind != SyncKindOlder || ev.AccountID != a.ID || ev.Folder != "INBOX" {
			t.Errorf("event = %+v, want older mail of INBOX", ev)
		}
		if ev.Phase == SyncPhaseReading && ev.Total == 30 && ev.Done == 30 {
			progress = true
		}
	}
	if !progress {
		t.Errorf("no reading event with 30 of 30: %+v", events)
	}
	last := events[len(events)-1]
	if last.Phase != SyncPhaseFinished || last.Cancelled || last.Failure != SyncFailNone {
		t.Errorf("the last event = %+v, want a finish", last)
	}
}

// A sync cancelled by the app says it was cancelled, not that it failed;
// one the server could not be reached for says why.
func TestSyncEndTellsCancellationFromFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := syncEnd(ctx, "a1", "INBOX", SyncKindFolder, SyncPhaseReading, 2, 9, context.Canceled)
	if !cancelled.Cancelled || cancelled.Failure != SyncFailNone {
		t.Errorf("a cancelled sync = %+v", cancelled)
	}
	if cancelled.Phase != SyncPhaseReading || cancelled.Done != 2 || cancelled.Total != 9 {
		t.Errorf("the cancelled sync lost its step: %+v", cancelled)
	}
	cases := []struct {
		err  error
		want SyncFailure
	}{
		{context.DeadlineExceeded, SyncFailNetwork},
		{errors.New("the server said no"), SyncFailOther},
		{ErrSignInRequired, SyncFailSignIn},
	}
	for _, c := range cases {
		ev := syncEnd(context.Background(), "a1", "INBOX", SyncKindFolder, SyncPhaseConnecting, 0, 0, c.err)
		if ev.Cancelled || ev.Failure != c.want {
			t.Errorf("%q: event = %+v, want %v", c.err, ev, c.want)
		}
	}
	done := syncEnd(context.Background(), "a1", "INBOX", SyncKindFolder, SyncPhaseReading, 0, 0, nil)
	if done.Phase != SyncPhaseFinished || done.Failure != SyncFailNone || done.Cancelled {
		t.Errorf("a sync that worked = %+v", done)
	}
	// A connection the app closed is the cancellation; a server that
	// refused in the same moment is no cancellation.
	closed := syncEnd(ctx, "a1", "INBOX", SyncKindFolder, SyncPhaseReading, 0, 0, net.ErrClosed)
	if !closed.Cancelled {
		t.Errorf("a connection closed by the app = %+v, want cancelled", closed)
	}
	refused := syncEnd(ctx, "a1", "INBOX", SyncKindFolder, SyncPhaseReading, 0, 0, errors.New("the server said no"))
	if refused.Cancelled || refused.Failure != SyncFailOther {
		t.Errorf("a refusal during a cancellation = %+v, want a failure", refused)
	}
}

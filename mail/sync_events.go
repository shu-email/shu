package mail

import (
	"context"
	"errors"
	"io"
	"net"
)

// Syncing is a report to the window, not a state of the engine: what a
// sync is doing, how far it has come, and why it stopped. The window draws
// it and says its own words; nothing here changes what a sync writes.

// SyncKind is what a sync reads.
type SyncKind int

const (
	// SyncKindFolder is a folder's changes, as a sync of it reads.
	SyncKindFolder SyncKind = iota + 1
	// SyncKindFirst is a folder's first sync, which caches its newest
	// messages.
	SyncKindFirst
	// SyncKindOlder is the older mail FetchOlder fetched.
	SyncKindOlder
	// SyncKindWatch is a watcher's check of its inbox.
	SyncKindWatch
	// SyncKindDrafts is an account's drafts: what goes up, and what other
	// mail clients changed.
	SyncKindDrafts
)

// SyncPhase is the step a sync is in. A sync that ends well says
// SyncPhaseFinished; one that fails says the step it reached.
type SyncPhase int

const (
	// SyncPhaseConnecting is opening the connection: reaching the server
	// and signing in.
	SyncPhaseConnecting SyncPhase = iota + 1
	// SyncPhaseReading is reading the headers and flags the server has.
	SyncPhaseReading
	// SyncPhaseWriting is writing what it read into the cache, or a draft
	// to the server.
	SyncPhaseWriting
	// SyncPhaseFinished ends a sync that worked.
	SyncPhaseFinished
)

// SyncFailure is why a sync failed: the classes a watcher sorts its own
// failures into, so the window says them in one set of words. The error
// itself, which may quote the server, stays here.
type SyncFailure = WatchFailure

const (
	// SyncFailNone is no failure.
	SyncFailNone = WatchFailNone
	// SyncFailNetwork is a server that cannot be reached.
	SyncFailNetwork = WatchFailNetwork
	// SyncFailAuth is a sign-in the server refused.
	SyncFailAuth = WatchFailAuth
	// SyncFailSignIn is an account that has to sign in again.
	SyncFailSignIn = WatchFailSignIn
	// SyncFailOther is anything else: the server or the cache refusing.
	SyncFailOther = WatchFailOther
)

// SyncEvent reports a sync of one folder of one account: the step it is
// in, how many of how many it has read when that is known, and, when it
// ends, why. Done and Total count the items of the phase, not messages
// written; zero Total says the sync does not know how much is left.
type SyncEvent struct {
	AccountID string
	// Folder is the folder synced; empty for drafts, which are the
	// account's rather than one folder's.
	Folder string
	Kind   SyncKind
	Phase  SyncPhase
	Done   int
	Total  int
	// Failure is set when the event ends a sync that failed.
	Failure SyncFailure
	// Cancelled is set when the event ends a sync the app stopped, as
	// closing the window does: it is no failure.
	Cancelled bool
}

// OnSync sets where sync events go. The service calls report from the
// goroutine that runs the sync, in the order a sync makes them; a new
// call replaces the receiver. One sync may run beside another, so events
// of different folders interleave: each event names its account and folder.
func (s *Service) OnSync(report func(SyncEvent)) {
	s.syncMu.Lock()
	s.syncReport = report
	s.syncMu.Unlock()
}

// reportSync hands an event to the window, if it listens.
func (s *Service) reportSync(ev SyncEvent) {
	s.syncMu.Lock()
	report := s.syncReport
	s.syncMu.Unlock()
	if report != nil {
		report(ev)
	}
}

// syncTracker reports one sync to the window as it runs: the step it is
// in, how many of how many it has done when the server said, and how it
// ended. Every sync of the service reports the same way (see
// sync_events.go), so the window draws and words them alike.
type syncTracker struct {
	svc     *Service
	account string
	folder  string
	kind    SyncKind
	phase   SyncPhase
	done    int
	total   int
}

// trackSync begins a sync's report, saying the step it begins in.
func (s *Service) trackSync(accountID, folder string, kind SyncKind) *syncTracker {
	t := &syncTracker{svc: s, account: accountID, folder: folder, kind: kind, phase: SyncPhaseConnecting}
	t.report()
	return t
}

// report tells the window where the sync is now.
func (t *syncTracker) report() {
	t.svc.reportSync(SyncEvent{AccountID: t.account, Folder: t.folder, Kind: t.kind,
		Phase: t.phase, Done: t.done, Total: t.total})
}

// step moves the sync to another step, reporting the one it moved to, and
// gives up nothing for a step it is in already.
func (t *syncTracker) step(p SyncPhase) {
	if t.phase == p {
		return
	}
	t.phase = p
	t.report()
}

// move names the folder the sync came to read, as a watcher's check does
// when a full check opens the inbox on a connection that had none. The
// step that follows reports the new name.
func (t *syncTracker) move(folder string) {
	t.folder = folder
}

// progress says how many of how many the sync has done, as the server
// said.
func (t *syncTracker) progress(done, total int) {
	t.done, t.total = done, total
	t.report()
}

// count says how many of how many without reporting: a fetch that resets
// a folder has no progress to show, but the step that ends it says the
// count.
func (t *syncTracker) count(done, total int) {
	t.done, t.total = done, total
}

// end reports the sync over, with the reason it ended.
func (t *syncTracker) end(ctx context.Context, err error) {
	t.svc.reportSync(syncEnd(ctx, t.account, t.folder, t.kind, t.phase, t.done, t.total, err))
}

// syncEnd is the event that ends a sync: finished when it worked,
// cancelled when the app stopped it, else failed at the step it reached.
func syncEnd(ctx context.Context, accountID, folder string, kind SyncKind, phase SyncPhase, done, total int, err error) SyncEvent {
	ev := SyncEvent{
		AccountID: accountID, Folder: folder, Kind: kind, Phase: phase,
		Done: done, Total: total,
	}
	switch {
	case err == nil:
		ev.Phase = SyncPhaseFinished
	case errors.Is(err, context.Canceled):
		ev.Cancelled = true
	case syncStopped(ctx, err):
		ev.Cancelled = true
	default:
		ev.Failure = failureOf(err)
	}
	return ev
}

// syncStopped tells a sync the app stopped from one that failed. A
// cancelled context closes the connection under the sync, which errs with
// a closed connection rather than a context error; a server that refused
// in the same moment is no cancellation.
func syncStopped(ctx context.Context, err error) bool {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe)
}

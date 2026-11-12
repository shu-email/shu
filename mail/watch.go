package mail

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// A watcher receives one account's mail while the app does something
// else. It keeps a connection idling on the inbox where the server can
// push (IDLE), and looks at intervals where it cannot; either way it
// refreshes the folder list and counts at the interval the user chose.
// It reports what is new and what it is doing, as data: showing any of
// it is the window's business. It only ever reads: the inbox is opened
// read-only and nothing it fetches marks a message read.

const (
	// watchWindow is how many of the inbox's newest messages a check
	// refreshes the flags of. Its first check caches the settings'
	// window, as the window's own first sync of a folder does.
	watchWindow = 50
	// watchBackoffMin and watchBackoffMax bound the wait after a failed
	// check, which doubles with every failure in a row.
	watchBackoffMin = 5 * time.Second
	watchBackoffMax = 5 * time.Minute
	// idleRenew is how long an IDLE lasts before it is issued again. A
	// server may hang up on one that lasted 30 minutes (RFC 2177), and
	// the routers in between forget a silent connection much sooner.
	idleRenew = 15 * time.Minute
	// watchPushGap is the least time between two checks in a row that
	// the server asked for.
	watchPushGap = 2 * time.Second
	// watchCheckTimeout bounds the network part of one check, and
	// watchProbeTimeout the answer to starting and ending an IDLE, which
	// a server that is there gives at once. The IMAP library waits
	// without limit for an answer to begin, so a connection that died
	// silently, as across a sleep, would hold the watcher until TCP
	// gives up.
	watchCheckTimeout = time.Minute
	watchProbeTimeout = 15 * time.Second
)

// WatchState is what an account's watcher is doing.
type WatchState int

const (
	// WatchConnecting is reaching the server, for the first time or
	// after a failure.
	WatchConnecting WatchState = iota
	// WatchUpToDate is the last check having worked: the watcher waits
	// for the server to push, or for the next check.
	WatchUpToDate
	// WatchRetrying is the last check having failed: the next one comes
	// after a wait.
	WatchRetrying
	// WatchOffline is the server out of reach while the computer has no
	// network: the watcher waits for the network instead of trying.
	WatchOffline
	// WatchSignIn is an account that has to sign in again
	// (ErrSignInRequired): the watcher does not check until it has.
	WatchSignIn
)

// WatchFailure is why a check failed, for the window to say in its own
// words. The error itself, which may quote the server, stays here.
type WatchFailure int

const (
	// WatchFailNone is no failure.
	WatchFailNone WatchFailure = iota
	// WatchFailNetwork is a server that cannot be reached.
	WatchFailNetwork
	// WatchFailAuth is a sign-in the server refused.
	WatchFailAuth
	// WatchFailOther is anything else: the server or the cache refusing.
	WatchFailOther
	// WatchFailSignIn is an account that has to sign in again.
	WatchFailSignIn
)

// WatchStatus is a watcher's state.
type WatchStatus struct {
	State WatchState
	// CheckedAt is when the last check that worked ended, zero when none
	// has yet.
	CheckedAt time.Time
	// Failure says why the watcher is retrying or offline.
	Failure WatchFailure
}

// WatchEvent is what a watcher reports: its state, and after a check
// that worked, what the check found.
type WatchEvent struct {
	AccountID string
	Status    WatchStatus
	// Folders is the account's folders with their counts as the check
	// left them in the cache, nil when the event is no check's.
	Folders []Folder
	// Folder is the folder watched, the inbox, whose cached messages the
	// check refreshed; empty when the event is no check's or the account
	// has no inbox.
	Folder string
	// New is the messages to announce, newest first: the unread ones
	// that arrived since the last announcement, each reported once ever.
	// All that a check found come together, however many they are.
	New []Summary
}

// watcher is one account's watcher. Its goroutine owns everything but
// the channels, which are how the service reaches it.
type watcher struct {
	svc       *Service
	accountID string
	report    func(WatchEvent)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// nudge asks for a check now; reconsider has a watcher that waits
	// after a failure look again at whether the computer is online.
	// redial has the next check hang up first: the connection signed in
	// with a sign-in that is gone.
	nudge      chan struct{}
	reconsider chan struct{}
	redial     atomic.Bool

	conn     *watchConn
	status   WatchStatus
	failures int
	// due is when the folder list and counts are refreshed next.
	due time.Time
	// lost is set when the connection ended while the watcher waited on
	// it. Once, that is a connection gone stale; twice in a row, it is
	// the server failing.
	lost bool
	// pushed is set when the last check was one the server asked for.
	pushed bool
}

// watchConn is the connection a watcher keeps: logged in, with the
// watched folder opened read-only once a check has run on it.
type watchConn struct {
	*imapSession
	// cancel ends the context the connection was dialed under.
	cancel context.CancelFunc
	// changed is set when the server says, unasked, that the selected
	// folder changed.
	changed     chan struct{}
	folder      string
	uidValidity uint32
	idles       bool
	// exists is how many messages the server last said the folder has.
	exists atomic.Uint32
}

// connLost is the error of a command whose connection ended under it.
// The IMAP library reports some of those as text, which would not tell
// a broken connection from a server's refusal.
type connLost struct{ err error }

func (e *connLost) Error() string { return e.err.Error() }
func (e *connLost) Unwrap() error { return e.err }

// realTimer is the timer of production.
func realTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// Watch starts receiving an account's mail in the background and
// reporting it. report is called from the watcher's goroutine, in order.
// An account watched already is watched anew, with its settings as they
// are now. Watch waits for the earlier watcher to end, so it is not for
// the UI thread.
func (s *Service) Watch(accountID string, report func(WatchEvent)) {
	s.watchLife.Lock()
	defer s.watchLife.Unlock()
	s.stopWatcher(accountID)
	if s.watchOver {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !s.intervalRead {
		// The interval is read before the first watcher runs;
		// SaveSettings keeps it current from then on.
		s.intervalRead = true
		if set, err := s.Settings(ctx); err == nil {
			s.watchMu.Lock()
			if s.interval == 0 {
				s.interval = time.Duration(set.CheckMinutes) * time.Minute
			}
			s.watchMu.Unlock()
		}
	}
	w := &watcher{
		svc: s, accountID: accountID, report: report,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		nudge: make(chan struct{}, 1), reconsider: make(chan struct{}, 1),
	}
	s.watchMu.Lock()
	s.watchers[accountID] = w
	s.watchMu.Unlock()
	go w.run()
}

// Unwatch stops watching an account. It returns when the watcher's
// goroutine has ended and its connection is closed.
func (s *Service) Unwatch(accountID string) {
	s.watchLife.Lock()
	defer s.watchLife.Unlock()
	s.stopWatcher(accountID)
}

// stopWatcher ends one watcher and waits for it. watchLife is held.
func (s *Service) stopWatcher(accountID string) {
	s.watchMu.Lock()
	w := s.watchers[accountID]
	delete(s.watchers, accountID)
	s.watchMu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
}

// StopWatching ends every watcher, for good: the app is quitting. The
// rules' actions applied in the background stop too. It returns when
// their goroutines have ended.
func (s *Service) StopWatching() {
	s.watchLife.Lock()
	defer s.watchLife.Unlock()
	s.watchOver = true
	s.watchMu.Lock()
	all := s.watchers
	s.watchers = map[string]*watcher{}
	s.watchMu.Unlock()
	for _, w := range all {
		w.cancel()
	}
	for _, w := range all {
		<-w.done
	}
	s.stopRules()
}

// Nudge has every watcher check now instead of waiting: the app came to
// the front, the computer woke, or the user asked for a sync.
func (s *Service) Nudge() {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	for _, w := range s.watchers {
		signal(w.nudge)
	}
}

// SetOnline tells the watchers whether the computer has a network.
// Without one, a watcher that cannot reach its server stops trying and
// says so; with it back, they all try at once.
func (s *Service) SetOnline(online bool) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.offline == !online {
		return
	}
	s.offline = !online
	for _, w := range s.watchers {
		if online {
			signal(w.nudge)
		} else {
			signal(w.reconsider)
		}
	}
}

// setInterval changes how often the watchers check, and has them check
// now so that the new interval counts from here.
func (s *Service) setInterval(d time.Duration) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.interval == d {
		return
	}
	s.interval = d
	for _, w := range s.watchers {
		signal(w.nudge)
	}
}

// checkEvery is the interval between checks.
func (s *Service) checkEvery() time.Duration {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.interval <= 0 {
		return DefaultCheckMinutes * time.Minute
	}
	return s.interval
}

func (s *Service) isOffline() bool {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return s.offline
}

// signal sets a flag channel without waiting on a reader.
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// taken reports whether a flag channel was set, and clears it.
func taken(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// run is the watcher's goroutine: check, wait, and again, until stopped.
func (w *watcher) run() {
	defer close(w.done)
	defer w.hangUp()
	full := true
	for {
		err := w.check(full)
		if full {
			w.pushed = false
		}
		if err == nil {
			w.failures = 0
			full, err = w.waitForNext()
		}
		if w.ctx.Err() != nil {
			return
		}
		if err != nil {
			w.hangUp()
			if !w.waitToRetry(err) {
				return
			}
			full = true
		}
	}
}

// say reports a state.
func (w *watcher) say(state WatchState, failure WatchFailure) {
	w.status.State, w.status.Failure = state, failure
	w.report(WatchEvent{AccountID: w.accountID, Status: w.status})
}

// check looks at the server once: with full, the folder list and counts
// and then the inbox; without, the inbox alone, which is what the server
// said had changed. It uses the connection kept from the last check, or
// a new one.
func (w *watcher) check(full bool) error {
	ctx, cancel := context.WithTimeout(w.ctx, watchCheckTimeout)
	defer cancel()
	if w.status.State != WatchUpToDate {
		w.say(WatchConnecting, WatchFailNone)
	}
	if w.redial.Swap(false) {
		w.hangUp()
	}
	if w.conn != nil {
		err := w.checkOn(ctx, w.conn, full)
		if err == nil || w.ctx.Err() != nil {
			return err
		}
		// A connection kept since the last check may have died meanwhile,
		// as across a sleep. That says nothing of the server until a new
		// connection fails too.
		w.hangUp()
	}
	c, err := w.dial(ctx)
	if err != nil {
		return err
	}
	w.conn = c
	if err := w.checkOn(ctx, c, true); err != nil {
		select {
		case <-c.client.Closed():
			err = &connLost{err}
		default:
		}
		return err
	}
	return nil
}

// dial opens the watcher's connection.
func (w *watcher) dial(ctx context.Context) (*watchConn, error) {
	a, password, err := w.svc.account(ctx, w.accountID)
	if err != nil {
		return nil, err
	}
	// The connection outlives this check, so it is dialed under a
	// context of its own, which the check's end cancels only while the
	// connection is coming up.
	life, cancel := context.WithCancel(context.Background())
	c := &watchConn{cancel: cancel, changed: make(chan struct{}, 1)}
	handler := &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) {
			if n := c.exists.Load(); n > 0 {
				c.exists.CompareAndSwap(n, n-1)
			}
			signal(c.changed)
		},
		// A count that is the one known already is not news: some
		// servers repeat it with every command.
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil && c.exists.Swap(*data.NumMessages) != *data.NumMessages {
				signal(c.changed)
			}
		},
		// Flags changed by another mail client: the unread count is no
		// longer what the cache says.
		Fetch: func(msg *imapclient.FetchMessageData) {
			// The client library also hands over here what it did not
			// expect of the answers to the watcher's own fetches, such
			// as the last message, which a server returns for a range
			// of UIDs above it. Taking that for a change would have the
			// next check ask the same and never end. Flags alone are
			// what a server sends unasked.
			if buf, err := msg.Collect(); err == nil && flagsAlone(buf) {
				signal(c.changed)
			}
		},
	}
	release := whenDone(ctx, cancel)
	session, err := w.svc.dialIMAPWith(life, a, password, handler)
	release()
	if err != nil {
		cancel()
		return nil, err
	}
	if life.Err() != nil {
		// The check ended just as the connection came up, which closes
		// it.
		session.release()
		session.client.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	c.imapSession = session
	c.idles = !w.svc.watchNoIdle && (session.capHas(imap.CapIdle) || session.capHas(imap.CapIMAP4rev2))
	return c, nil
}

// flagsAlone reports whether fetched data is flags and nothing a fetch
// of the watcher's asks for.
func flagsAlone(buf *imapclient.FetchMessageBuffer) bool {
	return buf.Envelope == nil && buf.BodyStructure == nil && buf.InternalDate.IsZero() &&
		buf.RFC822Size == 0 && len(buf.BodySection) == 0 && len(buf.BinarySection) == 0
}

// hangUp closes the watcher's connection, if it has one, without a
// goodbye: a connection whose IDLE failed cannot send another command.
func (w *watcher) hangUp() {
	c := w.conn
	if c == nil {
		return
	}
	w.conn = nil
	c.release()
	c.client.Close()
	c.cancel()
}

// logOut ends a connection that works, as a server expects.
func (w *watcher) logOut() {
	if c := w.conn; c != nil {
		release := w.guard(c, watchProbeTimeout)
		_ = c.client.Logout().Wait()
		release()
	}
	w.hangUp()
}

// guard closes the connection when d has passed or the watcher stops,
// until release is called: it is what ends a wait for a server that no
// longer answers.
func (w *watcher) guard(c *watchConn, d time.Duration) (release func()) {
	ctx, cancel := context.WithTimeout(w.ctx, d)
	done := whenDone(ctx, func() { c.client.Close() })
	return func() {
		done()
		cancel()
	}
}

// checkOn runs a check on a connection and reports what it found.
func (w *watcher) checkOn(ctx context.Context, c *watchConn, full bool) error {
	release := whenDone(ctx, func() { c.client.Close() })
	defer release()
	store := w.svc.store
	if full {
		folders, err := w.svc.listFolders(c.imapSession)
		if err != nil {
			return err
		}
		if err := store.SaveFolders(ctx, w.accountID, folders); err != nil {
			return err
		}
		inbox, validity := "", uint32(0)
		for _, f := range folders {
			if f.IsInbox() {
				inbox, validity = f.Name, f.UIDValidity
				break
			}
		}
		// The inbox is opened once per connection, and again if the
		// server now lists it under another UIDVALIDITY: it was replaced
		// while this connection looked at the old one.
		if inbox != "" && (c.folder != inbox || (validity != 0 && validity != c.uidValidity)) {
			// EXAMINE, not SELECT: nothing the watcher does may change
			// the mailbox, not even its \Recent flags.
			selected, err := c.client.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				return err
			}
			c.folder, c.uidValidity = inbox, selected.UIDValidity
			c.exists.Store(selected.NumMessages)
		}
		w.due = w.svc.now().Add(w.svc.checkEvery())
	}
	var fresh []Summary
	if c.folder != "" {
		// What the server signalled until now, this sync covers.
		taken(c.changed)
		span := syncSpan{first: w.svc.firstSyncWindow(ctx), refresh: watchWindow, keep: defaultSyncCap, sorts: true}
		if _, err := w.svc.syncSelected(ctx, c.imapSession, w.accountID, c.folder, c.uidValidity, nil, span); err != nil {
			return err
		}
		var err error
		if fresh, err = w.svc.takeNew(ctx, w.accountID, c.folder); err != nil {
			return err
		}
	}
	folders, err := store.Folders(ctx, w.accountID)
	if err != nil {
		return err
	}
	w.status = WatchStatus{State: WatchUpToDate, CheckedAt: w.svc.now()}
	w.report(WatchEvent{
		AccountID: w.accountID, Status: w.status,
		Folders: folders, Folder: c.folder, New: fresh,
	})
	return nil
}

// waitForNext waits until the next check is due: for the server to say
// the inbox changed where it can, else for the interval. It reports
// whether the next check is a full one.
func (w *watcher) waitForNext() (full bool, err error) {
	for {
		c := w.conn
		if c == nil || !c.idles || c.folder == "" {
			// Nothing to wait on: the connection is not kept, and the
			// next check opens its own.
			w.logOut()
			w.sleep(w.due.Sub(w.svc.now()))
			return true, nil
		}
		if taken(c.changed) {
			return w.afterPush(), nil
		}
		wait := w.due.Sub(w.svc.now())
		if wait <= 0 {
			return true, nil
		}
		// A server need not say, as an IDLE begins, what happened since
		// the client's last command, and some say it only in answer to
		// one: mail that came while the watcher was busy checking would
		// wait for the check at the interval. The NOOP asks. What it
		// brings is checked instead of idling. A change that falls
		// between its answer and the IDLE can still be held back by such
		// a server; servers that sync as an IDLE begins leave no gap.
		var changed, woken bool
		err := w.noop(c)
		if err == nil {
			if taken(c.changed) {
				return w.afterPush(), nil
			}
			changed, woken, err = w.idle(c, min(wait, idleRenew))
		}
		switch {
		case w.ctx.Err() != nil:
			return false, nil
		case err != nil:
			w.hangUp()
			if w.lost {
				return true, &connLost{err}
			}
			// Not a failure yet: the next check dials again.
			w.lost = true
			return true, nil
		}
		w.lost = false
		switch {
		case changed:
			return w.afterPush(), nil
		case woken || !w.svc.now().Before(w.due):
			return true, nil
		}
		// The IDLE was only due to be issued again.
	}
}

// afterPush is called when the server said the inbox changed. The check
// that follows is at once after a quiet time. When the last check was
// asked for by the server too, and is not watchPushGap old, the rest of
// that gap passes first: mail that arrives in a burst is one check and
// one report, and a server that says "changed" in answer to every check
// gets a check every few seconds, not thousands. It reports whether the
// next check is a full one, which a nudge during the wait makes it.
func (w *watcher) afterPush() (full bool) {
	if w.pushed {
		if wait := watchPushGap - w.svc.now().Sub(w.status.CheckedAt); wait > 0 {
			full = w.sleep(wait)
		}
	}
	w.pushed = true
	return full
}

// sleep waits for d, or less if nudged or stopped. It reports whether a
// nudge ended it.
func (w *watcher) sleep(d time.Duration) (nudged bool) {
	timer, stop := w.svc.newTimer(max(d, 0))
	defer stop()
	for {
		select {
		case <-timer:
			return false
		case <-w.nudge:
			return true
		case <-w.reconsider:
			// Being offline matters to a watcher that is failing only.
		case <-w.ctx.Done():
			return false
		}
	}
}

// noop sends a command that does nothing, for the answer: a server sends
// with it what it has to say of the selected folder. A server that is
// there answers at once.
func (w *watcher) noop(c *watchConn) error {
	release := w.guard(c, watchProbeTimeout)
	defer release()
	return c.client.Noop().Wait()
}

// idle has the server push for at most d. It reports whether the server
// said the folder changed and whether the watcher was nudged.
func (w *watcher) idle(c *watchConn, d time.Duration) (changed, woken bool, err error) {
	release := w.guard(c, watchProbeTimeout)
	cmd, err := c.client.Idle()
	release()
	if err != nil {
		return false, false, err
	}
	timer, stop := w.svc.newTimer(d)
	for waiting := true; waiting; {
		waiting = false
		select {
		case <-c.changed:
			changed = true
		case <-timer:
		case <-w.nudge:
			woken = true
		case <-w.reconsider:
			waiting = true
		case <-c.client.Closed():
		case <-w.ctx.Done():
		}
	}
	stop()
	// A server that is still there answers DONE at once.
	release = w.guard(c, watchProbeTimeout)
	defer release()
	if err := cmd.Close(); err != nil {
		return false, false, err
	}
	if err := cmd.Wait(); err != nil {
		return false, false, err
	}
	return changed, woken, nil
}

// waitToRetry waits after a failed check: longer with every failure in a
// row, or for the network when there is none. A nudge ends the wait and
// starts the count again. It reports false when the watcher was stopped.
func (w *watcher) waitToRetry(err error) bool {
	failure := failureOf(err)
	if failure == WatchFailSignIn {
		return w.waitForSignIn()
	}
	w.failures++
	wait := watchBackoffMax
	if shift := w.failures - 1; shift < 16 {
		wait = min(watchBackoffMin<<shift, watchBackoffMax)
	}
	if failure == WatchFailAuth {
		// The same password is refused the same way: asking again soon
		// only counts against the account.
		wait = watchBackoffMax
	}
	for {
		var timer <-chan time.Time
		stop := func() {}
		if failure == WatchFailNetwork && w.svc.isOffline() {
			// No timer: the network coming back is what ends this wait.
			w.say(WatchOffline, failure)
		} else {
			w.say(WatchRetrying, failure)
			timer, stop = w.svc.newTimer(wait)
		}
		select {
		case <-timer:
			stop()
			return true
		case <-w.nudge:
			stop()
			w.failures = 0
			return true
		case <-w.reconsider:
			stop()
		case <-w.ctx.Done():
			stop()
			return false
		}
	}
}

// waitForSignIn waits while the account has to sign in again, as a check
// could only fail the same way, with no timer to try again. Signing in
// again nudges the watcher; a nudge that finds the account still signed
// out changes nothing. It reports false when the watcher was stopped.
func (w *watcher) waitForSignIn() bool {
	w.failures = 0
	w.say(WatchSignIn, WatchFailSignIn)
	for {
		select {
		case <-w.nudge:
			if !errors.Is(w.svc.signInState(w.accountID), ErrSignInRequired) {
				return true
			}
		case <-w.reconsider:
		case <-w.ctx.Done():
			return false
		}
	}
}

// signedOut has an account's watcher hang up, as its connection signed
// in with tokens that are gone, and check again: it then waits for the
// account to sign in again.
func (s *Service) signedOut(accountID string) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if w := s.watchers[accountID]; w != nil {
		w.redial.Store(true)
		signal(w.nudge)
	}
}

// signedIn has the watcher of an account that signed in again check now.
func (s *Service) signedIn(accountID string) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if w := s.watchers[accountID]; w != nil {
		signal(w.nudge)
	}
}

// failureOf sorts an error into what the window can say about it.
func failureOf(err error) WatchFailure {
	var login *loginError
	var lost *connLost
	var netErr net.Error
	switch {
	case err == nil:
		return WatchFailNone
	case errors.Is(err, ErrSignInRequired):
		return WatchFailSignIn
	case errors.As(err, &login):
		return WatchFailAuth
	case errors.As(err, &lost), errors.As(err, &netErr), errors.Is(err, net.ErrClosed),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, context.DeadlineExceeded):
		return WatchFailNetwork
	}
	return WatchFailOther
}

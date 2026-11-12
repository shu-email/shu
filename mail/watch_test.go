package mail

import (
	"context"
	"net"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// watchGuard is how long a watch test waits for something that must
// happen before it gives up. Nothing waits it out when the code works:
// the intervals and backoffs of the watcher run on watchClock.
const watchGuard = 10 * time.Second

// watchClock is the time of a watch test: a clock that moves only when a
// timer fires, and timers that fire only when the test says so.
type watchClock struct {
	mu    sync.Mutex
	now   time.Time
	armed chan *watchTimer
	// gaps counts the waits between checks the server asked for. They
	// pass by themselves unless holdGaps is set, so that a test about
	// something else need not know of them.
	gaps     int
	holdGaps bool
}

// watchTimer is a timer the watcher armed.
type watchTimer struct {
	d     time.Duration
	c     chan time.Time
	clock *watchClock
}

func newWatchClock() *watchClock {
	return &watchClock{
		now:   time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		armed: make(chan *watchTimer, 1024),
	}
}

func (k *watchClock) Now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.now
}

func (k *watchClock) newTimer(d time.Duration) (<-chan time.Time, func()) {
	t := &watchTimer{d: d, c: make(chan time.Time, 1), clock: k}
	k.mu.Lock()
	gap := d > 0 && d <= watchPushGap
	if gap {
		k.gaps++
	}
	pass := gap && !k.holdGaps
	k.mu.Unlock()
	if pass {
		t.fire()
		return t.c, func() {}
	}
	k.armed <- t
	return t.c, func() {}
}

// gapCount is how many waits between pushed checks there were.
func (k *watchClock) gapCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gaps
}

// fire lets the timer's time pass.
func (t *watchTimer) fire() {
	t.clock.mu.Lock()
	t.clock.now = t.clock.now.Add(t.d)
	now := t.clock.now
	t.clock.mu.Unlock()
	t.c <- now
}

// watchRig is a service watching the fake server's account.
type watchRig struct {
	t       *testing.T
	f       *fakeIMAP
	svc     *Service
	store   *memStore
	secrets *memSecrets
	account Account
	clock   *watchClock
	events  chan WatchEvent
}

// newWatchRig makes a service with the account stored, on the test's
// clock. noIdle makes it treat the server as one that cannot push, which
// the fake server cannot be told to be: it always offers IDLE.
func newWatchRig(t *testing.T, f *fakeIMAP, password string, noIdle bool) *watchRig {
	t.Helper()
	svc, store, secrets := newTestService()
	a := fakeAccount(t, f.addr)
	if err := svc.AddAccount(context.Background(), a, password); err != nil {
		t.Fatal(err)
	}
	r := &watchRig{t: t, f: f, store: store, secrets: secrets, account: a, clock: newWatchClock()}
	r.use(svc, noIdle)
	return r
}

// use puts a service on the rig's clock and makes it the one watching.
func (r *watchRig) use(svc *Service, noIdle bool) {
	svc.SetTLSConfig(insecureTLS())
	svc.SetClock(r.clock.Now)
	svc.newTimer = r.clock.newTimer
	svc.watchNoIdle = noIdle
	r.svc = svc
	r.events = make(chan WatchEvent, 1024)
	r.t.Cleanup(svc.StopWatching)
}

// watch starts the watcher.
func (r *watchRig) watch() {
	events := r.events
	r.svc.Watch(r.account.ID, func(ev WatchEvent) { events <- ev })
}

// event waits for an event that matches, passing over the others.
func (r *watchRig) event(what string, match func(WatchEvent) bool) WatchEvent {
	r.t.Helper()
	guard := time.After(watchGuard)
	for {
		select {
		case ev := <-r.events:
			if match(ev) {
				return ev
			}
		case <-guard:
			r.t.Fatalf("the watcher did not report %s", what)
		}
	}
}

// checked waits for the report of a check that worked.
func (r *watchRig) checked() WatchEvent {
	r.t.Helper()
	return r.event("a check", func(ev WatchEvent) bool { return ev.Folders != nil })
}

// state waits for the watcher to say it is in a state.
func (r *watchRig) state(state WatchState) WatchEvent {
	r.t.Helper()
	return r.event("the state", func(ev WatchEvent) bool { return ev.Status.State == state })
}

// timer waits for the watcher to arm a timer.
func (r *watchRig) timer() *watchTimer {
	r.t.Helper()
	select {
	case t := <-r.clock.armed:
		return t
	case <-time.After(watchGuard):
		r.t.Fatal("the watcher armed no timer")
		return nil
	}
}

// quiet fails if the watcher reported anything since the last event the
// test took.
func (r *watchRig) quiet(when string) {
	r.t.Helper()
	select {
	case ev := <-r.events:
		r.t.Fatalf("%s the watcher reported %+v", when, ev)
	default:
	}
}

// otherClient logs in as another mail client would.
func otherClient(t *testing.T, f *fakeIMAP) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialStartTLS(f.addr, &imapclient.Options{TLSConfig: insecureTLS()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login("ann", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

// deliver appends a message from another session, unread unless flags
// say otherwise.
func deliver(t *testing.T, f *fakeIMAP, mailbox string, raw []byte, flags ...imap.Flag) {
	t.Helper()
	c := otherClient(t, f)
	defer c.Close()
	cmd := c.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags})
	if _, err := cmd.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// serverFlags reads the flags of every message of a mailbox, by UID, as
// another mail client sees them. It opens the mailbox read-only itself.
func serverFlags(t *testing.T, f *fakeIMAP, mailbox string) map[imap.UID][]string {
	t.Helper()
	c := otherClient(t, f)
	defer c.Close()
	selected, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	out := map[imap.UID][]string{}
	if selected.NumMessages == 0 {
		return out
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		flags := []string{}
		for _, flag := range m.Flags {
			flags = append(flags, string(flag))
		}
		sort.Strings(flags)
		out[m.UID] = flags
	}
	return out
}

func subjects(list []Summary) []string {
	var out []string
	for _, m := range list {
		out = append(out, m.Subject)
	}
	return out
}

// With a server that pushes, a message another session delivers is
// reported at once: no timer fires, so the interval is not waited for.
// The mail that was there at the first check is not news.
func TestWatchIdleReportsNewMailAtOnce(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	f.append(t, "INBOX", readFixture(t, "alternative.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()

	if ev := r.event("that it connects", func(WatchEvent) bool { return true }); ev.Status.State != WatchConnecting {
		t.Errorf("the first report = %+v, want connecting", ev.Status)
	}
	first := r.checked()
	if len(first.New) != 0 {
		t.Fatalf("the first check announced old mail: %v", subjects(first.New))
	}
	if first.Folder != "INBOX" || first.Status.State != WatchUpToDate || !first.Status.CheckedAt.Equal(r.clock.Now()) {
		t.Errorf("the first check = %+v in %q", first.Status, first.Folder)
	}
	if got, _ := r.svc.CachedMessages(context.Background(), r.account.ID, "INBOX", "", 50); len(got) != 2 {
		t.Errorf("the first check cached %d messages, want 2", len(got))
	}
	// The watcher now idles, with a timer for the next look at the
	// folders: the interval, five minutes unless set.
	idle := r.timer()
	if idle.d != DefaultCheckMinutes*time.Minute {
		t.Errorf("the idle lasts %v, want the default interval", idle.d)
	}

	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	ev := r.checked()
	if got := subjects(ev.New); !reflect.DeepEqual(got, []string{"HTML only"}) {
		t.Fatalf("new mail = %v, want the delivered message", got)
	}
	m := ev.New[0]
	if m.Folder != "INBOX" || m.UID != 3 || m.Seen || m.From.Email == "" {
		t.Errorf("the new message = %+v", m)
	}
	for _, folder := range ev.Folders {
		if folder.Name == "INBOX" && (folder.Unread != 3 || folder.Total != 3) {
			t.Errorf("inbox counts after new mail = %d unread of %d", folder.Unread, folder.Total)
		}
	}
	if got, _ := r.svc.CachedMessages(context.Background(), r.account.ID, "INBOX", "", 50); len(got) != 3 {
		t.Errorf("cached %d messages after new mail, want 3", len(got))
	}
	// It reported the message once, and went back to idling.
	r.timer()
	r.quiet("after the new mail")
}

// settled delivers a message and waits for its report, which must be
// the next one: a watcher that went on checking after the last report,
// with nothing to check for, has reports of that in between.
func (r *watchRig) settled(when string) {
	r.t.Helper()
	deliver(r.t, r.f, "INBOX", readFixture(r.t, "plain.eml"))
	extra := 0
	r.event("the mail that ends "+when, func(ev WatchEvent) bool {
		if len(ev.New) == 0 {
			extra++
		}
		return len(ev.New) > 0
	})
	if extra != 0 {
		r.t.Fatalf("%s the watcher checked %d more times with nothing to check for", when, extra)
	}
	r.timer()
}

// Mail comes in bursts. The second message of one may reach the server
// while the watcher is busy with the first, and a server need not say so
// as the next IDLE begins: the test server never does, it speaks only in
// answer to a command. The watcher has to ask, or the message waits for
// the check at the interval, which no timer here ever lets pass.
func TestWatchSeesEveryMessageOfABurst(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.timer()
	for round := 0; round < 10; round++ {
		deliver(t, f, "INBOX", readFixture(t, "plain.eml"))
		deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
		deliver(t, f, "INBOX", readFixture(t, "alternative.eml"))
		for announced := 0; announced < 3; {
			ev := r.checked()
			announced += len(ev.New)
			if announced > 3 {
				t.Fatalf("round %d announced %d messages of 3", round, announced)
			}
		}
	}
}

// A server may hold back what happened between a client's last command
// and its IDLE until the client's next command; the in-memory server
// does when left to itself. Mail that arrives while the watcher is busy
// would then wait for the check at the interval. The watcher asks with a
// NOOP before it idles, so it does not depend on the server here.
func TestWatchAsksBeforeIdling(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)

	// The report of a check can be made to wait, which keeps the watcher
	// where it is: done checking, and not idling yet.
	var park atomic.Bool
	parked, release := make(chan struct{}, 1), make(chan struct{})
	events := r.events
	r.svc.Watch(r.account.ID, func(ev WatchEvent) {
		if ev.Folders != nil && park.CompareAndSwap(true, false) {
			parked <- struct{}{}
			<-release
		}
		events <- ev
	})
	r.checked()
	// The watcher idles: this server pushes what happens from here on.
	r.timer()

	park.Store(true)
	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	select {
	case <-parked:
	case <-time.After(watchGuard):
		t.Fatal("the pushed message brought no check")
	}
	// From here the server holds back what happens until the next
	// command. It is turned on only now: an IDLE is only listened to some
	// time after the server accepts it, and a message that came in that
	// time would be held back too, which is no question this test asks.
	f.holdsBack.Store(true)
	// This one arrives while the watcher is neither idling nor checking.
	// The server says nothing of it until asked.
	deliver(t, f, "INBOX", readFixture(t, "alternative.eml"))
	close(release)

	var got []string
	for len(got) < 2 {
		got = append(got, subjects(r.checked().New)...)
	}
	if len(got) != 2 {
		t.Errorf("announced %v, want the two messages once each", got)
	}
}

// A check that finds nothing new is followed by waiting, not by another
// check. The server answers a fetch of the UIDs above the last one with
// the last message, which is not a change; flags another mail client
// changed are one, and get one check.
func TestWatchChecksOncePerChange(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	f.append(t, "INBOX", readFixture(t, "alternative.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.settled("after the first check")

	// The check at the interval, with nothing new.
	r.svc.Nudge()
	if ev := r.checked(); len(ev.New) != 0 {
		t.Fatalf("a check with nothing new announced %v", subjects(ev.New))
	}
	r.timer()
	r.settled("after a check that found nothing")

	// Another mail client reads a message.
	c := otherClient(t, f)
	defer c.Close()
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	var one imap.UIDSet
	one.AddNum(1)
	if err := c.Store(one, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	ev := r.checked()
	if len(ev.New) != 0 {
		t.Fatalf("a flag change announced %v", subjects(ev.New))
	}
	for _, folder := range ev.Folders {
		if folder.Name == "INBOX" && folder.Unread != 3 {
			t.Errorf("unread after another client read a message = %d, want 3 of 4", folder.Unread)
		}
	}
	r.timer()
	r.settled("after a flag change")

	// Another mail client deletes one.
	if err := c.Store(one, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Expunge().Close(); err != nil {
		t.Fatal(err)
	}
	r.event("the count after a delete", func(ev WatchEvent) bool {
		for _, folder := range ev.Folders {
			if folder.Name == "INBOX" && folder.Total == 4 {
				return true
			}
		}
		return false
	})
	r.timer()
}

// Checks the server asks for in a row are spaced: the first is at once,
// the next waits out the gap, and a burst of mail is one report.
func TestWatchSpacesPushedChecks(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	r.clock.holdGaps = true
	r.watch()
	r.checked()
	r.timer()

	deliver(t, f, "INBOX", readFixture(t, "plain.eml"))
	if ev := r.checked(); len(ev.New) != 1 {
		t.Fatalf("the first push reported %v", subjects(ev.New))
	}
	if got := r.clock.gapCount(); got != 0 {
		t.Fatalf("the first push waited (%d gaps)", got)
	}
	r.timer() // the idle's

	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	gap := r.timer()
	if gap.d != watchPushGap {
		t.Fatalf("after the second push the watcher armed %v, want the gap of %v", gap.d, watchPushGap)
	}
	deliver(t, f, "INBOX", readFixture(t, "alternative.eml"))
	r.quiet("during the gap")
	gap.fire()
	ev := r.checked()
	if len(ev.New) != 2 {
		t.Fatalf("the burst came as %v, want both messages in one report", subjects(ev.New))
	}

	// After a quiet time a push is at once again.
	idle := r.timer()
	if idle.d <= watchPushGap {
		// The third delivery's signal, which the report covered already.
		idle.fire()
		r.checked()
		idle = r.timer()
	}
	idle.fire() // the interval passes: a full check
	r.checked()
	r.timer()
	before := r.clock.gapCount()
	deliver(t, f, "INBOX", readFixture(t, "attachment.eml"))
	if ev := r.checked(); len(ev.New) != 1 {
		t.Fatalf("a push after a quiet time reported %v", subjects(ev.New))
	}
	if got := r.clock.gapCount(); got != before {
		t.Errorf("a push after a quiet time waited")
	}
}

// With a server that cannot push, the message is found at the next
// check, one interval later, and not before.
func TestWatchPollsWithinOneInterval(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", true)
	r.watch()
	if first := r.checked(); len(first.New) != 0 {
		t.Fatalf("the first check announced old mail: %v", subjects(first.New))
	}
	wait := r.timer()
	if wait.d != DefaultCheckMinutes*time.Minute {
		t.Fatalf("the wait between checks = %v, want the interval", wait.d)
	}
	// Between checks the watcher holds no connection.
	r.waitClosed()

	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	r.waitClosed()
	r.quiet("before the interval passed")
	wait.fire()
	ev := r.checked()
	if got := subjects(ev.New); !reflect.DeepEqual(got, []string{"HTML only"}) {
		t.Fatalf("new mail after one interval = %v", got)
	}
	// The next check finds nothing more to announce.
	r.timer().fire()
	if ev := r.checked(); len(ev.New) != 0 {
		t.Errorf("the message was announced again: %v", subjects(ev.New))
	}
}

// waitClosed waits until the fake server serves no connection.
func (r *watchRig) waitClosed() {
	r.t.Helper()
	guard := time.After(watchGuard)
	for r.f.open.Load() != 0 {
		select {
		case <-r.f.ended:
		case <-guard:
			r.t.Fatalf("%d connections are still open", r.f.open.Load())
		}
	}
}

// A message is announced once, ever: a new service on the same store, as
// after a restart of the app, does not announce it again, and announces
// what arrived while the app was closed.
func TestWatchAnnouncesOnceAcrossRestart(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	if ev := r.checked(); len(ev.New) != 1 {
		t.Fatalf("new mail = %v", subjects(ev.New))
	}
	r.svc.StopWatching()

	// The app is closed; a message arrives; the app starts again.
	deliver(t, f, "INBOX", readFixture(t, "alternative.eml"))
	r.use(NewService(r.store, r.secrets, ""), false)
	r.watch()
	ev := r.checked()
	if len(ev.New) != 1 || ev.New[0].UID != 3 {
		t.Fatalf("after the restart new mail = %+v, want only the message that arrived meanwhile", ev.New)
	}
	// Watching the account anew, as after its settings changed, starts
	// from the same watermark.
	r.watch()
	if ev := r.checked(); len(ev.New) != 0 {
		t.Fatalf("watching anew announced %v again", subjects(ev.New))
	}
}

// Mail that another mail client read before the watcher saw it is not
// announced.
func TestWatchSkipsMailAlreadyRead(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	deliver(t, f, "INBOX", readFixture(t, "plain.eml"), imap.FlagSeen)
	if ev := r.checked(); len(ev.New) != 0 {
		t.Fatalf("read mail was announced: %v", subjects(ev.New))
	}
	deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
	ev := r.checked()
	if got := subjects(ev.New); !reflect.DeepEqual(got, []string{"HTML only"}) {
		t.Fatalf("new mail = %v, want only the unread message", got)
	}
}

// When the inbox comes back under another UIDVALIDITY all of its mail
// looks new. None of it is announced; what arrives after is.
func TestWatchUIDValidityChangeAnnouncesNothing(t *testing.T) {
	for _, noIdle := range []bool{true, false} {
		name := "idle"
		if noIdle {
			name = "polling"
		}
		t.Run(name, func(t *testing.T) {
			f := startFakeIMAP(t, allCaps())
			f.create(t, "INBOX")
			f.append(t, "INBOX", readFixture(t, "plain.eml"))
			r := newWatchRig(t, f, "secret", noIdle)
			r.watch()
			r.checked()
			wait := r.timer()
			before, _, _ := r.store.Folder(context.Background(), r.account.ID, "INBOX")

			if err := f.user.Delete("INBOX"); err != nil {
				t.Fatal(err)
			}
			f.create(t, "INBOX")
			f.append(t, "INBOX", readFixture(t, "html-only.eml"))
			f.append(t, "INBOX", readFixture(t, "alternative.eml"))
			wait.fire()
			ev := r.checked()
			after, _, _ := r.store.Folder(context.Background(), r.account.ID, "INBOX")
			if after.UIDValidity == before.UIDValidity {
				t.Fatalf("UIDVALIDITY did not change: %d", after.UIDValidity)
			}
			if len(ev.New) != 0 {
				t.Fatalf("a UIDVALIDITY change announced %v", subjects(ev.New))
			}
			if got, _ := r.svc.CachedMessages(context.Background(), r.account.ID, "INBOX", "", 50); len(got) != 2 {
				t.Errorf("the cache holds %d messages after the change, want the 2 of the new inbox", len(got))
			}

			wait = r.timer()
			deliver(t, f, "INBOX", readFixture(t, "attachment.eml"))
			if noIdle {
				wait.fire()
			}
			if ev := r.checked(); len(ev.New) != 1 || ev.New[0].UID != 3 {
				t.Fatalf("new mail after the change = %+v, want the one delivered", ev.New)
			}
		})
	}
}

// The watcher only reads. After checks that fetched new mail, by push
// and by polling, every flag on the server is what it was, and every
// time the watcher opened a mailbox it opened it read-only.
func TestWatchLeavesTheServerUntouched(t *testing.T) {
	for _, noIdle := range []bool{false, true} {
		name := "idle"
		if noIdle {
			name = "polling"
		}
		t.Run(name, func(t *testing.T) {
			f := startFakeIMAP(t, allCaps())
			f.create(t, "INBOX")
			f.append(t, "INBOX", readFixture(t, "plain.eml"))
			deliver(t, f, "INBOX", readFixture(t, "alternative.eml"), imap.FlagFlagged)
			deliver(t, f, "INBOX", readFixture(t, "attachment.eml"), imap.FlagSeen)
			r := newWatchRig(t, f, "secret", noIdle)
			r.watch()
			r.checked()
			wait := r.timer()

			deliver(t, f, "INBOX", readFixture(t, "html-only.eml"))
			deliver(t, f, "INBOX", readFixture(t, "plain.eml"))
			want := serverFlags(t, f, "INBOX")
			if len(want) != 5 || len(want[1]) != 0 || len(want[4]) != 0 {
				t.Fatalf("the server's flags before the checks = %v", want)
			}
			f.mu.Lock()
			f.selects = nil
			f.mu.Unlock()

			if noIdle {
				wait.fire()
			}
			announced := 0
			for announced < 2 {
				announced += len(r.checked().New)
			}
			// Another full check, of the folders and the inbox again.
			r.svc.Nudge()
			r.checked()
			r.svc.StopWatching()

			if got := serverFlags(t, f, "INBOX"); !reflect.DeepEqual(got, want) {
				t.Errorf("the server's flags changed:\n got %v\nwant %v", got, want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, s := range f.selects {
				if !s.readOnly {
					t.Errorf("%s was opened for writing", s.mailbox)
				}
			}
			// The body of the new mail was fetched, for the preview, yet
			// read from the cache it is unread still.
			msgs, _ := r.svc.CachedMessages(context.Background(), r.account.ID, "INBOX", "", 50)
			for _, m := range msgs {
				if m.UID >= 4 && (m.Seen || m.Preview == "") {
					t.Errorf("cached new message %d: seen %v, preview %q", m.UID, m.Seen, m.Preview)
				}
			}
		})
	}
}

// A server that goes away puts the watcher in backoff, with waits that
// grow. A nudge tries at once, and when the server is back the mail that
// arrived meanwhile comes as one batch.
func TestWatchBacksOffAndRecovers(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	checked := r.checked().Status.CheckedAt
	r.timer() // the idle's

	f.stop()
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		ev := r.state(WatchRetrying)
		if ev.Status.Failure != WatchFailNetwork {
			t.Fatalf("failure %d = %v, want the network", i, ev.Status.Failure)
		}
		if !ev.Status.CheckedAt.Equal(checked) {
			t.Errorf("failure %d forgot when mail was last checked: %v", i, ev.Status.CheckedAt)
		}
		if ev.Folders != nil || len(ev.New) != 0 {
			t.Errorf("failure %d reported mail: %+v", i, ev)
		}
		wait := r.timer()
		waits = append(waits, wait.d)
		if i < 3 {
			wait.fire()
			r.state(WatchConnecting)
		}
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits between tries = %v, want %v", waits, want)
	}

	// Three messages arrive while the server cannot be reached from
	// here. It comes back, and nothing waits for the 40 seconds: a nudge
	// tries now.
	f.append(t, "INBOX", readFixture(t, "html-only.eml"))
	f.append(t, "INBOX", readFixture(t, "alternative.eml"))
	f.append(t, "INBOX", readFixture(t, "attachment.eml"))
	f.restart(t)
	r.svc.Nudge()
	r.state(WatchConnecting)
	ev := r.checked()
	if len(ev.New) != 3 {
		t.Fatalf("the backlog came as %d messages, want all 3 in one report: %v", len(ev.New), subjects(ev.New))
	}
	if ev.New[0].UID != 4 || ev.New[2].UID != 2 {
		t.Errorf("the backlog is not newest first: %+v", ev.New)
	}
	if ev.Status.State != WatchUpToDate || ev.Status.Failure != WatchFailNone {
		t.Errorf("status after recovery = %+v", ev.Status)
	}
	r.timer()
	r.quiet("after the backlog")

	// The count of failures starts again after a check that worked.
	f.stop()
	r.state(WatchRetrying)
	if wait := r.timer(); wait.d != 5*time.Second {
		t.Errorf("the first wait after a recovery = %v, want 5s", wait.d)
	}
}

// The waits stop growing at the cap.
func TestWatchBackoffIsCapped(t *testing.T) {
	// A port nothing listens on refuses every connection at once.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIMAP{addr: ln.Addr().String()}
	ln.Close()
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	var last time.Duration
	for i := 0; i < 9; i++ {
		r.state(WatchRetrying)
		wait := r.timer()
		if wait.d < last || wait.d > watchBackoffMax {
			t.Fatalf("wait %d = %v after %v", i, wait.d, last)
		}
		last = wait.d
		wait.fire()
	}
	if last != watchBackoffMax {
		t.Errorf("the wait settled at %v, want the cap of %v", last, watchBackoffMax)
	}
}

// Without a network the watcher does not keep trying: it says it is
// offline and arms no timer. The network coming back makes it try at
// once.
func TestWatchPausesOffline(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.timer() // the idle's

	// The server is out of reach while the system still says online: the
	// watcher retries. Then the system says offline, and it stops.
	f.stop()
	r.state(WatchRetrying)
	r.timer()
	r.svc.SetOnline(false)
	if ev := r.state(WatchOffline); ev.Status.Failure != WatchFailNetwork {
		t.Errorf("offline status = %+v", ev.Status)
	}
	select {
	case timer := <-r.clock.armed:
		t.Fatalf("offline, the watcher armed a timer of %v", timer.d)
	default:
	}
	r.quiet("while offline")

	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	f.restart(t)
	r.svc.SetOnline(true)
	r.state(WatchConnecting)
	if ev := r.checked(); len(ev.New) != 1 {
		t.Fatalf("back online, new mail = %v", subjects(ev.New))
	}

	// A watcher that fails while the system already says offline goes
	// straight to waiting for the network.
	r.timer()
	r.svc.SetOnline(false)
	f.stop()
	r.state(WatchOffline)
	select {
	case timer := <-r.clock.armed:
		t.Fatalf("offline, the watcher armed a timer of %v", timer.d)
	default:
	}
}

// A refused sign-in is said as such, and tried again only after the
// longest wait: the same password is refused the same way.
func TestWatchReportsRefusedSignIn(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "not the password", false)
	r.watch()
	ev := r.state(WatchRetrying)
	if ev.Status.Failure != WatchFailAuth {
		t.Fatalf("failure = %v, want a refused sign-in", ev.Status.Failure)
	}
	if wait := r.timer(); wait.d != watchBackoffMax {
		t.Errorf("the wait after a refused sign-in = %v, want %v", wait.d, watchBackoffMax)
	}
}

// Stopping returns when the watcher's goroutine has ended, and its
// connection is closed.
func TestWatchStopEndsGoroutineAndConnection(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.timer()
	if got := f.open.Load(); got != 1 {
		t.Fatalf("the idling watcher holds %d connections, want 1", got)
	}
	r.svc.watchMu.Lock()
	w := r.svc.watchers[r.account.ID]
	r.svc.watchMu.Unlock()
	if w == nil {
		t.Fatal("no watcher for the account")
	}

	r.svc.Unwatch(r.account.ID)
	select {
	case <-w.done:
	default:
		t.Fatal("Unwatch returned while the watcher's goroutine still ran")
	}
	if w.conn != nil {
		t.Error("the stopped watcher still has a connection")
	}
	r.waitClosed()
	r.quiet("after it was stopped")
	r.svc.watchMu.Lock()
	left := len(r.svc.watchers)
	r.svc.watchMu.Unlock()
	if left != 0 {
		t.Errorf("%d watchers are left", left)
	}

	// Stopped for good, as when the app quits, no watcher starts.
	r.svc.StopWatching()
	r.watch()
	r.svc.watchMu.Lock()
	left = len(r.svc.watchers)
	r.svc.watchMu.Unlock()
	if left != 0 {
		t.Error("a watcher started after the service stopped watching")
	}
}

// Removing an account ends its watcher before its rows go.
func TestRemoveAccountStopsItsWatcher(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.timer()
	if err := r.svc.RemoveAccount(context.Background(), r.account.ID); err != nil {
		t.Fatal(err)
	}
	r.svc.watchMu.Lock()
	left := len(r.svc.watchers)
	r.svc.watchMu.Unlock()
	if left != 0 {
		t.Fatalf("%d watchers are left for a removed account", left)
	}
	r.waitClosed()
	r.quiet("after the account was removed")
}

// A server that accepts the connection and then says nothing does not
// hold a watcher that is stopped: the IMAP library alone would wait 30
// seconds for the greeting.
func TestWatchStopsWhileConnecting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	r := newWatchRig(t, &fakeIMAP{addr: ln.Addr().String()}, "secret", false)
	r.watch()
	r.state(WatchConnecting)
	var conn net.Conn
	select {
	case conn = <-accepted:
		defer conn.Close()
	case <-time.After(watchGuard):
		t.Fatal("the watcher did not connect")
	}

	stopped := make(chan struct{})
	go func() {
		r.svc.Unwatch(r.account.ID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(watchGuard):
		t.Fatal("stopping waited for a server that does not answer")
	}
}

// The folder list and the counts of every folder are refreshed at the
// interval, though the server pushes for the inbox only. The interval is
// the one the settings hold, and changing it takes effect at once.
func TestWatchRefreshesCountsAtTheInterval(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	r := newWatchRig(t, f, "secret", false)
	set := DefaultSettings()
	set.CheckMinutes = 15
	if err := r.svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	r.watch()
	r.checked()
	wait := r.timer()
	if wait.d != 15*time.Minute {
		t.Fatalf("the interval = %v, want the 15 minutes of the settings", wait.d)
	}

	// Mail filed into another folder: the server says nothing of it.
	f.append(t, "Archive", readFixture(t, "plain.eml"))
	f.create(t, "Receipts")
	r.quiet("before the interval passed")
	wait.fire()
	ev := r.checked()
	byName := map[string]Folder{}
	for _, folder := range ev.Folders {
		byName[folder.Name] = folder
	}
	if got := byName["Archive"]; got.Total != 1 || got.Unread != 1 {
		t.Errorf("Archive after the interval = %+v", got)
	}
	if _, ok := byName["Receipts"]; !ok {
		t.Errorf("the new folder is missing: %v", ev.Folders)
	}
	if cached, _ := r.svc.CachedFolders(ctx, r.account.ID); len(cached) != 3 {
		t.Errorf("cached folders = %v", cached)
	}
	if !ev.Status.CheckedAt.Equal(r.clock.Now()) {
		t.Errorf("checked at %v, the clock says %v", ev.Status.CheckedAt, r.clock.Now())
	}
	r.timer()

	// A shorter interval checks now and counts from there.
	set.CheckMinutes = 1
	if err := r.svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	r.checked()
	if wait := r.timer(); wait.d != time.Minute {
		t.Errorf("the interval after the change = %v, want 1 minute", wait.d)
	}
}

// With an interval longer than an IDLE may last, the IDLE is issued
// again halfway, well inside the 29 minutes of RFC 2177, without a check.
func TestWatchRenewsIdle(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	r := newWatchRig(t, f, "secret", false)
	set := DefaultSettings()
	set.CheckMinutes = 30
	if err := r.svc.SaveSettings(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	r.watch()
	r.checked()
	first := r.timer()
	if first.d != idleRenew || idleRenew >= 29*time.Minute {
		t.Fatalf("the first idle lasts %v, want %v", first.d, idleRenew)
	}
	first.fire()
	second := r.timer()
	r.quiet("when the idle was renewed")
	if second.d != 30*time.Minute-idleRenew {
		t.Errorf("the second idle lasts %v, want the rest of the interval", second.d)
	}
	// The renewed IDLE still hears the server.
	deliver(t, f, "INBOX", readFixture(t, "plain.eml"))
	if ev := r.checked(); len(ev.New) != 1 {
		t.Fatalf("new mail on the renewed idle = %v", subjects(ev.New))
	}
	// New mail does not put off the look at the other folders: it is
	// still due when the interval ends.
	third := r.timer()
	if third.d != 30*time.Minute-idleRenew {
		t.Errorf("the idle after new mail lasts %v, want the rest of the interval", third.d)
	}
	third.fire()
	if ev := r.checked(); len(ev.New) != 0 {
		t.Errorf("the check at the interval announced %v", subjects(ev.New))
	}
	if next := r.timer(); next.d != idleRenew {
		t.Errorf("the idle after the check lasts %v, want %v", next.d, idleRenew)
	}
}

// The settings keep their defaults until set, come back from the store,
// and hold no interval that was not offered.
func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, store, secrets := newTestService()
	a := testAccount()
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckMinutes != 5 || !got.Notify || got.NotifyDetails || len(got.Muted) != 0 || !got.Conversations {
		t.Fatalf("default settings = %+v", got)
	}

	want := Settings{CheckMinutes: 30, Notify: false, NotifyDetails: true, Muted: map[string]bool{a.ID: true, "gone": true}}
	if err := svc.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	// Another service on the same store, as after a restart.
	got, err = NewService(store, secrets, "").Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckMinutes != 30 || got.Notify || !got.NotifyDetails || !got.Muted[a.ID] || got.Conversations {
		t.Errorf("settings read back = %+v", got)
	}
	want.Conversations = true
	if err := svc.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ = svc.Settings(ctx); !got.Conversations {
		t.Error("the conversation view did not come back on")
	}
	if got.Muted["gone"] {
		t.Error("an account that does not exist has a setting")
	}
	if _, ok, _ := store.Setting(ctx, MutedKey("gone")); ok {
		t.Error("a setting was stored for an account that does not exist")
	}

	want.CheckMinutes = 7
	want.Muted = nil
	if err := svc.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Settings(ctx)
	if got.CheckMinutes != DefaultCheckMinutes || got.Muted[a.ID] {
		t.Errorf("settings after an interval nobody offered = %+v", got)
	}

	// Removing the account removes its setting.
	want.Muted = map[string]bool{a.ID: true}
	if err := svc.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Setting(ctx, MutedKey(a.ID)); ok {
		t.Error("the removed account's setting is still stored")
	}
}

// What an error is sorted into, which is all the window hears of it.
func TestFailureOf(t *testing.T) {
	cases := []struct {
		err  error
		want WatchFailure
	}{
		{nil, WatchFailNone},
		{&loginError{&imap.Error{Text: "no"}}, WatchFailAuth},
		{&net.OpError{Op: "dial", Err: context.DeadlineExceeded}, WatchFailNetwork},
		{context.DeadlineExceeded, WatchFailNetwork},
		{&connLost{ErrNotFound}, WatchFailNetwork},
		{ErrNotFound, WatchFailOther},
		{&imap.Error{Text: "no such mailbox"}, WatchFailOther},
	}
	for _, c := range cases {
		if got := failureOf(c.err); got != c.want {
			t.Errorf("failureOf(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

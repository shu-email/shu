package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sendGuard is how long a send delay test waits for something that must
// happen before it gives up. The delays themselves run on sendClock.
const sendGuard = 10 * time.Second

// sendClock is the time of a send delay test: the service's clock, and
// the timers it arms, which fire only when the test fires them.
type sendClock struct {
	mu    sync.Mutex
	now   time.Time
	armed chan *sendTimer
}

// sendTimer is a timer the service armed.
type sendTimer struct {
	d     time.Duration
	c     chan time.Time
	clock *sendClock
}

func newSendClock() *sendClock {
	return &sendClock{now: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC), armed: make(chan *sendTimer, 64)}
}

func (k *sendClock) Now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.now
}

func (k *sendClock) advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.now = k.now.Add(d)
}

func (k *sendClock) newTimer(d time.Duration) (<-chan time.Time, func()) {
	t := &sendTimer{d: d, c: make(chan time.Time, 1), clock: k}
	k.armed <- t
	return t.c, func() {}
}

// fire lets the timer's time pass.
func (t *sendTimer) fire() {
	t.clock.advance(t.d)
	t.c <- t.clock.Now()
}

// timer waits for the service to arm a timer.
func (k *sendClock) timer(t *testing.T) *sendTimer {
	t.Helper()
	select {
	case tm := <-k.armed:
		return tm
	case <-time.After(sendGuard):
		t.Fatal("no timer was armed")
		return nil
	}
}

// noTimer fails if the service armed a timer.
func (k *sendClock) noTimer(t *testing.T, when string) {
	t.Helper()
	select {
	case tm := <-k.armed:
		t.Fatalf("%s a timer of %v was armed", when, tm.d)
	default:
	}
}

// newScheduleEnv is an attachment test's service on a clock and timers of
// the test's.
func newScheduleEnv(t *testing.T) (*attachEnv, *sendClock) {
	t.Helper()
	env := newAttachEnv(t, nil)
	clock := newSendClock()
	env.svc.SetClock(clock.Now)
	env.svc.newTimer = clock.newTimer
	return env, clock
}

// restartOn is the app launched again, on the same clock.
func restartOn(env *attachEnv, clock *sendClock) *Service {
	svc := env.restart()
	svc.SetClock(clock.Now)
	svc.newTimer = clock.newTimer
	return svc
}

// sendOutcome is what a SendScheduled run in the background returned.
type sendOutcome struct {
	result SendResult
	err    error
}

// sendInBackground runs SendScheduled as the window's job does.
func sendInBackground(ctx context.Context, svc *Service, id string) <-chan sendOutcome {
	done := make(chan sendOutcome, 1)
	go func() {
		result, err := svc.SendScheduled(ctx, id)
		done <- sendOutcome{result, err}
	}()
	return done
}

// outcome waits for a background send to end.
func outcome(t *testing.T, done <-chan sendOutcome) sendOutcome {
	t.Helper()
	select {
	case o := <-done:
		return o
	case <-time.After(sendGuard):
		t.Fatal("the send did not end")
		return sendOutcome{}
	}
}

// outboxState is the state of an outbox entry.
func outboxState(t *testing.T, env *attachEnv, id string) string {
	t.Helper()
	p, err := env.store.Outbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.State
}

// scheduledDraft is a draft with a file, and the message it makes.
func scheduledDraft(t *testing.T, env *attachEnv) (Draft, Attachment, Outgoing) {
	t.Helper()
	d, att := env.attach(t, Draft{
		AccountID: env.a.ID, To: "bob@example.com", Bcc: "erin@example.com", Subject: "Later", Body: "Text written",
	}, env.file(t, "notes.txt", []byte("the notes")))
	out := d.outgoing()
	out.DraftID = d.ID
	return d, att, out
}

// A message sent with a delay waits it out in the outbox, and nothing of
// it leaves meanwhile: not offered to send again, its draft and files
// kept. When the delay ends it is sent as Send sends, with progress, its
// copy in Sent, and its draft retired.
func TestScheduledSendWaitsForItsDelay(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, att, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateScheduled || !p.DueAt.Equal(clock.Now().Add(10*time.Second)) || p.DraftID != d.ID ||
		outboxState(t, env, p.ID) != StateScheduled {
		t.Fatalf("scheduled entry = %+v", p)
	}
	if pending, _ := env.svc.PendingSends(ctx, env.a.ID); len(pending) != 0 {
		t.Errorf("a waiting message is offered to send again: %+v", pending)
	}
	if _, err := env.svc.RetryPending(ctx, p.ID); err == nil {
		t.Error("a waiting message was sent again before its delay ended")
	}
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Errorf("files while it waits = %v", got)
	}

	var progress []int64
	var total int64
	done := sendInBackground(WithSendProgress(ctx, func(sent, all int64) {
		progress, total = append(progress, sent), all
	}), env.svc, p.ID)
	timer := clock.timer(t)
	if timer.d != 10*time.Second {
		t.Errorf("it waits %v, want the delay", timer.d)
	}
	select {
	case o := <-done:
		t.Fatalf("the send ended before the delay did: %+v", o)
	default:
	}
	if n := env.smtp.sessions.Load(); n != 0 {
		t.Fatalf("the SMTP server was reached %d times during the delay", n)
	}
	if got := mustDraft(t, env.svc, d.ID); got.PendingDelete {
		t.Fatal("the draft was retired during the delay")
	}

	timer.fire()
	o := outcome(t, done)
	if o.err != nil || !o.result.Copied || o.result.Outbox.State != StateSent {
		t.Fatalf("send after the delay = %+v, %v", o.result, o.err)
	}
	if len(env.smtp.received()) != 1 || len(env.imap.messages(t, "Sent")) != 1 {
		t.Errorf("delivered %d, saved %d", len(env.smtp.received()), len(env.imap.messages(t, "Sent")))
	}
	if len(progress) == 0 || progress[len(progress)-1] != total || total == 0 {
		t.Errorf("progress %v of %d", progress, total)
	}
	if _, ok, _ := env.svc.Draft(ctx, d.ID); ok {
		t.Error("the draft outlived its message")
	}
	if state := outboxState(t, env, p.ID); state != StateSent {
		t.Errorf("outbox state = %q", state)
	}
	if m, err := ParseMessage([]byte(env.smtp.received()[0])); err != nil || m.Subject != "Later" || len(m.Attachments) != 1 {
		t.Errorf("delivered %+v, %v", m.Summary, err)
	}
}

// The outbox keeps whole seconds. A message is due at the first whole
// second after its delay, never before, so that it does not leave before
// the time the window shows, and the time kept is the time returned.
func TestScheduledMessageIsDueOnAWholeSecond(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	clock.advance(900 * time.Millisecond)
	p, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Soon"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Now().Add(10 * time.Second).Truncate(time.Second).Add(time.Second); !p.DueAt.Equal(want) {
		t.Errorf("due at %v, want %v", p.DueAt, want)
	}
	if stored, err := env.store.Outbox(ctx, p.ID); err != nil || stored.DueAt.Unix() != p.DueAt.Unix() || p.DueAt.Nanosecond() != 0 {
		t.Errorf("kept due at %v, %v", stored.DueAt, err)
	}
	done := sendInBackground(ctx, env.svc, p.ID)
	timer := clock.timer(t)
	if timer.d != 10*time.Second+100*time.Millisecond {
		t.Errorf("it waits %v", timer.d)
	}
	timer.fire()
	if o := outcome(t, done); o.err != nil {
		t.Fatal(o.err)
	}
}

// Undo within the delay keeps the message from being sent at all: the
// SMTP server is never reached, the draft stays with its files, and the
// window gets the draft back to write on. It can then be sent again.
func TestUnsendTakesTheMessageBack(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, att, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := sendInBackground(ctx, env.svc, p.ID)
	clock.timer(t)

	back, err := env.svc.Unsend(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != d.ID || back.To != d.To || back.Bcc != d.Bcc || back.Subject != "Later" || back.Body != "Text written" ||
		back.PendingDelete || !sameFiles(back.Attachments, att) {
		t.Errorf("draft given back = %+v", back)
	}
	if o := outcome(t, done); !errors.Is(o.err, ErrUnsent) {
		t.Errorf("the waiting send ended with %v, want ErrUnsent", o.err)
	}
	if n := env.smtp.sessions.Load(); n != 0 || len(env.smtp.received()) != 0 {
		t.Errorf("the SMTP server was reached %d times", n)
	}
	if state := outboxState(t, env, p.ID); state != StateCancelled {
		t.Errorf("outbox state = %q", state)
	}
	if pending, _ := env.svc.PendingSends(ctx, env.a.ID); len(pending) != 0 {
		t.Errorf("the message taken back is offered to send: %+v", pending)
	}
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Errorf("files after the undo = %v", got)
	}
	if got := mustDraft(t, env.svc, d.ID); !sameFiles(got.Attachments, att) {
		t.Errorf("draft after the undo = %+v", got)
	}
	// Taken back is taken back: neither a second undo nor the send nor a
	// restart brings it out.
	if _, err := env.svc.Unsend(ctx, p.ID); !errors.Is(err, ErrUnsent) {
		t.Errorf("a second undo = %v", err)
	}
	if _, err := env.svc.SendScheduled(ctx, p.ID); !errors.Is(err, ErrUnsent) {
		t.Errorf("sending it after the undo = %v", err)
	}
	clock.advance(time.Hour)
	if results, err := restartOn(env, clock).DeliverScheduled(ctx); err != nil || len(results) != 0 {
		t.Errorf("a restart sent %+v, %v", results, err)
	}
	if env.smtp.sessions.Load() != 0 {
		t.Fatal("the message taken back reached the server")
	}

	again := back.outgoing()
	again.DraftID = back.ID
	if _, err := env.svc.Send(ctx, env.a.ID, again); err != nil {
		t.Fatal(err)
	}
	if received := env.smtp.received(); len(received) != 1 || !strings.Contains(received[0], "Text written") {
		t.Errorf("sent again: %d messages", len(received))
	}
}

// An undo that comes once the message has begun to leave changes
// nothing, and says the message left.
func TestUnsendAfterTheDelayIsTooLate(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	entered, release := make(chan struct{}), make(chan struct{})
	env.smtp.mu.Lock()
	env.smtp.onData = func(r io.Reader) error {
		entered <- struct{}{}
		<-release
		data, err := io.ReadAll(r)
		env.smtp.mu.Lock()
		env.smtp.messages = append(env.smtp.messages, string(data))
		env.smtp.mu.Unlock()
		return err
	}
	env.smtp.mu.Unlock()
	d, _, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := sendInBackground(ctx, env.svc, p.ID)
	clock.timer(t).fire()
	select {
	case <-entered:
	case <-time.After(sendGuard):
		t.Fatal("the message did not leave when its delay ended")
	}
	if _, err := env.svc.Unsend(ctx, p.ID); !errors.Is(err, ErrAlreadyLeft) {
		t.Errorf("undo while it leaves = %v, want ErrAlreadyLeft", err)
	}
	close(release)
	if o := outcome(t, done); o.err != nil {
		t.Fatalf("send = %v", o.err)
	}
	if len(env.smtp.received()) != 1 {
		t.Errorf("delivered %d messages", len(env.smtp.received()))
	}
	if _, ok, _ := env.svc.Draft(ctx, d.ID); ok {
		t.Error("the draft of a message sent stayed")
	}
	if _, err := env.svc.Unsend(ctx, p.ID); !errors.Is(err, ErrAlreadyLeft) {
		t.Errorf("undo once it was sent = %v", err)
	}
}

// Undo and the end of the delay at the same moment: exactly one wins,
// every time. Either nothing is sent and the send says it was taken back,
// or the message is delivered once and the undo says it left.
func TestUnsendRacesTheEndOfTheDelay(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	const rounds = 40
	undone := 0
	for i := 0; i < rounds; i++ {
		out := Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: fmt.Sprintf("Race %d", i), Text: "x"}
		p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		before := len(env.smtp.received())
		done := sendInBackground(ctx, env.svc, p.ID)
		timer := clock.timer(t)
		start := make(chan struct{})
		var unsendErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			timer.fire()
		}()
		go func() {
			defer wg.Done()
			<-start
			// The undo is quicker than the send it races, which has to
			// wake up first: by yielding a few times, more in some rounds
			// than in others, it wins some and loses some.
			for range i % 8 {
				runtime.Gosched()
			}
			_, unsendErr = env.svc.Unsend(ctx, p.ID)
		}()
		close(start)
		wg.Wait()
		o := outcome(t, done)
		after := len(env.smtp.received())
		state := outboxState(t, env, p.ID)
		switch {
		case unsendErr == nil:
			undone++
			if !errors.Is(o.err, ErrUnsent) || after != before || state != StateCancelled {
				t.Fatalf("round %d: undone, yet the send ended with %v, %d delivered, state %q", i, o.err, after-before, state)
			}
		case errors.Is(unsendErr, ErrAlreadyLeft):
			if o.err != nil || after != before+1 || state != StateSent {
				t.Fatalf("round %d: too late to undo, yet the send ended with %v, %d delivered, state %q", i, o.err, after-before, state)
			}
		default:
			t.Fatalf("round %d: undo = %v", i, unsendErr)
		}
	}
	t.Logf("the undo won %d of %d rounds", undone, rounds)
	if got := len(env.smtp.received()); got != rounds-undone {
		t.Errorf("delivered %d messages, want %d", got, rounds-undone)
	}
}

// A message whose delay ended while the app was closed is sent when it
// starts again, once: the user asked for it to be sent. The window learns
// that it was.
func TestScheduledMessageLeavesAfterARestart(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, _, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The app quits: nothing waits on the message.
	clock.advance(time.Hour)
	restarted := restartOn(env, clock)
	results, err := restarted.DeliverScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Outbox.ID != p.ID ||
		results[0].Outbox.Envelope.Subject != "Later" || !results[0].Result.Copied {
		t.Fatalf("delivered after the restart: %+v", results)
	}
	clock.noTimer(t, "for a message overdue")
	if len(env.smtp.received()) != 1 {
		t.Fatalf("delivered %d messages", len(env.smtp.received()))
	}
	if _, ok, _ := restarted.Draft(ctx, d.ID); ok {
		t.Error("the draft outlived its message")
	}
	// Once.
	if results, err := restarted.DeliverScheduled(ctx); err != nil || len(results) != 0 {
		t.Errorf("a second call sent %+v, %v", results, err)
	}
	if results, err := restartOn(env, clock).DeliverScheduled(ctx); err != nil || len(results) != 0 {
		t.Errorf("another restart sent %+v, %v", results, err)
	}
	if len(env.smtp.received()) != 1 {
		t.Errorf("delivered %d messages in all", len(env.smtp.received()))
	}
}

// A message whose delay had not ended when the app quit leaves when it
// ends, and can be taken back until then.
func TestScheduledMessageNotYetDueAfterARestart(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	first, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "First"}, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Second"}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(5 * time.Second)
	restarted := restartOn(env, clock)
	type delivered struct {
		results []ScheduledResult
		err     error
	}
	done := make(chan delivered, 1)
	go func() {
		results, err := restarted.DeliverScheduled(ctx)
		done <- delivered{results, err}
	}()
	timer := clock.timer(t)
	if timer.d != 15*time.Second {
		t.Errorf("the first waits %v more, want 15s", timer.d)
	}
	timer.fire()
	timer = clock.timer(t)
	if timer.d != 10*time.Second {
		t.Errorf("the second waits %v more, want 10s", timer.d)
	}
	if _, err := restarted.Unsend(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	var got delivered
	select {
	case got = <-done:
	case <-time.After(sendGuard):
		t.Fatal("the restart's delivery did not end")
	}
	if got.err != nil || len(got.results) != 1 || got.results[0].Outbox.ID != first.ID || got.results[0].Err != nil {
		t.Errorf("delivered %+v, %v", got.results, got.err)
	}
	received := env.smtp.received()
	if len(received) != 1 || !strings.Contains(received[0], "Subject: First") {
		t.Errorf("delivered %d messages", len(received))
	}
}

// A message that had begun to leave when the app stopped is not sent
// again on its own: the server may have it. It is offered as unknown.
func TestRestartLeavesASendingMessageUnknown(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, _, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Claimed for sending, and the app gone before it recorded more.
	if won, err := env.store.ClaimOutbox(ctx, p.ID, StateScheduled, StateSending); err != nil || !won {
		t.Fatal(won, err)
	}
	clock.advance(time.Hour)
	restarted := restartOn(env, clock)
	if results, err := restarted.DeliverScheduled(ctx); err != nil || len(results) != 0 {
		t.Errorf("the restart sent %+v, %v", results, err)
	}
	if n := env.smtp.sessions.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
	pending, _ := restarted.PendingSends(ctx, env.a.ID)
	if len(pending) != 1 || pending[0].ID != p.ID || pending[0].State != StateUnknown {
		t.Errorf("outbox = %+v", pending)
	}
	if _, err := restarted.Unsend(ctx, p.ID); !errors.Is(err, ErrAlreadyLeft) {
		t.Errorf("undo of a message that may have left = %v", err)
	}
	if got := mustDraft(t, restarted, d.ID); got.PendingDelete {
		t.Error("the draft of a message that may not have left was retired")
	}
}

// Without a delay a message leaves at once, as it always did: no timer,
// no waiting entry, the progress told and the copy saved.
func TestSendWithoutDelayIsUnchanged(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, _, out := scheduledDraft(t, env)
	var progress []int64
	var total int64
	result, err := env.svc.Send(WithSendProgress(ctx, func(sent, all int64) {
		progress, total = append(progress, sent), all
	}), env.a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	clock.noTimer(t, "for a message sent at once")
	if !result.Copied || result.Outbox.State != StateSent || !result.Outbox.DueAt.IsZero() {
		t.Errorf("result = %+v", result)
	}
	if len(progress) == 0 || progress[len(progress)-1] != total {
		t.Errorf("progress %v of %d", progress, total)
	}
	if len(env.smtp.received()) != 1 || len(env.imap.messages(t, "Sent")) != 1 {
		t.Errorf("delivered %d, saved %d", len(env.smtp.received()), len(env.imap.messages(t, "Sent")))
	}
	if _, ok, _ := env.svc.Draft(ctx, d.ID); ok {
		t.Error("the draft outlived its message")
	}
	if scheduled, _ := env.store.ScheduledSends(ctx); len(scheduled) != 0 {
		t.Errorf("waiting entries = %+v", scheduled)
	}

	// Scheduled with no delay, it is due at once and waits for nothing.
	p, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Now"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.DueAt.Equal(clock.Now()) {
		t.Errorf("due at %v, now is %v", p.DueAt, clock.Now())
	}
	if _, err := env.svc.SendScheduled(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	clock.noTimer(t, "for a message without a delay")
	if len(env.smtp.received()) != 2 {
		t.Errorf("delivered %d messages", len(env.smtp.received()))
	}
}

// A message is scheduled only if it could be sent: what Send refuses
// before anything leaves, Schedule refuses, and writes nothing.
func TestScheduleRefusesWhatSendRefuses(t *testing.T) {
	ctx := context.Background()
	env, _ := newScheduleEnv(t)
	if _, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{Subject: "Nobody"}, time.Second); err == nil {
		t.Error("a message without recipients was scheduled")
	}
	if _, err := env.svc.Schedule(ctx, "nobody", Outgoing{To: []Address{{Email: "bob@example.com"}}}, time.Second); !errors.Is(err, ErrNotFound) {
		t.Errorf("a message of no account = %v", err)
	}
	_, att, out := scheduledDraft(t, env)
	if err := env.svc.DiscardDraft(ctx, out.DraftID); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); slices.Contains(got, att.ContentHash) {
		t.Fatalf("the discarded draft's file stayed: %v", got)
	}
	if _, err := env.svc.Schedule(ctx, env.a.ID, out, time.Second); !errors.Is(err, ErrAttachmentMissing) {
		t.Errorf("a message whose file is gone = %v", err)
	}
	if scheduled, _ := env.store.ScheduledSends(ctx); len(scheduled) != 0 {
		t.Errorf("waiting entries = %+v", scheduled)
	}
	if n := env.smtp.sessions.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
}

// The same message sent again while an earlier send of it waits replaces
// that one, whether it is sent with a delay or at once: it is delivered
// once, in its latest form.
func TestLaterSendReplacesAWaitingOne(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d := mustSaveDraft(t, env.svc, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Offer", Body: "First"})
	out := d.outgoing()
	out.DraftID = d.ID
	first, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := sendInBackground(ctx, env.svc, first.ID)
	clock.timer(t)

	// The user opens the draft again, edits it and sends it.
	d.Body = "Edited"
	d = mustSaveDraft(t, env.svc, d)
	out = d.outgoing()
	out.DraftID = d.ID
	second, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, firstDone); !errors.Is(o.err, ErrUnsent) {
		t.Errorf("the replaced send ended with %v", o.err)
	}
	secondDone := sendInBackground(ctx, env.svc, second.ID)
	clock.timer(t).fire()
	if o := outcome(t, secondDone); o.err != nil {
		t.Fatal(o.err)
	}
	received := env.smtp.received()
	if len(received) != 1 || !strings.Contains(received[0], "Edited") {
		t.Fatalf("delivered %d messages", len(received))
	}

	// Sent at once while a send of it waits.
	d = mustSaveDraft(t, env.svc, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Again", Body: "Once"})
	out = d.outgoing()
	out.DraftID = d.ID
	waiting, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	if state := outboxState(t, env, waiting.ID); state != StateCancelled {
		t.Errorf("the waiting send is %q", state)
	}
	if _, err := env.svc.SendScheduled(ctx, waiting.ID); !errors.Is(err, ErrUnsent) {
		t.Errorf("the waiting send = %v", err)
	}
	if got := len(env.smtp.received()); got != 2 {
		t.Errorf("delivered %d messages in all, want 2", got)
	}
}

// The app quitting while a message waits does not take it back: the user
// asked for it to be sent, and it is sent after the next start.
func TestScheduledMessageOutlivesTheApp(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	_, _, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quit, cancel := context.WithCancel(ctx)
	done := sendInBackground(quit, env.svc, p.ID)
	clock.timer(t)
	cancel()
	if o := outcome(t, done); !errors.Is(o.err, context.Canceled) {
		t.Errorf("the send ended with %v", o.err)
	}
	if state := outboxState(t, env, p.ID); state != StateScheduled {
		t.Fatalf("outbox state = %q", state)
	}
	if n := env.smtp.sessions.Load(); n != 0 {
		t.Fatalf("the server was reached %d times", n)
	}
	clock.advance(time.Minute)
	if results, err := restartOn(env, clock).DeliverScheduled(ctx); err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("after the next start: %+v, %v", results, err)
	}
	if len(env.smtp.received()) != 1 {
		t.Errorf("delivered %d messages", len(env.smtp.received()))
	}
}

// A message taken back whose draft is gone, discarded while it waited or
// never written, comes back as a new draft with its text and its files.
func TestUnsendMakesADraftWhenThereIsNone(t *testing.T) {
	ctx := context.Background()
	env, clock := newScheduleEnv(t)
	d, att, out := scheduledDraft(t, env)
	p, err := env.svc.Schedule(ctx, env.a.ID, out, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	// The waiting message holds its file.
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Fatalf("files = %v", got)
	}
	back, err := env.svc.Unsend(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID == d.ID || back.To != "bob@example.com" || back.Bcc != "erin@example.com" || back.Subject != "Later" ||
		back.Body != "Text written" || back.MessageID != d.MessageID || !sameFiles(back.Attachments, att) {
		t.Errorf("draft made = %+v", back)
	}
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Errorf("files after the sweep = %v", got)
	}

	alone, err := env.svc.Schedule(ctx, env.a.ID, Outgoing{
		To: []Address{{Name: "Bob", Email: "bob@example.com"}}, Subject: "No draft", Text: "Typed",
	}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	made, err := env.svc.Unsend(ctx, alone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if made.To != "Bob <bob@example.com>" || made.Body != "Typed" || made.Subject != "No draft" {
		t.Errorf("draft made of a message without one = %+v", made)
	}
	if n := env.smtp.sessions.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
	clock.noTimer(t, "without a send waiting")
}

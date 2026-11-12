package mail

import (
	"context"
	"errors"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// A message can wait a few seconds before it leaves, its send delay, in
// which the user can take it back. Its outbox entry is written at once,
// scheduled, with the time it is due, so that it outlives the app: the
// user asked for it to be sent. SendScheduled waits for that time and
// sends it, Unsend takes it back until then, and DeliverScheduled sends
// those an earlier run of the app left waiting. One change of the entry's
// state in the store decides between sending and taking back, from
// scheduled to sending or to cancelled, and only one of them makes it.

// Errors of a message sent with a delay.
var (
	// ErrUnsent is a scheduled message that was taken back before it
	// left: by Unsend, or by a later send of the same message, which
	// replaced it. None of it was sent.
	ErrUnsent = errors.New("mail: the message was taken back before it was sent")
	// ErrAlreadyLeft is an Unsend that came too late: the message had
	// begun to leave. The send that took it says how that ended.
	ErrAlreadyLeft = errors.New("mail: the message has already left")
)

// ScheduledResult is what became of a message DeliverScheduled sent: its
// outbox entry as it waited, with its account and envelope, and what Send
// would have returned for it.
type ScheduledResult struct {
	Outbox PendingSend
	Result SendResult
	Err    error
}

// Schedule writes a message to the outbox, to leave once delay has passed,
// and returns its entry with the time it is due. Nothing is sent yet:
// SendScheduled sends it then, and until then Unsend takes it back. The
// draft it was written as stays a draft meanwhile, and goes when it has
// left if it still says what was sent, so the draft is saved with what is
// scheduled, as before Send. It refuses what Send refuses before anything
// leaves: no recipients, an account that has to sign in again, a file
// that is gone. A send of the same message still waiting is taken back:
// this one replaces it.
func (s *Service) Schedule(ctx context.Context, accountID string, out Outgoing, delay time.Duration) (PendingSend, error) {
	a, _, err := s.account(ctx, accountID)
	if err != nil {
		return PendingSend{}, err
	}
	now := s.now()
	if out, err = s.prepare(ctx, a, out); err != nil {
		return PendingSend{}, err
	}
	// The outbox keeps whole seconds: the time is rounded up to one, so
	// that the message never leaves before the time the window shows.
	due := now.Add(max(delay, 0))
	if whole := due.Truncate(time.Second); whole.Before(due) {
		due = whole.Add(time.Second)
	}
	pending := PendingSend{
		ID: newOutboxID(), AccountID: accountID, Envelope: out,
		State: StateScheduled, CreatedAt: now, DueAt: due,
	}
	if len(out.Attachments) == 0 {
		if _, pending.Raw, err = s.buildMessage(a, out, now); err != nil {
			return PendingSend{}, err
		}
	} else if _, err := s.newMessage(a, out, now); err != nil {
		// Its files are encoded when it leaves, and checked as it is
		// written to the outbox.
		return PendingSend{}, err
	}
	if s.draftMessageID(ctx, accountID, out.DraftID) != "" {
		pending.DraftID = out.DraftID
	}
	s.watchSend(pending.ID)
	if err := s.enqueue(ctx, &pending); err != nil {
		s.unwatchSend(pending.ID)
		return PendingSend{}, err
	}
	s.cancelScheduled(context.WithoutCancel(ctx), accountID, out.MessageID, pending.ID)
	return pending, nil
}

// SendScheduled waits until a scheduled message is due and sends it as
// Send does, returning what Send returns; WithSendProgress reports on it
// as on Send. It returns ErrUnsent at once when the message is taken back
// while it waits. When ctx ends while it waits, it returns ctx's error
// and the message stays scheduled, for DeliverScheduled to send after the
// next start: so the job that waits on it lasts as long as the app, not
// as a window. Once the message is due, ctx ending stops it as it stops
// Send.
func (s *Service) SendScheduled(ctx context.Context, id string) (SendResult, error) {
	p, err := s.store.Outbox(ctx, id)
	if err != nil {
		return SendResult{}, err
	}
	if p.State != StateScheduled {
		return SendResult{}, notScheduled(p.State)
	}
	taken, _ := s.watchSend(id)
	defer s.unwatchSend(id)
	if err := s.waitDue(ctx, p.DueAt, taken); err != nil {
		return SendResult{}, err
	}
	return s.sendDue(ctx, p)
}

// Unsend takes back a scheduled message that has not begun to leave, and
// returns the draft it was written as, to edit and send again: the draft
// as it is now, or a new one made from the message, with its files, when
// it has none left. None of the message was sent, and the SendScheduled
// waiting on it returns ErrUnsent. Unsend returns ErrAlreadyLeft when the
// message had begun to leave, and ErrUnsent when it was taken back
// already.
func (s *Service) Unsend(ctx context.Context, id string) (Draft, error) {
	p, err := s.store.Outbox(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	// The entry holds the message's files until it is cancelled, and the
	// draft after it: a sweep must not come in between.
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	won, err := s.store.ClaimOutbox(ctx, id, StateScheduled, StateCancelled)
	if err != nil {
		return Draft{}, err
	}
	if !won {
		return Draft{}, s.claimLost(ctx, id)
	}
	s.takenBack(id)
	return s.unsentDraft(context.WithoutCancel(ctx), p)
}

// DeliverScheduled sends the messages an earlier run of the app scheduled
// that had not left when it ended: the user asked for them to be sent.
// Those whose time came while the app was closed go at once, the others
// when it comes, and until then Unsend can take one back. The messages
// this run scheduled are left to the SendScheduled waiting on them. It
// returns what became of each it sent, in the order they were due: one
// it could not send stays in the outbox, among PendingSends, for the user
// to send again. The app calls it once as it starts, in a job of its own.
// ctx ending leaves the messages not yet due waiting for the next start.
func (s *Service) DeliverScheduled(ctx context.Context) ([]ScheduledResult, error) {
	scheduled, err := s.store.ScheduledSends(ctx)
	if err != nil {
		return nil, err
	}
	var out []ScheduledResult
	for _, p := range scheduled {
		taken, ours := s.watchSend(p.ID)
		if !ours {
			continue
		}
		if err := s.waitDue(ctx, p.DueAt, taken); err != nil {
			s.unwatchSend(p.ID)
			if errors.Is(err, ErrUnsent) {
				continue
			}
			// This one and those after it wait for the next start.
			return out, err
		}
		result, err := s.sendDue(ctx, p)
		s.unwatchSend(p.ID)
		if errors.Is(err, ErrUnsent) || errors.Is(err, ErrAlreadyLeft) {
			// Taken back, or by another send, as it came due.
			continue
		}
		out = append(out, ScheduledResult{Outbox: p, Result: result, Err: err})
		if err := ctx.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// waitDue waits until a scheduled message is due, which it may be
// already, unless it is taken back or ctx ends first.
func (s *Service) waitDue(ctx context.Context, due time.Time, taken <-chan struct{}) error {
	wait := due.Sub(s.now())
	if wait <= 0 {
		return nil
	}
	timer, stop := s.newTimer(wait)
	defer stop()
	select {
	case <-timer:
		return nil
	case <-taken:
		return ErrUnsent
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendDue sends a scheduled message whose time has come, unless it was
// taken back first: claiming it for sending is the one step that decides,
// and Unsend's claim is the other.
func (s *Service) sendDue(ctx context.Context, p PendingSend) (SendResult, error) {
	// What became of it is recorded, whatever ctx does.
	record := context.WithoutCancel(ctx)
	won, err := s.store.ClaimOutbox(ctx, p.ID, StateScheduled, StateSending)
	if err != nil {
		return SendResult{}, err
	}
	if !won {
		return SendResult{}, s.claimLost(record, p.ID)
	}
	a, password, err := s.account(ctx, p.AccountID)
	var msg *gomail.Msg
	var raw []byte
	if err == nil {
		msg, raw, err = s.scheduledMessage(a, p)
	}
	if err != nil {
		// None of it left. It waits in the outbox for the user to send
		// it again.
		_ = s.store.SetOutboxState(record, p.ID, StateFailed, err.Error())
		return SendResult{}, err
	}
	if err := s.deliverClaimed(ctx, a, password, p.ID, msg, raw); err != nil {
		return SendResult{}, err
	}
	p.State, p.SentAt = StateSent, s.now()
	result := SendResult{Outbox: p}
	sentDraft := s.retireScheduledDraft(record, p)
	superseded := s.supersede(record, p.AccountID, p.Envelope.MessageID, p.ID)
	s.afterDelivery(ctx, a, password, raw, p.Envelope.Bcc, sentDraft || superseded || len(p.Envelope.Attachments) > 0, &result)
	if sentDraft {
		s.flushDraftDeletions(ctx, p.AccountID)
	}
	return result, nil
}

// scheduledMessage is a scheduled message as it leaves: the bytes built
// when it was scheduled, which its Sent copy is too, or, for one with
// attachments, which the outbox does not keep, the message built again
// from its envelope and files under the date it was sent at. go-mail
// reads the sender and the recipients off the message.
func (s *Service) scheduledMessage(a Account, p PendingSend) (*gomail.Msg, []byte, error) {
	sentAt := p.CreatedAt.UTC()
	if len(p.Raw) > 0 {
		msg, err := s.newMessage(a, p.Envelope, sentAt)
		return msg, p.Raw, err
	}
	return s.buildMessage(a, p.Envelope, sentAt)
}

// retireScheduledDraft retires the draft a scheduled message was written
// as, if it still says what was sent: one edited while the message waited
// stays a draft. It reports whether it did.
func (s *Service) retireScheduledDraft(ctx context.Context, p PendingSend) bool {
	if p.DraftID == "" {
		return false
	}
	d, ok, err := s.store.Draft(ctx, p.DraftID)
	if err != nil || !ok || d.PendingDelete || d.AccountID != p.AccountID || !saysWhatWasSent(d, p.Envelope) {
		return false
	}
	return s.retireDraft(ctx, p.AccountID, p.DraftID)
}

// unsentDraft is the draft a message taken back was written as: the draft
// as it is now, with what was edited while the message waited, or, when
// it has none left, a new one made from the message, with those of its
// files that are there. fileMu is held.
func (s *Service) unsentDraft(ctx context.Context, p PendingSend) (Draft, error) {
	if p.DraftID != "" {
		d, ok, err := s.store.Draft(ctx, p.DraftID)
		if err != nil {
			return Draft{}, err
		}
		if ok && !d.PendingDelete {
			return d, nil
		}
	}
	a, ok, err := s.store.Account(ctx, p.AccountID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrNotFound
	}
	out := p.Envelope
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	d, err := s.createDraft(ctx, a, Draft{
		ID: NewDraftID(), AccountID: a.ID,
		To: addressesText(out.To), Cc: addressesText(out.Cc), Bcc: addressesText(out.Bcc),
		Subject: out.Subject, Body: out.Text, InReplyTo: out.InReplyTo, References: out.References,
		Identity: out.Identity, MessageID: out.MessageID,
	})
	if err != nil {
		return Draft{}, err
	}
	var files []Attachment
	for _, att := range out.Attachments {
		if s.checkAttachments([]Attachment{att}) == nil {
			files = append(files, att)
		}
	}
	if len(files) == 0 {
		return d, nil
	}
	return s.store.SetDraftAttachments(ctx, d.ID, files, s.now())
}

// cancelScheduled takes back the messages of an account still waiting to
// leave under a Message-ID, all but keep: earlier sends of a message that
// a later one replaces. It reports whether there was one.
func (s *Service) cancelScheduled(ctx context.Context, accountID, messageID, keep string) bool {
	if messageID == "" {
		return false
	}
	scheduled, err := s.store.ScheduledSends(ctx)
	if err != nil {
		return false
	}
	cancelled := false
	for _, p := range scheduled {
		if p.AccountID != accountID || p.ID == keep || p.Envelope.MessageID != messageID {
			continue
		}
		if won, err := s.store.ClaimOutbox(ctx, p.ID, StateScheduled, StateCancelled); err == nil && won {
			s.takenBack(p.ID)
			cancelled = true
		}
	}
	return cancelled
}

// claimLost says why a scheduled message could not be claimed.
func (s *Service) claimLost(ctx context.Context, id string) error {
	p, err := s.store.Outbox(ctx, id)
	if err != nil {
		return err
	}
	return notScheduled(p.State)
}

// notScheduled is the error for an entry no longer scheduled: one taken
// back, or one that began to leave.
func notScheduled(state string) error {
	if state == StateCancelled {
		return ErrUnsent
	}
	return ErrAlreadyLeft
}

// watchSend has this run look after a scheduled message, and returns the
// channel Unsend closes to end the wait for it. ours is false when the
// run looks after it already.
func (s *Service) watchSend(id string) (taken chan struct{}, ours bool) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if ch, ok := s.waits[id]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	s.waits[id] = ch
	return ch, true
}

// unwatchSend forgets a scheduled message nothing waits on any more.
func (s *Service) unwatchSend(id string) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	delete(s.waits, id)
}

// takenBack ends the wait for a message that was taken back.
func (s *Service) takenBack(id string) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if ch, ok := s.waits[id]; ok {
		close(ch)
		delete(s.waits, id)
	}
}

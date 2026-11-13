package mail

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A reminder is a conversation the user asked to come back to: "Remind
// me". Setting one archives the conversation's messages in the inbox, and
// saves the reminder with the time it is due; at that time DeliverReminders
// moves them back to the inbox. The reminder is this device's, in the
// store: no server knows of it, and mail in the Archive folder is the only
// trace it was set. A reminder is one per conversation, whatever folder
// its messages are in.

// ErrNoArchive is a reminder of a conversation in the inbox, by an
// account that has no Archive folder to move it to: the conversation
// cannot leave the inbox, so no reminder is set.
var ErrNoArchive = errors.New("mail: the account has no Archive folder to remind the conversation in")

// Reminder is a conversation to come back to the inbox at a time, with
// the subject it had when the user set it.
type Reminder struct {
	ID        int64     `json:"id"`
	Account   string    `json:"account,omitempty"`
	Thread    ThreadID  `json:"thread"`
	Subject   string    `json:"subject,omitempty"`
	RemindAt  time.Time `json:"remindAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// Reminders lists an account's reminders, the soonest due first.
func (s *Service) Reminders(ctx context.Context, accountID string) ([]Reminder, error) {
	return s.store.Reminders(ctx, accountID)
}

// SetReminder sets a reminder on a conversation: its messages in the
// inbox move to the account's Archive folder and the reminder is saved
// with the time. A conversation with no message in the inbox, as one the
// user archived already, keeps its folder and still gets the reminder.
// The moves it made come with it, for the undo that takes the reminder
// and the mail back. Nothing is set when the account has an inbox message
// to move and no Archive folder.
func (s *Service) SetReminder(ctx context.Context, accountID string, thread ThreadID, at time.Time) (Reminder, []MoveResult, error) {
	_, _, err := s.account(ctx, accountID)
	if err != nil {
		return Reminder{}, nil, err
	}
	folders, err := s.store.Folders(ctx, accountID)
	if err != nil {
		return Reminder{}, nil, err
	}
	archive, inbox := "", ""
	for _, f := range folders {
		switch f.SpecialUse {
		case SpecialArchive:
			archive = f.Name
		case SpecialInbox:
			inbox = f.Name
		}
	}
	var members []Summary
	if inbox != "" {
		members, err = s.ScopedConversation(ctx, accountID, Scope{Accounts: []string{accountID}, Special: SpecialInbox}, thread)
		if err != nil {
			return Reminder{}, nil, err
		}
	}
	// The subject is read before the move: the moved rows are gone after.
	subject := s.threadSubject(ctx, accountID, thread)
	var moved []MoveResult
	var syncErr error
	if len(members) > 0 {
		if archive == "" {
			return Reminder{}, nil, ErrNoArchive
		}
		refs := make([]MessageRef, len(members))
		for i, m := range members {
			refs[i] = MessageRef{Folder: m.Folder, UID: m.UID}
		}
		if moved, err = s.MoveMessages(ctx, accountID, refs, archive); err != nil {
			// MoveMessages may have moved some before it failed; their
			// inbox rows come back rather than stay archived with no
			// reminder to bring them back.
			back := s.MoveBackMessages(context.WithoutCancel(ctx), accountID, moved)
			return Reminder{}, moved, errors.Join(err, back)
		}
		// The archive's rows are cached so that the reminder's list and
		// reading pane have the conversation before the user opens the
		// folder. A failure leaves the reminder: the next sync brings them.
		syncErr = s.SyncFolder(ctx, accountID, archive, 0)
	}
	r := Reminder{Account: accountID, Thread: thread, Subject: subject, RemindAt: at, CreatedAt: s.now()}
	if r, err = s.store.SaveReminder(ctx, r); err != nil {
		// The conversation goes back where it was rather than wait for a
		// reminder that was not saved.
		back := s.MoveBackMessages(context.WithoutCancel(ctx), accountID, moved)
		return Reminder{}, moved, errors.Join(err, syncErr, back)
	}
	return r, moved, syncErr
}

// threadSubject is the subject of a conversation's newest cached message,
// which a reminder keeps as it was set.
func (s *Service) threadSubject(ctx context.Context, accountID string, thread ThreadID) string {
	msgs, err := s.store.ConversationMessages(ctx, accountID, "", thread)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	latest := msgs[0]
	for _, m := range msgs[1:] {
		if m.Date.After(latest.Date) {
			latest = m
		}
	}
	return latest.Subject
}

// RemoveReminder takes a reminder off a conversation, leaving every
// message where it is.
func (s *Service) RemoveReminder(ctx context.Context, accountID string, thread ThreadID) error {
	return s.store.DeleteReminder(ctx, accountID, thread)
}

// RestoreReminder saves a reminder as it was, moving no mail: the undo of
// a new time on a conversation that had one puts the old time back.
func (s *Service) RestoreReminder(ctx context.Context, r Reminder) (Reminder, error) {
	return s.store.SaveReminder(ctx, r)
}

// DeliverReminders returns the conversations whose reminder is due to
// their inbox, as the app starts and as it runs: each message still in the
// reminder's Archive folder moves back to the Inbox, the inbox is synced
// so that the rows are cached, and the reminder goes. A conversation the
// user moved elsewhere meanwhile is not touched. It returns the reminders
// it delivered, and the errors of those it could not, which stay saved for
// another try.
func (s *Service) DeliverReminders(ctx context.Context) ([]Reminder, error) {
	due, err := s.store.DueReminders(ctx, s.now())
	if err != nil {
		return nil, err
	}
	var delivered []Reminder
	var errs []error
	for _, r := range due {
		ok, err := s.deliverReminder(ctx, r)
		if err != nil {
			errs = append(errs, fmt.Errorf("mail: reminder of conversation %d in %s: %w", r.Thread, r.Account, err))
		}
		if ok {
			delivered = append(delivered, r)
		}
	}
	return delivered, errors.Join(errs...)
}

// deliverReminder returns one reminder's conversation to the inbox, and
// takes the reminder off. Only the messages in the Archive folder come
// back: those the user moved elsewhere, or deleted, stay. It reports the
// reminder delivered once it is off, even when the cache could not be
// brought in step: the mail is back on the server either way.
func (s *Service) deliverReminder(ctx context.Context, r Reminder) (bool, error) {
	folders, err := s.store.Folders(ctx, r.Account)
	if err != nil {
		return false, err
	}
	archive, inbox := "", ""
	for _, f := range folders {
		switch f.SpecialUse {
		case SpecialArchive:
			archive = f.Name
		case SpecialInbox:
			inbox = f.Name
		}
	}
	var syncErr error
	if archive != "" && inbox != "" {
		// The archive's rows may be missing: the sync that set the
		// reminder can fail. Read the folder before deciding there is
		// nothing to bring back; a failure leaves the reminder for
		// another try.
		if err := s.SyncFolder(ctx, r.Account, archive, 0); err != nil {
			return false, err
		}
		msgs, err := s.ScopedConversation(ctx, r.Account, Scope{Accounts: []string{r.Account}, Folder: archive}, r.Thread)
		if err != nil {
			return false, err
		}
		if len(msgs) > 0 {
			refs := make([]MessageRef, len(msgs))
			for i, m := range msgs {
				refs[i] = MessageRef{Folder: m.Folder, UID: m.UID}
			}
			if _, err := s.MoveMessages(ctx, r.Account, refs, inbox); err != nil {
				return false, err
			}
			// The mail is back on the server whatever this says: the
			// reminder goes, and the next sync of the inbox brings the
			// rows.
			syncErr = s.SyncFolder(ctx, r.Account, inbox, 0)
		}
	}
	if err := s.store.DeleteReminder(ctx, r.Account, r.Thread); err != nil {
		return false, errors.Join(err, syncErr)
	}
	return true, syncErr
}

package app

import (
	"time"

	"shu/locales"
	"shu/mail"
)

// undoAction remembers the last change the list can offer to take back,
// and says what it was. A move's destination UID comes from the server's
// MOVE/COPY response; a change still waiting in the queue is taken back
// by dropping its action, never by making it first.
type undoAction struct {
	accountID string
	source    string
	dest      string
	destUID   mail.UID
	label     string
	// moved is a move of several messages, as a conversation's, by the
	// account they are in, which MoveBackMessages takes back; accountID,
	// source, dest and destUID are then not used.
	moved map[string][]mail.MoveResult
	// refs is a change of one flag (flag, on) or one label (labelID, on)
	// on the messages of each account, which UndoFlagChange and
	// UndoLabelChange take back.
	refs    map[string][]mail.MessageRef
	flag    mail.Flag
	labelID mail.LabelID
	on      bool
	// reminder is the reminder the move was set for, which the undo takes
	// off as it takes the mail back, and prevReminder the one it replaced,
	// which the undo puts back.
	reminder     *mail.Reminder
	prevReminder *mail.Reminder
	// block is the sender this action blocked, which the undo unblocks,
	// and prevBlock the block it replaced, which the undo puts back.
	block     *mail.BlockedSender
	prevBlock *mail.BlockedSender
}

// undoOfMove is the undo a move of one message offers. A move that waits
// in the queue is taken back by dropping its action, which MoveBackMessages
// does from the result alone; one the server made comes back from its
// destination UID.
func undoOfMove(accountID, source, dest, label string, r mail.MoveResult) *undoAction {
	if r.Pending != 0 {
		return &undoAction{label: label, moved: map[string][]mail.MoveResult{accountID: {r}}}
	}
	return &undoAction{accountID: accountID, source: source, dest: dest, destUID: r.DestUID, label: label}
}

// offerFlagUndo offers to take back a change of one flag on messages, in
// the list's one undo banner.
func (a *app) offerFlagUndo(msgs []mail.Summary, flag mail.Flag, on bool) {
	if len(msgs) == 0 {
		return
	}
	label := locales.MarkedUnread()
	switch {
	case flag == mail.FlagFlagged && on:
		label = locales.Starred()
	case flag == mail.FlagFlagged:
		label = locales.Unstarred()
	case on:
		label = locales.MarkedRead()
	}
	_, of := byAccount(msgs)
	refs := map[string][]mail.MessageRef{}
	for account, list := range of {
		refs[account] = refsOf(list)
	}
	a.setUndo(&undoAction{label: label, refs: refs, flag: flag, on: on})
}

// offerLabelUndo offers to take back a change of one label on messages.
func (a *app) offerLabelUndo(accountID string, refs []mail.MessageRef, l mail.Label, on bool) {
	if len(refs) == 0 {
		return
	}
	label := locales.LabelRemoved(l.Name)
	if on {
		label = locales.LabelAdded(l.Name)
	}
	a.setUndo(&undoAction{label: label, refs: map[string][]mail.MessageRef{accountID: refs}, labelID: l.ID, on: on})
}

// setUndo offers an undo, or clears it, and retires the offer after a
// while so a stale banner does not linger.
func (a *app) setUndo(u *undoAction) {
	if a.undoTimer != nil {
		a.undoTimer.Stop()
		a.undoTimer = nil
	}
	a.undo = u
	if u == nil || a.after == nil {
		return
	}
	a.undoTimer = a.after(10*time.Second, func() {
		a.post(func() {
			if a.undo == u {
				a.undo = nil
			}
		})
	})
}

package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// A message sent with a delay waits it out in the outbox (see
// mail/schedule.go). The mail window counts it down, with Undo, which
// takes it back to a compose window, while a job that lasts as long as
// the app sends it once it is due; what came of it is said as a send's
// result is. The messages an earlier run left waiting leave as the app
// starts.

// waitingSend is a message sent with a delay that has not left, as the
// mail window shows it.
type waitingSend struct {
	id        string
	accountID string
	subject   string
	due       time.Time
	// delay is the delay it was sent with, in seconds: the most the
	// countdown says, as the engine rounds the time it is due up to a
	// whole second.
	delay int
	// undoing says Undo is under way, and tooLate that it came after the
	// message had begun to leave. sent and total are how much of the
	// message has gone to the server, of how much, once it is on its way.
	undoing, tooLate bool
	sent, total      int64
}

// waitForSend shows a message the engine scheduled as waiting to leave.
func (a *app) waitForSend(p mail.PendingSend, delay int) {
	a.waiting = append(a.waiting, &waitingSend{
		id: p.ID, accountID: p.AccountID, subject: p.Envelope.Subject, due: p.DueAt, delay: delay,
	})
}

// sendWhenDue runs the job that sends a scheduled message once it is due.
// It lasts as long as the app, not the compose window, which has closed:
// the user sent the message, and only Undo takes it back. A job may call
// it.
func (a *app) sendWhenDue(p mail.PendingSend) {
	a.run(func() {
		ctx := mail.WithSendProgress(context.Background(), func(sent, total int64) {
			a.post(func() {
				if w := a.waitingFor(p.ID); w != nil {
					w.sent, w.total = sent, total
				}
			})
		})
		result, err := a.svc.SendScheduled(ctx, p.ID)
		a.post(func() { a.scheduledSent(p, result, err) })
	})
}

// waitingFor is the message waiting to leave with an outbox ID, or nil.
func (a *app) waitingFor(id string) *waitingSend {
	for _, w := range a.waiting {
		if w.id == id {
			return w
		}
	}
	return nil
}

// dropWaiting stops showing a message as waiting to leave.
func (a *app) dropWaiting(id string) {
	a.waiting = slices.DeleteFunc(slices.Clone(a.waiting), func(w *waitingSend) bool { return w.id == id })
}

// scheduledSent takes what became of a message sent with a delay, as a
// send's result is taken: a copy that could not be saved in Sent is
// said, and a message that failed stays among the unsent ones, to send
// again, and in Drafts. The compose window is gone, so the mail window
// says it, in a note that stays until it is dismissed.
func (a *app) scheduledSent(p mail.PendingSend, result mail.SendResult, err error) {
	a.dropWaiting(p.ID)
	switch {
	case errors.Is(err, mail.ErrUnsent), errors.Is(err, mail.ErrAlreadyLeft):
		// Taken back, or sent by another send, which says how that went.
	case err != nil:
		reason := sendErrorText(err)
		if a.signInRequired(p.AccountID, err) {
			reason = locales.ActionNeedsSignIn()
		}
		a.tellSending(locales.WaitingNotSent(subjectOf(p.Envelope.Subject), reason), true)
		a.loadPending(a.gen)
	default:
		if result.Warning != nil {
			a.tellSending(locales.SentCopyWarning(), true)
		}
		a.loadMessages(a.gen)
		a.loadPending(a.gen)
	}
}

// undoSend takes a message back before it leaves, and opens it again as
// it was written: its text, its recipients, the identity it goes from
// and its files.
func (a *app) undoSend(w *waitingSend) {
	if w.undoing || w.tooLate {
		return
	}
	w.undoing = true
	id := w.id
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		d, err := a.svc.Unsend(ctx, id)
		a.post(func() {
			w.undoing = false
			switch {
			case err == nil:
				a.dropWaiting(id)
				a.resumeDraft(d)
				a.reloadDrafts(d.AccountID)
			case errors.Is(err, mail.ErrAlreadyLeft):
				// The send that took it says how that ends.
				w.tooLate = true
				a.tellSending(locales.UndoTooLate(), false)
			case errors.Is(err, mail.ErrUnsent):
				// Taken back already.
				a.dropWaiting(id)
			default:
				a.tellSending(locales.UndoSendFailed(err.Error()), true)
			}
		})
	})
}

// tellSending puts news of sending in the mail window's note, which stays
// until it is dismissed: news the window did not see happen, as a
// message's failure once its compose window is gone.
func (a *app) tellSending(text string, warn bool) {
	a.sendNote, a.sendNoteWarn = text, warn
}

// subjectOf is a subject as the window names a message by it.
func subjectOf(subject string) string {
	if strings.TrimSpace(subject) == "" {
		return locales.NoSubject()
	}
	return subject
}

// waitingNotice shows the messages sent with a delay that have not left:
// how long until each does, with Undo, then that it is on its way.
func (a *app) waitingNotice(c *ui.Context, t *ui.Theme) {
	for _, w := range a.waiting {
		left := w.due.Sub(c.Now())
		counting := left > 0 && !w.tooLate
		ui.Row(c.Key("waiting/"+w.id)).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
			Background(t.Accent.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
			Transition(bannerMotion).Children(func() {
			ui.Icon(c, sentIcon).FontSize(14).TextColor(t.Accent)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				text := locales.Sending()
				switch {
				case counting:
					n := min(int((left+time.Second-1)/time.Second), max(w.delay, 1))
					text = locales.SendingIn(n)
					// The next frame is the one whose number is less.
					c.After(left - time.Duration(n-1)*time.Second)
				case w.total > 0 && w.sent >= w.total-1:
					text = locales.SendFinishing()
				case w.total > 0:
					text = locales.SendProgress(formatSize(w.sent), formatSize(w.total))
				}
				ui.Text(c, text).FontSize(12).Bold().SingleLine()
				ui.Text(c, subjectOf(w.subject)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			if counting {
				ui.Button(c, locales.Undo()).Label(locales.UndoSendOf(subjectOf(w.subject))).
					Disabled(w.undoing).OnClick(func() { a.undoSend(w) })
			} else {
				ui.Spinner(c).Size(14, 14)
			}
		})
	}
}

// sendNoteBanner shows the note of sending.
func (a *app) sendNoteBanner(c *ui.Context, t *ui.Theme) {
	icon, color := sentIcon, t.Accent
	if a.sendNoteWarn {
		icon, color = alertIcon, t.Warning
	}
	ui.Row(c.Key("banner/send-note")).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(color.Alpha(0.10)).Transition(bannerMotion).Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(color)
		// A status, so that a screen reader says it once as it appears.
		ui.Text(c, a.sendNote).FontSize(12).TextColor(t.Text).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
		a.iconButton(c, closeIcon, locales.DismissSendNote(), locales.DismissSendNote()).
			OnClick(func() { a.tellSending("", false) })
	})
}

// deliverWaiting sends, once as the app starts, the messages an earlier
// run left waiting out their delay: the user sent them. The window says
// what came of them.
func (a *app) deliverWaiting() {
	a.run(func() {
		results, err := a.svc.DeliverScheduled(context.Background())
		a.post(func() { a.deliveredWaiting(results, err) })
	})
}

// deliveredWaiting says what came of the messages sent as the app
// started. One that failed stays among the unsent messages.
func (a *app) deliveredWaiting(results []mail.ScheduledResult, err error) {
	sent, failed, warned := 0, 0, false
	for _, r := range results {
		if r.Err != nil {
			failed++
			a.signInRequired(r.Outbox.AccountID, r.Err)
			continue
		}
		sent++
		warned = warned || r.Result.Warning != nil
	}
	var lines []string
	if sent > 0 {
		lines = append(lines, locales.SentAtStart(sent))
	}
	if failed > 0 {
		lines = append(lines, locales.NotSentAtStart(failed))
	}
	if err != nil {
		lines = append(lines, locales.DeliverAtStartFailed(err.Error()))
	}
	if warned {
		lines = append(lines, locales.SentCopyWarning())
	}
	if len(lines) > 0 {
		a.tellSending(strings.Join(lines, "\n"), failed > 0 || err != nil || warned)
	}
	if len(results) > 0 {
		a.loadMessages(a.gen)
		a.loadPending(a.gen)
	}
}

// composeChoices are where the compose settings' controls stand: each
// setting's place among its choices.
type composeChoices struct {
	delay, reply, quote, position int
}

// choicesOf places compose settings among their choices.
func choicesOf(set mail.ComposeSettings) composeChoices {
	ch := composeChoices{delay: slices.Index(mail.SendDelays, set.SendDelay)}
	if ch.delay < 0 {
		ch.delay = slices.Index(mail.SendDelays, mail.DefaultSendDelay)
	}
	if set.Reply == mail.ReplyAll {
		ch.reply = 1
	}
	if set.Quote == mail.QuoteHeader {
		ch.quote = 1
	}
	if set.Position == mail.ReplyBelow {
		ch.position = 1
	}
	return ch
}

// settings reads the controls back into compose settings.
func (ch composeChoices) settings(forwardAttachments bool) mail.ComposeSettings {
	set := mail.ComposeSettings{
		SendDelay: mail.SendDelays[min(max(ch.delay, 0), len(mail.SendDelays)-1)],
		Reply:     mail.ReplySender, Quote: mail.QuotePrefix, Position: mail.ReplyAbove,
		ForwardAttachments: forwardAttachments,
	}
	if ch.reply == 1 {
		set.Reply = mail.ReplyAll
	}
	if ch.quote == 1 {
		set.Quote = mail.QuoteHeader
	}
	if ch.position == 1 {
		set.Position = mail.ReplyBelow
	}
	return set
}

// setComposeSettings takes the stored compose settings.
func (a *app) setComposeSettings(set mail.ComposeSettings) {
	a.composeSet = set
	a.composeChoice = choicesOf(set)
}

// saveComposeSettings writes the compose settings as the controls show
// them. Every control calls it; the saves take turns, and one that a
// later save overtook does not write, so the last choice made is the one
// stored.
func (a *app) saveComposeSettings() {
	set := a.composeChoice.settings(a.composeSet.ForwardAttachments)
	a.composeSet = set
	seq := a.composeSaveSeq.Add(1)
	a.run(func() {
		a.composeSaveMu.Lock()
		defer a.composeSaveMu.Unlock()
		if a.composeSaveSeq.Load() != seq {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SaveComposeSettings(ctx, set)
		a.post(func() { a.composeSetFailed = err != nil })
	})
}

// composeSettingsView draws the settings of writing and sending: the
// delay that leaves time for Undo, the default reply, how a reply quotes
// and where it is written, and whether a forward carries the original's
// attachments. The format is plain text, the only one, which it says.
func (a *app) composeSettingsView(c *ui.Context, t *ui.Theme) {
	ch := &a.composeChoice
	a.section(c, locales.ComposeSection(), func() {
		delays := make([]string, 0, len(mail.SendDelays))
		for _, seconds := range mail.SendDelays {
			if seconds == 0 {
				delays = append(delays, locales.SendDelayOff())
			} else {
				delays = append(delays, locales.SendDelaySeconds(seconds))
			}
		}
		a.composeChoiceRow(c, t, locales.SendDelay(), &ch.delay, delays, locales.SendDelayDetail())
		a.composeChoiceRow(c, t, locales.ReplyDefault(), &ch.reply,
			[]string{locales.ReplyDefaultSender(), locales.ReplyDefaultAll()}, locales.ReplyDefaultDetail())
		quote := locales.QuoteStylePrefixDetail()
		if ch.quote == 1 {
			quote = locales.QuoteStyleHeaderDetail()
		}
		a.composeChoiceRow(c, t, locales.QuoteStyle(), &ch.quote,
			[]string{locales.QuoteStylePrefix(), locales.QuoteStyleHeader()}, quote)
		a.composeChoiceRow(c, t, locales.ReplyPosition(), &ch.position,
			[]string{locales.ReplyAboveQuote(), locales.ReplyBelowQuote()}, "")
		ui.Column(c).Padding(2, 0, 0, 0).Children(func() {
			a.switchLine(c, t, locales.ForwardAttachments(), locales.ForwardAttachmentsDetail(),
				&a.composeSet.ForwardAttachments, false, a.saveComposeSettings)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, composeIcon).FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, locales.ComposeFormat()).FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0)
		})
		if a.composeSetFailed {
			a.settingsWarning(c, t, locales.SettingsSaveFailed())
		}
	})
}

// composeChoiceRow is a compose setting's choices, under its name, with
// a line about it.
func (a *app) composeChoiceRow(c *ui.Context, t *ui.Theme, title string, at *int, labels []string, detail string) {
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, title).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Segmented(c, at, labels...).Label(title).Width(320).OnChange(a.saveComposeSettings)
		if detail != "" {
			ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted)
		}
	})
}

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// remindersFolder is the sidebar's Reminders entry: the conversations the
// user asked to come back to, in every folder but Junk, Trash and Drafts,
// soonest due first (mail.Reminder). Setting a reminder archives the
// conversation and saves it here; the engine brings it back to the inbox
// at its time.
const remindersFolder = "reminders"

// reminderTick is how often the window asks the engine for due reminders
// while it runs. A reminder saved for later is due within a minute of its
// time.
const reminderTick = time.Minute

// reminderDeliveryTimeout is how long one check of due reminders may take:
// each one moves mail and syncs a folder, and several can be due after a
// run offline.
const reminderDeliveryTimeout = 5 * time.Minute

// reminderLayout is how a custom reminder time is typed: the date, then
// the time of day, in the app's own clock.
const reminderLayout = "2006-01-02 15:04"

// reminderForm is the dialog of a custom reminder time: the conversation
// it is set on, the text typed and the reason it was refused. now is when
// the dialog opened, before which a time makes no sense.
type reminderForm struct {
	open    bool
	account string
	thread  mail.ThreadID
	when    string
	loc     *time.Location
	err     string
}

// reminderPreset is one of the times "Remind me…" offers.
type reminderPreset struct {
	label string
	at    time.Time
}

// reminderPresets are the times the menu offers from now: later today,
// tomorrow, this weekend and next week.
func reminderPresets(now time.Time) []reminderPreset {
	return []reminderPreset{
		{locales.RemindLaterToday(), laterToday(now)},
		{locales.RemindTomorrow(), dayAt(now, 1, 9)},
		{locales.RemindThisWeekend(), thisWeekend(now)},
		{locales.RemindNextWeek(), nextWeek(now)},
	}
}

// dayAt is a day at a time of day, in now's clock.
func dayAt(now time.Time, days, hour int) time.Time {
	y, m, d := now.AddDate(0, 0, days).Date()
	return time.Date(y, m, d, hour, 0, 0, 0, now.Location())
}

// laterToday is this evening, or tomorrow morning when the evening is
// past.
func laterToday(now time.Time) time.Time {
	evening := dayAt(now, 0, 18)
	if evening.After(now) {
		return evening
	}
	return dayAt(now, 1, 9)
}

// thisWeekend is the coming Saturday morning, or Sunday's when Saturday's
// is past, or next Saturday's when the weekend is over.
func thisWeekend(now time.Time) time.Time {
	if now.Weekday() == time.Sunday {
		if morning := dayAt(now, 0, 9); morning.After(now) {
			return morning
		}
		return dayAt(now, 6, 9)
	}
	saturday := dayAt(now, (int(time.Saturday)-int(now.Weekday())+7)%7, 9)
	if saturday.After(now) {
		return saturday
	}
	return saturday.AddDate(0, 0, 1)
}

// nextWeek is the coming Monday morning, a week after when today is
// Monday or it is past.
func nextWeek(now time.Time) time.Time {
	days := (int(time.Monday) - int(now.Weekday()) + 7) % 7
	if days == 0 {
		days = 7
	}
	return dayAt(now, days, 9)
}

// reminderFor is the active reminder of a conversation, nil when it has
// none.
func (a *app) reminderFor(account string, thread mail.ThreadID) *mail.Reminder {
	for i := range a.reminders {
		if a.reminders[i].Account == account && a.reminders[i].Thread == thread {
			return &a.reminders[i]
		}
	}
	return nil
}

// rememberReminder puts a saved reminder in the list the window has,
// replacing the one of its conversation.
func (a *app) rememberReminder(r mail.Reminder) {
	if old := a.reminderFor(r.Account, r.Thread); old != nil {
		*old = r
	} else {
		a.reminders = append(a.reminders, r)
	}
}

// forgetReminder takes a conversation's reminder off the list the window
// has.
func (a *app) forgetReminder(account string, thread mail.ThreadID) {
	for i := range a.reminders {
		if a.reminders[i].Account == account && a.reminders[i].Thread == thread {
			a.reminders = append(a.reminders[:i], a.reminders[i+1:]...)
			return
		}
	}
}

// loadReminders reads the active account's reminders for the sidebar and
// the list.
func (a *app) loadReminders(g uint64) {
	accountID := a.active
	if accountID == "" {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		list, err := a.svc.Reminders(ctx, accountID)
		a.post(func() {
			if a.stale(g) || a.active != accountID || err != nil {
				return
			}
			a.reminders = list
		})
	})
}

// deliverReminders asks the engine to bring back what is due, and shows
// what came: the list, the folders' counts, and a notification where the
// settings allow one.
func (a *app) deliverReminders() {
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), reminderDeliveryTimeout)
		defer cancel()
		delivered, err := a.svc.DeliverReminders(ctx)
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				a.err = failureText(err)
			}
			if len(delivered) == 0 {
				return
			}
			a.notifyReminders(delivered)
			a.loadReminders(a.gen)
			a.reloadFolders()
		})
	})
}

// startReminderLoop has the due check run while the app does.
func (a *app) startReminderLoop() {
	if a.reminderLoop || a.after == nil || a.closed {
		return
	}
	a.reminderLoop = true
	a.after(reminderTick, func() {
		a.post(func() {
			if a.closed {
				return
			}
			a.deliverReminders()
			a.reminderLoop = false
			a.startReminderLoop()
		})
	})
}

// notifyReminders notifies of the mail a reminder brought back, as new
// mail is notified under the same settings: none when they are off, when
// the account is muted or when the user is looking at the inbox. What the
// notification says of the mail follows the details setting too.
func (a *app) notifyReminders(delivered []mail.Reminder) {
	if a.notify == nil || !a.mailSet.Notify {
		return
	}
	var notes []note
	for _, r := range delivered {
		if a.mailSet.Muted[r.Account] {
			continue
		}
		acc := a.account(r.Account)
		inbox := a.specialFolder(r.Account, mail.SpecialInbox)
		if acc == nil || inbox == "" || a.looksAt(r.Account, inbox) {
			continue
		}
		target := noteTarget{account: r.Account, folder: inbox}
		for _, f := range a.foldersFor(r.Account) {
			if f.Name == inbox {
				target.validity = f.UIDValidity
			}
		}
		body := locales.ReminderDue()
		if a.mailSet.NotifyDetails {
			if body = strings.TrimSpace(r.Subject); body == "" {
				body = locales.NoSubject()
			}
		}
		notes = append(notes, note{ID: target.id(), Title: acc.Label(), Body: body, Group: acc.ID})
	}
	a.showNotes(notes)
}

// reminderWhen writes a reminder time for a menu or a banner: the time
// today, the date and time otherwise.
func reminderWhen(now, at time.Time) string {
	at = at.In(now.Location())
	y, m, d := now.Date()
	ay, am, ad := at.Date()
	if y == ay && m == am && d == ad {
		return i18n.FormatDate(i18n.Locale(), at, i18n.DateOptions{Time: i18n.ShortStyle})
	}
	return i18n.FormatDate(i18n.Locale(), at, i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
}

// remindMenuItems adds to a menu the times a conversation can be reminded
// for, the dialog of another time, and the way to take a reminder off.
func (a *app) remindMenuItems(m *ui.Menu, now time.Time, conv mail.Conversation) {
	for _, p := range reminderPresets(now) {
		item := m.Item(p.label)
		item.Subtitle(reminderWhen(now, p.at))
		if item.Chosen() {
			a.setReminder(conv.Account, conv.Thread, p.at)
		}
	}
	if m.Item(locales.RemindCustomEllipsis()).Chosen() {
		a.openReminderForm(conv.Account, conv.Thread, now)
	}
	if a.reminderFor(conv.Account, conv.Thread) != nil {
		m.Separator()
		if m.Item(locales.RemoveReminder()).Chosen() {
			a.removeReminder(conv.Account, conv.Thread)
		}
	}
}

// setReminder archives a conversation and saves a reminder for at, and
// offers the undo that puts both back. One reminder of a conversation
// replaces another.
func (a *app) setReminder(account string, thread mail.ThreadID, at time.Time) {
	g := a.gen
	var prev *mail.Reminder
	if old := a.reminderFor(account, thread); old != nil {
		kept := *old
		prev = &kept
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		r, moved, err := a.svc.SetReminder(ctx, account, thread, at)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if r.ID == 0 {
				switch {
				case errors.Is(err, mail.ErrNoArchive):
					a.err = locales.ReminderNoArchive()
				case err != nil:
					a.err = failureText(err)
				}
				return
			}
			a.rememberReminder(r)
			switch {
			case err != nil:
				// The reminder was saved; only caching the archive failed,
				// which a sync will mend.
				a.err = locales.ReminderSyncFailed(err.Error())
			default:
				a.err = ""
			}
			a.setUndo(&undoAction{
				label:        locales.ReminderSet(reminderWhen(time.Now(), r.RemindAt)),
				moved:        map[string][]mail.MoveResult{account: moved},
				reminder:     &r,
				prevReminder: prev,
			})
			a.loadReminders(g)
			a.reloadFolders()
		})
	})
}

// removeReminder takes a reminder off a conversation and leaves the mail
// where it is.
func (a *app) removeReminder(account string, thread mail.ThreadID) {
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.RemoveReminder(ctx, account, thread)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = failureText(err)
				return
			}
			a.forgetReminder(account, thread)
			a.loadReminders(g)
			a.reloadFolders()
		})
	})
}

// openReminderForm opens the dialog of a custom time, with tomorrow's
// beginning in it.
func (a *app) openReminderForm(account string, thread mail.ThreadID, now time.Time) {
	a.remindForm = reminderForm{
		open: true, account: account, thread: thread,
		when: dayAt(now, 1, 9).Format(reminderLayout), loc: now.Location(),
	}
}

// submitReminderForm sets the reminder the dialog holds, when its time is
// one that can be read and is still ahead at now, the time of the frame
// that submits it: a dialog left open for a day refuses the time it
// opened with.
func (a *app) submitReminderForm(now time.Time) {
	f := &a.remindForm
	if !f.open {
		return
	}
	at, err := time.ParseInLocation(reminderLayout, strings.TrimSpace(f.when), f.loc)
	if err != nil {
		f.err = locales.ReminderBadTime()
		return
	}
	if !at.After(now) {
		f.err = locales.ReminderTimePast()
		return
	}
	f.open = false
	a.setReminder(f.account, f.thread, at)
}

// reminderDialog draws the dialog of a custom reminder time.
func (a *app) reminderDialog(c *ui.Context) {
	f := &a.remindForm
	t := c.Theme()
	if !f.open {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(14).Children(func() {
			ui.Text(c, locales.RemindCustomTitle()).FontSize(17).Bold()
			var when ui.Element
			ui.Column(c).Gap(4).Children(func() {
				caption := ui.Text(c, locales.RemindCustomLabel()).FontSize(11).Bold().TextColor(t.TextMuted)
				when = ui.TextInput(c, &f.when).LabelledBy(caption).
					FillWidth().AutoFocus().Invalid(f.err != "").OnSubmit(func() { a.submitReminderForm(c.Now()) })
			})
			if f.err != "" {
				a.formError(c, when, f.err)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				ui.Button(c, locales.Cancel()).Label(locales.RemindCustomCancelLabel()).OnClick(func() { f.open = false })
				ui.PrimaryButton(c, locales.RemindCustomSave()).
					Disabled(strings.TrimSpace(f.when) == "").OnClick(func() { a.submitReminderForm(c.Now()) })
			})
		})
	})
}

// formError is the line under a form's field that says what was wrong
// with it, as folderDialog writes it.
func (a *app) formError(c *ui.Context, field ui.Element, text string) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		field.ErrorMessage(ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus))
	})
}

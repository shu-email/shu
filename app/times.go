package app

import (
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// rowTime is the timestamp at the end of a list row: the message's; in
// the reminders' list the time the reminder is due, and in the
// follow-ups' one how long the conversation has waited for a reply.
func (a *app) rowTime(c *ui.Context, m mail.Summary) string {
	if a.folder == remindersFolder {
		if r := a.reminderFor(m.Account, m.Thread); r != nil {
			return a.messageTime(c, r.RemindAt.In(c.Now().Location()))
		}
	}
	if a.folder == followUpFolder {
		if days, ok := mail.FollowUpAge(c.Now(), m.Date); ok {
			return i18n.FormatRelativeTime(c.Locale(), -float64(days), i18n.RelativeDay,
				i18n.RelativeTimeOptions{Style: i18n.RelativeShort})
		}
	}
	return a.messageTime(c, m.Date)
}

// messageTime writes a list timestamp the way mail clients do: a time
// today, "yesterday", a weekday this week, a date this year.
func (a *app) messageTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := c.Now()
	y, m, d := now.Date()
	ty, tm, td := t.Date()
	if y == ty && m == tm && d == td {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Time: i18n.ShortStyle})
	}
	yesterday := now.AddDate(0, 0, -1)
	yy, ym, yd := yesterday.Date()
	if ty == yy && tm == ym && td == yd {
		return i18n.FormatRelativeTime(c.Locale(), -1, i18n.RelativeDay, i18n.RelativeTimeOptions{Auto: true})
	}
	if now.Sub(t) < 7*24*time.Hour && now.Sub(t) > 0 {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "EEE"})
	}
	if y == ty {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "MMMd"})
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "yMMMd"})
}

// longTime is the reading pane's full timestamp.
func (a *app) longTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
}

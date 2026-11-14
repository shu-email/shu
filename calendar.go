package main

import (
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Meeting invitations: the card a message with a calendar part shows, its
// RSVP, and opening the event in the system calendar. An answer is sent
// only when the user clicks one, as an iMIP reply.

// inviteResponse is the user's own answer to an invitation.
func (a *app) inviteResponse(inv *mail.CalendarInvite, m mail.Summary) string {
	if s := a.inviteResponses[inv.UID]; s != "" {
		return s
	}
	acc := a.account(m.Account)
	if acc == nil {
		return ""
	}
	for _, att := range inv.Attendees {
		if strings.EqualFold(att.Email, acc.Email) {
			return att.Status
		}
	}
	for _, id := range a.aliases[m.Account] {
		for _, att := range inv.Attendees {
			if strings.EqualFold(att.Email, id.Email) {
				return att.Status
			}
		}
	}
	return ""
}

func responseLabel(status string) string {
	switch strings.ToUpper(status) {
	case "ACCEPTED":
		return locales.InviteAccepted()
	case "TENTATIVE":
		return locales.InviteMaybe()
	case "DECLINED":
		return locales.InviteDeclined()
	}
	return ""
}

// calendarCard shows the invitation a message carries, or says that it
// cannot be read.
func (a *app) calendarCard(c *ui.Context, t *ui.Theme, m mail.Summary, msg *mail.Message) {
	if msg == nil || (msg.Calendar == nil && msg.CalendarRaw == "") {
		return
	}
	if msg.Calendar == nil {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(10, 12).Radius(8).
			Background(t.Warning.Alpha(0.10)).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, locales.InviteUnreadable()).FontSize(12).Grow(1).MinWidth(0).MaxLines(3)
		})
		return
	}
	inv := msg.Calendar
	ui.Column(c).Gap(8).Padding(12, 14).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, clockIcon).FontSize(15).TextColor(t.Accent)
			ui.Text(c, inv.Summary).FontSize(14).Bold().Grow(1).MinWidth(0).MaxLines(2)
		})
		ui.Text(c, inviteWhen(c.Locale(), inv)).FontSize(12).TextColor(t.Text)
		if inv.Location != "" {
			ui.Text(c, locales.InviteLocation(inv.Location)).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		}
		if inv.Organizer.Email != "" {
			name := inv.Organizer.Name
			if name == "" {
				name = inv.Organizer.Email
			}
			ui.Text(c, locales.InviteOrganizer(name)).FontSize(11).TextColor(t.TextMuted)
		}
		if len(inv.Attendees) > 0 {
			ui.Text(c, locales.InviteAttendees(len(inv.Attendees))).FontSize(11).TextColor(t.TextMuted)
		}
		if inv.Canceled {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(12).TextColor(t.Danger)
				ui.Text(c, locales.InviteCanceled()).FontSize(11).Bold().TextColor(t.Danger)
			})
		} else if inv.Sequence > 0 {
			ui.Text(c, locales.InviteUpdated()).FontSize(11).TextColor(t.TextMuted)
		}
		if response := a.inviteResponse(inv, m); response != "" && responseLabel(response) != "" {
			ui.Text(c, locales.InviteYourResponse(responseLabel(response))).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Wrap().Gap(8).Children(func() {
			if !inv.Canceled && inv.Organizer.Email != "" {
				for _, choice := range []struct{ label, status string }{
					{locales.InviteAccept(), "ACCEPTED"},
					{locales.InviteTentative(), "TENTATIVE"},
					{locales.InviteDecline(), "DECLINED"},
				} {
					choice := choice
					ui.Button(c, choice.label).Disabled(a.inviteBusy).OnClick(func() {
						a.replyInvite(m, inv, choice.status)
					})
				}
			}
			ui.Button(c, locales.InviteAddToCalendar()).OnClick(func() { a.addInviteToCalendar(inv) })
		})
		if a.inviteNote != "" {
			ui.Text(c, a.inviteNote).FontSize(11).TextColor(t.TextMuted).MaxLines(3).Role(ui.RoleStatus)
		}
	})
}

// inviteWhen says the time range in the user's own time zone and in the
// user's locale: an all-day event is a date of its own, which no time
// zone moves, and a floating one is the reader's own clock, with no zone
// to move it to or name.
func inviteWhen(locale string, inv *mail.CalendarInvite) string {
	if inv.Start.IsZero() {
		return ""
	}
	if inv.AllDay {
		return i18n.FormatDate(locale, inv.Start, i18n.DateOptions{Skeleton: "yMMMdE"})
	}
	skeleton := "yMMMdEjmz"
	start, end := inv.Start.Local(), inv.End.Local()
	if inv.Floating {
		skeleton = "yMMMdEjm"
		start, end = inv.Start, inv.End
	}
	if inv.End.IsZero() {
		// DTEND is optional, and a missing one leaves the start alone
		// rather than a year-one end.
		return i18n.FormatDate(locale, start, i18n.DateOptions{Skeleton: skeleton})
	}
	return i18n.FormatDateRange(locale, start, end, i18n.DateOptions{Skeleton: skeleton})
}

// replyInvite sends an iMIP answer to the organizer. Nothing is sent
// without this call, which only a click makes.
func (a *app) replyInvite(m mail.Summary, inv *mail.CalendarInvite, status string) {
	if a.inviteBusy || inv.Organizer.Email == "" {
		if inv.Organizer.Email == "" {
			a.inviteNote = locales.InviteNoReply()
		}
		return
	}
	acc := a.account(m.Account)
	if acc == nil {
		return
	}
	from := mail.Address{Name: acc.Name, Email: acc.Email}
	ics := mail.CalendarReply(inv, from, status)
	out := mail.Outgoing{
		To: []mail.Address{inv.Organizer}, Subject: "Re: " + m.Subject,
		Text: mail.CalendarResponseText(status), InReplyTo: m.MessageID, Calendar: ics,
	}
	a.inviteBusy, a.inviteNote = true, ""
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, err := a.svc.Send(ctx, m.Account, out)
		a.post(func() {
			a.inviteBusy = false
			if a.stale(g) {
				return
			}
			if err != nil {
				a.inviteNote = locales.InviteReplyFailed(failureText(err))
				return
			}
			if a.inviteResponses == nil {
				a.inviteResponses = map[string]string{}
			}
			a.inviteResponses[inv.UID] = status
			a.inviteNote = locales.InviteReplySent(inv.Organizer.String())
		})
	})
}

// addInviteToCalendar opens the event in the system calendar by writing
// its calendar part to a temporary file and handing it to the system.
func (a *app) addInviteToCalendar(inv *mail.CalendarInvite) {
	if inv.Raw == "" {
		a.inviteNote = locales.InviteAddFailed(locales.InviteUnreadable())
		return
	}
	path, err := a.svc.TempFile(".ics", []byte(inv.Raw))
	if err != nil {
		a.inviteNote = locales.InviteAddFailed(err.Error())
		return
	}
	if a.win == nil {
		a.inviteNote = locales.InviteAddOpened()
		return
	}
	if err := mygo.Shell.OpenExternal("file://" + path); err != nil {
		_ = os.Remove(path)
		a.inviteNote = locales.InviteAddFailed(err.Error())
		return
	}
	a.inviteNote = locales.InviteAddOpened()
}

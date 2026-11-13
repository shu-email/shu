package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

func testInvite() *mail.CalendarInvite {
	return &mail.CalendarInvite{
		Method: "REQUEST", UID: "meeting-1@example.com", Sequence: 0,
		Summary: "Quarterly planning", Start: time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 3, 5, 11, 0, 0, 0, time.UTC),
		StartRaw: "DTSTART:20260305T100000Z", EndRaw: "DTEND:20260305T110000Z", Location: "Room 4",
		Organizer: mail.Address{Name: "Maya Chen", Email: "maya@example.com"},
		Attendees: []mail.CalendarAttendee{
			{Address: mail.Address{Name: "Ann", Email: "me@example.com"}, Status: "NEEDS-ACTION"},
			{Address: mail.Address{Name: "Bob", Email: "bob@example.com"}, Status: "ACCEPTED"},
		},
		Raw: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:meeting-1@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}
}

func loadCalendarFirst(t *testing.T, fs *fakeService, a *app, inv *mail.CalendarInvite) mail.Summary {
	t.Helper()
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Join us.", BodyCached: true, Calendar: inv,
	}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	return m
}

func TestCalendarCardShowsTheInvitation(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadCalendarFirst(t, fs, a, testInvite())
	tt := ui.NewTester(a.view, 1220, 800)
	for _, text := range []string{"Quarterly planning", locales.InviteLocation("Room 4"), locales.InviteOrganizer("Maya Chen"),
		locales.InviteAttendees(2), locales.InviteAccept(), locales.InviteTentative(), locales.InviteDecline(), locales.InviteAddToCalendar()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
}

func TestCalendarRSVPSendsAnAnswer(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadCalendarFirst(t, fs, a, testInvite())
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.InviteAccept())
	if len(fs.sent) != 1 {
		t.Fatalf("sent = %+v", fs.sent)
	}
	out := fs.sent[0]
	if out.To[0].Email != "maya@example.com" || !strings.Contains(out.Calendar, "METHOD:REPLY") ||
		!strings.Contains(out.Calendar, "PARTSTAT=ACCEPTED:mailto:me@example.com") {
		t.Errorf("outgoing = %+v", out)
	}
	if a.inviteResponses["meeting-1@example.com"] != "ACCEPTED" {
		t.Errorf("responses = %+v", a.inviteResponses)
	}
	if !tt.HasText(locales.InviteYourResponse(locales.InviteAccepted())) {
		t.Errorf("the card does not show the answer: %q", tt.Texts())
	}
}

func TestCalendarCanceledAndUnreadable(t *testing.T) {
	a, fs := sampleMessageApp(t)
	inv := testInvite()
	inv.Canceled = true
	inv.Sequence = 3
	loadCalendarFirst(t, fs, a, inv)
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.InviteCanceled()) {
		t.Errorf("missing cancellation: %q", tt.Texts())
	}
	if tt.HasText(locales.InviteAccept()) {
		t.Error("a canceled meeting offers an RSVP")
	}

	a, fs = sampleMessageApp(t)
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Join us.", BodyCached: true, CalendarRaw: "BEGIN:VCALENDAR\r\nEND:VCALENDAR",
	}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	tt = ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.InviteUnreadable()) {
		t.Errorf("an unreadable invitation says nothing: %q", tt.Texts())
	}
}

func TestCalendarRSVPFailureIsSaid(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadCalendarFirst(t, fs, a, testInvite())
	fs.sendErr = errors.New("the server refused the message")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.InviteDecline())
	if !strings.Contains(a.inviteNote, "refused") && a.inviteNote == "" {
		t.Error("the failure was not said")
	}
}

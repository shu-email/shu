package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
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

// The card writes the time in the user's locale and time zone, not in
// one language's layout: the day once, the times as a range of the
// locale's own, and the zone where the locale puts it.
func TestInviteWhenWritesTheUserLocale(t *testing.T) {
	inv := testInvite()
	timed := i18n.DateOptions{Skeleton: "yMMMdEjmz"}
	start, end := inv.Start.Local(), inv.End.Local()
	for _, locale := range []string{"en", "zh-Hans"} {
		if got, want := inviteWhen(locale, inv), i18n.FormatDateRange(locale, start, end, timed); got != want {
			t.Errorf("%s: %q, want %q", locale, got, want)
		}

		// A range over two days names both.
		multi := *inv
		multi.End = inv.End.Add(24 * time.Hour)
		if got, want := inviteWhen(locale, &multi), i18n.FormatDateRange(locale, start, multi.End.Local(), timed); got != want {
			t.Errorf("%s over two days: %q, want %q", locale, got, want)
		}

		// One with no end names its start alone, not a year-one end.
		noEnd := *inv
		noEnd.End = time.Time{}
		if got, want := inviteWhen(locale, &noEnd), i18n.FormatDate(locale, start, timed); got != want {
			t.Errorf("%s with no end: %q, want %q", locale, got, want)
		}
	}
}

// An all-day event is the date the organizer wrote, which no zone moves,
// and a floating time is the reader's own clock, with no zone to name.
func TestInviteWhenKeepsTheEventsOwnTime(t *testing.T) {
	inv := testInvite()
	inv.AllDay = true
	// The parser reads an all-day start as midnight in a zone of its own;
	// a card that moved it to the machine's zone would show the day before.
	inv.Start = time.Date(2026, 3, 5, 0, 0, 0, 0, time.FixedZone("", 11*3600))
	for _, locale := range []string{"en", "zh-Hans"} {
		want := i18n.FormatDate(locale, inv.Start, i18n.DateOptions{Skeleton: "yMMMdE"})
		if got := inviteWhen(locale, inv); got != want {
			t.Errorf("%s all-day = %q, want %q", locale, got, want)
		}
		if got := inviteWhen(locale, inv); strings.Contains(got, ":") {
			t.Errorf("%s all-day shows a time: %q", locale, got)
		}
	}

	floating := *testInvite()
	floating.Floating = true
	floating.Start = time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	floating.End = floating.Start.Add(time.Hour)
	for _, locale := range []string{"en", "zh-Hans"} {
		want := i18n.FormatDateRange(locale, floating.Start, floating.End, i18n.DateOptions{Skeleton: "yMMMdEjm"})
		got := inviteWhen(locale, &floating)
		if got != want {
			t.Errorf("%s floating = %q, want %q", locale, got, want)
		}
		for _, zone := range []string{"GMT", "UTC"} {
			if strings.Contains(got, zone) {
				t.Errorf("%s floating names a zone: %q", locale, got)
			}
		}
	}
}

// The card takes its language from the window, as the rest of it does.
func TestCalendarCardUsesTheWindowLocale(t *testing.T) {
	a, fs := sampleMessageApp(t)
	inv := testInvite()
	loadCalendarFirst(t, fs, a, inv)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	want := i18n.FormatDateRange("zh-Hans", inv.Start.Local(), inv.End.Local(), i18n.DateOptions{Skeleton: "yMMMdEjmz"})
	if !tt.HasText(want) {
		t.Errorf("the card lacks %q: %q", want, tt.Texts())
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

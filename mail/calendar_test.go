package mail

import (
	"strings"
	"testing"
	"time"
)

const testInvite = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\nUID:meeting-1@example.com\r\nSEQUENCE:2\r\n" +
	"SUMMARY:Quarterly planning\\, with lunch\r\n" +
	"DTSTART;TZID=Europe/Berlin:20260305T100000\r\nDTEND;TZID=Europe/Berlin:20260305T110000\r\n" +
	"LOCATION:Room 4\r\nORGANIZER;CN=Maya Chen:mailto:maya@example.com\r\n" +
	"ATTENDEE;CN=Ann;PARTSTAT=NEEDS-ACTION:mailto:ann@example.com\r\n" +
	"ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED:mailto:bob@example.com\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseCalendarInvite(t *testing.T) {
	inv, err := ParseCalendar(testInvite)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Method != "REQUEST" || inv.UID != "meeting-1@example.com" || inv.Sequence != 2 {
		t.Errorf("invite = %+v", inv)
	}
	if inv.Summary != "Quarterly planning, with lunch" || inv.Location != "Room 4" {
		t.Errorf("summary %q, location %q", inv.Summary, inv.Location)
	}
	if inv.Organizer.Email != "maya@example.com" || inv.Organizer.Name != "Maya Chen" {
		t.Errorf("organizer = %+v", inv.Organizer)
	}
	if len(inv.Attendees) != 2 || inv.Attendees[0].Status != "NEEDS-ACTION" || inv.Attendees[1].Status != "ACCEPTED" {
		t.Errorf("attendees = %+v", inv.Attendees)
	}
	if inv.AllDay {
		t.Error("a timed event is all-day")
	}
	want := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	if !inv.Start.Equal(want) {
		t.Errorf("start = %v, want %v", inv.Start, want)
	}
	if inv.Canceled {
		t.Error("a request is canceled")
	}
}

func TestParseCalendarAllDayAndCancel(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:x\r\nSUMMARY:Offsite\r\n" +
		"DTSTART;VALUE=DATE:20260401\r\nDTEND;VALUE=DATE:20260402\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	inv, err := ParseCalendar(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.AllDay || !inv.Canceled || inv.Start.Year() != 2026 {
		t.Errorf("invite = %+v", inv)
	}
	if _, err := ParseCalendar("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"); err == nil {
		t.Error("a calendar with no event parsed")
	}
}

// A date-time with neither a zone nor a "Z" is floating: the reader's
// own wall clock, wherever they are.
func TestParseCalendarFloatingTime(t *testing.T) {
	base := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:x\r\nSUMMARY:S\r\n"
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"DTSTART:20260305T100000\r\n", true},
		{"DTSTART:20260305T100000Z\r\n", false},
		{"DTSTART;TZID=Europe/Berlin:20260305T100000\r\n", false},
		{"DTSTART;VALUE=DATE:20260305\r\n", false},
		{"DTSTART:20260305\r\n", false},
	} {
		inv, err := ParseCalendar(base + tc.line + "END:VEVENT\r\nEND:VCALENDAR\r\n")
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if inv.Floating != tc.want {
			t.Errorf("%q: floating = %v, want %v", tc.line, inv.Floating, tc.want)
		}
	}
}

func TestCalendarReply(t *testing.T) {
	inv, err := ParseCalendar(testInvite)
	if err != nil {
		t.Fatal(err)
	}
	reply := CalendarReply(inv, Address{Name: "Ann", Email: "ann@example.com"}, "ACCEPTED")
	for _, want := range []string{"METHOD:REPLY", "UID:meeting-1@example.com", "SEQUENCE:2",
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:ann@example.com", "DTSTART;TZID=Europe/Berlin:20260305T100000",
		"ORGANIZER:mailto:maya@example.com"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply misses %q: %q", want, reply)
		}
	}
	if got := CalendarResponseText("DECLINED"); got == "" {
		t.Error("no decline text")
	}
}

// A calendar part is the message's invitation, not body text.
func TestParseMessageFindsTheInvitation(t *testing.T) {
	raw := strings.Join([]string{
		"From: Maya <maya@example.com>",
		"Subject: Invitation",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Join us.",
		"--b",
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8",
		"",
		testInvite,
		"--b--",
	}, "\r\n")
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Calendar == nil || m.Calendar.UID != "meeting-1@example.com" {
		t.Errorf("calendar = %+v", m.Calendar)
	}
	if strings.Contains(m.BodyText, "BEGIN:VCALENDAR") {
		t.Errorf("the calendar became body text: %q", m.BodyText)
	}
	if m.BodyText != "Join us." {
		t.Errorf("body = %q", m.BodyText)
	}
}

func TestParseMessageKeepsAnUnreadableCalendar(t *testing.T) {
	raw := strings.Join([]string{
		"From: Maya <maya@example.com>",
		"Subject: Broken",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/calendar; method=REQUEST",
		"",
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR",
		"--b--",
	}, "\r\n")
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Calendar != nil {
		t.Error("a calendar with no event parsed")
	}
	if !strings.Contains(m.CalendarRaw, "BEGIN:VCALENDAR") {
		t.Error("the unreadable part was not kept")
	}
}

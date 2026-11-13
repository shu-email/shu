package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

// The invitation a body fetch found is in the cache, so the card shows
// after a restart too.
func TestMessageCalendarRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	inv := &mail.CalendarInvite{
		Method: "REQUEST", UID: "meeting-1@example.com", Sequence: 2,
		Summary: "Quarterly planning", Location: "Room 4",
		Start:     time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 3, 5, 11, 0, 0, 0, time.UTC),
		Organizer: mail.Address{Name: "Maya", Email: "maya@example.com"},
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", mail.Message{
		Summary: mail.Summary{UID: 1, Folder: "INBOX", Subject: "Invitation"}, Calendar: inv,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetMessage(ctx, "a1", "INBOX", 1)
	if err != nil || !ok {
		t.Fatalf("message = %v, %v", ok, err)
	}
	if got.Calendar == nil || got.Calendar.UID != inv.UID || got.Calendar.Summary != inv.Summary ||
		!got.Calendar.Start.Equal(inv.Start) || got.Calendar.Organizer.Email != inv.Organizer.Email {
		t.Errorf("calendar = %+v", got.Calendar)
	}
}

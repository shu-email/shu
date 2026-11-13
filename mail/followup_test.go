package mail

import (
	"context"
	"testing"
	"time"
)

// A conversation waits from its third day to its thirtieth.
func TestFollowUpAgeBounds(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		age  time.Duration
		want int
		ok   bool
	}{
		{2 * day, 0, false},
		{2*day + 23*time.Hour, 0, false},
		{3 * day, 3, true},
		{3*day + 23*time.Hour, 3, true},
		{30 * day, 30, true},
		{30*day - time.Second, 29, true},
		{30*day + time.Second, 0, false},
		{31 * day, 0, false},
	} {
		got, ok := FollowUpAge(now, now.Add(-tc.age))
		if got != tc.want || ok != tc.ok {
			t.Errorf("age %v = %d, %v; want %d, %v", tc.age, got, ok, tc.want, tc.ok)
		}
	}
}

// The count is the conversations waiting at the service's clock, and a
// dismissal keeps one off until a newer message is cached.
func TestFollowUpCountAndDismissal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	svc, store, _ := newTestService()
	svc.SetClock(func() time.Time { return now })
	if err := store.SaveFolders(ctx, "a1", []Folder{
		{Name: "Sent", SpecialUse: SpecialSent},
		{Name: "INBOX", SpecialUse: SpecialInbox},
		{Name: "Drafts", SpecialUse: SpecialDrafts},
	}); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-10 * 24 * time.Hour)
	sync := func(folder string, uid UID, id, replyTo string, thread ThreadID, date time.Time) {
		t.Helper()
		if err := store.ApplySync(ctx, "a1", SyncBatch{Folder: Folder{Name: folder}, Messages: []Message{
			{Summary: Summary{UID: uid, MessageID: id, InReplyTo: replyTo, Thread: thread, Date: date}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	sync("Sent", 1, "plan@example.com", "", 5, at)
	sync("Sent", 2, "bills@example.com", "", 6, at)
	sync("Drafts", 1, "bills-draft@example.com", "bills@example.com", 6, now.Add(-5*24*time.Hour))
	n, err := svc.FollowUpCount(ctx, "a1")
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2", n, err)
	}
	if err := svc.DismissFollowUp(ctx, "a1", 5); err != nil {
		t.Fatal(err)
	}
	if n, _ = svc.FollowUpCount(ctx, "a1"); n != 1 {
		t.Errorf("count after the dismissal = %d, want 1", n)
	}
	// A message newer than the dismissal brings the conversation back.
	later := now.Add(20 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return later })
	sync("Sent", 3, "plan-2@example.com", "plan@example.com", 5, now.Add(10*24*time.Hour))
	if n, _ = svc.FollowUpCount(ctx, "a1"); n != 2 {
		t.Errorf("count with a newer message = %d, want 2", n)
	}
}

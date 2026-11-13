package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"shu/mail"
)

// followUpFixture is Ann's mail as the follow-up list reads it, on a
// clock at followUpNow:
//
//	a two-day answer      sent two days ago: too soon
//	a three-day answer    sent three days ago: it waits
//	a month-old answer    sent thirty days ago: it waits
//	an older answer       sent thirty-one days ago: too late
//	an answered thread    sent ten days ago, answered five days ago
//	a drafted thread      sent ten days ago, a draft since
//	a trashed thread      sent ten days ago, a message in Trash since
//	a junked thread       sent ten days ago, a message in Junk since
//	a dismissed thread    sent ten days ago, dismissed now
//	a revived thread      sent ten days ago, dismissed eight days ago, sent again six days ago
//	an archived thread    sent ten days ago, a newer message in Archive
func followUpFixture(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(context.Background(), "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
		{Name: "Drafts", SpecialUse: mail.SpecialDrafts},
		{Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
	}); err != nil {
		t.Fatal(err)
	}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	lin := mail.Address{Name: "Lin", Email: "lin@example.com"}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	sent := func(uid mail.UID, id, subject string, at time.Time) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, MessageID: id, From: ann, To: []mail.Address{lin},
			Subject: subject, Date: at, Seen: true}}
	}
	receive := func(uid mail.UID, id, replyTo, subject string, at time.Time) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, MessageID: id, InReplyTo: replyTo,
			From: lin, To: []mail.Address{ann}, Subject: subject, Date: at}}
	}
	syncMessages(t, s, "a1", "Sent",
		sent(1, "two@example.com", "Two days", ago(2*day)),
		sent(2, "three@example.com", "Three days", ago(3*day)),
		sent(3, "month@example.com", "A month old", ago(30*day)),
		sent(4, "older@example.com", "An older answer", ago(31*day)),
		sent(5, "answered@example.com", "An answered thread", ago(10*day)),
		sent(6, "drafted@example.com", "A drafted thread", ago(10*day)),
		sent(7, "trashed@example.com", "A trashed thread", ago(10*day)),
		sent(8, "junked@example.com", "A junked thread", ago(10*day)),
		sent(9, "dismissed@example.com", "A dismissed thread", ago(10*day)),
		sent(10, "revived@example.com", "A revived thread", ago(10*day)),
		sent(12, "revived@example.com", "Re: A revived thread", ago(6*day)),
		sent(13, "archived@example.com", "An archived thread", ago(10*day)))
	syncMessages(t, s, "a1", "INBOX", receive(1, "answer@example.com", "answered@example.com",
		"Re: An answered thread", ago(5*day)))
	syncMessages(t, s, "a1", "Drafts", sent(1, "draft@example.com", "Re: A drafted thread", ago(5*day)))
	syncMessages(t, s, "a1", "Trash", receive(1, "into-trash@example.com", "trashed@example.com",
		"Re: A trashed thread", ago(5*day)))
	syncMessages(t, s, "a1", "Junk", receive(1, "into-junk@example.com", "junked@example.com",
		"Re: A junked thread", ago(5*day)))
	syncMessages(t, s, "a1", "Archive", receive(1, "into-archive@example.com", "archived@example.com",
		"Re: An archived thread", ago(5*day)))
	ctx := context.Background()
	if err := s.DismissFollowUp(ctx, "a1", threadOf(t, s, "a1", "Sent", 9), now); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissFollowUp(ctx, "a1", threadOf(t, s, "a1", "Sent", 10), now.Add(-8*day)); err != nil {
		t.Fatal(err)
	}
}

func followUpSubjects(t *testing.T, s *Store, now time.Time) []string {
	t.Helper()
	page, err := s.ConversationPage(context.Background(),
		mail.ListQuery{Scope: mail.FollowUpScope("a1", now), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range page.Conversations {
		out = append(out, c.Latest.Subject)
	}
	return out
}

// A conversation waits for a reply from its third day to its thirtieth,
// and only while what the user sent is the newest message that is not a
// draft, junk or trash.
func TestFollowUpWaitsBetweenItsAges(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	s := openTest(t)
	s.now = func() time.Time { return now }
	followUpFixture(t, s, now)
	got := followUpSubjects(t, s, now)
	want := []string{"Three days", "Re: A revived thread", "A drafted thread", "A month old"}
	if !slices.Equal(got, want) {
		t.Errorf("follow-ups = %q, want %q", got, want)
	}
	n, err := s.CountFollowUps(context.Background(),
		mail.ListQuery{Scope: mail.FollowUpScope("a1", time.Time{})})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) {
		t.Errorf("count = %d, want %d", n, len(want))
	}
	// A clock ahead of the messages' ages moves every one of them out.
	if got := followUpSubjects(t, s, now.Add(29*24*time.Hour)); len(got) != 0 {
		t.Errorf("a later clock left %q", got)
	}
}

// The list's messages are the conversations' in every folder that keeps
// mail, and its rows are the messages the user sent.
func TestFollowUpMessagesAreTheSentOnes(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	s := openTest(t)
	s.now = func() time.Time { return now }
	followUpFixture(t, s, now)
	page, err := s.MessagePage(context.Background(),
		mail.ListQuery{Scope: mail.FollowUpScope("a1", now), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range page.Messages {
		got = append(got, m.Subject)
	}
	want := []string{"Three days", "Re: A revived thread", "A revived thread",
		"A drafted thread", "A month old"}
	if !slices.Equal(got, want) {
		t.Errorf("messages = %q, want %q", got, want)
	}
}

// Dismissing a conversation keeps it off until a message newer than the
// dismissal is cached.
func TestFollowUpDismissalLastsUntilNewerMail(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	s := openTest(t)
	s.now = func() time.Time { return now }
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "Sent")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "Sent", SpecialUse: mail.SpecialSent},
	}); err != nil {
		t.Fatal(err)
	}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	msg := func(uid mail.UID, id string, at time.Time) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, MessageID: id, From: ann,
			To: []mail.Address{{Email: "lin@example.com"}}, Subject: "Plan", Date: at, Seen: true}}
	}
	syncMessages(t, s, "a1", "Sent", msg(1, "plan@example.com", now.Add(-10*24*time.Hour)))
	thread := threadOf(t, s, "a1", "Sent", 1)
	if err := s.DismissFollowUp(ctx, "a1", thread, now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := followUpSubjects(t, s, now); len(got) != 0 {
		t.Errorf("a dismissed conversation stayed: %q", got)
	}
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 2, MessageID: "plan-2@example.com",
		InReplyTo: "plan@example.com", From: ann, To: []mail.Address{{Email: "lin@example.com"}},
		Subject: "Plan", Date: now.Add(-6 * 24 * time.Hour), Seen: true}})
	if got := followUpSubjects(t, s, now); !slices.Equal(got, []string{"Plan"}) {
		t.Errorf("a newer message did not bring it back: %q", got)
	}
	// Dismissing it again keeps it off, whatever older mail is cached.
	if err := s.DismissFollowUp(ctx, "a1", thread, now); err != nil {
		t.Fatal(err)
	}
	if got := followUpSubjects(t, s, now); len(got) != 0 {
		t.Errorf("the second dismissal was lost: %q", got)
	}
}

// A conversation that merges into another keeps its dismissal.
func TestFollowUpDismissalFollowsAMerge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	s := openTest(t)
	s.now = func() time.Time { return now }
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "Sent", SpecialUse: mail.SpecialSent},
	}); err != nil {
		t.Fatal(err)
	}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	at := now.Add(-10 * 24 * time.Hour)
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 1, MessageID: "a@example.com",
		From: ann, Subject: "One", Date: at, Seen: true}})
	first := threadOf(t, s, "a1", "Sent", 1)
	if err := s.DismissFollowUp(ctx, "a1", first, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A second beginning, dismissed later.
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 2, MessageID: "b@example.com",
		From: ann, Subject: "Two", Date: at, Seen: true}})
	second := threadOf(t, s, "a1", "Sent", 2)
	if err := s.DismissFollowUp(ctx, "a1", second, now); err != nil {
		t.Fatal(err)
	}
	// A message linking both, which merges them into the lower ID.
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 3, MessageID: "c@example.com",
		InReplyTo: "b@example.com", References: []string{"a@example.com"}, From: ann, Subject: "Plan",
		Date: at, Seen: true}})
	merged := threadOf(t, s, "a1", "Sent", 3)
	if merged != min(first, second) {
		t.Fatalf("the merge kept %d, want %d", merged, min(first, second))
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM follow_up_dismissals`); got != 1 {
		t.Errorf("dismissals = %d, want 1", got)
	}
	if got := countRows(t, s, `SELECT MAX(dismissed_at) FROM follow_up_dismissals WHERE thread_id = ?`,
		int64(merged)); got != now.Unix() {
		t.Errorf("dismissed at %d, want %d", got, now.Unix())
	}
	// The merged conversation is dismissed still.
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 4, MessageID: "d@example.com",
		InReplyTo: "c@example.com", From: ann, Subject: "Plan", Date: at, Seen: true}})
	if got := followUpSubjects(t, s, now); len(got) != 0 {
		t.Errorf("the merged conversation came back: %q", got)
	}
}

// A server that keeps a copy of sent mail in another folder, as Gmail's
// All Mail does, does not take the conversation off the list: the copy is
// the message the user sent.
func TestFollowUpReadsACopyOfSentMail(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	s := openTest(t)
	s.now = func() time.Time { return now }
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "All Mail", SpecialUse: mail.SpecialArchive},
	}); err != nil {
		t.Fatal(err)
	}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	lin := mail.Address{Name: "Lin", Email: "lin@example.com"}
	at := now.Add(-10 * 24 * time.Hour)
	copy := mail.Summary{MessageID: "plan@example.com", From: ann, To: []mail.Address{lin},
		Subject: "Plan", Date: at, Seen: true}
	sent := copy
	sent.UID = 1
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: sent})
	kept := copy
	kept.UID = 1
	syncMessages(t, s, "a1", "All Mail", mail.Message{Summary: kept})
	if got := followUpSubjects(t, s, now); !slices.Equal(got, []string{"Plan"}) {
		t.Fatalf("a copy of the sent mail took it off: %q", got)
	}
	// An answer still counts, whichever folder the server files it in.
	syncMessages(t, s, "a1", "All Mail", mail.Message{Summary: mail.Summary{UID: 2,
		MessageID: "answer@example.com", InReplyTo: "plan@example.com", From: lin,
		To: []mail.Address{ann}, Subject: "Re: Plan", Date: now.Add(-5 * 24 * time.Hour)}})
	if got := followUpSubjects(t, s, now); len(got) != 0 {
		t.Errorf("an answered conversation waits: %q", got)
	}
}

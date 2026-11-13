package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

var reminderBase = time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)

// reminderFixture has an account with two conversations: one in INBOX/1
// and one in Sent/1.
func reminderFixture(t *testing.T, s *Store) (inboxThread, sentThread mail.ThreadID) {
	t.Helper()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(context.Background(), "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
	}); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "in@example.org", Subject: "Plan", Date: reminderBase}})
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "out@example.org", Subject: "Report", Date: reminderBase}})
	return threadOf(t, s, "a1", "INBOX", 1), threadOf(t, s, "a1", "Sent", 1)
}

// Reminders keep a conversation's subject and time, read the soonest
// first, become due at their time and no earlier, and one set again
// keeps its place.
func TestReminderRoundTripAndDue(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	inboxThread, sentThread := reminderFixture(t, s)
	later := reminderBase.Add(2 * time.Hour)
	sooner := reminderBase.Add(time.Hour)
	plan, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: inboxThread,
		Subject: "Plan", RemindAt: later, CreatedAt: reminderBase})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ID == 0 || !plan.RemindAt.Equal(later) {
		t.Fatalf("saved reminder = %+v", plan)
	}
	_, err = s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: sentThread,
		Subject: "Report", RemindAt: sooner, CreatedAt: reminderBase})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Reminders(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Thread != sentThread || list[1].Thread != inboxThread ||
		list[0].Subject != "Report" || list[1].Subject != "Plan" {
		t.Errorf("reminders = %+v", list)
	}
	if due, err := s.DueReminders(ctx, reminderBase.Add(30*time.Minute)); err != nil || len(due) != 0 {
		t.Errorf("nothing is due yet: %+v, %v", due, err)
	}
	if due, err := s.DueReminders(ctx, sooner); err != nil || len(due) != 1 || due[0].Thread != sentThread {
		t.Errorf("at its time a reminder is due: %+v, %v", due, err)
	}
	if due, err := s.DueReminders(ctx, later); err != nil || len(due) != 2 {
		t.Errorf("both are due at the later time: %+v, %v", due, err)
	}
	// Setting one again moves its time, and keeps it where it was.
	plan2, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: inboxThread,
		Subject: "Plan", RemindAt: sooner, CreatedAt: reminderBase.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.ID != plan.ID || !plan2.RemindAt.Equal(sooner) || !plan2.CreatedAt.Equal(plan.CreatedAt) {
		t.Errorf("a reminder set again = %+v, was %+v", plan2, plan)
	}
	if due, err := s.DueReminders(ctx, sooner); err != nil || len(due) != 2 {
		t.Errorf("both are due after the change: %+v, %v", due, err)
	}
	if err := s.DeleteReminder(ctx, "a1", inboxThread); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteReminder(ctx, "a1", inboxThread); err != nil {
		t.Errorf("deleting a reminder twice = %v", err)
	}
	if list, err := s.Reminders(ctx, "a1"); err != nil || len(list) != 1 || list[0].Thread != sentThread {
		t.Errorf("after a deletion = %+v, %v", list, err)
	}
}

// A list of reminders reads only their conversations, and every folder
// that holds them.
func TestRemindedScopeListsConversations(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	inboxThread, _ := reminderFixture(t, s)
	syncMessages(t, s, "a1", "Archive", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "in@example.org", Subject: "Plan", Date: reminderBase.Add(-time.Hour)}})
	if _, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: inboxThread,
		Subject: "Plan", RemindAt: reminderBase, CreatedAt: reminderBase}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.RemindersScope("a1")})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0].Thread != inboxThread || page.Conversations[0].Count != 2 {
		t.Fatalf("reminders page = %+v", page)
	}
	msgs, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.RemindersScope("a1")})
	if err != nil || len(msgs.Messages) != 2 {
		t.Errorf("reminded messages = %+v, %v", msgs, err)
	}
	// Another account's reminders are not in its scope.
	if page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.RemindersScope("a2")}); err != nil ||
		len(page.Conversations) != 0 {
		t.Errorf("another account's page = %+v, %v", page, err)
	}
	if err := s.DeleteReminder(ctx, "a1", inboxThread); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.RemindersScope("a1")}); err != nil ||
		len(page.Conversations) != 0 {
		t.Errorf("after a deletion the page = %+v, %v", page, err)
	}
}

// A reminder follows its conversation when it merges into another, and
// two reminders of merged conversations become one, the earlier due.
func TestReminderFollowsMergedConversation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	inboxThread, sentThread := reminderFixture(t, s)
	first := reminderBase.Add(2 * time.Hour)
	if _, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: inboxThread,
		Subject: "Plan", RemindAt: first, CreatedAt: reminderBase}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: sentThread,
		Subject: "Report", RemindAt: reminderBase.Add(time.Hour), CreatedAt: reminderBase}); err != nil {
		t.Fatal(err)
	}
	// A message that names both conversations makes them one.
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 2,
		MessageID: "both@example.org", References: []string{"in@example.org", "out@example.org"},
		Subject: "Plan and report", Date: reminderBase.Add(3 * time.Hour)}})
	merged := threadOf(t, s, "a1", "INBOX", 2)
	if merged != min(inboxThread, sentThread) {
		t.Fatalf("conversation %d did not merge into %d", merged, min(inboxThread, sentThread))
	}
	list, err := s.Reminders(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Thread != merged {
		t.Fatalf("reminders after the merge = %+v", list)
	}
	if !list[0].RemindAt.Equal(reminderBase.Add(time.Hour)) {
		t.Errorf("the merged reminder is due %v, want the earlier time", list[0].RemindAt)
	}
}

// The reminders' list is soonest due first, and itself a page at a time;
// a copy in the trash or a draft is not one of its messages.
func TestRemindedScopeOrdersByDueTime(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
		{Name: "Drafts", SpecialUse: mail.SpecialDrafts},
	}); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "in@example.org", Subject: "Plan", Date: reminderBase}})
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "out@example.org", Subject: "Report", Date: reminderBase}})
	// A newer message in the sent conversation: its date alone would put
	// it first.
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 2,
		MessageID: "out2@example.org", InReplyTo: "out@example.org", Subject: "Re: Report",
		Date: reminderBase.Add(time.Hour)}})
	inboxThread := threadOf(t, s, "a1", "INBOX", 1)
	sentThread := threadOf(t, s, "a1", "Sent", 1)
	// Copies in the trash and the drafts are of the inbox conversation,
	// and are not in the reminders' scope.
	syncMessages(t, s, "a1", "Trash", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "in@example.org", Subject: "Plan", Date: reminderBase.Add(2 * time.Hour)}})
	syncMessages(t, s, "a1", "Drafts", mail.Message{Summary: mail.Summary{UID: 1,
		MessageID: "draft@example.org", InReplyTo: "in@example.org", Subject: "Re: Plan",
		Date: reminderBase.Add(3 * time.Hour)}})
	if _, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: sentThread,
		Subject: "Report", RemindAt: reminderBase.Add(2 * time.Hour), CreatedAt: reminderBase}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReminder(ctx, mail.Reminder{Account: "a1", Thread: inboxThread,
		Subject: "Plan", RemindAt: reminderBase.Add(time.Hour), CreatedAt: reminderBase}); err != nil {
		t.Fatal(err)
	}

	var order []mail.ThreadID
	after := mail.Cursor("")
	for range 3 {
		page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.RemindersScope("a1"),
			After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Conversations {
			order = append(order, c.Thread)
			if c.Thread == inboxThread && c.Count != 1 {
				t.Errorf("the inbox conversation has %d messages in the reminders, want 1", c.Count)
			}
		}
		after = page.Next
	}
	if len(order) != 2 || order[0] != inboxThread || order[1] != sentThread {
		t.Errorf("the reminders are listed %v, want %d then %d", order, inboxThread, sentThread)
	}
}

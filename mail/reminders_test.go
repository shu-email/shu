package mail

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

var reminderNow = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// reminderEnv is a service whose account has an INBOX with one message
// and an Archive, synced.
func reminderEnv(t *testing.T) (*Service, *memStore, *memSecrets, *fakeIMAP, Account) {
	t.Helper()
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	svc.SetClock(func() time.Time { return reminderNow })
	a := fakeAccount(t, f.addr)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	return svc, store, secrets, f, a
}

// inboxThread is the conversation of the inbox's only message.
func inboxThread(t *testing.T, svc *Service, a Account) ThreadID {
	t.Helper()
	list, err := svc.CachedConversations(context.Background(), a.ID, "INBOX", "", 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("inbox conversations = %+v, %v", list, err)
	}
	return list[0].Thread
}

// Setting a reminder takes the conversation out of the inbox, saves the
// reminder, and lists it as the list of reminders; at its time the
// conversation comes back, announced as new mail, and the reminder goes.
func TestSetAndDeliverReminder(t *testing.T) {
	ctx := context.Background()
	svc, _, _, f, a := reminderEnv(t)
	thread := inboxThread(t, svc, a)
	// The watermark is set before the reminder, so what comes back is
	// above it: new mail to announce.
	if _, err := svc.store.TakeNew(ctx, a.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	at := reminderNow.Add(2 * time.Hour)
	r, moved, err := svc.SetReminder(ctx, a.ID, thread, at)
	if err != nil {
		t.Fatal(err)
	}
	if r.Thread != thread || r.Subject != "Plain message" || !r.RemindAt.Equal(at) || r.CreatedAt.IsZero() {
		t.Errorf("reminder = %+v", r)
	}
	if len(moved) != 1 || moved[0].Folder != "Archive" || moved[0].From != "INBOX" {
		t.Errorf("moved = %+v", moved)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 0 {
		t.Errorf("the inbox still has %q", got)
	}
	if got := f.subjects(t, "Archive"); !slices.Equal(got, []string{"Plain message"}) {
		t.Errorf("the archive has %q", got)
	}
	list, err := svc.Reminders(ctx, a.ID)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("reminders = %+v, %v", list, err)
	}
	page, err := svc.ConversationPage(ctx, ListQuery{Scope: RemindersScope(a.ID)})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].Thread != thread {
		t.Errorf("the reminder's list = %+v, %v", page, err)
	}

	delivered, err := svc.DeliverReminders(ctx)
	if err != nil || len(delivered) != 0 {
		t.Fatalf("a reminder delivered before its time = %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "Archive"); len(got) != 1 {
		t.Errorf("the archive changed before the time: %q", got)
	}

	svc.SetClock(func() time.Time { return at })
	delivered, err = svc.DeliverReminders(ctx)
	if err != nil || len(delivered) != 1 || delivered[0].ID != r.ID {
		t.Fatalf("delivered = %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "INBOX"); !slices.Equal(got, []string{"Plain message"}) {
		t.Errorf("the inbox has %q", got)
	}
	if got := f.subjects(t, "Archive"); len(got) != 0 {
		t.Errorf("the archive still has %q", got)
	}
	if list, err := svc.Reminders(ctx, a.ID); err != nil || len(list) != 0 {
		t.Errorf("the reminder stayed: %+v, %v", list, err)
	}
	if msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); err != nil || len(msgs) != 1 {
		t.Errorf("the inbox's cache = %+v, %v", msgs, err)
	}
	fresh, err := svc.store.TakeNew(ctx, a.ID, "INBOX")
	if err != nil || len(fresh) != 1 {
		t.Errorf("the returned mail is announced at most once: %+v, %v", fresh, err)
	}
}

// Removing a reminder leaves the conversation where it is.
func TestRemoveReminderLeavesMail(t *testing.T) {
	ctx := context.Background()
	svc, _, _, f, a := reminderEnv(t)
	thread := inboxThread(t, svc, a)
	if _, _, err := svc.SetReminder(ctx, a.ID, thread, reminderNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveReminder(ctx, a.ID, thread); err != nil {
		t.Fatal(err)
	}
	if list, err := svc.Reminders(ctx, a.ID); err != nil || len(list) != 0 {
		t.Errorf("reminders after removal = %+v, %v", list, err)
	}
	if got := f.subjects(t, "Archive"); !slices.Equal(got, []string{"Plain message"}) {
		t.Errorf("removing a reminder moved mail: archive %q", got)
	}
	if delivered, err := svc.DeliverReminders(ctx); err != nil || len(delivered) != 0 {
		t.Errorf("a removed reminder was delivered: %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 0 {
		t.Errorf("the inbox has %q", got)
	}
}

// A reminder outlives the run that set it: the next run delivers it at
// its time, and not before.
func TestReminderSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	svc, store, secrets, f, a := reminderEnv(t)
	thread := inboxThread(t, svc, a)
	at := reminderNow.Add(3 * time.Hour)
	if _, _, err := svc.SetReminder(ctx, a.ID, thread, at); err != nil {
		t.Fatal(err)
	}
	restart := func(now time.Time) *Service {
		next := NewService(store, secrets, "")
		next.SetTLSConfig(insecureTLS())
		next.SetClock(func() time.Time { return now })
		return next
	}
	delivered, err := restart(reminderNow.Add(time.Hour)).DeliverReminders(ctx)
	if err != nil || len(delivered) != 0 {
		t.Fatalf("the next run delivered it early: %+v, %v", delivered, err)
	}
	if list, err := restart(at).Reminders(ctx, a.ID); err != nil || len(list) != 1 {
		t.Fatalf("the reminder did not survive the restart: %+v, %v", list, err)
	}
	delivered, err = restart(at).DeliverReminders(ctx)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("the next run at the time = %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "INBOX"); !slices.Equal(got, []string{"Plain message"}) {
		t.Errorf("the inbox has %q", got)
	}
}

// A conversation already in the archive gets a reminder without a move,
// and one whose mail the user moved elsewhere meanwhile is not brought
// back, though the reminder goes.
func TestReminderOfArchivedMail(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive", "Trash")
	f.append(t, "Archive", readFixture(t, "plain.eml"))
	svc, _, a := newIMAPService(t, f, "secret")
	svc.SetClock(func() time.Time { return reminderNow })
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Archive", 50); err != nil {
		t.Fatal(err)
	}
	list, err := svc.CachedConversations(ctx, a.ID, "Archive", "", 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("archive conversations = %+v, %v", list, err)
	}
	thread := list[0].Thread
	_, moved, err := svc.SetReminder(ctx, a.ID, thread, reminderNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 0 {
		t.Errorf("an archived conversation moved: %+v", moved)
	}
	svc.SetClock(func() time.Time { return reminderNow.Add(time.Hour) })
	delivered, err := svc.DeliverReminders(ctx)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("delivered = %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "INBOX"); !slices.Equal(got, []string{"Plain message"}) {
		t.Errorf("the archived mail did not come back: %q", got)
	}

	// The same reminder, with the mail moved to the trash before its
	// time: nothing returns, and the reminder goes.
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	thread = inboxThread(t, svc, a)
	if _, _, err := svc.SetReminder(ctx, a.ID, thread, reminderNow.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	archived, err := svc.CachedMessages(ctx, a.ID, "Archive", "", 50)
	if err != nil || len(archived) != 1 {
		t.Fatalf("the archive's cache = %+v, %v", archived, err)
	}
	refs := []MessageRef{{Folder: "Archive", UID: archived[0].UID}}
	if _, err := svc.MoveMessages(ctx, a.ID, refs, "Trash"); err != nil {
		t.Fatal(err)
	}
	svc.SetClock(func() time.Time { return reminderNow.Add(2 * time.Hour) })
	delivered, err = svc.DeliverReminders(ctx)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("delivered after a move = %+v, %v", delivered, err)
	}
	if got := f.subjects(t, "Trash"); len(got) != 1 {
		t.Errorf("the trash has %q", got)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 0 {
		t.Errorf("mail moved away was brought back: %q", got)
	}
	if list, err := svc.Reminders(ctx, a.ID); err != nil || len(list) != 0 {
		t.Errorf("the reminder stayed: %+v, %v", list, err)
	}
}

// An account with no Archive folder cannot remind a conversation in its
// inbox: nothing moves and no reminder is saved.
func TestReminderWithoutArchive(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, _, a := newIMAPService(t, f, "secret")
	svc.SetClock(func() time.Time { return reminderNow })
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	thread := inboxThread(t, svc, a)
	if _, _, err := svc.SetReminder(ctx, a.ID, thread, reminderNow.Add(time.Hour)); !errors.Is(err, ErrNoArchive) {
		t.Fatalf("a reminder with no archive = %v", err)
	}
	if list, err := svc.Reminders(ctx, a.ID); err != nil || len(list) != 0 {
		t.Errorf("a reminder was saved: %+v, %v", list, err)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 1 {
		t.Errorf("the conversation left the inbox: %q", got)
	}
}

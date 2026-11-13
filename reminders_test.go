package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// reminderThread is the offsite conversation of convService, which is in
// the inbox.
func reminderThread(t *testing.T, a *app) mail.Conversation {
	t.Helper()
	for _, c := range a.convs {
		if c.Thread == 7 {
			return c
		}
	}
	t.Fatalf("no conversation 7 in %+v", a.convs)
	return mail.Conversation{}
}

func subjects(msgs []mail.Summary) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// Setting a reminder archives the conversation, takes it out of the
// inbox and lists it under Reminders with its time.
func TestRemindMeArchivesTheConversation(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	thread := reminderThread(t, a)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	a.setReminder("a1", thread.Thread, at)
	tt.Frame()
	if len(fs.reminders) != 1 || fs.reminders[0].Thread != 7 || !fs.reminders[0].RemindAt.Equal(at) {
		t.Fatalf("reminders = %+v", fs.reminders)
	}
	if got := subjects(fs.messages[listKey("a1", "INBOX")]); slices.Contains(got, "Venue for the offsite") {
		t.Errorf("the inbox still has the conversation: %q", got)
	}
	if got := subjects(fs.messages[listKey("a1", "Archive")]); !slices.Contains(got, "Venue for the offsite") {
		t.Errorf("the archive lacks the conversation: %q", got)
	}
	if len(a.reminders) != 1 || a.reminders[0].Thread != 7 {
		t.Errorf("the window's reminders = %+v", a.reminders)
	}
	if len(fs.reminderSet) != 1 {
		t.Errorf("the engine was asked %d times", len(fs.reminderSet))
	}
	if a.undo == nil || a.undo.reminder == nil {
		t.Fatalf("no undo of the reminder: %+v", a.undo)
	}
	if !tt.HasText(locales.Reminders()) {
		t.Errorf("the sidebar has no Reminders entry: %q", tt.Texts())
	}
	// The Reminders entry lists the conversation, with the reminder's
	// time in its row.
	click(t, tt, locales.Reminders())
	tt.Frame()
	if !tt.HasText("Re: Venue for the offsite") {
		t.Errorf("the reminders' list lacks the conversation: %q", tt.Texts())
	}
	if a.folder != remindersFolder || len(a.convs) != 1 || a.convs[0].Thread != 7 {
		t.Errorf("the reminders' list = %q, %+v", a.folder, a.convs)
	}
}

// The undo of a reminder puts the conversation back in the inbox and
// takes the reminder off.
func TestUndoReminder(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	thread := reminderThread(t, a)
	a.setReminder("a1", thread.Thread, time.Now().Add(time.Hour))
	a.undoLastMove()
	tt.Frame()
	if len(fs.reminders) != 0 {
		t.Errorf("the reminder stayed: %+v", fs.reminders)
	}
	if got := subjects(fs.messages[listKey("a1", "INBOX")]); !slices.Contains(got, "Venue for the offsite") {
		t.Errorf("the inbox lacks the conversation: %q", got)
	}
	if len(a.reminders) != 0 {
		t.Errorf("the window's reminders = %+v", a.reminders)
	}
	if a.undo != nil {
		t.Errorf("the undo is still offered: %+v", a.undo)
	}
}

// A new time on a conversation that has a reminder replaces it; its undo
// puts the old time back and leaves the mail archived.
func TestUndoReminderRestoresTheOldTime(t *testing.T) {
	a, fs := convApp(t)
	thread := reminderThread(t, a)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	a.setReminder("a1", thread.Thread, at)
	later := at.Add(24 * time.Hour)
	a.setReminder("a1", thread.Thread, later)
	if len(fs.reminders) != 1 || !fs.reminders[0].RemindAt.Equal(later) {
		t.Fatalf("the new time did not replace the old: %+v", fs.reminders)
	}
	a.undoLastMove()
	if len(fs.reminders) != 1 || !fs.reminders[0].RemindAt.Equal(at) {
		t.Fatalf("the old time did not come back: %+v", fs.reminders)
	}
	if len(a.reminders) != 1 || !a.reminders[0].RemindAt.Equal(at) {
		t.Errorf("the window's reminders = %+v", a.reminders)
	}
	if got := subjects(fs.messages[listKey("a1", "Archive")]); !slices.Contains(got, "Venue for the offsite") {
		t.Errorf("the undo moved the mail: archive %q", got)
	}
}

// Removing a reminder leaves the conversation where it is.
func TestRemoveReminder(t *testing.T) {
	a, fs := convApp(t)
	thread := reminderThread(t, a)
	a.setReminder("a1", thread.Thread, time.Now().Add(time.Hour))
	a.removeReminder("a1", thread.Thread)
	if len(fs.reminders) != 0 || len(a.reminders) != 0 {
		t.Errorf("the reminder stayed: %+v, %+v", fs.reminders, a.reminders)
	}
	if got := subjects(fs.messages[listKey("a1", "Archive")]); !slices.Contains(got, "Venue for the offsite") {
		t.Errorf("removing a reminder moved the mail: archive %q", got)
	}
	if got := subjects(fs.messages[listKey("a1", "INBOX")]); slices.Contains(got, "Venue for the offsite") {
		t.Errorf("removing a reminder brought the mail back: %q", got)
	}
}

// What is due comes back to the inbox, with a notification under the
// settings, and the reminder goes.
func TestDueReminderReturnsAndNotifies(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	thread := reminderThread(t, a)
	var notes []note
	a.notify = func(n note) error {
		notes = append(notes, n)
		return nil
	}
	a.focused = func() bool { return false }
	a.mailSet.Notify, a.mailSet.NotifyDetails = true, true
	a.setReminder("a1", thread.Thread, time.Now().Add(-time.Minute))
	a.deliverReminders()
	tt.Frame()
	if len(fs.reminders) != 0 || len(a.reminders) != 0 {
		t.Errorf("a due reminder stayed: %+v, %+v", fs.reminders, a.reminders)
	}
	if got := subjects(fs.messages[listKey("a1", "INBOX")]); !slices.Contains(got, "Venue for the offsite") {
		t.Errorf("the conversation did not come back: %q", got)
	}
	if len(notes) != 1 || notes[0].Title != "Ann" {
		t.Errorf("notes = %+v", notes)
	}
	if !strings.HasSuffix(notes[0].Body, "Venue for the offsite") {
		t.Errorf("the notification says %q", notes[0].Body)
	}

	// With the settings off, the mail still comes back, and nothing is
	// notified.
	notes = nil
	a.mailSet.Notify = false
	a.setReminder("a1", 8, time.Now().Add(-time.Minute))
	a.deliverReminders()
	if len(notes) != 0 {
		t.Errorf("a notification showed while notifications are off: %+v", notes)
	}
	if got, _ := a.svc.Reminders(t.Context(), "a1"); len(got) != 0 {
		t.Errorf("the due reminder stayed: %+v", got)
	}
}

// The presets are times of the days the menu names.
func TestReminderPresets(t *testing.T) {
	monday := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) // a Monday
	presets := reminderPresets(monday)
	if len(presets) != 4 {
		t.Fatalf("presets = %+v", presets)
	}
	want := []time.Time{
		time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC),
	}
	for i, p := range presets {
		if !p.at.Equal(want[i]) {
			t.Errorf("preset %d %q = %v, want %v", i, p.label, p.at, want[i])
		}
	}
	// After the evening, later today is tomorrow morning.
	evening := time.Date(2026, 11, 16, 19, 0, 0, 0, time.UTC)
	if got := laterToday(evening); !got.Equal(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("later today at 19:00 = %v", got)
	}
	// On Saturday the weekend is Sunday.
	saturday := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	if got := thisWeekend(saturday); !got.Equal(time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("this weekend on Saturday = %v", got)
	}
	// On Sunday evening it is the next weekend.
	sunday := time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)
	if got := thisWeekend(sunday); !got.Equal(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("this weekend on Sunday night = %v", got)
	}
}

// The custom dialog refuses what is not a time ahead, and sets what is.
func TestReminderCustomForm(t *testing.T) {
	a, fs := convApp(t)
	thread := reminderThread(t, a)
	now := time.Now()
	a.openReminderForm("a1", thread.Thread, now)
	if !a.remindForm.open {
		t.Fatal("the dialog did not open")
	}
	a.remindForm.when = "not a time"
	a.submitReminderForm()
	if !a.remindForm.open || a.remindForm.err != locales.ReminderBadTime() {
		t.Errorf("a bad time: open %v, err %q", a.remindForm.open, a.remindForm.err)
	}
	a.remindForm.when = now.Add(-time.Hour).Format(reminderLayout)
	a.submitReminderForm()
	if !a.remindForm.open || a.remindForm.err != locales.ReminderTimePast() {
		t.Errorf("a past time: open %v, err %q", a.remindForm.open, a.remindForm.err)
	}
	at := now.Add(26 * time.Hour)
	a.remindForm.when = at.Format(reminderLayout)
	a.submitReminderForm()
	if a.remindForm.open {
		t.Error("the dialog stayed open")
	}
	if len(fs.reminders) != 1 {
		t.Fatalf("reminders = %+v", fs.reminders)
	}
	if got := fs.reminders[0].RemindAt.Truncate(time.Minute); !got.Equal(at.Truncate(time.Minute)) {
		t.Errorf("the reminder is due %v, want %v", got, at)
	}
}

// The reminder's list is reachable without a folder of the server, and
// its search stays within it.
func TestRemindersListScope(t *testing.T) {
	a, _ := convApp(t)
	a.folder = remindersFolder
	a.loadMessages(a.gen)
	if q := a.listQuery(); q.mailScope().Reminded != true || len(q.mailScope().Accounts) != 1 {
		t.Errorf("the reminders' scope = %+v", q.mailScope())
	}
	a.query = "venue"
	if s := a.searchScope(); s != scopeHere {
		t.Errorf("a search of reminders covers %v", s)
	}
	if got := a.scopeChoices(); len(got) != 1 || got[0] != scopeHere {
		t.Errorf("the scope choices of reminders = %+v", got)
	}
}

package main

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// followUpFolders are the folders the follow-up fixture sorts mail into.
func followUpFolders() []mail.Folder {
	return []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
		{Name: "Drafts", SpecialUse: mail.SpecialDrafts},
		{Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
	}
}

// followUpMessages are Ann's mail as the follow-up list reads it:
//
//	1  sent ten days ago, no answer: it waits
//	2  sent yesterday: too soon
//	3  sent ten days ago, answered five days ago
//	4  sent ten days ago, a message in Trash since
//	5  sent ten days ago, a message in Junk since
//	6  sent ten days ago, a draft since: the draft does not count
//	7  sent ten days ago, dismissed
func followUpMessages() []mail.Summary {
	now := time.Now()
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	lin := mail.Address{Name: "Lin Wei", Email: "lin@example.com"}
	sent := func(uid mail.UID, thread mail.ThreadID, subject string, at time.Time) mail.Summary {
		return mail.Summary{UID: uid, Folder: "Sent", Thread: thread, MessageID: subject,
			From: ann, To: []mail.Address{lin}, Subject: subject, Date: at, Seen: true}
	}
	from := func(uid mail.UID, folder string, thread mail.ThreadID, subject string, at time.Time) mail.Summary {
		return mail.Summary{UID: uid, Folder: folder, Thread: thread, MessageID: folder + subject,
			From: lin, To: []mail.Address{ann}, Subject: subject, Date: at}
	}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	return []mail.Summary{
		sent(1, 1, "Venue for the offsite", ago(10*day)),
		sent(2, 2, "Quick question", ago(day)),
		sent(3, 3, "Lunch on Friday?", ago(10*day)),
		from(1, "INBOX", 3, "Re: Lunch on Friday?", ago(5*day)),
		sent(4, 4, "Wrong file", ago(10*day)),
		from(1, "Trash", 4, "Re: Wrong file", ago(5*day)),
		sent(5, 5, "Junked thread", ago(10*day)),
		from(1, "Junk", 5, "Re: Junked thread", ago(5*day)),
		sent(6, 6, "Draft in progress", ago(10*day)),
		{UID: 1, Folder: "Drafts", Thread: 6, MessageID: "draft", From: ann, To: []mail.Address{lin},
			Subject: "Re: Draft in progress", Date: ago(5 * day), Seen: true},
		sent(7, 7, "Dismissed thread", ago(10*day)),
	}
}

// followUpApp is the window on the follow-up fixture, with the dismissed
// conversation already taken off.
func followUpApp(t *testing.T) (*app, *fakeService) {
	t.Helper()
	fs := newFakeService()
	fs.dismissed = map[string]map[mail.ThreadID]time.Time{"a1": {7: time.Now()}}
	fs.addAccount(testAccount("a1", "Ann", "ann@example.com"), "secret", followUpFolders(), followUpMessages())
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, fs
}

// The sidebar counts the conversations that wait for a reply, and its
// list shows the message sent, whom it waits for and for how long.
func TestFollowUpList(t *testing.T) {
	a, _ := followUpApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	if !tt.HasText(locales.FollowUp()) {
		t.Fatalf("the sidebar has no Follow Up entry: %q", tt.Texts())
	}
	if a.followUps != 2 {
		t.Errorf("follow-ups = %d, want 2", a.followUps)
	}
	click(t, tt, locales.FollowUp())
	tt.Frame()
	if a.folder != followUpFolder {
		t.Fatalf("folder = %q", a.folder)
	}
	if !tt.HasText("Venue for the offsite") || !tt.HasText("Draft in progress") {
		t.Errorf("the list lacks the waiting conversations: %q", tt.Texts())
	}
	for _, gone := range []string{"Quick question", "Lunch on Friday?", "Wrong file", "Junked thread", "Dismissed thread"} {
		if tt.HasText(gone) {
			t.Errorf("%q is in the follow-up list", gone)
		}
	}
	if !tt.HasText("Lin Wei") {
		t.Errorf("a row does not say whom it waits for: %q", tt.Texts())
	}
	if !tt.HasText("10 days ago") {
		t.Errorf("a row does not say how long it waits: %q", tt.Texts())
	}
	if len(a.convs) != 2 {
		t.Errorf("conversations = %+v", a.convs)
	}
}

// Dismissing a conversation takes it off the list and the sidebar count,
// and the engine is asked to remember it.
func TestDismissFollowUp(t *testing.T) {
	a, fs := followUpApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	click(t, tt, locales.FollowUp())
	tt.Frame()
	if err := tt.RightClick("Venue for the offsite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.DismissFollowUp()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := fs.dismissedThreads; !slices.Equal(got, []string{"a1/1"}) {
		t.Errorf("dismissed = %q", got)
	}
	if a.followUps != 1 {
		t.Errorf("follow-ups = %d, want 1", a.followUps)
	}
	if tt.HasText("Venue for the offsite") {
		t.Errorf("the dismissed conversation is still listed: %q", tt.Texts())
	}
	if !tt.HasText("Draft in progress") {
		t.Errorf("the list lost the other conversation: %q", tt.Texts())
	}
}

// A dismissal the engine refuses leaves the row, and says why.
func TestDismissFollowUpFails(t *testing.T) {
	a, fs := followUpApp(t)
	fs.followUpErr = errors.New("the cache is gone")
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	click(t, tt, locales.FollowUp())
	tt.Frame()
	if err := tt.RightClick("Venue for the offsite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.DismissFollowUp()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.err == "" {
		t.Error("the window does not say the dismissal failed")
	}
	if !tt.HasText("Venue for the offsite") {
		t.Errorf("the row went without the dismissal: %q", tt.Texts())
	}
}

// The follow-ups are conversations, even where the user lists single
// messages.
func TestFollowUpListIsConversations(t *testing.T) {
	fs := newFakeService()
	inMessageView(fs)
	fs.dismissed = map[string]map[mail.ThreadID]time.Time{"a1": {7: time.Now()}}
	fs.addAccount(testAccount("a1", "Ann", "ann@example.com"), "secret", followUpFolders(), followUpMessages())
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	if a.threaded() {
		t.Fatal("the window lists conversations by default")
	}
	click(t, tt, locales.FollowUp())
	tt.Frame()
	if !a.threaded() {
		t.Fatal("the follow-up list lists messages one by one")
	}
	if len(a.convs) != 2 || a.convs[0].Count != 1 {
		t.Errorf("conversations = %+v", a.convs)
	}
	// Its own searches stay in its scope, which spans the account's
	// folders.
	if got := a.listScope(); !got.FollowUp || !slices.Equal(got.Accounts, []string{"a1"}) {
		t.Errorf("scope = %+v", got)
	}
	if !a.spansFolders() {
		t.Error("the follow-ups do not span folders")
	}
}

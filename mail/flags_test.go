package mail

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// flaggedOn reads which messages of a mailbox of the fake server have a
// flag, by subject.
func flaggedOn(t *testing.T, f *fakeIMAP, mailbox string, flag imap.Flag) []string {
	t.Helper()
	var out []string
	for _, m := range f.messages(t, mailbox) {
		if m.hasFlag(flag) {
			out = append(out, m.Subject)
		}
	}
	slices.Sort(out)
	return out
}

// selectsSince returns the SELECTs and EXAMINEs the fake server received
// after the first n.
func (f *fakeIMAP) selectsSince(n int) []fakeSelect {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSelect(nil), f.selects[n:]...)
}

func (f *fakeIMAP) selectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.selects)
}

// A conversation is starred or marked read with one connection, one
// read-write SELECT and one STORE per folder: those messages change, on
// the server and in the cache, and no others. Nothing is expunged, not
// even a message another mail client marked deleted in the same folder.
func TestSetFlagsOnAConversation(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(),
		append(in("INBOX", "gmail-1.eml", "same-subject-1.eml", "gmail-3.eml"), in("Sent", "gmail-2.eml")...))
	f.flag(t, "INBOX", 2, imap.FlagDeleted)
	c := conversationOf(t, svc, a, "", "Venue for the offsite")
	members, err := svc.CachedConversation(ctx, a.ID, "", c.Thread)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("conversation = %q", subjects(members))
	}
	inbox, _, _ := store.Folder(ctx, a.ID, "INBOX")

	sessions, selects := f.sessions.Load(), f.selectCount()
	if err := svc.SetFlags(ctx, a.ID, refsOf(members), FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if n := f.sessions.Load() - sessions; n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}
	if got := f.selectsSince(selects); !slices.Equal(got, []fakeSelect{{"INBOX", false}, {"Sent", false}}) {
		t.Errorf("selects = %+v, want INBOX then Sent, read-write", got)
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagSeen); !slices.Equal(got, []string{"Re: Venue for the offsite", "Venue for the offsite"}) {
		t.Errorf("read on the server's inbox = %q", got)
	}
	if got := flaggedOn(t, f, "Sent", imap.FlagSeen); len(got) != 1 {
		t.Errorf("read in Sent = %q", got)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 3 {
		t.Fatalf("server inbox = %q: something was expunged", got)
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagDeleted); !slices.Equal(got, []string{"Lunch on Friday?"}) {
		t.Errorf("deleted elsewhere = %q", got)
	}
	for _, m := range members {
		cached, _, _ := store.GetMessage(ctx, a.ID, m.Folder, m.UID)
		if !cached.Seen {
			t.Errorf("%s/%d is unread in the cache", m.Folder, m.UID)
		}
	}
	if other, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 2); other.Seen {
		t.Error("a message of another conversation was marked read")
	}
	if after, _, _ := store.Folder(ctx, a.ID, "INBOX"); after.Unread != inbox.Unread-2 {
		t.Errorf("inbox unread = %d, was %d", after.Unread, inbox.Unread)
	}

	// The star goes on one message and comes off them all.
	latest := members[len(members)-1]
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{latest.Folder, latest.UID}}, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); !slices.Equal(got, []string{"Re: Venue for the offsite"}) {
		t.Errorf("starred = %q", got)
	}
	if err := svc.SetFlags(ctx, a.ID, refsOf(members), FlagFlagged, false); err != nil {
		t.Fatal(err)
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("still starred = %q", got)
	}
	if cached, _, _ := store.GetMessage(ctx, a.ID, latest.Folder, latest.UID); cached.Flagged {
		t.Error("the star stays in the cache")
	}
	// Nothing to change opens no connection.
	sessions = f.sessions.Load()
	if err := svc.SetFlags(ctx, a.ID, nil, FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if f.sessions.Load() != sessions {
		t.Error("an empty change connected")
	}
}

// What the bulk change refuses or fails on leaves the rest as it was: a
// flag that deletes, a folder the server lost, a folder whose UIDs mean
// other messages now, and a cancelled context.
func TestSetFlagsFailures(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(),
		append(append(in("INBOX", "gmail-1.eml", "gmail-3.eml"), in("Sent", "gmail-2.eml")...), in("Lost", "same-subject-1.eml")...))
	c := conversationOf(t, svc, a, "", "Venue for the offsite")
	members, err := svc.CachedConversation(ctx, a.ID, "", c.Thread)
	if err != nil {
		t.Fatal(err)
	}

	sessions := f.sessions.Load()
	if err := svc.SetFlags(ctx, a.ID, refsOf(members), FlagDeleted, true); err == nil {
		t.Error("\\Deleted was set on several messages")
	}
	if f.sessions.Load() != sessions {
		t.Error("a refused change connected")
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagDeleted); len(got) != 0 {
		t.Errorf("marked deleted = %q", got)
	}

	// The folders before the one that fails change; those after it do not.
	if err := f.user.Delete("Lost"); err != nil {
		t.Fatal(err)
	}
	refs := []MessageRef{{"INBOX", members[0].UID}, {"Lost", 1}, {"Sent", members[1].UID}}
	if err := svc.SetFlags(ctx, a.ID, refs, FlagFlagged, true); err == nil {
		t.Fatal("a change in a folder the server lost succeeded")
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); !slices.Equal(got, []string{"Venue for the offsite"}) {
		t.Errorf("starred in the inbox = %q", got)
	}
	if got := flaggedOn(t, f, "Sent", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("starred in Sent after the failure = %q", got)
	}
	if sent, _, _ := store.GetMessage(ctx, a.ID, "Sent", members[1].UID); sent.Flagged {
		t.Error("the cache starred a message the server did not")
	}
	if first, _, _ := store.GetMessage(ctx, a.ID, "INBOX", members[0].UID); !first.Flagged {
		t.Error("the cache lost a change the server made")
	}

	// Sent is made again: its UID 1 is another message now.
	if err := f.user.Delete("Sent"); err != nil {
		t.Fatal(err)
	}
	f.create(t, "Sent")
	f.append(t, "Sent", readFixture(t, "threads/apple-1.eml"))
	err = svc.SetFlags(ctx, a.ID, []MessageRef{{"Sent", members[1].UID}}, FlagSeen, true)
	if !errors.Is(err, errValidityChanged) {
		t.Errorf("change under a new UIDVALIDITY = %v", err)
	}
	if got := flaggedOn(t, f, "Sent", imap.FlagSeen); len(got) != 0 {
		t.Errorf("read in the new Sent = %q", got)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.SetFlags(cancelled, a.ID, refsOf(members[2:]), FlagSeen, true); err == nil {
		t.Error("a cancelled change succeeded")
	}
	if last, _, _ := store.GetMessage(ctx, a.ID, "INBOX", members[2].UID); last.Seen {
		t.Error("a cancelled change reached the cache")
	}
	if err := svc.SetFlags(ctx, a.ID, []MessageRef{{"Nowhere", 1}}, FlagSeen, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("a folder never cached = %v", err)
	}
}

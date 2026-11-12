package mail

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// threadService serves the fixtures of testdata/threads in the given
// mailboxes, in order, with an Archive and a Trash, and syncs them once.
func threadService(t *testing.T, caps imap.CapSet, boxes [][2]string) (*Service, *memStore, *fakeIMAP, Account) {
	t.Helper()
	ctx := context.Background()
	f := startFakeIMAP(t, caps)
	names := []string{"Archive", "Trash"}
	for _, box := range boxes {
		if !slices.Contains(names, box[0]) {
			names = append(names, box[0])
		}
	}
	f.create(t, names...)
	for _, box := range boxes {
		f.append(t, box[0], readFixture(t, "threads/"+box[1]))
	}
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := svc.SyncFolder(ctx, a.ID, name, 50); err != nil {
			t.Fatal(err)
		}
	}
	return svc, store, f, a
}

// in puts fixtures in a mailbox, for threadService.
func in(mailbox string, names ...string) [][2]string {
	var out [][2]string
	for _, name := range names {
		out = append(out, [2]string{mailbox, name})
	}
	return out
}

// conversationOf finds the conversation of the message with a subject in
// a scope.
func conversationOf(t *testing.T, svc *Service, a Account, folder, subject string) Conversation {
	t.Helper()
	list, err := svc.CachedConversations(context.Background(), a.ID, folder, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		msgs, err := svc.CachedConversation(context.Background(), a.ID, folder, c.Thread)
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(msgs, func(m Summary) bool { return m.Subject == subject }) {
			return c
		}
	}
	t.Fatalf("no conversation in %q has %q", folder, subject)
	return Conversation{}
}

func refsOf(list []Summary) []MessageRef {
	var out []MessageRef
	for _, m := range list {
		out = append(out, MessageRef{Folder: m.Folder, UID: m.UID})
	}
	return out
}

func checkNotSeen(t *testing.T, f *fakeIMAP, mailbox string) {
	t.Helper()
	for _, m := range f.messages(t, mailbox) {
		if m.hasFlag(imap.FlagSeen) {
			t.Errorf("syncing marked %q read", m.Subject)
		}
	}
}

// Syncing the providers' conversations from the server threads them: the
// References and Thread-Index are read without marking anything read.
func TestSyncThreadsProviderFixtures(t *testing.T) {
	ctx := context.Background()
	groups := [][]string{
		{"gmail-1.eml", "gmail-2.eml", "gmail-3.eml"},
		{"outlook-1.eml", "outlook-2.eml", "outlook-3.eml"},
		{"apple-1.eml", "apple-2.eml", "apple-3.eml"},
		{"same-subject-1.eml"}, {"same-subject-2.eml"}, {"no-message-id.eml"},
		{"broken-references.eml"}, {"early-reply.eml"},
	}
	// The inbox holds them interleaved, as mail arrives.
	var order []string
	for i := range 3 {
		for _, g := range groups[:3] {
			order = append(order, g[i])
		}
	}
	for _, g := range groups[3:] {
		order = append(order, g[0])
	}
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", order...))

	label := func(m Summary) string { return m.From.Email + " " + m.Subject }
	groupOf := map[string]int{}
	for i, g := range groups {
		for _, name := range g {
			m, err := ParseMessage(readFixture(t, "threads/"+name))
			if err != nil {
				t.Fatal(err)
			}
			groupOf[label(m.Summary)] = i
		}
	}
	list, err := svc.CachedConversations(ctx, a.ID, "INBOX", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(groups) {
		t.Fatalf("conversations = %d, want %d", len(list), len(groups))
	}
	for _, c := range list {
		msgs, err := svc.CachedConversation(ctx, a.ID, "", c.Thread)
		if err != nil {
			t.Fatal(err)
		}
		group, ok := groupOf[label(msgs[0])]
		if !ok || c.Count != len(groups[group]) || len(msgs) != len(groups[group]) {
			t.Errorf("conversation of %q has %d messages (count %d)", label(msgs[0]), len(msgs), c.Count)
			continue
		}
		for _, m := range msgs {
			if groupOf[label(m)] != group {
				t.Errorf("%q is with %q", label(m), label(msgs[0]))
			}
		}
	}

	// What threads them came with the headers: References of Gmail,
	// Outlook's Thread-Index and the In-Reply-To of its reply without
	// References.
	gmail3, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 7)
	if len(gmail3.References) != 2 || gmail3.Subject != "Re: Venue for the offsite" {
		t.Errorf("gmail-3 = %q with references %q", gmail3.Subject, gmail3.References)
	}
	outlook1, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 2)
	outlook3, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 8)
	if outlook1.ThreadIndex != outlookHead || outlook3.ThreadIndex != outlookReply2 || len(outlook3.References) != 0 ||
		!strings.HasPrefix(outlook3.InReplyTo, "BY5PR11MB4056") {
		t.Errorf("outlook = %q / %q, references %q, in reply to %q",
			outlook1.ThreadIndex, outlook3.ThreadIndex, outlook3.References, outlook3.InReplyTo)
	}
	checkNotSeen(t, f, "INBOX")

	// The original that comes after its reply joins it.
	f.append(t, "INBOX", readFixture(t, "threads/late-original.eml"))
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if c := conversationOf(t, svc, a, "INBOX", "Garden plans"); c.Count != 2 || c.Latest.Subject != "Re: Garden plans" {
		t.Errorf("garden conversation = %d messages, latest %q", c.Count, c.Latest.Subject)
	}
	checkNotSeen(t, f, "INBOX")
}

// A conversation reads across Inbox and Sent, and each list counts its
// own folder's messages. A search finds it by the body of the reply in
// Sent.
func TestSyncThreadsInboxAndSent(t *testing.T) {
	ctx := context.Background()
	svc, _, _, a := threadService(t, allCaps(),
		append(in("INBOX", "span-1.eml", "span-3.eml"), in("Sent", "span-2.eml")...))
	whole := conversationOf(t, svc, a, "", "Contract review")
	if whole.Count != 3 || whole.Latest.Folder != "INBOX" || whole.Latest.From.Email != "bob@example.org" {
		t.Errorf("conversation = %+v", whole)
	}
	msgs, err := svc.CachedConversation(ctx, a.ID, "", whole.Thread)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Folder+" "+m.From.Email)
	}
	want := []string{"INBOX bob@example.org", "Sent ann@example.com", "INBOX bob@example.org"}
	if !slices.Equal(got, want) {
		t.Errorf("conversation reads %q, want %q", got, want)
	}
	if c := conversationOf(t, svc, a, "INBOX", "Contract review"); c.Count != 2 || c.Thread != whole.Thread {
		t.Errorf("inbox row = %+v", c)
	}
	if c := conversationOf(t, svc, a, "Sent", "Re: Contract review"); c.Count != 1 || c.Thread != whole.Thread {
		t.Errorf("sent row = %+v", c)
	}
	if _, err := svc.FetchMessage(ctx, a.ID, "Sent", 1, false); err != nil {
		t.Fatal(err)
	}
	found, err := svc.CachedConversations(ctx, a.ID, "INBOX", "indemnity", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Thread != whole.Thread || !slices.Equal(found[0].Matches, []MessageRef{{Folder: "Sent", UID: 1}}) {
		t.Errorf("search = %+v", found)
	}
}

// A sync that reads the cached window again gives a message the
// References a cache from before conversations lacks, and threads it.
func TestSyncRefreshThreadsCachedMessages(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	message := func(id, refs, subject string) []byte {
		return []byte("From: Kim Ito <kim@example.org>\r\nTo: Ann Example <ann@example.com>\r\nSubject: " + subject +
			"\r\nMessage-ID: <" + id + ">\r\n" + refs + "Date: Sun, 8 Nov 2026 10:00:00 +0000\r\n\r\nHello.\r\n")
	}
	// The reply, which names its original in References only, came first.
	f.append(t, "INBOX", message("kim-2@example.org", "References: <kim-1@example.org>\r\n", "Re: Tickets"))
	f.append(t, "INBOX", message("kim-1@example.org", "", "Tickets"))
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if c := conversationOf(t, svc, a, "INBOX", "Tickets"); c.Count != 2 {
		t.Fatalf("a first sync made %d of 2 messages a conversation", c.Count)
	}
	// What the cache of the release before held: no References.
	store.mu.Lock()
	m := store.messages[msgKey(a.ID, "INBOX")][1]
	m.References = nil
	store.messages[msgKey(a.ID, "INBOX")][1] = m
	store.mu.Unlock()
	if c := conversationOf(t, svc, a, "INBOX", "Tickets"); c.Count != 1 {
		t.Fatalf("without References the conversation has %d messages", c.Count)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if c := conversationOf(t, svc, a, "INBOX", "Tickets"); c.Count != 2 {
		t.Errorf("after a refresh the conversation has %d messages, want 2", c.Count)
	}
	checkNotSeen(t, f, "INBOX")
}

// Moving a conversation moves exactly the messages its row stands for,
// one command per folder, and the results bring them back.
func TestMoveConversationAndUndo(t *testing.T) {
	withoutMove := allCaps()
	delete(withoutMove, imap.CapMove)
	for _, tc := range []struct {
		name string
		caps imap.CapSet
	}{{"MOVE", allCaps()}, {"COPY", withoutMove}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _, f, a := threadService(t, tc.caps,
				append(in("INBOX", "span-1.eml", "gmail-1.eml", "span-3.eml"), in("Sent", "span-2.eml")...))
			c := conversationOf(t, svc, a, "INBOX", "Contract review")
			members, err := svc.CachedConversation(ctx, a.ID, "INBOX", c.Thread)
			if err != nil {
				t.Fatal(err)
			}
			moved, err := svc.MoveMessages(ctx, a.ID, refsOf(members), "Archive")
			if err != nil {
				t.Fatal(err)
			}
			if len(moved) != 2 {
				t.Fatalf("moved = %+v", moved)
			}
			for i, m := range moved {
				if m.Folder != "Archive" || m.From != "INBOX" || m.UID != members[i].UID || m.DestUID == 0 {
					t.Errorf("result %d = %+v", i, m)
				}
			}
			if got := f.subjects(t, "INBOX"); !slices.Equal(got, []string{"Venue for the offsite"}) {
				t.Errorf("server inbox = %q", got)
			}
			if got := f.subjects(t, "Archive"); !slices.Equal(got, []string{"Contract review", "Re: Contract review"}) {
				t.Errorf("server archive = %q", got)
			}
			if got := f.subjects(t, "Sent"); len(got) != 1 {
				t.Errorf("the move took the reply in Sent: %q", got)
			}
			if list, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); len(list) != 1 {
				t.Errorf("cached inbox = %q", subjects(list))
			}

			if err := svc.MoveBackMessages(ctx, a.ID, moved); err != nil {
				t.Fatal(err)
			}
			if got := f.subjects(t, "Archive"); len(got) != 0 {
				t.Errorf("archive after the undo = %q", got)
			}
			if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
				t.Fatal(err)
			}
			if back := conversationOf(t, svc, a, "INBOX", "Contract review"); back.Count != 2 {
				t.Errorf("after the undo the inbox row has %d messages", back.Count)
			}

			// The whole conversation, in two folders, goes and comes back.
			whole := conversationOf(t, svc, a, "", "Contract review")
			all, err := svc.CachedConversation(ctx, a.ID, "", whole.Thread)
			if err != nil {
				t.Fatal(err)
			}
			moved, err = svc.MoveMessages(ctx, a.ID, refsOf(all), "Trash")
			if err != nil {
				t.Fatal(err)
			}
			if len(moved) != 3 || len(f.subjects(t, "Trash")) != 3 || len(f.subjects(t, "Sent")) != 0 {
				t.Fatalf("moved %+v; trash %q", moved, f.subjects(t, "Trash"))
			}
			if err := svc.MoveBackMessages(ctx, a.ID, moved); err != nil {
				t.Fatal(err)
			}
			if inbox, sent := f.subjects(t, "INBOX"), f.subjects(t, "Sent"); len(inbox) != 3 || len(sent) != 1 {
				t.Errorf("after the undo: inbox %q, sent %q", inbox, sent)
			}
		})
	}
}

// On a server without UIDPLUS, a move's EXPUNGE removes the messages
// moved and leaves those another mail client flagged \Deleted, flagged.
func TestMoveSparesMailMarkedDeletedElsewhere(t *testing.T) {
	ctx := context.Background()
	caps := allCaps()
	delete(caps, imap.CapMove)
	delete(caps, imap.CapUIDPlus)
	svc, _, f, a := threadService(t, caps, in("INBOX", "span-1.eml", "same-subject-1.eml", "span-3.eml", "gmail-1.eml"))
	f.flag(t, "INBOX", 2, imap.FlagDeleted)
	c := conversationOf(t, svc, a, "INBOX", "Contract review")
	members, err := svc.CachedConversation(ctx, a.ID, "INBOX", c.Thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveMessages(ctx, a.ID, refsOf(members), "Archive"); err != nil {
		t.Fatal(err)
	}
	// A single message's move takes the same care.
	if _, err := svc.Move(ctx, a.ID, "INBOX", 4, "Trash"); err != nil {
		t.Fatal(err)
	}
	inbox := f.messages(t, "INBOX")
	if len(inbox) != 1 || inbox[0].Subject != "Lunch on Friday?" {
		t.Fatalf("server inbox = %+v; the moves expunged the message another client marked", inbox)
	}
	if !inbox[0].hasFlag(imap.FlagDeleted) {
		t.Errorf("the other client's message lost its \\Deleted flag: %v", inbox[0].Flags)
	}
	if got := f.subjects(t, "Archive"); len(got) != 2 {
		t.Errorf("archive = %q", got)
	}
}

// A move that fails leaves the messages where they were, in the cache as
// on the server; an undo without the destination UID says so.
func TestMoveConversationFailures(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := threadService(t, allCaps(), in("INBOX", "apple-1.eml", "apple-2.eml", "apple-3.eml"))
	c := conversationOf(t, svc, a, "INBOX", "Photos from the trip")
	members, err := svc.CachedConversation(ctx, a.ID, "INBOX", c.Thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveMessages(ctx, a.ID, refsOf(members), ""); err == nil {
		t.Error("a move without a destination succeeded")
	}
	moved, err := svc.MoveMessages(ctx, a.ID, refsOf(members), "Nowhere")
	if err == nil || len(moved) != 0 {
		t.Fatalf("a move to a missing folder = %+v, %v", moved, err)
	}
	if again := conversationOf(t, svc, a, "INBOX", "Photos from the trip"); again.Count != 3 {
		t.Errorf("after the failed move the cache shows %d of 3 messages", again.Count)
	}
	if got := f.subjects(t, "INBOX"); len(got) != 3 {
		t.Errorf("server inbox = %q", got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.MoveMessages(cancelled, a.ID, refsOf(members), "Archive"); err == nil {
		t.Error("a cancelled move succeeded")
	}
	if again := conversationOf(t, svc, a, "INBOX", "Photos from the trip"); again.Count != 3 {
		t.Errorf("after the cancelled move the cache shows %d of 3 messages", again.Count)
	}
	// Messages already in the destination stay where they are.
	if moved, err := svc.MoveMessages(ctx, a.ID, refsOf(members), "INBOX"); err != nil || len(moved) != 0 {
		t.Errorf("a move to the same folder = %+v, %v", moved, err)
	}

	moved, err = svc.MoveMessages(ctx, a.ID, refsOf(members), "Archive")
	if err != nil || len(moved) != 3 {
		t.Fatalf("moved = %+v, %v", moved, err)
	}
	moved[0].DestUID = 0
	if err := svc.MoveBackMessages(ctx, a.ID, moved); err == nil {
		t.Error("an undo without the destination UID reported no error")
	}
	if inbox, archive := f.subjects(t, "INBOX"), f.subjects(t, "Archive"); len(inbox) != 2 || len(archive) != 1 {
		t.Errorf("after a partial undo: inbox %q, archive %q", inbox, archive)
	}
}

package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// threadFixture parses a message of testdata/threads.
func threadFixture(t *testing.T, name string, uid mail.UID) mail.Message {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "threads", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	m.UID = uid
	return m
}

// headerOnly is what a sync writes of a message: no body.
func headerOnly(m mail.Message) mail.Message {
	m.BodyText, m.BodyHTML, m.Attachments, m.CachedAt = "", "", nil, time.Time{}
	return m
}

func addFolders(t *testing.T, s *Store, accountID string, names ...string) {
	t.Helper()
	var folders []mail.Folder
	for _, name := range names {
		folders = append(folders, mail.Folder{Name: name})
	}
	if err := s.SaveFolders(context.Background(), accountID, folders); err != nil {
		t.Fatal(err)
	}
}

// syncMessages writes messages as one folder sync.
func syncMessages(t *testing.T, s *Store, accountID, folder string, msgs ...mail.Message) {
	t.Helper()
	var last mail.UID
	for i := range msgs {
		msgs[i] = headerOnly(msgs[i])
		last = max(last, msgs[i].UID)
	}
	if err := s.ApplySync(context.Background(), accountID, mail.SyncBatch{
		Folder: mail.Folder{Name: folder, UIDValidity: 1, LastUID: last}, Messages: msgs,
	}); err != nil {
		t.Fatal(err)
	}
}

func threadOf(t *testing.T, s *Store, accountID, folder string, uid mail.UID) mail.ThreadID {
	t.Helper()
	m, ok, err := s.GetMessage(context.Background(), accountID, folder, uid)
	if err != nil || !ok {
		t.Fatalf("message %s/%d: %v, %v", folder, uid, ok, err)
	}
	if m.Thread == 0 {
		t.Fatalf("message %s/%d has no conversation", folder, uid)
	}
	return m.Thread
}

// checkThreadRows checks what must hold after any write: every message has
// a conversation that exists, every key names one, and the full-text
// index still matches the rows.
func checkThreadRows(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`SELECT COUNT(*) FROM messages WHERE thread_id IS NULL OR thread_id NOT IN (SELECT id FROM threads)`,
		`SELECT COUNT(*) FROM thread_refs WHERE thread_id NOT IN (SELECT id FROM threads)`,
		`SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id
			JOIN threads t ON t.id = m.thread_id WHERE t.account_id <> f.account_id`,
	} {
		rows, err := s.query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if n := number(rows.Values[0][0]); n != 0 {
			t.Errorf("%d rows break %s", n, q)
		}
	}
	if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("full-text index: %v", err)
	}
}

func countRows(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	rows, err := s.query(context.Background(), q, args...)
	if err != nil {
		t.Fatal(err)
	}
	return number(rows.Values[0][0])
}

var providerGroups = map[string][]string{
	"gmail":   {"gmail-1.eml", "gmail-2.eml", "gmail-3.eml"},
	"outlook": {"outlook-1.eml", "outlook-2.eml", "outlook-3.eml"},
	"apple":   {"apple-1.eml", "apple-2.eml", "apple-3.eml"},
}

// Each provider's three messages are one conversation, whether a sync
// brings them together or one by one in any order, and the three
// conversations stay apart.
func TestThreadsOfProviderFixtures(t *testing.T) {
	ctx := context.Background()
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		for _, together := range []bool{true, false} {
			t.Run(fmt.Sprintf("%v together=%v", order, together), func(t *testing.T) {
				s := openTest(t)
				addAccount(t, s, "a1")
				addFolders(t, s, "a1", "INBOX")
				threads := map[mail.ThreadID]string{}
				uid := mail.UID(0)
				for _, provider := range sortedKeys(providerGroups) {
					var msgs []mail.Message
					for _, i := range order {
						uid++
						msgs = append(msgs, threadFixture(t, providerGroups[provider][i], uid))
					}
					if together {
						syncMessages(t, s, "a1", "INBOX", msgs...)
					} else {
						for _, m := range msgs {
							syncMessages(t, s, "a1", "INBOX", m)
						}
					}
					thread := threadOf(t, s, "a1", "INBOX", msgs[0].UID)
					for _, m := range msgs[1:] {
						if got := threadOf(t, s, "a1", "INBOX", m.UID); got != thread {
							t.Errorf("%s: %q is in conversation %d, %q in %d", provider, m.Subject, got, msgs[0].Subject, thread)
						}
					}
					if other, ok := threads[thread]; ok {
						t.Errorf("%s shares conversation %d with %s", provider, thread, other)
					}
					threads[thread] = provider
				}
				list, err := s.ListConversations(ctx, "a1", "INBOX", "", 50)
				if err != nil {
					t.Fatal(err)
				}
				if len(list) != 3 {
					t.Fatalf("conversations = %d, want 3", len(list))
				}
				for _, c := range list {
					if c.Count != 3 {
						t.Errorf("%s: count = %d, want 3", threads[c.Thread], c.Count)
					}
				}
				checkThreadRows(t, s)
			})
		}
	}
}

// A message that links two conversations makes them one, under the lower
// ID, whatever the order the messages come in and whatever path writes
// them. The other ID's keys and row go with it.
func TestThreadsMergeInEveryOrder(t *testing.T) {
	ctx := context.Background()
	chain := []mail.Message{
		{Summary: mail.Summary{UID: 1, MessageID: "a@example.com", Subject: "Plan"}},
		{Summary: mail.Summary{UID: 2, MessageID: "b@example.com", InReplyTo: "a@example.com", Subject: "Re: Plan"}},
		{Summary: mail.Summary{UID: 3, MessageID: "c@example.com", InReplyTo: "b@example.com", Subject: "Re: Plan"}},
	}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			s := openTest(t)
			addAccount(t, s, "a1")
			addFolders(t, s, "a1", "INBOX")
			for n, i := range order {
				m := chain[i]
				if n%2 == 1 {
					if err := s.SaveMessage(ctx, "a1", "INBOX", m); err != nil {
						t.Fatal(err)
					}
				} else {
					syncMessages(t, s, "a1", "INBOX", m)
				}
			}
			want := threadOf(t, s, "a1", "INBOX", 1)
			for _, uid := range []mail.UID{2, 3} {
				if got := threadOf(t, s, "a1", "INBOX", uid); got != want {
					t.Errorf("message %d in %d, want %d", uid, got, want)
				}
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM threads`); got != 1 {
				t.Errorf("threads = %d, want 1", got)
			}
			if got := countRows(t, s, `SELECT MIN(id) FROM threads`); got != 1 {
				t.Errorf("the conversation is %d, want the lowest ID, 1", got)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM thread_refs WHERE thread_id = ?`, int64(want)); got != 3 {
				t.Errorf("keys of the conversation = %d, want 3", got)
			}
			checkThreadRows(t, s)
		})
	}
	// In one batch, a later message can merge what earlier ones began.
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	syncMessages(t, s, "a1", "INBOX", chain[2], chain[0], chain[1])
	if a, c := threadOf(t, s, "a1", "INBOX", 1), threadOf(t, s, "a1", "INBOX", 3); a != c {
		t.Errorf("one batch left two conversations: %d and %d", a, c)
	}
	checkThreadRows(t, s)
}

// Messages that share only a subject are never one conversation: not with
// Re:, RE: or the same text, and not a message without any headers.
func TestSameSubjectIsNotMerged(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	msgs := []mail.Message{
		threadFixture(t, "same-subject-1.eml", 1),
		threadFixture(t, "same-subject-2.eml", 2),
		threadFixture(t, "no-message-id.eml", 3),
		{Summary: mail.Summary{UID: 4, Subject: "Lunch on Friday?"}},
		{Summary: mail.Summary{UID: 5, Subject: "Re: Re: Lunch on Friday?"}},
		{Summary: mail.Summary{UID: 6, Subject: "Re: Lunch on Friday?", MessageID: "lunch-c@example.com"}},
	}
	syncMessages(t, s, "a1", "INBOX", msgs...)
	seen := map[mail.ThreadID]mail.UID{}
	for _, m := range msgs {
		thread := threadOf(t, s, "a1", "INBOX", m.UID)
		if other, ok := seen[thread]; ok {
			t.Errorf("messages %d and %d (%q) were merged", other, m.UID, m.Subject)
		}
		seen[thread] = m.UID
	}
	list, err := s.ListConversations(ctx, "a1", "INBOX", "", 50)
	if err != nil || len(list) != len(msgs) {
		t.Fatalf("conversations = %d, %v; want %d", len(list), err, len(msgs))
	}
	// A message without keys keeps its conversation when it is synced
	// again, and its body fetched.
	before := threadOf(t, s, "a1", "INBOX", 3)
	syncMessages(t, s, "a1", "INBOX", threadFixture(t, "no-message-id.eml", 3))
	if err := s.SaveMessage(ctx, "a1", "INBOX", threadFixture(t, "no-message-id.eml", 3)); err != nil {
		t.Fatal(err)
	}
	if after := threadOf(t, s, "a1", "INBOX", 3); after != before {
		t.Errorf("writing it again moved it from %d to %d", before, after)
	}
	checkThreadRows(t, s)
}

// Outlook's Thread-Index puts a reply with neither References nor
// In-Reply-To with its conversation; a malformed one puts nothing.
func TestThreadIndexJoinsAReplyWithoutReferences(t *testing.T) {
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	first := threadFixture(t, "outlook-1.eml", 1)
	reply := mail.Message{Summary: mail.Summary{UID: 2, MessageID: "lone@example.org", Subject: "RE: Quarterly numbers",
		ThreadIndex: "AdpPK5wxfA5aHZOyT2qOPBXXqStOYAAASxwg"}}
	stranger := mail.Message{Summary: mail.Summary{UID: 3, MessageID: "stranger@example.org", Subject: "RE: Quarterly numbers",
		ThreadIndex: "AdpPK5wxfA5aHZOyT2qOPBXXqStO"}}
	syncMessages(t, s, "a1", "INBOX", reply, stranger)
	syncMessages(t, s, "a1", "INBOX", first)
	if a, b := threadOf(t, s, "a1", "INBOX", 1), threadOf(t, s, "a1", "INBOX", 2); a != b {
		t.Errorf("the reply sharing the Thread-Index is in %d, the first message in %d", b, a)
	}
	if threadOf(t, s, "a1", "INBOX", 3) == threadOf(t, s, "a1", "INBOX", 1) {
		t.Error("a short Thread-Index joined the conversation")
	}
	m, _, _ := s.GetMessage(context.Background(), "a1", "INBOX", 1)
	if m.ThreadIndex != first.ThreadIndex {
		t.Errorf("stored Thread-Index = %q", m.ThreadIndex)
	}
	checkThreadRows(t, s)
}

// A broken References header contributes its valid ids and nothing else,
// and a reply that came before its original finds it.
func TestBrokenHeadersAndEarlyReplies(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	syncMessages(t, s, "a1", "INBOX", threadFixture(t, "broken-references.eml", 1), threadFixture(t, "early-reply.eml", 2))
	var keys []string
	rows, err := s.query(ctx, `SELECT key FROM thread_refs ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Values {
		keys = append(keys, text(r[0]))
	}
	want := []string{"broken-refs.4410@example.net", "broken-root.4400@example.com",
		"early-reply.7731@example.org", "late-original.7730@example.com"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %q, want %q", keys, want)
	}
	if threadOf(t, s, "a1", "INBOX", 1) == threadOf(t, s, "a1", "INBOX", 2) {
		t.Fatal("unrelated messages were merged")
	}
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 3, MessageID: "broken-root.4400@example.com", Subject: "Server move"}},
		threadFixture(t, "late-original.eml", 4))
	if threadOf(t, s, "a1", "INBOX", 3) != threadOf(t, s, "a1", "INBOX", 1) {
		t.Error("the broken reply did not join its original")
	}
	if threadOf(t, s, "a1", "INBOX", 4) != threadOf(t, s, "a1", "INBOX", 2) {
		t.Error("the original that came late did not join its reply")
	}
	checkThreadRows(t, s)
}

// A conversation's ID is never given to another: not after it merged into
// a lower one, and not after its account was deleted.
func TestThreadIDsAreNeverReused(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, id := range []string{"a1", "a2"} {
		addAccount(t, s, id)
		addFolders(t, s, id, "INBOX")
	}
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "x@example.com"}},
		mail.Message{Summary: mail.Summary{UID: 2, MessageID: "y@example.com"}})
	merged := threadOf(t, s, "a1", "INBOX", 2)
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 3, MessageID: "z@example.com",
		References: []string{"x@example.com", "y@example.com"}}})
	if threadOf(t, s, "a1", "INBOX", 2) == merged {
		t.Fatalf("the merge kept the higher ID %d", merged)
	}
	syncMessages(t, s, "a2", "INBOX", mail.Message{Summary: mail.Summary{UID: 1, MessageID: "w@example.com"}})
	fresh := threadOf(t, s, "a2", "INBOX", 1)
	if fresh <= merged {
		t.Errorf("new conversation %d reuses an ID up to %d", fresh, merged)
	}
	if err := s.DeleteAccount(ctx, "a2"); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 4, MessageID: "v@example.com"}})
	if got := threadOf(t, s, "a1", "INBOX", 4); got <= fresh {
		t.Errorf("after a delete, new conversation %d reuses an ID up to %d", got, fresh)
	}
	checkThreadRows(t, s)
}

// The conversations and keys of an account go with it; another account's,
// even with the same Message-IDs, are its own and stay.
func TestThreadsCascadeOnAccountDelete(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, id := range []string{"a1", "a2"} {
		addAccount(t, s, id)
		addFolders(t, s, id, "INBOX")
		syncMessages(t, s, id, "INBOX", threadFixture(t, "apple-1.eml", 1), threadFixture(t, "apple-2.eml", 2))
	}
	if threadOf(t, s, "a1", "INBOX", 1) == threadOf(t, s, "a2", "INBOX", 1) {
		t.Fatal("two accounts share a conversation")
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"threads", "thread_refs"} {
		if got := countRows(t, s, `SELECT COUNT(*) FROM `+table+` WHERE account_id = 'a1'`); got != 0 {
			t.Errorf("%s rows of the deleted account = %d", table, got)
		}
		if got := countRows(t, s, `SELECT COUNT(*) FROM `+table+` WHERE account_id = 'a2'`); got == 0 {
			t.Errorf("the other account lost its %s rows", table)
		}
	}
	checkThreadRows(t, s)
}

// A sync that reads cached messages' headers again threads them again
// when it learns something: References a cache from before conversations
// lacks. What it did not get, it does not erase.
func TestSyncRefreshRethreadsCachedRows(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	// As the envelope alone gave them: no References.
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "root@example.com"}},
		mail.Message{Summary: mail.Summary{UID: 2, MessageID: "third@example.com", InReplyTo: "second@example.com"}})
	if threadOf(t, s, "a1", "INBOX", 1) == threadOf(t, s, "a1", "INBOX", 2) {
		t.Fatal("merged before the References were known")
	}
	refresh := func(h ...mail.ThreadHeaders) {
		t.Helper()
		if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
			Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2}, Threading: h,
		}); err != nil {
			t.Fatal(err)
		}
	}
	refresh(mail.ThreadHeaders{UID: 2, MessageID: "third@example.com", InReplyTo: "second@example.com",
		References: []string{"root@example.com", "second@example.com"}},
		mail.ThreadHeaders{UID: 9, MessageID: "uncached@example.com", References: []string{"root@example.com"}})
	if threadOf(t, s, "a1", "INBOX", 1) != threadOf(t, s, "a1", "INBOX", 2) {
		t.Error("the References read again did not join the conversations")
	}
	m, _, _ := s.GetMessage(ctx, "a1", "INBOX", 2)
	if !slices.Equal(m.References, []string{"root@example.com", "second@example.com"}) {
		t.Errorf("references = %q", m.References)
	}
	if _, ok, _ := s.GetMessage(ctx, "a1", "INBOX", 9); ok {
		t.Error("a refresh created a row for a message the cache does not have")
	}
	// A refresh without the headers, or with the same, changes nothing.
	refresh(mail.ThreadHeaders{UID: 2, MessageID: "third@example.com"})
	m, _, _ = s.GetMessage(ctx, "a1", "INBOX", 2)
	if len(m.References) != 2 {
		t.Errorf("a refresh without References erased them: %q", m.References)
	}
	checkThreadRows(t, s)
}

// The conversation list of a folder, of the starred messages and of the
// whole account, each made of the messages in its scope.
func TestConversationListScopes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Sent", "Archive")
	conversationFixtures(t, s)
	span := threadOf(t, s, "a1", "INBOX", 1)
	gmail := threadOf(t, s, "a1", "INBOX", 3)
	apple := threadOf(t, s, "a1", "Archive", 1)

	type row struct {
		thread                   mail.ThreadID
		count                    int
		latest                   string
		unread, flagged, attachs bool
	}
	list := func(folder string, limit int) []row {
		t.Helper()
		got, err := s.ListConversations(ctx, "a1", folder, "", limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []row
		for _, c := range got {
			if c.Thread != c.Latest.Thread {
				t.Errorf("row %d has a latest message of %d", c.Thread, c.Latest.Thread)
			}
			out = append(out, row{c.Thread, c.Count, c.Latest.Folder + "/" + c.Latest.Subject + "/" + c.Latest.From.Email,
				c.Unread, c.Flagged, c.HasAttachments})
		}
		return out
	}
	lunch := threadOf(t, s, "a1", "INBOX", 6)
	for _, tc := range []struct {
		folder string
		want   []row
	}{
		{"INBOX", []row{
			{span, 2, "INBOX/Re: Contract review/bob@example.org", true, false, false},
			{lunch, 1, "INBOX/Lunch on Friday?/dana@example.org", true, false, false},
			{gmail, 3, "INBOX/Re: Venue for the offsite/lin.wei@example.com", false, true, true},
		}},
		{"Sent", []row{{span, 1, "Sent/Re: Contract review/ann@example.com", false, false, false}}},
		{"starred", []row{
			{apple, 1, "Archive/Photos from the trip/mateo@example.net", false, true, false},
			{gmail, 1, "INBOX/Venue for the offsite/lin.wei@example.com", false, true, false},
		}},
		{"", []row{
			{span, 3, "INBOX/Re: Contract review/bob@example.org", true, false, false},
			{lunch, 1, "INBOX/Lunch on Friday?/dana@example.org", true, false, false},
			{apple, 1, "Archive/Photos from the trip/mateo@example.net", false, true, false},
			{gmail, 3, "INBOX/Re: Venue for the offsite/lin.wei@example.com", false, true, true},
		}},
	} {
		if got := list(tc.folder, 50); !slices.Equal(got, tc.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.folder, got, tc.want)
		}
	}
	if got := list("INBOX", 2); len(got) != 2 || got[1].thread != lunch {
		t.Errorf("limit 2 = %+v", got)
	}
	if got := list("Junk", 50); len(got) != 0 {
		t.Errorf("an empty folder lists %+v", got)
	}

	// A tombstone is no part of any scope.
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, true); err != nil {
		t.Fatal(err)
	}
	if got := list("INBOX", 50); got[0] != (row{span, 1, "INBOX/Contract review/bob@example.org", false, false, false}) {
		t.Errorf("with a tombstone, the first row = %+v", got[0])
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, false); err != nil {
		t.Fatal(err)
	}

	// The conversation reads across folders, oldest first; in a scope it
	// is the messages its row stands for.
	members := func(folder string, thread mail.ThreadID) []string {
		t.Helper()
		got, err := s.ConversationMessages(ctx, "a1", folder, thread)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range got {
			if m.Thread != thread {
				t.Errorf("%s/%d is in %d, not %d", m.Folder, m.UID, m.Thread, thread)
			}
			out = append(out, fmt.Sprintf("%s/%d", m.Folder, m.UID))
		}
		return out
	}
	for _, tc := range []struct {
		folder string
		thread mail.ThreadID
		want   []string
	}{
		{"", span, []string{"INBOX/1", "Sent/1", "INBOX/2"}},
		{"INBOX", span, []string{"INBOX/1", "INBOX/2"}},
		{"Sent", span, []string{"Sent/1"}},
		{"starred", span, nil},
		{"starred", gmail, []string{"INBOX/3"}},
		{"", gmail, []string{"INBOX/3", "INBOX/4", "INBOX/5"}},
	} {
		if got := members(tc.folder, tc.thread); !slices.Equal(got, tc.want) {
			t.Errorf("messages of %d in %q = %v, want %v", tc.thread, tc.folder, got, tc.want)
		}
	}
	if got, _ := s.ConversationMessages(ctx, "other", "", span); len(got) != 0 {
		t.Errorf("another account reads the conversation: %+v", got)
	}
	if err := s.MarkDeleted(ctx, "a1", "Sent", 1, true); err != nil {
		t.Fatal(err)
	}
	if got := members("", span); !slices.Equal(got, []string{"INBOX/1", "INBOX/2"}) {
		t.Errorf("with a tombstone the conversation reads %v", got)
	}
	checkThreadRows(t, s)
}

// conversationFixtures fills an account: a conversation across Inbox and
// Sent, Gmail's in the inbox with a flag and an attachment, Apple's
// first message flagged in Archive, and a lone unread message.
func conversationFixtures(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	seen := func(m mail.Message, flagged bool) mail.Message {
		m.Seen, m.Flagged = true, flagged
		return m
	}
	syncMessages(t, s, "a1", "INBOX",
		seen(threadFixture(t, "span-1.eml", 1), false),
		threadFixture(t, "span-3.eml", 2),
		seen(threadFixture(t, "gmail-1.eml", 3), true),
		seen(threadFixture(t, "gmail-2.eml", 4), false),
		seen(threadFixture(t, "gmail-3.eml", 5), false),
		threadFixture(t, "same-subject-1.eml", 6))
	syncMessages(t, s, "a1", "Archive", seen(threadFixture(t, "apple-1.eml", 1), true))
	// Bodies, as fetched: the reply in Sent and Gmail's with the quote.
	if err := s.SaveMessage(ctx, "a1", "Sent", seen(threadFixture(t, "span-2.eml", 1), false)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", seen(threadFixture(t, "gmail-3.eml", 5), false)); err != nil {
		t.Fatal(err)
	}
}

// A search finds a conversation by any of its messages, in any folder,
// and says which matched.
func TestConversationSearch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Sent", "Archive")
	conversationFixtures(t, s)
	// Two characters take the LIKE fallback.
	if err := s.SaveMessage(ctx, "a1", "Archive", mail.Message{Summary: mail.Summary{UID: 2,
		MessageID: "april@example.com", Subject: "Plans", Date: time.Unix(1_700_000_000, 0)}, BodyText: "四月见"}); err != nil {
		t.Fatal(err)
	}
	span := threadOf(t, s, "a1", "INBOX", 1)
	gmail := threadOf(t, s, "a1", "INBOX", 3)
	for _, tc := range []struct {
		folder, query string
		thread        mail.ThreadID
		count         int
		matches       []mail.MessageRef
	}{
		// The body of the reply in Sent finds the conversation in the inbox.
		{"INBOX", "indemnity", span, 2, []mail.MessageRef{{Folder: "Sent", UID: 1}}},
		// The sender of the inbox messages finds it in Sent.
		{"Sent", "bob@example.org", span, 1, []mail.MessageRef{{Folder: "INBOX", UID: 2}, {Folder: "INBOX", UID: 1}}},
		{"", "lakeside", gmail, 3, []mail.MessageRef{{Folder: "INBOX", UID: 5}}},
		{"", "四月", threadOf(t, s, "a1", "Archive", 2), 1, []mail.MessageRef{{Folder: "Archive", UID: 2}}},
	} {
		got, err := s.ListConversations(ctx, "a1", tc.folder, tc.query, 50)
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if len(got) != 1 || got[0].Thread != tc.thread || got[0].Count != tc.count || !slices.Equal(got[0].Matches, tc.matches) {
			t.Errorf("search %q in %q = %+v, want conversation %d of %d with matches %v", tc.query, tc.folder, got, tc.thread, tc.count, tc.matches)
		}
	}
	// A match outside the scope's conversations finds nothing there.
	if got, _ := s.ListConversations(ctx, "a1", "Sent", "lakeside", 50); len(got) != 0 {
		t.Errorf("search in Sent found %+v", got)
	}
	// Without a search, no row has matches.
	if got, _ := s.ListConversations(ctx, "a1", "", "", 50); len(got) == 0 || got[0].Matches != nil {
		t.Errorf("a list that is no search = %+v", got)
	}
}

// Many writes at once of messages of one conversation, each linked only
// to the one before it and arriving in any order, by every path, end as
// one conversation.
func TestConcurrentWritesMakeOneConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Sent")
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := mail.Message{Summary: mail.Summary{UID: mail.UID(i + 1), MessageID: fmt.Sprintf("m%d@example.com", i)}}
			if i > 0 {
				m.InReplyTo = fmt.Sprintf("m%d@example.com", i-1)
			}
			var err error
			switch i % 3 {
			case 0:
				err = s.ApplySync(ctx, "a1", mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 1}, Messages: []mail.Message{m}})
			case 1:
				err = s.ApplySync(ctx, "a1", mail.SyncBatch{Folder: mail.Folder{Name: "Sent", UIDValidity: 1}, Messages: []mail.Message{m}})
			default:
				err = s.SaveMessage(ctx, "a1", "INBOX", m)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages`); got != n {
		t.Fatalf("messages = %d, want %d", got, n)
	}
	if got := countRows(t, s, `SELECT COUNT(DISTINCT thread_id) FROM messages`); got != 1 {
		t.Errorf("conversations = %d, want 1", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM threads`); got != 1 {
		t.Errorf("thread rows = %d, want 1", got)
	}
	checkThreadRows(t, s)
}

// A database of the release before, with mail in it, gains conversations
// for every row from what it holds, per account.
func TestMigrationThreadsAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 5; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls'),
			('a2', 'Bo', 'bo@example.com', 'bo', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name) VALUES (1, 'a1', 'INBOX'), (2, 'a1', 'Sent'), (3, 'a2', 'INBOX')`,
		// A conversation across folders, by In-Reply-To and by the
		// References of a fetched body; the third reply comes first.
		`INSERT INTO messages (id, folder_id, server_uid, message_id, in_reply_to, references_json, subject, body_text)
			VALUES (1, 1, 1, 'r3@x.org', 'r2@x.org', NULL, 'Re: Plan', NULL),
			(2, 1, 2, 'root@x.org', NULL, NULL, 'Plan', 'the plan'),
			(3, 2, 1, 'r1@x.org', 'root@x.org', NULL, 'Re: Plan', NULL),
			(4, 1, 3, 'r2@x.org', NULL, '["root@x.org","r1@x.org"]', 'Re: Plan', 'agreed'),
			(5, 1, 4, 'other@x.org', NULL, NULL, 'Re: Plan', NULL),
			(6, 1, 5, NULL, NULL, NULL, 'Plan', NULL),
			(7, 3, 1, 'root@x.org', NULL, NULL, 'Plan', NULL)`,
		"PRAGMA user_version=5",
	} {
		if _, err := db.Execute(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	plan := threadOf(t, s, "a1", "INBOX", 2)
	for _, m := range []struct {
		folder string
		uid    mail.UID
	}{{"INBOX", 1}, {"Sent", 1}, {"INBOX", 3}} {
		if got := threadOf(t, s, "a1", m.folder, m.uid); got != plan {
			t.Errorf("%s/%d is in %d, want %d", m.folder, m.uid, got, plan)
		}
	}
	other, lone, elsewhere := threadOf(t, s, "a1", "INBOX", 4), threadOf(t, s, "a1", "INBOX", 5), threadOf(t, s, "a2", "INBOX", 1)
	if len(map[mail.ThreadID]bool{plan: true, other: true, lone: true, elsewhere: true}) != 4 {
		t.Errorf("conversations %d, %d, %d, %d are not all apart", plan, other, lone, elsewhere)
	}
	list, err := s.ListConversations(ctx, "a1", "", "", 50)
	if err != nil || len(list) != 3 {
		t.Fatalf("conversations = %+v, %v", list, err)
	}
	checkThreadRows(t, s)

	// The index follows the rows still, and a write after the upgrade
	// takes a new ID and joins by the keys of the old rows.
	if got, _ := s.ListMessages(ctx, "a1", "", "agreed", 10); len(got) != 1 {
		t.Errorf("search after the upgrade = %+v", got)
	}
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 9, MessageID: "r4@x.org", InReplyTo: "r3@x.org"}},
		mail.Message{Summary: mail.Summary{UID: 10, MessageID: "new@x.org"}})
	if got := threadOf(t, s, "a1", "INBOX", 9); got != plan {
		t.Errorf("a new reply is in %d, want %d", got, plan)
	}
	if got := threadOf(t, s, "a1", "INBOX", 10); got <= max(plan, other, lone, elsewhere) {
		t.Errorf("a new conversation took ID %d", got)
	}
	// No path writes a message without a conversation.
	if _, err := s.exec(ctx, `INSERT INTO messages (folder_id, server_uid) VALUES (1, 99)`); err == nil {
		t.Error("a message was written without a conversation")
	}
	if _, err := s.exec(ctx, `UPDATE messages SET thread_id = NULL WHERE id = 1`); err == nil {
		t.Error("a message lost its conversation")
	}
	checkThreadRows(t, s)
}

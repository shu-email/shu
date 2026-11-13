package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"shu/mail"
)

var aggregateBase = time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)

// aggregateFixture is one conversation of three messages across two
// folders and a second conversation elsewhere:
//
//	INBOX/1  bob   root@example.com   unread, 09:01
//	INBOX/2  bob   reply@example.com  answering 1, unread, attachment, 09:02
//	Sent/1   ann   out@example.com    answering 2, seen, 09:03
//	Archive/1 carol other@example.com 08:59
func aggregateFixture(t *testing.T, s *Store) (thread, other mail.ThreadID) {
	t.Helper()
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Sent", "Archive")
	bob := mail.Address{Name: "Bob", Email: "bob@example.org"}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	carol := mail.Address{Name: "Carol", Email: "carol@example.net"}
	day := func(minute int) time.Time { return aggregateBase.Add(time.Duration(minute) * time.Minute) }
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "root@example.com", From: bob, To: []mail.Address{ann},
			Subject: "Plan", Preview: "Shall we meet?", Date: day(1)}},
		mail.Message{Summary: mail.Summary{UID: 2, MessageID: "reply@example.com", InReplyTo: "root@example.com",
			From: bob, To: []mail.Address{ann}, Subject: "Re: Plan", Preview: "Attached the draft",
			Date: day(2), HasAttachments: true}},
	)
	syncMessages(t, s, "a1", "Sent",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "out@example.com", InReplyTo: "reply@example.com",
			From: ann, To: []mail.Address{bob}, Subject: "Re: Plan", Preview: "Looks good",
			Date: day(3), Seen: true}},
	)
	syncMessages(t, s, "a1", "Archive",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "other@example.com", From: carol,
			Subject: "Other", Date: day(-1), Seen: true}},
	)
	return threadOf(t, s, "a1", "INBOX", 1), threadOf(t, s, "a1", "Archive", 1)
}

func threadRow(t *testing.T, s *Store, thread mail.ThreadID) []string {
	t.Helper()
	rows, err := s.query(context.Background(), `SELECT message_count, unread_count, starred_count,
			attachment_count, last_message_at, snippet, participants_json
		FROM threads WHERE id = ?`, int64(thread))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 {
		t.Fatalf("thread %d has %d rows", thread, len(rows.Values))
	}
	out := make([]string, len(rows.Values[0]))
	for i, v := range rows.Values[0] {
		out[i] = text(v)
	}
	return out
}

func folderAggregate(t *testing.T, s *Store, thread mail.ThreadID, folder string) ([]int64, bool) {
	t.Helper()
	rows, err := s.query(context.Background(), `SELECT tf.message_count, tf.unread_count,
			tf.starred_count, tf.attachment_count
		FROM thread_folders tf JOIN folders f ON f.id = tf.folder_id
		WHERE tf.thread_id = ? AND f.name = ?`, int64(thread), folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) == 0 {
		return nil, false
	}
	out := make([]int64, len(rows.Values[0]))
	for i, v := range rows.Values[0] {
		out[i] = number(v)
	}
	return out, true
}

func aggregateSnapshot(t *testing.T, s *Store) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	rows, err := s.query(ctx, `SELECT id, last_message_at, message_count, unread_count, starred_count,
			attachment_count, snippet, participants_json FROM threads ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Values {
		out = append(out, fmt.Sprint(append([]any{"thread"}, r...)...))
	}
	rows, err = s.query(ctx, `SELECT thread_id, folder_id, last_message_at,
			message_count, unread_count, starred_count, attachment_count
		FROM thread_folders ORDER BY thread_id, folder_id`)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Values {
		out = append(out, fmt.Sprint(append([]any{"folder"}, r...)...))
	}
	return out
}

// The aggregates of a conversation follow every write of its messages:
// flags, attachments, hiding one while it moves, deleting it, and a merge.
func TestThreadAggregatesFollowMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	thread, other := aggregateFixture(t, s)

	unread := func() int { return int(number(threadRow(t, s, thread)[1])) }
	row := threadRow(t, s, thread)
	if row[0] != "3" || row[1] != "2" || row[2] != "0" || row[3] != "1" {
		t.Errorf("conversation = count %s, unread %s, starred %s, attachments %s; want 3, 2, 0, 1", row[0], row[1], row[2], row[3])
	}
	if row[4] != fmt.Sprint(aggregateBase.Add(3*time.Minute).Unix()) {
		t.Errorf("last_message_at = %s, want the newest message", row[4])
	}
	if row[5] != "Looks good" {
		t.Errorf("snippet = %q, want the newest message's preview", row[5])
	}
	if !strings.Contains(row[6], "bob@example.org") || !strings.Contains(row[6], "ann@example.com") {
		t.Errorf("participants = %s, want both senders", row[6])
	}
	if got, ok := folderAggregate(t, s, thread, "INBOX"); !ok || !slicesEqualInts(got, []int64{2, 2, 0, 1}) {
		t.Errorf("INBOX aggregate = %v, %v; want 2 messages, 2 unread, 1 attachment", got, ok)
	}
	if got, ok := folderAggregate(t, s, thread, "Sent"); !ok || !slicesEqualInts(got, []int64{1, 0, 0, 0}) {
		t.Errorf("Sent aggregate = %v, %v", got, ok)
	}

	// A flag changes the counts it belongs to, not the others.
	if err := s.SetFlag(ctx, "a1", "INBOX", 2, mail.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	if got := unread(); got != 1 {
		t.Errorf("unread = %d after one of two messages was read", got)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 1, mail.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if got := threadRow(t, s, thread)[2]; got != "1" {
		t.Errorf("starred = %s after a message was starred", got)
	}
	if got, _ := folderAggregate(t, s, thread, "INBOX"); got[2] != 1 {
		t.Errorf("INBOX starred = %d after a message was starred", got[2])
	}

	// A message hidden while it moves counts nowhere until it is back.
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, true); err != nil {
		t.Fatal(err)
	}
	if got := threadRow(t, s, thread)[0]; got != "2" {
		t.Errorf("count = %s with a message hidden", got)
	}
	if got, _ := folderAggregate(t, s, thread, "INBOX"); got[0] != 1 || got[3] != 0 {
		t.Errorf("INBOX aggregate with a message hidden = %v", got)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, false); err != nil {
		t.Fatal(err)
	}
	if got := threadRow(t, s, thread)[0]; got != "3" {
		t.Errorf("count = %s after the message came back", got)
	}

	// Deleting a message takes it from its folder's aggregate too.
	if err := s.DeleteMessage(ctx, "a1", "Sent", 1); err != nil {
		t.Fatal(err)
	}
	if got := threadRow(t, s, thread)[0]; got != "2" {
		t.Errorf("count = %s after a message was deleted", got)
	}
	if _, ok := folderAggregate(t, s, thread, "Sent"); ok {
		t.Error("Sent kept an aggregate with no message")
	}

	// The other conversation is untouched, and a rebuild reproduces
	// everything the writes kept.
	if got := threadRow(t, s, other)[0]; got != "1" {
		t.Errorf("the other conversation's count = %s", got)
	}
	before := aggregateSnapshot(t, s)
	if err := s.RebuildThreadAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	if after := aggregateSnapshot(t, s); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("a rebuild changed the aggregates:\n before %v\n after %v", before, after)
	}
}

func slicesEqualInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A message that links two conversations merges their aggregates into the
// one that remains.
func TestThreadAggregatesMerge(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "one@example.com", Date: aggregateBase}},
		mail.Message{Summary: mail.Summary{UID: 2, MessageID: "two@example.com", Date: aggregateBase.Add(time.Minute)}},
	)
	first, second := threadOf(t, s, "a1", "INBOX", 1), threadOf(t, s, "a1", "INBOX", 2)
	if first == second {
		t.Fatal("the messages began in one conversation")
	}
	// The third links both, which makes the lower ID the conversation.
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 3, MessageID: "three@example.com",
		References: []string{"one@example.com", "two@example.com"}, Date: aggregateBase.Add(2 * time.Minute)}})
	merged := min(first, second)
	if got := threadRow(t, s, merged)[0]; got != "3" {
		t.Errorf("merged count = %s, want 3", got)
	}
	if rows, err := s.query(ctx, `SELECT COUNT(*) FROM threads WHERE id = ?`, int64(max(first, second))); err != nil || number(rows.Values[0][0]) != 0 {
		t.Errorf("the merged-away conversation kept a row: %v, %v", rows.Values, err)
	}
	if rows, err := s.query(ctx, `SELECT COUNT(*) FROM thread_folders WHERE thread_id = ?`, int64(max(first, second))); err != nil || number(rows.Values[0][0]) != 0 {
		t.Errorf("the merged-away conversation kept a folder aggregate: %v, %v", rows.Values, err)
	}
	before := aggregateSnapshot(t, s)
	if err := s.RebuildThreadAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	if after := aggregateSnapshot(t, s); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("a rebuild changed the merged aggregates:\n before %v\n after %v", before, after)
	}
}

// A list's counts are the messages of its scope, whichever aggregate
// answers it: the conversation's own for the whole account, one row per
// folder for a folder, the messages for a flag.
func TestConversationCountsMatchMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	aggregateFixture(t, s)
	reference := func(scope mail.Scope) map[mail.ThreadID][4]int {
		t.Helper()
		p, err := s.plan(ctx, mail.ListQuery{Scope: scope}, nil)
		if err != nil {
			t.Fatal(err)
		}
		cond, args := p.inScope("m", p.folders, false)
		rows, err := s.query(ctx, `SELECT m.thread_id, COUNT(*), MAX(m.seen = 0), MAX(m.flagged), MAX(m.has_attachments)
			FROM messages m WHERE `+cond+` AND m.deleted = 0 GROUP BY m.thread_id`, args...)
		if err != nil {
			t.Fatal(err)
		}
		out := map[mail.ThreadID][4]int{}
		for _, r := range rows.Values {
			out[mail.ThreadID(number(r[0]))] = [4]int{int(number(r[1])), btoi(truth(r[2])), btoi(truth(r[3])), btoi(truth(r[4]))}
		}
		return out
	}
	for _, scope := range []mail.Scope{
		mail.AccountScope("a1", "INBOX"),
		mail.AccountScope("a1", "Sent"),
		mail.AccountScope("a1", ""),
		{Accounts: []string{"a1"}, Starred: true},
	} {
		want := reference(scope)
		page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: scope})
		if err != nil {
			t.Fatalf("%+v: %v", scope, err)
		}
		if len(page.Conversations) != len(want) {
			t.Errorf("%+v: %d conversations, want %d", scope, len(page.Conversations), len(want))
		}
		for _, c := range page.Conversations {
			got := [4]int{c.Count, btoi(c.Unread), btoi(c.Flagged), btoi(c.HasAttachments)}
			if got != want[c.Thread] {
				t.Errorf("%+v: conversation %d = %v, want %v", scope, c.Thread, got, want[c.Thread])
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"shu/mail"
)

var pageBase = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// pageFixture fills an account with n messages across Inbox and Archive:
// every seventh answers the one before it, every tenth has the date of
// the one before it, and every 250th has no date at all.
func pageFixture(t *testing.T, s *Store, n int) {
	t.Helper()
	addFolders(t, s, "a1", "INBOX", "Archive")
	var inbox, archive []mail.Message
	for i := 1; i <= n; i++ {
		m := mail.Message{Summary: mail.Summary{
			MessageID: fmt.Sprintf("m%d@example.com", i), Subject: fmt.Sprintf("Message %d", i),
			Date: pageBase.Add(time.Duration(i) * time.Minute),
		}}
		if i%10 == 0 {
			m.Date = pageBase.Add(time.Duration(i-1) * time.Minute)
		}
		if i%7 == 0 {
			m.InReplyTo = fmt.Sprintf("m%d@example.com", i-1)
		}
		if i%250 == 0 {
			m.Date = time.Time{}
		}
		if i%3 == 0 {
			m.UID = mail.UID(len(archive) + 1)
			archive = append(archive, m)
		} else {
			m.UID = mail.UID(len(inbox) + 1)
			inbox = append(inbox, m)
		}
	}
	syncMessages(t, s, "a1", "INBOX", inbox...)
	syncMessages(t, s, "a1", "Archive", archive...)
}

// cachedOrder is every message of a scope newest first, as folder/UID, with
// its conversation: the order every page read follows.
func cachedOrder(t *testing.T, s *Store, folder string) (keys []string, threads []mail.ThreadID) {
	t.Helper()
	scope, args := scopeClause(folder)
	rows, err := s.query(context.Background(), `SELECT f.name, m.server_uid, m.thread_id
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = 'a1' AND m.deleted = 0`+scope+`
		ORDER BY m.date DESC, m.id DESC`, args...)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Values {
		keys = append(keys, fmt.Sprintf("%s/%d", text(r[0]), number(r[1])))
		threads = append(threads, mail.ThreadID(number(r[2])))
	}
	return keys, threads
}

func rowKey(m mail.Summary) string { return fmt.Sprintf("%s/%d", m.Folder, m.UID) }

// arriveOnTop syncs a new message into the inbox, newer than all the others.
func arriveOnTop(t *testing.T, s *Store, uid mail.UID, inReplyTo string, minutes int) {
	t.Helper()
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{
		UID: uid, MessageID: fmt.Sprintf("new%d@example.com", uid), InReplyTo: inReplyTo,
		Subject: fmt.Sprintf("New %d", uid), Date: pageBase.Add(time.Duration(minutes) * time.Minute),
	}})
}

// Paging from the newest message to the oldest lists each once, in
// order, while new mail arrives at the top between the pages: it comes
// before the first page and moves none of the later ones.
func TestMessagePagesListEachMessageOnceAsMailArrives(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	pageFixture(t, s, 1200)
	for _, folder := range []string{"", "INBOX"} {
		want, _ := cachedOrder(t, s, folder)
		var got []string
		var after mail.Cursor
		for pages := 0; ; pages++ {
			page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", folder), After: after, Limit: 37})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range page.Messages {
				if m.Account != "a1" {
					t.Fatalf("a row without its account: %+v", m)
				}
				got = append(got, rowKey(m))
			}
			if page.More != (len(got) < len(want)) || (page.More && len(page.Messages) != 37) {
				t.Fatalf("page %d of %q: %d rows, more %v, after %d of %d", pages, folder, len(page.Messages), page.More, len(got), len(want))
			}
			after = page.Next
			if !page.More {
				break
			}
			arriveOnTop(t, s, mail.UID(5000+len(got)), "", 5000+len(got))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("pages of %q listed %d rows, want %d: first difference at %d", folder, len(got), len(want), firstDifference(got, want))
		}
		// The end stays the end.
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", folder), After: after, Limit: 37})
		if err != nil || len(page.Messages) != 0 || page.More || page.Next != after {
			t.Errorf("after the last page of %q: %+v, %v", folder, page, err)
		}
	}
	// Messages without a date come last, and in their order.
	page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "")})
	if err != nil {
		t.Fatal(err)
	}
	if first := page.Messages[0]; !strings.HasPrefix(first.Subject, "New ") {
		t.Errorf("the newest row is %+v", first)
	}
}

func firstDifference(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// Paging through conversations lists each once, by its newest message,
// while mail arrives: a new conversation comes before the first page, and
// a conversation a reply brings to the top leaves the pages after it and
// is found again at the top.
func TestConversationPagesListEachConversationOnceAsMailArrives(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	pageFixture(t, s, 1200)
	keys, threads := cachedOrder(t, s, "")
	var want []mail.ThreadID
	latest := map[mail.ThreadID]string{}
	for i, thread := range threads {
		if _, ok := latest[thread]; !ok {
			latest[thread] = keys[i]
			want = append(want, thread)
		}
	}
	if len(want) < 1000 {
		t.Fatalf("the fixture has %d conversations", len(want))
	}
	// The conversation that a reply moves up while the pages are read.
	moved := want[len(want)/2]
	var got []mail.ThreadID
	var after mail.Cursor
	for pages := 0; ; pages++ {
		page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), After: after, Limit: 41})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Conversations {
			if c.Account != "a1" || c.Latest.Thread != c.Thread || latest[c.Thread] != rowKey(c.Latest) && c.Thread != moved {
				t.Fatalf("row %+v", c)
			}
			got = append(got, c.Thread)
		}
		after = page.Next
		if !page.More {
			break
		}
		switch pages {
		case 0:
			// A reply to a conversation listed already, and a new one.
			first := page.Conversations[0].Latest
			reply, _, _ := s.GetMessage(ctx, "a1", first.Folder, first.UID)
			arriveOnTop(t, s, 9001, reply.MessageID, 9001)
			arriveOnTop(t, s, 9002, "", 9002)
		case 1:
			m := strings.Split(latest[moved], "/")
			var uid mail.UID
			fmt.Sscan(m[1], &uid)
			reply, _, _ := s.GetMessage(ctx, "a1", m[0], uid)
			arriveOnTop(t, s, 9003, reply.MessageID, 9003)
		}
	}
	expect := slices.DeleteFunc(slices.Clone(want), func(id mail.ThreadID) bool { return id == moved })
	if !slices.Equal(got, expect) {
		t.Fatalf("pages listed %d conversations, want %d: first difference at %d", len(got), len(expect), firstThreadDifference(got, expect))
	}
	top, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if top.Conversations[0].Thread != moved || top.Conversations[0].Latest.UID != 9003 || top.Conversations[0].Count < 2 {
		t.Errorf("the conversation the reply moved is not at the top: %+v", top.Conversations[0])
	}
	if top.Conversations[1].Latest.UID != 9002 || top.Conversations[2].Latest.UID != 9001 {
		t.Errorf("the top of the list = %+v", top.Conversations)
	}
}

func firstThreadDifference(a, b []mail.ThreadID) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// scopeFixture is two accounts. a1 has Inbox, Sent and Archive; a2 calls
// its inbox "Inbox" and its sent folder "Sent Items". Their mail
// interleaves in time, and a conversation of a1 spans Inbox and Sent.
func scopeFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox, Unread: 2}, {Name: "Sent", SpecialUse: mail.SpecialSent}, {Name: "Archive", SpecialUse: mail.SpecialArchive},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFolders(ctx, "a2", []mail.Folder{
		{Name: "Inbox", SpecialUse: mail.SpecialInbox, Unread: 1}, {Name: "Sent Items", SpecialUse: mail.SpecialSent},
	}); err != nil {
		t.Fatal(err)
	}
	at := func(minutes int) time.Time { return pageBase.Add(time.Duration(minutes) * time.Minute) }
	m := func(uid mail.UID, id, subject string, minutes int) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, MessageID: id, Subject: subject, Date: at(minutes), Seen: true}}
	}
	plan := m(1, "plan@a1", "Plan", 1)
	reply := m(1, "reply@a1", "Re: Plan", 4)
	reply.InReplyTo = "plan@a1"
	again := m(2, "again@a1", "Re: Plan", 7)
	again.InReplyTo, again.Seen = "reply@a1", false
	starred := m(3, "star@a1", "Starred", 9)
	starred.Flagged = true
	syncMessages(t, s, "a1", "INBOX", plan, again, starred)
	syncMessages(t, s, "a1", "Sent", reply)
	syncMessages(t, s, "a1", "Archive", m(1, "old@a1", "Archived", 0))
	hello := m(1, "hello@a2", "Hello", 2)
	hello.Seen = false
	news := m(2, "news@a2", "News", 8)
	news.Flagged = true
	syncMessages(t, s, "a2", "Inbox", hello, news)
	syncMessages(t, s, "a2", "Sent Items", m(1, "sent@a2", "Sent by a2", 5))
}

func subjectsOf(list []mail.Summary) []string {
	var out []string
	for _, m := range list {
		out = append(out, m.Account+":"+m.Subject)
	}
	return out
}

// A scope reads one folder, every folder of an account, the folder of a
// special use in each account, or everything, newest first across
// accounts, each row with the account it is in.
func TestPageScopes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	scopeFixture(t, s)
	for _, tc := range []struct {
		name  string
		scope mail.Scope
		want  []string
	}{
		{"a folder", mail.AccountScope("a1", "INBOX"), []string{"a1:Starred", "a1:Re: Plan", "a1:Plan"}},
		{"an account", mail.AccountScope("a1", ""), []string{"a1:Starred", "a1:Re: Plan", "a1:Re: Plan", "a1:Plan", "a1:Archived"}},
		{"the starred of an account", mail.AccountScope("a1", "starred"), []string{"a1:Starred"}},
		{"everything", mail.Scope{}, []string{"a1:Starred", "a2:News", "a1:Re: Plan", "a2:Sent by a2", "a1:Re: Plan", "a2:Hello", "a1:Plan", "a1:Archived"}},
		{"every inbox", mail.AllInboxes(), []string{"a1:Starred", "a2:News", "a1:Re: Plan", "a2:Hello", "a1:Plan"}},
		{"every sent folder", mail.Scope{Special: mail.SpecialSent}, []string{"a2:Sent by a2", "a1:Re: Plan"}},
		{"the inboxes of one account", mail.Scope{Accounts: []string{"a2"}, Special: mail.SpecialInbox}, []string{"a2:News", "a2:Hello"}},
		{"a folder name in several accounts", mail.Scope{Accounts: []string{"a1", "a2"}, Folder: "INBOX"}, []string{"a1:Starred", "a1:Re: Plan", "a1:Plan"}},
		{"everything starred", mail.Scope{Starred: true}, []string{"a1:Starred", "a2:News"}},
		{"an account that is not there", mail.Scope{Accounts: []string{"nobody"}}, nil},
		{"a folder that is not there", mail.AccountScope("a1", "Nowhere"), nil},
	} {
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: tc.scope})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := subjectsOf(page.Messages); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %v\nwant %v", tc.name, got, tc.want)
		}
		if page.More {
			t.Errorf("%s: more after everything", tc.name)
		}
	}
	if _, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.Scope{Folder: "INBOX", Starred: true}}); !errors.Is(err, mail.ErrBadScope) {
		t.Errorf("a scope narrowed twice = %v", err)
	}
	if _, err := s.MessagePage(ctx, mail.ListQuery{After: "not a cursor"}); err == nil {
		t.Error("a cursor no page gave was read")
	}

	// The unified inbox lists the conversations of both accounts, each
	// row made of its inbox messages alone.
	page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AllInboxes()})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, c := range page.Conversations {
		rows = append(rows, fmt.Sprintf("%s:%s:%d:%v", c.Account, c.Latest.Subject, c.Count, c.Unread))
		if c.Latest.Account != c.Account {
			t.Errorf("row %+v", c)
		}
	}
	if want := []string{"a1:Starred:1:false", "a2:News:1:false", "a1:Re: Plan:2:true", "a2:Hello:1:true"}; !slices.Equal(rows, want) {
		t.Errorf("every inbox's conversations:\n got %v\nwant %v", rows, want)
	}
	// Across every folder, the conversation counts its Sent message too.
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "")})
	if err != nil {
		t.Fatal(err)
	}
	if c := page.Conversations[1]; c.Latest.Subject != "Re: Plan" || c.Count != 3 {
		t.Errorf("the conversation across folders = %+v", c)
	}
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.Scope{Special: mail.SpecialSent}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 2 || page.Conversations[1].Count != 1 || page.Conversations[1].Account != "a1" {
		t.Errorf("every sent folder's conversations = %+v", page.Conversations)
	}
}

// The reads every list makes walk an index, newest first, from the
// cursor: none reads a scope whole or sorts it, however large it is. So
// does a search of messages by flags and dates. (A search of
// conversations first finds the conversations of the messages that
// match, which the large-cache test times.)
func TestPageQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	scopeFixture(t, s)
	after := cursor{date: pageBase.Unix(), id: 5, set: true}.value()
	for _, tc := range []struct {
		name          string
		q             mail.ListQuery
		index         string
		conversations bool
	}{
		{"a folder", mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), After: after}, "messages_list", true},
		{"an account", mail.ListQuery{Scope: mail.AccountScope("a1", ""), After: after}, "messages_date", true},
		{"every inbox", mail.ListQuery{Scope: mail.AllInboxes(), After: after}, "messages_date", true},
		{"the starred", mail.ListQuery{Scope: mail.AccountScope("a1", "starred"), After: after}, "messages_flagged", true},
		{"unread with attachments", mail.ListQuery{Search: mail.Search{Unread: true, Attachment: true}, After: after}, "messages_date", false},
		{"a week", mail.ListQuery{Search: mail.Search{Since: pageBase, Before: pageBase.AddDate(0, 0, 7)}}, "messages_date", false},
	} {
		p, err := s.plan(ctx, tc.q, searchCriteria(tc.q.Search))
		if err != nil {
			t.Fatal(err)
		}
		query, args, _ := p.messageSQL()
		checkPlan(t, s, tc.name+" (messages)", query, args, tc.index)
		if tc.conversations {
			query, args = p.conversationSQL()
			checkPlan(t, s, tc.name+" (conversations)", query, args, tc.index)
		}
	}
}

func checkPlan(t *testing.T, s *Store, name, query string, args []any, index string) {
	t.Helper()
	rows, err := s.query(context.Background(), `EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var steps []string
	for _, r := range rows.Values {
		steps = append(steps, text(r[len(r)-1]))
	}
	plan := strings.Join(steps, "\n")
	if !strings.Contains(plan, "SEARCH m USING INDEX "+index) && !strings.Contains(plan, "SCAN m USING INDEX "+index) {
		t.Errorf("%s does not read %s:\n%s", name, index, plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") || strings.Contains(plan, "SCAN m\n") || strings.HasSuffix(plan, "SCAN m") {
		t.Errorf("%s reads or sorts its scope whole:\n%s", name, plan)
	}
}

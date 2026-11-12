package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"shu/mail"
)

// largeCache writes n messages straight into the tables, as years of
// syncs of two accounts would have left them: a1 has Inbox, Sent,
// Archive and Lists, a2 Inbox, Sent and Archive; conversations of about
// three messages across folders; a sender of 300, a short body on every
// fourth message, a third unread, one in fifty starred, one in ten with
// attachments, and a message every 25 minutes.
func largeCache(t *testing.T, s *Store, n int) {
	t.Helper()
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	stmts := []stmtType{
		stmt(`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid, total) VALUES
			(1, 'a1', 'INBOX', '\Inbox', 1, ?1, ?2), (2, 'a1', 'Sent', '\Sent', 1, ?1, ?2), (3, 'a1', 'Archive', '\Archive', 1, ?1, ?2),
			(4, 'a1', 'Lists', '', 1, ?1, ?2), (5, 'a2', 'INBOX', '\Inbox', 1, ?1, ?2), (6, 'a2', 'Sent', '\Sent', 1, ?1, ?2),
			(7, 'a2', 'Archive', '\Archive', 1, ?1, ?2)`, n, n/7),
		stmt(`WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < 20000)
			INSERT INTO threads (id, account_id) SELECT i, CASE WHEN i <= 12000 THEN 'a1' ELSE 'a2' END FROM k`),
		stmt(`WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < ?)
			INSERT INTO messages (folder_id, server_uid, message_id, from_name, from_email, to_json, recipients,
				subject, preview, body_text, date, seen, flagged, has_attachments, thread_id)
			SELECT i % 7 + 1, i, 'm' || i || '@example.com', 'Sender ' || (i % 300), 'sender' || (i % 300) || '@example.com',
				'[{"name":"Reader ' || (i % 40) || '","email":"reader' || (i % 40) || '@example.org"}]',
				'Reader ' || (i % 40) || ' reader' || (i % 40) || '@example.org',
				'Message ' || i || ' about ' || CASE i % 5 WHEN 0 THEN 'the budget' WHEN 1 THEN 'lunch' WHEN 2 THEN 'travel plans'
					WHEN 3 THEN 'the contract' ELSE 'the weekly report' END,
				'A preview of message ' || i || ', with a few words in it',
				CASE WHEN i % 4 = 0 THEN 'The body of message ' || i || '. ' || printf('%.*c', 300, 'x') END,
				1600000000 + i * 1500, i % 3 > 0, i % 50 = 0, i % 10 = 0,
				CASE WHEN i % 7 + 1 <= 4 THEN i % 12000 + 1 ELSE 12001 + i % 8000 END
			FROM k`, n),
	}
	if err := s.transaction(ctx, stmts); err != nil {
		t.Fatal(err)
	}
}

// With a cache of 50,000 messages across two accounts, a page, a page
// deep in a folder, a page of conversations, a search across accounts
// and a search by several parts each take a small part of what a person
// waits for. The times are logged; the bound is generous, for slow and
// busy machines, as the tests run under the race detector too.
func TestLargeCacheStaysResponsive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a cache of 50,000 messages")
	}
	const n = 50000
	const budget = 250 * time.Millisecond
	ctx := context.Background()
	s := openTest(t)
	start := time.Now()
	largeCache(t, s, n)
	t.Logf("wrote %d messages in %v", n, time.Since(start))

	// A cursor halfway down the inbox.
	var deep mail.Cursor
	if page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), Limit: n / 14}); err != nil || !page.More {
		t.Fatalf("the inbox in one page: %d rows, %v", len(page.Messages), err)
	} else {
		deep = page.Next
	}
	type read struct {
		name string
		do   func() (int, error)
	}
	messages := func(q mail.ListQuery) func() (int, error) {
		return func() (int, error) {
			page, err := s.MessagePage(ctx, q)
			return len(page.Messages), err
		}
	}
	conversations := func(q mail.ListQuery) func() (int, error) {
		return func() (int, error) {
			page, err := s.ConversationPage(ctx, q)
			return len(page.Conversations), err
		}
	}
	since, before := time.Unix(1600000000+20000*1500, 0), time.Unix(1600000000+50000*1500, 0)
	for _, r := range []read{
		{"a page of a folder", messages(mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX")})},
		{"a page deep in a folder", messages(mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), After: deep})},
		{"a page of an account", messages(mail.ListQuery{Scope: mail.AccountScope("a1", "")})},
		{"a page of every inbox", messages(mail.ListQuery{Scope: mail.AllInboxes()})},
		{"a page of the starred", messages(mail.ListQuery{Scope: mail.AccountScope("a1", "starred")})},
		{"a page of conversations of a folder", conversations(mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX")})},
		{"a page of conversations deep in a folder", conversations(mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), After: deep})},
		{"a page of conversations of every inbox", conversations(mail.ListQuery{Scope: mail.AllInboxes()})},
		{"a search across accounts", messages(mail.ListQuery{Search: mail.Search{Text: []string{"budget"}}})},
		{"a search across accounts, in every message", messages(mail.ListQuery{Search: mail.Search{Text: []string{"preview"}}})},
		{"a search across accounts for a rare word", messages(mail.ListQuery{Search: mail.Search{Text: []string{"message 4242,"}}})},
		{"a search of two letters across accounts", messages(mail.ListQuery{Search: mail.Search{Text: []string{"zq"}}})},
		{"a search by sender, date, unread and attachments", messages(mail.ListQuery{Scope: mail.AccountScope("a1", ""),
			Search: mail.Search{From: []string{"sender30@"}, Since: since, Before: before, Unread: true, Attachment: true}})},
		{"a search by recipient and subject", messages(mail.ListQuery{Search: mail.Search{To: []string{"reader8@"}, Subject: []string{"contract"}}})},
		{"a search of conversations across accounts", conversations(mail.ListQuery{Search: mail.Search{Text: []string{"budget"}}})},
		{"a search of conversations by flags", conversations(mail.ListQuery{Scope: mail.AllInboxes(), Search: mail.Search{Unread: true, Attachment: true}})},
	} {
		var times []time.Duration
		var rows int
		for range 3 {
			begin := time.Now()
			got, err := r.do()
			times = append(times, time.Since(begin))
			if err != nil {
				t.Fatalf("%s: %v", r.name, err)
			}
			rows = got
		}
		slices.Sort(times)
		t.Logf("%-52s %3d rows  %v", r.name, rows, times[1])
		if times[1] > budget {
			t.Errorf("%s took %v, more than %v", r.name, times[1], budget)
		}
		if rows == 0 && r.name != "a search of two letters across accounts" {
			t.Errorf("%s found nothing", r.name)
		}
	}
}

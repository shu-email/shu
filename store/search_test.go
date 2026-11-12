package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
	_ "time/tzdata"

	"shu/mail"
)

// searchFixture is an account's mail with something different in each
// message, and a second account with a budget of its own.
//
//	INBOX/1    from Maya to Bob, Cc Carol: "Q3 budget review", body about the budget,
//	           unread, an attachment, 1 March 10:00 UTC
//	INBOX/2    from Bob to Maya: "Lunch", preview "Lunch on Friday?", starred, 5 March
//	Sent/1     from Ann to Maya, answering INBOX/1: "Re: Q3 budget review", 2 March 00:00 UTC
//	Archive/1  from 张三 to Ann: "发票", preview "请查收本月发票", unread, 20 February
//	INBOX/3    from a newsletter: "Weekly news", preview "budget tips", 7 March
func searchFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox}, {Name: "Sent", SpecialUse: mail.SpecialSent}, {Name: "Archive"},
	}); err != nil {
		t.Fatal(err)
	}
	addFolder(t, s, "a2", "INBOX")
	maya := mail.Address{Name: "Maya Lin", Email: "maya@example.com"}
	bob := mail.Address{Name: "Bob", Email: "bob@example.org"}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	day := func(d, h int) time.Time { return time.Date(2026, 3, d, h, 0, 0, 0, time.UTC) }
	syncMessages(t, s, "a1", "INBOX",
		mail.Message{Summary: mail.Summary{UID: 1, MessageID: "budget@example.com", From: maya, To: []mail.Address{bob},
			Cc: []mail.Address{{Name: "Carol", Email: "carol@example.net"}}, Subject: "Q3 budget review",
			Preview: "Numbers attached", Date: day(1, 10), HasAttachments: true}},
		mail.Message{Summary: mail.Summary{UID: 2, MessageID: "lunch@example.com", From: bob, To: []mail.Address{maya},
			Subject: "Lunch", Preview: "Lunch on Friday?", Date: day(5, 12), Seen: true, Flagged: true}},
		mail.Message{Summary: mail.Summary{UID: 3, MessageID: "news@example.com", From: mail.Address{Name: "News", Email: "news@example.net"},
			Subject: "Weekly news", Preview: "budget tips", Date: day(7, 8), Seen: true}})
	syncMessages(t, s, "a1", "Sent", mail.Message{Summary: mail.Summary{UID: 1, MessageID: "re-budget@example.com",
		InReplyTo: "budget@example.com", From: ann, To: []mail.Address{maya}, Subject: "Re: Q3 budget review",
		Preview: "Looks good", Date: day(2, 0), Seen: true}})
	syncMessages(t, s, "a1", "Archive", mail.Message{Summary: mail.Summary{UID: 1, MessageID: "fapiao@example.cn",
		From: mail.Address{Name: "张三", Email: "zhang@example.cn"}, To: []mail.Address{ann}, Subject: "发票",
		Preview: "请查收本月发票", Date: time.Date(2026, 2, 20, 9, 0, 0, 0, time.UTC)}})
	// The body of the first, as opening it fetched it.
	m, _, _ := s.GetMessage(ctx, "a1", "INBOX", 1)
	m.BodyText, m.CachedAt = "The budget for Q3 is attached. Thanks, Maya", day(1, 11)
	if err := s.SaveMessage(ctx, "a1", "INBOX", m); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a2", "INBOX", mail.Message{Summary: mail.Summary{UID: 1, MessageID: "a2@example.com",
		From: maya, Subject: "Another budget", Date: day(3, 9)}})
}

func keysOf(list []mail.Summary) []string {
	var out []string
	for _, m := range list {
		out = append(out, fmt.Sprintf("%s:%s/%d", m.Account, m.Folder, m.UID))
	}
	return out
}

// Each part of a search narrows it, alone and with the others, and the
// recipients are searched as the sender is.
func TestSearchFiltersAloneAndCombined(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	march := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }
	const (
		budget = "a1:INBOX/1"
		lunch  = "a1:INBOX/2"
		news   = "a1:INBOX/3"
		reply  = "a1:Sent/1"
		fapiao = "a1:Archive/1"
		other  = "a2:INBOX/1"
	)
	for _, tc := range []struct {
		name   string
		scope  mail.Scope
		search mail.Search
		want   []string
	}{
		{"nothing", mail.AccountScope("a1", ""), mail.Search{}, []string{news, lunch, reply, budget, fapiao}},
		{"a word", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget"}}, []string{news, reply, budget}},
		{"two words", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget", "review"}}, []string{reply, budget}},
		{"a phrase", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget review"}}, []string{reply, budget}},
		{"a phrase that is not there", mail.AccountScope("a1", ""), mail.Search{Text: []string{"review budget"}}, nil},
		{"text finds recipients", mail.AccountScope("a1", ""), mail.Search{Text: []string{"maya"}}, []string{lunch, reply, budget}},
		{"text in any case", mail.AccountScope("a1", ""), mail.Search{Text: []string{"BUDGET TIPS"}}, []string{news}},
		{"from", mail.AccountScope("a1", ""), mail.Search{From: []string{"maya"}}, []string{budget}},
		{"from by address", mail.AccountScope("a1", ""), mail.Search{From: []string{"bob@example.org"}}, []string{lunch}},
		{"to", mail.AccountScope("a1", ""), mail.Search{To: []string{"maya"}}, []string{lunch, reply}},
		{"cc is a recipient", mail.AccountScope("a1", ""), mail.Search{To: []string{"carol"}}, []string{budget}},
		{"subject", mail.AccountScope("a1", ""), mail.Search{Subject: []string{"budget"}}, []string{reply, budget}},
		{"body", mail.AccountScope("a1", ""), mail.Search{Body: []string{"budget"}}, []string{news, budget}},
		{"body by its preview", mail.AccountScope("a1", ""), mail.Search{Body: []string{"friday"}}, []string{lunch}},
		{"since, from midnight on", mail.AccountScope("a1", ""), mail.Search{Since: march(2)}, []string{news, lunch, reply}},
		{"before, up to midnight", mail.AccountScope("a1", ""), mail.Search{Before: march(2)}, []string{budget, fapiao}},
		{"a range", mail.AccountScope("a1", ""), mail.Search{Since: march(1), Before: march(5)}, []string{reply, budget}},
		{"unread", mail.AccountScope("a1", ""), mail.Search{Unread: true}, []string{budget, fapiao}},
		{"starred", mail.AccountScope("a1", ""), mail.Search{Starred: true}, []string{lunch}},
		{"attachments", mail.AccountScope("a1", ""), mail.Search{Attachment: true}, []string{budget}},
		{"in a folder", mail.AccountScope("a1", ""), mail.Search{In: "archive"}, []string{fapiao}},
		{"in a special use", mail.AccountScope("a1", ""), mail.Search{In: "sent"}, []string{reply}},
		{"in a folder that is not there", mail.AccountScope("a1", ""), mail.Search{In: "Nowhere"}, nil},
		{"in another folder than the list's", mail.AccountScope("a1", "INBOX"), mail.Search{In: "sent"}, nil},
		{"text and unread", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget"}, Unread: true}, []string{budget}},
		{"from, attachments and a date", mail.AccountScope("a1", ""), mail.Search{From: []string{"maya"}, Attachment: true, Since: march(1)}, []string{budget}},
		{"from and too late", mail.AccountScope("a1", ""), mail.Search{From: []string{"maya"}, Since: march(2)}, nil},
		{"text in a folder", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget"}, In: "sent"}, []string{reply}},
		{"to and subject", mail.AccountScope("a1", ""), mail.Search{To: []string{"maya"}, Subject: []string{"lunch"}}, []string{lunch}},
		{"to, but not from", mail.AccountScope("a1", ""), mail.Search{To: []string{"bob"}, From: []string{"bob"}}, nil},
		// Shorter than a trigram: compared as text, in every part.
		{"two letters", mail.AccountScope("a1", ""), mail.Search{Text: []string{"Q3"}}, []string{reply, budget}},
		{"two Chinese characters", mail.AccountScope("a1", ""), mail.Search{Text: []string{"发票"}}, []string{fapiao}},
		{"a short sender", mail.AccountScope("a1", ""), mail.Search{From: []string{"张三"}}, []string{fapiao}},
		{"a short recipient", mail.AccountScope("a1", ""), mail.Search{To: []string{"bo"}}, []string{budget}},
		{"long and short terms", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget", "Q3"}, Subject: []string{"Re:"}}, []string{reply}},
		{"a short term in a part", mail.AccountScope("a1", ""), mail.Search{Text: []string{"budget"}, Subject: []string{"Re"}}, []string{reply, budget}},
		{"Chinese of three characters", mail.AccountScope("a1", ""), mail.Search{Body: []string{"请查收"}}, []string{fapiao}},
		// The account is the scope's.
		{"every account", mail.Scope{}, mail.Search{Text: []string{"budget"}}, []string{news, other, reply, budget}},
		{"the other account", mail.Scope{Accounts: []string{"a2"}}, mail.Search{Text: []string{"budget"}}, []string{other}},
		{"every inbox", mail.AllInboxes(), mail.Search{From: []string{"maya"}}, []string{other, budget}},
	} {
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: tc.scope, Search: tc.search})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := keysOf(page.Messages); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %v\nwant %v", tc.name, got, tc.want)
		}
	}
}

// What the user types is searched for as it is: nothing in it is an
// operator of the full-text index, and nothing fails.
func TestSearchTakesOperatorsAsText(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 9, MessageID: "ops@example.com",
		Subject: `Use "quotes" OR NEAR(x) AND * -minus {subject}: 50% off_now`, Date: pageBase}})
	for _, term := range []string{`"quotes"`, `"`, `OR NEAR(x)`, `NEAR(`, `*`, `-minus`, `{subject}:`, `50%`, `off_now`, `%`, `_`, `\`, `AND`} {
		for _, search := range []mail.Search{{Text: []string{term}}, {Subject: []string{term}}} {
			page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: search})
			if err != nil {
				t.Errorf("search %+v: %v", search, err)
				continue
			}
			want := []string{"a1:INBOX/9"}
			if term == `\` {
				want = nil
			}
			if got := keysOf(page.Messages); !slices.Equal(got, want) {
				t.Errorf("search %+v found %v, want %v", search, got, want)
			}
		}
	}
}

// A message found says where its terms were; a conversation found says
// which of its messages were, in any folder, and where.
func TestSearchSaysWhyItMatched(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	matched := func(search mail.Search) map[string]mail.Field {
		t.Helper()
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: search})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]mail.Field{}
		for _, m := range page.Messages {
			out[fmt.Sprintf("%s/%d", m.Folder, m.UID)] = m.Matched
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		search mail.Search
		want   map[string]mail.Field
	}{
		{"a word", mail.Search{Text: []string{"budget"}}, map[string]mail.Field{
			"INBOX/1": mail.FieldSubject | mail.FieldBody, "Sent/1": mail.FieldSubject, "INBOX/3": mail.FieldBody}},
		{"a name", mail.Search{Text: []string{"maya"}}, map[string]mail.Field{
			"INBOX/1": mail.FieldFrom | mail.FieldBody, "INBOX/2": mail.FieldTo, "Sent/1": mail.FieldTo}},
		{"a sender and a word", mail.Search{From: []string{"maya"}, Text: []string{"attached"}}, map[string]mail.Field{
			"INBOX/1": mail.FieldFrom | mail.FieldBody}},
		{"a recipient in Cc", mail.Search{To: []string{"carol"}}, map[string]mail.Field{"INBOX/1": mail.FieldTo}},
		{"two letters", mail.Search{Text: []string{"q3"}}, map[string]mail.Field{
			"INBOX/1": mail.FieldSubject | mail.FieldBody, "Sent/1": mail.FieldSubject}},
		{"flags alone", mail.Search{Starred: true}, map[string]mail.Field{"INBOX/2": 0}},
	} {
		if got := matched(tc.search); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: matched %v, want %v", tc.name, got, tc.want)
		}
	}

	// Searched from the inbox, the reply in Sent finds its conversation.
	page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), Search: mail.Search{Text: []string{"looks good"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 1 {
		t.Fatalf("conversations = %+v", page.Conversations)
	}
	c := page.Conversations[0]
	sent := mail.MessageRef{Folder: "Sent", UID: 1}
	if c.Latest.Folder != "INBOX" || c.Latest.UID != 1 || c.Count != 1 || c.Account != "a1" || c.Latest.Matched != 0 ||
		!slices.Equal(c.Matches, []mail.MessageRef{sent}) || !slices.Equal(c.Found, []mail.Match{{MessageRef: sent, Fields: mail.FieldBody}}) {
		t.Errorf("the conversation found by its reply = %+v", c)
	}
	// Both of its messages match a word, each in its own parts; the row's
	// message says where it did.
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), Search: mail.Search{Text: []string{"budget"}}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, c := range page.Conversations {
		rows = append(rows, fmt.Sprintf("%s/%d:%v:%d", c.Latest.Folder, c.Latest.UID, c.Found, c.Latest.Matched))
	}
	want := []string{
		fmt.Sprintf("INBOX/3:[{{INBOX 3} %d}]:%d", mail.FieldBody, mail.FieldBody),
		fmt.Sprintf("INBOX/1:[{{Sent 1} %d} {{INBOX 1} %d}]:%d", mail.FieldSubject, mail.FieldSubject|mail.FieldBody, mail.FieldSubject|mail.FieldBody),
	}
	if !slices.Equal(rows, want) {
		t.Errorf("conversations of budget:\n got %v\nwant %v", rows, want)
	}
	// A search must match one message whole: the sender of one and the
	// attachment of another do not make a conversation found.
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: mail.Search{From: []string{"ann"}, Attachment: true}})
	if err != nil || len(page.Conversations) != 0 {
		t.Errorf("parts of two messages found a conversation: %+v, %v", page.Conversations, err)
	}
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), Search: mail.Search{From: []string{"ann"}, In: "sent"}})
	if err != nil || len(page.Conversations) != 1 || !slices.Equal(page.Conversations[0].Matches, []mail.MessageRef{sent}) {
		t.Errorf("the inbox's conversations with a message from Ann in Sent = %+v, %v", page.Conversations, err)
	}
}

// A day searched for is the caller's day: the same words find other
// messages in another zone, and a day clocks go forward on lasts 23
// hours.
func TestSearchDaysAcrossZones(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	// New York moves its clocks forward at 2:00 on 8 March 2026: that day
	// runs from 05:00 UTC to 04:00 UTC the next day.
	at := []time.Time{
		time.Date(2026, 3, 8, 4, 59, 59, 0, time.UTC),
		time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 3, 59, 59, 0, time.UTC),
		time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC),
	}
	for i, when := range at {
		syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: mail.UID(i + 1),
			MessageID: fmt.Sprintf("d%d@example.com", i), Subject: "Day", Date: when}})
	}
	for _, tc := range []struct {
		zone string
		want []string
	}{
		{"America/New_York", []string{"a1:INBOX/3", "a1:INBOX/2"}},
		// 8 March in Shanghai is 16:00 UTC on the 7th to 16:00 UTC on the 8th.
		{"Asia/Shanghai", []string{"a1:INBOX/2", "a1:INBOX/1"}},
		{"UTC", []string{"a1:INBOX/2", "a1:INBOX/1"}},
	} {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		search := mail.ParseSearch("after:2026-03-08 before:2026-03-09", loc)
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: search})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(page.Messages); !slices.Equal(got, tc.want) {
			t.Errorf("8 March in %s found %v, want %v", tc.zone, got, tc.want)
		}
	}
}

// The search of the reads that take a query is what it was: the text
// whole, in the subject, the sender and the body, not the recipients.
func TestQuerySearchIsUnchanged(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	for query, want := range map[string]int{"budget review": 2, "review budget": 0, "carol": 0, "maya": 1, "q3": 2} {
		got, err := s.ListMessages(ctx, "a1", "", query, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Errorf("query %q found %d, want %d: %v", query, len(got), want, keysOf(got))
		}
	}
}

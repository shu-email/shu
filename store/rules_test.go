package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

func ruleNames(t *testing.T, s *Store) []string {
	t.Helper()
	rules, err := s.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, r := range rules {
		out = append(out, r.Name)
	}
	return out
}

// starRule is a rule of the tests that stars a message of a subject; one
// with an account moves it to that account's Archive too.
func starRule(name, account string) mail.Rule {
	r := mail.Rule{Name: name, Match: mail.MatchAll,
		Conditions: []mail.Condition{{Field: mail.CondSubject, Value: name}},
		Actions:    []mail.Action{{Kind: mail.ActionStar}}}
	if account != "" {
		r.Actions = append(r.Actions, mail.Action{Kind: mail.ActionMove, Account: account, Folder: "Archive"})
	}
	return r
}

// Rules are in the order they were made until one is moved, keep what
// they hold, and have IDs never given again; a rule goes with the account
// it names, and one that names none stays.
func TestRulesInOrderAndWithTheirAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	var saved []mail.Rule
	for _, r := range []mail.Rule{starRule("One", ""), starRule("Two", "a1"), starRule("Three", "a2"), starRule("Four", "a1")} {
		got, err := s.SaveRule(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, got)
	}
	rules, err := s.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules, saved) {
		t.Fatalf("rules =\n%+v\nsaved\n%+v", rules, saved)
	}
	edited := saved[1]
	edited.Name, edited.Paused, edited.Match, edited.Stop = "Two edited", true, mail.MatchAny, true
	edited.Conditions = append(edited.Conditions, mail.Condition{Field: mail.CondHeader, Header: "List-Id", Value: "x"})
	if _, err := s.SaveRule(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if rules, _ := s.Rules(ctx); !reflect.DeepEqual(rules[1], edited) {
		t.Errorf("edited = %+v", rules[1])
	}
	if _, err := s.SaveRule(ctx, mail.Rule{ID: 99, Name: "Gone", Match: mail.MatchAll}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("saved a rule there is not: %v", err)
	}
	for _, step := range []struct {
		id    mail.RuleID
		index int
		want  []string
	}{
		{saved[3].ID, 0, []string{"Four", "One", "Two edited", "Three"}},
		{saved[0].ID, 9, []string{"Four", "Two edited", "Three", "One"}},
		{saved[2].ID, -1, []string{"Three", "Four", "Two edited", "One"}},
	} {
		if err := s.MoveRule(ctx, step.id, step.index); err != nil {
			t.Fatal(err)
		}
		if got := ruleNames(t, s); !slices.Equal(got, step.want) {
			t.Errorf("moved to %d: %v, want %v", step.index, got, step.want)
		}
	}
	if err := s.MoveRule(ctx, 99, 0); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("moved a rule there is not: %v", err)
	}
	if err := s.DeleteRule(ctx, saved[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRule(ctx, saved[2].ID); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	five, err := s.SaveRule(ctx, starRule("Five", ""))
	if err != nil {
		t.Fatal(err)
	}
	if five.ID <= saved[3].ID {
		t.Errorf("a new rule got ID %d after %d", five.ID, saved[3].ID)
	}
	if got := ruleNames(t, s); !slices.Equal(got, []string{"Four", "Two edited", "One", "Five"}) {
		t.Errorf("after a deletion and a new rule: %v", got)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got := ruleNames(t, s); !slices.Equal(got, []string{"One", "Five"}) {
		t.Errorf("after a1 went: %v", got)
	}
	if _, err := s.SaveRule(ctx, starRule("Six", "a1")); err == nil {
		t.Error("saved a rule of an account that is gone")
	}
}

// activity is an entry of the log of the tests.
func activity(account, folder string, uid mail.UID, rule mail.RuleID, state mail.ActivityState) mail.RuleActivity {
	return mail.RuleActivity{
		At: labelBase, Rule: rule, RuleName: "R", Account: account, Folder: folder, UIDValidity: 7, UID: uid,
		MessageID: "m@example.org", Key: "m@example.org", Subject: "S", From: mail.Address{Name: "Maya", Email: "maya@example.org"},
		Actions: []mail.Action{{Kind: mail.ActionMove, Account: account, Folder: "Archive"}}, Quiet: true, State: state,
	}
}

// The log is read newest first, narrowed by entry, rule, account, state
// and message, a page at a time with how many in all; entries keep all
// they hold, are changed in their state, changes and error only, outlive
// their rule and go with their account.
func TestRuleLogQueries(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	written, err := s.AddRuleActivity(ctx, []mail.RuleActivity{
		activity("a1", "INBOX", 1, 1, mail.ActivityApplied),
		activity("a1", "INBOX", 2, 2, mail.ActivityPending),
		activity("a2", "INBOX", 1, 1, mail.ActivityPending),
		activity("a1", "Lists", 2, 1, mail.ActivityFailed),
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.RuleActivity(ctx, mail.ActivityQuery{ID: written[0].ID})
	if err != nil || len(read.Entries) != 1 || !reflect.DeepEqual(read.Entries[0], written[0]) {
		t.Fatalf("read back %+v, %v\nwritten %+v", read.Entries, err, written[0])
	}
	ids := func(q mail.ActivityQuery) ([]mail.ActivityID, int) {
		t.Helper()
		page, err := s.RuleActivity(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []mail.ActivityID
		for _, e := range page.Entries {
			out = append(out, e.ID)
		}
		return out, page.Total
	}
	w := func(i int) mail.ActivityID { return written[i].ID }
	for _, tc := range []struct {
		q     mail.ActivityQuery
		want  []mail.ActivityID
		total int
	}{
		{mail.ActivityQuery{}, []mail.ActivityID{w(3), w(2), w(1), w(0)}, 4},
		{mail.ActivityQuery{Rule: 1}, []mail.ActivityID{w(3), w(2), w(0)}, 3},
		{mail.ActivityQuery{Account: "a1", State: mail.ActivityPending}, []mail.ActivityID{w(1)}, 1},
		{mail.ActivityQuery{Account: "a1", Folder: "INBOX", UIDValidity: 7, UIDs: []mail.UID{2, 5}}, []mail.ActivityID{w(1)}, 1},
		{mail.ActivityQuery{Account: "a1", Folder: "INBOX", UIDValidity: 8}, nil, 0},
		{mail.ActivityQuery{Offset: 1, Limit: 2}, []mail.ActivityID{w(2), w(1)}, 4},
		{mail.ActivityQuery{Offset: 9}, nil, 4},
	} {
		got, total := ids(tc.q)
		if !slices.Equal(got, tc.want) || total != tc.total {
			t.Errorf("%+v: %v of %d, want %v of %d", tc.q, got, total, tc.want, tc.total)
		}
	}
	settled := written[1]
	settled.State, settled.Error = mail.ActivityApplied, ""
	settled.Changes = mail.RuleChanges{MovedTo: "Archive", DestUID: 4, Flags: []mail.FlagChange{{Flag: mail.FlagSeen, Was: false}},
		LabelsAdded: []mail.LabelID{3}, LabelsRemoved: []mail.LabelID{4}, MovedBack: true, BackUID: 9}
	settled.Subject = "not written"
	if err := s.UpdateRuleActivity(ctx, []mail.RuleActivity{settled, activity("a1", "INBOX", 1, 1, mail.ActivityUndone)}); err != nil {
		t.Fatal(err)
	}
	settled.Subject = "S"
	if read, _ := s.RuleActivity(ctx, mail.ActivityQuery{ID: settled.ID}); !reflect.DeepEqual(read.Entries[0], settled) {
		t.Errorf("updated %+v\nwant %+v", read.Entries[0], settled)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got, total := ids(mail.ActivityQuery{}); !slices.Equal(got, []mail.ActivityID{w(2)}) || total != 1 {
		t.Errorf("after a1 went the log holds %v", got)
	}
}

// A sync keeps the header lines it fetched for the rules with a message
// it writes, and a sync of it again without them keeps them. The reads
// for the rules give each message's labels and header lines, the
// tombstones left out.
func TestMessageHeadersForTheRules(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	one := labelled(1, "one@example.org", "One", 1, "Work")
	one.RawHeaders = "List-Id: <one.example.org>\r\n"
	two := labelled(2, "two@example.org", "Two", 2)
	syncMessages(t, s, "a1", "INBOX", one, two, labelled(3, "three@example.org", "Three", 3))
	syncMessages(t, s, "a1", "INBOX", labelled(1, "one@example.org", "One", 1, "Work"))
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 3, true); err != nil {
		t.Fatal(err)
	}
	all, err := s.MessageHeaders(ctx, "a1", "INBOX", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].UID != 1 || all[1].UID != 2 {
		t.Fatalf("read %+v", all)
	}
	if all[0].RawHeaders != one.RawHeaders || all[1].RawHeaders != "" || all[0].Account != "a1" || all[0].Folder != "INBOX" {
		t.Errorf("header lines %q and %q", all[0].RawHeaders, all[1].RawHeaders)
	}
	if got := labelNames(t, s, "a1", all[0].Summary); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("One has %v", got)
	}
	some, err := s.MessageHeaders(ctx, "a1", "INBOX", []mail.UID{2, 3, 9})
	if err != nil || len(some) != 1 || some[0].Subject != "Two" {
		t.Errorf("of 2, 3 and 9: %+v, %v", some, err)
	}
	if none, err := s.MessageHeaders(ctx, "a1", "INBOX", []mail.UID{}); err != nil || len(none) != 0 {
		t.Errorf("of none: %+v, %v", none, err)
	}
}

// What this app moves into a folder is noted by key, taken back once, and
// kept to the bound; the notes go with their folder.
func TestExpectedArrivals(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Archive")
	if err := s.ExpectArrivals(ctx, "a1", "INBOX", []string{"k1", "k2", "k2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpectArrivals(ctx, "a1", "Nowhere", []string{"k1"}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("a note of a folder there is not: %v", err)
	}
	if got, err := s.TakeArrivals(ctx, "a1", "Archive", []string{"k1"}); err != nil || len(got) != 0 {
		t.Errorf("Archive's notes = %v, %v", got, err)
	}
	if got, err := s.TakeArrivals(ctx, "a1", "INBOX", []string{"k2", "k3"}); err != nil || !slices.Equal(got, []string{"k2"}) {
		t.Errorf("taken %v, %v", got, err)
	}
	if got, err := s.TakeArrivals(ctx, "a1", "INBOX", []string{"k2"}); err != nil || len(got) != 0 {
		t.Errorf("taken twice: %v, %v", got, err)
	}
	keys := make([]string, mail.MaxExpectedArrivals)
	for i := range keys {
		keys[i] = "n" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	if err := s.ExpectArrivals(ctx, "a1", "Archive", keys); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM expected_arrivals`); n != mail.MaxExpectedArrivals {
		t.Errorf("%d notes are kept", n)
	}
	if got, err := s.TakeArrivals(ctx, "a1", "INBOX", []string{"k1"}); err != nil || len(got) != 0 {
		t.Errorf("the oldest note outlived the bound: %v, %v", got, err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM expected_arrivals`); n != 0 {
		t.Errorf("%d notes outlived their account", n)
	}
}

// A database of the previous release, v12, gains rules and their log,
// empty, and keeps what it had; a rule can then be written and go with
// its account.
func TestMigrationAddsRulesToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 12; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid, synced_at) VALUES (1, 'a1', 'INBOX', '\Inbox', 1, 1, 100)`,
		`INSERT INTO threads (id, account_id) VALUES (1, 'a1')`,
		`INSERT INTO messages (folder_id, server_uid, message_id, subject, date, thread_id, msg_key, raw_headers)
			VALUES (1, 1, 'one@example.org', 'One', 0, 1, 'one@example.org', 'Delivered-To: ann@example.com' || char(13, 10))`,
		"PRAGMA user_version=12",
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
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) || SchemaVersion < 13 {
		t.Fatalf("user_version = %d, schema v%d", got, SchemaVersion)
	}
	if got := ruleNames(t, s); len(got) != 0 {
		t.Errorf("rules = %v", got)
	}
	if page, err := s.RuleActivity(ctx, mail.ActivityQuery{}); err != nil || page.Total != 0 {
		t.Errorf("log = %+v, %v", page, err)
	}
	msgs, err := s.MessageHeaders(ctx, "a1", "INBOX", nil)
	if err != nil || len(msgs) != 1 || msgs[0].RawHeaders != "Delivered-To: ann@example.com\r\n" {
		t.Errorf("the message from before = %+v, %v", msgs, err)
	}
	if _, err := s.SaveRule(ctx, starRule("News", "a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRuleActivity(ctx, []mail.RuleActivity{activity("a1", "INBOX", 1, 1, mail.ActivityApplied)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT (SELECT COUNT(*) FROM rules) + (SELECT COUNT(*) FROM rule_log)`); n != 0 {
		t.Errorf("%d rows outlived their account", n)
	}
}

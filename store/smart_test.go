package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"shu/mail"
)

// smartFixture is Ann's mail for the smart mailboxes' reads: unread weekly
// reports in the inbox and the archive, a read one, a weekly message in
// Junk and one in Trash, and one without the weekly word.
func smartFixture(t *testing.T, s *Store) {
	t.Helper()
	addAccount(t, s, "a1")
	if err := s.SaveFolders(context.Background(), "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
		{Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
	}); err != nil {
		t.Fatal(err)
	}
	ann := mail.Address{Name: "Ann", Email: "ann@example.com"}
	now := time.Now()
	report := func(uid mail.UID, id, subject string, seen, attached bool, at time.Time) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, MessageID: id, From: ann,
			To: []mail.Address{{Name: "Me", Email: "me@example.com"}}, Subject: subject,
			Date: at, Seen: seen, HasAttachments: attached}}
	}
	syncMessages(t, s, "a1", "INBOX",
		report(1, "weekly-1@example.com", "Weekly report", false, true, now.Add(-time.Hour)),
		report(2, "weekly-2@example.com", "Weekly plan", false, false, now.Add(-2*time.Hour)),
		report(3, "lunch@example.com", "Lunch on Friday", false, false, now.Add(-3*time.Hour)),
		report(4, "read@example.com", "Weekly read", true, false, now.Add(-4*time.Hour)))
	syncMessages(t, s, "a1", "Archive",
		report(1, "weekly-3@example.com", "Weekly archive", false, false, now.Add(-5*time.Hour)))
	syncMessages(t, s, "a1", "Junk",
		report(1, "weekly-4@example.com", "Weekly junk", false, false, now.Add(-6*time.Hour)))
	syncMessages(t, s, "a1", "Trash",
		report(1, "weekly-5@example.com", "Weekly trash", false, false, now.Add(-7*time.Hour)))
}

func TestSmartMailboxRoundTrip(t *testing.T) {
	s := openTest(t)
	smartFixture(t, s)
	ctx := context.Background()
	saved, err := s.SaveSmartMailbox(ctx, mail.SmartMailbox{Account: "a1", Name: "Weekly reports",
		Folder: "INBOX", Query: "weekly has:attachment", Scope: mail.SmartScopeHere})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 {
		t.Fatal("a saved smart mailbox has no ID")
	}
	second, err := s.SaveSmartMailbox(ctx, mail.SmartMailbox{Account: "a1", Name: "All folders",
		Folder: "", Query: "weekly", Scope: mail.SmartScopeAccount})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.SmartMailboxes(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0] != saved || list[1] != second {
		t.Fatalf("the mailboxes = %+v", list)
	}
	// The name is one per account whatever its case; another account's is
	// its own.
	if _, err := s.SaveSmartMailbox(ctx, mail.SmartMailbox{Account: "a1", Name: "weekly REPORTS",
		Folder: "INBOX", Query: "x", Scope: mail.SmartScopeHere}); !errors.Is(err, mail.ErrSmartExists) {
		t.Errorf("a name in another case: %v", err)
	}
	// A change keeps the list it was saved from.
	renamed, err := s.SaveSmartMailbox(ctx, mail.SmartMailbox{ID: saved.ID, Account: "a1",
		Name: "Reports", Folder: "Sent", Query: "weekly from:ann", Scope: mail.SmartScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != saved.ID || renamed.Folder != "INBOX" || renamed.Name != "Reports" ||
		renamed.Query != "weekly from:ann" || renamed.Scope != mail.SmartScopeAll {
		t.Errorf("renamed to %+v, was %+v", renamed, saved)
	}
	if _, err := s.SaveSmartMailbox(ctx, mail.SmartMailbox{ID: 999, Account: "a1", Name: "Gone",
		Query: "x", Scope: mail.SmartScopeHere}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("a mailbox there is not: %v", err)
	}
	if err := s.DeleteSmartMailbox(ctx, "a1", second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSmartMailbox(ctx, "a1", second.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("deleting it twice: %v", err)
	}
	list, err = s.SmartMailboxes(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != saved.ID {
		t.Errorf("after deleting one, the mailboxes = %+v", list)
	}
}

// A smart mailbox's badge counts the unread messages its scope and search
// read, by message, as the list would find them.
func TestCountUnread(t *testing.T) {
	s := openTest(t)
	smartFixture(t, s)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		scope mail.Scope
		query string
		want  int
	}{
		{"the inbox's weekly mail", mail.AccountScope("a1", "INBOX"), "weekly", 2},
		{"one of them", mail.AccountScope("a1", "INBOX"), "weekly has:attachment", 1},
		{"the account's weekly mail", mail.AccountScope("a1", ""), "weekly", 5},
		{"a folder without it", mail.AccountScope("a1", "Sent"), "weekly", 0},
		{"a search of none", mail.AccountScope("a1", "INBOX"), "monthly", 0},
		{"unread alone", mail.AccountScope("a1", "INBOX"), "is:unread", 3},
	} {
		got, err := s.CountUnread(ctx, mail.ListQuery{Scope: tc.scope, Search: mail.ParseSearch(tc.query, time.UTC)})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: counted %d, want %d", tc.name, got, tc.want)
		}
	}
}

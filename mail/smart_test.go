package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A smart mailbox is saved with a name, the list it was saved from, the
// search and the scope it covered; the service trims and checks them, and
// reads one back as saved.
func TestSaveSmartMailbox(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.SaveSmartMailbox(ctx, SmartMailbox{Account: "a1", Name: "  Weekly  ",
		Folder: "INBOX", Query: "  weekly has:attachment  ", Scope: SmartScopeHere})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Weekly" || saved.Query != "weekly has:attachment" || saved.Scope != SmartScopeHere {
		t.Errorf("saved %+v", saved)
	}
	if saved.ID == 0 {
		t.Error("saved without an ID")
	}
	list, err := svc.SmartMailboxes(ctx, "a1")
	if err != nil || len(list) != 1 || list[0] != saved {
		t.Fatalf("the mailboxes = %+v, %v", list, err)
	}
	// A scope of "" is the list's own.
	second, err := svc.SaveSmartMailbox(ctx, SmartMailbox{Account: "a1", Name: "All", Query: "weekly"})
	if err != nil || second.Scope != SmartScopeHere {
		t.Fatalf("a scope by default: %+v, %v", second, err)
	}
	for _, tc := range []struct {
		name string
		mb   SmartMailbox
		want error
	}{
		{"no name", SmartMailbox{Account: "a1", Query: "x"}, ErrSmartName},
		{"a blank name", SmartMailbox{Account: "a1", Name: "   ", Query: "x"}, ErrSmartName},
		{"a long name", SmartMailbox{Account: "a1", Name: strings.Repeat("a", MaxSmartName+1), Query: "x"}, ErrSmartName},
		{"a long search", SmartMailbox{Account: "a1", Name: "Long", Query: strings.Repeat("a", MaxSmartQuery+1)}, ErrSmartQuery},
		{"another scope", SmartMailbox{Account: "a1", Name: "Odd", Query: "x", Scope: "somewhere"}, ErrSmartScope},
		{"a name another has", SmartMailbox{Account: "a1", Name: "weekly", Query: "x"}, ErrSmartExists},
		{"no account", SmartMailbox{Account: "gone", Name: "X", Query: "x"}, ErrNotFound},
	} {
		if _, err := svc.SaveSmartMailbox(ctx, tc.mb); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// It changes in place, keeping its list.
	changed, err := svc.SaveSmartMailbox(ctx, SmartMailbox{ID: saved.ID, Account: "a1", Name: "Reports",
		Folder: "Sent", Query: "from:ann", Scope: SmartScopeAccount})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID != saved.ID || changed.Name != "Reports" || changed.Query != "from:ann" ||
		changed.Scope != SmartScopeAccount || changed.Folder != "INBOX" {
		t.Errorf("changed to %+v", changed)
	}
	if err := svc.DeleteSmartMailbox(ctx, "a1", second.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSmartMailbox(ctx, "a1", second.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting it twice: %v", err)
	}
	if list, _ := store.SmartMailboxes(ctx, "a1"); len(list) != 1 || list[0].ID != saved.ID {
		t.Errorf("after the deletion, the mailboxes = %+v", list)
	}
}

// CountUnread reads the same scope as the list, so that a saved search and
// its badge agree.
func TestCountUnreadScope(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFolders(ctx, "a1", []Folder{
		{Name: "INBOX", SpecialUse: SpecialInbox},
		{Name: "Sent", SpecialUse: SpecialSent},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySync(ctx, "a1", SyncBatch{Folder: Folder{Name: "INBOX"}, Messages: []Message{
		{Summary: Summary{UID: 1, MessageID: "one@example.com", Subject: "Weekly report", Date: time.Now()}},
		{Summary: Summary{UID: 2, MessageID: "two@example.com", Subject: "Weekly plan", Date: time.Now()}},
		{Summary: Summary{UID: 3, MessageID: "three@example.com", Subject: "Weekly read", Date: time.Now(), Seen: true}},
	}}); err != nil {
		t.Fatal(err)
	}
	n, err := svc.CountUnread(ctx, ListQuery{Scope: AccountScope("a1", "INBOX"),
		Search: ParseSearch("weekly", time.UTC)})
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2", n, err)
	}
	if n, _ = svc.CountUnread(ctx, ListQuery{Scope: AccountScope("a1", ""), Search: ParseSearch("weekly", time.UTC)}); n != 2 {
		t.Errorf("the account's count = %d, want 2", n)
	}
	if n, _ = svc.CountUnread(ctx, ListQuery{Scope: AccountScope("a1", "Sent"), Search: ParseSearch("weekly", time.UTC)}); n != 0 {
		t.Errorf("Sent's count = %d, want 0", n)
	}
}

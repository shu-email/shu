package mail

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// A block names an address or a domain, and matches what it names,
// whatever case the mail writes it in.
func TestBlockedMatchesAddressAndDomain(t *testing.T) {
	blocks := []BlockedSender{
		{Value: "deals@shop.example"},
		{Value: "shop.example", Domain: true},
	}
	for _, c := range []struct {
		from string
		want bool
	}{
		{"deals@shop.example", true},
		{"Deals@Shop.Example", true},
		{"news@shop.example", true},
		{"deals@other.example", false},
		{"deals@sub.shop.example", false},
		{"", false},
	} {
		_, got := Blocked(blocks, Address{Email: c.from})
		if got != c.want {
			t.Errorf("Blocked(%q) = %v, want %v", c.from, got, c.want)
		}
	}
	for _, c := range []struct {
		address string
		domain  string
		ok      bool
	}{
		{"Ann <Deals@Shop.Example>", "", true},
		{"deals@shop.example", "", true},
		{"no-at-sign", "", false},
		{"@shop.example", "", false},
		{"", "shop.example", true},
		{"", "@Shop.Example", true},
		{"", "no-dot", false},
		{"", "two words.example", false},
	} {
		if c.domain == "" {
			if got, err := BlockAddress(c.address); (err == nil) != c.ok || (c.ok && got != "deals@shop.example") {
				t.Errorf("BlockAddress(%q) = %q, %v", c.address, got, err)
			}
			continue
		}
		if got, err := BlockDomain(c.domain); (err == nil) != c.ok || (c.ok && got != "shop.example") {
			t.Errorf("BlockDomain(%q) = %q, %v", c.domain, got, err)
		}
	}
	if _, err := BlockAddress("nobody"); !errors.Is(err, ErrBlockInvalid) {
		t.Errorf("a block without an address = %v", err)
	}
}

// A block files its sender's mail as it arrives, and none of the mail
// that was there: the old newsletter stays in the inbox.
func TestBlockFilesTheMailThatArrives(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.deliver("INBOX", news("Weekly news 1", "n1@shop.example", 1))
	r.sync("INBOX")
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "News <news@shop.example>", false, "Trash"); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Weekly news 2", "n2@shop.example", 3))
	r.deliver("INBOX", ruleMail("Lunch?", "Bob <bob@example.org>", "l1@example.org", "", 4))
	r.sync("INBOX")
	waitFor(t, "the block to file the newsletter", func() bool {
		return slices.Contains(r.onServer("Trash"), "Weekly news 2")
	})
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Lunch?", "Weekly news 1"}) {
		t.Errorf("INBOX holds %v", got)
	}
	if got := r.serverMessage("Trash", "Weekly news 2").Subject; got != "Weekly news 2" {
		t.Errorf("Trash holds %q", got)
	}
	// A domain blocks every sender of it, and an unblock takes it back.
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "other.example", true, "Trash"); err != nil {
		t.Fatal(err)
	}
	blocks, err := r.svc.BlockedSenders(r.ctx, r.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || !blocks[1].Domain || blocks[1].Value != "other.example" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if err := r.svc.UnblockSender(r.ctx, r.a.ID, "news@shop.example", false); err != nil {
		t.Fatal(err)
	}
	if blocks, err = r.svc.BlockedSenders(r.ctx, r.a.ID); err != nil || len(blocks) != 1 {
		t.Errorf("after unblocking: %+v, %v", blocks, err)
	}
}

// A block holds back the mail that arrives in the inbox alone: mail the
// user or another mail client put in another folder stays where it is.
func TestBlockLeavesOtherFoldersAlone(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("Trash")
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "news@shop.example", false, "Lists"); err != nil {
		t.Fatal(err)
	}
	r.deliver("Trash", news("Weekly news 1", "n1@shop.example", 1))
	r.sync("Trash")
	if got := r.onServer("Trash"); !slices.Contains(got, "Weekly news 1") {
		t.Errorf("Trash lost the newsletter: %v", got)
	}
	if got := r.onServer("Lists"); slices.Contains(got, "Weekly news 1") {
		t.Errorf("the block filed mail of another folder: Lists holds %v", got)
	}
}

// A block keeps the watcher quiet: the mail is filed before it may be
// announced.
func TestBlockKeepsTheWatcherQuiet(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Trash")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	ctx := context.Background()
	if _, err := r.svc.SyncAccount(ctx, r.account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.BlockSender(ctx, r.account.ID, "shop.example", true, "Trash"); err != nil {
		t.Fatal(err)
	}
	r.watch()
	r.checked()
	r.timer()
	deliver(t, f, "INBOX", news("Sale", "s1@shop.example", 1))
	deliver(t, f, "INBOX", ruleMail("Dinner", "Bob <bob@example.org>", "b1@example.org", "", 2))
	var announced []string
	r.event("the dinner", func(ev WatchEvent) bool {
		announced = append(announced, subjects(ev.New)...)
		return slices.Contains(subjects(ev.New), "Dinner")
	})
	if !slices.Equal(announced, []string{"Dinner"}) {
		t.Fatalf("announced %v, want the dinner alone", announced)
	}
	waitFor(t, "the block to file the sale", func() bool {
		for _, m := range f.messages(t, "Trash") {
			if m.Subject == "Sale" {
				return true
			}
		}
		return false
	})
}

// A block of a folder the account has not is no block.
func TestBlockNeedsAFolder(t *testing.T) {
	r := newRuleRig(t, allCaps())
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "news@shop.example", false, "Nowhere"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a block of a folder the account has not = %v", err)
	}
}

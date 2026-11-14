package mail

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
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
	startQueueRun(t, r.svc, r.a.ID)
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

// A block's move runs on the account's worker, so the check that found
// the mail ends without waiting for the server.
func TestBlockedFilingRunsOutsideTheArrivalLock(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Trash")
	svc, _, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	// A first sync caches the folder; only mail that arrives after it is
	// sorted and filed.
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	startQueueRun(t, svc, a.ID)
	if _, err := svc.BlockSender(ctx, a.ID, "shop.example", true, "Trash"); err != nil {
		t.Fatal(err)
	}
	deliver(t, f, "INBOX", news("Sale", "s1@shop.example", 1))
	started := make(chan struct{})
	release := make(chan struct{})
	f.mu.Lock()
	f.onMove = func() {
		close(started)
		<-release
	}
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- svc.SyncFolder(ctx, a.ID, "INBOX", 50) }()
	// The move waits at the server here; the check must end all the same.
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check waited for the blocked move")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked move never reached the server")
	}
	if !svc.arrivalMu.TryLock() {
		t.Error("the arrival lock was held while the block moved the mail")
	} else {
		svc.arrivalMu.Unlock()
	}
	close(release)
	waitFor(t, "the filed mail", func() bool {
		return slices.Contains(serverSubjects(f.messages(t, "Trash")), "Sale")
	})
}

// serverSubjects names the messages of a folder of the server.
func serverSubjects(list []serverMessage) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.Subject)
	}
	return out
}

// A move the server refuses comes back to the list and stays in the queue
// as failed, with its reason, for the window to show.
func TestBlockedMoveTheServerRefusesIsShown(t *testing.T) {
	r := newRuleRig(t, allCaps())
	startQueueRun(t, r.svc, r.a.ID)
	r.sync("INBOX")
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "shop.example", true, "Lists"); err != nil {
		t.Fatal(err)
	}
	// The block's folder goes on the server after the block was made.
	if err := r.f.user.Delete("Lists"); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	r.sync("INBOX")
	waitFor(t, "the refused move", func() bool {
		ops, err := r.svc.store.Operations(r.ctx, r.a.ID)
		if err != nil || len(ops) != 1 || ops[0].State != OperationFailed || ops[0].LastError == "" {
			return false
		}
		return slices.Contains(r.cached("INBOX"), "Sale")
	})
}

// A sync of an account no watcher runs on leaves the blocked mail in the
// list: with no worker to file it, the move waits in the queue instead of
// hiding a row nothing would bring back.
func TestBlockWaitsForAWorkerWithoutAWatcher(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "shop.example", true, "Trash"); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	r.sync("INBOX")
	if got := r.cached("INBOX"); !slices.Contains(got, "Sale") {
		t.Errorf("the block hid mail no worker would file: %v", got)
	}
	if ops, err := r.svc.store.Operations(r.ctx, r.a.ID); err != nil || len(ops) != 1 ||
		ops[0].State != OperationPending || ops[0].Kind != OpMove {
		t.Errorf("queue = %+v, %v", ops, err)
	}
	// The worker a watcher starts files the mail it waited for.
	startQueueRun(t, r.svc, r.a.ID)
	waitFor(t, "the filed mail", func() bool {
		return slices.Contains(r.onServer("Trash"), "Sale")
	})
}

// A block of a folder the account has not is no block.
func TestBlockNeedsAFolder(t *testing.T) {
	r := newRuleRig(t, allCaps())
	if _, err := r.svc.BlockSender(r.ctx, r.a.ID, "news@shop.example", false, "Nowhere"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a block of a folder the account has not = %v", err)
	}
}

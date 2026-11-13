package mail

import (
	"context"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// Taking back a flag the server was never told of drops its action: the
// cache shows what it did before the change, and the server never hears
// of it.
func TestUndoAFlagWaitingForTheServer(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	svc.SetClock(newTestClock().Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	refs := []MessageRef{{"INBOX", msgs[0].UID}}
	f.stop()
	if err := svc.SetFlags(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatalf("a star with the server away = %v", err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 1 || ops[0].Kind != OpFlags {
		t.Fatalf("queued = %+v", ops)
	}
	if cached, _, _ := store.GetMessage(ctx, a.ID, "INBOX", refs[0].UID); !cached.Flagged {
		t.Fatal("the cache did not show the star")
	}
	if err := svc.UndoFlagChange(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 0 {
		t.Fatalf("after the undo = %+v", ops)
	}
	if cached, _, _ := store.GetMessage(ctx, a.ID, "INBOX", refs[0].UID); cached.Flagged {
		t.Error("the undo left the star in the cache")
	}
	f.restart(t)
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("the server heard of a star taken back: %v", got)
	}
}

// A flag the server has is set back: the undo makes the other change.
func TestUndoAFlagTheServerHas(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml", "gmail-3.eml"))
	svc.SetClock(newTestClock().Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	refs := []MessageRef{{"INBOX", msgs[0].UID}}
	if err := svc.SetFlags(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	before := len(flaggedOn(t, f, "INBOX", imap.FlagFlagged))
	if before != 1 {
		t.Fatalf("starred on the server = %d, want one", before)
	}
	if err := svc.UndoFlagChange(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("the server's stars = %v, want none", got)
	}
	if cached, _, _ := store.GetMessage(ctx, a.ID, "INBOX", refs[0].UID); cached.Flagged {
		t.Error("the cache kept the star")
	}
	if ops := queued(t, store, a.ID); len(ops) != 0 {
		t.Errorf("made online = %+v", ops)
	}
}

// A change the server refused changed nothing: taking it back has only
// to drop it, and touches neither the cache nor the server.
func TestUndoAFlagTheServerRefused(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := threadService(t, allCaps(), in("INBOX", "gmail-1.eml"))
	svc.SetClock(newTestClock().Now)
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("cached = %d, %v", len(msgs), err)
	}
	refs := []MessageRef{{"INBOX", msgs[0].UID}}
	f.stop()
	if err := svc.SetFlags(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	ops := queued(t, store, a.ID)
	if len(ops) != 1 {
		t.Fatalf("queued = %+v", ops)
	}
	// As the worker leaves it when the server refuses: the cache is back.
	if err := store.FailOperation(ctx, ops[0].ID, "the server refused it"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFlag(ctx, a.ID, "INBOX", refs[0].UID, FlagFlagged, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.UndoFlagChange(ctx, a.ID, refs, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if ops := queued(t, store, a.ID); len(ops) != 0 {
		t.Fatalf("after the undo = %+v", ops)
	}
	f.restart(t)
	if got := flaggedOn(t, f, "INBOX", imap.FlagFlagged); len(got) != 0 {
		t.Errorf("the server's stars = %v, want none", got)
	}
}

// A label taken back while it waits for the server is dropped, and the
// server never hears of it.
func TestUndoALabelWaitingForTheServer(t *testing.T) {
	r, store := newLabelRig(t, nil, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1)},
	})
	r.svc.SetClock(newTestClock().Now)
	work := r.create("Work")
	one := r.message("INBOX", "One")
	refs := []MessageRef{{"INBOX", one.UID}}
	r.f.stop()
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatalf("a label with the server away = %v", err)
	}
	if ops := queued(t, store, r.a.ID); len(ops) != 1 || ops[0].Kind != OpLabel {
		t.Fatalf("queued = %+v", ops)
	}
	if err := r.svc.UndoLabelChange(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if ops := queued(t, store, r.a.ID); len(ops) != 0 {
		t.Fatalf("after the undo = %+v", ops)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); len(got) != 0 {
		t.Errorf("the cache kept the label: %v", got)
	}
}

// A label the server has is taken off by the undo.
func TestUndoALabelTheServerHas(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1)},
	})
	r.svc.SetClock(newTestClock().Now)
	work := r.create("Work")
	one := r.message("INBOX", "One")
	refs := []MessageRef{{"INBOX", one.UID}}
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.UndoLabelChange(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); len(got) != 0 {
		t.Errorf("the cache kept the label: %v", got)
	}
	if got := keywordsOn(t, r.f, "INBOX")["One"]; len(got) != 0 {
		t.Errorf("the server's keywords = %v, want none", got)
	}
}

// Taking a label off, then taking that back before the server is told,
// puts it back on the cache and leaves the server alone.
func TestUndoTakingALabelOff(t *testing.T) {
	r, store := newLabelRig(t, nil, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1)},
	})
	r.svc.SetClock(newTestClock().Now)
	work := r.create("Work")
	one := r.message("INBOX", "One")
	refs := []MessageRef{{"INBOX", one.UID}}
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	r.f.stop()
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refs, work.ID, false); err != nil {
		t.Fatalf("taking it off with the server away = %v", err)
	}
	if ops := queued(t, store, r.a.ID); len(ops) != 1 || ops[0].On {
		t.Fatalf("queued = %+v", ops)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); len(got) != 0 {
		t.Fatalf("the cache kept the label: %v", got)
	}
	if err := r.svc.UndoLabelChange(r.ctx, r.a.ID, refs, work.ID, false); err != nil {
		t.Fatal(err)
	}
	if ops := queued(t, store, r.a.ID); len(ops) != 0 {
		t.Fatalf("after the undo = %+v", ops)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("labels = %v, want it back", got)
	}
}

// A label a folder keeps on this device alone goes back the same way.
func TestUndoALabelKeptHere(t *testing.T) {
	r, _ := newLabelRig(t, map[string]fakeKeywords{"INBOX": keepsNone}, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1)},
	})
	r.svc.SetClock(newTestClock().Now)
	work := r.create("Work")
	one := r.message("INBOX", "One")
	refs := []MessageRef{{"INBOX", one.UID}}
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"Work (here)"}) {
		t.Fatalf("labels = %v", got)
	}
	if err := r.svc.UndoLabelChange(r.ctx, r.a.ID, refs, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); len(got) != 0 {
		t.Errorf("the label stayed: %v", got)
	}
}

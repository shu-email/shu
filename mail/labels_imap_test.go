package mail

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// newLabelRig is an account on a fake server with mailboxes, each with
// the messages given, of which it syncs every one. Mailboxes named in
// keeps keep keywords so; the others any.
func newLabelRig(t *testing.T, keeps map[string]fakeKeywords, boxes map[string][][]byte) (*labelRig, *memStore) {
	t.Helper()
	f := startFakeIMAP(t, allCaps())
	var names []string
	for name := range boxes {
		names = append(names, name)
	}
	slices.Sort(names)
	f.create(t, names...)
	for _, name := range names {
		for _, raw := range boxes[name] {
			f.append(t, name, raw)
		}
	}
	for name, k := range keeps {
		f.keepKeywords(name, k)
	}
	svc, store, a := newIMAPService(t, f, "secret")
	r := &labelRig{t: t, ctx: context.Background(), svc: svc, f: f, a: a}
	r.sync(names...)
	return r, store
}

func (r *labelRig) create(name string) Label {
	r.t.Helper()
	l, err := r.svc.CreateLabel(r.ctx, r.a.ID, name, "")
	if err != nil {
		r.t.Fatal(err)
	}
	return l
}

// A label goes on the messages of a conversation with one connection, a
// read-write SELECT and one silent STORE of its keyword alone in each
// folder, and comes off them so. Nothing else changes on the server.
func TestSetLabelOneStorePerFolder(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{
		"INBOX":   {labelMail("One", "a@example.org", "one@x", 1), labelMail("Two", "a@example.org", "two@x", 2), labelMail("Other", "a@example.org", "other@x", 3)},
		"Archive": {labelMail("Three", "a@example.org", "three@x", 4)},
	})
	work := r.create("Work")
	msgs := []Summary{r.message("INBOX", "One"), r.message("Archive", "Three"), r.message("INBOX", "Two")}
	sessions, selects, stores := r.f.sessions.Load(), r.f.selectCount(), r.f.storeCount()
	r.set(work.ID, true, msgs...)
	if n := r.f.sessions.Load() - sessions; n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}
	if got := r.f.selectsSince(selects); !slices.Equal(got, []fakeSelect{{"INBOX", false}, {"Archive", false}}) {
		t.Errorf("selects = %+v", got)
	}
	want := []fakeStore{{"INBOX", imap.StoreFlagsAdd, []imap.Flag{"Work"}}, {"Archive", imap.StoreFlagsAdd, []imap.Flag{"Work"}}}
	if got := r.f.storesSince(stores); !reflectEqual(got, want) {
		t.Errorf("stores = %+v", got)
	}
	inbox := keywordsOn(t, r.f, "INBOX")
	if !slices.Equal(inbox["One"], []string{"work"}) || !slices.Equal(inbox["Two"], []string{"work"}) || len(inbox["Other"]) != 0 {
		t.Errorf("the server's inbox = %v", inbox)
	}
	if got := keywordsOn(t, r.f, "Archive")["Three"]; !slices.Equal(got, []string{"work"}) {
		t.Errorf("the server's archive = %v", got)
	}
	for _, m := range msgs {
		if got := r.labelsOf(r.message(m.Folder, m.Subject)); !slices.Equal(got, []string{"Work"}) {
			t.Errorf("%s has %v in the cache", m.Subject, got)
		}
	}
	if got := r.labelsOf(r.message("INBOX", "Other")); len(got) != 0 {
		t.Errorf("another message has %v", got)
	}

	stores = r.f.storeCount()
	r.set(work.ID, false, msgs...)
	for _, s := range r.f.storesSince(stores) {
		if s.op != imap.StoreFlagsDel || !reflectEqual(s.flags, []imap.Flag{"Work"}) {
			t.Errorf("taking the label off stored %+v", s)
		}
	}
	for _, box := range []string{"INBOX", "Archive"} {
		for subject, kws := range keywordsOn(t, r.f, box) {
			if len(kws) != 0 {
				t.Errorf("%s/%s keeps %v", box, subject, kws)
			}
		}
	}
	if got := r.label("Work"); got.Unread != 0 {
		t.Errorf("Work counts %d unread", got.Unread)
	}
	// Nothing to change opens no connection.
	sessions = r.f.sessions.Load()
	if err := r.svc.SetLabel(r.ctx, r.a.ID, nil, work.ID, true); err != nil || r.f.sessions.Load() != sessions {
		t.Errorf("an empty change: %v, %d connections", err, r.f.sessions.Load()-sessions)
	}
}

func reflectEqual[T any](a, b []T) bool { return reflect.DeepEqual(a, b) }

// What the server refuses comes back off the cache, and what a change
// refuses or fails on leaves the rest as it was: a server that refuses
// the keyword, a folder whose UIDs mean other messages now, a cancelled
// context, a label of another account, a folder the cache does not have.
func TestSetLabelFailures(t *testing.T) {
	r, store := newLabelRig(t, map[string]fakeKeywords{"Archive": {any: true, refuse: true}}, map[string][][]byte{
		"INBOX":   {labelMail("One", "a@example.org", "one@x", 1)},
		"Archive": {labelMail("Two", "a@example.org", "two@x", 2)},
		"Sent":    {labelMail("Three", "a@example.org", "three@x", 3)},
	})
	work := r.create("Work")
	one, two, three := r.message("INBOX", "One"), r.message("Archive", "Two"), r.message("Sent", "Three")

	// The inbox, before the archive that refuses, keeps its change.
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refsOf([]Summary{one, two, three}), work.ID, true); err == nil {
		t.Fatal("a refused keyword was put on")
	}
	if got := r.labelsOf(r.message("Archive", "Two")); len(got) != 0 {
		t.Errorf("the refused message has %v in the cache", got)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("the message before the failure has %v", got)
	}
	if got := keywordsOn(t, r.f, "Sent")["Three"]; len(got) != 0 {
		t.Errorf("the folder after the failure has %v", got)
	}

	// Taking it off a folder the server refuses puts it back too.
	r.f.keepKeywords("Archive", fakeKeywords{any: true})
	r.set(work.ID, true, two)
	r.f.keepKeywords("Archive", fakeKeywords{any: true, refuse: true})
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refsOf([]Summary{two}), work.ID, false); err == nil {
		t.Fatal("a refused keyword came off")
	}
	if got := r.labelsOf(r.message("Archive", "Two")); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("after a refused removal, Two has %v", got)
	}

	// Sent is made again: its UID 1 is another message now.
	if err := r.f.user.Delete("Sent"); err != nil {
		t.Fatal(err)
	}
	r.f.create(t, "Sent")
	r.f.append(t, "Sent", labelMail("Stranger", "s@example.org", "stranger@x", 9))
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refsOf([]Summary{three}), work.ID, true); !errors.Is(err, errValidityChanged) {
		t.Errorf("a change under a new UIDVALIDITY = %v", err)
	}
	if got := keywordsOn(t, r.f, "Sent")["Stranger"]; len(got) != 0 {
		t.Errorf("another message got %v", got)
	}
	if got := r.labelsOf(r.message("Sent", "Three")); len(got) != 0 {
		t.Errorf("the cache kept %v", got)
	}

	cancelled, cancel := context.WithCancel(r.ctx)
	cancel()
	if err := r.svc.SetLabel(cancelled, r.a.ID, refsOf([]Summary{one}), work.ID, false); err == nil {
		t.Error("a cancelled change succeeded")
	}
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("a cancelled change left %v", got)
	}
	if err := r.svc.SetLabel(r.ctx, r.a.ID, []MessageRef{{"Nowhere", 1}}, work.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("a folder never cached = %v", err)
	}
	if err := store.SaveAccount(r.ctx, Account{ID: "a2", Name: "Other", Email: "o@example.org"}); err != nil {
		t.Fatal(err)
	}
	other, err := r.svc.CreateLabel(r.ctx, "a2", "Work", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refsOf([]Summary{one}), other.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account's label = %v", err)
	}
	if err := r.svc.DeleteLabel(r.ctx, r.a.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting another account's label = %v", err)
	}
}

// A folder whose PERMANENTFLAGS name some keywords and not \* keeps those
// on the server, and the others on this device; a folder never opened to
// change says how it keeps them as a label is put on its messages.
func TestFoldersThatListTheirKeywords(t *testing.T) {
	r, store := newLabelRig(t, map[string]fakeKeywords{"INBOX": {listed: []string{"$label1"}}, "Notes": keepsNone}, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1)},
		"Notes": {labelMail("Two", "a@example.org", "two@x", 2)},
	})
	r.f.flag(t, "INBOX", 1, "$label1")
	r.sync("INBOX")
	important, work := r.label("Important"), r.create("Work")
	one := r.message("INBOX", "One")
	r.set(work.ID, true, one)
	r.set(important.ID, false, one)
	if got := keywordsOn(t, r.f, "INBOX")["One"]; len(got) != 0 {
		t.Errorf("the server's One has %v", got)
	}
	r.set(important.ID, true, one)
	if got := keywordsOn(t, r.f, "INBOX")["One"]; !slices.Equal(got, []string{"$label1"}) {
		t.Errorf("the server's One has %v", got)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"Important", "Work (here)"}) {
		t.Errorf("One has %v", got)
	}
	// Notes keeps no keyword and the inbox none but Thunderbird's.
	if r.label("Important").Local || !r.label("Work").Local || !r.message("INBOX", "One").LabelsLocal {
		t.Errorf("Important %+v, Work %+v", r.label("Important"), r.label("Work"))
	}

	// Notes was never opened to change, as far as the cache knows.
	if err := store.SetPermanentFlags(r.ctx, r.a.ID, "Notes", nil); err != nil {
		t.Fatal(err)
	}
	stores := r.f.storeCount()
	r.set(work.ID, true, r.message("Notes", "Two"))
	if got := r.f.storesSince(stores); len(got) != 0 {
		t.Errorf("stored %+v in a folder that keeps no keyword", got)
	}
	if f, _, _ := store.Folder(r.ctx, r.a.ID, "Notes"); !f.KeywordsKnown() || f.KeepsKeyword("Work") {
		t.Errorf("Notes = %+v", f)
	}
	if got := r.labelsOf(r.message("Notes", "Two")); !slices.Equal(got, []string{"Work (here)"}) {
		t.Errorf("Two has %v", got)
	}
}

// What a folder keeps is learnt as it is opened to change, as a sync or a
// change of labels opens it; the watcher and the fetch of older mail open
// it read-only, where a server says nothing can change, and leave what was
// learnt as it was, while they bring the keywords of what they fetch.
func TestReadOnlyOpensSayNothingOfKeywords(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	f.append(t, "INBOX", labelMail("One", "a@example.org", "one@x", 1))
	f.appendFlagged(t, "Archive", labelMail("Old", "a@example.org", "old@x", 0), "Work")
	w := newWatchRig(t, f, "secret", false)
	r := &labelRig{t: t, ctx: context.Background(), svc: w.svc, f: f, a: w.account}
	r.sync("INBOX")
	keeps := func(when string) {
		t.Helper()
		if folder, _, _ := w.store.Folder(r.ctx, r.a.ID, "INBOX"); !folder.KeepsNewKeywords() {
			t.Errorf("%s, the inbox keeps %v", when, folder.PermanentFlags)
		}
	}
	keeps("synced")
	f.appendFlagged(t, "INBOX", labelMail("Two", "a@example.org", "two@x", 2), "Work")
	w.watch()
	w.checked()
	w.svc.Unwatch(r.a.ID)
	keeps("watched")
	if got := r.labelsOf(r.message("INBOX", "Two")); !slices.Equal(got, []string{"work"}) {
		t.Errorf("the watcher brought %v", got)
	}
	if _, err := r.svc.FetchOlder(r.ctx, r.a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	keeps("after older mail")
	// A folder never synced is synced read-only by a fetch of its older
	// mail, which learns nothing of what it keeps.
	if got, err := r.svc.FetchOlder(r.ctx, r.a.ID, "Archive", 0); err != nil || !got.Reset {
		t.Fatalf("older mail of a folder never synced = %+v, %v", got, err)
	}
	if folder, _, _ := w.store.Folder(r.ctx, r.a.ID, "Archive"); folder.KeywordsKnown() {
		t.Errorf("examined, the archive keeps %v", folder.PermanentFlags)
	}
	if got := r.labelsOf(r.message("Archive", "Old")); !slices.Equal(got, []string{"work"}) {
		t.Errorf("older mail brought %v", got)
	}
}

// Keywords another mail client sets and takes off reach the labels with
// the next sync's refresh of flags; a keyword set in another case is the
// label's, and one taken off leaves its label for the other messages.
func TestKeywordsChangedElsewhere(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{
		"INBOX": {labelMail("One", "a@example.org", "one@x", 1), labelMail("Two", "a@example.org", "two@x", 2)},
	})
	work := r.create("Work")
	r.set(work.ID, true, r.message("INBOX", "One"))
	r.f.flag(t, "INBOX", 2, "WORK")
	r.f.flag(t, "INBOX", 2, "Clients")
	r.f.flag(t, "INBOX", 2, "$MDNSent")
	r.f.unflag(t, "INBOX", 1, "work")
	r.sync("INBOX")
	if got := r.labelNames(); !slices.Equal(got, []string{"Work", "clients"}) {
		t.Errorf("labels = %v", got)
	}
	if got := r.labelsOf(r.message("INBOX", "Two")); !slices.Equal(got, []string{"Work", "clients"}) {
		t.Errorf("Two has %v", got)
	}
	if got := r.labelsOf(r.message("INBOX", "One")); len(got) != 0 {
		t.Errorf("One has %v after another client took Work off", got)
	}
}

// An account whose folders keep no keyword, as Outlook's, keeps its labels
// on this device and says so; they work without the server, which hears
// of none of them.
func TestLabelsOfAnAccountThatKeepsNone(t *testing.T) {
	r, _ := newLabelRig(t, map[string]fakeKeywords{"INBOX": keepsNone, "Archive": keepsNone}, map[string][][]byte{
		"INBOX":   {labelMail("One", "a@example.org", "one@x", 1)},
		"Archive": {labelMail("Two", "a@example.org", "two@x", 2)},
	})
	receipts := r.create("Receipts")
	if !receipts.Local || !r.label("Receipts").Local {
		t.Errorf("Receipts = %+v", receipts)
	}
	stores := r.f.storeCount()
	r.f.stop()
	r.set(receipts.ID, true, r.message("INBOX", "One"), r.message("Archive", "Two"))
	for _, box := range []string{"INBOX", "Archive"} {
		for _, m := range r.page(AccountScope(r.a.ID, box), Search{}) {
			if got := r.labelsOf(m); !slices.Equal(got, []string{"Receipts (here)"}) || !m.LabelsLocal {
				t.Errorf("%s has %v, local %v", m.Subject, got, m.LabelsLocal)
			}
		}
	}
	r.set(receipts.ID, false, r.message("Archive", "Two"))
	if got := r.labelsOf(r.message("Archive", "Two")); len(got) != 0 {
		t.Errorf("Two has %v", got)
	}
	if err := r.svc.DeleteLabel(r.ctx, r.a.ID, receipts.ID); err != nil {
		t.Fatalf("deleting a label kept here without the server: %v", err)
	}
	r.f.restart(t)
	if n := r.f.storeCount() - stores; n != 0 {
		t.Errorf("%d stores reached the server", n)
	}
	if got := r.labelNames(); len(got) != 0 {
		t.Errorf("labels = %v", got)
	}
}

// Deleting a label takes its keyword off every message of every folder of
// the server, those the cache never had too, and nothing else. A folder
// that fails leaves the label, partly removed, and deleting it again
// finishes.
func TestDeleteLabelPartlyAndAgain(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{
		"INBOX":   {labelMail("One", "a@example.org", "one@x", 1), labelMail("Two", "a@example.org", "two@x", 2)},
		"Archive": {labelMail("Three", "a@example.org", "three@x", 3)},
	})
	work := r.create("Work")
	r.set(work.ID, true, r.message("INBOX", "One"), r.message("Archive", "Three"))
	r.f.flag(t, "INBOX", 1, imap.FlagFlagged)
	r.f.flag(t, "INBOX", 2, imap.FlagSeen)
	r.f.create(t, "Projects")
	r.f.appendFlagged(t, "Projects", labelMail("Four", "a@example.org", "four@x", 4), "Work", "Other")
	boxes := []string{"INBOX", "Archive", "Projects"}
	before := serverMail(t, r.f, boxes...)

	r.f.keepKeywords("Projects", fakeKeywords{any: true, refuse: true})
	stores := r.f.storeCount()
	err := r.svc.DeleteLabel(r.ctx, r.a.ID, work.ID)
	if !errors.Is(err, ErrLabelPartlyRemoved) {
		t.Fatalf("a deletion the server refused in part = %v", err)
	}
	if got := r.label("Work"); got.ID != work.ID {
		t.Errorf("the label after the failure = %+v", got)
	}
	if got := keywordsOn(t, r.f, "Projects")["Four"]; !slices.Equal(got, []string{"other", "work"}) {
		t.Errorf("the refused folder has %v", got)
	}
	r.f.keepKeywords("Projects", fakeKeywords{any: true})
	if err := r.svc.DeleteLabel(r.ctx, r.a.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.f.storesSince(stores) {
		if s.op != imap.StoreFlagsDel || !reflectEqual(s.flags, []imap.Flag{"Work"}) {
			t.Errorf("the deletion stored %+v", s)
		}
	}
	for _, box := range boxes {
		for subject, kws := range keywordsOn(t, r.f, box) {
			if slices.Contains(kws, "work") {
				t.Errorf("%s/%s keeps %v", box, subject, kws)
			}
		}
	}
	if got := keywordsOn(t, r.f, "Projects")["Four"]; !slices.Equal(got, []string{"other"}) {
		t.Errorf("Four has %v", got)
	}
	if after := serverMail(t, r.f, boxes...); !reflect.DeepEqual(after, before) {
		t.Errorf("the deletion changed the mail:\n was %v\n now %v", before, after)
	}
	if got := r.labelNames(); len(got) != 0 {
		t.Errorf("labels = %v", got)
	}
	if err := r.svc.DeleteLabel(r.ctx, r.a.ID, work.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting it a third time = %v", err)
	}
	// The first failure, before any keyword came off, is no partial one.
	again := r.create("Again")
	r.set(again.ID, true, r.message("INBOX", "One"))
	r.f.stop()
	if err := r.svc.DeleteLabel(r.ctx, r.a.ID, again.ID); err == nil || errors.Is(err, ErrLabelPartlyRemoved) {
		t.Errorf("a deletion without the server = %v", err)
	}
	if got := r.label("Again"); got.ID != again.ID {
		t.Errorf("Again = %+v", got)
	}
}

// A sync that read the server before a label was deleted, and writes
// after, does not make the label again from what it read.
func TestDeletedLabelDoesNotComeBack(t *testing.T) {
	r, store := newLabelRig(t, nil, map[string][][]byte{"INBOX": {labelMail("One", "a@example.org", "one@x", 1)}})
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	r.svc.SetClock(func() time.Time { return now })
	work := r.create("Work")
	r.set(work.ID, true, r.message("INBOX", "One"))
	store.beforeApply = func() {
		now = now.Add(time.Second)
		if err := r.svc.DeleteLabel(r.ctx, r.a.ID, work.ID); err != nil {
			t.Errorf("delete during the sync: %v", err)
		}
		now = now.Add(time.Second)
	}
	r.sync("INBOX")
	if got := r.labelNames(); len(got) != 0 {
		t.Errorf("the sync made %v again", got)
	}
	// Once the deletion is over, the keyword set again is a label.
	r.f.flag(t, "INBOX", 1, "Work")
	r.sync("INBOX")
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"work"}) {
		t.Errorf("set again, One has %v", got)
	}
}

// Labels are made, renamed and colored on this device: a rename keeps the
// keyword, so the server's messages keep the label, and a new label of
// the old name gets a keyword of its own.
func TestEditLabels(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{"INBOX": {labelMail("One", "a@example.org", "one@x", 1)}})
	work := r.create("Work")
	r.set(work.ID, true, r.message("INBOX", "One"))
	for _, tc := range []struct {
		name  string
		color LabelColor
		err   error
	}{
		{"", "", ErrLabelName}, {"a\nb", "", ErrLabelName}, {"WORK", "", ErrLabelExists}, {"New", "mauve", ErrLabelColor},
	} {
		if _, err := r.svc.CreateLabel(r.ctx, r.a.ID, tc.name, tc.color); !errors.Is(err, tc.err) {
			t.Errorf("CreateLabel(%q, %q) = %v, want %v", tc.name, tc.color, err, tc.err)
		}
	}
	if _, err := r.svc.CreateLabel(r.ctx, "nobody", "Work", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a label of no account = %v", err)
	}
	stores := r.f.storeCount()
	clients, err := r.svc.UpdateLabel(r.ctx, r.a.ID, work.ID, "Clients", LabelTeal)
	if err != nil {
		t.Fatal(err)
	}
	if clients.Name != "Clients" || clients.Keyword != "Work" || clients.Color != LabelTeal || clients.ID != work.ID {
		t.Errorf("renamed = %+v", clients)
	}
	if n := r.f.storeCount() - stores; n != 0 {
		t.Errorf("a rename stored %d times", n)
	}
	if again, err := r.svc.UpdateLabel(r.ctx, r.a.ID, work.ID, "clients", ""); err != nil || again.Color != LabelTeal || again.Name != "clients" {
		t.Errorf("a change of case = %+v, %v", again, err)
	}
	r.sync("INBOX")
	if got := r.labelsOf(r.message("INBOX", "One")); !slices.Equal(got, []string{"clients"}) {
		t.Errorf("synced after the rename, One has %v", got)
	}
	newWork := r.create("Work")
	if newWork.Keyword != "Work-2" {
		t.Errorf("the new Work = %+v", newWork)
	}
	if _, err := r.svc.UpdateLabel(r.ctx, r.a.ID, newWork.ID, "CLIENTS", ""); !errors.Is(err, ErrLabelExists) {
		t.Errorf("a rename to another's name = %v", err)
	}
	for _, tc := range []struct {
		name  string
		color LabelColor
		err   error
	}{{"", "", ErrLabelName}, {"Ok", "mauve", ErrLabelColor}} {
		if _, err := r.svc.UpdateLabel(r.ctx, r.a.ID, newWork.ID, tc.name, tc.color); !errors.Is(err, tc.err) {
			t.Errorf("UpdateLabel(%q, %q) = %v", tc.name, tc.color, err)
		}
	}
	if _, err := r.svc.UpdateLabel(r.ctx, r.a.ID, 999, "Gone", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a label that is not there = %v", err)
	}
}

// A label's list reads its messages of every folder but Junk, Trash and
// Drafts; a conversation's row there is made of them, and carries the
// labels of the messages it is made of; a conversation-wide action acts
// on those.
func TestLabelLists(t *testing.T) {
	r, _ := newLabelRig(t, nil, map[string][][]byte{
		"INBOX": {labelMail("Plan", "a@example.org", "plan@x", 1), labelMail("Other", "a@example.org", "other@x", 4)},
		"Sent":  {[]byte("From: Ann <ann@example.com>\r\nTo: a@example.org\r\nSubject: Re: Plan\r\nMessage-ID: <reply@x>\r\nIn-Reply-To: <plan@x>\r\nDate: Mon, 02 Mar 2026 09:02:00 +0000\r\n\r\nYes.\r\n")},
		"Trash": {labelMail("Old plan", "a@example.org", "old@x", 3)},
	})
	work, later := r.create("Work"), r.create("Later")
	plan, reply := r.message("INBOX", "Plan"), r.message("Sent", "Re: Plan")
	r.set(work.ID, true, plan, reply, r.message("Trash", "Old plan"))
	r.set(later.ID, true, reply)
	if got := r.subjectsIn(LabelScope(r.a.ID, work.ID), Search{}); !slices.Equal(got, []string{"Re: Plan", "Plan"}) {
		t.Errorf("Work lists %v", got)
	}
	if got := r.label("Work"); got.Unread != 2 {
		t.Errorf("Work counts %d unread", got.Unread)
	}
	page, err := r.svc.ConversationPage(r.ctx, ListQuery{Scope: LabelScope(r.a.ID, later.ID)})
	if err != nil || len(page.Conversations) != 1 {
		t.Fatalf("Later's conversations = %+v, %v", page, err)
	}
	c := page.Conversations[0]
	if c.Count != 1 || c.Latest.Subject != "Re: Plan" || !slices.Equal(c.Labels, []LabelID{work.ID, later.ID}) {
		t.Errorf("Later's row = %+v", c)
	}
	scoped, err := r.svc.ScopedConversation(r.ctx, r.a.ID, LabelScope(r.a.ID, later.ID), c.Thread)
	if err != nil || len(scoped) != 1 || scoped[0].Subject != "Re: Plan" {
		t.Errorf("Later's conversation = %v, %v", subjects(scoped), err)
	}
	scoped, err = r.svc.ScopedConversation(r.ctx, r.a.ID, LabelScope(r.a.ID, work.ID), c.Thread)
	if err != nil || !slices.Equal(subjects(scoped), []string{"Plan", "Re: Plan"}) {
		t.Errorf("Work's conversation = %v, %v", subjects(scoped), err)
	}
	inbox, err := r.svc.ConversationPage(r.ctx, ListQuery{Scope: AccountScope(r.a.ID, "INBOX")})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range inbox.Conversations {
		want := []LabelID(nil)
		if row.Latest.Subject == "Plan" {
			want = []LabelID{work.ID}
		}
		if !slices.Equal(row.Labels, want) {
			t.Errorf("the inbox's row of %s has %v", row.Latest.Subject, row.Labels)
		}
	}
	if got := r.subjectsIn(LabelScope("someone else", work.ID), Search{}); len(got) != 0 {
		t.Errorf("Work in another account's scope lists %v", got)
	}
	if got := r.subjectsIn(LabelScope(r.a.ID, 999), Search{}); len(got) != 0 {
		t.Errorf("a label that is not there lists %v", got)
	}
}

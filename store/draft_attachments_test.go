package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"shu/internal/sqlitetest"
	"shu/mail"

	"github.com/egoist/mygo/plugins/sqlite"
)

// hashes are content hashes as the draft file store names its files.
var hashes = []string{
	strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64),
}

func testAttachments() []mail.Attachment {
	return []mail.Attachment{
		{Filename: "季度计划 第一版.pdf", ContentType: "application/pdf", Size: 5 << 20, ContentHash: hashes[0]},
		{Filename: `she said "hi"; ok.txt`, ContentType: "text/plain; charset=utf-8", Size: 0, ContentHash: hashes[1]},
		{Filename: "a.bin", ContentType: "application/octet-stream", Size: 7, ContentHash: hashes[2]},
	}
}

// sameRows reports whether a draft's attachments are the wanted ones, in
// order. An Attachment may carry bytes, so == does not compare it; the
// rows of a draft never do.
func sameRows(got, want []mail.Attachment) bool {
	return slices.EqualFunc(got, want, func(a, b mail.Attachment) bool {
		return a.Filename == b.Filename && a.ContentType == b.ContentType && a.Size == b.Size &&
			a.ContentHash == b.ContentHash && a.Data == nil && b.Data == nil
	})
}

// The attachments of a draft come back as they were written and in the
// order they were added, from the read of one draft and from the list.
// Only SetDraftAttachments changes them once the draft exists.
func TestDraftAttachmentsRoundTripInOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	at := time.Unix(1_700_000_000, 0)
	want := testAttachments()
	saved, err := s.SaveDraft(ctx, mail.Draft{
		ID: "d1", AccountID: "a1", Subject: "With files", UpdatedAt: at, Attachments: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameRows(saved.Attachments, want) || saved.Revision != 1 {
		t.Fatalf("a new draft = %+v", saved)
	}
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d2", AccountID: "a1", Subject: "Without", UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Draft(ctx, "d1")
	if err != nil || !ok || !sameRows(got.Attachments, want) {
		t.Fatalf("read back = %+v, %v, %v", got.Attachments, ok, err)
	}
	drafts, err := s.Drafts(ctx, "a1")
	if err != nil || len(drafts) != 2 {
		t.Fatalf("drafts = %+v, %v", drafts, err)
	}
	for _, d := range drafts {
		if d.ID == "d1" && !sameRows(d.Attachments, want) || d.ID == "d2" && len(d.Attachments) != 0 {
			t.Errorf("listed draft %s = %+v", d.ID, d.Attachments)
		}
	}

	// A save of the text carries a list that may be out of date: it
	// changes the text and leaves the files, whatever it lists.
	for _, stale := range [][]mail.Attachment{nil, want[:1], {want[2], want[0]}} {
		edited, err := s.SaveDraft(ctx, mail.Draft{
			ID: "d1", AccountID: "a1", Subject: "Edited", UpdatedAt: at.Add(time.Minute), Attachments: stale,
		})
		if err != nil {
			t.Fatal(err)
		}
		if edited.Subject != "Edited" || !sameRows(edited.Attachments, want) {
			t.Fatalf("after a save listing %d attachments = %+v", len(stale), edited)
		}
	}

	// A new list replaces the old one whole, in its own order, and
	// counts as an edit: the draft has to upload again.
	before, _, _ := s.Draft(ctx, "d1")
	before.SyncedRevision = before.Revision
	if err := s.SetDraftSync(ctx, before); err != nil {
		t.Fatal(err)
	}
	reordered := []mail.Attachment{want[2], want[0],
		{Filename: "new.png", ContentType: "image/png", Size: 9, ContentHash: hashes[3]}}
	set, err := s.SetDraftAttachments(ctx, "d1", reordered, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !sameRows(set.Attachments, reordered) || set.Revision != before.Revision+1 || !set.Dirty() ||
		!set.UpdatedAt.Equal(at.Add(time.Hour)) || set.Subject != "Edited" {
		t.Errorf("after a new list = %+v", set)
	}
	// The same file twice under two names is two rows.
	twice := []mail.Attachment{want[0], {Filename: "copy.pdf", ContentType: "application/pdf", Size: want[0].Size, ContentHash: hashes[0]}}
	if set, err = s.SetDraftAttachments(ctx, "d1", twice, at); err != nil || !sameRows(set.Attachments, twice) {
		t.Errorf("one file under two names = %+v, %v", set.Attachments, err)
	}
	// And none is none.
	if set, err = s.SetDraftAttachments(ctx, "d1", nil, at); err != nil || len(set.Attachments) != 0 {
		t.Errorf("an empty list = %+v, %v", set.Attachments, err)
	}
	if got, _, _ := s.Draft(ctx, "d2"); len(got.Attachments) != 0 {
		t.Errorf("another draft gained attachments: %+v", got.Attachments)
	}

	// A draft that is gone gets no rows: they would belong to nothing.
	if _, err := s.SetDraftAttachments(ctx, "missing", want, at); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("attachments of a missing draft = %v", err)
	}
	rows, err := s.query(ctx, `SELECT count(*) FROM draft_attachments WHERE draft_id = 'missing'`)
	if err != nil || number(rows.Values[0][0]) != 0 {
		t.Errorf("rows of a missing draft = %v, %v", rows.Values, err)
	}
}

// The rows go with their draft, and with the draft's account.
func TestDraftAttachmentsCascade(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	at := time.Unix(1_700_000_000, 0)
	list := testAttachments()
	for _, d := range []mail.Draft{
		{ID: "d1", AccountID: "a1", UpdatedAt: at, Attachments: list},
		{ID: "d2", AccountID: "a1", UpdatedAt: at, Attachments: list[:2]},
		{ID: "d3", AccountID: "a2", UpdatedAt: at, Attachments: list[:1]},
	} {
		if _, err := s.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	count := func() map[string]int64 {
		t.Helper()
		rows, err := s.query(ctx, `SELECT draft_id, count(*) FROM draft_attachments GROUP BY draft_id`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, r := range rows.Values {
			out[text(r[0])] = number(r[1])
		}
		return out
	}
	if got := count(); got["d1"] != 3 || got["d2"] != 2 || got["d3"] != 1 {
		t.Fatalf("rows = %v", got)
	}
	if err := s.DeleteDraft(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if got := count(); len(got) != 2 || got["d2"] != 2 || got["d3"] != 1 {
		t.Errorf("rows after the draft went = %v", got)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got := count(); len(got) != 1 || got["d3"] != 1 {
		t.Errorf("rows after the account went = %v", got)
	}
	if d, ok, err := s.Draft(ctx, "d3"); err != nil || !ok || !sameRows(d.Attachments, list[:1]) {
		t.Errorf("the other account's draft = %+v, %v, %v", d.Attachments, ok, err)
	}
}

// DraftFileRefs is what keeps a stored file: it names the files of the
// drafts still kept and of the messages still to deliver, and no others.
func TestDraftFileRefs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	at := time.Unix(1_700_000_000, 0)
	list := testAttachments()
	refs := func() []string {
		t.Helper()
		got, err := s.DraftFileRefs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		return slices.Compact(got)
	}
	if got := refs(); len(got) != 0 {
		t.Fatalf("refs of an empty database = %v", got)
	}
	// Two drafts share a file; a third is discarded and awaits the
	// deletion of its server copy.
	for _, d := range []mail.Draft{
		{ID: "d1", AccountID: "a1", UpdatedAt: at, Attachments: list[:1]},
		{ID: "d2", AccountID: "a1", UpdatedAt: at, Attachments: list[:1]},
		{ID: "d3", AccountID: "a1", UpdatedAt: at, Attachments: list[1:2], PendingDelete: true},
	} {
		if _, err := s.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if got := refs(); !slices.Equal(got, []string{hashes[0]}) {
		t.Errorf("refs of the drafts = %v", got)
	}
	// One of the two drafts goes: the other still needs the file.
	if err := s.DeleteDraft(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if got := refs(); !slices.Equal(got, []string{hashes[0]}) {
		t.Errorf("refs with one draft left = %v", got)
	}

	// An outbox entry with attachments keeps the envelope and the
	// names of its files, not the message: it needs them in every
	// state from which it may still be sent.
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1",
		Envelope: mail.Outgoing{
			To: []mail.Address{{Email: "bob@example.com"}}, Subject: "files", Attachments: list[1:],
		},
		CreatedAt: at,
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Outbox(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Raw) != 0 || !sameRows(stored.Envelope.Attachments, list[1:]) {
		t.Errorf("stored entry = raw of %d bytes, %+v", len(stored.Raw), stored.Envelope.Attachments)
	}
	withOutbox := []string{hashes[0], hashes[1], hashes[2]}
	for _, state := range []string{mail.StatePending, mail.StateSending, mail.StateFailed, mail.StateUnknown} {
		if err := s.SetOutboxState(ctx, "o1", state, ""); err != nil {
			t.Fatal(err)
		}
		if got := refs(); !slices.Equal(got, withOutbox) {
			t.Errorf("refs with the entry %s = %v", state, got)
		}
	}
	// Delivered, or cancelled before anything left: the entry is done
	// with the files.
	for _, state := range []string{mail.StateSent, mail.StateCancelled} {
		if err := s.SetOutboxState(ctx, "o1", state, ""); err != nil {
			t.Fatal(err)
		}
		if got := refs(); !slices.Equal(got, []string{hashes[0]}) {
			t.Errorf("refs with the entry %s = %v", state, got)
		}
	}

	// An envelope that cannot be read may name files: the answer is an
	// error, never a list that leaves them out.
	if err := s.SetOutboxState(ctx, "o1", mail.StateFailed, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, `UPDATE outbox SET envelope_json = '{"attachments": [' WHERE id = 'o1'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.DraftFileRefs(ctx); err == nil {
		t.Errorf("refs with an unreadable envelope = %v", got)
	}
}

// A send cancelled before anything left is over: it is not offered to
// send again.
func TestCancelledSendIsNotPending(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	for _, id := range []string{"o1", "o2"} {
		if err := s.EnqueueOutbox(ctx, mail.PendingSend{
			ID: id, AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
			Envelope:  mail.Outgoing{To: []mail.Address{{Email: "a@example.com"}}, Subject: id},
			CreatedAt: time.Unix(1_700_000_000, 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateCancelled, "cancelled"); err != nil {
		t.Fatal(err)
	}
	// A send cut off after its data started is of unknown outcome, and
	// stays to be looked at.
	if err := s.SetOutboxState(ctx, "o2", mail.StateUnknown, "cut off"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingSends(ctx, "a1")
	if err != nil || len(pending) != 1 || pending[0].ID != "o2" || pending[0].State != mail.StateUnknown {
		t.Errorf("pending = %+v, %v", pending, err)
	}
	if got, err := s.Outbox(ctx, "o1"); err != nil || got.State != mail.StateCancelled {
		t.Errorf("the cancelled entry = %+v, %v", got, err)
	}
}

// A database from before attachments gains the table and keeps its
// drafts, which have none.
func TestMigrationAddsDraftAttachmentsToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := t.TempDir() + "/shu.db"
	// Build a v3 database by hand, as the previous release left it.
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 3; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts
			(id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO drafts (id, account_id, subject, body, updated_at, revision)
			VALUES ('d1', 'a1', 'Old draft', 'kept', 1700000000, 4)`,
		"PRAGMA user_version=3",
	} {
		if _, err := db.Execute(ctx, q); err != nil {
			t.Fatal(err)
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
	rows, err := s.query(ctx, "PRAGMA user_version")
	if err != nil || number(rows.Values[0][0]) != int64(SchemaVersion) {
		t.Fatalf("user_version = %v, %v", rows.Values, err)
	}
	d, ok, err := s.Draft(ctx, "d1")
	if err != nil || !ok || d.Subject != "Old draft" || d.Body != "kept" || d.Revision != 4 || len(d.Attachments) != 0 {
		t.Fatalf("the old draft = %+v, %v, %v", d, ok, err)
	}
	list := testAttachments()
	if d, err = s.SetDraftAttachments(ctx, "d1", list, time.Unix(1_700_000_100, 0)); err != nil ||
		!sameRows(d.Attachments, list) || d.Revision != 5 || d.Body != "kept" {
		t.Fatalf("attachments in a migrated database = %+v, %v", d, err)
	}
}

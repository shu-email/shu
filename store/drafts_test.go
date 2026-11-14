package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"shu/mail"
)

// A draft comes back exactly as written: the recipient text as typed,
// with its linkage, at revision 1 and with nothing on the server.
func TestDraftRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	at := time.Unix(1_700_000_000, 0)
	d := mail.Draft{
		ID: "d1", AccountID: "a1",
		To: "Bob <bob@example.com>, carol@", Cc: "dave@exa", Bcc: "erin@example.com",
		Subject: "  Plan ", Body: "line one\n\n  indented\ttab\n", InReplyTo: "prev@example.com",
		References: []string{"root@example.com", "prev@example.com"},
		MessageID:  "m1@example.com", UpdatedAt: at,
	}
	saved, err := s.SaveDraft(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := d
	want.Revision = 1
	check := func(got mail.Draft) {
		t.Helper()
		if got.ID != want.ID || got.AccountID != want.AccountID || got.To != want.To || got.Cc != want.Cc ||
			got.Bcc != want.Bcc || got.Subject != want.Subject || got.Body != want.Body ||
			got.InReplyTo != want.InReplyTo || len(got.References) != 2 || got.References[1] != "prev@example.com" ||
			got.MessageID != want.MessageID || !got.UpdatedAt.Equal(want.UpdatedAt) {
			t.Errorf("draft = %+v, want %+v", got, want)
		}
		if got.Revision != want.Revision || got.SyncedRevision != want.SyncedRevision ||
			got.ServerFolder != want.ServerFolder || got.ServerUID != want.ServerUID ||
			got.ServerUIDValidity != want.ServerUIDValidity || got.PendingDelete != want.PendingDelete ||
			got.Conflict != want.Conflict {
			t.Errorf("sync state = %+v, want %+v", got, want)
		}
	}
	check(saved)
	if !saved.Dirty() {
		t.Error("a new draft has nothing to upload")
	}
	got, ok, err := s.Draft(ctx, "d1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	check(got)
	if _, ok, err := s.Draft(ctx, "missing"); err != nil || ok {
		t.Errorf("missing draft = %v, %v", ok, err)
	}
	// A draft created with its place on the server, as one taken over
	// from another mail client is, starts in step with it.
	adopted, err := s.SaveDraft(ctx, mail.Draft{
		ID: "d2", AccountID: "a1", Subject: "Adopted", MessageID: "m2@example.com", UpdatedAt: at,
		SyncedRevision: 1, ServerFolder: "Drafts", ServerUID: 7, ServerUIDValidity: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Dirty() || adopted.Revision != 1 || adopted.ServerUID != 7 || adopted.ServerUIDValidity != 42 ||
		adopted.ServerFolder != "Drafts" {
		t.Errorf("adopted draft = %+v", adopted)
	}
	// A draft of an account that does not exist is refused.
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d3", AccountID: "nobody", UpdatedAt: at}); err == nil {
		t.Error("a draft without an account was stored")
	}
}

// A save raises the revision and leaves the sync state alone; recording
// a sync leaves the content and the revision alone.
func TestDraftRevisionAndSyncState(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	at := time.Unix(1_700_000_000, 0)
	d, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "v1", MessageID: "m1@example.com", UpdatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	// The first revision was uploaded.
	d.SyncedRevision, d.ServerFolder, d.ServerUID, d.ServerUIDValidity = 1, "Drafts", 5, 42
	if err := s.SetDraftSync(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Draft(ctx, "d1"); got.Dirty() || got.ServerUID != 5 || got.Subject != "v1" || got.Revision != 1 {
		t.Fatalf("after the upload = %+v", got)
	}

	// An edit: the revision moves, the server's place does not, and the
	// Message-ID and account a careless caller passes are ignored.
	edit := mail.Draft{
		ID: "d1", AccountID: "other", Subject: "v2", Body: "more", To: "bob@exa",
		MessageID: "changed@example.com", UpdatedAt: at.Add(time.Minute),
	}
	saved, err := s.SaveDraft(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 2 || !saved.Dirty() || saved.SyncedRevision != 1 || saved.ServerUID != 5 ||
		saved.ServerFolder != "Drafts" || saved.ServerUIDValidity != 42 {
		t.Errorf("sync state after an edit = %+v", saved)
	}
	if saved.Subject != "v2" || saved.Body != "more" || saved.To != "bob@exa" ||
		saved.MessageID != "m1@example.com" || saved.AccountID != "a1" || !saved.UpdatedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("content after an edit = %+v", saved)
	}

	// The upload of revision 1 is recorded after revision 3 was saved:
	// the content stays, and the draft stays to upload.
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "v3", UpdatedAt: at.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	stale := d
	stale.ServerUID, stale.Conflict, stale.MessageID = 6, true, "forked@example.com"
	if err := s.SetDraftSync(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Draft(ctx, "d1")
	if got.Subject != "v3" || got.Revision != 3 || got.SyncedRevision != 1 || !got.Dirty() {
		t.Errorf("recording a sync changed the content: %+v", got)
	}
	if got.ServerUID != 6 || !got.Conflict || got.MessageID != "forked@example.com" {
		t.Errorf("sync state = %+v", got)
	}
	// A sync recorded for a draft that is gone says so.
	if err := s.SetDraftSync(ctx, mail.Draft{ID: "missing"}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("sync of a missing draft = %v", err)
	}
}

// A draft discarded keeps its row, marked, until its server copy is
// removed; then the row goes.
func TestDraftPendingDeleteAndList(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	at := time.Unix(1_700_000_000, 0)
	for i, d := range []mail.Draft{
		{ID: "old", AccountID: "a1", Subject: "old", UpdatedAt: at},
		{ID: "new", AccountID: "a1", Subject: "new", UpdatedAt: at.Add(time.Hour)},
		{ID: "tie", AccountID: "a1", Subject: "tie", UpdatedAt: at.Add(time.Hour)},
		{ID: "other", AccountID: "a2", Subject: "other", UpdatedAt: at},
	} {
		if _, err := s.SaveDraft(ctx, d); err != nil {
			t.Fatalf("draft %d: %v", i, err)
		}
	}
	ids := func(accountID string) []string {
		t.Helper()
		drafts, err := s.Drafts(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range drafts {
			out = append(out, d.ID)
		}
		return out
	}
	// Newest first; of two edited in the same second, the later made.
	if got := ids("a1"); len(got) != 3 || got[0] != "tie" || got[1] != "new" || got[2] != "old" {
		t.Errorf("drafts of a1 = %v", got)
	}
	if got := ids("a2"); len(got) != 1 || got[0] != "other" {
		t.Errorf("drafts of a2 = %v", got)
	}
	// Editing the oldest brings it to the top.
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "old", AccountID: "a1", Subject: "old, edited", UpdatedAt: at.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := ids("a1"); got[0] != "old" {
		t.Errorf("drafts after an edit = %v", got)
	}

	d, _, _ := s.Draft(ctx, "new")
	d.PendingDelete = true
	if err := s.SetDraftSync(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Draft(ctx, "new")
	if !ok || !got.PendingDelete || got.Subject != "new" {
		t.Fatalf("pending deletion = %+v, %v", got, ok)
	}
	// The row is still listed for the sync that will remove its copy.
	if got := ids("a1"); len(got) != 3 {
		t.Errorf("drafts with a pending deletion = %v", got)
	}
	if err := s.DeleteDraft(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Draft(ctx, "new"); ok {
		t.Error("deleted draft remains")
	}
	if err := s.DeleteDraft(ctx, "new"); err != nil {
		t.Errorf("deleting a draft twice: %v", err)
	}
	if got := ids("a1"); len(got) != 2 {
		t.Errorf("drafts after a delete = %v", got)
	}
}

// Deleting an account removes its drafts and no one else's.
func TestDraftsCascadeOnAccountDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	at := time.Unix(1_700_000_000, 0)
	for _, d := range []mail.Draft{
		{ID: "d1", AccountID: "a1", Subject: "mine", UpdatedAt: at},
		{ID: "d2", AccountID: "a1", Subject: "mine too", UpdatedAt: at, PendingDelete: true},
		{ID: "d3", AccountID: "a2", Subject: "theirs", UpdatedAt: at},
	} {
		if _, err := s.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.query(ctx, `SELECT id FROM drafts`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 1 || text(rows.Values[0][0]) != "d3" {
		t.Errorf("drafts after the account delete = %v", rows.Values)
	}
}

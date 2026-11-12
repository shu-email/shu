package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// The identities added to an account come back in the order they were
// added, a change keeps an identity's place, and an address is one
// identity of an account at most, whatever its case. Deleting one has the
// drafts written from it go from the account's own address, as an edit.
// They go with their account, and so does its signature.
func TestIdentitiesInOrderAndWithTheirAccount(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	for i, id := range []mail.Identity{
		{ID: "i1", AccountID: "a1", Name: "Sales", Email: "sales@example.com"},
		{ID: "i2", AccountID: "a2", Name: "Other", Email: "sales@example.com"},
		{ID: "i3", AccountID: "a1", Name: "Home", Email: "ann@home.example"},
		{ID: "i4", AccountID: "a1", Email: "lab@example.org"},
	} {
		if err := s.SaveIdentity(ctx, id); err != nil {
			t.Fatalf("identity %d: %v", i, err)
		}
	}
	got, err := s.Identities(ctx, "a1")
	if err != nil || len(got) != 3 || got[0].ID != "i1" || got[1].ID != "i3" || got[2].ID != "i4" ||
		got[1] != (mail.Identity{ID: "i3", AccountID: "a1", Name: "Home", Email: "ann@home.example"}) {
		t.Fatalf("identities = %+v, %v", got, err)
	}
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i1", AccountID: "a1", Name: "Deals", Email: "deals@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Identities(ctx, "a1"); got[0] != (mail.Identity{ID: "i1", AccountID: "a1", Name: "Deals", Email: "deals@example.com"}) {
		t.Errorf("after a change = %+v", got)
	}
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i5", AccountID: "a1", Email: "LAB@Example.org"}); err == nil {
		t.Error("an account got the same address twice")
	}
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i6", AccountID: "nobody", Email: "x@example.com"}); err == nil {
		t.Error("an identity was saved for no account")
	}

	d, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "Offer", Identity: "i3", UpdatedAt: time.Unix(1_700_000_000, 0)})
	if err != nil || d.Identity != "i3" {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	d.Identity = "i4"
	if d, err = s.SaveDraft(ctx, d); err != nil || d.Identity != "i4" || d.Revision != 2 {
		t.Fatalf("draft after its identity changed = %+v, %v", d, err)
	}
	other, err := s.SaveDraft(ctx, mail.Draft{ID: "d2", AccountID: "a1", Subject: "Other", Identity: "i1", UpdatedAt: time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIdentity(ctx, "i4"); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := s.Draft(ctx, "d1"); after.Identity != "" || after.Revision != d.Revision+1 {
		t.Errorf("draft after its identity went = %+v", after)
	}
	if kept, _, _ := s.Draft(ctx, "d2"); kept.Identity != "i1" || kept.Revision != other.Revision {
		t.Errorf("another draft = %+v", kept)
	}
	if got, _ := s.Identities(ctx, "a1"); len(got) != 2 {
		t.Errorf("identities after a deletion = %+v", got)
	}

	if err := s.SetSetting(ctx, mail.SignatureKey("a1"), `{"text":"Ann"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, mail.SignatureKey("a2"), `{"text":"Other"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM identities WHERE account_id = 'a1'`); got != 0 {
		t.Errorf("%d identities outlived their account", got)
	}
	if _, ok, _ := s.Setting(ctx, mail.SignatureKey("a1")); ok {
		t.Error("the signature outlived its account")
	}
	if got, _ := s.Identities(ctx, "a2"); len(got) != 1 {
		t.Errorf("the other account's identities = %+v", got)
	}
	if _, ok, _ := s.Setting(ctx, mail.SignatureKey("a2")); !ok {
		t.Error("the other account's signature went")
	}
}

// A message waiting out its send delay keeps its due time and its draft,
// is listed among the scheduled, the soonest first, and not among those
// to send again; it holds on to its files. One step claims it, and only
// one caller gets it, however many try at once.
func TestScheduledOutboxEntries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	created := time.Unix(1_700_000_000, 0)
	file := mail.Attachment{Filename: "a.txt", ContentType: "text/plain", Size: 3, ContentHash: "hash-a"}
	for _, p := range []mail.PendingSend{
		{ID: "late", AccountID: "a1", State: mail.StateScheduled, CreatedAt: created, DueAt: created.Add(30 * time.Second),
			DraftID: "d1", Envelope: mail.Outgoing{Subject: "Late", Attachments: []mail.Attachment{file}}},
		{ID: "soon", AccountID: "a2", Raw: []byte("raw"), State: mail.StateScheduled, CreatedAt: created, DueAt: created.Add(5 * time.Second),
			Envelope: mail.Outgoing{Subject: "Soon", Identity: "i1", From: mail.Address{Name: "Sales", Email: "sales@example.com"}}},
		{ID: "failed", AccountID: "a1", State: mail.StateFailed, CreatedAt: created, Envelope: mail.Outgoing{Subject: "Failed"}},
	} {
		if err := s.EnqueueOutbox(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	scheduled, err := s.ScheduledSends(ctx)
	if err != nil || len(scheduled) != 2 || scheduled[0].ID != "soon" || scheduled[1].ID != "late" {
		t.Fatalf("scheduled = %+v, %v", scheduled, err)
	}
	if p := scheduled[1]; !p.DueAt.Equal(created.Add(30*time.Second)) || p.DraftID != "d1" || p.State != mail.StateScheduled {
		t.Errorf("late = %+v", p)
	}
	if p := scheduled[0]; string(p.Raw) != "raw" || p.Envelope.From.Email != "sales@example.com" || p.Envelope.Identity != "i1" || p.DraftID != "" {
		t.Errorf("soon = %+v", p)
	}
	if pending, _ := s.PendingSends(ctx, "a1"); len(pending) != 1 || pending[0].ID != "failed" || !pending[0].DueAt.IsZero() {
		t.Errorf("to send again = %+v", pending)
	}
	if refs, _ := s.DraftFileRefs(ctx); len(refs) != 1 || refs[0] != "hash-a" {
		t.Errorf("files in use = %v", refs)
	}

	if won, err := s.ClaimOutbox(ctx, "soon", mail.StateScheduled, mail.StateSending); err != nil || !won {
		t.Fatalf("claim = %v, %v", won, err)
	}
	if won, _ := s.ClaimOutbox(ctx, "soon", mail.StateScheduled, mail.StateCancelled); won {
		t.Error("a claimed entry was claimed again")
	}
	if won, _ := s.ClaimOutbox(ctx, "missing", mail.StateScheduled, mail.StateSending); won {
		t.Error("an entry that is not there was claimed")
	}
	if p, _ := s.Outbox(ctx, "soon"); p.State != mail.StateSending {
		t.Errorf("state = %q", p.State)
	}
	if won, _ := s.ClaimOutbox(ctx, "late", mail.StateScheduled, mail.StateCancelled); !won {
		t.Error("the late one could not be taken back")
	}
	if refs, _ := s.DraftFileRefs(ctx); len(refs) != 0 {
		t.Errorf("files of a message taken back still in use: %v", refs)
	}

	// Many at once, on the one connection, as the undo and the end of
	// the delay of every message meet.
	const entries = 30
	for i := range entries {
		if err := s.EnqueueOutbox(ctx, mail.PendingSend{
			ID: fmt.Sprintf("race-%d", i), AccountID: "a1", State: mail.StateScheduled, CreatedAt: created, DueAt: created,
		}); err != nil {
			t.Fatal(err)
		}
	}
	wins := make([]int, entries)
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range entries {
		for _, to := range []string{mail.StateSending, mail.StateCancelled, mail.StateSending, mail.StateCancelled} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				won, err := s.ClaimOutbox(ctx, fmt.Sprintf("race-%d", i), mail.StateScheduled, to)
				if err != nil {
					t.Error(err)
				}
				if won {
					mu.Lock()
					wins[i]++
					mu.Unlock()
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	for i, n := range wins {
		if n != 1 {
			t.Errorf("entry %d was claimed %d times", i, n)
		}
	}
	if left, _ := s.ScheduledSends(ctx); len(left) != 0 {
		t.Errorf("still scheduled: %+v", left)
	}
}

// A database of the previous release, v9, gains identities and the send
// delay, and keeps its drafts and outbox: they go from the account's own
// address, and the messages there were are none that wait.
func TestMigrationAddsIdentitiesToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 9; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO drafts (id, account_id, subject, bcc_text, message_id, updated_at, revision, synced_revision)
			VALUES ('d1', 'a1', 'Old draft', 'erin@example.com', 'old@example.com', 1700000000, 3, 3)`,
		`INSERT INTO outbox (id, account_id, raw, envelope_json, state, created_at)
			VALUES ('o1', 'a1', 'raw', '{"to":[{"email":"bob@example.com"}],"subject":"Old","text":"x"}', 'failed', 1700000000)`,
		"PRAGMA user_version=9",
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
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) || SchemaVersion < 10 {
		t.Fatalf("user_version = %d, schema v%d", got, SchemaVersion)
	}
	d, ok, err := s.Draft(ctx, "d1")
	if err != nil || !ok || d.Identity != "" || d.Bcc != "erin@example.com" || d.Revision != 3 || d.Dirty() {
		t.Errorf("draft after the migration = %+v, %v, %v", d, ok, err)
	}
	pending, err := s.PendingSends(ctx, "a1")
	if err != nil || len(pending) != 1 || pending[0].ID != "o1" || !pending[0].DueAt.IsZero() || pending[0].DraftID != "" ||
		pending[0].Envelope.From != (mail.Address{}) {
		t.Errorf("outbox after the migration = %+v, %v", pending, err)
	}
	if scheduled, err := s.ScheduledSends(ctx); err != nil || len(scheduled) != 0 {
		t.Errorf("scheduled after the migration = %+v, %v", scheduled, err)
	}
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i1", AccountID: "a1", Email: "sales@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Identities(ctx, "a1"); err != nil || len(got) != 1 {
		t.Errorf("identities = %+v, %v", got, err)
	}
}

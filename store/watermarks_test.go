package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// arrive writes a sync that found messages in the inbox, each unread
// unless its UID is in seen.
func arrive(t *testing.T, s *Store, validity uint32, seen map[mail.UID]bool, uids ...mail.UID) {
	t.Helper()
	batch := mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: validity}}
	for _, uid := range uids {
		batch.Messages = append(batch.Messages, mail.Message{Summary: mail.Summary{
			UID: uid, Subject: "Message", Seen: seen[uid],
			From: mail.Address{Name: "Ann", Email: "ann@example.com"},
		}})
		batch.Folder.LastUID = max(batch.Folder.LastUID, uid)
	}
	// A sync keeps the cursor it had when it found nothing newer.
	if f, ok, _ := s.Folder(context.Background(), "a1", "INBOX"); ok && f.UIDValidity == validity {
		batch.Folder.LastUID = max(batch.Folder.LastUID, f.LastUID)
	}
	if err := s.ApplySync(context.Background(), "a1", batch); err != nil {
		t.Fatal(err)
	}
}

// taken is the UIDs TakeNew returns for the inbox.
func taken(t *testing.T, s *Store) []mail.UID {
	t.Helper()
	list, err := s.TakeNew(context.Background(), "a1", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	var out []mail.UID
	for _, m := range list {
		if m.Folder != "INBOX" || m.From.Email != "ann@example.com" {
			t.Errorf("a new message came back without its header: %+v", m)
		}
		out = append(out, m.UID)
	}
	return out
}

func sameUIDs(got []mail.UID, want ...mail.UID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The mail a folder holds when it is first looked at is not news. What
// arrives after is returned once, newest first, and never again.
func TestTakeNewOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")

	// Before any sync there is nothing to compare with, and no watermark
	// is made of it.
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("a folder never synced announced %v", got)
	}
	arrive(t, s, 7, nil, 1, 2, 3)
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("the first sync announced old mail: %v", got)
	}
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("asking again announced old mail: %v", got)
	}

	arrive(t, s, 7, nil, 4, 5)
	if got := taken(t, s); !sameUIDs(got, 5, 4) {
		t.Fatalf("new mail = %v, want 5 and 4, newest first", got)
	}
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("the same mail was announced twice: %v", got)
	}

	// A sync that fetches the same messages again announces nothing.
	arrive(t, s, 7, nil, 4, 5)
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("a repeated sync announced %v again", got)
	}

	// A folder the store does not know is an error, not silence.
	if _, err := s.TakeNew(ctx, "a1", "Missing"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("an unknown folder: %v, want ErrNotFound", err)
	}
}

// Mail read elsewhere before it was seen here is not announced, not even
// when it is marked unread later; nor is a message being moved away.
func TestTakeNewSkipsSeenAndTombstones(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	arrive(t, s, 7, nil, 1)
	taken(t, s)

	arrive(t, s, 7, map[mail.UID]bool{2: true}, 2, 3, 4)
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 4, true); err != nil {
		t.Fatal(err)
	}
	if got := taken(t, s); !sameUIDs(got, 3) {
		t.Fatalf("new mail = %v, want only the unread message 3", got)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 2, mail.FlagSeen, false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 4, false); err != nil {
		t.Fatal(err)
	}
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("mail passed over was announced later: %v", got)
	}

	// Only read mail arrived: nothing is announced, and it stays so.
	arrive(t, s, 7, map[mail.UID]bool{5: true}, 5)
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("read mail was announced: %v", got)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 5, mail.FlagSeen, false); err != nil {
		t.Fatal(err)
	}
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("read mail was announced once marked unread: %v", got)
	}
}

// A new UIDVALIDITY makes every message of the folder look new. None of
// them is announced: the watermark starts again from what is there.
func TestTakeNewAfterUIDValidityChange(t *testing.T) {
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	arrive(t, s, 7, nil, 1, 2)
	taken(t, s)

	reset := mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 8, LastUID: 3},
		Reset:  true,
		Messages: []mail.Message{
			{Summary: mail.Summary{UID: 1, Subject: "Again", From: mail.Address{Email: "ann@example.com"}}},
			{Summary: mail.Summary{UID: 3, Subject: "Again", From: mail.Address{Email: "ann@example.com"}}},
		},
	}
	if err := s.ApplySync(context.Background(), "a1", reset); err != nil {
		t.Fatal(err)
	}
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("a UIDVALIDITY change announced %v", got)
	}
	// Under the new UIDVALIDITY the watermark works as before.
	arrive(t, s, 8, nil, 4)
	if got := taken(t, s); !sameUIDs(got, 4) {
		t.Fatalf("new mail after the change = %v, want 4", got)
	}
}

// The watermark is on disk: what was announced before the app quit is
// not announced after it starts again, and what arrived meanwhile is.
func TestWatermarkSurvivesReopen(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	arrive(t, s, 7, nil, 1)
	taken(t, s)
	arrive(t, s, 7, nil, 2)
	if got := taken(t, s); !sameUIDs(got, 2) {
		t.Fatalf("new mail = %v, want 2", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("after a restart %v was announced again", got)
	}
	arrive(t, s, 7, nil, 3)
	if got := taken(t, s); !sameUIDs(got, 3) {
		t.Fatalf("new mail after a restart = %v, want 3", got)
	}
}

// A database of the release before, with mail in it and no watermark,
// gains the table, and the mail it already holds is not announced.
func TestMigrationAddsWatermarksToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 4; v++ {
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
		`INSERT INTO folders (id, account_id, name, special_use, unread, total, uidvalidity, last_uid)
			VALUES (1, 'a1', 'INBOX', '\Inbox', 2, 2, 7, 2)`,
		`INSERT INTO messages (folder_id, server_uid, subject, from_email, seen) VALUES (1, 1, 'Old', 'ann@example.com', 0)`,
		`INSERT INTO messages (folder_id, server_uid, subject, from_email, seen) VALUES (1, 2, 'Old', 'ann@example.com', 0)`,
		"PRAGMA user_version=4",
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
	if got := taken(t, s); len(got) != 0 {
		t.Fatalf("the upgrade announced the mail it found: %v", got)
	}
	arrive(t, s, 7, nil, 3)
	if got := taken(t, s); !sameUIDs(got, 3) {
		t.Fatalf("new mail after the upgrade = %v, want 3", got)
	}
}

// The watermark and the account's notification setting go with the
// account, and those of another account stay.
func TestWatermarkAndMuteCascadeOnAccountDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, id := range []string{"a1", "a2"} {
		addAccount(t, s, id)
		addFolder(t, s, id, "INBOX")
		if err := s.ApplySync(ctx, id, mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 7, LastUID: 1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TakeNew(ctx, id, "INBOX"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, mail.MutedKey(id), "1"); err != nil {
			t.Fatal(err)
		}
	}
	marks := func() int64 {
		rows, err := s.query(ctx, `SELECT COUNT(*) FROM watermarks`)
		if err != nil {
			t.Fatal(err)
		}
		return number(rows.Values[0][0])
	}
	if got := marks(); got != 2 {
		t.Fatalf("watermarks = %d, want one per inbox", got)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if got := marks(); got != 1 {
		t.Errorf("watermarks after deleting one account = %d, want 1", got)
	}
	if _, ok, _ := s.Setting(ctx, mail.MutedKey("a1")); ok {
		t.Error("the deleted account's notification setting is still stored")
	}
	if value, ok, _ := s.Setting(ctx, mail.MutedKey("a2")); !ok || value != "1" {
		t.Errorf("the other account's notification setting = %q, %v", value, ok)
	}
}

package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// Older mail fetched on demand joins the cache and its conversations, and
// changes nothing else of the folder: not its cursor or counts, and not
// the watermark of announced mail, so none of it is announced.
func TestApplyOlderLeavesCursorAndWatermark(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	synced := time.Unix(1_700_000_000, 0)
	var window []mail.Message
	for uid := mail.UID(101); uid <= 150; uid++ {
		window = append(window, mail.Message{Summary: mail.Summary{UID: uid, MessageID: fmt.Sprintf("m%d@example.com", uid),
			Subject: "Window", Date: pageBase.Add(time.Duration(uid) * time.Minute)}})
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 7, LastUID: 150, Unread: 3, Total: 200, SyncedAt: synced},
		Messages: window,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TakeNew(ctx, "a1", "INBOX"); err != nil || len(got) != 0 {
		t.Fatalf("the first look announced %v, %v", got, err)
	}
	before, _, _ := s.Folder(ctx, "a1", "INBOX")
	mark := countRows(t, s, `SELECT uid FROM watermarks`)

	// Older mail, unread, one answering a message of the window.
	var older []mail.Message
	for uid := mail.UID(51); uid <= 100; uid++ {
		older = append(older, mail.Message{Summary: mail.Summary{UID: uid, MessageID: fmt.Sprintf("m%d@example.com", uid),
			Subject: "Older", Date: pageBase.Add(time.Duration(uid) * time.Minute)}})
	}
	older[49].InReplyTo, older[49].MessageID = "m120@example.com", "early-reply@example.com"
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 7, Messages: older}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Folder(ctx, "a1", "INBOX")
	before.Complete = after.Complete
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the folder changed:\n was %+v\n now %+v", before, after)
	}
	if got := countRows(t, s, `SELECT uid FROM watermarks`); got != mark {
		t.Errorf("the watermark moved from %d to %d", mark, got)
	}
	if got, err := s.TakeNew(ctx, "a1", "INBOX"); err != nil || len(got) != 0 {
		t.Errorf("older mail was announced: %v, %v", got, err)
	}
	if threadOf(t, s, "a1", "INBOX", 100) != threadOf(t, s, "a1", "INBOX", 120) {
		t.Error("an older message did not join its conversation")
	}
	uids, err := s.FolderUIDs(ctx, "a1", "INBOX")
	if err != nil || len(uids) != 100 || uids[0] != 51 || uids[99] != 150 {
		t.Errorf("cached UIDs = %v, %v", uids, err)
	}
	cache, err := s.FolderCache(ctx, "a1", "INBOX")
	if err != nil || cache != (mail.FolderCache{Cached: 100, Total: 200}) {
		t.Errorf("the cache of the folder = %+v, %v", cache, err)
	}
	page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), Limit: 100})
	if err != nil || len(page.Messages) != 100 || page.Messages[99].UID != 51 {
		t.Errorf("the list after older mail = %d rows, %v", len(page.Messages), err)
	}

	// The last of it makes the folder complete, and a sync keeps that.
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 7, Messages: []mail.Message{
		{Summary: mail.Summary{UID: 3, MessageID: "m3@example.com", Subject: "Oldest", Date: pageBase}},
	}, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if f, _, _ := s.Folder(ctx, "a1", "INBOX"); !f.Complete || f.LastUID != 150 {
		t.Errorf("after the oldest = %+v", f)
	}
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "INBOX", SpecialUse: mail.SpecialInbox, Total: 201}}); err != nil {
		t.Fatal(err)
	}
	if cache, _ := s.FolderCache(ctx, "a1", "INBOX"); !cache.Complete || cache.Total != 201 || cache.Cached != 101 {
		t.Errorf("a LIST changed what the cache holds: %+v", cache)
	}

	// A batch fetched under another UIDVALIDITY is refused whole.
	err = s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 8, Messages: []mail.Message{
		{Summary: mail.Summary{UID: 2, Subject: "Stale"}},
	}})
	if !errors.Is(err, mail.ErrFolderReset) {
		t.Errorf("a batch of another UIDVALIDITY = %v", err)
	}
	if _, ok, _ := s.GetMessage(ctx, "a1", "INBOX", 2); ok {
		t.Error("a message of another UIDVALIDITY was written")
	}
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "Nowhere", UIDValidity: 7}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("older mail of a folder that is not there = %v", err)
	}
	checkThreadRows(t, s)
}

// A sync records whether it read a folder whole; a reset reads it anew.
func TestSyncRecordsWhetherTheCacheIsComplete(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	for _, step := range []struct {
		batch mail.SyncBatch
		want  bool
	}{
		{mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 5, Complete: true}}, true},
		{mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 9, Complete: false}}, false},
		{mail.SyncBatch{Reset: true, Folder: mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 3, Complete: true}}, true},
	} {
		if err := s.ApplySync(ctx, "a1", step.batch); err != nil {
			t.Fatal(err)
		}
		if f, _, _ := s.Folder(ctx, "a1", "INBOX"); f.Complete != step.want {
			t.Errorf("after %+v the folder is complete: %v", step.batch.Folder, f.Complete)
		}
	}
	if _, err := s.FolderCache(ctx, "a1", "Nowhere"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("the cache of a folder that is not there = %v", err)
	}
	if _, err := s.FolderUIDs(ctx, "a1", "Nowhere"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("the UIDs of a folder that is not there = %v", err)
	}
}

// A cache of the release before, with mail in it, has its recipients
// indexed by the upgrade, its dates made whole and its folders not yet
// complete; what it held is listed and found as before.
func TestMigrationIndexesRecipientsOfAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 7; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid) VALUES (1, 'a1', 'INBOX', '\Inbox', 5, 4)`,
		`INSERT INTO threads (id, account_id) VALUES (1, 'a1'), (2, 'a1'), (3, 'a1'), (4, 'a1')`,
		`INSERT INTO messages (id, folder_id, server_uid, message_id, subject, to_json, cc_json, date, preview, thread_id)
			VALUES (1, 1, 1, 'one@example.com', 'Budget', '[{"name":"Maya Lin","email":"maya@example.com"}]',
				'[{"email":"carol@example.net"}]', 1700000000, 'The numbers', 1),
			(2, 1, 2, 'two@example.com', 'Undated', '', NULL, NULL, 'No date', 2),
			(3, 1, 3, 'three@example.com', 'Broken', 'not json', '[]', 1700000100, 'Bad recipients', 3),
			(4, 1, 4, 'four@example.com', 'Lunch', '[{"name":"Bob","email":"bob@example.org"}]', NULL, 1700000200, 'Friday', 4)`,
		"PRAGMA user_version=7",
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
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) || SchemaVersion < 8 {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	for _, tc := range []struct {
		search mail.Search
		want   []string
	}{
		{mail.Search{To: []string{"maya lin"}}, []string{"a1:INBOX/1"}},
		{mail.Search{To: []string{"carol"}}, []string{"a1:INBOX/1"}},
		{mail.Search{Text: []string{"bob@example.org"}}, []string{"a1:INBOX/4"}},
		{mail.Search{Text: []string{"numbers"}}, []string{"a1:INBOX/1"}},
		{mail.Search{To: []string{"json"}}, nil},
	} {
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: tc.search})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(page.Messages); !slices.Equal(got, tc.want) {
			t.Errorf("after the upgrade %+v found %v, want %v", tc.search, got, tc.want)
		}
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE date IS NULL`); got != 0 {
		t.Errorf("%d dates are still null", got)
	}
	// The undated message comes last, on a page of its own as well.
	var keys []string
	var after mail.Cursor
	for {
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX"), After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, keysOf(page.Messages)...)
		after = page.Next
		if !page.More {
			break
		}
	}
	if want := []string{"a1:INBOX/4", "a1:INBOX/3", "a1:INBOX/1", "a1:INBOX/2"}; !slices.Equal(keys, want) {
		t.Errorf("pages after the upgrade = %v, want %v", keys, want)
	}
	if f, _, _ := s.Folder(ctx, "a1", "INBOX"); f.Complete || f.LastUID != 4 || f.UIDValidity != 5 {
		t.Errorf("the folder after the upgrade = %+v", f)
	}
	if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("full-text index: %v", err)
	}
	for _, name := range []string{"messages_list", "messages_date", "messages_flagged"} {
		if countRows(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name) != 1 {
			t.Errorf("no index %s", name)
		}
	}
}

// An account removed takes its mail out of every list and search, and
// out of the full-text index; the other account's stays.
func TestPagesAfterAccountDelete(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []mail.ListQuery{
		{}, {Search: mail.Search{Text: []string{"budget"}}}, {Scope: mail.AllInboxes(), Search: mail.Search{From: []string{"maya"}}},
	} {
		page, err := s.MessagePage(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(page.Messages); !slices.Equal(got, []string{"a2:INBOX/1"}) {
			t.Errorf("%+v after the delete = %v", q, got)
		}
		convs, err := s.ConversationPage(ctx, q)
		if err != nil || len(convs.Conversations) != 1 || convs.Conversations[0].Account != "a2" {
			t.Errorf("conversations of %+v after the delete = %+v, %v", q, convs.Conversations, err)
		}
	}
	if page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "")}); err != nil || len(page.Messages) != 0 {
		t.Errorf("the removed account lists %+v, %v", page.Messages, err)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH '"review"'`); got != 0 {
		t.Errorf("the index still finds %d of the removed account's messages", got)
	}
	if _, err := s.exec(ctx, `INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("full-text index: %v", err)
	}
}

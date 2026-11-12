package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	if err := sqlite.Load(); err != nil {
		t.Skipf("sqlite native module is not ready (%v); run go tool mygo generate", err)
	}
	s, err := InMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addAccount(t *testing.T, s *Store, id string) mail.Account {
	t.Helper()
	a := mail.Account{
		ID: id, Name: "Test", Email: id + "@example.com", Username: id,
		IMAP: mail.Server{Host: "imap.example.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP: mail.Server{Host: "smtp.example.com", Port: 465, TLS: mail.TLSImplicit},
	}
	if err := s.SaveAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func addFolder(t *testing.T, s *Store, accountID, name string) {
	t.Helper()
	if err := s.SaveFolders(context.Background(), accountID, []mail.Folder{{Name: name, Delimiter: "/", SpecialUse: mail.SpecialInbox}}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsFromEmpty(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	rows, err := s.query(ctx, "PRAGMA user_version")
	if err != nil {
		t.Fatal(err)
	}
	if got := number(rows.Values[0][0]); got != int64(SchemaVersion) {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	rows, err = s.query(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'index', 'trigger')`)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, r := range rows.Values {
		have[text(r[0])] = true
	}
	for _, name := range []string{
		"accounts", "kv", "folders", "messages", "attachments", "outbox",
		"messages_fts", "messages_list", "messages_message_id", "folders_account",
		"messages_ai", "messages_ad", "messages_au", "outbox_account",
	} {
		if !have[name] {
			t.Errorf("missing schema object %q", name)
		}
	}
	// Reopening runs no migration and keeps the data.
	addAccount(t, s, "a1")
	path := t.TempDir() + "/shu.db"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		accounts, err := db.Accounts(ctx)
		if err != nil || len(accounts) != 0 {
			t.Fatalf("reopen %d accounts = %v, %v", i, accounts, err)
		}
		db.Close()
	}
}

func TestAccountRoundTripAndActive(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a := addAccount(t, s, "a1")
	got, ok, err := s.Account(ctx, "a1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got != a {
		t.Errorf("account = %+v, want %+v", got, a)
	}
	if _, ok, _ := s.Account(ctx, "missing"); ok {
		t.Error("missing account found")
	}
	if err := s.SetActiveAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	id, err := s.ActiveAccount(ctx)
	if err != nil || id != "a1" {
		t.Errorf("active = %q, %v", id, err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.ActiveAccount(ctx); id != "" {
		t.Errorf("active after delete = %q", id)
	}
}

func TestFoldersKeepCursorOnRefresh(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	f, ok, err := s.Folder(ctx, "a1", "INBOX")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if f.UIDValidity != 0 || f.LastUID != 0 {
		t.Fatalf("new folder cursor = %+v", f)
	}
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "INBOX", Unread: 2, Total: 5}}); err != nil {
		t.Fatal(err)
	}
	f, _, _ = s.Folder(ctx, "a1", "INBOX")
	if f.Unread != 2 || f.Total != 5 {
		t.Errorf("refresh did not update counts: %+v", f)
	}
	folders, err := s.Folders(ctx, "a1")
	if err != nil || len(folders) != 1 {
		t.Fatalf("folders = %v, %v", folders, err)
	}
}

func TestSyncBatchAdvancesCursorWithMessages(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	now := time.Unix(1_700_000_000, 0)
	batch := mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 42, LastUID: 10, Unread: 1, Total: 2, SyncedAt: now},
		Messages: []mail.Message{{Summary: mail.Summary{
			UID: 10, Subject: "Hello", From: mail.Address{Name: "Ann", Email: "ann@example.com"},
			Date: now, Size: 12, Seen: false, Flagged: true,
		}}},
		Flags: []mail.FlagUpdate{{UID: 10, Seen: false, Flagged: true}},
	}
	if err := s.ApplySync(ctx, "a1", batch); err != nil {
		t.Fatal(err)
	}
	f, _, _ := s.Folder(ctx, "a1", "INBOX")
	if f.UIDValidity != 42 || f.LastUID != 10 || f.Unread != 1 || !f.SyncedAt.After(now.Add(-time.Second)) {
		t.Errorf("folder after sync = %+v", f)
	}
	msgs, err := s.ListMessages(ctx, "a1", "INBOX", "", 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %v, %v", msgs, err)
	}
	if msgs[0].Subject != "Hello" || msgs[0].From.Email != "ann@example.com" || !msgs[0].Flagged {
		t.Errorf("message = %+v", msgs[0])
	}
}

func TestUIDValidityResetDropsMessages(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	msg := func(uid mail.UID) mail.Message {
		return mail.Message{Summary: mail.Summary{UID: uid, Subject: "old"}}
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1},
		Messages: []mail.Message{msg(1)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Reset:  true,
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 5},
		Messages: []mail.Message{{
			Summary: mail.Summary{UID: 5, Subject: "new"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10)
	if len(msgs) != 1 || msgs[0].Subject != "new" || msgs[0].UID != 5 {
		t.Errorf("messages after reset = %+v", msgs)
	}
	f, _, _ := s.Folder(ctx, "a1", "INBOX")
	if f.UIDValidity != 2 || f.LastUID != 5 {
		t.Errorf("cursor after reset = %+v", f)
	}
}

func TestMessageBodyAndAttachments(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	now := time.Unix(1_700_000_000, 0)
	msg := mail.Message{
		Summary: mail.Summary{UID: 7, Subject: "Body", Date: now, Seen: true, HasAttachments: true,
			To: []mail.Address{{Email: "me@example.com"}}, References: []string{"<a@b>"}},
		BodyText: "plain body", BodyHTML: "<p>html</p>", CachedAt: now, RawHeaders: "Subject: Body\r\n",
		Attachments: []mail.Attachment{{Part: "2", Filename: "a.pdf", ContentType: "application/pdf", Size: 3, ContentHash: "h"}},
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", msg); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetMessage(ctx, "a1", "INBOX", 7)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.BodyText != "plain body" || got.BodyHTML != "<p>html</p>" || !got.BodyCached {
		t.Errorf("body = %+v", got)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Filename != "a.pdf" {
		t.Errorf("attachments = %+v", got.Attachments)
	}
	if err := s.SetAttachmentCache(ctx, "a1", "INBOX", 7, "2", "hash", "/tmp/a"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetMessage(ctx, "a1", "INBOX", 7)
	if got.Attachments[0].CachePath != "/tmp/a" || got.Attachments[0].ContentHash != "hash" {
		t.Errorf("cache path = %+v", got.Attachments[0])
	}
	// Saving the header again keeps the body.
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 7},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 7, Subject: "Body", Seen: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetMessage(ctx, "a1", "INBOX", 7)
	if got.BodyText != "plain body" || !got.BodyCached {
		t.Errorf("sync dropped body: %+v", got)
	}
}

func TestHeaderOnlyMessageHasNoBody(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 2, Subject: "header only"}}},
	}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.GetMessage(ctx, "a1", "INBOX", 2)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if m.BodyCached {
		t.Error("a header-only message reports a cached body")
	}
}

func TestSetFlagAdjustsUnread(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "INBOX", Unread: 1, Total: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3, Unread: 1, Total: 1},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 3, Subject: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 3, mail.FlagSeen, true); err != nil {
		t.Fatal(err)
	}
	f, _, _ := s.Folder(ctx, "a1", "INBOX")
	if f.Unread != 0 {
		t.Errorf("unread = %d after marking read", f.Unread)
	}
	if err := s.SetFlag(ctx, "a1", "INBOX", 3, mail.FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	m, _, _ := s.GetMessage(ctx, "a1", "INBOX", 3)
	if !m.Flagged || !m.Seen {
		t.Errorf("flags = %+v", m.Summary)
	}
}

func TestTombstoneAndDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 2},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 2, Subject: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, true); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 0 {
		t.Errorf("tombstoned message is visible: %+v", msgs)
	}
	if err := s.MarkDeleted(ctx, "a1", "INBOX", 2, false); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 1 {
		t.Errorf("restored message is hidden: %+v", msgs)
	}
	if err := s.DeleteMessage(ctx, "a1", "INBOX", 2); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetMessage(ctx, "a1", "INBOX", 2); ok {
		t.Error("deleted message remains")
	}
}

func TestCascadeOnAccountDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1},
		Messages: []mail.Message{{Summary: mail.Summary{UID: 1, Subject: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"folders", "messages"} {
		rows, err := s.query(ctx, `SELECT count(*) FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		if number(rows.Values[0][0]) != 0 {
			t.Errorf("%s rows remain after account delete", table)
		}
	}
}

func TestSearchFindsChineseAndFallsBack(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	now := time.Unix(1_700_000_000, 0)
	for i, subject := range []string{"项目进度更新", "Weekly report", "发票"} {
		msg := mail.Message{
			Summary:  mail.Summary{UID: mail.UID(i + 1), Subject: subject, From: mail.Address{Name: "同事", Email: "a@example.com"}, Date: now.Add(time.Duration(i) * time.Minute)},
			BodyText: "本周完成了核心功能的开发，下周继续。",
			CachedAt: now,
		}
		if err := s.SaveMessage(ctx, "a1", "INBOX", msg); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"项目", "项目进度", "进度", "核心功能", "Weekly", "report"} {
		msgs, err := s.ListMessages(ctx, "a1", "INBOX", q, 10)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(msgs) == 0 {
			t.Errorf("search %q found nothing", q)
		}
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "发票", 10); len(msgs) != 1 || msgs[0].Subject != "发票" {
		t.Errorf("two-character search = %+v", msgs)
	}
	// The two-character query takes the LIKE fallback, which also sees
	// the name.
	if msgs, _ := s.ListMessages(ctx, "a1", "", "同事", 10); len(msgs) == 0 {
		t.Error("sender fallback found nothing")
	}
}

func TestSearchScopedToFolderAndStarred(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{{Name: "Sent"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for _, m := range []mail.Message{
		{Summary: mail.Summary{UID: 1, Subject: "inbox star", Flagged: true, Date: now}},
		{Summary: mail.Summary{UID: 2, Subject: "sent plain", Date: now}},
	} {
		folder := "INBOX"
		if m.UID == 2 {
			folder = "Sent"
		}
		if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
			Folder: mail.Folder{Name: folder, UIDValidity: 1, LastUID: m.UID}, Messages: []mail.Message{m},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "starred", "", 10); len(msgs) != 1 || msgs[0].Folder != "INBOX" {
		t.Errorf("starred = %+v", msgs)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "Sent", "", 10); len(msgs) != 1 || msgs[0].Subject != "sent plain" {
		t.Errorf("sent = %+v", msgs)
	}
	if msgs, _ := s.ListMessages(ctx, "a1", "Sent", "star", 10); len(msgs) != 0 {
		t.Errorf("folder search leaked across folders: %+v", msgs)
	}
}

func TestOutboxTransitionsAndCrashRecovery(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	now := time.Unix(1_700_000_000, 0)
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
		Envelope:  mail.Outgoing{To: []mail.Address{{Email: "a@example.com"}}, Subject: "hi"},
		CreatedAt: now,
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Outbox(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != mail.StatePending || string(got.Raw) != string(p.Raw) || got.Envelope.Subject != "hi" {
		t.Errorf("outbox = %+v", got)
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateSending, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingSends(ctx, "a1")
	if err != nil || len(pending) != 1 || pending[0].State != mail.StateUnknown {
		t.Fatalf("crash recovery = %+v, %v", pending, err)
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingSends(ctx, "a1")
	if len(pending) != 0 {
		t.Errorf("sent entry still pending: %+v", pending)
	}
	if _, err := s.Outbox(ctx, "missing"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing outbox error = %v", err)
	}
}

func TestSaveAccountUpdates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a := addAccount(t, s, "a1")
	a.Name = "Renamed"
	if err := s.SaveAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Account(ctx, "a1")
	if got.Name != "Renamed" {
		t.Errorf("name = %q", got.Name)
	}
	if accounts, _ := s.Accounts(ctx); len(accounts) != 1 {
		t.Errorf("accounts = %+v", accounts)
	}
}

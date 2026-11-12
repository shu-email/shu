package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/ai"
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
		"ai_cache", "ai_cache_account",
		"drafts", "drafts_account",
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

// The outbox keeps what a message says, not which draft it was: the
// draft's ID is no part of the stored envelope.
func TestOutboxEnvelopeLeavesOutTheDraft(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
		Envelope: mail.Outgoing{
			To: []mail.Address{{Email: "a@example.com"}}, Subject: "hi",
			MessageID: "m1@example.com", DraftID: "d1",
		},
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Outbox(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Envelope.DraftID != "" || got.Envelope.MessageID != "m1@example.com" || got.Envelope.Subject != "hi" {
		t.Errorf("stored envelope = %+v", got.Envelope)
	}
	rows, err := s.query(ctx, `SELECT envelope_json FROM outbox WHERE id = 'o1'`)
	if err != nil {
		t.Fatal(err)
	}
	if raw := text(rows.Values[0][0]); strings.Contains(raw, "d1") || strings.Contains(strings.ToLower(raw), "draft") {
		t.Errorf("envelope JSON names the draft: %s", raw)
	}
}

// A database from before drafts gains the table and keeps its data.
func TestMigrationAddsDraftsToAnOlderDatabase(t *testing.T) {
	if err := sqlite.Load(); err != nil {
		t.Skipf("sqlite native module is not ready (%v); run go tool mygo generate", err)
	}
	ctx := context.Background()
	path := t.TempDir() + "/shu.db"
	// Build a v2 database by hand, as the previous release left it.
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 2; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	if _, err := db.Execute(ctx, `INSERT INTO accounts
		(id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
		VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execute(ctx, "PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
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
	if accounts, err := s.Accounts(ctx); err != nil || len(accounts) != 1 {
		t.Fatalf("accounts after the migration = %v, %v", accounts, err)
	}
	if _, err := s.SaveDraft(ctx, mail.Draft{ID: "d1", AccountID: "a1", Subject: "x", UpdatedAt: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("draft in a migrated database: %v", err)
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

// Settings round-trip through the kv table and replace their old value.
func TestSettings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok, err := s.Setting(ctx, "ai.config"); err != nil || ok {
		t.Fatalf("missing setting = %v, %v", ok, err)
	}
	if err := s.SetSetting(ctx, "ai.config", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "ai.config", `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Setting(ctx, "ai.config")
	if err != nil || !ok || got != `{"enabled":false}` {
		t.Errorf("setting = %q, %v, %v", got, ok, err)
	}
	// The active account shares the table and is not disturbed.
	addAccount(t, s, "a1")
	if err := s.SetActiveAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.ActiveAccount(ctx); id != "a1" {
		t.Errorf("active = %q", id)
	}
}

// Cached AI answers are read back by key and leave with their account.
func TestAICache(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	at := time.Unix(1_700_000_000, 0)
	entry := ai.CacheEntry{
		Key: "k1", Scope: "a1", Feature: ai.FeatureSummary, Provider: ai.ProviderOpenAI,
		Model: "gpt-4o-mini", Text: "- A summary", CreatedAt: at,
	}
	if _, ok, err := s.AICached(ctx, "k1"); err != nil || ok {
		t.Fatalf("empty cache = %v, %v", ok, err)
	}
	if err := s.SaveAICache(ctx, entry); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.AICached(ctx, "k1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got.Text != entry.Text || got.Model != entry.Model || got.Provider != entry.Provider ||
		got.Feature != entry.Feature || got.Scope != "a1" || !got.CreatedAt.Equal(at) {
		t.Errorf("entry = %+v", got)
	}
	// Saving the same key again replaces the answer.
	entry.Text = "- Another"
	if err := s.SaveAICache(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.AICached(ctx, "k1"); got.Text != "- Another" {
		t.Errorf("replaced entry = %+v", got)
	}
	// A scope that is no account is stored without one.
	if err := s.SaveAICache(ctx, ai.CacheEntry{Key: "k2", Scope: "gone", Feature: ai.FeatureSummary, Text: "x", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.AICached(ctx, "k2"); !ok || got.Scope != "" {
		t.Errorf("unscoped entry = %+v, %v", got, ok)
	}
	// Deleting the account removes the answers made from its mail.
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AICached(ctx, "k1"); ok {
		t.Error("a cached answer outlived its account")
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"shu/ai"
	"shu/internal/sqlitetest"
	"shu/mail"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	sqlitetest.Require(t)
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

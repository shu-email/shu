package main

import (
	"context"
	"crypto/tls"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shu/internal/fakemail"
	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// The seeded mailbox of the dev build, synced by the real engine into a
// database on disk, has what a check of the window relies on. The engine
// verifies the local servers' certificate as it would a real server's.
func TestSeededMailbox(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	cert, pool, err := fakemail.Cert()
	if err != nil {
		t.Fatal(err)
	}
	server, err := fakemail.StartIMAP(fakemail.IMAPOptions{
		Cert: cert, Username: fakemail.SeedUser, Password: fakemail.SeedPassword,
		Mailboxes: fakemail.SeedMailboxes, Logger: serverLog{t},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	if err := fakemail.Seed(server, now); err != nil {
		t.Fatal(err)
	}
	host, port := hostPort(t, server.Addr)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	account := mail.Account{
		ID: "seed", Name: fakemail.SeedName, Email: fakemail.SeedAddress, Username: fakemail.SeedUser,
		IMAP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: host, Port: port, TLS: mail.TLSStartTLS},
	}
	if err := svc.AddAccount(ctx, account, fakemail.SeedPassword); err != nil {
		t.Fatal(err)
	}
	folders, err := svc.SyncAccount(ctx, "seed")
	if err != nil {
		t.Fatal(err)
	}
	special := map[string]string{}
	for _, f := range folders {
		special[f.SpecialUse] = f.Name
		if err := svc.SyncFolder(ctx, "seed", f.Name, 0); err != nil {
			t.Fatalf("sync %s: %v", f.Name, err)
		}
	}
	for _, use := range []string{mail.SpecialInbox, mail.SpecialArchive, mail.SpecialDrafts, mail.SpecialSent, mail.SpecialJunk, mail.SpecialTrash} {
		if special[use] == "" {
			t.Errorf("no folder of special use %s in %v", use, folders)
		}
	}

	// The inbox holds more than the first sync keeps.
	inbox, err := svc.FolderCache(ctx, "seed", "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Cached != mail.DefaultSyncWindow || inbox.Total <= inbox.Cached {
		t.Errorf("the inbox: %+v", inbox)
	}

	// The roadmap conversation spans the inbox and Sent, the offsite one
	// the inbox and Archive.
	page, err := svc.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("seed", "INBOX"), Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	bySubject := map[string]mail.Conversation{}
	for _, c := range page.Conversations {
		bySubject[c.Latest.Subject] = c
	}
	for subject, want := range map[string]map[string]int{
		"Re: Q3 roadmap review":     {"INBOX": 2, "Sent": 1},
		"Re: Venue for the offsite": {"INBOX": 1, "Archive": 1},
	} {
		c, ok := bySubject[subject]
		if !ok {
			t.Errorf("no conversation %q in the inbox's first page", subject)
			continue
		}
		msgs, err := svc.CachedConversation(ctx, "seed", "", c.Thread)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, m := range msgs {
			got[m.Folder]++
		}
		if !maps.Equal(got, want) {
			t.Errorf("%q is in %v, want %v", subject, got, want)
		}
	}
	for _, subject := range []string{"This week at Acme", "Numbers for the board", "周五团队聚餐安排", "Incident report: sync outage"} {
		if _, ok := bySubject[subject]; !ok {
			t.Errorf("no %q in the inbox's first page", subject)
		}
	}

	// The newsletter's images are remote, and the board mail carries two
	// attachments.
	for _, c := range page.Conversations {
		switch c.Latest.Subject {
		case "This week at Acme":
			got, err := svc.FetchMessage(ctx, "seed", "INBOX", c.Latest.UID, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Message.BodyHTML, `src="https://images.example.com/hero.png"`) {
				t.Errorf("the newsletter's HTML: %q", got.Message.BodyHTML)
			}
		case "Numbers for the board":
			got, err := svc.FetchMessage(ctx, "seed", "INBOX", c.Latest.UID, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Message.Attachments) != 2 {
				t.Errorf("the board mail's attachments: %+v", got.Message.Attachments)
			}
		}
	}
}

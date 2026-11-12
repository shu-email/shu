package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// hasColumn reports whether a table has a column.
func hasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.query(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Values {
		if text(r[0]) == column {
			return true
		}
	}
	return false
}

// An account keeps how it signs in, and an update of the account keeps it
// too; nothing of the sign-in but the provider's name is stored.
func TestOAuthAccountRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	password := addAccount(t, s, "a1")
	a := mail.Account{
		ID: "a2", Name: "Ann", Email: "ann@gmail.com", Username: "ann@gmail.com",
		IMAP:  mail.Server{Host: "imap.gmail.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP:  mail.Server{Host: "smtp.gmail.com", Port: 465, TLS: mail.TLSImplicit},
		OAuth: mail.OAuthGoogle,
	}
	if err := s.SaveAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Account(ctx, "a2"); err != nil || !ok || got != a {
		t.Fatalf("account = %+v, %v, %v; want %+v", got, ok, err, a)
	}
	a.Name = "Ann Lee"
	if err := s.SaveAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.Accounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	if accounts[0] != password || accounts[0].OAuth != "" {
		t.Errorf("password account = %+v, want %+v", accounts[0], password)
	}
	if accounts[1] != a {
		t.Errorf("OAuth account after an update = %+v, want %+v", accounts[1], a)
	}
}

// The accounts of a database from before OAuth sign in with their
// passwords, as they did, and keep everything else.
func TestMigrationKeepsAccountsOnPasswords(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 8; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	for _, q := range []string{
		`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@gmail.com', 'ann@gmail.com', 'imap.gmail.com', 993, 'tls', 'smtp.gmail.com', 465, 'tls')`,
		`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid) VALUES (1, 'a1', 'INBOX', '\Inbox', 5, 1)`,
		"PRAGMA user_version=8",
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
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	want := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@gmail.com", Username: "ann@gmail.com",
		IMAP: mail.Server{Host: "imap.gmail.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP: mail.Server{Host: "smtp.gmail.com", Port: 465, TLS: mail.TLSImplicit},
	}
	if got, ok, err := s.Account(ctx, "a1"); err != nil || !ok || got != want {
		t.Fatalf("account after the migration = %+v, %v, %v; want %+v", got, ok, err, want)
	}
	if folders, err := s.Folders(ctx, "a1"); err != nil || len(folders) != 1 || folders[0].LastUID != 1 {
		t.Errorf("folders after the migration = %+v, %v", folders, err)
	}
}

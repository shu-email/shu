package mail_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// No code or token of a sign-in, and not the client's secret, is ever in
// the database: not in a row, not in the outbox's errors, not in a page
// SQLite freed, not in its journal. The scenario runs on a database file
// that is read whole, byte for byte, once it is closed.
func TestOAuthTokensNeverReachTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := mail.OAuthScenario(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var files []string
	var all []byte
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "shu.db") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, e.Name())
		all = append(all, b...)
	}
	// What is there is found: the account's address and the mail.
	for _, want := range []string{"ann@example.com", "Plain message", "Sent with a token"} {
		if !bytes.Contains(all, []byte(want)) {
			t.Fatalf("the database files %v do not hold %q", files, want)
		}
	}
	if len(secrets) < 8 {
		t.Fatalf("the provider handed out %d secrets", len(secrets))
	}
	for _, secret := range secrets {
		if bytes.Contains(all, []byte(secret)) {
			kind, _, _ := strings.Cut(secret, "-")
			t.Errorf("the database holds a %s of the provider's", kind)
		}
	}
}

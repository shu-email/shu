package mail_test

import (
	"context"
	"path/filepath"
	"testing"

	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// The address book on the database the app keeps, from mail on fake
// servers.
func TestContactsScenarioOnTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mail.ContactsScenario(t, db)
}

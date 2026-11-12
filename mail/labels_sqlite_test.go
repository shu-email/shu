package mail_test

import (
	"context"
	"path/filepath"
	"testing"

	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// Labels on the database the app keeps, from mail on a fake server: a
// restart opens the file again.
func TestLabelsScenarioOnTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	path := filepath.Join(t.TempDir(), "shu.db")
	var db *store.Store
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
	})
	mail.LabelsScenario(t, func() mail.Store {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		if db, err = store.Open(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		return db
	})
}

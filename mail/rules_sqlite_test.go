package mail_test

import (
	"context"
	"path/filepath"
	"testing"

	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// Rules on the database the app keeps, from mail on a fake server: a
// restart opens the file again.
func TestRulesScenarioOnTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	path := filepath.Join(t.TempDir(), "shu.db")
	var db *store.Store
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
	})
	mail.RulesScenario(t, func() mail.Store {
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

func TestRuleLogBoundOnTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	db, err := store.InMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mail.RuleLogBound(t, db)
}

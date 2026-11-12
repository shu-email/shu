package mail_test

import (
	"context"
	"path/filepath"
	"testing"

	"shu/internal/sqlitetest"
	"shu/mail"
	"shu/store"
)

// The send delay on the database the app keeps: taken back, sent when
// due, sent once after a restart, and never sent again once it may have
// left.
func TestSendDelayOnTheDatabase(t *testing.T) {
	sqlitetest.Require(t)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mail.ScheduleScenario(t, db)
}

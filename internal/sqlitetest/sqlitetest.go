// Package sqlitetest guards tests that need the SQLite native module.
//
// The module is a prebuilt library that go test downloads into the user's
// cache the first time it is used, so on a fresh machine without network
// access it cannot load. Tests skip then, as the suite must pass offline;
// CI sets SHU_REQUIRE_SQLITE so that the same tests fail instead of
// passing without running.
package sqlitetest

import (
	"os"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
)

// RequireEnv is the environment variable that turns the skip into a failure.
const RequireEnv = "SHU_REQUIRE_SQLITE"

// Require skips t when the SQLite native module cannot load, or fails it
// when RequireEnv is set.
func Require(t testing.TB) {
	t.Helper()
	if err := sqlite.Load(); err != nil {
		if os.Getenv(RequireEnv) != "" {
			t.Fatalf("sqlite native module did not load: %v", err)
		}
		t.Skipf("sqlite native module is not ready (%v); run go tool mygo generate", err)
	}
}

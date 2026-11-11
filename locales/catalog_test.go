package locales_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// messageKeys returns the keys of a catalog without @@locale and the @key
// metadata entries.
func messageKeys(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		if k == "@@locale" || strings.HasPrefix(k, "@") {
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Every shipped catalog says the same keys, so a string added to one is
// never missing from another.
func TestCatalogsHaveTheSameKeys(t *testing.T) {
	files, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("want at least two catalogs, found %v", files)
	}
	want := messageKeys(t, "en.json")
	for _, f := range files {
		if f == "en.json" {
			continue
		}
		if got := messageKeys(t, f); !slices.Equal(got, want) {
			t.Errorf("%s keys differ from en.json:\n got %v\nwant %v", f, got, want)
		}
	}
}

// The source catalog documents the placeholders of its messages.
func TestSourceCatalogDocumentsPlaceholders(t *testing.T) {
	b, err := os.ReadFile("en.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if k == "@@locale" || strings.HasPrefix(k, "@") {
			continue
		}
		var msg string
		if err := json.Unmarshal(v, &msg); err != nil {
			t.Fatal(err)
		}
		meta, ok := m["@"+k]
		if !strings.Contains(msg, "{") {
			continue
		}
		if !ok {
			t.Errorf("%s has placeholders but no @%s metadata", k, k)
			continue
		}
		if !strings.Contains(string(meta), "placeholders") {
			t.Errorf("@%s documents no placeholders", k)
		}
	}
}

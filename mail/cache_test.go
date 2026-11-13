package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cacheEnv is a service with a cache directory of its own.
func cacheEnv(t *testing.T) (*Service, *memStore, string) {
	t.Helper()
	store := newMemStore()
	dir := t.TempDir()
	return NewService(store, newMemSecrets(), dir), store, dir
}

func writeCacheFile(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The sweep removes the attachment files no row names, and the half-copied
// downloads a crash left, leaving named files, files young enough to be
// copied in and the temporary files of this run.
func TestSweepCacheRemovesWhatNoRowNames(t *testing.T) {
	svc, store, dir := cacheEnv(t)
	ctx := context.Background()
	// A draft's file is the user's data, in the data directory, not the
	// cache: no sweep of the cache may take it.
	dataDir := t.TempDir()
	svc.SetDataDir(dataDir)
	draftFile := filepath.Join(dataDir, draftFilesFolder, "draft-hash")
	now := time.Now()
	old := now.Add(-2 * cacheGrace)
	writeCacheFile(t, draftFile, old)
	oldPreview := svc.started.Add(-time.Hour)
	attachments := filepath.Join(dir, attachmentCacheDir)
	previews := filepath.Join(dir, previewCacheDir)
	keep := filepath.Join(attachments, "named")
	store.attachmentFiles = []string{keep}
	writeCacheFile(t, keep, old)
	orphan := filepath.Join(attachments, "orphan")
	writeCacheFile(t, orphan, old)
	young := filepath.Join(attachments, "young")
	writeCacheFile(t, young, now)
	download := filepath.Join(attachments, "download-123")
	writeCacheFile(t, download, old)
	copying := filepath.Join(attachments, "download-456")
	writeCacheFile(t, copying, now)
	staleTemp := filepath.Join(previews, "shu-1.pdf")
	writeCacheFile(t, staleTemp, oldPreview)
	liveTemp := filepath.Join(previews, "shu-2.pdf")
	writeCacheFile(t, liveTemp, now)

	if err := svc.SweepCache(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{orphan, download, staleTemp} {
		if exists(path) {
			t.Errorf("%s survived the sweep", filepath.Base(path))
		}
	}
	for _, path := range []string{keep, young, copying, liveTemp, draftFile} {
		if !exists(path) {
			t.Errorf("%s was swept", filepath.Base(path))
		}
	}
}

// A sweep that is asked to stop stops before removing anything.
func TestSweepCacheStopsWhenItsContextEnds(t *testing.T) {
	svc, _, dir := cacheEnv(t)
	old := time.Now().Add(-2 * cacheGrace)
	path := filepath.Join(dir, attachmentCacheDir, "orphan")
	writeCacheFile(t, path, old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.SweepCache(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled sweep = %v", err)
	}
	if !exists(path) {
		t.Error("a canceled sweep removed a file")
	}
}

// A temporary file for the user is written in the cache, and what an
// earlier run left goes at the next start.
func TestTempFileIsInTheCache(t *testing.T) {
	svc, _, dir := cacheEnv(t)
	path, err := svc.TempFile(".pdf", []byte("document"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(dir, previewCacheDir) || filepath.Ext(path) != ".pdf" {
		t.Errorf("temporary file %q is not a pdf preview in the cache", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "document" {
		t.Fatalf("temporary file = %q, %v", data, err)
	}
	// This run's file is kept.
	if err := svc.SweepCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(path) {
		t.Fatal("the sweep removed the run's own temporary file")
	}
	// An earlier run's is taken.
	past := svc.started.Add(-time.Minute)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if err := svc.SweepCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(path) {
		t.Error("the sweep kept an earlier run's temporary file")
	}
}

// PrepareCache brings the store's parsed cache to this build's version and
// sweeps behind it.
func TestPrepareCacheSweeps(t *testing.T) {
	svc, store, dir := cacheEnv(t)
	path := filepath.Join(dir, attachmentCacheDir, "orphan")
	writeCacheFile(t, path, time.Now().Add(-2*cacheGrace))
	if err := svc.PrepareCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.cacheVersion != CacheVersion {
		t.Errorf("cache version = %d, want %d", store.cacheVersion, CacheVersion)
	}
	if exists(path) {
		t.Error("PrepareCache did not sweep the cache")
	}
}

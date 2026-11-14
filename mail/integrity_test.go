package mail

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The stored files of drafts are named by a content hash, 64 hex
// characters; these stand for three.
const (
	draftHashB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	draftHashGone = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// integrityService is a service whose attachment cache and draft file
// store are temporary directories, on the fake store.
func integrityService(t *testing.T) (*Service, *memStore, string, string) {
	t.Helper()
	cacheDir, dataDir := t.TempDir(), t.TempDir()
	svc, store, _ := newTestService()
	svc.cacheDir = cacheDir
	svc.SetDataDir(dataDir)
	svc.SetClock(func() time.Time { return time.Unix(1_800_000_000, 0) })
	return svc, store, cacheDir, dataDir
}

// oldFile writes a file and gives it a time older than the cache grace, as
// a file no row names has after a crash.
func oldFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_800_000_000, 0).Add(-2 * cacheGrace)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

// A cache whose rows and files agree reports nothing.
func TestCheckIntegrityInASoundCache(t *testing.T) {
	svc, store, cacheDir, dataDir := integrityService(t)
	attachment := oldFile(t, filepath.Join(cacheDir, attachmentCacheDir), "hash-a")
	store.attachmentFiles = []string{attachment}
	oldFile(t, filepath.Join(dataDir, draftFilesFolder), draftHashB)
	store.drafts["d1"] = Draft{ID: "d1", Attachments: []Attachment{{ContentHash: draftHashB}}}
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("a sound cache reports %+v", report.Findings)
	}
}

// An attachment row whose file is gone is reported by its hash, and the
// file no row names as an orphan.
func TestCheckIntegrityFindsAttachmentFiles(t *testing.T) {
	svc, store, cacheDir, _ := integrityService(t)
	dir := filepath.Join(cacheDir, attachmentCacheDir)
	missing := filepath.Join(dir, "hash-gone")
	store.attachmentFiles = []string{missing}
	orphan := oldFile(t, dir, "hash-orphan")
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	got := report.Findings
	if got[0].Kind != IntegrityAttachmentMissing || got[0].Count != 1 || !slices.Equal(got[0].IDs, []string{"hash-gone"}) {
		t.Errorf("missing = %+v", got[0])
	}
	if got[1].Kind != IntegrityAttachmentOrphans || got[1].Count != 1 || !slices.Equal(got[1].IDs, []string{filepath.Base(orphan)}) {
		t.Errorf("orphans = %+v", got[1])
	}
}

// Messages that share an attachment share its one file: a file that is
// gone is reported once, whatever number of rows name it.
func TestCheckIntegrityCountsAMissingAttachmentOnce(t *testing.T) {
	svc, store, cacheDir, _ := integrityService(t)
	missing := filepath.Join(cacheDir, attachmentCacheDir, "hash-gone")
	store.attachmentFiles = []string{missing, missing}
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Kind != IntegrityAttachmentMissing ||
		report.Findings[0].Count != 1 || !slices.Equal(report.Findings[0].IDs, []string{"hash-gone"}) {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

// A draft another client replaced keeps its files until the window saves
// again: they are no orphans, as the draft sweep keeps them too.
func TestCheckIntegrityKeepsReplacedDraftFiles(t *testing.T) {
	svc, _, _, dataDir := integrityService(t)
	oldFile(t, filepath.Join(dataDir, draftFilesFolder), draftHashB)
	svc.draftsReplaced["d1"] = []Attachment{{ContentHash: draftHashB}}
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("a replaced draft's file reports %+v", report.Findings)
	}
}

// A draft whose stored file is gone is reported, and so is a stored file
// nothing names; a file being copied in is not.
func TestCheckIntegrityFindsDraftFiles(t *testing.T) {
	svc, store, _, dataDir := integrityService(t)
	dir := filepath.Join(dataDir, draftFilesFolder)
	store.drafts["d1"] = Draft{ID: "d1", Attachments: []Attachment{{ContentHash: draftHashGone}}}
	orphan := oldFile(t, dir, "hash-orphan")
	// A copy in flight has a name the service knows and is younger than
	// the grace.
	inFlight := oldFile(t, dir, "incoming-1")
	svc.incoming["incoming-1"] = true
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %+v", report.Findings)
	}
	got := report.Findings
	if got[0].Kind != IntegrityDraftMissing || !slices.Equal(got[0].IDs, []string{draftHashGone}) {
		t.Errorf("missing = %+v", got[0])
	}
	if got[1].Kind != IntegrityDraftOrphans || !slices.Equal(got[1].IDs, []string{filepath.Base(orphan)}) {
		t.Errorf("orphans = %+v", got[1])
	}
	if slices.Contains(got[1].IDs, filepath.Base(inFlight)) {
		t.Errorf("a copy in progress was reported: %+v", got[1])
	}
}

// A file younger than the grace may be one being copied in, and is no
// orphan the check reports.
func TestCheckIntegrityLeavesYoungFilesAlone(t *testing.T) {
	svc, _, cacheDir, _ := integrityService(t)
	dir := filepath.Join(cacheDir, attachmentCacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "download-1"), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	if err := os.Chtimes(filepath.Join(dir, "download-1"), now, now); err != nil {
		t.Fatal(err)
	}
	report, err := svc.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("a young file reports %+v", report.Findings)
	}
}

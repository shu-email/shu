package mail

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

// CacheVersion is the version of the parsed cache: the bodies and
// attachment rows the store keeps of messages, and the attachment files.
// Bump it when their stored form changes; the next PrepareCache drops what
// the older build wrote and a sync fetches it again. Drafts, the outbox,
// contacts, labels, rules and settings are no cache: no version touches
// them.
const CacheVersion = 1

const (
	// attachmentCacheDir is the directory of the received attachments'
	// bytes, one file per content hash.
	attachmentCacheDir = "attachments"
	// previewCacheDir holds the files the window opens for the user, such
	// as a PDF preview or a calendar invite, which are temporary.
	previewCacheDir = "previews"
	// cacheGrace is how old a file no row names must be before a sweep
	// takes it: one being copied in is younger, and its row follows.
	cacheGrace = time.Hour
	// cacheSweepAfter is how many messages one action must take from the
	// cache before it sweeps the files they leave.
	cacheSweepAfter = 100
	// sweepStep is how many directory entries a sweep reads between two
	// checks that its context is still alive.
	sweepStep = 512
)

// PrepareCache brings the parsed cache to this build's CacheVersion and
// sweeps the files no row names. The app runs it as it starts, before it
// reads any mail.
func (s *Service) PrepareCache(ctx context.Context) error {
	if _, err := s.store.PrepareCache(ctx, CacheVersion); err != nil {
		return err
	}
	return s.SweepCache(ctx)
}

// SweepCache removes what the cache no longer names: attachment files and
// half-copied downloads, and the temporary files an earlier run left for
// the user to open. It reads no message and removes nothing a row names;
// it checks ctx between entries, so a large cache can be swept a part at a
// time, and it is one pass over each directory.
func (s *Service) SweepCache(ctx context.Context) error {
	if s.cacheDir == "" {
		return nil
	}
	attachments, attachmentErr := s.sweepAttachmentFiles(ctx)
	previews, previewErr := s.sweepPreviewFiles(ctx)
	// The counts are all the log says: never a message, a file's name or
	// a path.
	if attachments+previews > 0 {
		log.Printf("shu: cache sweep removed %d attachment files and %d temporary files", attachments, previews)
	}
	return errors.Join(attachmentErr, previewErr)
}

// sweepAttachmentFiles removes the attachment files no attachment row
// names, and the copies a crash interrupted. A file younger than the grace
// is left: a copy in progress has no row yet, and one just written may be
// about to have one.
func (s *Service) sweepAttachmentFiles(ctx context.Context) (int, error) {
	dir := filepath.Join(s.cacheDir, attachmentCacheDir)
	var named map[string]bool
	removed := 0
	err := s.walkCacheFiles(ctx, dir, s.now().Add(-cacheGrace), func(name string) error {
		if named == nil {
			paths, err := s.store.AttachmentFileRefs(ctx)
			if err != nil {
				// Without the whole list, any file may be in use.
				return err
			}
			named = make(map[string]bool, len(paths))
			for _, path := range paths {
				named[filepath.Base(path)] = true
			}
		}
		if named[name] {
			return nil
		}
		switch err := os.Remove(filepath.Join(dir, name)); {
		case err == nil:
			removed++
			return nil
		case errors.Is(err, fs.ErrNotExist):
			return nil
		default:
			return err
		}
	})
	return removed, err
}

// walkCacheFiles calls visit for each file of a cache directory that is
// older than cutoff: younger ones may be a copy in progress, and the
// checks that read these directories and the sweeps that empty them agree
// on what an orphan is this way. It checks ctx between entries, so a
// large cache can be walked a part at a time.
func (s *Service) walkCacheFiles(ctx context.Context, dir string, cutoff time.Time, visit func(name string) error) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	for i, e := range entries {
		if i%sweepStep == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := visit(e.Name()); err != nil {
			return err
		}
	}
	return nil
}

// sweepPreviewFiles removes the temporary files of earlier runs. One this
// run wrote is younger than the service and stays: it may be open.
// A filesystem's clock can lag the wall clock, so a file written just
// after the service began may carry a time a moment before it; the sweep
// gives that up rather than remove a file of this run.
func (s *Service) sweepPreviewFiles(ctx context.Context) (int, error) {
	dir := filepath.Join(s.cacheDir, previewCacheDir)
	removed := 0
	err := s.walkCacheFiles(ctx, dir, s.started.Add(-time.Second), func(name string) error {
		switch err := os.Remove(filepath.Join(dir, name)); {
		case err == nil:
			removed++
			return nil
		case errors.Is(err, fs.ErrNotExist):
			return nil
		default:
			return err
		}
	})
	return removed, err
}

// TempFile writes data to a temporary file of the cache for the window to
// open, and returns its path. It is the user's to read, not a cache entry
// a row names: an earlier run's is swept as the app starts.
func (s *Service) TempFile(ext string, data []byte) (string, error) {
	dir := filepath.Join(s.cacheDir, previewCacheDir)
	if s.cacheDir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext = filepath.Ext(ext)
	f, err := os.CreateTemp(dir, "shu-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

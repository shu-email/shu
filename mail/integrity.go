package mail

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Checking the cache without repairing it. The cache holds what the server
// can give again (messages, bodies, attachment files) and what it cannot
// (drafts, the outbox, contacts, labels, rules, settings); this check reads
// only the first, and reports what a crash or a bug left out of step. It
// never reports message content: a finding names a thread, a row or a
// file's content hash.

// IntegrityKind names what a check found out of step.
type IntegrityKind string

const (
	// IntegritySchema says the database's schema version is not this
	// build's.
	IntegritySchema IntegrityKind = "schema"
	// IntegrityReferences says rows point at rows that are not there.
	IntegrityReferences IntegrityKind = "references"
	// IntegritySearchIndex says the full-text index is out of step with
	// the messages.
	IntegritySearchIndex IntegrityKind = "search-index"
	// IntegrityThreads says a conversation's aggregates are out of step
	// with its messages.
	IntegrityThreads IntegrityKind = "thread-aggregates"
	// IntegrityAttachmentMissing says an attachment row names a file the
	// cache does not hold.
	IntegrityAttachmentMissing IntegrityKind = "attachment-missing"
	// IntegrityAttachmentOrphans says the cache holds an attachment file
	// no row names.
	IntegrityAttachmentOrphans IntegrityKind = "attachment-orphans"
	// IntegrityDraftMissing says a draft or an unsent message names a
	// stored file that is not there.
	IntegrityDraftMissing IntegrityKind = "draft-missing"
	// IntegrityDraftOrphans says the draft file store holds a file no
	// draft and no unsent message names.
	IntegrityDraftOrphans IntegrityKind = "draft-orphans"
)

// IntegrityFinding is one kind of problem, how many the check found, and
// the first few of their IDs: thread or row numbers, or content hashes.
type IntegrityFinding struct {
	Kind  IntegrityKind `json:"kind"`
	Count int           `json:"count"`
	IDs   []string      `json:"ids,omitempty"`
}

// IntegrityReport is what a check found; no findings means the cache is in
// step with what it is derived from.
type IntegrityReport struct {
	Findings []IntegrityFinding `json:"findings"`
}

// maxIntegrityIDs is how many IDs one finding names.
const maxIntegrityIDs = 5

// IntegrityOf makes a finding of the IDs found, keeping the first few.
func IntegrityOf(kind IntegrityKind, ids []string) IntegrityFinding {
	f := IntegrityFinding{Kind: kind, Count: len(ids)}
	if len(ids) > maxIntegrityIDs {
		ids = ids[:maxIntegrityIDs]
	}
	f.IDs = ids
	return f
}

// integrityOrder is the order findings are reported in: the store's
// derived state first, then the files.
var integrityOrder = []IntegrityKind{
	IntegritySchema, IntegrityReferences, IntegritySearchIndex, IntegrityThreads,
	IntegrityAttachmentMissing, IntegrityAttachmentOrphans,
	IntegrityDraftMissing, IntegrityDraftOrphans,
}

// CheckIntegrity reads the cache and reports what is out of step with what
// it is derived from: the schema's version and the references it keeps,
// the full-text index, the conversations' aggregates, and the files of
// attachments and drafts. It repairs nothing, reads no message content and
// works offline.
func (s *Service) CheckIntegrity(ctx context.Context) (IntegrityReport, error) {
	findings, err := s.store.CheckCache(ctx)
	if err != nil {
		return IntegrityReport{}, err
	}
	for _, check := range []func(context.Context) ([]IntegrityFinding, error){
		s.attachmentFiles, s.draftFiles,
	} {
		more, err := check(ctx)
		if err != nil {
			return IntegrityReport{}, err
		}
		findings = append(findings, more...)
	}
	rank := func(kind IntegrityKind) int { return slices.Index(integrityOrder, kind) }
	slices.SortStableFunc(findings, func(a, b IntegrityFinding) int { return rank(a.Kind) - rank(b.Kind) })
	return IntegrityReport{Findings: findings}, nil
}

// attachmentFiles reports the cached attachment rows whose file is gone,
// and the attachment files no row names.
func (s *Service) attachmentFiles(ctx context.Context) ([]IntegrityFinding, error) {
	if s.cacheDir == "" {
		return nil, nil
	}
	paths, err := s.store.AttachmentFileRefs(ctx)
	if err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(paths))
	var missing []string
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, filepath.Base(path))
				continue
			}
			return nil, err
		}
		named[filepath.Base(path)] = true
	}
	var out []IntegrityFinding
	if len(missing) > 0 {
		out = append(out, IntegrityOf(IntegrityAttachmentMissing, missing))
	}
	orphans, err := s.orphanFiles(ctx, filepath.Join(s.cacheDir, attachmentCacheDir), named, nil)
	if err != nil {
		return nil, err
	}
	if len(orphans) > 0 {
		out = append(out, IntegrityOf(IntegrityAttachmentOrphans, orphans))
	}
	return out, nil
}

// draftFiles reports the drafts and unsent messages whose stored file is
// gone, and the stored files nothing names.
func (s *Service) draftFiles(ctx context.Context) ([]IntegrityFinding, error) {
	dir := s.draftFilesDir()
	if dir == "" {
		return nil, nil
	}
	refs, err := s.store.DraftFileRefs(ctx)
	if err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(refs))
	var missing []string
	for _, hash := range refs {
		path, err := s.draftFilePath(hash)
		if err != nil {
			missing = append(missing, hash)
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, hash)
				continue
			}
			return nil, err
		}
		named[hash] = true
	}
	var out []IntegrityFinding
	if len(missing) > 0 {
		out = append(out, IntegrityOf(IntegrityDraftMissing, missing))
	}
	// A file being copied in has a name in incoming, as the sweep reads it.
	s.fileMu.Lock()
	inFlight := make(map[string]bool, len(s.incoming))
	for name := range s.incoming {
		inFlight[name] = true
	}
	s.fileMu.Unlock()
	orphans, err := s.orphanFiles(ctx, dir, named, inFlight)
	if err != nil {
		return nil, err
	}
	if len(orphans) > 0 {
		out = append(out, IntegrityOf(IntegrityDraftOrphans, orphans))
	}
	return out, nil
}

// orphanFiles names the files of a directory that nothing names and no
// copy in progress wrote. A file younger than the grace the sweep keeps
// may be one being copied in, and is not reported: the same rule the
// sweep takes.
func (s *Service) orphanFiles(ctx context.Context, dir string, named, inFlight map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := s.now().Add(-cacheGrace)
	var out []string
	for i, e := range entries {
		if i%sweepStep == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if named[name] || inFlight[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

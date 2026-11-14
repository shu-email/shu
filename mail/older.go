package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emersion/go-imap/v2"
)

// OlderResult is what FetchOlder did.
type OlderResult struct {
	// Fetched is how many messages it added to the cache.
	Fetched int `json:"fetched"`
	// More reports that the server has messages of the folder the cache
	// still lacks: older mail to fetch next.
	More bool `json:"more"`
	// Reset is set when the folder was synced instead, from its newest
	// messages, as SyncFolder does: it had never been, or the server gave
	// it a new UIDVALIDITY, which makes its cached UIDs name other
	// messages. A list of the folder starts again from the top.
	Reset bool `json:"reset"`
}

// FetchOlder fetches the headers of up to n messages of a folder that the
// cache lacks, the newest of them first: those older than the oldest
// cached, and any a sync had to leave out. The server's own list of UIDs
// says which there are, so gaps in the UIDs, on the server or in the
// cache, are no matter. They are fetched as a sync fetches, with their
// threading headers and previews, and from a folder opened read-only, so
// nothing is marked read; they join their conversations as any message
// does. The sync cursor and the watermark of announced mail stay where
// they are, so none of them is announced as new. n of zero or less is the
// settings' first-sync window. The folder records whether the cache then
// lacks none of its messages (Folder.Complete, FolderCache).
func (s *Service) FetchOlder(ctx context.Context, accountID, folder string, n int) (result OlderResult, err error) {
	phase := SyncPhaseConnecting
	var done, total int
	report := func() {
		s.reportSync(SyncEvent{AccountID: accountID, Folder: folder, Kind: SyncKindOlder,
			Phase: phase, Done: done, Total: total})
	}
	setPhase := func(p SyncPhase) {
		if phase == p {
			return
		}
		phase = p
		report()
	}
	report()
	defer func() {
		s.reportSync(syncEnd(ctx, accountID, folder, SyncKindOlder, phase, done, total, err))
	}()
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return OlderResult{}, err
	}
	window := s.firstSyncWindow(ctx)
	if n <= 0 {
		n = window
	}
	n = min(n, defaultSyncCap)
	if _, ok, err := s.store.Folder(ctx, accountID, folder); err != nil {
		return OlderResult{}, err
	} else if !ok {
		if _, err := s.SyncAccount(ctx, accountID); err != nil {
			return OlderResult{}, err
		}
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return OlderResult{}, err
	}
	defer c.close()
	// EXAMINE, not SELECT: looking for older mail changes nothing, not
	// even the \Recent flags.
	selected, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return OlderResult{}, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	setPhase(SyncPhaseReading)
	cached, ok, err := s.store.Folder(ctx, accountID, folder)
	if err != nil {
		return OlderResult{}, err
	}
	if !ok || cached.LastUID == 0 || cached.UIDValidity != selected.UIDValidity {
		// What this finds was there before: none of it arrived.
		span := windowSpan(window)
		span.sorts = false
		written, err := s.syncSelected(ctx, c, accountID, folder, selected.UIDValidity, nil, span, setPhase)
		if err != nil {
			return OlderResult{}, err
		}
		done, total = written, written
		synced, _, err := s.store.Folder(ctx, accountID, folder)
		return OlderResult{Fetched: written, More: !synced.Complete, Reset: true}, err
	}
	missing, err := s.missingUIDs(ctx, c, accountID, folder, cached.LastUID)
	if err != nil {
		return OlderResult{}, err
	}
	total = min(n, len(missing))
	report()
	batch := OlderBatch{Folder: folder, UIDValidity: selected.UIDValidity}
	// A fetch names at most fetchChunk UIDs: scattered ones make a long
	// command, and a server need take no line of more than 8000 octets.
	for take := missing[:min(n, len(missing))]; len(take) > 0; take = take[min(fetchChunk, len(take)):] {
		var set imap.UIDSet
		asked := map[UID]bool{}
		for _, uid := range take[:min(fetchChunk, len(take))] {
			set.AddNum(imap.UID(uid))
			asked[uid] = true
		}
		messages, _, err := fetchSummaries(c, set, false)
		if err != nil {
			return OlderResult{}, err
		}
		// Only what was asked for: anything else the server sent along
		// is no older mail.
		var got []Message
		for _, m := range messages {
			if asked[m.UID] {
				delete(asked, m.UID)
				got = append(got, m)
			}
		}
		addPreviews(c, set, got)
		batch.Messages = append(batch.Messages, got...)
		done = len(batch.Messages)
		report()
	}
	// A message the server did not give, as one expunged meanwhile, is
	// still lacking; the next fetch asks the server again.
	left := len(missing) - len(batch.Messages)
	batch.Complete = left == 0
	setPhase(SyncPhaseWriting)
	if err := s.store.ApplyOlder(ctx, accountID, batch); err != nil {
		if !errors.Is(err, ErrFolderReset) {
			return OlderResult{}, err
		}
		// A sync reset the folder meanwhile, and cached its newest
		// messages in place of the old ones.
		synced, _, err := s.store.Folder(ctx, accountID, folder)
		return OlderResult{More: !synced.Complete, Reset: true}, err
	}
	return OlderResult{Fetched: len(batch.Messages), More: left > 0}, nil
}

// fetchChunk is the most UIDs one fetch of older mail names. Tests make
// it small.
var fetchChunk = 500

// missingUIDs lists, newest first, the messages of the selected folder up
// to UID last that the cache does not hold.
func (s *Service) missingUIDs(ctx context.Context, c *imapSession, accountID, folder string, last UID) ([]UID, error) {
	var upTo imap.UIDSet
	upTo.AddRange(1, imap.UID(last))
	var options *imap.SearchOptions
	if c.capHas(imap.CapESearch) {
		// ESEARCH answers with ranges; a plain SEARCH lists every UID.
		options = &imap.SearchOptions{ReturnAll: true}
	}
	data, err := c.client.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{upTo}}, options).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: search %s: %w", folder, err)
	}
	held, err := s.store.FolderUIDs(ctx, accountID, folder)
	if err != nil {
		return nil, err
	}
	have := make(map[UID]bool, len(held))
	for _, uid := range held {
		have[uid] = true
	}
	var out []UID
	found, _ := data.All.(imap.UIDSet)
	for _, r := range found {
		// A range open at the top ends at last, which the search asked
		// for: a server that answers "*" names no UID above it.
		start, stop := UID(r.Start), UID(r.Stop)
		if stop == 0 || stop > last {
			stop = last
		}
		for uid := start; uid >= 1 && uid <= stop; uid++ {
			if !have[uid] {
				out = append(out, uid)
			}
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	return slices.Compact(out), nil
}

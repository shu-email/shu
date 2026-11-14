package mail

import (
	"context"
	"errors"
	"fmt"
)

// SetFlag changes a flag on the server and then in the cache. Callers
// show the change optimistically and roll it back when this fails.
func (s *Service) SetFlag(ctx context.Context, accountID, folder string, uid UID, flag Flag, on bool) error {
	return s.setFlags(ctx, accountID, []MessageRef{{Folder: folder, UID: uid}}, flag, on, true)
}

// SetFlags changes a flag on several messages, as starring a conversation
// or marking it read does: one connection, and for each folder one SELECT
// and one STORE, then the cache. Only \Seen and \Flagged change this way,
// and nothing is moved, deleted or expunged. A folder whose UIDVALIDITY is
// no longer the cache's is not touched: its UIDs would name other
// messages. When a folder fails, those before it keep their change. A
// change the server cannot be reached for waits in the queue (see
// queue.go), the cache showing it meanwhile.
func (s *Service) SetFlags(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool) error {
	return s.setFlags(ctx, accountID, msgs, flag, on, true)
}

// setFlags is SetFlags with the queue or without it. The rules and their
// undo keep their own log of what the server has not made yet, so they
// ask for every change to be made now or to fail.
func (s *Service) setFlags(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool, queue bool) error {
	if flag != FlagSeen && flag != FlagFlagged {
		return fmt.Errorf("mail: flag %q is not set on several messages", flag)
	}
	groups := byFolder(msgs, "")
	if len(groups) == 0 {
		return nil
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	// What each folder asks of the server is written before it is
	// touched: a change made offline outlives the app.
	validity := make([]uint32, len(groups))
	ids := make([][]int64, len(groups))
	for i, g := range groups {
		cached, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			s.dropOperations(ctx, allIDs(ids))
			return err
		}
		if !ok {
			s.dropOperations(ctx, allIDs(ids))
			return fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		validity[i] = cached.UIDValidity
		if !queue {
			continue
		}
		ops := make([]PendingOperation, 0, len(g.uids))
		for _, uid := range g.uids {
			ops = append(ops, PendingOperation{
				Kind: OpFlags, Folder: g.folder, UID: uid,
				Flag: flag, On: on, Validity: cached.UIDValidity,
			})
		}
		if ids[i], err = s.enqueueOperations(ctx, accountID, ops); err != nil {
			s.dropOperations(ctx, allIDs(ids))
			return err
		}
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		if queue && retryable(err) {
			// The server is out of reach: the cache shows the change the
			// user asked for, and the worker makes it later.
			_ = s.setFlagsInCache(ctx, accountID, groups, flag, on)
			s.requeueOperations(ctx, accountID, allIDs(ids))
			return nil
		}
		s.dropOperations(ctx, allIDs(ids))
		return err
	}
	defer c.close()
	for i, g := range groups {
		if err := storeFlagsIn(c, g.folder, validity[i], g.uids, flag, on); err != nil {
			if queue && retryable(err) {
				_ = s.setFlagsInCache(ctx, accountID, groups[i:], flag, on)
				s.requeueOperations(ctx, accountID, allIDs(ids[i:]))
				return nil
			}
			s.dropOperations(ctx, allIDs(ids[i:]))
			return err
		}
		// A row a sync removed meanwhile has nothing to change.
		if err := s.store.SetFlags(ctx, accountID, g.folder, g.uids, flag, on); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		s.dropOperations(ctx, ids[i])
	}
	return nil
}

// setFlagsInCache shows flags the server has not been told of yet: the
// change waits in the queue for it. A row a sync removed meanwhile has
// nothing to change.
func (s *Service) setFlagsInCache(ctx context.Context, accountID string, groups []uidGroup, flag Flag, on bool) error {
	bg := context.WithoutCancel(ctx)
	for _, g := range groups {
		if err := s.store.SetFlags(bg, accountID, g.folder, g.uids, flag, on); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// Move archives, junks or deletes a message: the cache row is a tombstone
// until the server confirms the move, and a move the server cannot be
// reached for waits in the queue, its result naming it.
func (s *Service) Move(ctx context.Context, accountID, folder string, uid UID, dest string) (MoveResult, error) {
	moved, err := s.MoveMessages(ctx, accountID, []MessageRef{{Folder: folder, UID: uid}}, dest)
	if err != nil || len(moved) == 0 {
		return MoveResult{}, err
	}
	return moved[0], nil
}

// MoveBack undoes a move when the server reported the destination UID.
// The caller syncs the source folder to restore its cache row.
func (s *Service) MoveBack(ctx context.Context, accountID, from, to string, uid UID) error {
	if uid == 0 {
		return errors.New("mail: the moved message has no destination UID")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	_, err = s.moveOnServer(ctx, a, password, from, []UID{uid}, to)
	return err
}

// MoveMessages moves several messages to dest: a conversation, or what of
// it a list shows. The messages of each folder move in one command. Their
// cache rows are tombstones until the server confirms, and come back if
// it fails. It returns a result for each message that moved, with what
// MoveBackMessages needs; when a folder fails, the error comes with the
// results of the folders that moved before it. Messages already in dest
// stay where they are. A move the server cannot be reached for waits in
// the queue (see queue.go): the rows stay tombstones, and the result of
// each message names the action to undo it.
func (s *Service) MoveMessages(ctx context.Context, accountID string, msgs []MessageRef, dest string) ([]MoveResult, error) {
	return s.moveMessages(ctx, accountID, msgs, dest, true)
}

// moveMessages is MoveMessages with the queue or without it. The rules and
// their undo keep their own log of what the server has not made yet, and
// a reminder waits for the next run instead, so they ask for every move to
// be made now or to fail.
func (s *Service) moveMessages(ctx context.Context, accountID string, msgs []MessageRef, dest string, queue bool) ([]MoveResult, error) {
	if dest == "" {
		return nil, errors.New("mail: the destination folder is empty")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	groups := byFolder(msgs, dest)
	// The move of each folder is written before the server is touched, so
	// one made offline outlives the app.
	opIDs := make([][]int64, len(groups))
	for i, g := range groups {
		if !queue {
			break
		}
		cached, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, err
		}
		if !ok {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		ops := make([]PendingOperation, 0, len(g.uids))
		for _, uid := range g.uids {
			ops = append(ops, PendingOperation{
				Kind: OpMove, Folder: g.folder, UID: uid, Dest: dest, Validity: cached.UIDValidity,
			})
		}
		if opIDs[i], err = s.enqueueOperations(ctx, accountID, ops); err != nil {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, err
		}
	}
	// The rows come back even when the move failed because ctx ended.
	restore := func(groups []uidGroup) { s.showQueued(ctx, accountID, groups) }
	if err := s.hideQueued(ctx, accountID, groups, allIDs(opIDs)); err != nil {
		return nil, err
	}
	var results []MoveResult
	var cacheErr error
	for i, g := range groups {
		moved, err := s.moveOnServer(ctx, a, password, g.folder, g.uids, dest)
		if err != nil {
			if queue && retryable(err) {
				// The server is out of reach: the rows stay tombstones,
				// and this and the later folders wait in the queue.
				s.requeueOperations(ctx, accountID, allIDs(opIDs[i:]))
				for j := i; j < len(groups); j++ {
					for k, uid := range groups[j].uids {
						results = append(results, MoveResult{
							Folder: dest, From: groups[j].folder, UID: uid, Pending: opIDs[j][k],
						})
					}
				}
				return results, nil
			}
			s.dropOperations(ctx, allIDs(opIDs[i:]))
			restore(groups[i:])
			return results, err
		}
		results = append(results, moved...)
		s.dropOperations(ctx, opIDs[i])
		if err := s.store.DeleteMessages(ctx, accountID, g.folder, g.uids); err != nil && cacheErr == nil {
			cacheErr = err
		}
	}
	if len(results) >= cacheSweepAfter {
		// A move of many messages leaves their attachments' files with
		// no row: take them now rather than at the next start.
		_ = s.SweepCache(ctx)
	}
	return results, cacheErr
}

// MoveBackMessages undoes MoveMessages: each message goes back to the
// folder it came from, found by the UID the server gave it where it went.
// A message the server gave no UID for cannot be found, and stays; the
// error says so once the others are back. A move still in the queue is
// cancelled, never made: one the worker has begun already stays where it
// went, and the error says how many. The caller syncs the folders the
// messages return to, to restore their cache rows.
func (s *Service) MoveBackMessages(ctx context.Context, accountID string, moved []MoveResult) error {
	// A move the server was never told of is undone by dropping its queued
	// action: the cache row stops being a tombstone, and the caller's sync
	// of the folder brings it back.
	var queued []int64
	for _, m := range moved {
		if m.Pending != 0 {
			queued = append(queued, m.Pending)
		}
	}
	cancelled := map[int64]bool{}
	if len(queued) > 0 {
		ids, err := s.store.CancelOperations(ctx, queued)
		if err != nil {
			return err
		}
		for _, id := range ids {
			cancelled[id] = true
		}
	}
	made, lost := 0, 0
	for _, m := range moved {
		if m.Pending != 0 {
			if !cancelled[m.Pending] {
				// The worker had the action already: the message is where
				// it moved, and cannot be found to come back.
				made++
				continue
			}
			_ = s.store.MarkDeleted(context.WithoutCancel(ctx), accountID, m.From, m.UID, false)
		}
	}
	type route struct{ from, to string }
	var order []route
	byRoute := map[route][]UID{}
	seen := map[route]map[UID]bool{}
	for _, m := range moved {
		if m.Pending != 0 {
			continue
		}
		if m.DestUID == 0 {
			lost++
			continue
		}
		r := route{m.Folder, m.From}
		if _, ok := byRoute[r]; !ok {
			order = append(order, r)
			seen[r] = map[UID]bool{}
		}
		if seen[r][m.DestUID] {
			continue
		}
		seen[r][m.DestUID] = true
		byRoute[r] = append(byRoute[r], m.DestUID)
	}
	if len(order) > 0 {
		a, password, err := s.account(ctx, accountID)
		if err != nil {
			return err
		}
		for _, r := range order {
			if _, err := s.moveOnServer(ctx, a, password, r.from, byRoute[r], r.to); err != nil {
				return err
			}
		}
	}
	var trouble error
	if lost > 0 {
		trouble = fmt.Errorf("mail: the server gave no UID for %d moved messages, which stay where they are", lost)
	}
	if made > 0 {
		trouble = errors.Join(trouble,
			fmt.Errorf("mail: %d moves were made already, and stay where they are", made))
	}
	return trouble
}

// uidGroup is the UIDs of one folder, in the order first asked for.
type uidGroup struct {
	folder string
	uids   []UID
}

// byFolder groups messages by folder, once each and in their order,
// leaving out those in skip.
func byFolder(msgs []MessageRef, skip string) []uidGroup {
	var out []uidGroup
	index := map[string]int{}
	seen := map[string]map[UID]bool{}
	for _, m := range msgs {
		if m.Folder == skip {
			continue
		}
		i, ok := index[m.Folder]
		if !ok {
			i = len(out)
			index[m.Folder] = i
			seen[m.Folder] = map[UID]bool{}
			out = append(out, uidGroup{folder: m.Folder})
		}
		if seen[m.Folder][m.UID] {
			continue
		}
		seen[m.Folder][m.UID] = true
		out[i].uids = append(out[i].uids, m.UID)
	}
	return out
}

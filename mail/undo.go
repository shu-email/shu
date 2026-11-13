package mail

import (
	"context"
	"errors"
)

// UndoFlagChange takes back a change SetFlags made on the messages it
// was made on: the actions of it still waiting in the queue are dropped
// before the server makes them, and the cache shows what it did before
// them; a change the server has is set back.
func (s *Service) UndoFlagChange(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool) error {
	return s.undoChange(ctx, accountID, msgs,
		func(op PendingOperation) bool {
			return op.Kind == OpFlags && op.Flag == flag && op.On == on
		},
		func(ctx context.Context, ref MessageRef) bool {
			m, ok, err := s.store.GetMessage(ctx, accountID, ref.Folder, ref.UID)
			if err != nil || !ok {
				return false
			}
			switch flag {
			case FlagFlagged:
				return m.Flagged == on
			case FlagSeen:
				return m.Seen == on
			}
			return false
		},
		func(ctx context.Context, refs []MessageRef) error {
			return s.setFlagsInCache(ctx, accountID, byFolder(refs, ""), flag, !on)
		},
		func(ctx context.Context, refs []MessageRef) error {
			return s.setFlags(ctx, accountID, refs, flag, !on, true)
		})
}

// UndoLabelChange takes back a change SetLabel made on the messages it
// was made on, as UndoFlagChange takes back a flag's. A label the folder
// keeps on this device alone is dealt with by the cache, as the change
// itself was.
func (s *Service) UndoLabelChange(ctx context.Context, accountID string, msgs []MessageRef, id LabelID, on bool) error {
	return s.undoChange(ctx, accountID, msgs,
		func(op PendingOperation) bool {
			return op.Kind == OpLabel && op.Label == id && op.On == on
		},
		func(ctx context.Context, ref MessageRef) bool {
			m, ok, err := s.store.GetMessage(ctx, accountID, ref.Folder, ref.UID)
			if err != nil || !ok {
				return false
			}
			return m.HasLabel(id) == on
		},
		func(ctx context.Context, refs []MessageRef) error {
			bg := context.WithoutCancel(ctx)
			for _, g := range byFolder(refs, "") {
				_, err := s.store.SetLabel(bg, accountID, id, g.folder, g.uids, !on, false)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
			}
			return nil
		},
		func(ctx context.Context, refs []MessageRef) error {
			return s.setLabel(ctx, accountID, refs, id, !on, true)
		})
}

// undoChange takes back a change the window made on the messages it was
// made on. The actions of it that still wait are dropped, never made, and
// revert puts the cache back as it was before them; those the server
// refused go too, as their cache change went back when they failed. What
// is left is inverted only where the cache still shows the change, which
// tells a failed action from one the server made after it. One a worker
// is making can land after the inverse, which the next sync of the flags
// puts right; the worker makes one action at a time, so this is the work
// of a moment.
func (s *Service) undoChange(ctx context.Context, accountID string, msgs []MessageRef,
	matches func(PendingOperation) bool,
	shown func(context.Context, MessageRef) bool,
	revert func(context.Context, []MessageRef) error,
	invert func(context.Context, []MessageRef) error) error {
	ops, err := s.store.Operations(ctx, accountID)
	if err != nil {
		return err
	}
	wanted := map[MessageRef]bool{}
	for _, m := range msgs {
		wanted[m] = true
	}
	refOf := map[int64]MessageRef{}
	validity := map[int64]uint32{}
	done := map[MessageRef]bool{}
	dropped := 0
	var waiting, refused []int64
	for _, op := range ops {
		ref := MessageRef{Folder: op.Folder, UID: op.UID}
		if !wanted[ref] || !matches(op) {
			continue
		}
		switch op.State {
		case OperationPending:
			waiting = append(waiting, op.ID)
			refOf[op.ID] = ref
			validity[op.ID] = op.Validity
		case OperationFailed:
			// It changed nothing, and the cache went back when it failed:
			// dropping it is all its undo needs.
			refused = append(refused, op.ID)
			dropped++
		}
	}
	if len(waiting) > 0 {
		cancelled, err := s.store.CancelOperations(ctx, waiting)
		if err != nil {
			return err
		}
		var back []MessageRef
		for _, id := range cancelled {
			ref := refOf[id]
			// One a worker began meanwhile is no longer the caller's.
			done[ref] = true
			dropped++
			// A folder the server reset no longer knows this UID: the row
			// it names now is another message's, and is not touched.
			if valid, err := s.folderIs(ctx, accountID, ref.Folder, validity[id]); err != nil || !valid {
				continue
			}
			back = append(back, ref)
		}
		if len(back) > 0 {
			if err := revert(ctx, back); err != nil {
				return err
			}
		}
	}
	if err := s.store.FinishOperations(ctx, refused); err != nil {
		return err
	}
	if dropped > 0 {
		// The window's line of the queue is now out of date.
		s.wakeQueue(accountID)
	}
	var rest []MessageRef
	for _, m := range msgs {
		if done[m] {
			continue
		}
		if shown(ctx, m) {
			rest = append(rest, m)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	return invert(ctx, rest)
}

// folderIs reports whether a folder's UIDVALIDITY is still the one an
// action was queued under.
func (s *Service) folderIs(ctx context.Context, accountID, folder string, validity uint32) (bool, error) {
	if validity == 0 {
		return true, nil
	}
	f, ok, err := s.store.Folder(ctx, accountID, folder)
	if err != nil || !ok {
		return false, err
	}
	return f.UIDValidity == validity, nil
}

package mail

import (
	"context"
	"errors"
)

// UndoFlagChange takes back a change SetFlags made: the actions of it
// still waiting in the queue are dropped before the server makes them,
// and the cache shows what it did before them; a change the server has
// is set back.
func (s *Service) UndoFlagChange(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool) error {
	return s.undoChange(ctx, accountID, msgs,
		func(op PendingOperation) bool {
			return op.Kind == OpFlags && op.Flag == flag && op.On == on
		},
		func(ctx context.Context, refs []MessageRef) error {
			s.setFlagsInCache(ctx, accountID, byFolder(refs, ""), flag, !on)
			return nil
		},
		func(ctx context.Context, refs []MessageRef) error {
			return s.setFlags(ctx, accountID, refs, flag, !on, true)
		})
}

// UndoLabelChange takes back a change SetLabel made, as UndoFlagChange
// takes back a flag's. A label the folder keeps on this device alone is
// dealt with by the cache, as the change itself was.
func (s *Service) UndoLabelChange(ctx context.Context, accountID string, msgs []MessageRef, id LabelID, on bool) error {
	return s.undoChange(ctx, accountID, msgs,
		func(op PendingOperation) bool {
			return op.Kind == OpLabel && op.Label == id && op.On == on
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

// undoChange takes back a change the window made on several messages.
// The actions of it that still wait are dropped, never made, and revert
// puts the cache back as it was before them; those the server refused go
// too, as their cache change went back when they failed. What is left is
// a change the server has, or one a worker began: invert makes the other
// one, which waits in the queue as any change does.
func (s *Service) undoChange(ctx context.Context, accountID string, msgs []MessageRef,
	matches func(PendingOperation) bool,
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
	done := map[MessageRef]bool{}
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
		case OperationFailed:
			// It changed nothing: the cache went back when it failed.
			refused = append(refused, op.ID)
			done[ref] = true
		}
	}
	if len(waiting) > 0 {
		cancelled, err := s.store.CancelOperations(ctx, waiting)
		if err != nil {
			return err
		}
		var back []MessageRef
		for _, id := range cancelled {
			// One a worker began meanwhile is no longer the caller's.
			done[refOf[id]] = true
			back = append(back, refOf[id])
		}
		if len(back) > 0 {
			if err := revert(ctx, back); err != nil {
				return err
			}
		}
	}
	// An action the server refused changed nothing, so the user taking
	// it back has only to have it gone.
	if err := s.store.FinishOperations(ctx, refused); err != nil {
		return err
	}
	var rest []MessageRef
	for _, m := range msgs {
		if !done[m] {
			rest = append(rest, m)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	return invert(ctx, rest)
}

package mail

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
)

// ActivityID names an entry of the rules' log.
type ActivityID int64

// ActivityState is where an entry of the log stands.
type ActivityState string

const (
	// ActivityPending is a match whose actions have still to reach the
	// server: they wait for it, and are tried again after a later sync.
	ActivityPending ActivityState = "pending"
	// ActivityApplied is a match whose actions were all taken.
	ActivityApplied ActivityState = "applied"
	// ActivityFailed is a match an action of which the server refused, or
	// whose message was gone; Error says why. What the other actions
	// changed is in Changes, which an undo puts back.
	ActivityFailed ActivityState = "failed"
	// ActivityUndone is a match whose changes were put back, or whose
	// pending actions were dropped before any was taken.
	ActivityUndone ActivityState = "undone"
)

// MaxRuleActivity is how many entries the log keeps: the newest, and every
// pending one besides. An entry older than those is gone, and what it did
// can no longer be undone.
const MaxRuleActivity = 2000

// FlagChange is a flag a rule changed, and what it was before.
type FlagChange struct {
	Flag Flag `json:"flag"`
	Was  bool `json:"was"`
}

// RuleChanges is what a rule changed of a message, which an undo puts back.
type RuleChanges struct {
	// MovedTo is the folder the rule moved the message to, "" for none,
	// and DestUID its UID there when the server said.
	MovedTo string `json:"movedTo,omitempty"`
	DestUID UID    `json:"destUID,omitempty"`
	// Flags are the flags it changed, each with what it was.
	Flags []FlagChange `json:"flags,omitempty"`
	// LabelsAdded and LabelsRemoved are the labels it put on and took off.
	LabelsAdded   []LabelID `json:"labelsAdded,omitempty"`
	LabelsRemoved []LabelID `json:"labelsRemoved,omitempty"`
	// MovedBack is set once an undo took the move back, and BackUID is
	// the message's UID back in its folder, when known: an undo that
	// failed after that goes on from there.
	MovedBack bool `json:"movedBack,omitempty"`
	BackUID   UID  `json:"backUID,omitempty"`
}

// empty reports whether nothing was changed.
func (c RuleChanges) empty() bool {
	return c.MovedTo == "" && len(c.Flags) == 0 && len(c.LabelsAdded) == 0 && len(c.LabelsRemoved) == 0
}

// RuleActivity is an entry of the rules' log: a rule that matched a
// message, what it was to do and what it did.
type RuleActivity struct {
	ID ActivityID `json:"id"`
	At time.Time  `json:"at"`
	// Rule is the rule that matched, which may be gone since, and RuleName
	// its name then.
	Rule     RuleID `json:"rule"`
	RuleName string `json:"ruleName"`
	// Manual is set on a match of a run the user started (RunRule), unset
	// on one of mail that arrived.
	Manual bool `json:"manual,omitempty"`
	// The message, where the rule found it. Key is what it is known by
	// in any folder (LabelKey), by which an undo makes sure of it.
	Account     string  `json:"account"`
	Folder      string  `json:"folder"`
	UIDValidity uint32  `json:"uidValidity"`
	UID         UID     `json:"uid"`
	MessageID   string  `json:"messageID,omitempty"`
	Key         string  `json:"key"`
	Subject     string  `json:"subject"`
	From        Address `json:"from"`
	// Actions are the rule's actions as it matched, and Changes what they
	// changed.
	Actions []Action    `json:"actions"`
	Changes RuleChanges `json:"changes"`
	// Quiet is set when the message is not to be announced: a rule keeps
	// it quiet, moves it away or marks it read.
	Quiet bool          `json:"quiet,omitempty"`
	State ActivityState `json:"state"`
	// Error says why a failed entry failed, or why a pending one waits.
	Error string `json:"error,omitempty"`
}

// ActivityQuery asks for entries of the log, newest first. Each field set
// narrows it.
type ActivityQuery struct {
	ID      ActivityID
	Rule    RuleID
	Account string
	State   ActivityState
	// Folder, UIDValidity and UIDs narrow it to the entries of messages
	// where the rules found them.
	Folder      string
	UIDValidity uint32
	UIDs        []UID
	// Offset and Limit page the entries; a Limit not above zero reads all.
	Offset, Limit int
}

// ActivityPage is a page of the log, newest first, and how many entries
// the query finds in all.
type ActivityPage struct {
	Entries []RuleActivity `json:"entries"`
	Total   int            `json:"total"`
}

// ErrRuleMessageGone is a message no longer where a rule found it, or
// left it: moved or deleted since, or never given a UID there that can
// be found again.
var ErrRuleMessageGone = errors.New("mail: the message is no longer where the rule left it")

// RuleActivity reads the rules' log, newest first: all of it, or what the
// query narrows it to, as a rule's entries.
func (s *Service) RuleActivity(ctx context.Context, q ActivityQuery) (ActivityPage, error) {
	return s.store.RuleActivity(ctx, q)
}

// OnRuleActivity has report hear, on a goroutine of the engine, of each
// time actions of rules were applied in the background, with the account
// whose mail they changed: the window reads its list and the log again.
func (s *Service) OnRuleActivity(report func(accountID string)) {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	s.ruleReport = report
}

// newActivity is the entry of a rule's match of a message: pending, its
// actions still to apply, unless all it does is keep the message quiet.
func newActivity(r Rule, m Message, validity uint32, at time.Time) RuleActivity {
	e := RuleActivity{
		At: at, Rule: r.ID, RuleName: r.Name,
		Account: m.Account, Folder: m.Folder, UIDValidity: validity, UID: m.UID,
		MessageID: m.MessageID, Key: LabelKey(m.Summary), Subject: m.Subject, From: m.From,
		Actions: slices.Clone(r.Actions), Quiet: r.quiet(), State: ActivityApplied,
	}
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.Kind != ActionSkipNotification }) {
		e.State = ActivityPending
	}
	return e
}

// ruleLock returns the lock of an account's rule actions: applying them
// and undoing them take turns, so that neither works on an entry the
// other is changing.
func (s *Service) ruleLock(accountID string) *sync.Mutex {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	l := s.ruleLocks[accountID]
	if l == nil {
		l = &sync.Mutex{}
		s.ruleLocks[accountID] = l
	}
	return l
}

// ruleApplyTimeout bounds one round of applying an account's pending
// actions in the background.
const ruleApplyTimeout = 2 * time.Minute

// applyLater has an account's pending actions applied in the background:
// now, or once more when the round under way ends.
func (s *Service) applyLater(accountID string) {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	if s.ruleCtx.Err() != nil {
		return
	}
	if s.ruleRunning[accountID] {
		s.ruleAgain[accountID] = true
		return
	}
	s.ruleRunning[accountID] = true
	go s.applyPending(accountID)
}

// applyPending is the goroutine that applies an account's pending
// actions, round after round while more are asked for.
func (s *Service) applyPending(accountID string) {
	for {
		if s.applyPendingOnce(accountID) {
			s.ruleMu.Lock()
			report := s.ruleReport
			s.ruleMu.Unlock()
			if report != nil {
				report(accountID)
			}
		}
		s.ruleMu.Lock()
		if !s.ruleAgain[accountID] || s.ruleCtx.Err() != nil {
			delete(s.ruleRunning, accountID)
			delete(s.ruleAgain, accountID)
			s.ruleIdle.Broadcast()
			s.ruleMu.Unlock()
			return
		}
		s.ruleAgain[accountID] = false
		s.ruleMu.Unlock()
	}
}

// stopRules ends the background work of the rules for good, as the app
// quits, and returns when it has ended. What it left pending is applied
// after a sync of the next run.
func (s *Service) stopRules() {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	s.ruleStop()
	for len(s.ruleRunning) > 0 {
		s.ruleIdle.Wait()
	}
}

// applyPendingOnce applies an account's pending entries of enabled rules,
// oldest first. A paused rule's wait until it is enabled or undone. It
// reports whether an entry was settled.
func (s *Service) applyPendingOnce(accountID string) bool {
	ctx, cancel := context.WithTimeout(s.ruleCtx, ruleApplyTimeout)
	defer cancel()
	lock := s.ruleLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Account: accountID, State: ActivityPending})
	if err != nil || len(page.Entries) == 0 {
		return false
	}
	rules, err := s.store.Rules(ctx)
	if err != nil {
		return false
	}
	enabled := map[RuleID]bool{}
	for _, r := range rules {
		enabled[r.ID] = !r.Paused
	}
	var due []RuleActivity
	for _, e := range slices.Backward(page.Entries) {
		if enabled[e.Rule] {
			due = append(due, e)
		}
	}
	return slices.ContainsFunc(s.applyEntries(ctx, accountID, due), func(e RuleActivity) bool {
		return e.State != ActivityPending
	})
}

// hasPending reports whether an account has entries with actions still to
// apply.
func (s *Service) hasPending(ctx context.Context, accountID string) bool {
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Account: accountID, State: ActivityPending, Limit: 1})
	return err == nil && len(page.Entries) > 0
}

// ruleWaits reports whether an action that failed so is to be tried again
// later rather than given up: the server could not be reached, or would
// not let the account in.
func ruleWaits(err error) bool {
	switch failureOf(err) {
	case WatchFailNetwork, WatchFailAuth, WatchFailSignIn:
		return true
	}
	return errors.Is(err, context.Canceled)
}

// ruleOp is one change of messages of one folder, made with one call of
// the engine's own: a label put on (ActionAddLabel) or taken off
// (ActionRemoveLabel), a move (ActionMove), or a flag set or cleared
// (kind "").
type ruleOp struct {
	folder string
	kind   ActionKind
	label  LabelID
	flag   Flag
	on     bool
	dest   string
}

// opOrder puts labels and flags before moves: they change the message in
// the folder the rule found it in, before it goes.
func opOrder(op ruleOp) int {
	if op.kind == ActionMove {
		return 1
	}
	return 0
}

// applyEntries applies the actions of pending entries of one account,
// writes how each ended and returns them so. An entry's actions are
// applied from what the cache says of its message then, so that one
// changes only what is not so already, and what it changes is recorded
// for an undo. The entries of one message are applied in their order, one
// after the other; the rest go together, each change of a folder's
// messages in one call. An action the server refuses fails its entry; a
// server that cannot be reached leaves what is not done pending, to be
// tried after a later sync. The caller holds the account's rule lock.
func (s *Service) applyEntries(ctx context.Context, accountID string, entries []RuleActivity) []RuleActivity {
	out := slices.Clone(entries)
	var rounds [][]int
	nth := map[MessageRef]int{}
	for i, e := range out {
		if e.State != ActivityPending {
			continue
		}
		ref := MessageRef{e.Folder, e.UID}
		k := nth[ref]
		nth[ref]++
		if k == len(rounds) {
			rounds = append(rounds, nil)
		}
		rounds[k] = append(rounds[k], i)
	}
	for _, round := range rounds {
		goOn := s.applyRound(ctx, accountID, out, round)
		var settled []RuleActivity
		for _, i := range round {
			settled = append(settled, out[i])
		}
		// What was done is recorded even when ctx ended on the way.
		_ = s.store.UpdateRuleActivity(context.WithoutCancel(ctx), settled)
		if !goOn {
			break
		}
	}
	return out
}

// applyRound applies the entries of out at round, each of another message,
// and reports false when the server could not be reached.
func (s *Service) applyRound(ctx context.Context, accountID string, out []RuleActivity, round []int) bool {
	type place struct {
		folder string
		uid    UID
	}
	byFolder := map[string][]UID{}
	for _, i := range round {
		byFolder[out[i].Folder] = append(byFolder[out[i].Folder], out[i].UID)
	}
	cached := map[place]Message{}
	validity := map[string]uint32{}
	for folder, uids := range byFolder {
		f, ok, err := s.store.Folder(ctx, accountID, folder)
		var msgs []Message
		if err == nil && ok {
			validity[folder] = f.UIDValidity
			msgs, err = s.store.MessageHeaders(ctx, accountID, folder, uids)
		}
		if err != nil {
			for _, i := range round {
				out[i].Error = err.Error()
			}
			return false
		}
		for _, m := range msgs {
			cached[place{folder, m.UID}] = m
		}
	}
	var ops []ruleOp
	members := map[ruleOp][]int{}
	add := func(op ruleOp, i int) {
		if _, ok := members[op]; !ok {
			ops = append(ops, op)
		}
		members[op] = append(members[op], i)
	}
	failed := map[int]error{}
	for _, i := range round {
		e := out[i]
		m, ok := cached[place{e.Folder, e.UID}]
		if !ok || validity[e.Folder] != e.UIDValidity {
			failed[i] = ErrRuleMessageGone
			continue
		}
		for _, a := range e.Actions {
			switch {
			case a.Kind == ActionAddLabel && !m.HasLabel(a.Label):
				add(ruleOp{folder: e.Folder, kind: ActionAddLabel, label: a.Label, on: true}, i)
			case a.Kind == ActionRemoveLabel && m.HasLabel(a.Label):
				add(ruleOp{folder: e.Folder, kind: ActionRemoveLabel, label: a.Label}, i)
			case a.Kind == ActionStar && !m.Flagged:
				add(ruleOp{folder: e.Folder, flag: FlagFlagged, on: true}, i)
			case a.Kind == ActionMarkRead && !m.Seen:
				add(ruleOp{folder: e.Folder, flag: FlagSeen, on: true}, i)
			case a.Kind == ActionMarkUnread && m.Seen:
				add(ruleOp{folder: e.Folder, flag: FlagSeen}, i)
			case a.Kind == ActionMove && a.Folder != e.Folder:
				add(ruleOp{folder: e.Folder, kind: ActionMove, dest: a.Folder}, i)
			}
		}
	}
	sort.SliceStable(ops, func(a, b int) bool { return opOrder(ops[a]) < opOrder(ops[b]) })
	var waiting error
	for _, op := range ops {
		var refs []MessageRef
		for _, i := range members[op] {
			refs = append(refs, MessageRef{op.folder, out[i].UID})
		}
		var err error
		dest := map[UID]UID{}
		switch op.kind {
		case ActionMove:
			var moved []MoveResult
			moved, err = s.moveMessages(ctx, accountID, refs, op.dest, false)
			for _, r := range moved {
				dest[r.UID] = r.DestUID
			}
		case ActionAddLabel, ActionRemoveLabel:
			err = s.setLabel(ctx, accountID, refs, op.label, op.on, false)
		default:
			err = s.setFlags(ctx, accountID, refs, op.flag, op.on, false)
		}
		if err != nil && ruleWaits(err) {
			waiting = err
			break
		}
		for _, i := range members[op] {
			if err != nil {
				if failed[i] == nil {
					failed[i] = err
				}
				continue
			}
			c := &out[i].Changes
			switch op.kind {
			case ActionMove:
				c.MovedTo, c.DestUID = op.dest, dest[out[i].UID]
			case ActionAddLabel:
				c.LabelsAdded = append(c.LabelsAdded, op.label)
			case ActionRemoveLabel:
				c.LabelsRemoved = append(c.LabelsRemoved, op.label)
			default:
				c.Flags = append(c.Flags, FlagChange{Flag: op.flag, Was: !op.on})
			}
		}
	}
	for _, i := range round {
		e := &out[i]
		switch {
		case failed[i] != nil:
			e.State, e.Error = ActivityFailed, failed[i].Error()
		case waiting != nil:
			e.Error = waiting.Error()
		default:
			e.State, e.Error = ActivityApplied, ""
		}
	}
	return waiting == nil
}

// dropPending drops a rule's pending entries that had not begun, as the
// rule is paused or deleted: they are marked undone. Those that changed
// something already stay pending, to be finished or undone.
func (s *Service) dropPending(ctx context.Context, id RuleID) error {
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Rule: id, State: ActivityPending})
	if err != nil {
		return err
	}
	var accounts []string
	for _, e := range page.Entries {
		if !slices.Contains(accounts, e.Account) {
			accounts = append(accounts, e.Account)
		}
	}
	for _, account := range accounts {
		if err := s.dropPendingOf(ctx, account, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) dropPendingOf(ctx context.Context, accountID string, id RuleID) error {
	lock := s.ruleLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Rule: id, Account: accountID, State: ActivityPending})
	if err != nil {
		return err
	}
	var dropped []RuleActivity
	for _, e := range page.Entries {
		if e.Changes.empty() {
			e.State, e.Error = ActivityUndone, ""
			dropped = append(dropped, e)
		}
	}
	return s.store.UpdateRuleActivity(ctx, dropped)
}

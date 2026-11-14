package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emersion/go-imap/v2"
)

// UndoActivity puts back what an entry of the log changed and marks it
// undone: a move goes back, then the flags the rule changed are as they
// were, and the labels it put on or took off are off or on again. The
// message is found where the rule moved it by the UID the server gave it
// there or, without one, by its Message-ID, and either way only when what
// the server says of it is the entry's message. An entry that changed
// nothing, as one pending that had not begun, is marked undone at once;
// one undone already is left as it is. ErrNotFound for no such entry, and
// ErrRuleMessageGone for a message that is no longer where the rule left
// it, which is then left alone.
func (s *Service) UndoActivity(ctx context.Context, id ActivityID) error {
	page, err := s.store.RuleActivity(ctx, ActivityQuery{ID: id})
	if err != nil {
		return err
	}
	if len(page.Entries) == 0 {
		return ErrNotFound
	}
	accountID := page.Entries[0].Account
	lock := s.ruleLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	// Read again under the lock: a round of applying may have changed it.
	if page, err = s.store.RuleActivity(ctx, ActivityQuery{ID: id}); err != nil {
		return err
	}
	_, err = s.undoEntries(ctx, accountID, page.Entries)
	return err
}

// UndoRule undoes every entry of a rule's that is not undone yet, as
// UndoActivity does, account by account: what the window offers as a rule
// is disabled. It returns how many entries it undid; the error is of the
// first it could not, the others being undone all the same.
func (s *Service) UndoRule(ctx context.Context, id RuleID) (int, error) {
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Rule: id})
	if err != nil {
		return 0, err
	}
	var accounts []string
	for _, e := range page.Entries {
		if e.State != ActivityUndone && !slices.Contains(accounts, e.Account) {
			accounts = append(accounts, e.Account)
		}
	}
	undone := 0
	var first error
	for _, account := range accounts {
		n, err := s.undoRuleOf(ctx, account, id)
		undone += n
		if err != nil && first == nil {
			first = err
		}
	}
	return undone, first
}

func (s *Service) undoRuleOf(ctx context.Context, accountID string, id RuleID) (int, error) {
	lock := s.ruleLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	page, err := s.store.RuleActivity(ctx, ActivityQuery{Rule: id, Account: accountID})
	if err != nil {
		return 0, err
	}
	return s.undoEntries(ctx, accountID, page.Entries)
}

// marks reports whether the changes hold flags or labels to put back.
func (c RuleChanges) marks() bool {
	return len(c.Flags) > 0 || len(c.LabelsAdded) > 0 || len(c.LabelsRemoved) > 0
}

// undoRun is one undo of entries of an account, newest first: what each
// has still to put back, and why one could not.
type undoRun struct {
	s       *Service
	ctx     context.Context
	account string
	todo    []RuleActivity
	errs    map[int]error
	first   error
	// halted is set when the server could not be reached: what is left
	// waits for the next undo.
	halted bool
}

func (u *undoRun) fail(i int, err error) {
	if u.errs[i] == nil {
		u.errs[i] = err
	}
	if u.first == nil {
		u.first = err
	}
	u.halted = u.halted || ruleWaits(err)
}

// undoEntries puts back what entries of one account changed, in few
// connections: the moves back between each pair of folders in one
// command, then the flags and labels of each folder that change alike in
// one call. It writes how each ended, and returns how many it undid with
// the first error. The caller holds the account's rule lock.
func (s *Service) undoEntries(ctx context.Context, accountID string, entries []RuleActivity) (int, error) {
	u := &undoRun{s: s, ctx: ctx, account: accountID, errs: map[int]error{}}
	for _, e := range entries {
		if e.State != ActivityUndone {
			u.todo = append(u.todo, e)
		}
	}
	if len(u.todo) == 0 {
		return 0, nil
	}
	u.moveBack()
	restored := u.restoreMarks()
	undone := 0
	for i := range u.todo {
		e := &u.todo[i]
		if err := u.errs[i]; err != nil {
			e.Error = err.Error()
			continue
		}
		if (e.Changes.MovedTo == "" || e.Changes.MovedBack) && (!e.Changes.marks() || restored[i]) {
			e.State, e.Error = ActivityUndone, ""
			undone++
		}
	}
	// What was put back is recorded even when ctx ended on the way.
	if err := s.store.UpdateRuleActivity(context.WithoutCancel(ctx), u.todo); err != nil && u.first == nil {
		u.first = err
	}
	return undone, u.first
}

// moveBack takes back the moves of the entries: each message goes from
// where the rule moved it to the folder it was in, one command for each
// pair of folders. An entry records that its message went back, and its
// UID there when the server said, so that an undo that fails after goes
// on from there.
func (u *undoRun) moveBack() {
	type route struct{ from, to string }
	routes := map[route][]int{}
	var order []route
	for i, e := range u.todo {
		if e.Changes.MovedTo != "" && !e.Changes.MovedBack {
			r := route{e.Changes.MovedTo, e.Folder}
			if routes[r] == nil {
				order = append(order, r)
			}
			routes[r] = append(routes[r], i)
		}
	}
	if len(order) == 0 {
		return
	}
	a, password, err := u.s.account(u.ctx, u.account)
	if err != nil {
		for _, r := range order {
			for _, i := range routes[r] {
				u.fail(i, err)
			}
		}
		return
	}
	for _, r := range order {
		if u.halted {
			return
		}
		at, err := u.s.locate(u.ctx, a, password, r.from, u.todo, routes[r])
		if err != nil {
			for _, i := range routes[r] {
				u.fail(i, err)
			}
			continue
		}
		var uids []UID
		var moving []int
		for _, i := range routes[r] {
			uid, ok := at[i]
			if !ok {
				u.fail(i, ErrRuleMessageGone)
				continue
			}
			moving = append(moving, i)
			uids = append(uids, uid)
		}
		slices.Sort(uids)
		uids = slices.Compact(uids)
		if len(uids) == 0 {
			continue
		}
		moved, err := u.s.moveOnServer(u.ctx, a, password, r.from, uids, r.to)
		if err != nil {
			for _, i := range moving {
				u.fail(i, err)
			}
			continue
		}
		back := map[UID]UID{}
		for _, m := range moved {
			back[m.UID] = m.DestUID
		}
		for _, i := range moving {
			u.todo[i].Changes.MovedBack, u.todo[i].Changes.BackUID = true, back[at[i]]
		}
		// Where the folder they left is cached, their rows go with them.
		if stale, err := u.s.store.MessageHeaders(u.ctx, u.account, r.from, uids); err == nil {
			for _, m := range stale {
				_ = u.s.store.DeleteMessage(u.ctx, u.account, r.from, m.UID)
			}
		}
	}
}

// restoreMarks puts back the flags and labels the entries changed, where
// each message is now: where the rule found it, or back in that folder
// after a move, which is synced first so that the cache has it there. A
// message is changed only when the cache has, at that place, the entry's
// message. It returns the entries whose flags and labels are all back.
func (u *undoRun) restoreMarks() map[int]bool {
	type place struct {
		folder string
		uid    UID
	}
	where := map[int]place{}
	synced := map[string]error{}
	for i, e := range u.todo {
		c := e.Changes
		if u.halted {
			break
		}
		if u.errs[i] != nil || !c.marks() {
			continue
		}
		switch {
		case c.MovedTo == "":
			where[i] = place{e.Folder, e.UID}
		case c.MovedBack:
			err, done := synced[e.Folder]
			if !done {
				err = u.s.SyncFolder(u.ctx, u.account, e.Folder, 0)
				synced[e.Folder] = err
			}
			if err != nil {
				u.fail(i, err)
				continue
			}
			uid := c.BackUID
			if uid == 0 {
				uid = u.s.cachedByKey(u.ctx, u.account, e.Folder, e.Key)
			}
			if uid == 0 {
				u.fail(i, ErrRuleMessageGone)
				continue
			}
			where[i] = place{e.Folder, uid}
		}
	}
	rows := map[place]Message{}
	byFolder := map[string][]UID{}
	for _, p := range where {
		byFolder[p.folder] = append(byFolder[p.folder], p.uid)
	}
	for folder, uids := range byFolder {
		msgs, err := u.s.store.MessageHeaders(u.ctx, u.account, folder, uids)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			rows[place{folder, m.UID}] = m
		}
	}
	var ops []ruleOp
	members := map[ruleOp][]int{}
	left := map[int]int{}
	add := func(op ruleOp, i int) {
		if _, ok := members[op]; !ok {
			ops = append(ops, op)
		}
		members[op] = append(members[op], i)
		left[i]++
	}
	for i := range u.todo {
		p, ok := where[i]
		if !ok {
			continue
		}
		if m, ok := rows[p]; !ok || LabelKey(m.Summary) != u.todo[i].Key {
			u.fail(i, ErrRuleMessageGone)
			continue
		}
		c := u.todo[i].Changes
		for _, f := range c.Flags {
			add(ruleOp{folder: p.folder, flag: f.Flag, on: f.Was}, i)
		}
		for _, l := range c.LabelsAdded {
			add(ruleOp{folder: p.folder, kind: ActionRemoveLabel, label: l}, i)
		}
		for _, l := range c.LabelsRemoved {
			add(ruleOp{folder: p.folder, kind: ActionAddLabel, label: l, on: true}, i)
		}
	}
	for _, op := range ops {
		if u.halted {
			break
		}
		var refs []MessageRef
		for _, i := range members[op] {
			refs = append(refs, MessageRef{op.folder, where[i].uid})
		}
		var err error
		if op.kind == "" {
			err = u.s.setFlags(u.ctx, u.account, refs, op.flag, op.on, false)
		} else if err = u.s.setLabel(u.ctx, u.account, refs, op.label, op.on, false); errors.Is(err, ErrNotFound) {
			// The label is gone, and with it what there was to put back.
			err = nil
		}
		for _, i := range members[op] {
			if err != nil {
				u.fail(i, err)
			} else {
				left[i]--
			}
		}
	}
	restored := map[int]bool{}
	for i, n := range left {
		restored[i] = n == 0 && u.errs[i] == nil
	}
	return restored
}

// cachedByKey returns the UID of the newest cached message of a folder that
// is known by a key (LabelKey), 0 for none.
func (s *Service) cachedByKey(ctx context.Context, accountID, folder, key string) UID {
	msgs, err := s.store.MessageHeaders(ctx, accountID, folder, nil)
	if err != nil {
		return 0
	}
	var uid UID
	for _, m := range msgs {
		if LabelKey(m.Summary) == key {
			uid = max(uid, m.UID)
		}
	}
	return uid
}

// locate finds in a folder on the server the messages of entries that
// rules moved there: by the UID the server gave each, else by its
// Message-ID, and either way only a message whose envelope makes the
// entry's key. What a UID names changes with the folder's UIDVALIDITY,
// and a HEADER search matches any header that contains the value, so
// both only propose (see draftBox.confirm); an empty Message-ID, which
// would match every message that has the header, is never searched for.
// It returns the UID of each entry found, the newest where several are
// its message.
func (s *Service) locate(ctx context.Context, a Account, password, folder string, entries []RuleActivity, idx []int) (map[int]UID, error) {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return nil, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	out := map[int]UID{}
	confirm := func(proposed map[int][]imap.UID) error {
		var set imap.UIDSet
		for _, uids := range proposed {
			for _, uid := range uids {
				set.AddNum(uid)
			}
		}
		if len(set) == 0 {
			return nil
		}
		fetched, err := c.client.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
		if err != nil {
			return fmt.Errorf("imap: fetch %s: %w", folder, err)
		}
		keys := map[imap.UID]string{}
		for _, buf := range fetched {
			if buf.UID != 0 && buf.Envelope != nil {
				keys[buf.UID] = LabelKey(summaryFromFetch(buf, false).Summary)
			}
		}
		for i, uids := range proposed {
			for _, uid := range uids {
				if key := keys[uid]; key != "" && key == entries[i].Key && UID(uid) > out[i] {
					out[i] = UID(uid)
				}
			}
		}
		return nil
	}
	byUID := map[int][]imap.UID{}
	for _, i := range idx {
		if uid := entries[i].Changes.DestUID; uid != 0 {
			byUID[i] = []imap.UID{imap.UID(uid)}
		}
	}
	if err := confirm(byUID); err != nil {
		return nil, err
	}
	searched := map[int][]imap.UID{}
	for _, i := range idx {
		e := entries[i]
		if _, found := out[i]; found || e.MessageID == "" {
			continue
		}
		data, err := c.client.UIDSearch(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: e.MessageID}},
		}, nil).Wait()
		if err != nil {
			return nil, fmt.Errorf("imap: search %s: %w", folder, err)
		}
		searched[i] = data.AllUIDs()
	}
	if err := confirm(searched); err != nil {
		return nil, err
	}
	return out, nil
}

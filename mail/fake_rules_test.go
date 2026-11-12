package mail

import (
	"context"
	"fmt"
	"slices"
	"sort"
)

// The rules of memStore, kept by the rules the SQLite store keeps them by:
// rules in their order with IDs never given again, a log whose entries go
// with their account and outlive their rule, and notes of messages moving
// into a folder.

// memExpected is a note of a message moving into a folder.
type memExpected struct{ account, folder, key string }

func cloneRule(r Rule) Rule {
	r.Conditions, r.Actions = slices.Clone(r.Conditions), slices.Clone(r.Actions)
	return r
}

func cloneActivity(e RuleActivity) RuleActivity {
	e.Actions = slices.Clone(e.Actions)
	e.Changes.Flags = slices.Clone(e.Changes.Flags)
	e.Changes.LabelsAdded = slices.Clone(e.Changes.LabelsAdded)
	e.Changes.LabelsRemoved = slices.Clone(e.Changes.LabelsRemoved)
	return e
}

func (s *memStore) Rules(ctx context.Context) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, cloneRule(r))
	}
	return out, nil
}

// SaveRule refuses a rule of an account there is not, as the SQLite
// store's foreign key does.
func (s *memStore) SaveRule(ctx context.Context, r Rule) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := r.Account(); a != "" {
		if _, ok := s.accounts[a]; !ok {
			return Rule{}, fmt.Errorf("rule %q: no account %q", r.Name, a)
		}
	}
	if r.ID == 0 {
		s.rulesNext++
		r.ID = s.rulesNext
		s.rules = append(s.rules, cloneRule(r))
		return r, nil
	}
	for i := range s.rules {
		if s.rules[i].ID == r.ID {
			s.rules[i] = cloneRule(r)
			return r, nil
		}
	}
	return Rule{}, ErrNotFound
}

func (s *memStore) MoveRule(ctx context.Context, id RuleID, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := slices.IndexFunc(s.rules, func(r Rule) bool { return r.ID == id })
	if at < 0 {
		return ErrNotFound
	}
	r := s.rules[at]
	s.rules = slices.Delete(s.rules, at, at+1)
	s.rules = slices.Insert(s.rules, min(max(index, 0), len(s.rules)), r)
	return nil
}

func (s *memStore) DeleteRule(ctx context.Context, id RuleID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := slices.IndexFunc(s.rules, func(r Rule) bool { return r.ID == id })
	if at < 0 {
		return ErrNotFound
	}
	s.rules = slices.Delete(s.rules, at, at+1)
	return nil
}

// AddRuleActivity keeps the newest MaxRuleActivity entries and every
// pending one, as the SQLite store does.
func (s *memStore) AddRuleActivity(ctx context.Context, entries []RuleActivity) ([]RuleActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(entries)
	for i := range out {
		s.logNext++
		out[i].ID = s.logNext
		s.ruleLog = append(s.ruleLog, cloneActivity(out[i]))
	}
	newest := len(s.ruleLog) - MaxRuleActivity
	kept := s.ruleLog[:0]
	for i, e := range s.ruleLog {
		if i >= newest || e.State == ActivityPending {
			kept = append(kept, e)
		}
	}
	s.ruleLog = kept
	return out, nil
}

// inQuery reports whether an entry is one a query asks for.
func inQuery(q ActivityQuery, e RuleActivity) bool {
	return (q.ID == 0 || e.ID == q.ID) && (q.Rule == 0 || e.Rule == q.Rule) &&
		(q.Account == "" || e.Account == q.Account) && (q.State == "" || e.State == q.State) &&
		(q.Folder == "" || e.Folder == q.Folder) && (q.UIDValidity == 0 || e.UIDValidity == q.UIDValidity) &&
		(len(q.UIDs) == 0 || slices.Contains(q.UIDs, e.UID))
}

func (s *memStore) RuleActivity(ctx context.Context, q ActivityQuery) (ActivityPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []RuleActivity
	for _, e := range slices.Backward(s.ruleLog) {
		if inQuery(q, e) {
			found = append(found, cloneActivity(e))
		}
	}
	page := ActivityPage{Entries: []RuleActivity{}, Total: len(found)}
	found = found[min(max(q.Offset, 0), len(found)):]
	if q.Limit > 0 && len(found) > q.Limit {
		found = found[:q.Limit]
	}
	page.Entries = append(page.Entries, found...)
	return page, nil
}

func (s *memStore) UpdateRuleActivity(ctx context.Context, entries []RuleActivity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		for i := range s.ruleLog {
			if s.ruleLog[i].ID == e.ID {
				changed := cloneActivity(e)
				s.ruleLog[i].State, s.ruleLog[i].Changes, s.ruleLog[i].Error = changed.State, changed.Changes, changed.Error
			}
		}
	}
	return nil
}

func (s *memStore) MessageHeaders(ctx context.Context, accountID, folder string, uids []UID) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folderLocked(accountID, folder)
	var out []Message
	for uid, m := range s.messages[msgKey(accountID, folder)] {
		if s.tombs[tombKey(accountID, folder, uid)] || (uids != nil && !slices.Contains(uids, uid)) {
			continue
		}
		head := Message{Summary: s.labelledLocked(m.Summary, accountID, f, m), RawHeaders: m.RawHeaders}
		head.Account, head.Folder, head.UID = accountID, folder, uid
		out = append(out, head)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

func (s *memStore) hasFolderLocked(accountID, folder string) bool {
	return slices.ContainsFunc(s.folders[accountID], func(f Folder) bool { return f.Name == folder })
}

func (s *memStore) ExpectArrivals(ctx context.Context, accountID, folder string, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasFolderLocked(accountID, folder) {
		return ErrNotFound
	}
	for _, key := range keys {
		s.expected = append(s.expected, memExpected{accountID, folder, key})
	}
	if extra := len(s.expected) - MaxExpectedArrivals; extra > 0 {
		s.expected = slices.Delete(s.expected, 0, extra)
	}
	return nil
}

func (s *memStore) TakeArrivals(ctx context.Context, accountID, folder string, keys []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasFolderLocked(accountID, folder) {
		return nil, ErrNotFound
	}
	var out []string
	s.expected = slices.DeleteFunc(s.expected, func(x memExpected) bool {
		taken := x.account == accountID && x.folder == folder && slices.Contains(keys, x.key)
		if taken && !slices.Contains(out, x.key) {
			out = append(out, x.key)
		}
		return taken
	})
	return out, nil
}

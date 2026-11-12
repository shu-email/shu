package mail

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
)

// The labels of memStore, kept by the rules the SQLite store keeps them
// by: a label's messages as the server keeps them by their rows, and
// those kept on this device by their LabelKey.

// memLabel is a label with its place among its account's.
type memLabel struct {
	Label
	position int
}

// labelKeyOf is where memStore keeps the labels a message has on this
// device: its account and LabelKey.
func labelKeyOf(accountID string, m Message) string { return accountID + "\x00" + LabelKey(m.Summary) }

func (s *memStore) labelsOfLocked(accountID string) []*memLabel {
	var out []*memLabel
	for _, l := range s.labels {
		if l.Account == accountID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].position != out[j].position {
			return out[i].position < out[j].position
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *memStore) nameTakenLocked(accountID string, except LabelID) func(string) bool {
	return func(key string) bool {
		for _, l := range s.labelsOfLocked(accountID) {
			if l.ID != except && LabelNameKey(l.Name) == key {
				return true
			}
		}
		return false
	}
}

func (s *memStore) keywordTakenLocked(accountID string) func(string) bool {
	return func(lower string) bool { return s.labelByKeywordLocked(accountID, lower) != nil }
}

func (s *memStore) labelByKeywordLocked(accountID, keyword string) *memLabel {
	for _, l := range s.labelsOfLocked(accountID) {
		if strings.EqualFold(l.Keyword, keyword) {
			return l
		}
	}
	return nil
}

func (s *memStore) lastPositionLocked(accountID string) int {
	last := -1
	for _, l := range s.labelsOfLocked(accountID) {
		last = max(last, l.position)
	}
	return last
}

func (s *memStore) addLabelLocked(l Label) *memLabel {
	s.labelsNext++
	l.ID = s.labelsNext
	m := &memLabel{Label: l, position: s.lastPositionLocked(l.Account) + 1}
	if m.Color == "" {
		m.Color = LabelColorAt(m.position)
	}
	s.labels[l.ID] = m
	return m
}

// messageLabelsLocked are the labels a message has, in its account's
// order, those the server keeps on it first.
func (s *memStore) messageLabelsLocked(accountID, folder string, uid UID, m Message) []MessageLabel {
	synced := s.msgLabels[tombKey(accountID, folder, uid)]
	local := s.keptLabels[labelKeyOf(accountID, m)]
	var out []MessageLabel
	for _, l := range s.labelsOfLocked(accountID) {
		switch {
		case synced[l.ID]:
			out = append(out, MessageLabel{ID: l.ID})
		case local[l.ID]:
			out = append(out, MessageLabel{ID: l.ID, Local: true})
		}
	}
	return out
}

// orderedLabelsLocked lists a set of an account's labels in its order.
func (s *memStore) orderedLabelsLocked(accountID string, set map[LabelID]bool) []LabelID {
	var out []LabelID
	for _, l := range s.labelsOfLocked(accountID) {
		if set[l.ID] {
			out = append(out, l.ID)
		}
	}
	return out
}

// labelledLocked gives a summary of a message of a folder its labels.
func (s *memStore) labelledLocked(sum Summary, accountID string, f Folder, m Message) Summary {
	sum.Labels = s.messageLabelsLocked(accountID, f.Name, m.UID, m)
	sum.LabelsLocal = f.KeywordsKnown() && !f.KeepsNewKeywords()
	return sum
}

func (s *memStore) folderLocked(accountID, name string) Folder {
	for _, f := range s.folders[accountID] {
		if f.Name == name {
			return f
		}
	}
	return Folder{Name: name}
}

// hasLabelLocked reports whether a message has a label, of either kind.
func (s *memStore) hasLabelLocked(accountID, folder string, m Message, id LabelID) bool {
	return s.msgLabels[tombKey(accountID, folder, m.UID)][id] || s.keptLabels[labelKeyOf(accountID, m)][id]
}

// namedLabelLocked reports whether a message has its account's label of a
// name.
func (s *memStore) namedLabelLocked(accountID, folder string, m Message, name string) bool {
	key := LabelNameKey(strings.TrimSpace(name))
	for _, l := range s.labelsOfLocked(accountID) {
		if LabelNameKey(l.Name) == key && s.hasLabelLocked(accountID, folder, m, l.ID) {
			return true
		}
	}
	return false
}

func (s *memStore) Labels(ctx context.Context, accountID string) ([]Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Label{}
	for _, l := range s.labelsOfLocked(accountID) {
		label := l.Label
		label.Local = KeptLocally(s.folders[accountID], l.Keyword)
		label.Unread = 0
		for _, f := range s.folders[accountID] {
			if !f.InLabelScope() {
				continue
			}
			for uid, m := range s.messages[msgKey(accountID, f.Name)] {
				if !m.Seen && !s.tombs[tombKey(accountID, f.Name, uid)] && s.hasLabelLocked(accountID, f.Name, m, l.ID) {
					label.Unread++
				}
			}
		}
		out = append(out, label)
	}
	return out, nil
}

func (s *memStore) CreateLabel(ctx context.Context, l Label) (Label, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTakenLocked(l.Account, 0)(LabelNameKey(l.Name)) {
		return Label{}, ErrLabelExists
	}
	l.Keyword = FreeKeyword(l.Keyword, s.keywordTakenLocked(l.Account))
	m := s.addLabelLocked(l)
	out := m.Label
	out.Local = KeptLocally(s.folders[l.Account], out.Keyword)
	return out, nil
}

func (s *memStore) UpdateLabel(ctx context.Context, l Label) (Label, error) {
	s.mu.Lock()
	have := s.labels[l.ID]
	if have == nil || have.Account != l.Account {
		s.mu.Unlock()
		return Label{}, ErrNotFound
	}
	if s.nameTakenLocked(l.Account, l.ID)(LabelNameKey(l.Name)) {
		s.mu.Unlock()
		return Label{}, ErrLabelExists
	}
	have.Name = l.Name
	if l.Color != "" {
		have.Color = l.Color
	}
	s.mu.Unlock()
	labels, _ := s.Labels(ctx, l.Account)
	for _, got := range labels {
		if got.ID == l.ID {
			return got, nil
		}
	}
	return Label{}, ErrNotFound
}

func (s *memStore) DeleteLabel(ctx context.Context, accountID string, id LabelID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.labels[id]
	if l == nil || l.Account != accountID {
		return ErrNotFound
	}
	delete(s.labels, id)
	for _, set := range s.msgLabels {
		delete(set, id)
	}
	for _, set := range s.keptLabels {
		delete(set, id)
	}
	s.labelsGone[accountID+"\x00"+strings.ToLower(l.Keyword)] = at
	return nil
}

func (s *memStore) SetLabel(ctx context.Context, accountID string, id LabelID, folder string, uids []UID, on, local bool) ([]UID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.labels[id]; l == nil || l.Account != accountID {
		return nil, ErrNotFound
	}
	if !slices.ContainsFunc(s.folders[accountID], func(f Folder) bool { return f.Name == folder }) {
		return nil, ErrNotFound
	}
	sorted := slices.Clone(uids)
	slices.Sort(sorted)
	var changed []UID
	keys := map[string]bool{}
	for _, uid := range slices.Compact(sorted) {
		m, ok := s.messages[msgKey(accountID, folder)][uid]
		if !ok {
			continue
		}
		key := tombKey(accountID, folder, uid)
		set := s.msgLabels
		if local {
			key, set = labelKeyOf(accountID, m), s.keptLabels
			if keys[key] {
				changed = append(changed, uid)
				continue
			}
		}
		if set[key][id] == on {
			continue
		}
		keys[key] = true
		changed = append(changed, uid)
		if set[key] == nil {
			set[key] = map[LabelID]bool{}
		}
		if on {
			set[key][id] = true
		} else {
			delete(set[key], id)
		}
	}
	return changed, nil
}

func (s *memStore) LabelRefs(ctx context.Context, accountID string, id LabelID) ([]MessageRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.labels[id]; l == nil || l.Account != accountID {
		return nil, ErrNotFound
	}
	var out []MessageRef
	for _, f := range s.folders[accountID] {
		for uid := range s.messages[msgKey(accountID, f.Name)] {
			if s.msgLabels[tombKey(accountID, f.Name, uid)][id] {
				out = append(out, MessageRef{f.Name, uid})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Folder != out[j].Folder {
			return out[i].Folder < out[j].Folder
		}
		return out[i].UID < out[j].UID
	})
	return out, nil
}

func (s *memStore) SetPermanentFlags(ctx context.Context, accountID, folder string, flags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.folders[accountID] {
		if s.folders[accountID][i].Name == folder {
			s.folders[accountID][i].PermanentFlags = slices.Clone(flags)
			return nil
		}
	}
	return ErrNotFound
}

// keywordsLocked gives messages of a folder the labels of their keywords,
// as the SQLite store does (see keywordLabels there).
func (s *memStore) keywordsLocked(accountID, folder string, msgs []Message, flags []FlagUpdate, since time.Time) {
	apply := func(uid UID, keywords []string) {
		if keywords == nil {
			return
		}
		set := map[LabelID]bool{}
		for _, keyword := range keywords {
			if !IsLabelKeyword(keyword) {
				continue
			}
			if at, ok := s.labelsGone[accountID+"\x00"+strings.ToLower(keyword)]; ok && !since.IsZero() && !since.After(at) {
				continue
			}
			l := s.labelByKeywordLocked(accountID, keyword)
			if l == nil {
				color := KeywordColor(keyword)
				l = s.addLabelLocked(Label{Account: accountID, Keyword: keyword, Color: color,
					Name: FreeLabelName(LabelName(keyword), s.nameTakenLocked(accountID, 0))})
			}
			set[l.ID] = true
		}
		s.msgLabels[tombKey(accountID, folder, uid)] = set
	}
	for _, m := range msgs {
		apply(m.UID, m.Keywords)
	}
	for _, f := range flags {
		if _, ok := s.messages[msgKey(accountID, folder)][f.UID]; ok {
			apply(f.UID, f.Keywords)
		}
	}
}

// dropLabelsLocked forgets the labels the server keeps on the cached
// messages of a folder, as their rows go.
func (s *memStore) dropLabelsLocked(accountID, folder string) {
	for uid := range s.messages[msgKey(accountID, folder)] {
		delete(s.msgLabels, tombKey(accountID, folder, uid))
	}
}

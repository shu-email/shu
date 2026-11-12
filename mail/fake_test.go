package mail

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// memStore is the in-memory Store the mail tests use. The UI and the
// SQLite store are tested at their own level.
type memStore struct {
	mu       sync.Mutex
	accounts map[string]Account
	active   string
	folders  map[string][]Folder
	messages map[string]map[UID]Message
	outbox   map[string]PendingSend
	drafts   map[string]Draft
	// draftOrder is when each draft was created, which breaks a tie
	// between two edited in the same second as SQLite's rowid does.
	draftOrder map[string]int
	// failDraftSync makes SetDraftSync fail, as a full disk would.
	failDraftSync error
	// afterDrafts runs once, right after the next Drafts call returned
	// its rows: the moment a sync has read the drafts and something else
	// changes them. afterDraft does the same for the next Draft call,
	// which is an upload reading the text it is about to send.
	afterDrafts func()
	afterDraft  func()
}

func newMemStore() *memStore {
	return &memStore{
		accounts:   map[string]Account{},
		folders:    map[string][]Folder{},
		messages:   map[string]map[UID]Message{},
		outbox:     map[string]PendingSend{},
		drafts:     map[string]Draft{},
		draftOrder: map[string]int{},
	}
}

func msgKey(accountID, folder string) string { return accountID + "/" + folder }

func (s *memStore) Accounts(ctx context.Context) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Account
	for _, a := range s.accounts {
		out = append(out, a)
	}
	return out, nil
}

func (s *memStore) Account(ctx context.Context, id string) (Account, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	return a, ok, nil
}

func (s *memStore) SaveAccount(ctx context.Context, a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[a.ID] = a
	return nil
}

func (s *memStore) DeleteAccount(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.accounts, id)
	delete(s.folders, id)
	for k := range s.messages {
		if len(k) > len(id) && k[:len(id)+1] == id+"/" {
			delete(s.messages, k)
		}
	}
	for k, p := range s.outbox {
		if p.AccountID == id {
			delete(s.outbox, k)
		}
	}
	for k, d := range s.drafts {
		if d.AccountID == id {
			delete(s.drafts, k)
		}
	}
	if s.active == id {
		s.active = ""
	}
	return nil
}

func (s *memStore) ActiveAccount(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, nil
}

func (s *memStore) SetActiveAccount(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = id
	return nil
}

func (s *memStore) Folders(ctx context.Context, accountID string) ([]Folder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Folder(nil), s.folders[accountID]...), nil
}

func (s *memStore) Folder(ctx context.Context, accountID, name string) (Folder, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.folders[accountID] {
		if f.Name == name {
			return f, true, nil
		}
	}
	return Folder{}, false, nil
}

func (s *memStore) SaveFolders(ctx context.Context, accountID string, folders []Folder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders[accountID] = append([]Folder(nil), folders...)
	return nil
}

func (s *memStore) ListMessages(ctx context.Context, accountID, folder, query string, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Summary
	for _, m := range s.messages[msgKey(accountID, folder)] {
		// A row says which folder it is in, as the SQLite store's do.
		summary := m.Summary
		summary.Folder = folder
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID > out[j].UID })
	return out, nil
}

func (s *memStore) GetMessage(ctx context.Context, accountID, folder string, uid UID) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[msgKey(accountID, folder)][uid]
	if !ok {
		return Message{}, false, nil
	}
	m.BodyCached = true
	return m, true, nil
}

func (s *memStore) SaveMessage(ctx context.Context, accountID, folder string, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := msgKey(accountID, folder)
	if s.messages[key] == nil {
		s.messages[key] = map[UID]Message{}
	}
	m.BodyCached = true
	if old, ok := s.messages[key][m.UID]; ok && m.Seen && !old.Seen {
		for i := range s.folders[accountID] {
			if s.folders[accountID][i].Name == folder {
				s.folders[accountID][i].Unread = max(0, s.folders[accountID][i].Unread-1)
			}
		}
	}
	s.messages[key][m.UID] = m
	return nil
}

func (s *memStore) ApplySync(ctx context.Context, accountID string, b SyncBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := msgKey(accountID, b.Folder.Name)
	if b.Reset {
		s.messages[key] = map[UID]Message{}
	}
	if s.messages[key] == nil {
		s.messages[key] = map[UID]Message{}
	}
	for _, m := range b.Messages {
		if old, ok := s.messages[key][m.UID]; ok {
			old.Summary = m.Summary
			s.messages[key][m.UID] = old
		} else {
			s.messages[key][m.UID] = m
		}
	}
	for _, f := range b.Flags {
		if m, ok := s.messages[key][f.UID]; ok {
			m.Seen = f.Seen
			m.Flagged = f.Flagged
			s.messages[key][f.UID] = m
		}
	}
	found := false
	for i := range s.folders[accountID] {
		if s.folders[accountID][i].Name == b.Folder.Name {
			// The cursor and counts change; what LIST said of the folder,
			// its special use above all, stays, as in the SQLite store.
			f := &s.folders[accountID][i]
			f.UIDValidity, f.LastUID = b.Folder.UIDValidity, b.Folder.LastUID
			f.Unread, f.Total, f.SyncedAt = b.Folder.Unread, b.Folder.Total, b.Folder.SyncedAt
			found = true
		}
	}
	if !found {
		s.folders[accountID] = append(s.folders[accountID], b.Folder)
	}
	return nil
}

func (s *memStore) SetFlag(ctx context.Context, accountID, folder string, uid UID, flag Flag, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := msgKey(accountID, folder)
	m, ok := s.messages[key][uid]
	if !ok {
		return ErrNotFound
	}
	switch flag {
	case FlagSeen:
		if m.Seen != on {
			for i := range s.folders[accountID] {
				if s.folders[accountID][i].Name == folder {
					if on {
						s.folders[accountID][i].Unread = max(0, s.folders[accountID][i].Unread-1)
					} else {
						s.folders[accountID][i].Unread++
					}
				}
			}
		}
		m.Seen = on
	case FlagFlagged:
		m.Flagged = on
	default:
		return fmt.Errorf("unsupported flag %q", flag)
	}
	s.messages[key][uid] = m
	return nil
}

func (s *memStore) MarkDeleted(ctx context.Context, accountID, folder string, uid UID, deleted bool) error {
	return nil
}

func (s *memStore) DeleteMessage(ctx context.Context, accountID, folder string, uid UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages[msgKey(accountID, folder)], uid)
	return nil
}

func (s *memStore) SetAttachmentCache(ctx context.Context, accountID, folder string, uid UID, part, hash, path string) error {
	return nil
}

func (s *memStore) EnqueueOutbox(ctx context.Context, p PendingSend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Raw = append([]byte(nil), p.Raw...)
	s.outbox[p.ID] = p
	return nil
}

func (s *memStore) Outbox(ctx context.Context, id string) (PendingSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.outbox[id]
	if !ok {
		return PendingSend{}, ErrNotFound
	}
	return p, nil
}

func (s *memStore) PendingSends(ctx context.Context, accountID string) ([]PendingSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingSend
	for _, p := range s.outbox {
		if p.AccountID != accountID {
			continue
		}
		switch p.State {
		case StatePending, StateFailed:
			out = append(out, p)
		case StateSending:
			p.State = StateUnknown
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *memStore) SetOutboxState(ctx context.Context, id, state, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.outbox[id]
	if !ok {
		return ErrNotFound
	}
	p.State = state
	p.LastError = lastError
	s.outbox[id] = p
	return nil
}

func (s *memStore) Drafts(ctx context.Context, accountID string) ([]Draft, error) {
	s.mu.Lock()
	var out []Draft
	for _, d := range s.drafts {
		if d.AccountID == accountID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return s.draftOrder[out[i].ID] > s.draftOrder[out[j].ID]
	})
	after := s.afterDrafts
	s.afterDrafts = nil
	s.mu.Unlock()
	if after != nil {
		after()
	}
	return out, nil
}

func (s *memStore) Draft(ctx context.Context, id string) (Draft, bool, error) {
	s.mu.Lock()
	d, ok := s.drafts[id]
	after := s.afterDraft
	s.afterDraft = nil
	s.mu.Unlock()
	if after != nil {
		after()
	}
	return d, ok, nil
}

func (s *memStore) SaveDraft(ctx context.Context, d Draft) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[d.AccountID]; !ok {
		return Draft{}, fmt.Errorf("draft %s: no account %q", d.ID, d.AccountID)
	}
	d.References = append([]string(nil), d.References...)
	old, ok := s.drafts[d.ID]
	if !ok {
		d.Revision = 1
		s.draftOrder[d.ID] = len(s.draftOrder) + 1
		s.drafts[d.ID] = d
		return d, nil
	}
	old.To, old.Cc, old.Bcc = d.To, d.Cc, d.Bcc
	old.Subject, old.Body = d.Subject, d.Body
	old.InReplyTo, old.References = d.InReplyTo, d.References
	old.UpdatedAt = d.UpdatedAt
	old.Revision++
	s.drafts[d.ID] = old
	return old, nil
}

func (s *memStore) SetDraftSync(ctx context.Context, d Draft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDraftSync != nil {
		return s.failDraftSync
	}
	old, ok := s.drafts[d.ID]
	if !ok {
		return ErrNotFound
	}
	old.MessageID = d.MessageID
	old.SyncedRevision = d.SyncedRevision
	old.ServerFolder, old.ServerUID, old.ServerUIDValidity = d.ServerFolder, d.ServerUID, d.ServerUIDValidity
	old.PendingDelete, old.Conflict = d.PendingDelete, d.Conflict
	s.drafts[d.ID] = old
	return nil
}

func (s *memStore) DeleteDraft(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts, id)
	return nil
}

// memSecrets is an in-memory keychain.
type memSecrets struct {
	mu   sync.Mutex
	data map[string][]byte
	fail bool
}

func newMemSecrets() *memSecrets { return &memSecrets{data: map[string][]byte{}} }

func (s *memSecrets) Set(key string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return fmt.Errorf("keychain unavailable")
	}
	s.data[key] = append([]byte(nil), secret...)
	return nil
}

func (s *memSecrets) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}

func (s *memSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

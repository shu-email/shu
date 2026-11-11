package mail

import (
	"context"
	"fmt"
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
}

func newMemStore() *memStore {
	return &memStore{
		accounts: map[string]Account{},
		folders:  map[string][]Folder{},
		messages: map[string]map[UID]Message{},
		outbox:   map[string]PendingSend{},
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
		out = append(out, m.Summary)
	}
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
			s.folders[accountID][i] = b.Folder
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

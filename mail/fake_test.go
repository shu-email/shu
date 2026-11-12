package mail

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
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
	// failDraftSync makes SetDraftSync fail, as a full disk would, and
	// failFileRefs the listing of the attachment files in use.
	failDraftSync error
	failFileRefs  error
	// kv is the settings, and marks the watermark of each folder watched:
	// the highest UID announced, under its UIDVALIDITY.
	kv    map[string]string
	marks map[string]memMark
	// afterDrafts runs once, right after the next Drafts call returned
	// its rows: the moment a sync has read the drafts and something else
	// changes them. afterDraft does the same for the next Draft call,
	// which is an upload reading the text it is about to send.
	afterDrafts func()
	afterDraft  func()
	// tombs is the messages a move hides until the server confirms it,
	// which the reads leave out as the SQLite store's do.
	tombs map[string]bool
}

// memMark is a folder's watermark.
type memMark struct {
	validity uint32
	uid      UID
}

func newMemStore() *memStore {
	return &memStore{
		kv:         map[string]string{},
		marks:      map[string]memMark{},
		accounts:   map[string]Account{},
		folders:    map[string][]Folder{},
		messages:   map[string]map[UID]Message{},
		outbox:     map[string]PendingSend{},
		drafts:     map[string]Draft{},
		draftOrder: map[string]int{},
		tombs:      map[string]bool{},
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
	for k := range s.marks {
		if len(k) > len(id) && k[:len(id)+1] == id+"/" {
			delete(s.marks, k)
		}
	}
	delete(s.kv, MutedKey(id))
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

func (s *memStore) Setting(ctx context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.kv[key]
	return value, ok, nil
}

func (s *memStore) SetSetting(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = value
	return nil
}

// TakeNew does what the SQLite store's does: the unread messages above
// the watermark, once, and none for a folder first seen or reset.
func (s *memStore) TakeNew(ctx context.Context, accountID, folder string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f *Folder
	for i := range s.folders[accountID] {
		if s.folders[accountID][i].Name == folder {
			f = &s.folders[accountID][i]
		}
	}
	if f == nil {
		return nil, ErrNotFound
	}
	if f.UIDValidity == 0 {
		return nil, nil
	}
	key := msgKey(accountID, folder)
	highest := f.LastUID
	for uid := range s.messages[key] {
		highest = max(highest, uid)
	}
	mark, ok := s.marks[key]
	if !ok || mark.validity != f.UIDValidity {
		s.marks[key] = memMark{validity: f.UIDValidity, uid: highest}
		return nil, nil
	}
	var out []Summary
	for uid, m := range s.messages[key] {
		if uid > mark.uid && !m.Seen {
			summary := m.Summary
			summary.Folder = folder
			out = append(out, summary)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID > out[j].UID })
	s.marks[key] = memMark{validity: f.UIDValidity, uid: max(highest, mark.uid)}
	return out, nil
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
		if s.tombs[tombKey(accountID, folder, m.UID)] {
			continue
		}
		// A row says which folder it is in, as the SQLite store's do.
		summary := m.Summary
		summary.Folder = folder
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID > out[j].UID })
	return out, nil
}

// threadsLocked puts an account's messages in conversations as the SQLite
// store does: those that share a key are one. It works them out on every
// read; a conversation's ID is the place of its first message by folder
// and UID, which is stable enough for a test.
func (s *memStore) threadsLocked(accountID string) map[MessageRef]ThreadID {
	var refs []MessageRef
	for key, folderMsgs := range s.messages {
		folder, ok := strings.CutPrefix(key, accountID+"/")
		if !ok {
			continue
		}
		for uid := range folderMsgs {
			refs = append(refs, MessageRef{folder, uid})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Folder != refs[j].Folder {
			return refs[i].Folder < refs[j].Folder
		}
		return refs[i].UID < refs[j].UID
	})
	parent := make([]int, len(refs))
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i, ref := range refs {
		parent[i] = i
		m := s.messages[msgKey(accountID, ref.Folder)][ref.UID]
		for _, key := range ThreadKeys(m.MessageID, m.InReplyTo, m.References, m.ThreadIndex) {
			j, ok := owner[key]
			if !ok {
				owner[key] = i
				continue
			}
			a, b := find(i), find(j)
			parent[max(a, b)] = min(a, b)
		}
	}
	out := make(map[MessageRef]ThreadID, len(refs))
	for i, ref := range refs {
		out[ref] = ThreadID(find(i) + 1)
	}
	return out
}

// inScope reports whether a message is in a conversation list's scope.
func inScope(folder string, ref MessageRef, m Message) bool {
	switch folder {
	case "":
		return true
	case "starred":
		return m.Flagged
	default:
		return ref.Folder == folder
	}
}

// newerSummary orders a conversation's messages, newest first.
func newerSummary(a, b Summary) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.After(b.Date)
	}
	if a.Folder != b.Folder {
		return a.Folder > b.Folder
	}
	return a.UID > b.UID
}

func (s *memStore) ListConversations(ctx context.Context, accountID, folder, query string, limit int) ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	rows := map[ThreadID]*Conversation{}
	hits := map[ThreadID][]Summary{}
	for ref, thread := range s.threadsLocked(accountID) {
		if s.tombs[tombKey(accountID, ref.Folder, ref.UID)] {
			continue
		}
		m := s.messages[msgKey(accountID, ref.Folder)][ref.UID]
		summary := m.Summary
		summary.Folder, summary.Thread = ref.Folder, thread
		for _, field := range []string{m.Subject, m.From.Name, m.From.Email, m.BodyText} {
			if q != "" && strings.Contains(strings.ToLower(field), q) {
				hits[thread] = append(hits[thread], summary)
				break
			}
		}
		if !inScope(folder, ref, m) {
			continue
		}
		c := rows[thread]
		if c == nil {
			c = &Conversation{Thread: thread, Latest: summary}
			rows[thread] = c
		}
		c.Count++
		c.Unread = c.Unread || !m.Seen
		c.Flagged = c.Flagged || m.Flagged
		c.HasAttachments = c.HasAttachments || m.HasAttachments
		if newerSummary(summary, c.Latest) {
			c.Latest = summary
		}
	}
	var out []Conversation
	for thread, c := range rows {
		if q != "" {
			if len(hits[thread]) == 0 {
				continue
			}
			sort.Slice(hits[thread], func(i, j int) bool { return newerSummary(hits[thread][i], hits[thread][j]) })
			for _, h := range hits[thread] {
				c.Matches = append(c.Matches, MessageRef{h.Folder, h.UID})
			}
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return newerSummary(out[i].Latest, out[j].Latest) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memStore) ConversationMessages(ctx context.Context, accountID, folder string, thread ThreadID) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Summary
	for ref, t := range s.threadsLocked(accountID) {
		m := s.messages[msgKey(accountID, ref.Folder)][ref.UID]
		if t != thread || !inScope(folder, ref, m) || s.tombs[tombKey(accountID, ref.Folder, ref.UID)] {
			continue
		}
		summary := m.Summary
		summary.Folder, summary.Thread = ref.Folder, t
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return newerSummary(out[j], out[i]) })
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
	written := map[UID]bool{}
	for _, m := range b.Messages {
		written[m.UID] = true
		if old, ok := s.messages[key][m.UID]; ok {
			refs, index := old.References, old.ThreadIndex
			old.Summary = m.Summary
			// A header the sync did not get keeps what the cache had.
			if len(old.References) == 0 {
				old.References = refs
			}
			if old.ThreadIndex == "" {
				old.ThreadIndex = index
			}
			s.messages[key][m.UID] = old
		} else {
			s.messages[key][m.UID] = m
		}
	}
	for _, h := range b.Threading {
		m, ok := s.messages[key][h.UID]
		if !ok || written[h.UID] {
			continue
		}
		if len(h.References) > 0 {
			m.References = h.References
		}
		if h.ThreadIndex != "" {
			m.ThreadIndex = h.ThreadIndex
		}
		s.messages[key][h.UID] = m
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
	s.mu.Lock()
	defer s.mu.Unlock()
	key := tombKey(accountID, folder, uid)
	if deleted {
		s.tombs[key] = true
	} else {
		delete(s.tombs, key)
	}
	return nil
}

func tombKey(accountID, folder string, uid UID) string {
	return fmt.Sprintf("%s/%d", msgKey(accountID, folder), uid)
}

func (s *memStore) DeleteMessage(ctx context.Context, accountID, folder string, uid UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages[msgKey(accountID, folder)], uid)
	delete(s.tombs, tombKey(accountID, folder, uid))
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
		case StatePending, StateFailed, StateUnknown:
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
		d.Attachments = append([]Attachment(nil), d.Attachments...)
		s.draftOrder[d.ID] = len(s.draftOrder) + 1
		s.drafts[d.ID] = d
		return d, nil
	}
	// The attachments of a stored draft are not a save's to change.
	old.To, old.Cc, old.Bcc = d.To, d.Cc, d.Bcc
	old.Subject, old.Body = d.Subject, d.Body
	old.InReplyTo, old.References = d.InReplyTo, d.References
	old.UpdatedAt = d.UpdatedAt
	old.Revision++
	s.drafts[d.ID] = old
	return old, nil
}

func (s *memStore) SetDraftAttachments(ctx context.Context, id string, list []Attachment, updatedAt time.Time) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.drafts[id]
	if !ok {
		return Draft{}, ErrNotFound
	}
	old.Attachments = append([]Attachment(nil), list...)
	old.UpdatedAt = updatedAt
	old.Revision++
	s.drafts[id] = old
	return old, nil
}

func (s *memStore) DraftFileRefs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFileRefs != nil {
		return nil, s.failFileRefs
	}
	var out []string
	for _, d := range s.drafts {
		if d.PendingDelete {
			continue
		}
		for _, a := range d.Attachments {
			out = append(out, a.ContentHash)
		}
	}
	for _, p := range s.outbox {
		if p.State == StateSent || p.State == StateCancelled {
			continue
		}
		for _, a := range p.Envelope.Attachments {
			out = append(out, a.ContentHash)
		}
	}
	return out, nil
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

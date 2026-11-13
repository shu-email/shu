package mail

import (
	"context"
	"fmt"
	"slices"
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
	// identities is the identities added, in the order they were.
	identities []Identity
	// draftOrder is when each draft was created, which breaks a tie
	// between two edited in the same second as SQLite's rowid does.
	draftOrder map[string]int
	// failDraftSync makes SetDraftSync fail, as a full disk would, and
	// failFileRefs the listing of the attachment files in use.
	failDraftSync error
	failFileRefs  error
	// attachmentFiles are the paths AttachmentFileRefs reports, and
	// cacheVersion the version PrepareCache last saw.
	attachmentFiles []string
	cacheVersion    int
	// kv is the settings, and marks the watermark of each folder watched:
	// the highest UID announced, under its UIDVALIDITY.
	kv    map[string]string
	marks map[string]memMark
	// keepsSubscriptions makes SaveFolders keep what the cache knew about
	// a folder's subscription, as a real LIST does when the user changed
	// it here but the fake server's list says nothing.
	keepsSubscriptions bool
	// afterDrafts runs once, right after the next Drafts call returned
	// its rows: the moment a sync has read the drafts and something else
	// changes them. afterDraft does the same for the next Draft call,
	// which is an upload reading the text it is about to send.
	afterDrafts func()
	afterDraft  func()
	// tombs is the messages a move hides until the server confirms it,
	// which the reads leave out as the SQLite store's do.
	tombs map[string]bool
	// olderWrites counts the batches of older mail written. afterUIDs
	// runs once, right after the next FolderUIDs call: the moment a fetch
	// of older mail has read what the cache holds, before it writes.
	olderWrites int
	afterUIDs   func()
	// contacts is the address book by contact key, and counted the keys
	// of the messages counted in it, kept by the rules the SQLite store
	// keeps them by.
	contacts map[string]*ContactState
	counted  map[string]bool
	// labels are the labels by ID, the last given labelsNext. msgLabels
	// is the labels each message has as the server keeps them, by
	// tombKey; keptLabels those kept on this device, by labelKeyOf;
	// labelsGone when each label deleted went, by its account and keyword
	// in lower case (see fake_labels_test.go). beforeApply runs once, at
	// the start of the next ApplySync: the moment a sync has read the
	// server and not yet written what it read.
	labels      map[LabelID]*memLabel
	labelsNext  LabelID
	msgLabels   map[string]map[LabelID]bool
	keptLabels  map[string]map[LabelID]bool
	labelsGone  map[string]time.Time
	beforeApply func()
	// rules are the rules in their order, the last ID given rulesNext;
	// ruleLog is the rules' log, oldest first, the last ID given logNext;
	// expected the notes of messages moving into a folder, oldest first
	// (see fake_rules_test.go).
	rules     []Rule
	rulesNext RuleID
	ruleLog   []RuleActivity
	logNext   ActivityID
	expected  []memExpected
	// reminders is an account's reminders, by conversation; reminderNext
	// gives the IDs.
	reminders    map[string]map[ThreadID]Reminder
	reminderNext int64
	// dismissed is when each account took a conversation off its
	// follow-up list.
	dismissed map[string]map[ThreadID]time.Time
	// smarts are the smart mailboxes by account, in their order, and
	// smartNext gives the IDs.
	smarts    map[string][]SmartMailbox
	smartNext int64
	// discoveries are the servers kept for each domain.
	discoveries map[string]Discovery
	// pending are the actions queued for the server (see queue.go), the
	// last ID given pendingNext. blocks are the senders an account files
	// away, and unsubs the lists it left, by account and sender.
	pending     []PendingOperation
	pendingNext int64
	blocks      map[string][]BlockedSender
	unsubs      map[string]UnsubscribeRecord
}

// memMark is a folder's watermark.
type memMark struct {
	validity uint32
	uid      UID
}

func newMemStore() *memStore {
	return &memStore{
		kv:          map[string]string{},
		marks:       map[string]memMark{},
		accounts:    map[string]Account{},
		folders:     map[string][]Folder{},
		messages:    map[string]map[UID]Message{},
		outbox:      map[string]PendingSend{},
		drafts:      map[string]Draft{},
		draftOrder:  map[string]int{},
		tombs:       map[string]bool{},
		contacts:    map[string]*ContactState{},
		counted:     map[string]bool{},
		labels:      map[LabelID]*memLabel{},
		msgLabels:   map[string]map[LabelID]bool{},
		keptLabels:  map[string]map[LabelID]bool{},
		labelsGone:  map[string]time.Time{},
		reminders:   map[string]map[ThreadID]Reminder{},
		dismissed:   map[string]map[ThreadID]time.Time{},
		smarts:      map[string][]SmartMailbox{},
		discoveries: map[string]Discovery{},
		blocks:      map[string][]BlockedSender{},
		unsubs:      map[string]UnsubscribeRecord{},
	}
}

// Discovery reads the servers kept for a domain.
func (s *memStore) Discovery(ctx context.Context, domain string) (Discovery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.discoveries[domain]
	return d, ok, nil
}

// SaveDiscovery keeps a domain's servers, replacing what it had.
func (s *memStore) SaveDiscovery(ctx context.Context, d Discovery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries[d.Domain] = d
	return nil
}

// EnqueueOperations writes queued actions, each with its ID.
func (s *memStore) EnqueueOperations(ctx context.Context, ops []PendingOperation) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(ops))
	for _, op := range ops {
		s.pendingNext++
		op.ID = s.pendingNext
		if op.State == "" {
			op.State = OperationPending
		}
		if op.CreatedAt.IsZero() {
			op.CreatedAt = time.Now()
		}
		s.pending = append(s.pending, op)
		ids = append(ids, op.ID)
	}
	return ids, nil
}

// ClaimOperation takes the oldest action of an account that is due.
func (s *memStore) ClaimOperation(ctx context.Context, accountID string, now time.Time) (PendingOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, op := range s.pending {
		if op.AccountID != accountID || op.State != OperationPending || op.NextTry.After(now) {
			continue
		}
		s.pending[i].State = OperationClaimed
		s.pending[i].ClaimedAt = now
		return s.pending[i], true, nil
	}
	return PendingOperation{}, false, nil
}

// ClaimOperationsByKey claims what one command can make with op.
func (s *memStore) ClaimOperationsByKey(ctx context.Context, op PendingOperation, now time.Time) ([]PendingOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingOperation
	for i, got := range s.pending {
		if got.AccountID != op.AccountID || got.State != OperationPending ||
			got.Kind != op.Kind || got.Folder != op.Folder || got.Dest != op.Dest {
			continue
		}
		if got.Flag != op.Flag || got.On != op.On || got.Label != op.Label || got.Validity != op.Validity {
			continue
		}
		s.pending[i].State = OperationClaimed
		s.pending[i].ClaimedAt = now
		out = append(out, s.pending[i])
	}
	return out, nil
}

// RetryOperation puts an action back for a later try.
func (s *memStore) RetryOperation(ctx context.Context, id int64, nextTry time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, op := range s.pending {
		if op.ID != id {
			continue
		}
		s.pending[i].State = OperationPending
		s.pending[i].ClaimedAt = time.Time{}
		s.pending[i].Attempts++
		s.pending[i].NextTry = nextTry
		s.pending[i].LastError = reason
	}
	return nil
}

// FailOperation keeps an action the server refused.
func (s *memStore) FailOperation(ctx context.Context, id int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, op := range s.pending {
		if op.ID != id {
			continue
		}
		s.pending[i].State = OperationFailed
		s.pending[i].ClaimedAt = time.Time{}
		s.pending[i].Attempts++
		s.pending[i].LastError = reason
	}
	return nil
}

// Operations reads an account's actions, oldest first.
func (s *memStore) Operations(ctx context.Context, accountID string) ([]PendingOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingOperation
	for _, op := range s.pending {
		if op.AccountID == accountID {
			out = append(out, op)
		}
	}
	return out, nil
}

// ReclaimOperations gives back what a run that ended left claimed before
// a time.
func (s *memStore) ReclaimOperations(ctx context.Context, accountID string, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, op := range s.pending {
		if op.AccountID == accountID && op.State == OperationClaimed && !op.ClaimedAt.After(before) {
			s.pending[i].State = OperationPending
			s.pending[i].ClaimedAt = time.Time{}
		}
	}
	return nil
}

// CancelOperations drops actions before the server makes them, and
// returns those it dropped.
func (s *memStore) CancelOperations(ctx context.Context, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []int64
	for _, id := range ids {
		for _, op := range s.pending {
			if op.ID == id && op.State == OperationPending {
				s.dropOperation(id)
				gone = append(gone, id)
				break
			}
		}
	}
	return gone, nil
}

// RetryFailedOperations puts the actions the server refused back in the
// queue.
func (s *memStore) RetryFailedOperations(ctx context.Context, accountID string, nextTry time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i, op := range s.pending {
		if op.AccountID == accountID && op.State == OperationFailed {
			s.pending[i].State = OperationPending
			s.pending[i].NextTry = nextTry
			s.pending[i].ClaimedAt = time.Time{}
			n++
		}
	}
	return n, nil
}

// FinishOperations drops the actions the server made.
func (s *memStore) FinishOperations(ctx context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.dropOperation(id)
	}
	return nil
}

// dropOperation removes one action. mu is held.
func (s *memStore) dropOperation(id int64) {
	for i, op := range s.pending {
		if op.ID == id {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
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
	for _, f := range s.folders[id] {
		s.dropLabelsLocked(id, f.Name)
	}
	delete(s.folders, id)
	for k := range s.messages {
		if len(k) > len(id) && k[:len(id)+1] == id+"/" {
			delete(s.messages, k)
		}
	}
	for lid, l := range s.labels {
		if l.Account == id {
			delete(s.labels, lid)
		}
	}
	for k := range s.keptLabels {
		if strings.HasPrefix(k, id+"\x00") {
			delete(s.keptLabels, k)
		}
	}
	for k := range s.marks {
		if len(k) > len(id) && k[:len(id)+1] == id+"/" {
			delete(s.marks, k)
		}
	}
	delete(s.kv, MutedKey(id))
	delete(s.kv, SignatureKey(id))
	s.identities = slices.DeleteFunc(s.identities, func(i Identity) bool { return i.AccountID == id })
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
	s.rules = slices.DeleteFunc(s.rules, func(r Rule) bool { return r.Account() == id })
	s.ruleLog = slices.DeleteFunc(s.ruleLog, func(e RuleActivity) bool { return e.Account == id })
	s.expected = slices.DeleteFunc(s.expected, func(x memExpected) bool { return x.account == id })
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

func (s *memStore) Identities(ctx context.Context, accountID string) ([]Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Identity
	for _, id := range s.identities {
		if id.AccountID == accountID {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *memStore) SaveIdentity(ctx context.Context, id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[id.AccountID]; !ok {
		return fmt.Errorf("identity %s: no account %q", id.ID, id.AccountID)
	}
	for i, have := range s.identities {
		if have.ID == id.ID {
			s.identities[i].Name, s.identities[i].Email = id.Name, id.Email
			return nil
		}
	}
	s.identities = append(s.identities, id)
	return nil
}

// DeleteIdentity has the drafts written from the identity go from their
// account's own address, an edit, as the SQLite store does.
func (s *memStore) DeleteIdentity(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, d := range s.drafts {
		if d.Identity == id {
			d.Identity = ""
			d.Revision++
			s.drafts[key] = d
		}
	}
	s.identities = slices.DeleteFunc(s.identities, func(i Identity) bool { return i.ID == id })
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
			summary.Account, summary.Folder = accountID, folder
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

// SaveFolders keeps the sync cursor of the folders it knows, and what
// their PERMANENTFLAGS said, as the SQLite store does: a LIST says
// nothing of them.
func (s *memStore) SaveFolders(ctx context.Context, accountID string, folders []Folder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := append([]Folder(nil), folders...)
	for i := range saved {
		for _, old := range s.folders[accountID] {
			if old.Name == saved[i].Name {
				saved[i].UIDValidity, saved[i].LastUID = old.UIDValidity, old.LastUID
				saved[i].SyncedAt, saved[i].Complete = old.SyncedAt, old.Complete
				saved[i].PermanentFlags = old.PermanentFlags
				// A list the fake server answers keeps subscriptions as
				// they were when it says nothing of them.
				if s.keepsSubscriptions {
					saved[i].Subscribed = old.Subscribed
				}
			}
		}
	}
	s.folders[accountID] = saved
	return nil
}

// RenameFolder moves a folder and the folders below it to their new
// names, keeping their messages and cursors, as the SQLite store does.
func (s *memStore) RenameFolder(ctx context.Context, accountID, from, to, delimiter string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if delimiter == "" {
		delimiter = "/"
	}
	found := false
	for i := range s.folders[accountID] {
		name := s.folders[accountID][i].Name
		if name == from || strings.HasPrefix(name, from+delimiter) {
			next := to + strings.TrimPrefix(name, from)
			if msgs, ok := s.messages[msgKey(accountID, name)]; ok {
				delete(s.messages, msgKey(accountID, name))
				s.messages[msgKey(accountID, next)] = msgs
			}
			s.folders[accountID][i].Name = next
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (s *memStore) DeleteFolder(ctx context.Context, accountID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.folders[accountID][:0]
	found := false
	for _, f := range s.folders[accountID] {
		if f.Name == name {
			found = true
			continue
		}
		kept = append(kept, f)
	}
	if !found {
		return ErrNotFound
	}
	s.folders[accountID] = kept
	delete(s.messages, msgKey(accountID, name))
	return nil
}

func (s *memStore) SetFolderSubscribed(ctx context.Context, accountID, name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.folders[accountID] {
		if s.folders[accountID][i].Name == name {
			s.folders[accountID][i].Subscribed = on
			s.keepsSubscriptions = true
			return nil
		}
	}
	return ErrNotFound
}

func (s *memStore) ListMessages(ctx context.Context, accountID, folder, query string, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Summary
	for _, m := range s.messages[msgKey(accountID, folder)] {
		if s.tombs[tombKey(accountID, folder, m.UID)] {
			continue
		}
		// A row says which account and folder it is in, as the SQLite
		// store's do.
		summary := s.labelledLocked(m.Summary, accountID, s.folderLocked(accountID, folder), m)
		summary.Account, summary.Folder = accountID, folder
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
	labels := map[ThreadID]map[LabelID]bool{}
	for ref, thread := range s.threadsLocked(accountID) {
		if s.tombs[tombKey(accountID, ref.Folder, ref.UID)] {
			continue
		}
		m := s.messages[msgKey(accountID, ref.Folder)][ref.UID]
		summary := s.labelledLocked(m.Summary, accountID, s.folderLocked(accountID, ref.Folder), m)
		summary.Account, summary.Folder, summary.Thread = accountID, ref.Folder, thread
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
			c = &Conversation{Thread: thread, Account: accountID, Latest: summary}
			rows[thread] = c
			labels[thread] = map[LabelID]bool{}
		}
		c.Count++
		c.Unread = c.Unread || !m.Seen
		c.Flagged = c.Flagged || m.Flagged
		c.HasAttachments = c.HasAttachments || m.HasAttachments
		for _, l := range summary.Labels {
			labels[thread][l.ID] = true
		}
		if newerSummary(summary, c.Latest) {
			c.Latest = summary
		}
	}
	var out []Conversation
	for thread, c := range rows {
		c.Labels = s.orderedLabelsLocked(accountID, labels[thread])
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
		summary := s.labelledLocked(m.Summary, accountID, s.folderLocked(accountID, ref.Folder), m)
		summary.Account, summary.Folder, summary.Thread = accountID, ref.Folder, t
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return newerSummary(out[j], out[i]) })
	return out, nil
}

// memRow is a message a page read found, with what orders it.
type memRow struct {
	Summary
	key string
}

// memRowKey orders rows as the SQLite store does, newest first, with the
// account, folder and UID in place of the row number.
func memRowKey(m Summary) string {
	return fmt.Sprintf("%020d/%s/%s/%010d", m.Date.Unix()+1<<40, m.Account, m.Folder, m.UID)
}

// memFound reports whether a message matches a search whole, and the
// parts its terms are in. named reports whether it has its account's
// label of a name.
func memFound(q Search, f Folder, m Message, named func(string) bool) (Field, bool) {
	parts := map[Field][]string{
		FieldSubject: {m.Subject}, FieldFrom: {m.From.Name, m.From.Email},
		FieldBody: {m.BodyText, m.Preview},
	}
	for _, a := range append(append([]Address(nil), m.To...), m.Cc...) {
		parts[FieldTo] = append(parts[FieldTo], a.Name, a.Email)
	}
	var found Field
	for _, c := range []struct {
		terms  []string
		fields Field
	}{{q.Text, FieldAny}, {q.From, FieldFrom}, {q.To, FieldTo}, {q.Subject, FieldSubject}, {q.Body, FieldBody}} {
		for _, term := range c.terms {
			var in Field
			for _, field := range []Field{FieldSubject, FieldFrom, FieldTo, FieldBody} {
				for _, text := range parts[field] {
					if c.fields.Has(field) && strings.Contains(strings.ToLower(text), strings.ToLower(term)) {
						in |= field
					}
				}
			}
			if in == 0 {
				return 0, false
			}
			found |= in
		}
	}
	if (!q.Since.IsZero() && m.Date.Before(q.Since)) || (!q.Before.IsZero() && !m.Date.Before(q.Before)) ||
		(q.Unread && m.Seen) || (q.Starred && !m.Flagged) || (q.Attachment && !m.HasAttachments) ||
		(q.In != "" && !f.NamedBy(q.In)) {
		return 0, false
	}
	for _, name := range q.Labels {
		if !named(name) {
			return 0, false
		}
		found |= FieldLabel
	}
	return found, true
}

// pageLocked lists every message of the accounts of a scope, those in it
// and the others, as rows with their conversation and labels.
func (s *memStore) pageLocked(sc Scope, each func(f Folder, m Message, row memRow, inScope bool)) {
	accounts := sc.Accounts
	if len(accounts) == 0 {
		for id := range s.accounts {
			accounts = append(accounts, id)
		}
	}
	if sc.Label != 0 {
		l := s.labels[sc.Label]
		if l == nil || !slices.Contains(accounts, l.Account) {
			return
		}
		accounts = []string{l.Account}
	}
	for _, accountID := range accounts {
		threads := s.threadsLocked(accountID)
		for _, f := range s.folders[accountID] {
			for uid, m := range s.messages[msgKey(accountID, f.Name)] {
				if s.tombs[tombKey(accountID, f.Name, uid)] {
					continue
				}
				thread := threads[MessageRef{f.Name, uid}]
				inScope := (sc.Folder == "" || f.Name == sc.Folder) &&
					(sc.Special == "" || f.SpecialUse == sc.Special) && (!sc.Starred || m.Flagged) &&
					(sc.Label == 0 || f.InLabelScope() && s.hasLabelLocked(accountID, f.Name, m, sc.Label)) &&
					(!sc.Reminded || s.remindedLocked(accountID, thread))
				summary := s.labelledLocked(m.Summary, accountID, f, m)
				summary.Account, summary.Folder, summary.UID = accountID, f.Name, uid
				summary.Thread = thread
				each(f, m, memRow{summary, memRowKey(summary)}, inScope)
			}
		}
	}
}

// namedLocked is memFound's named of a row.
func (s *memStore) namedLocked(row memRow, m Message) func(string) bool {
	return func(name string) bool { return s.namedLabelLocked(row.Account, row.Folder, m, name) }
}

// memPage cuts the rows after a cursor, newest first, to a page.
func memPage(rows []memRow, after Cursor, limit int) (page []memRow, next Cursor, more bool) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].key > rows[j].key })
	if limit <= 0 {
		limit = DefaultPageSize
	}
	next = after
	for _, r := range rows {
		if after != "" && r.key >= string(after) {
			continue
		}
		if len(page) == limit {
			return page, next, true
		}
		page = append(page, r)
		next = Cursor(r.key)
	}
	return page, next, false
}

func (s *memStore) MessagePage(ctx context.Context, q ListQuery) (MessagePage, error) {
	if err := q.Scope.Check(); err != nil {
		return MessagePage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []memRow
	s.pageLocked(q.Scope, func(f Folder, m Message, row memRow, inScope bool) {
		if fields, ok := memFound(q.Search, f, m, s.namedLocked(row, m)); ok && inScope {
			row.Matched = fields
			rows = append(rows, row)
		}
	})
	page, next, more := memPage(rows, q.After, q.Limit)
	out := MessagePage{Messages: []Summary{}, Next: next, More: more}
	for _, r := range page {
		out.Messages = append(out.Messages, r.Summary)
	}
	return out, nil
}

func (s *memStore) ConversationPage(ctx context.Context, q ListQuery) (ConversationPage, error) {
	if err := q.Scope.Check(); err != nil {
		return ConversationPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type thread struct {
		account string
		id      ThreadID
	}
	heads := map[thread]*memRow{}
	rows := map[thread]*Conversation{}
	found := map[thread][]memRow{}
	labels := map[thread]map[LabelID]bool{}
	s.pageLocked(q.Scope, func(f Folder, m Message, row memRow, inScope bool) {
		t := thread{row.Account, row.Thread}
		if fields, ok := memFound(q.Search, f, m, s.namedLocked(row, m)); ok && !q.Search.IsZero() {
			row.Matched = fields
			found[t] = append(found[t], row)
		}
		if !inScope {
			return
		}
		c := rows[t]
		if c == nil {
			c = &Conversation{Thread: row.Thread, Account: row.Account}
			rows[t] = c
			labels[t] = map[LabelID]bool{}
		}
		c.Count++
		c.Unread = c.Unread || !m.Seen
		c.Flagged = c.Flagged || m.Flagged
		c.HasAttachments = c.HasAttachments || m.HasAttachments
		for _, l := range row.Labels {
			labels[t][l.ID] = true
		}
		if heads[t] == nil || row.key > heads[t].key {
			heads[t] = &row
		}
	})
	var list []memRow
	for t, head := range heads {
		if q.Search.IsZero() || len(found[t]) > 0 {
			list = append(list, *head)
		}
	}
	page, next, more := memPage(list, q.After, q.Limit)
	out := ConversationPage{Conversations: []Conversation{}, Next: next, More: more}
	for _, head := range page {
		t := thread{head.Account, head.Thread}
		c := *rows[t]
		c.Latest = head.Summary
		c.Labels = s.orderedLabelsLocked(head.Account, labels[t])
		hits := found[t]
		sort.Slice(hits, func(i, j int) bool { return hits[i].key > hits[j].key })
		for _, h := range hits {
			ref := MessageRef{h.Folder, h.UID}
			c.Matches = append(c.Matches, ref)
			c.Found = append(c.Found, Match{ref, h.Matched})
			if ref == (MessageRef{c.Latest.Folder, c.Latest.UID}) {
				c.Latest.Matched = h.Matched
			}
		}
		out.Conversations = append(out.Conversations, c)
	}
	return out, nil
}

func (s *memStore) FolderCache(ctx context.Context, accountID, folder string) (FolderCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.folders[accountID] {
		if f.Name != folder {
			continue
		}
		out := FolderCache{Total: f.Total, Complete: f.Complete}
		for uid := range s.messages[msgKey(accountID, folder)] {
			if !s.tombs[tombKey(accountID, folder, uid)] {
				out.Cached++
			}
		}
		return out, nil
	}
	return FolderCache{}, ErrNotFound
}

func (s *memStore) FolderUIDs(ctx context.Context, accountID, folder string) ([]UID, error) {
	s.mu.Lock()
	var out []UID
	for uid := range s.messages[msgKey(accountID, folder)] {
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	after := s.afterUIDs
	s.afterUIDs = nil
	s.mu.Unlock()
	if after != nil {
		after()
	}
	return out, nil
}

// ApplyOlder writes the messages and the folder's Complete, and nothing
// else of the folder, as the SQLite store does.
func (s *memStore) ApplyOlder(ctx context.Context, accountID string, b OlderBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.folders[accountID] {
		f := &s.folders[accountID][i]
		if f.Name != b.Folder {
			continue
		}
		if f.UIDValidity != b.UIDValidity {
			return ErrFolderReset
		}
		key := msgKey(accountID, b.Folder)
		if s.messages[key] == nil {
			s.messages[key] = map[UID]Message{}
		}
		for _, m := range b.Messages {
			s.messages[key][m.UID] = m
		}
		s.keywordsLocked(accountID, b.Folder, b.Messages, nil, time.Time{})
		f.Complete = b.Complete
		s.olderWrites++
		s.countFolderLocked(accountID, b.Folder, b.Messages)
		return nil
	}
	return ErrNotFound
}

func (s *memStore) GetMessage(ctx context.Context, accountID, folder string, uid UID) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[msgKey(accountID, folder)][uid]
	if !ok {
		return Message{}, false, nil
	}
	m.Summary = s.labelledLocked(m.Summary, accountID, s.folderLocked(accountID, folder), m)
	m.Account = accountID
	return m, true, nil
}

func (s *memStore) BlockedSenders(ctx context.Context, accountID string) ([]BlockedSender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BlockedSender(nil), s.blocks[accountID]...), nil
}

func (s *memStore) SaveBlockedSender(ctx context.Context, b BlockedSender) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.blocks[b.AccountID]
	for i := range list {
		if list[i].Value == b.Value && list[i].Domain == b.Domain {
			list[i] = b
			return nil
		}
	}
	s.blocks[b.AccountID] = append(list, b)
	return nil
}

func (s *memStore) RemoveBlockedSender(ctx context.Context, accountID, value string, domain bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.blocks[accountID]
	out := list[:0]
	for _, b := range list {
		if b.Value != value || b.Domain != domain {
			out = append(out, b)
		}
	}
	s.blocks[accountID] = out
	return nil
}

func (s *memStore) UnsubscribeRecord(ctx context.Context, accountID, email string) (UnsubscribeRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.unsubs[accountID+"\x00"+email]
	return r, ok, nil
}

func (s *memStore) Unsubscribes(ctx context.Context, accountID string) ([]UnsubscribeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UnsubscribeRecord
	for _, r := range s.unsubs {
		if r.AccountID == accountID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *memStore) SaveUnsubscribe(ctx context.Context, r UnsubscribeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsubs[r.AccountID+"\x00"+r.Email] = r
	return nil
}

func (s *memStore) SaveMessage(ctx context.Context, accountID, folder string, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := msgKey(accountID, folder)
	if s.messages[key] == nil {
		s.messages[key] = map[UID]Message{}
	}
	m.BodyCached = true
	old, ok := s.messages[key][m.UID]
	if ok && m.Seen && !old.Seen {
		for i := range s.folders[accountID] {
			if s.folders[accountID][i].Name == folder {
				s.folders[accountID][i].Unread = max(0, s.folders[accountID][i].Unread-1)
			}
		}
	}
	s.messages[key][m.UID] = m
	if !ok {
		s.keywordsLocked(accountID, folder, []Message{m}, nil, time.Time{})
		s.countFolderLocked(accountID, folder, []Message{m})
	}
	return nil
}

func (s *memStore) ApplySync(ctx context.Context, accountID string, b SyncBatch) error {
	s.mu.Lock()
	before := s.beforeApply
	s.beforeApply = nil
	s.mu.Unlock()
	if before != nil {
		before()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := msgKey(accountID, b.Folder.Name)
	if b.Reset {
		s.dropLabelsLocked(accountID, b.Folder.Name)
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
			if old.RawHeaders == "" {
				old.RawHeaders = m.RawHeaders
			}
			if old.Security == nil {
				old.Security = m.Security
			}
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
	s.keywordsLocked(accountID, b.Folder.Name, b.Messages, b.Flags, b.Folder.SyncedAt)
	found := false
	for i := range s.folders[accountID] {
		if s.folders[accountID][i].Name == b.Folder.Name {
			// The cursor and counts change; what LIST said of the folder,
			// its special use above all, stays, as in the SQLite store.
			f := &s.folders[accountID][i]
			f.UIDValidity, f.LastUID = b.Folder.UIDValidity, b.Folder.LastUID
			f.Unread, f.Total, f.SyncedAt = b.Folder.Unread, b.Folder.Total, b.Folder.SyncedAt
			f.Complete = b.Folder.Complete
			if b.Folder.PermanentFlags != nil {
				f.PermanentFlags = b.Folder.PermanentFlags
			}
			found = true
		}
	}
	if !found {
		s.folders[accountID] = append(s.folders[accountID], b.Folder)
	}
	s.countFolderLocked(accountID, b.Folder.Name, b.Messages)
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
	delete(s.msgLabels, tombKey(accountID, folder, uid))
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
	if state == StateSent && p.State != StateSent {
		at := p.CreatedAt
		if p.DueAt.After(at) {
			at = p.DueAt
		}
		s.countLocked(ContactMessageKey(p.Envelope.MessageID, p.Envelope.From.Email, at, p.Envelope.Subject),
			SentContacts(p.Envelope, at, s.ownLocked()))
	}
	p.State = state
	p.LastError = lastError
	s.outbox[id] = p
	return nil
}

func (s *memStore) ClaimOutbox(ctx context.Context, id, from, to string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.outbox[id]
	if !ok || p.State != from {
		return false, nil
	}
	p.State = to
	s.outbox[id] = p
	return true, nil
}

func (s *memStore) ScheduledSends(ctx context.Context) ([]PendingSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingSend
	for _, p := range s.outbox {
		if p.State == StateScheduled {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DueAt.Before(out[j].DueAt) })
	return out, nil
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
	old.Subject, old.Body, old.Identity = d.Subject, d.Body, d.Identity
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

// ownLocked reports whether an address is one of the user's, as the
// SQLite store reads them: the accounts' and their identities'.
func (s *memStore) ownLocked() func(string) bool {
	own := map[string]bool{}
	for _, a := range s.accounts {
		own[ContactKey(a.Email)] = true
	}
	for _, id := range s.identities {
		own[ContactKey(id.Email)] = true
	}
	return func(email string) bool { return own[ContactKey(email)] }
}

// countLocked counts a message in the address book, once ever.
func (s *memStore) countLocked(key string, events []ContactEvent) {
	if len(events) == 0 || s.counted[key] {
		return
	}
	s.counted[key] = true
	for _, e := range events {
		c := s.contacts[ContactKey(e.Email)]
		if c == nil {
			c = &ContactState{}
		}
		if c.Apply(e) {
			s.contacts[ContactKey(e.Email)] = c
		}
	}
}

// countFolderLocked counts the messages written into a folder, as the
// SQLite store does: none of the Junk and Drafts folders.
func (s *memStore) countFolderLocked(accountID, folder string, msgs []Message) {
	for _, f := range s.folders[accountID] {
		if f.Name == folder && (f.SpecialUse == SpecialJunk || f.SpecialUse == SpecialDrafts) {
			return
		}
	}
	own := s.ownLocked()
	for _, m := range msgs {
		s.countLocked(ContactMessageKey(m.MessageID, m.From.Email, m.Date, m.Subject), MessageContacts(m, own, time.Now()))
	}
}

// memRanksBefore orders the address book as the SQLite store does.
func memRanksBefore(ka string, a *ContactState, kb string, b *ContactState) bool {
	tier := func(c *ContactState) bool { return c.Sent > 0 || c.Added }
	score := func(c *ContactState) float64 {
		if tier(c) {
			return c.SentScore
		}
		return c.ReceivedScore
	}
	switch {
	case a.Favorite != b.Favorite:
		return a.Favorite
	case tier(a) != tier(b):
		return tier(a)
	case score(a) != score(b):
		return score(a) > score(b)
	case a.ReceivedScore != b.ReceivedScore:
		return a.ReceivedScore > b.ReceivedScore
	}
	return ka < kb
}

func (s *memStore) Contacts(ctx context.Context, q ContactQuery) ([]Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	own := s.ownLocked()
	excluded := map[string]bool{}
	for _, email := range q.Exclude {
		excluded[ContactKey(email)] = true
	}
	var keys []string
	for key, c := range s.contacts {
		if c.Deleted || own(c.Email) || excluded[key] {
			continue
		}
		terms := ContactTerms(c.Name, c.Email)
		found := true
		for _, w := range q.Words {
			found = found && slices.ContainsFunc(terms, func(t string) bool { return strings.HasPrefix(t, w) })
		}
		if found {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return memRanksBefore(keys[i], s.contacts[keys[i]], keys[j], s.contacts[keys[j]])
	})
	keys = keys[min(max(q.Offset, 0), len(keys)):]
	if q.Limit > 0 && len(keys) > q.Limit {
		keys = keys[:q.Limit]
	}
	out := []Contact{}
	for _, key := range keys {
		out = append(out, s.contacts[key].Contact())
	}
	return out, nil
}

func (s *memStore) AddContact(ctx context.Context, name, email string) (Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownLocked()(email) {
		return Contact{}, ErrContactOwn
	}
	c := s.contacts[ContactKey(email)]
	if c != nil && !c.Deleted {
		return Contact{}, ErrContactExists
	}
	if c == nil {
		c = &ContactState{}
		s.contacts[ContactKey(email)] = c
	}
	c.Add(name, email)
	return c.Contact(), nil
}

// editContact changes a contact of the book: one there and not deleted.
func (s *memStore) editContact(email string, edit func(*ContactState)) (Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.contacts[ContactKey(email)]
	if c == nil || c.Deleted {
		return Contact{}, ErrNotFound
	}
	edit(c)
	return c.Contact(), nil
}

func (s *memStore) RenameContact(ctx context.Context, email, name string) (Contact, error) {
	return s.editContact(email, func(c *ContactState) { c.Rename(name) })
}

func (s *memStore) FavoriteContact(ctx context.Context, email string, on bool) (Contact, error) {
	return s.editContact(email, func(c *ContactState) { c.Favorite = on })
}

func (s *memStore) DeleteContact(ctx context.Context, email string, at time.Time) error {
	_, err := s.editContact(email, func(c *ContactState) { c.Forget(at) })
	return err
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

func (s *memStore) AttachmentFileRefs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.attachmentFiles), nil
}

func (s *memStore) PrepareCache(ctx context.Context, version int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheVersion == version {
		return false, nil
	}
	s.cacheVersion = version
	for _, byUID := range s.messages {
		for uid, m := range byUID {
			m.BodyText, m.BodyHTML, m.BodyTruncated = "", "", false
			m.CachedAt, m.Attachments = time.Time{}, nil
			byUID[uid] = m
		}
	}
	return true, nil
}

// remindedLocked reports whether an account's conversation has a
// reminder.
func (s *memStore) remindedLocked(accountID string, thread ThreadID) bool {
	_, ok := s.reminders[accountID][thread]
	return ok
}

func (s *memStore) Reminders(ctx context.Context, accountID string) ([]Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Reminder
	for _, r := range s.reminders[accountID] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RemindAt.Equal(out[j].RemindAt) {
			return out[i].RemindAt.Before(out[j].RemindAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *memStore) DueReminders(ctx context.Context, at time.Time) ([]Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Reminder
	for _, byThread := range s.reminders {
		for _, r := range byThread {
			if !r.RemindAt.After(at) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RemindAt.Equal(out[j].RemindAt) {
			return out[i].RemindAt.Before(out[j].RemindAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *memStore) SaveReminder(ctx context.Context, r Reminder) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reminders[r.Account] == nil {
		s.reminders[r.Account] = map[ThreadID]Reminder{}
	}
	if old, ok := s.reminders[r.Account][r.Thread]; ok {
		r.ID, r.CreatedAt = old.ID, old.CreatedAt
	} else {
		s.reminderNext++
		r.ID = s.reminderNext
	}
	s.reminders[r.Account][r.Thread] = r
	return r, nil
}

func (s *memStore) DeleteReminder(ctx context.Context, accountID string, thread ThreadID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reminders[accountID], thread)
	return nil
}

func (s *memStore) DismissFollowUp(ctx context.Context, accountID string, thread ThreadID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dismissed[accountID] == nil {
		s.dismissed[accountID] = map[ThreadID]time.Time{}
	}
	s.dismissed[accountID][thread] = at
	return nil
}

// CountFollowUps counts the conversations that wait for a reply, as the
// engine's query does from the cache.
func (s *memStore) CountFollowUps(ctx context.Context, q ListQuery) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := q.Scope.Accounts
	if len(accounts) == 0 {
		for id := range s.accounts {
			accounts = append(accounts, id)
		}
	}
	count := 0
	for _, accountID := range accounts {
		count += len(s.followThreadsLocked(accountID, q.Scope.FollowUpNow))
	}
	return count, nil
}

// followThreadsLocked are an account's conversations that wait for a
// reply at now, as FollowUpScope finds them.
func (s *memStore) followThreadsLocked(accountID string, now time.Time) map[ThreadID]bool {
	if now.IsZero() {
		now = time.Now()
	}
	folders := map[string]Folder{}
	for _, f := range s.folders[accountID] {
		folders[f.Name] = f
	}
	newest := map[ThreadID]Summary{}
	aside := map[ThreadID]bool{}
	for key, byUID := range s.messages {
		if !strings.HasPrefix(key, accountID+"/") {
			continue
		}
		folder := key[len(accountID)+1:]
		fl, ok := folders[folder]
		if !ok {
			continue
		}
		for _, msg := range byUID {
			m := msg.Summary
			m.Folder = folder
			switch fl.SpecialUse {
			case SpecialJunk, SpecialTrash:
				aside[m.Thread] = true
				continue
			case SpecialDrafts:
				continue
			}
			if cur, ok := newest[m.Thread]; !ok || m.Date.After(cur.Date) {
				newest[m.Thread] = m
			}
		}
	}
	out := map[ThreadID]bool{}
	for thread, m := range newest {
		if aside[thread] {
			continue
		}
		if folders[m.Folder].SpecialUse != SpecialSent {
			continue
		}
		if _, ok := FollowUpAge(now, m.Date); !ok {
			continue
		}
		if at, ok := s.dismissed[accountID][thread]; ok && !at.Before(m.Date) {
			continue
		}
		out[thread] = true
	}
	return out
}

func (s *memStore) SmartMailboxes(ctx context.Context, accountID string) ([]SmartMailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SmartMailbox(nil), s.smarts[accountID]...), nil
}

func (s *memStore) SaveSmartMailbox(ctx context.Context, m SmartMailbox) (SmartMailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := SmartNameKey(m.Name)
	for _, have := range s.smarts[m.Account] {
		if have.ID != m.ID && SmartNameKey(have.Name) == key {
			return SmartMailbox{}, ErrSmartExists
		}
	}
	if m.ID == 0 {
		s.smartNext++
		m.ID = s.smartNext
		s.smarts[m.Account] = append(s.smarts[m.Account], m)
		return m, nil
	}
	for i := range s.smarts[m.Account] {
		if s.smarts[m.Account][i].ID == m.ID {
			m.Folder = s.smarts[m.Account][i].Folder
			s.smarts[m.Account][i] = m
			return m, nil
		}
	}
	return SmartMailbox{}, ErrNotFound
}

func (s *memStore) DeleteSmartMailbox(ctx context.Context, accountID string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.smarts[accountID] {
		if m.ID == id {
			s.smarts[accountID] = append(s.smarts[accountID][:i], s.smarts[accountID][i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// CountUnread counts the unread messages a list query reads.
func (s *memStore) CountUnread(ctx context.Context, q ListQuery) (int, error) {
	if err := q.Scope.Check(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var follow map[string]map[ThreadID]bool
	if q.Scope.FollowUp {
		follow = map[string]map[ThreadID]bool{}
		accounts := q.Scope.Accounts
		if len(accounts) == 0 {
			for id := range s.accounts {
				accounts = append(accounts, id)
			}
		}
		for _, accountID := range accounts {
			follow[accountID] = s.followThreadsLocked(accountID, q.Scope.FollowUpNow)
		}
	}
	n := 0
	s.pageLocked(q.Scope, func(f Folder, m Message, row memRow, inScope bool) {
		if !inScope || m.Seen {
			return
		}
		if _, ok := memFound(q.Search, f, m, s.namedLocked(row, m)); !ok {
			return
		}
		if follow != nil && !follow[row.Account][row.Thread] {
			return
		}
		n++
	})
	return n, nil
}

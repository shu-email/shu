package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// defaultSyncCap is how many headers one folder sync fetches.
const defaultSyncCap = 2000

// Service is the mail engine's public surface. It reads the cache first,
// writes it from background syncs, and talks IMAP and SMTP one short
// connection at a time.
type Service struct {
	store     Store
	secrets   SecretStore
	cacheDir  string
	dataDir   string
	tlsConfig *tls.Config
	clock     func() time.Time

	// Drafts are saved while a sync uploads them. draftMu makes each
	// read-then-write of a draft row one step, draftSync lets one job at
	// a time change the server's Drafts folder, and draftsReplaced
	// remembers the drafts another mail client replaced, with the files
	// they had (see draft.go).
	draftMu        sync.Mutex
	draftSync      chan struct{}
	draftsReplaced map[string][]Attachment

	// fileMu makes a file of the draft file store and the row naming it
	// one step for the sweep that removes unnamed files. incoming is the
	// files still being copied in, which the sweep leaves alone. Where
	// both are needed, fileMu is taken before draftMu.
	fileMu   sync.Mutex
	incoming map[string]bool

	// The messages waiting out their send delay that this run looks
	// after (see schedule.go): those it scheduled and those it took over
	// from an earlier run, each with the channel Unsend closes to end the
	// wait for it. sendMu guards the map.
	sendMu sync.Mutex
	waits  map[string]chan struct{}

	// The watchers, one per account watched (see watch.go). watchLife
	// makes each start and stop of one a single step; watchMu guards the
	// map and what the watchers share: the interval between checks and
	// whether the computer is offline. watchOver is set when the app
	// quits, after which none starts.
	watchLife    sync.Mutex
	watchOver    bool
	intervalRead bool
	watchMu      sync.Mutex
	watchers     map[string]*watcher
	interval     time.Duration
	offline      bool
	// newTimer arms the timers the watchers and the messages waiting out
	// their send delay wait on. Tests replace it, with the clock, so that
	// nothing waits in real time.
	newTimer func(time.Duration) (<-chan time.Time, func())
	// watchNoIdle has the watchers treat every server as one that cannot
	// push. It is for tests: the fake server always offers IDLE.
	watchNoIdle bool

	// Signing in with OAuth (oauth.go, signin.go): the providers, which
	// tests point at a fake one, the HTTP client of their token
	// endpoints, and the access token of each OAuth account, which lives
	// in memory only, with the lock of its renewals.
	oauthProviders map[string]oauthProvider
	httpClient     *http.Client
	tokenMu        sync.Mutex
	tokens         map[string]*oauthToken
}

// NewService wires the cache, the keychain and the attachment cache
// directory. SetDataDir gives it the directory the attachments of drafts
// are kept in, which is no cache.
func NewService(store Store, secrets SecretStore, cacheDir string) *Service {
	return &Service{
		store: store, secrets: secrets, cacheDir: cacheDir, clock: time.Now,
		draftSync:      make(chan struct{}, 1),
		draftsReplaced: map[string][]Attachment{},
		incoming:       map[string]bool{},
		waits:          map[string]chan struct{}{},
		watchers:       map[string]*watcher{},
		newTimer:       realTimer,
		oauthProviders: defaultOAuthProviders(),
		httpClient:     newOAuthClient(),
		tokens:         map[string]*oauthToken{},
	}
}

// SetTLSConfig replaces the TLS configuration, for tests that trust a
// local fake server's self-signed certificate.
func (s *Service) SetTLSConfig(cfg *tls.Config) { s.tlsConfig = cfg }

// SetClock replaces the clock, for deterministic tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

func (s *Service) now() time.Time { return s.clock().UTC() }

// Accounts lists the accounts and the active one.
func (s *Service) Accounts(ctx context.Context) ([]Account, string, error) {
	accounts, err := s.store.Accounts(ctx)
	if err != nil {
		return nil, "", err
	}
	active, err := s.store.ActiveAccount(ctx)
	if err != nil {
		return nil, "", err
	}
	if active == "" && len(accounts) > 0 {
		active = accounts[0].ID
	}
	return accounts, active, nil
}

// AddAccount validates an account, stores it and puts its password in the
// keychain. An account that signs in with OAuth is added by signing in
// (AddOAuthAccount).
func (s *Service) AddAccount(ctx context.Context, a Account, password string) error {
	if a.ID == "" {
		a.ID = NewAccountID()
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if a.OAuth != "" {
		return errors.New("mail: an account that signs in with OAuth is added by signing in")
	}
	if password == "" {
		return errors.New("mail: the password is empty")
	}
	if err := s.store.SaveAccount(ctx, a); err != nil {
		return err
	}
	if err := s.secrets.Set(PasswordKey(a.ID), []byte(password)); err != nil {
		_ = s.store.DeleteAccount(ctx, a.ID)
		return fmt.Errorf("mail: store the password: %w", err)
	}
	active, err := s.store.ActiveAccount(ctx)
	if err == nil && active == "" {
		_ = s.store.SetActiveAccount(ctx, a.ID)
	}
	return nil
}

// UpdateAccount saves changes. An empty password keeps the stored one.
// How an account signs in does not change here. Of an account that signs
// in with OAuth only the name does: its address, servers and sign-in are
// the provider's, and it has no password.
func (s *Service) UpdateAccount(ctx context.Context, a Account, password string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	stored, ok, err := s.store.Account(ctx, a.ID)
	if err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	switch {
	case stored.OAuth != "" && password != "":
		return errors.New("mail: an account that signs in with OAuth has no password")
	case stored.OAuth != "":
		stored.Name = a.Name
		a = stored
	case a.OAuth != "":
		return errors.New("mail: an account that signs in with a password cannot change to OAuth")
	}
	if err := s.store.SaveAccount(ctx, a); err != nil {
		return err
	}
	if password != "" {
		if err := s.secrets.Set(PasswordKey(a.ID), []byte(password)); err != nil {
			return fmt.Errorf("mail: store the password: %w", err)
		}
	}
	return nil
}

// RemoveAccount deletes an account's rows and its keychain entries: its
// password, or its OAuth tokens.
func (s *Service) RemoveAccount(ctx context.Context, id string) error {
	// Its watcher ends first: a check that ran on would write the mail
	// of an account that is no longer there.
	s.Unwatch(id)
	a, ok, err := s.store.Account(ctx, id)
	if err != nil {
		return err
	}
	if ok && a.OAuth != "" {
		// The tokens go before the rows, and once a renewal under way
		// has stored the refresh token it got: none may stay in the
		// keychain for an account that is gone.
		t := s.tokenOf(id)
		if err := t.acquire(ctx); err != nil {
			return err
		}
		defer t.release()
		if err := s.secrets.Delete(tokensKey(id)); err != nil {
			return fmt.Errorf("mail: remove the sign-in from the keychain: %w", err)
		}
		t.access, t.renewAt = "", time.Time{}
	}
	if err := s.store.DeleteAccount(ctx, id); err != nil {
		return err
	}
	s.tokenMu.Lock()
	delete(s.tokens, id)
	s.tokenMu.Unlock()
	// A missing keychain entry is not an error; the account is gone
	// either way.
	_ = s.secrets.Delete(PasswordKey(id))
	return nil
}

// SetActive remembers the account the window shows.
func (s *Service) SetActive(ctx context.Context, id string) error {
	return s.store.SetActiveAccount(ctx, id)
}

// account loads an account and its password. An account that signs in
// with OAuth has none: each connection signs in with its access token,
// renewed as needed (accessToken). Its keychain entry is read here all
// the same, as a password is, so that an operation of an account that
// has to sign in again fails before it begins (ErrSignInRequired).
func (s *Service) account(ctx context.Context, id string) (Account, string, error) {
	a, ok, err := s.store.Account(ctx, id)
	if err != nil {
		return Account{}, "", err
	}
	if !ok {
		return Account{}, "", ErrNotFound
	}
	if a.OAuth != "" {
		if err := s.signInState(a.ID); err != nil {
			return Account{}, "", err
		}
		return a, "", nil
	}
	password, err := s.password(a.ID)
	if err != nil {
		return Account{}, "", err
	}
	return a, password, nil
}

func (s *Service) password(id string) (string, error) {
	data, err := s.secrets.Get(PasswordKey(id))
	if err != nil {
		return "", fmt.Errorf("mail: read the password from the keychain: %w", err)
	}
	return string(data), nil
}

// TestConnection logs in to IMAP and dials and authenticates SMTP, without
// sending anything. An account that signs in with OAuth does so with its
// tokens, and takes no password.
func (s *Service) TestConnection(ctx context.Context, a Account, password string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if password == "" && a.OAuth == "" {
		var err error
		if password, err = s.password(a.ID); err != nil {
			return err
		}
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	if _, err := s.listFolders(c); err != nil {
		return err
	}
	conn, err := s.dialSMTP(ctx, a, password)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return conn.Quit()
}

// CachedFolders reads the folder list, without network.
func (s *Service) CachedFolders(ctx context.Context, accountID string) ([]Folder, error) {
	return s.store.Folders(ctx, accountID)
}

// CachedMessages reads the newest cached headers of a folder, or search
// results from the cache.
func (s *Service) CachedMessages(ctx context.Context, accountID, folder, query string, limit int) ([]Summary, error) {
	return s.store.ListMessages(ctx, accountID, folder, query, limit)
}

// CachedConversations reads a conversation list from the cache, without
// network: one row for each conversation with a message in folder (a
// folder's name, "starred" for the flagged messages, "" for the whole
// account), newest first, at most limit. A query keeps the conversations
// any message of which matches it in its subject, sender or body, in any
// folder, and each row says which did. The message list of
// CachedMessages stays for a view of single messages.
func (s *Service) CachedConversations(ctx context.Context, accountID, folder, query string, limit int) ([]Conversation, error) {
	return s.store.ListConversations(ctx, accountID, folder, query, limit)
}

// CachedConversation reads the messages of a conversation from the cache,
// oldest first, each with its folder: those in folder, a scope as
// CachedConversations takes, so that "" reads the conversation whole, in
// every folder, to show it. With the scope of a list it returns exactly
// the messages the list's row stands for, which a conversation-wide
// action acts on. Tombstones are left out.
func (s *Service) CachedConversation(ctx context.Context, accountID, folder string, thread ThreadID) ([]Summary, error) {
	return s.store.ConversationMessages(ctx, accountID, folder, thread)
}

// MessagePage reads a page of messages from the cache, without network:
// the newest of a scope, which may span folders and accounts, that a
// search finds, after a cursor. Each row has its account, and the parts
// a search found its terms in.
func (s *Service) MessagePage(ctx context.Context, q ListQuery) (MessagePage, error) {
	return s.store.MessagePage(ctx, q)
}

// ConversationPage reads a page of conversations from the cache, without
// network: a row for each conversation with a message in the scope, made
// of the scope's messages, the one with the newest first. A search keeps
// the conversations any message of which, in any folder of its account,
// matches it whole, and each row names those messages with the parts the
// terms were found in.
func (s *Service) ConversationPage(ctx context.Context, q ListQuery) (ConversationPage, error) {
	return s.store.ConversationPage(ctx, q)
}

// ScopedConversation reads the messages of an account's conversation in a
// scope, oldest first: those a row of ConversationPage for that scope
// stands for, which a conversation-wide action acts on. Nothing is in the
// scope of an account it leaves out.
func (s *Service) ScopedConversation(ctx context.Context, accountID string, scope Scope, thread ThreadID) ([]Summary, error) {
	if err := scope.Check(); err != nil {
		return nil, err
	}
	if len(scope.Accounts) > 0 && !slices.Contains(scope.Accounts, accountID) {
		return nil, nil
	}
	all, err := s.store.ConversationMessages(ctx, accountID, "", thread)
	if err != nil {
		return nil, err
	}
	// in is whether a folder is one the scope reads, of those a special
	// use or a label narrows it to.
	in := map[string]bool{}
	if scope.Special != "" || scope.Label != 0 {
		folders, err := s.store.Folders(ctx, accountID)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			if scope.Label != 0 {
				in[f.Name] = f.InLabelScope()
			} else {
				in[f.Name] = f.SpecialUse == scope.Special
			}
		}
	}
	return slices.DeleteFunc(all, func(m Summary) bool {
		switch {
		case scope.Starred:
			return !m.Flagged
		case scope.Folder != "":
			return m.Folder != scope.Folder
		case scope.Special != "":
			return !in[m.Folder]
		case scope.Label != 0:
			return !in[m.Folder] || !m.HasLabel(scope.Label)
		}
		return false
	}), nil
}

// Inboxes returns the inbox of every account, with its counts, in the
// order the accounts were added: what a unified inbox shows of each.
func (s *Service) Inboxes(ctx context.Context) ([]AccountFolder, error) {
	accounts, err := s.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []AccountFolder
	for _, a := range accounts {
		folders, err := s.store.Folders(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			if f.IsInbox() {
				out = append(out, AccountFolder{AccountID: a.ID, Folder: f})
			}
		}
	}
	return out, nil
}

// FolderCache says how much of a folder is on this device: how many of
// its messages, of how many on the server, and whether older ones are
// left there for FetchOlder.
func (s *Service) FolderCache(ctx context.Context, accountID, folder string) (FolderCache, error) {
	return s.store.FolderCache(ctx, accountID, folder)
}

// CachedMessage reads one cached message. The bool is false when only the
// header is cached and the body must be fetched.
func (s *Service) CachedMessage(ctx context.Context, accountID, folder string, uid UID) (FetchResult, bool, error) {
	m, ok, err := s.store.GetMessage(ctx, accountID, folder, uid)
	if err != nil || !ok {
		return FetchResult{}, false, err
	}
	if !m.BodyCached {
		return FetchResult{Message: m}, false, nil
	}
	return FetchResult{Message: m}, true, nil
}

// SyncAccount refreshes the folder list and counts.
func (s *Service) SyncAccount(ctx context.Context, accountID string) ([]Folder, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return nil, err
	}
	defer c.close()
	folders, err := s.listFolders(c)
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveFolders(ctx, accountID, folders); err != nil {
		return nil, err
	}
	return s.store.Folders(ctx, accountID)
}

// SyncFolder fetches a folder's changes into the cache. Its first sync
// caches the newest limit messages, the settings' window (SyncWindow)
// when limit is not above zero; every sync refreshes the flags of as
// many, and of the messages that arrived since the last it keeps the
// newest 2000, or limit if that is more. What it leaves out FetchOlder
// fetches.
func (s *Service) SyncFolder(ctx context.Context, accountID, folder string, limit int) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, folder); err != nil {
		return err
	} else if !ok {
		if _, err := s.SyncAccount(ctx, accountID); err != nil {
			return err
		}
	}
	if err := s.syncFolder(ctx, a, password, folder, limit); err != nil {
		return err
	}
	_ = PruneAttachments(s.cacheDir, defaultAttachmentBudget)
	return nil
}

// FetchMessage fetches a body on demand and caches it.
func (s *Service) FetchMessage(ctx context.Context, accountID, folder string, uid UID, markSeen bool) (FetchResult, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return FetchResult{}, err
	}
	result, err := s.fetchMessage(ctx, a, password, folder, uid, markSeen)
	if err != nil {
		return FetchResult{}, err
	}
	m := result.Message
	m.BodyCached = true
	if err := s.store.SaveMessage(ctx, accountID, folder, m); err != nil {
		return result, err
	}
	// Its labels are the cache's: those kept on this device too.
	if cached, ok, err := s.store.GetMessage(ctx, accountID, folder, uid); err == nil && ok {
		result.Message.Labels, result.Message.LabelsLocal = cached.Labels, cached.LabelsLocal
	}
	return result, nil
}

// SetFlag changes a flag on the server and then in the cache. Callers
// show the change optimistically and roll it back when this fails.
func (s *Service) SetFlag(ctx context.Context, accountID, folder string, uid UID, flag Flag, on bool) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if err := s.storeFlags(ctx, a, password, folder, uid, flag, on); err != nil {
		return err
	}
	return s.store.SetFlag(ctx, accountID, folder, uid, flag, on)
}

// SetFlags changes a flag on several messages, as starring a conversation
// or marking it read does: one connection, and for each folder one SELECT
// and one STORE, then the cache. Only \Seen and \Flagged change this way,
// and nothing is moved, deleted or expunged. A folder whose UIDVALIDITY is
// no longer the cache's is not touched: its UIDs would name other
// messages. When a folder fails, those before it keep their change.
func (s *Service) SetFlags(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool) error {
	if flag != FlagSeen && flag != FlagFlagged {
		return fmt.Errorf("mail: flag %q is not set on several messages", flag)
	}
	groups := byFolder(msgs, "")
	if len(groups) == 0 {
		return nil
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	for _, g := range groups {
		cached, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		if err := storeFlagsIn(c, g.folder, cached.UIDValidity, g.uids, flag, on); err != nil {
			return err
		}
		for _, uid := range g.uids {
			// A row a sync removed meanwhile has nothing to change.
			if err := s.store.SetFlag(ctx, accountID, g.folder, uid, flag, on); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

// Move archives, junks or deletes a message: the cache row is a tombstone
// until the server confirms the move.
func (s *Service) Move(ctx context.Context, accountID, folder string, uid UID, dest string) (MoveResult, error) {
	if dest == "" {
		return MoveResult{}, errors.New("mail: the destination folder is empty")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return MoveResult{}, err
	}
	if err := s.store.MarkDeleted(ctx, accountID, folder, uid, true); err != nil {
		return MoveResult{}, err
	}
	moved, err := s.moveOnServer(ctx, a, password, folder, []UID{uid}, dest)
	if err != nil {
		_ = s.store.MarkDeleted(ctx, accountID, folder, uid, false)
		return MoveResult{}, err
	}
	return moved[0], s.store.DeleteMessage(ctx, accountID, folder, uid)
}

// MoveBack undoes a move when the server reported the destination UID.
// The caller syncs the source folder to restore its cache row.
func (s *Service) MoveBack(ctx context.Context, accountID, from, to string, uid UID) error {
	if uid == 0 {
		return errors.New("mail: the moved message has no destination UID")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	_, err = s.moveOnServer(ctx, a, password, from, []UID{uid}, to)
	return err
}

// MoveMessages moves several messages to dest: a conversation, or what of
// it a list shows. The messages of each folder move in one command. Their
// cache rows are tombstones until the server confirms, and come back if
// it fails. It returns a result for each message that moved, with what
// MoveBackMessages needs; when a folder fails, the error comes with the
// results of the folders that moved before it. Messages already in dest
// stay where they are.
func (s *Service) MoveMessages(ctx context.Context, accountID string, msgs []MessageRef, dest string) ([]MoveResult, error) {
	if dest == "" {
		return nil, errors.New("mail: the destination folder is empty")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	groups := byFolder(msgs, dest)
	// The rows come back even when the move failed because ctx ended.
	restore := func(groups []uidGroup) {
		for _, g := range groups {
			for _, uid := range g.uids {
				_ = s.store.MarkDeleted(context.WithoutCancel(ctx), accountID, g.folder, uid, false)
			}
		}
	}
	for i, g := range groups {
		for j, uid := range g.uids {
			if err := s.store.MarkDeleted(ctx, accountID, g.folder, uid, true); err != nil {
				restore(groups[:i])
				restore([]uidGroup{{g.folder, g.uids[:j]}})
				return nil, err
			}
		}
	}
	var results []MoveResult
	var cacheErr error
	for i, g := range groups {
		moved, err := s.moveOnServer(ctx, a, password, g.folder, g.uids, dest)
		if err != nil {
			restore(groups[i:])
			return results, err
		}
		results = append(results, moved...)
		for _, uid := range g.uids {
			if err := s.store.DeleteMessage(ctx, accountID, g.folder, uid); err != nil && cacheErr == nil {
				cacheErr = err
			}
		}
	}
	return results, cacheErr
}

// MoveBackMessages undoes MoveMessages: each message goes back to the
// folder it came from, found by the UID the server gave it where it went.
// A message the server gave no UID for cannot be found, and stays; the
// error says so once the others are back. The caller syncs the folders
// the messages return to, to restore their cache rows.
func (s *Service) MoveBackMessages(ctx context.Context, accountID string, moved []MoveResult) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	type route struct{ from, to string }
	var order []route
	byRoute := map[route][]UID{}
	lost := 0
	for _, m := range moved {
		if m.DestUID == 0 {
			lost++
			continue
		}
		r := route{m.Folder, m.From}
		if _, ok := byRoute[r]; !ok {
			order = append(order, r)
		}
		if !slices.Contains(byRoute[r], m.DestUID) {
			byRoute[r] = append(byRoute[r], m.DestUID)
		}
	}
	for _, r := range order {
		if _, err := s.moveOnServer(ctx, a, password, r.from, byRoute[r], r.to); err != nil {
			return err
		}
	}
	if lost > 0 {
		return fmt.Errorf("mail: the server gave no UID for %d moved messages, which stay where they are", lost)
	}
	return nil
}

// uidGroup is the UIDs of one folder, in the order first asked for.
type uidGroup struct {
	folder string
	uids   []UID
}

// byFolder groups messages by folder, once each and in their order,
// leaving out those in skip.
func byFolder(msgs []MessageRef, skip string) []uidGroup {
	var out []uidGroup
	index := map[string]int{}
	for _, m := range msgs {
		if m.Folder == skip {
			continue
		}
		i, ok := index[m.Folder]
		if !ok {
			i = len(out)
			index[m.Folder] = i
			out = append(out, uidGroup{folder: m.Folder})
		}
		if !slices.Contains(out[i].uids, m.UID) {
			out[i].uids = append(out[i].uids, m.UID)
		}
	}
	return out
}

// Attachment returns an attachment's bytes, from the disk cache or by
// fetching the message again.
func (s *Service) Attachment(ctx context.Context, accountID, folder string, uid UID, part string) (Attachment, []byte, error) {
	m, ok, err := s.store.GetMessage(ctx, accountID, folder, uid)
	if err != nil {
		return Attachment{}, nil, err
	}
	if !ok {
		return Attachment{}, nil, ErrNotFound
	}
	find := func(list []Attachment) (Attachment, bool) {
		for _, a := range list {
			if a.Part == part {
				return a, true
			}
		}
		return Attachment{}, false
	}
	a, found := find(m.Attachments)
	if !found || (a.CachePath == "" && !m.BodyCached) {
		result, err := s.FetchMessage(ctx, accountID, folder, uid, false)
		if err != nil {
			return Attachment{}, nil, err
		}
		a, found = find(result.Message.Attachments)
		if !found {
			return Attachment{}, nil, ErrNotFound
		}
		if a.CachePath == "" && len(a.Data) > 0 {
			return a, a.Data, nil
		}
	}
	if a.CachePath != "" {
		data, err := os.ReadFile(a.CachePath)
		if err == nil {
			return a, data, nil
		}
	}
	if len(a.Data) > 0 {
		return a, a.Data, nil
	}
	return Attachment{}, nil, ErrNotFound
}

// PendingSends lists unsent outbox entries, including sends whose outcome
// a crash left unknown.
func (s *Service) PendingSends(ctx context.Context, accountID string) ([]PendingSend, error) {
	return s.store.PendingSends(ctx, accountID)
}

// findSent returns the account's Sent folder, or "".
func (s *Service) findSent(ctx context.Context, accountID string) string {
	folders, err := s.store.Folders(ctx, accountID)
	if err != nil {
		return ""
	}
	for _, f := range folders {
		if f.SpecialUse == SpecialSent {
			return f.Name
		}
	}
	return ""
}

// cacheAttachment writes an attachment's bytes once per content hash.
func (s *Service) cacheAttachment(a *Attachment) error {
	if s.cacheDir == "" || len(a.Data) == 0 {
		return nil
	}
	if a.ContentHash == "" {
		return errors.New("mail: attachment has no content hash")
	}
	dir := filepath.Join(s.cacheDir, "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, a.ContentHash)
	if _, err := os.Stat(path); err == nil {
		a.CachePath = path
		return nil
	}
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(a.Data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	a.CachePath = path
	return nil
}

// defaultAttachmentBudget bounds the disk cache.
const defaultAttachmentBudget = 200 << 20

// PruneAttachments removes the least recently used attachment files until
// the cache fits maxBytes.
func PruneAttachments(dir string, maxBytes int64) error {
	if dir == "" {
		return nil
	}
	root := filepath.Join(dir, "attachments")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type file struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{filepath.Join(root, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= maxBytes {
		return nil
	}
	// Oldest first, as the LRU policy wants.
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].mtime.Before(files[j-1].mtime); j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
	return nil
}

package mail

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
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
	// started is when this run began: a temporary file older than it is
	// an earlier run's, which the sweep may take.
	started time.Time

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

	// syncMu guards the window's receiver of sync events (see
	// sync_events.go), which OnSync sets.
	syncMu     sync.Mutex
	syncReport func(SyncEvent)

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
	// queues hold the worker that makes each watched account's queued
	// actions (see queue.go), under the watcher's life.
	queues   map[string]*queueWorker
	interval time.Duration
	offline  bool
	// newTimer arms the timers the watchers and the messages waiting out
	// their send delay wait on. Tests replace it, with the clock, so that
	// nothing waits in real time.
	newTimer func(time.Duration) (<-chan time.Time, func())
	// queueTimer arms the waits of the queued actions' workers, which
	// tests drive apart from the watchers'.
	queueTimer func(time.Duration) (<-chan time.Time, func())
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

	// Rules (rules.go, arrivals.go, ruleapply.go, ruleundo.go).
	// arrivalMu makes the write of a sync's mail and the sorting of what
	// arrived one step, which the watcher's taking of the mail to announce
	// waits for. movedIn is the messages a sync found that this app moved
	// into an inbox, which are no news, by account and folder. ruleMu
	// guards the rest: the lock of each account's rule actions, the
	// goroutine that applies them in the background, one per account at
	// most, with whether it is asked for another round, ruleIdle
	// signalled as one ends, the context that ends them all as the app
	// quits, and the window's hook.
	arrivalMu sync.Mutex
	movedIn   map[string]map[UID]bool
	// heldBack is the mail a block queued to file as a check found it, by
	// account and folder, which the watcher announces none of (see
	// planBlocked). A move the server refuses brings the row back to the
	// list, but the mail stays unannounced: the user blocked its sender.
	heldBack    map[string]map[UID]bool
	ruleMu      sync.Mutex
	ruleLocks   map[string]*sync.Mutex
	ruleRunning map[string]bool
	ruleAgain   map[string]bool
	ruleIdle    *sync.Cond
	ruleCtx     context.Context
	ruleStop    context.CancelFunc
	ruleReport  func(accountID string)

	// Finding a domain's servers from an address (discover.go): the
	// client the configuration endpoints are read with, the resolver the
	// DNS records come from, and how long a lookup and each request may
	// take. Tests replace them with fixtures and no real network.
	discoverClient         func(context.Context) (*http.Client, error)
	lookupSRV              func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	discoverTimeout        time.Duration
	discoverRequestTimeout time.Duration
}

// NewService wires the cache, the keychain and the attachment cache
// directory. SetDataDir gives it the directory the attachments of drafts
// are kept in, which is no cache.
func NewService(store Store, secrets SecretStore, cacheDir string) *Service {
	s := &Service{
		store: store, secrets: secrets, cacheDir: cacheDir, clock: time.Now, started: time.Now(),
		draftSync:      make(chan struct{}, 1),
		draftsReplaced: map[string][]Attachment{},
		incoming:       map[string]bool{},
		waits:          map[string]chan struct{}{},
		watchers:       map[string]*watcher{},
		queues:         map[string]*queueWorker{},
		newTimer:       realTimer,
		queueTimer:     realTimer,
		oauthProviders: defaultOAuthProviders(),
		httpClient:     newOAuthClient(),
		tokens:         map[string]*oauthToken{},
		movedIn:        map[string]map[UID]bool{},
		heldBack:       map[string]map[UID]bool{},
		ruleLocks:      map[string]*sync.Mutex{},
		ruleRunning:    map[string]bool{},
		ruleAgain:      map[string]bool{},
	}
	s.ruleIdle = sync.NewCond(&s.ruleMu)
	s.ruleCtx, s.ruleStop = context.WithCancel(context.Background())
	s.discoverClient = s.HTTPClient
	s.lookupSRV = net.DefaultResolver.LookupSRV
	s.discoverTimeout = discoverTimeout
	s.discoverRequestTimeout = discoverRequestTimeout
	return s
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
	// Its cached mail went with it, and so may its attachment files.
	_ = s.SweepCache(ctx)
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
	if scope.Special != "" || scope.Label != 0 || scope.Reminded || scope.FollowUp {
		folders, err := s.store.Folders(ctx, accountID)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			switch {
			case scope.Label != 0, scope.Reminded, scope.FollowUp:
				in[f.Name] = f.InLabelScope()
			default:
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
		case scope.Reminded, scope.FollowUp:
			return !in[m.Folder]
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
func (s *Service) SyncFolder(ctx context.Context, accountID, folder string, limit int) (err error) {
	// The kind is read from the cache first, so that a failure before the
	// conversation with the server, as an account that has to sign in
	// again, still says what this sync was.
	kind := SyncKindFolder
	cached, ok, cacheErr := s.store.Folder(ctx, accountID, folder)
	if cacheErr == nil && (!ok || cached.LastUID == 0) {
		kind = SyncKindFirst
	}
	tr := s.trackSync(accountID, folder, kind)
	defer func() { tr.end(ctx, err) }()
	if cacheErr != nil {
		return cacheErr
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if kind == SyncKindFirst && !ok {
		if _, err := s.SyncAccount(ctx, accountID); err != nil {
			return err
		}
	}
	if _, err := s.syncFolder(ctx, a, password, folder, limit, tr.step); err != nil {
		return err
	}
	_ = s.pruneAttachments(ctx)
	return nil
}

// pruneAttachments bounds the attachment cache, and forgets the files it
// removed so that no row names a file the cache no longer holds. The cache
// is the store's to record: a file gone without its row cleared would read
// as a body cached and an attachment that cannot be opened.
func (s *Service) pruneAttachments(ctx context.Context) error {
	removed, err := PruneAttachments(s.cacheDir, defaultAttachmentBudget)
	if err != nil || len(removed) == 0 {
		return err
	}
	return s.store.ForgetAttachmentFiles(ctx, removed)
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
	return s.setFlags(ctx, accountID, []MessageRef{{Folder: folder, UID: uid}}, flag, on, true)
}

// SetFlags changes a flag on several messages, as starring a conversation
// or marking it read does: one connection, and for each folder one SELECT
// and one STORE, then the cache. Only \Seen and \Flagged change this way,
// and nothing is moved, deleted or expunged. A folder whose UIDVALIDITY is
// no longer the cache's is not touched: its UIDs would name other
// messages. When a folder fails, those before it keep their change. A
// change the server cannot be reached for waits in the queue (see
// queue.go), the cache showing it meanwhile.
func (s *Service) SetFlags(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool) error {
	return s.setFlags(ctx, accountID, msgs, flag, on, true)
}

// setFlags is SetFlags with the queue or without it. The rules and their
// undo keep their own log of what the server has not made yet, so they
// ask for every change to be made now or to fail.
func (s *Service) setFlags(ctx context.Context, accountID string, msgs []MessageRef, flag Flag, on bool, queue bool) error {
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
	// What each folder asks of the server is written before it is
	// touched: a change made offline outlives the app.
	validity := make([]uint32, len(groups))
	ids := make([][]int64, len(groups))
	for i, g := range groups {
		cached, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			s.dropOperations(ctx, allIDs(ids))
			return err
		}
		if !ok {
			s.dropOperations(ctx, allIDs(ids))
			return fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		validity[i] = cached.UIDValidity
		if !queue {
			continue
		}
		ops := make([]PendingOperation, 0, len(g.uids))
		for _, uid := range g.uids {
			ops = append(ops, PendingOperation{
				Kind: OpFlags, Folder: g.folder, UID: uid,
				Flag: flag, On: on, Validity: cached.UIDValidity,
			})
		}
		if ids[i], err = s.enqueueOperations(ctx, accountID, ops); err != nil {
			s.dropOperations(ctx, allIDs(ids))
			return err
		}
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		if queue && retryable(err) {
			// The server is out of reach: the cache shows the change the
			// user asked for, and the worker makes it later.
			_ = s.setFlagsInCache(ctx, accountID, groups, flag, on)
			s.requeueOperations(ctx, accountID, allIDs(ids))
			return nil
		}
		s.dropOperations(ctx, allIDs(ids))
		return err
	}
	defer c.close()
	for i, g := range groups {
		if err := storeFlagsIn(c, g.folder, validity[i], g.uids, flag, on); err != nil {
			if queue && retryable(err) {
				_ = s.setFlagsInCache(ctx, accountID, groups[i:], flag, on)
				s.requeueOperations(ctx, accountID, allIDs(ids[i:]))
				return nil
			}
			s.dropOperations(ctx, allIDs(ids[i:]))
			return err
		}
		// A row a sync removed meanwhile has nothing to change.
		if err := s.store.SetFlags(ctx, accountID, g.folder, g.uids, flag, on); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		s.dropOperations(ctx, ids[i])
	}
	return nil
}

// setFlagsInCache shows flags the server has not been told of yet: the
// change waits in the queue for it. A row a sync removed meanwhile has
// nothing to change.
func (s *Service) setFlagsInCache(ctx context.Context, accountID string, groups []uidGroup, flag Flag, on bool) error {
	bg := context.WithoutCancel(ctx)
	for _, g := range groups {
		if err := s.store.SetFlags(bg, accountID, g.folder, g.uids, flag, on); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// Move archives, junks or deletes a message: the cache row is a tombstone
// until the server confirms the move, and a move the server cannot be
// reached for waits in the queue, its result naming it.
func (s *Service) Move(ctx context.Context, accountID, folder string, uid UID, dest string) (MoveResult, error) {
	moved, err := s.MoveMessages(ctx, accountID, []MessageRef{{Folder: folder, UID: uid}}, dest)
	if err != nil || len(moved) == 0 {
		return MoveResult{}, err
	}
	return moved[0], nil
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
// stay where they are. A move the server cannot be reached for waits in
// the queue (see queue.go): the rows stay tombstones, and the result of
// each message names the action to undo it.
func (s *Service) MoveMessages(ctx context.Context, accountID string, msgs []MessageRef, dest string) ([]MoveResult, error) {
	return s.moveMessages(ctx, accountID, msgs, dest, true)
}

// moveMessages is MoveMessages with the queue or without it. The rules and
// their undo keep their own log of what the server has not made yet, and
// a reminder waits for the next run instead, so they ask for every move to
// be made now or to fail.
func (s *Service) moveMessages(ctx context.Context, accountID string, msgs []MessageRef, dest string, queue bool) ([]MoveResult, error) {
	if dest == "" {
		return nil, errors.New("mail: the destination folder is empty")
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	groups := byFolder(msgs, dest)
	// The move of each folder is written before the server is touched, so
	// one made offline outlives the app.
	opIDs := make([][]int64, len(groups))
	for i, g := range groups {
		if !queue {
			break
		}
		cached, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, err
		}
		if !ok {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		ops := make([]PendingOperation, 0, len(g.uids))
		for _, uid := range g.uids {
			ops = append(ops, PendingOperation{
				Kind: OpMove, Folder: g.folder, UID: uid, Dest: dest, Validity: cached.UIDValidity,
			})
		}
		if opIDs[i], err = s.enqueueOperations(ctx, accountID, ops); err != nil {
			s.dropOperations(ctx, allIDs(opIDs))
			return nil, err
		}
	}
	// The rows come back even when the move failed because ctx ended.
	restore := func(groups []uidGroup) { s.showQueued(ctx, accountID, groups) }
	if err := s.hideQueued(ctx, accountID, groups, allIDs(opIDs)); err != nil {
		return nil, err
	}
	var results []MoveResult
	var cacheErr error
	for i, g := range groups {
		moved, err := s.moveOnServer(ctx, a, password, g.folder, g.uids, dest)
		if err != nil {
			if queue && retryable(err) {
				// The server is out of reach: the rows stay tombstones,
				// and this and the later folders wait in the queue.
				s.requeueOperations(ctx, accountID, allIDs(opIDs[i:]))
				for j := i; j < len(groups); j++ {
					for k, uid := range groups[j].uids {
						results = append(results, MoveResult{
							Folder: dest, From: groups[j].folder, UID: uid, Pending: opIDs[j][k],
						})
					}
				}
				return results, nil
			}
			s.dropOperations(ctx, allIDs(opIDs[i:]))
			restore(groups[i:])
			return results, err
		}
		results = append(results, moved...)
		s.dropOperations(ctx, opIDs[i])
		if err := s.store.DeleteMessages(ctx, accountID, g.folder, g.uids); err != nil && cacheErr == nil {
			cacheErr = err
		}
	}
	if len(results) >= cacheSweepAfter {
		// A move of many messages leaves their attachments' files with
		// no row: take them now rather than at the next start.
		_ = s.SweepCache(ctx)
	}
	return results, cacheErr
}

// MoveBackMessages undoes MoveMessages: each message goes back to the
// folder it came from, found by the UID the server gave it where it went.
// A message the server gave no UID for cannot be found, and stays; the
// error says so once the others are back. A move still in the queue is
// cancelled, never made: one the worker has begun already stays where it
// went, and the error says how many. The caller syncs the folders the
// messages return to, to restore their cache rows.
func (s *Service) MoveBackMessages(ctx context.Context, accountID string, moved []MoveResult) error {
	// A move the server was never told of is undone by dropping its queued
	// action: the cache row stops being a tombstone, and the caller's sync
	// of the folder brings it back.
	var queued []int64
	for _, m := range moved {
		if m.Pending != 0 {
			queued = append(queued, m.Pending)
		}
	}
	cancelled := map[int64]bool{}
	if len(queued) > 0 {
		ids, err := s.store.CancelOperations(ctx, queued)
		if err != nil {
			return err
		}
		for _, id := range ids {
			cancelled[id] = true
		}
	}
	made, lost := 0, 0
	for _, m := range moved {
		if m.Pending != 0 {
			if !cancelled[m.Pending] {
				// The worker had the action already: the message is where
				// it moved, and cannot be found to come back.
				made++
				continue
			}
			_ = s.store.MarkDeleted(context.WithoutCancel(ctx), accountID, m.From, m.UID, false)
		}
	}
	type route struct{ from, to string }
	var order []route
	byRoute := map[route][]UID{}
	for _, m := range moved {
		if m.Pending != 0 {
			continue
		}
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
	if len(order) > 0 {
		a, password, err := s.account(ctx, accountID)
		if err != nil {
			return err
		}
		for _, r := range order {
			if _, err := s.moveOnServer(ctx, a, password, r.from, byRoute[r], r.to); err != nil {
				return err
			}
		}
	}
	var trouble error
	if lost > 0 {
		trouble = fmt.Errorf("mail: the server gave no UID for %d moved messages, which stay where they are", lost)
	}
	if made > 0 {
		trouble = errors.Join(trouble,
			fmt.Errorf("mail: %d moves were made already, and stay where they are", made))
	}
	return trouble
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
// fetching the message again. A path that names no file, or one whose
// bytes are not the attachment's, is no cache: the server has them.
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
	// cached reads the bytes a row's path names, when they are the
	// attachment's own.
	cached := func(a Attachment) ([]byte, bool) {
		if a.CachePath == "" {
			return nil, false
		}
		data, err := os.ReadFile(a.CachePath)
		if err != nil {
			return nil, false
		}
		if a.ContentHash != "" && contentHashOf(data) != a.ContentHash {
			return nil, false
		}
		return data, true
	}
	a, found := find(m.Attachments)
	if found {
		if data, ok := cached(a); ok {
			return a, data, nil
		}
		if len(a.Data) > 0 {
			return a, a.Data, nil
		}
		if a.Size == 0 {
			// Nothing to read, and nothing to fetch: the attachment is
			// empty.
			return a, []byte{}, nil
		}
	}
	// The bytes are not on disk: the body was never fetched, or a prune
	// removed the file and cleared the path, or the file is damaged. A
	// cached body does not say otherwise, so ask the server.
	result, err := s.FetchMessage(ctx, accountID, folder, uid, false)
	if err != nil {
		return Attachment{}, nil, err
	}
	a, found = find(result.Message.Attachments)
	if !found {
		return Attachment{}, nil, ErrNotFound
	}
	if data, ok := cached(a); ok {
		return a, data, nil
	}
	if len(a.Data) > 0 {
		return a, a.Data, nil
	}
	if a.Size == 0 {
		return a, []byte{}, nil
	}
	return Attachment{}, nil, ErrNotFound
}

// contentHashOf is the hash a cached attachment file is stored under.
func contentHashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PendingSends lists unsent outbox entries, including sends whose outcome
// a crash left unknown.
func (s *Service) PendingSends(ctx context.Context, accountID string) ([]PendingSend, error) {
	return s.store.PendingSends(ctx, accountID)
}

// cacheAttachment writes an attachment's bytes once per content hash.
func (s *Service) cacheAttachment(a *Attachment) error {
	if s.cacheDir == "" || len(a.Data) == 0 {
		return nil
	}
	if a.ContentHash == "" {
		return errors.New("mail: attachment has no content hash")
	}
	dir := filepath.Join(s.cacheDir, attachmentCacheDir)
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
// the cache fits maxBytes, and returns the paths it removed: the rows that
// name them are cleared, so that no row names a file the cache no longer
// holds.
func PruneAttachments(dir string, maxBytes int64) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	root := filepath.Join(dir, "attachments")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
		return nil, nil
	}
	// Oldest first, as the LRU policy wants.
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].mtime.Before(files[j-1].mtime); j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	var removed []string
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			removed = append(removed, f.path)
		}
	}
	return removed, nil
}

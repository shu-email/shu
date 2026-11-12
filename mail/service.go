package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	tlsConfig *tls.Config
	clock     func() time.Time
}

// NewService wires the cache, the keychain and the attachment cache
// directory.
func NewService(store Store, secrets SecretStore, cacheDir string) *Service {
	return &Service{store: store, secrets: secrets, cacheDir: cacheDir, clock: time.Now}
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
// keychain.
func (s *Service) AddAccount(ctx context.Context, a Account, password string) error {
	if a.ID == "" {
		a.ID = NewAccountID()
	}
	if err := a.Validate(); err != nil {
		return err
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
func (s *Service) UpdateAccount(ctx context.Context, a Account, password string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if _, ok, err := s.store.Account(ctx, a.ID); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
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

// RemoveAccount deletes an account's rows and its keychain entry.
func (s *Service) RemoveAccount(ctx context.Context, id string) error {
	if err := s.store.DeleteAccount(ctx, id); err != nil {
		return err
	}
	// A missing keychain entry is not an error; the account is gone
	// either way.
	_ = s.secrets.Delete(PasswordKey(id))
	return nil
}

// SetActive remembers the account the window shows.
func (s *Service) SetActive(ctx context.Context, id string) error {
	return s.store.SetActiveAccount(ctx, id)
}

// account loads an account and its password.
func (s *Service) account(ctx context.Context, id string) (Account, string, error) {
	a, ok, err := s.store.Account(ctx, id)
	if err != nil {
		return Account{}, "", err
	}
	if !ok {
		return Account{}, "", ErrNotFound
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
// sending anything.
func (s *Service) TestConnection(ctx context.Context, a Account, password string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if password == "" {
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
	client, err := s.smtpClient(a, password)
	if err != nil {
		return err
	}
	if err := client.DialWithContext(ctx); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return client.Close()
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

// SyncFolder fetches a folder's changes into the cache.
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
	result, err := s.moveOnServer(ctx, a, password, folder, uid, dest)
	if err != nil {
		_ = s.store.MarkDeleted(ctx, accountID, folder, uid, false)
		return MoveResult{}, err
	}
	return result, s.store.DeleteMessage(ctx, accountID, folder, uid)
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
	_, err = s.moveOnServer(ctx, a, password, from, uid, to)
	return err
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

package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"
)

// Creating, renaming, deleting and subscribing to folders. The server is
// told first; the cache changes only after it agreed, so a failed action
// leaves the folder list as it was. A system folder is never renamed or
// deleted.

// The errors folder actions report.
var (
	// ErrFolderName says a name is empty, has control characters, or
	// starts or ends with the server's hierarchy delimiter.
	ErrFolderName = errors.New("mail: invalid folder name")
	// ErrFolderSystem says the folder is a system one, which cannot be
	// renamed or deleted.
	ErrFolderSystem = errors.New("mail: the folder is a system folder")
	// ErrFolderExists says the account already has a folder of that name.
	ErrFolderExists = errors.New("mail: the folder exists")
)

// IsSystem reports whether the folder is one of the account's own: the
// inbox, or the folder a special use names. Such a folder cannot be
// renamed or deleted.
func (f Folder) IsSystem() bool {
	switch f.SpecialUse {
	case SpecialInbox, SpecialSent, SpecialDrafts, SpecialTrash, SpecialJunk, SpecialArchive, SpecialFlagged:
		return true
	}
	return false
}

// CreateFolder makes a folder on the server and in the cache. name is the
// server's own, with the hierarchy delimiter for a child folder.
func (s *Service) CreateFolder(ctx context.Context, accountID, name string) (Folder, error) {
	name = strings.TrimSpace(name)
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return Folder{}, err
	}
	if err := s.checkFolderName(ctx, accountID, name); err != nil {
		return Folder{}, err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, name); err != nil {
		return Folder{}, err
	} else if ok {
		return Folder{}, ErrFolderExists
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return Folder{}, err
	}
	defer c.close()
	if err := c.client.Create(name, nil).Wait(); err != nil {
		return Folder{}, fmt.Errorf("imap: create %s: %w", name, err)
	}
	// The user made it to use it: keep it in the sidebar. A server that
	// keeps no subscriptions lists it as subscribed all the same.
	_ = c.client.Subscribe(name).Wait()
	folders, err := s.listFolders(c)
	if err != nil {
		return Folder{}, err
	}
	if err := s.store.SaveFolders(ctx, accountID, folders); err != nil {
		return Folder{}, err
	}
	f, ok, err := s.store.Folder(ctx, accountID, name)
	if err != nil {
		return Folder{}, err
	}
	if !ok {
		return Folder{}, fmt.Errorf("imap: the server did not list %s after creating it", name)
	}
	return f, nil
}

// RenameFolder renames a folder on the server, with the folders below it,
// and moves their cache rows. Messages, cursors and subscriptions stay
// with them.
func (s *Service) RenameFolder(ctx context.Context, accountID, from, to string) error {
	to = strings.TrimSpace(to)
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	cached, ok, err := s.store.Folder(ctx, accountID, from)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if cached.IsSystem() {
		return ErrFolderSystem
	}
	if err := s.checkFolderName(ctx, accountID, to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if _, exists, err := s.store.Folder(ctx, accountID, to); err != nil {
		return err
	} else if exists {
		return ErrFolderExists
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.client.Rename(from, to, nil).Wait(); err != nil {
		return fmt.Errorf("imap: rename %s to %s: %w", from, to, err)
	}
	if err := s.store.RenameFolder(ctx, accountID, from, to, cached.Delimiter); err != nil {
		return err
	}
	// A server may normalize a name; LIST says what it kept.
	if folders, err := s.listFolders(c); err == nil {
		_ = s.store.SaveFolders(ctx, accountID, folders)
	}
	return nil
}

// DeleteFolder deletes a folder on the server, with the mail in it, and
// its cached messages. It never deletes a system folder, and the caller
// asks the user first: this is mail going away, not a move.
func (s *Service) DeleteFolder(ctx context.Context, accountID, name string) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	cached, ok, err := s.store.Folder(ctx, accountID, name)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if cached.IsSystem() {
		return ErrFolderSystem
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	if err := c.client.Delete(name).Wait(); err != nil {
		return fmt.Errorf("imap: delete %s: %w", name, err)
	}
	if err := s.store.DeleteFolder(ctx, accountID, name); err != nil {
		return err
	}
	// The folder's mail is gone from the cache with it.
	_ = s.SweepCache(ctx)
	return nil
}

// SetFolderSubscribed keeps a folder in the sidebar or hides it, on the
// server where it distinguishes subscriptions.
func (s *Service) SetFolderSubscribed(ctx context.Context, accountID, name string, on bool) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, name); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	var cmd *imapclient.Command
	if on {
		cmd = c.client.Subscribe(name)
	} else {
		cmd = c.client.Unsubscribe(name)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("imap: subscribe %s: %w", name, err)
	}
	return s.store.SetFolderSubscribed(ctx, accountID, name, on)
}

// checkFolderName refuses a name that is not one, and one the account
// already has.
func (s *Service) checkFolderName(ctx context.Context, accountID, name string) error {
	if name == "" {
		return ErrFolderName
	}
	delimiter := "/"
	if folders, err := s.store.Folders(ctx, accountID); err == nil {
		for _, f := range folders {
			if f.Delimiter != "" {
				delimiter = f.Delimiter
			}
		}
	}
	if strings.HasPrefix(name, delimiter) || strings.HasSuffix(name, delimiter) {
		return ErrFolderName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ErrFolderName
		}
	}
	return nil
}

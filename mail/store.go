package mail

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a cached row does not exist.
var ErrNotFound = errors.New("mail: not found")

// Store is the engine's cache: accounts and their settings, folders and
// their sync cursors, message headers and bodies, attachments, and the
// outbox. The engine is the only writer; the UI reads through Service.
type Store interface {
	// Accounts and settings.
	Accounts(ctx context.Context) ([]Account, error)
	Account(ctx context.Context, id string) (Account, bool, error)
	SaveAccount(ctx context.Context, a Account) error
	DeleteAccount(ctx context.Context, id string) error
	ActiveAccount(ctx context.Context) (string, error)
	SetActiveAccount(ctx context.Context, id string) error

	// Folders.
	Folders(ctx context.Context, accountID string) ([]Folder, error)
	Folder(ctx context.Context, accountID, name string) (Folder, bool, error)
	SaveFolders(ctx context.Context, accountID string, folders []Folder) error

	// Messages.
	ListMessages(ctx context.Context, accountID, folder, query string, limit int) ([]Summary, error)
	GetMessage(ctx context.Context, accountID, folder string, uid UID) (Message, bool, error)
	SaveMessage(ctx context.Context, accountID, folder string, m Message) error
	ApplySync(ctx context.Context, accountID string, batch SyncBatch) error
	SetFlag(ctx context.Context, accountID, folder string, uid UID, flag Flag, on bool) error
	MarkDeleted(ctx context.Context, accountID, folder string, uid UID, deleted bool) error
	DeleteMessage(ctx context.Context, accountID, folder string, uid UID) error
	SetAttachmentCache(ctx context.Context, accountID, folder string, uid UID, part, hash, path string) error

	// Outbox.
	EnqueueOutbox(ctx context.Context, p PendingSend) error
	Outbox(ctx context.Context, id string) (PendingSend, error)
	PendingSends(ctx context.Context, accountID string) ([]PendingSend, error)
	SetOutboxState(ctx context.Context, id, state, lastError string) error
}

// SecretStore keeps account passwords. production uses mygo.Secrets, tests
// an in-memory map.
type SecretStore interface {
	Set(key string, secret []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// PasswordKey is the keychain key of an account's password.
func PasswordKey(accountID string) string { return "account:" + accountID + ":password" }

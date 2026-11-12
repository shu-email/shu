package mail

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a cached row does not exist.
var ErrNotFound = errors.New("mail: not found")

// Store is the engine's cache: accounts and their settings, folders and
// their sync cursors, message headers and bodies, attachments, the outbox
// and the drafts. The engine is the only writer; the UI reads through
// Service.
type Store interface {
	// Accounts and settings.
	Accounts(ctx context.Context) ([]Account, error)
	Account(ctx context.Context, id string) (Account, bool, error)
	SaveAccount(ctx context.Context, a Account) error
	DeleteAccount(ctx context.Context, id string) error
	ActiveAccount(ctx context.Context) (string, error)
	SetActiveAccount(ctx context.Context, id string) error
	// Setting and SetSetting keep small values that are no secrets. The
	// one under MutedKey goes when its account is deleted.
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error

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
	// TakeNew returns the unread messages of a folder above its
	// watermark, newest first, and raises the watermark over them in the
	// same step: a message is returned once, ever. The watermark is the
	// highest UID announced, under the UIDVALIDITY it belongs to; a
	// folder that has none yet, or whose UIDVALIDITY changed, gets it set
	// to the highest UID cached and returns nothing. ErrNotFound when the
	// folder is not known.
	TakeNew(ctx context.Context, accountID, folder string) ([]Summary, error)

	// Conversations. Every message written is put in one, with the
	// messages of any folder of its account it shares a key with (see
	// ThreadKeys); a message that links two conversations makes them
	// one. A scope is a folder's name, "starred" for the flagged
	// messages, or "" for the whole account, tombstones left out.
	// ListConversations returns a row for each conversation with a
	// message in the scope, newest first, from that scope's messages; a
	// query keeps those any message of which matches, in any folder, and
	// names them. ConversationMessages returns a conversation's messages
	// in the scope, oldest first.
	ListConversations(ctx context.Context, accountID, folder, query string, limit int) ([]Conversation, error)
	ConversationMessages(ctx context.Context, accountID, folder string, thread ThreadID) ([]Summary, error)

	// Outbox.
	EnqueueOutbox(ctx context.Context, p PendingSend) error
	Outbox(ctx context.Context, id string) (PendingSend, error)
	PendingSends(ctx context.Context, accountID string) ([]PendingSend, error)
	SetOutboxState(ctx context.Context, id, state, lastError string) error

	// Drafts. Drafts lists an account's rows newest first, those awaiting
	// deletion on the server included. SaveDraft creates a row as given,
	// at revision 1 and with its attachments, or writes the text of an
	// existing one and raises its revision, leaving its attachments as
	// they are; it returns the stored row. SetDraftAttachments replaces
	// the attachments of an existing row and raises its revision.
	// SetDraftSync writes the sync state and the Message-ID of an
	// existing row, never its content or revision. Both report
	// ErrNotFound when the row is gone.
	Drafts(ctx context.Context, accountID string) ([]Draft, error)
	Draft(ctx context.Context, id string) (Draft, bool, error)
	SaveDraft(ctx context.Context, d Draft) (Draft, error)
	SetDraftAttachments(ctx context.Context, id string, list []Attachment, updatedAt time.Time) (Draft, error)
	SetDraftSync(ctx context.Context, d Draft) error
	DeleteDraft(ctx context.Context, id string) error
	// DraftFileRefs lists the content hashes of the attachment files in
	// use, over all accounts: those of the drafts neither discarded nor
	// sent, and those of the outbox entries still to deliver.
	DraftFileRefs(ctx context.Context) ([]string, error)
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

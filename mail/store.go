package mail

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a cached row does not exist.
var ErrNotFound = errors.New("mail: not found")

// ErrFolderReset is a write of older mail into a folder whose UIDVALIDITY
// changed since the mail was fetched: its UIDs name other messages now.
var ErrFolderReset = errors.New("mail: the folder was reset meanwhile")

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
	// Setting and SetSetting keep small values that are no secrets. Those
	// under MutedKey and SignatureKey go when their account is deleted.
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error

	// Identities. Identities lists those added to an account, in the
	// order they were; its own address is the account's and no row.
	// SaveIdentity adds one after them, or changes the name and address
	// of one there is. DeleteIdentity removes one, and in the same step
	// has the drafts written from it go from the account's own address,
	// an edit their server copies are to follow. They go with their
	// account.
	Identities(ctx context.Context, accountID string) ([]Identity, error)
	SaveIdentity(ctx context.Context, id Identity) error
	DeleteIdentity(ctx context.Context, id string) error

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

	// Pages of a Scope, across accounts as well: the rows newest first,
	// after a cursor, each with its account and with where a Search
	// found its terms. ConversationPage lists a conversation when it has
	// a message in the scope and any of its messages, in any folder,
	// matches the whole search; its row adds up the scope's messages.
	MessagePage(ctx context.Context, q ListQuery) (MessagePage, error)
	ConversationPage(ctx context.Context, q ListQuery) (ConversationPage, error)

	// Labels (see Label), which go with their account. Every message a
	// read returns has its labels: those its row has as the server keeps
	// them, which ApplySync, ApplyOlder and SaveMessage write from the
	// Keywords of the messages and flags they write (a new row only, for
	// SaveMessage), a keyword no label has yet becoming one named
	// LabelName; and those kept on this device by its LabelKey, wherever
	// it is. ApplySync leaves out a keyword whose label DeleteLabel
	// deleted at or after the batch's Folder.SyncedAt: the sync read it
	// before the deletion took it off the server.
	//
	// Labels lists an account's labels in their order, each with the
	// unread messages of LabelScope that have it and whether it is kept
	// on this device only (KeptLocally). CreateLabel adds one after
	// them, the palette's next color when it has none: ErrLabelExists
	// when the account has one of the same name in any case
	// (LabelNameKey), and a keyword another label has is made free
	// (FreeKeyword). UpdateLabel changes the name and color ("" keeps
	// it) of one and never its keyword. DeleteLabel removes one, from
	// every message here too, at a time. SetLabel puts a label on cached
	// messages of a folder or takes it off: as the server keeps it, on
	// their rows, or with local set on this device, by their LabelKey; it
	// returns the UIDs of the messages it changed, and skips those not
	// cached. LabelRefs lists the cached messages that have a label as
	// the server keeps it. Each returns ErrNotFound for a label or folder
	// the account has not.
	Labels(ctx context.Context, accountID string) ([]Label, error)
	CreateLabel(ctx context.Context, l Label) (Label, error)
	UpdateLabel(ctx context.Context, l Label) (Label, error)
	DeleteLabel(ctx context.Context, accountID string, id LabelID, at time.Time) error
	SetLabel(ctx context.Context, accountID string, id LabelID, folder string, uids []UID, on, local bool) ([]UID, error)
	LabelRefs(ctx context.Context, accountID string, id LabelID) ([]MessageRef, error)
	// SetPermanentFlags records what a folder's PERMANENTFLAGS said as it
	// was opened to change (Folder.PermanentFlags). ApplySync records a
	// batch's when its Folder has them, and SaveFolders keeps them.
	SetPermanentFlags(ctx context.Context, accountID, folder string, flags []string) error

	// Older mail. FolderCache counts what the cache holds of a folder.
	// FolderUIDs lists the UIDs it holds there, those being moved away
	// included. ApplyOlder writes messages a sync left out, threading
	// them as any, and the folder's Complete, and nothing else of the
	// folder: not its cursor, counts or watermark. It returns
	// ErrFolderReset, writing nothing, when the folder's UIDVALIDITY is
	// no longer the batch's.
	FolderCache(ctx context.Context, accountID, folder string) (FolderCache, error)
	FolderUIDs(ctx context.Context, accountID, folder string) ([]UID, error)
	ApplyOlder(ctx context.Context, accountID string, b OlderBatch) error

	// Outbox. PendingSends lists an account's entries that are neither
	// delivered, cancelled nor scheduled. ClaimOutbox moves an entry from
	// one state to another if it is in the first, and reports whether it
	// was: of callers that claim the same entry from the same state, one
	// gets true. ScheduledSends lists the scheduled entries of every
	// account, the soonest due first.
	EnqueueOutbox(ctx context.Context, p PendingSend) error
	Outbox(ctx context.Context, id string) (PendingSend, error)
	PendingSends(ctx context.Context, accountID string) ([]PendingSend, error)
	SetOutboxState(ctx context.Context, id, state, lastError string) error
	ClaimOutbox(ctx context.Context, id, from, to string) (bool, error)
	ScheduledSends(ctx context.Context) ([]PendingSend, error)

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

	// The address book, one contact per address whatever its case, across
	// the accounts. The store keeps it as it writes mail, by the rules of
	// ContactState: of each message ApplySync, ApplyOlder and SaveMessage
	// cache outside the Junk and Drafts folders, MessageContacts, and when
	// SetOutboxState records an entry as sent, SentContacts of its
	// envelope at the time it was due, each in the transaction of that
	// write, and each message once, by ContactMessageKey. Contacts reads
	// it (see ContactQuery). AddContact adds an address under the user's
	// name for it, ErrContactExists when it is in the book, ErrContactOwn
	// when it is the user's; RenameContact, FavoriteContact and
	// DeleteContact change a contact of the book, ErrNotFound when the
	// address is not in it. The edits return the contact as they left it.
	Contacts(ctx context.Context, q ContactQuery) ([]Contact, error)
	AddContact(ctx context.Context, name, email string) (Contact, error)
	RenameContact(ctx context.Context, email, name string) (Contact, error)
	FavoriteContact(ctx context.Context, email string, on bool) (Contact, error)
	DeleteContact(ctx context.Context, email string, at time.Time) error
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

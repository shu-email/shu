package main

import (
	"context"

	"shu/mail"
)

// mailService is what the window needs from the engine. Production passes
// *mail.Service; tests pass a fake that returns immediately.
type mailService interface {
	Accounts(ctx context.Context) ([]mail.Account, string, error)
	AddAccount(ctx context.Context, a mail.Account, password string) error
	UpdateAccount(ctx context.Context, a mail.Account, password string) error
	RemoveAccount(ctx context.Context, id string) error
	SetActive(ctx context.Context, id string) error
	TestConnection(ctx context.Context, a mail.Account, password string) error

	CachedFolders(ctx context.Context, accountID string) ([]mail.Folder, error)
	CachedMessages(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Summary, error)
	CachedMessage(ctx context.Context, accountID, folder string, uid mail.UID) (mail.FetchResult, bool, error)
	// Conversations, from the cache alone. A conversation-wide action acts
	// on exactly what CachedConversation returns for the list's scope; a
	// read that returns nothing is of a conversation that merged into
	// another, or left, since the list was read.
	CachedConversations(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Conversation, error)
	CachedConversation(ctx context.Context, accountID, folder string, thread mail.ThreadID) ([]mail.Summary, error)

	SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error)
	SyncFolder(ctx context.Context, accountID, folder string, limit int) error
	FetchMessage(ctx context.Context, accountID, folder string, uid mail.UID, markSeen bool) (mail.FetchResult, error)

	SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error
	SetFlags(ctx context.Context, accountID string, msgs []mail.MessageRef, flag mail.Flag, on bool) error
	Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) (mail.MoveResult, error)
	MoveBack(ctx context.Context, accountID, from, to string, uid mail.UID) error
	MoveMessages(ctx context.Context, accountID string, msgs []mail.MessageRef, dest string) ([]mail.MoveResult, error)
	MoveBackMessages(ctx context.Context, accountID string, moved []mail.MoveResult) error
	Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error)
	RetryPending(ctx context.Context, id string) (mail.SendResult, error)
	PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error)
	Attachment(ctx context.Context, accountID, folder string, uid mail.UID, part string) (mail.Attachment, []byte, error)

	// Drafts. SaveDraft never needs the network; SyncDrafts is the step
	// that does, and may fail and run again.
	DraftList(ctx context.Context, accountID, query string, limit int) ([]mail.Summary, error)
	Draft(ctx context.Context, id string) (mail.Draft, bool, error)
	SaveDraft(ctx context.Context, d mail.Draft) (mail.Draft, error)
	SyncDrafts(ctx context.Context, accountID string) (mail.DraftSyncResult, error)
	// SyncDraftEdits is the sync a pause in typing asks for: it leaves
	// the drafts with large attachments for SyncDrafts.
	SyncDraftEdits(ctx context.Context, accountID string) (mail.DraftSyncResult, error)
	// AttachFile copies a file into a draft, which it creates if it is
	// not stored yet. Only it and RemoveAttachment change what a draft
	// has attached.
	AttachFile(ctx context.Context, d mail.Draft, path string) (mail.Draft, mail.Attachment, error)
	RemoveAttachment(ctx context.Context, id string, att mail.Attachment) (mail.Draft, error)
	SweepDraftFiles(ctx context.Context) error
	DiscardDraft(ctx context.Context, id string) error
	AdoptDraft(ctx context.Context, accountID, folder string, uid mail.UID) (mail.Draft, error)
	DismissDraftConflict(ctx context.Context, id string) error

	// Watching for new mail. Watch and Unwatch return when the account's
	// earlier watcher has ended, so jobs call them; report is called
	// from the watcher's goroutine. Nudge and SetOnline return at once.
	Watch(accountID string, report func(mail.WatchEvent))
	Unwatch(accountID string)
	Nudge()
	SetOnline(online bool)
	Settings(ctx context.Context) (mail.Settings, error)
	SaveSettings(ctx context.Context, s mail.Settings) error
}

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

	SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error)
	SyncFolder(ctx context.Context, accountID, folder string, limit int) error
	FetchMessage(ctx context.Context, accountID, folder string, uid mail.UID, markSeen bool) (mail.FetchResult, error)

	SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error
	Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) (mail.MoveResult, error)
	MoveBack(ctx context.Context, accountID, from, to string, uid mail.UID) error
	Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error)
	RetryPending(ctx context.Context, id string) (mail.SendResult, error)
	PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error)
	Attachment(ctx context.Context, accountID, folder string, uid mail.UID, part string) (mail.Attachment, []byte, error)
}

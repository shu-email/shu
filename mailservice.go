package main

import (
	"context"
	"io"
	"net/http"
	"time"

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

	// Signing in with Google or Microsoft. OAuthConfigured says whether
	// this build can, at once. A sign-in lasts as long as the user takes
	// in the browser and ends with ctx, so jobs call AddOAuthAccount and
	// SignInAgain; NeedsSignIn reads the keychain only.
	OAuthConfigured(provider string) bool
	AddOAuthAccount(ctx context.Context, provider, name string, prompt mail.OAuthPrompt) (mail.Account, error)
	SignInAgain(ctx context.Context, accountID string, prompt mail.OAuthPrompt) error
	SignOut(ctx context.Context, accountID string) error
	NeedsSignIn(ctx context.Context, accountID string) (bool, error)

	// Import and export, and the configuration backup (transfer.go).
	ImportMbox(ctx context.Context, accountID, folder string, r io.Reader, progress func(mail.ImportResult)) (mail.ImportResult, error)
	ImportEML(ctx context.Context, accountID, folder string, raw []byte) (mail.ImportResult, error)
	Export(ctx context.Context, req mail.ExportRequest, w io.Writer, progress func(mail.ExportResult)) (mail.ExportResult, error)
	BackupConfig(ctx context.Context) ([]byte, error)
	RestoreConfig(ctx context.Context, data []byte) (int, error)

	NetworkSettings(ctx context.Context) (mail.NetworkSettings, bool, error)
	SaveNetworkSettings(ctx context.Context, set mail.NetworkSettings, password string, setPassword bool) error
	TestNetwork(ctx context.Context, accountID string) error
	HTTPClient(ctx context.Context) (*http.Client, error)

	CachedFolders(ctx context.Context, accountID string) ([]mail.Folder, error)
	// Folder actions reach the server first and change the cache only
	// once it agreed; a system folder is never renamed or deleted.
	CreateFolder(ctx context.Context, accountID, name string) (mail.Folder, error)
	RenameFolder(ctx context.Context, accountID, from, to string) error
	DeleteFolder(ctx context.Context, accountID, name string) error
	SetFolderSubscribed(ctx context.Context, accountID, name string, on bool) error
	CachedMessage(ctx context.Context, accountID, folder string, uid mail.UID) (mail.FetchResult, bool, error)
	// The list reads the cache a page at a time, newest first, each page
	// after the cursor the one before it ended at.
	MessagePage(ctx context.Context, q mail.ListQuery) (mail.MessagePage, error)
	ConversationPage(ctx context.Context, q mail.ListQuery) (mail.ConversationPage, error)
	// Conversations, from the cache alone. A conversation-wide action acts
	// on exactly what ScopedConversation returns for the list's scope; a
	// read that returns nothing is of a conversation that merged into
	// another, or left, since the list was read. CachedConversation with
	// no folder reads a conversation whole, to show it.
	ScopedConversation(ctx context.Context, accountID string, scope mail.Scope, thread mail.ThreadID) ([]mail.Summary, error)
	CachedConversation(ctx context.Context, accountID, folder string, thread mail.ThreadID) ([]mail.Summary, error)
	// FolderCache says how much of a folder is on this device, and
	// FetchOlder fetches what the cache lacks of it, n of zero being the
	// settings' first-sync window. Only a user's action fetches.
	FolderCache(ctx context.Context, accountID, folder string) (mail.FolderCache, error)
	FetchOlder(ctx context.Context, accountID, folder string, n int) (mail.OlderResult, error)

	SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error)
	// SyncFolder with a limit of zero caches the settings' first-sync
	// window of a folder synced for the first time.
	SyncFolder(ctx context.Context, accountID, folder string, limit int) error
	FetchMessage(ctx context.Context, accountID, folder string, uid mail.UID, markSeen bool) (mail.FetchResult, error)
	// Remote image policy, by sender: pictures an HTML message names on
	// the network stay blocked until the user allows them.
	RemoteImages(ctx context.Context) (map[string]bool, error)
	SetRemoteImages(ctx context.Context, from mail.Address, on bool) error

	SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error
	SetFlags(ctx context.Context, accountID string, msgs []mail.MessageRef, flag mail.Flag, on bool) error
	// UndoFlagChange and UndoLabelChange take a change back, as the undo
	// banner offers: a change still waiting in the queue is dropped
	// before the server makes it, and one it has is set back.
	UndoFlagChange(ctx context.Context, accountID string, msgs []mail.MessageRef, flag mail.Flag, on bool) error
	UndoLabelChange(ctx context.Context, accountID string, msgs []mail.MessageRef, id mail.LabelID, on bool) error
	Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) (mail.MoveResult, error)
	MoveBack(ctx context.Context, accountID, from, to string, uid mail.UID) error
	MoveMessages(ctx context.Context, accountID string, msgs []mail.MessageRef, dest string) ([]mail.MoveResult, error)
	MoveBackMessages(ctx context.Context, accountID string, moved []mail.MoveResult) error
	// Reminders are this device's: a conversation taken out of the inbox
	// until a time, when DeliverReminders brings it back. SetReminder
	// moves the conversation to the archive, and returns the moves it
	// made for the undo.
	Reminders(ctx context.Context, accountID string) ([]mail.Reminder, error)
	SetReminder(ctx context.Context, accountID string, thread mail.ThreadID, at time.Time) (mail.Reminder, []mail.MoveResult, error)
	RemoveReminder(ctx context.Context, accountID string, thread mail.ThreadID) error
	RestoreReminder(ctx context.Context, r mail.Reminder) (mail.Reminder, error)
	DeliverReminders(ctx context.Context) ([]mail.Reminder, error)
	// Follow Up is a list derived from the cache: the conversations whose
	// newest message, outside the drafts, junk and trash, is one the user
	// sent three days to a month ago and no one answered. FollowUpCount is
	// how many there are; DismissFollowUp takes one off until a newer
	// message of it is cached. No server hears of either.
	FollowUpCount(ctx context.Context, accountID string) (int, error)
	DismissFollowUp(ctx context.Context, accountID string, thread mail.ThreadID) error
	Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error)
	RetryPending(ctx context.Context, id string) (mail.SendResult, error)
	PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error)
	// Sending with a delay. Schedule writes the message to the outbox and
	// sends nothing; SendScheduled waits until it is due and sends it, so
	// a job that lasts as long as the app calls it; Unsend takes it back
	// until then and returns its draft. DeliverScheduled sends what an
	// earlier run left waiting, once, as the app starts.
	Schedule(ctx context.Context, accountID string, out mail.Outgoing, delay time.Duration) (mail.PendingSend, error)
	SendScheduled(ctx context.Context, id string) (mail.SendResult, error)
	Unsend(ctx context.Context, id string) (mail.Draft, error)
	DeliverScheduled(ctx context.Context) ([]mail.ScheduledResult, error)

	// How messages are written: the settings of every account, and each
	// account's signature and the addresses it sends from, its own first.
	ComposeSettings(ctx context.Context) (mail.ComposeSettings, error)
	SaveComposeSettings(ctx context.Context, set mail.ComposeSettings) error
	Signature(ctx context.Context, accountID string) (mail.Signature, error)
	SaveSignature(ctx context.Context, accountID string, sig mail.Signature) error
	Identities(ctx context.Context, accountID string) ([]mail.Identity, error)
	AddIdentity(ctx context.Context, accountID, name, email string) (mail.Identity, error)
	UpdateIdentity(ctx context.Context, id mail.Identity) (mail.Identity, error)
	DeleteIdentity(ctx context.Context, accountID, id string) error
	Attachment(ctx context.Context, accountID, folder string, uid mail.UID, part string) (mail.Attachment, []byte, error)
	// The parsed cache. PrepareCache runs as the app starts: it drops what
	// an older build wrote when the cache version changed, and sweeps the
	// files no row names. TempFile writes bytes to a temporary file of the
	// cache for the window to open.
	PrepareCache(ctx context.Context) error
	TempFile(ext string, data []byte) (string, error)

	// The address book, kept on this device alone. Suggest reads the
	// contacts a recipient being typed may be, best first, leaving out
	// the addresses exclude holds; Contacts reads a page of the book;
	// the edits show in both at once. CheckRecipients reads a recipient
	// field as a send takes it, and says what of it is no address.
	Suggest(ctx context.Context, typed string, exclude []string, limit int) ([]mail.Contact, error)
	Contacts(ctx context.Context, search string, offset, limit int) (mail.ContactPage, error)
	AddContact(ctx context.Context, name, email string) (mail.Contact, error)
	RenameContact(ctx context.Context, email, name string) (mail.Contact, error)
	FavoriteContact(ctx context.Context, email string, on bool) (mail.Contact, error)
	DeleteContact(ctx context.Context, email string) error
	CheckRecipients(ctx context.Context, field string) (mail.RecipientCheck, error)

	// Labels, an account's each. Labels reads them from the cache, with
	// their unread counts and whether each is kept on this device only;
	// a label's name and color are this device's. SetLabel puts a label
	// on messages of one account, or takes it off, the cache first, and
	// keeps it on this device where their folder keeps no such keyword.
	// DeleteLabel, which the window asks about first, takes it off every
	// message, on the server too, moving and deleting none; it may need
	// to run again (mail.ErrLabelPartlyRemoved).
	Labels(ctx context.Context, accountID string) ([]mail.Label, error)
	CreateLabel(ctx context.Context, accountID, name string, color mail.LabelColor) (mail.Label, error)
	UpdateLabel(ctx context.Context, accountID string, id mail.LabelID, name string, color mail.LabelColor) (mail.Label, error)
	SetLabel(ctx context.Context, accountID string, msgs []mail.MessageRef, id mail.LabelID, on bool) error
	DeleteLabel(ctx context.Context, accountID string, id mail.LabelID) error

	// Smart mailboxes, searches the user saved. SmartMailboxes reads an
	// account's from the cache; SaveSmartMailbox adds or changes one, and
	// DeleteSmartMailbox drops one, touching no mail. CountUnread counts
	// the unread messages a list query reads, which a mailbox's badge
	// shows.
	SmartMailboxes(ctx context.Context, accountID string) ([]mail.SmartMailbox, error)
	SaveSmartMailbox(ctx context.Context, m mail.SmartMailbox) (mail.SmartMailbox, error)
	DeleteSmartMailbox(ctx context.Context, accountID string, id int64) error
	CountUnread(ctx context.Context, q mail.ListQuery) (int, error)

	// Discovery finds the servers of an address's domain for the account
	// form: what the user entered for it, a preset, what an earlier
	// lookup kept, and then the configuration endpoints and DNS, with no
	// password sent and every step bounded. RememberDiscovery keeps the
	// servers the user entered, which then win over later lookups.
	Discover(ctx context.Context, email string) (mail.Discovery, error)
	RememberDiscovery(ctx context.Context, email string, imap, smtp mail.Server) error

	// Rules, which sort the mail that arrives, in order, from the cache.
	// CheckRule says what SaveRule would refuse, as a *mail.RuleError that
	// names the condition or action at fault; saving touches no mail.
	// PreviewRule lists the cached messages a rule, saved or not, matches
	// now, and changes nothing; RunRule runs a saved rule once over what
	// its preview found, which only the user's action asks for. The log
	// is read newest first, and its entries, or all of a rule's, undone.
	// OnRuleActivity's report is called from a goroutine of the engine
	// after actions were applied in the background.
	Rules(ctx context.Context) ([]mail.Rule, error)
	CheckRule(ctx context.Context, r mail.Rule) error
	SaveRule(ctx context.Context, r mail.Rule) (mail.Rule, error)
	PauseRule(ctx context.Context, id mail.RuleID, paused bool) (mail.Rule, error)
	DeleteRule(ctx context.Context, id mail.RuleID) error
	MoveRule(ctx context.Context, id mail.RuleID, index int) error
	PreviewRule(ctx context.Context, r mail.Rule, offset, limit int) (mail.RulePreview, error)
	RunRule(ctx context.Context, id mail.RuleID, targets []mail.RuleTarget) ([]mail.RuleActivity, error)
	RuleActivity(ctx context.Context, q mail.ActivityQuery) (mail.ActivityPage, error)
	UndoActivity(ctx context.Context, id mail.ActivityID) error
	UndoRule(ctx context.Context, id mail.RuleID) (int, error)
	OnRuleActivity(report func(accountID string))

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
	// AttachFromMessage copies a received message's attachments into a
	// draft, as a forward of it carries them, and reports each it could
	// not copy without failing the others.
	AttachFromMessage(ctx context.Context, d mail.Draft, accountID, folder string, uid mail.UID) (mail.Draft, []mail.AttachFailure, error)
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
	// RetryFailedActions tries the account's queued actions the server
	// refused once more, as the footer offers, and returns how many went
	// back to the queue.
	RetryFailedActions(ctx context.Context, accountID string) (int, error)

	// Leaving a mailing list, and blocking a sender. UnsubscribeOffer reads
	// what a message's headers say the list offers, through the cache
	// alone; Unsubscribe makes the one the user chose, and keeps it for
	// this sender. Blocks are this device's: the sender's mail is filed as
	// it arrives, and mail already here stays.
	UnsubscribeOffer(ctx context.Context, accountID, folder string, uid mail.UID) (mail.UnsubscribeOffer, error)
	Unsubscribe(ctx context.Context, accountID, folder string, uid mail.UID, method mail.UnsubscribeMethod, dest string) (mail.UnsubscribeRecord, error)
	Unsubscribed(ctx context.Context, accountID, email string) (*mail.UnsubscribeRecord, error)
	Unsubscribes(ctx context.Context, accountID string) ([]mail.UnsubscribeRecord, error)
	BlockSender(ctx context.Context, accountID, address string, domain bool, folder string) (mail.BlockedSender, error)
	UnblockSender(ctx context.Context, accountID, value string, domain bool) error
	BlockedSenders(ctx context.Context, accountID string) ([]mail.BlockedSender, error)

	// CheckIntegrity reports what the cache holds out of step with what it
	// is derived from, and changes nothing (see mail.IntegrityReport).
	CheckIntegrity(ctx context.Context) (mail.IntegrityReport, error)

	Settings(ctx context.Context) (mail.Settings, error)
	SaveSettings(ctx context.Context, s mail.Settings) error
}

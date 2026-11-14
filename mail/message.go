// Package mail is Shu's mail engine: accounts, the IMAP and SMTP protocols,
// MIME parsing and the cache-first service the UI calls. It never imports
// the UI or a storage driver; a Store (see store/) keeps what it syncs.
package mail

import (
	"net/mail"
	"strings"
	"time"
)

// UID is a message's IMAP unique identifier within a folder.
type UID uint32

// Address is a message address.
type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// String writes the address for a text field, quoting the name only when
// it needs it.
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	if strings.ContainsAny(a.Name, `"<>(),;:\[]@`) {
		return (&mail.Address{Name: a.Name, Address: a.Email}).String()
	}
	return a.Name + " <" + a.Email + ">"
}

// Folder is a mailbox folder, with its sync cursor.
type Folder struct {
	Name        string    `json:"name"`
	Delimiter   string    `json:"delimiter,omitempty"`
	SpecialUse  string    `json:"specialUse,omitempty"`
	Unread      int       `json:"unread"`
	Total       int       `json:"total"`
	UIDValidity uint32    `json:"uidValidity"`
	LastUID     UID       `json:"lastUID"`
	SyncedAt    time.Time `json:"syncedAt,omitempty"`
	// Complete reports that the cache lacks none of the folder's messages
	// up to LastUID: a sync read the folder whole, or FetchOlder reached
	// its oldest message. A sync keeps it; a LIST says nothing of it.
	Complete bool `json:"complete,omitempty"`
	// Subscribed reports that the user wants the folder in the sidebar,
	// as IMAP's subscription says. A server without LIST-EXTENDED keeps
	// every folder subscribed.
	Subscribed bool `json:"subscribed"`
	// PermanentFlags is what the folder's PERMANENTFLAGS said the last
	// time it was opened to change, which tells whether it keeps labels
	// (KeepsKeyword): the flags it keeps, \* for any keyword. It is nil
	// until then; a folder opened read-only says nothing of it.
	PermanentFlags []string `json:"permanentFlags"`
}

// IsInbox reports whether the folder is the inbox.
func (f Folder) IsInbox() bool { return f.SpecialUse == SpecialInbox }

// Summary is the header of a message, what the list shows.
type Summary struct {
	// Account is the account the message is in. Every list read sets it,
	// so a row of a list across accounts is acted on in its own.
	Account        string    `json:"account,omitempty"`
	UID            UID       `json:"uid"`
	Folder         string    `json:"folder,omitempty"`
	MessageID      string    `json:"messageID,omitempty"`
	InReplyTo      string    `json:"inReplyTo,omitempty"`
	References     []string  `json:"references,omitempty"`
	From           Address   `json:"from"`
	To             []Address `json:"to,omitempty"`
	Cc             []Address `json:"cc,omitempty"`
	ReplyTo        []Address `json:"replyTo,omitempty"`
	Subject        string    `json:"subject"`
	Preview        string    `json:"preview"`
	Date           time.Time `json:"date"`
	Size           int64     `json:"size"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	HasAttachments bool      `json:"hasAttachments"`
	// ThreadIndex is an Outlook Thread-Index header, which ThreadKeys
	// reads. Thread is the conversation the cache put the message in.
	ThreadIndex string   `json:"threadIndex,omitempty"`
	Thread      ThreadID `json:"thread,omitempty"`
	// DraftID is set on the row of a local draft, which has no folder or
	// UID of its own: the draft is on this device, whatever the server
	// holds of it.
	DraftID string `json:"draftID,omitempty"`
	// Matched is set on a message a search found: the parts of it its
	// terms were found in.
	Matched Field `json:"matched,omitempty"`
	// Labels are the labels the message has, in the order of its
	// account's. LabelsLocal reports that its folder keeps no new keyword
	// on the server, so a label put on it is kept on this device only,
	// unless the folder names that label's keyword among those it keeps.
	Labels      []MessageLabel `json:"labels,omitempty"`
	LabelsLocal bool           `json:"labelsLocal,omitempty"`
}

// Message is a summary with the parts the reading pane needs.
type Message struct {
	Summary
	BodyText      string       `json:"bodyText,omitempty"`
	BodyHTML      string       `json:"bodyHTML,omitempty"`
	BodyTruncated bool         `json:"bodyTruncated,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	// RawHeaders is the header lines the cache keeps of the message as
	// they came, which no other field holds: its Delivered-To, which says
	// which of the user's addresses it reached (see DeliveredTo).
	RawHeaders string `json:"rawHeaders,omitempty"`
	// Security is what those headers said of the message when they were
	// kept (see Security), read once and not again.
	Security *Security `json:"-"`
	CachedAt time.Time `json:"cachedAt,omitempty"`
	// BodyCached reports whether the body is in the cache; a summary
	// whose body was never fetched has none.
	BodyCached bool `json:"bodyCached,omitempty"`
	// Calendar is the meeting invitation the message carries, when it
	// has one; CalendarRaw is its part as it came, whether it parsed or
	// not, so an unreadable invitation can say so.
	Calendar    *CalendarInvite `json:"calendar,omitempty"`
	CalendarRaw string          `json:"-"`
	// Keywords are the flags of the user's that a fetch read of the
	// message on the server, which the cache keeps as its labels; nil
	// when it read none, as a message not fetched from a server.
	Keywords []string `json:"-"`
}

// Attachment is one part of a message. Data is only in memory; the cache
// keeps the bytes on disk and CachePath points at them.
//
// On a draft, or on a message to send, it is a file the user attached:
// its bytes are in the draft file store under ContentHash, and Part,
// CachePath and Data are unused.
type Attachment struct {
	Part        string `json:"part"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	CachePath   string `json:"cachePath,omitempty"`
	Data        []byte `json:"-"`
}

// FetchResult is a fetched message, and whether marking it read failed.
// The failure never blocks showing the message.
type FetchResult struct {
	Message Message
	SeenErr error
}

// Flag is an IMAP flag the app changes.
type Flag string

// The flags the app uses.
const (
	FlagSeen    Flag = `\Seen`
	FlagFlagged Flag = `\Flagged`
	FlagDeleted Flag = `\Deleted`
)

// Special-use attributes, as IMAP calls them.
const (
	SpecialInbox   = `\Inbox`
	SpecialSent    = `\Sent`
	SpecialDrafts  = `\Drafts`
	SpecialTrash   = `\Trash`
	SpecialJunk    = `\Junk`
	SpecialArchive = `\Archive`
	SpecialFlagged = `\Flagged`
)

// Outgoing is a message to send. Its Bcc recipients get it and are named
// nowhere a recipient sees: not in the message sent, only in the copy
// the Sent folder keeps.
type Outgoing struct {
	To         []Address `json:"to,omitempty"`
	Cc         []Address `json:"cc,omitempty"`
	Bcc        []Address `json:"bcc,omitempty"`
	Subject    string    `json:"subject"`
	Text       string    `json:"text"`
	InReplyTo  string    `json:"inReplyTo,omitempty"`
	References []string  `json:"references,omitempty"`
	MessageID  string    `json:"messageID,omitempty"`
	// Identity is the identity the message goes from, by its ID: "" for
	// the account's own address, as is one deleted meanwhile.
	Identity string `json:"identity,omitempty"`
	// From is the address the message goes from, which the engine sets
	// from Identity as it builds the message. The outbox keeps it, so
	// that a message sent again goes from the address it was sent from.
	From Address `json:"from,omitzero"`
	// Attachments are files of the draft file store, in the order they
	// go into the message. The outbox keeps these few fields of each, and
	// never the bytes.
	Attachments []Attachment `json:"attachments,omitempty"`
	// Calendar is the iMIP body a reply to an invitation carries, as
	// text/calendar; it is no part of the envelope.
	Calendar string `json:"-"`
	// DraftID names the draft the message was written as. Send removes
	// that draft once the server took the message. It is no part of the
	// envelope, so the outbox does not keep it.
	DraftID string `json:"-"`
}

// MoveResult is where a message went and its UID there, when the server
// reports one, and where it was. An undo needs that UID.
type MoveResult struct {
	Folder  string
	DestUID UID
	From    string
	UID     UID
	// Pending is the queued action of a message the move could not reach
	// the server for: DestUID is 0, and an undo cancels the action
	// instead of moving the message back.
	Pending int64
}

// SendResult says whether the Sent copy was saved. Copied is false when
// the provider has no Sent folder, which the UI reports.
type SendResult struct {
	Copied  bool
	Outbox  PendingSend
	Warning error
}

// Outbox states.
const (
	StatePending = "pending"
	StateSending = "sending"
	StateSent    = "sent"
	StateFailed  = "failed"
	// StateUnknown is a send that stopped once the message had begun to
	// leave, without the server's answer: one cancelled or cut off on the
	// way, or a "sending" row found again after a crash. The server may
	// or may not have the message, so it is never resent automatically.
	StateUnknown = "unknown"
	// StateCancelled is a send that will not be tried again: one the
	// user stopped before any of the message left, whose draft is still
	// there, or an earlier try of a message that has since been
	// delivered. It is not offered again and holds on to no files.
	StateCancelled = "cancelled"
	// StateScheduled is a message waiting out its send delay: none of it
	// has left. Until it is due Unsend takes it back; then it leaves. One
	// step that succeeds once moves it on, to sending or to cancelled, so
	// that it is never both sent and taken back.
	StateScheduled = "scheduled"
)

// PendingSend is an outbox entry. Raw is the message as it was built,
// except for a message with attachments: that one is built again from its
// envelope and the attached files when it is sent, so that their bytes
// stay out of the database. An entry that reached a state no send
// follows, sent or cancelled, keeps none.
type PendingSend struct {
	ID        string    `json:"id"`
	AccountID string    `json:"accountID"`
	Raw       []byte    `json:"-"`
	Envelope  Outgoing  `json:"envelope"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	SentAt    time.Time `json:"sentAt,omitempty"`
	// DueAt is when a message scheduled with a send delay leaves; zero
	// for one sent at once.
	DueAt time.Time `json:"dueAt,omitzero"`
	// DraftID is the draft a scheduled message was written as. It stays
	// a draft while the message waits, which is what Unsend gives back.
	DraftID string `json:"draftID,omitempty"`
}

// FlagUpdate refreshes the flags of a cached message during a sync, and
// its labels from its keywords, unless Keywords is nil.
type FlagUpdate struct {
	UID      UID
	Seen     bool
	Flagged  bool
	Keywords []string
}

// SyncBatch is one folder sync's changes, written with the cursor in one
// transaction.
type SyncBatch struct {
	Folder   Folder
	Reset    bool
	Messages []Message
	Flags    []FlagUpdate
	// Threading is what the sync read again of the threading headers of
	// cached messages. Only what the server gave is written, and the
	// messages are put in their conversations again.
	Threading []ThreadHeaders
}

// OlderBatch is what one fetch of older mail writes: messages of a folder
// the cache lacked below its sync cursor, fetched under UIDValidity, and
// whether the cache then lacks none of them. The cursor, the counts and
// the watermark of announced mail are no part of it.
type OlderBatch struct {
	Folder      string
	UIDValidity uint32
	Messages    []Message
	Complete    bool
}

// nowUTC is the clock of the cache's timestamps.
func nowUTC() time.Time { return time.Now().UTC() }

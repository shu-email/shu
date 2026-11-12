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

// ParseAddress parses one address, keeping its name.
func ParseAddress(s string) Address {
	s = strings.TrimSpace(s)
	if a, err := mail.ParseAddress(s); err == nil {
		return Address{Name: a.Name, Email: a.Address}
	}
	return Address{Email: s}
}

// ParseAddresses parses a comma-separated list of addresses, dropping what
// it cannot parse.
func ParseAddresses(list []string) []Address {
	var out []Address
	for _, s := range list {
		for _, part := range strings.Split(s, ",") {
			if a := ParseAddress(part); a.Email != "" {
				out = append(out, a)
			}
		}
	}
	return out
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
}

// IsInbox reports whether the folder is the inbox.
func (f Folder) IsInbox() bool { return f.SpecialUse == SpecialInbox }

// Summary is the header of a message, what the list shows.
type Summary struct {
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
	// DraftID is set on the row of a local draft, which has no folder or
	// UID of its own: the draft is on this device, whatever the server
	// holds of it.
	DraftID string `json:"draftID,omitempty"`
}

// Message is a summary with the parts the reading pane needs.
type Message struct {
	Summary
	BodyText      string       `json:"bodyText,omitempty"`
	BodyHTML      string       `json:"bodyHTML,omitempty"`
	BodyTruncated bool         `json:"bodyTruncated,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	RawHeaders    string       `json:"rawHeaders,omitempty"`
	CachedAt      time.Time    `json:"cachedAt,omitempty"`
	// BodyCached reports whether the body is in the cache; a summary
	// whose body was never fetched has none.
	BodyCached bool `json:"bodyCached,omitempty"`
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

// Outgoing is a message to send.
type Outgoing struct {
	To         []Address `json:"to,omitempty"`
	Cc         []Address `json:"cc,omitempty"`
	Bcc        []Address `json:"bcc,omitempty"`
	Subject    string    `json:"subject"`
	Text       string    `json:"text"`
	InReplyTo  string    `json:"inReplyTo,omitempty"`
	References []string  `json:"references,omitempty"`
	MessageID  string    `json:"messageID,omitempty"`
	// Attachments are files of the draft file store, in the order they
	// go into the message. The outbox keeps these few fields of each, and
	// never the bytes.
	Attachments []Attachment `json:"attachments,omitempty"`
	// DraftID names the draft the message was written as. Send removes
	// that draft once the server took the message. It is no part of the
	// envelope, so the outbox does not keep it.
	DraftID string `json:"-"`
}

// MoveResult is where a message went and its UID there, when the server
// reports one. An undo needs that UID.
type MoveResult struct {
	Folder  string
	DestUID UID
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
)

// PendingSend is an outbox entry. Raw is the message as it was built,
// except for a message with attachments: that one is built again from its
// envelope and the attached files when it is sent, so that their bytes
// stay out of the database.
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
}

// FlagUpdate refreshes the flags of a cached message during a sync.
type FlagUpdate struct {
	UID     UID
	Seen    bool
	Flagged bool
}

// SyncBatch is one folder sync's changes, written with the cursor in one
// transaction.
type SyncBatch struct {
	Folder   Folder
	Reset    bool
	Messages []Message
	Flags    []FlagUpdate
}

// nowUTC is the clock of the cache's timestamps.
func nowUTC() time.Time { return time.Now().UTC() }

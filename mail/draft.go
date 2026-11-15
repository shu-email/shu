package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Draft is a message being written. The draft on this device is the truth;
// the copy in the server's Drafts folder mirrors it for other mail clients.
// The recipient fields hold the text as typed, so a half-typed address
// survives a restart.
type Draft struct {
	ID        string
	AccountID string

	To         string
	Cc         string
	Bcc        string
	Subject    string
	Body       string
	InReplyTo  string
	References []string
	// Identity is the identity the draft is written from, by ID: "" for
	// the account's own address, which one deleted falls back to.
	Identity string
	// Attachments are the files attached, in the order they were added.
	// Their bytes are in the draft file store (see attach.go). They
	// change through AttachFile and RemoveAttachment only: a save of the
	// text leaves them as they are.
	Attachments []Attachment
	// MessageID stays the same in every copy uploaded and in the message
	// sent, so other clients see one message being rewritten.
	MessageID string
	UpdatedAt time.Time

	// Revision counts the saves. SyncedRevision is the revision the server
	// copy holds, 0 before the first upload.
	Revision       int64
	SyncedRevision int64
	// The uploaded copy's place. A UID means something only under the
	// UIDVALIDITY it was given.
	ServerFolder      string
	ServerUID         UID
	ServerUIDValidity uint32
	// PendingDelete marks a draft discarded or sent whose server copy is
	// still to remove. The row stays until then, so the copy cannot come
	// back as a draft at the next sync.
	PendingDelete bool
	// Conflict marks a draft another mail client changed while it had
	// edits of its own here. Both versions were kept.
	Conflict bool
}

// Dirty reports edits the server copy does not have.
func (d Draft) Dirty() bool { return d.Revision > d.SyncedRevision }

// DraftSyncResult is what one sync of the drafts did.
type DraftSyncResult struct {
	// Folder is the server's Drafts folder. It is empty when the server
	// has none: drafts then stay on this device, which is no error.
	Folder string
	// Uploaded counts the drafts whose server copy was written, Removed
	// the discarded and sent drafts whose copy was deleted.
	Uploaded int
	Removed  int
	// Replaced counts the drafts another mail client changed or deleted
	// and that had no edits here: the local draft gave way. Conflicts
	// counts those that did have edits, and were kept beside the other
	// client's version.
	Replaced  int
	Conflicts int
	// Deferred counts the drafts SyncDraftEdits left for a full sync:
	// their attachments are too large to upload at every pause.
	Deferred int
}

// Draft errors the UI explains.
var (
	// ErrDraftDiscarded is a save that came after the draft was discarded
	// or sent.
	ErrDraftDiscarded = errors.New("mail: the draft was discarded")
	// ErrDraftHasAttachments refuses to take over another client's draft
	// that has attachments this app could not keep whole: an edit here
	// would replace the message with one that lacks them.
	ErrDraftHasAttachments = errors.New("mail: the draft has attachments that are too large to keep")
)

// draftIDHeader marks a server copy as the upload of a local draft. A
// search for it finds the copies an interrupted sync left behind.
const draftIDHeader = "X-Shu-Draft-ID"

// NewDraftID returns a random draft ID.
func NewDraftID() string {
	return randomID(func() string { return fmt.Sprintf("draft%d", time.Now().UnixNano()) })
}

// sameDraftContent reports whether two drafts say the same. The
// attachments are no part of it: a save does not change them.
func sameDraftContent(a, b Draft) bool {
	return a.To == b.To && a.Cc == b.Cc && a.Bcc == b.Bcc && a.Subject == b.Subject &&
		a.Body == b.Body && a.InReplyTo == b.InReplyTo && slices.Equal(a.References, b.References) &&
		a.Identity == b.Identity
}

// saysWhatWasSent reports whether a draft still says what a message sent
// from it said: one edited since stays a draft.
func saysWhatWasSent(d Draft, sent Outgoing) bool {
	out := d.outgoing()
	return out.Subject == sent.Subject && out.Text == sent.Text &&
		slices.Equal(out.To, sent.To) && slices.Equal(out.Cc, sent.Cc) && slices.Equal(out.Bcc, sent.Bcc) &&
		out.Identity == sent.Identity && sameAttachments(out.Attachments, sent.Attachments)
}

// draftAddresses reads a recipient field as far as it parses. A draft may
// hold an address still being typed, which its server copy leaves out.
func draftAddresses(text string) []Address {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if list, err := ParseAddressList([]string{text}); err == nil {
		return list
	}
	var out []Address
	for _, part := range strings.Split(text, ",") {
		if a, err := mail.ParseAddress(strings.TrimSpace(part)); err == nil {
			out = append(out, Address{Name: a.Name, Email: a.Address})
		}
	}
	return out
}

// AddressesText writes addresses as a recipient field holds them.
func AddressesText(list []Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

// outgoing is the draft as a message, with the recipients that parse.
func (d Draft) outgoing() Outgoing {
	return Outgoing{
		To:          draftAddresses(d.To),
		Cc:          draftAddresses(d.Cc),
		Bcc:         draftAddresses(d.Bcc),
		Subject:     strings.TrimSpace(d.Subject),
		Text:        d.Body,
		InReplyTo:   d.InReplyTo,
		References:  d.References,
		MessageID:   d.MessageID,
		Identity:    d.Identity,
		Attachments: d.Attachments,
	}
}

// summary is the draft's row in the Drafts folder, from the identity it
// is written from.
func (d Draft) summary(from Identity) Summary {
	return Summary{
		Account:        from.AccountID,
		DraftID:        d.ID,
		MessageID:      d.MessageID,
		InReplyTo:      d.InReplyTo,
		References:     d.References,
		From:           from.Address(),
		To:             draftAddresses(d.To),
		Cc:             draftAddresses(d.Cc),
		Subject:        strings.TrimSpace(d.Subject),
		Preview:        preview(d.Body),
		Date:           d.UpdatedAt,
		Seen:           true,
		HasAttachments: len(d.Attachments) > 0,
	}
}

// matches reports whether a search for query finds the draft.
func (d Draft) matches(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, field := range []string{d.Subject, d.To, d.Cc, d.Bcc, d.Body} {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// draftMessage builds the server copy of a draft, attachments included:
// another mail client that opens it gets the whole draft, from the
// identity it is written from.
func (s *Service) draftMessage(a Account, from Identity, d Draft) ([]byte, error) {
	out := d.outgoing()
	out.From = from.Address()
	bcc := out.Bcc
	out.Bcc = nil
	msg, err := s.newMessage(a, out, d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	msg.SetGenHeaderPreformatted(gomail.Header(draftIDHeader), d.ID)
	// A message going out must not show its Bcc, so go-mail never writes
	// it; a draft has to keep it for the next editor.
	if value := headerAddresses(bcc); value != "" {
		msg.SetGenHeaderPreformatted(gomail.Header("Bcc"), value)
	}
	return rawMessage(msg, d.AttachmentBytes())
}

// SaveDraft writes a draft to the cache and nothing else. No connection is
// made, so writing works offline; SyncDrafts uploads it later. A draft
// without an ID is new. The stored draft comes back, with its revision.
// The attachments are not SaveDraft's to change: the stored draft keeps
// the ones it has, whatever d lists.
func (s *Service) SaveDraft(ctx context.Context, d Draft) (Draft, error) {
	a, ok, err := s.store.Account(ctx, d.AccountID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrNotFound
	}
	if d.ID == "" {
		d.ID = NewDraftID()
	}
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	old, exists, err := s.store.Draft(ctx, d.ID)
	if err != nil {
		return Draft{}, err
	}
	if !exists {
		return s.createDraft(ctx, a, d)
	}
	if old.PendingDelete {
		return Draft{}, ErrDraftDiscarded
	}
	if sameDraftContent(old, d) {
		return old, nil
	}
	d.MessageID = old.MessageID
	d.UpdatedAt = s.now()
	return s.store.SaveDraft(ctx, d)
}

// createDraft stores a draft that has no row yet. draftMu is held.
func (s *Service) createDraft(ctx context.Context, a Account, d Draft) (Draft, error) {
	// A new row starts with nothing on the server and nothing attached,
	// whatever was passed.
	d.Revision, d.SyncedRevision = 0, 0
	d.ServerFolder, d.ServerUID, d.ServerUIDValidity = "", 0, 0
	d.PendingDelete = false
	// A draft another client replaced while its window stayed open
	// comes back with the window's next save. It is another message
	// now, kept beside the other client's, with the files it had.
	d.Attachments, d.Conflict = s.draftsReplaced[d.ID]
	delete(s.draftsReplaced, d.ID)
	if d.MessageID == "" || d.Conflict {
		from, err := s.identity(ctx, a, d.Identity)
		if err != nil {
			return Draft{}, err
		}
		d.MessageID = newMessageID(from.Email)
	}
	d.UpdatedAt = s.now()
	return s.store.SaveDraft(ctx, d)
}

// Draft reads one draft. A draft discarded or sent is not found, even
// while its server copy awaits deletion.
func (s *Service) Draft(ctx context.Context, id string) (Draft, bool, error) {
	d, ok, err := s.store.Draft(ctx, id)
	if err != nil || !ok || d.PendingDelete {
		return Draft{}, false, err
	}
	return d, true, nil
}

// DraftList is what the Drafts folder shows: the drafts on this device,
// newest first, then the server's cached drafts that are no upload of
// those, which other mail clients wrote.
func (s *Service) DraftList(ctx context.Context, accountID, query string, limit int) ([]Summary, error) {
	a, ok, err := s.store.Account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	drafts, err := s.store.Drafts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	folder, err := s.cachedDraftsFolder(ctx, accountID)
	if err != nil {
		return nil, err
	}
	added, err := s.store.Identities(ctx, accountID)
	if err != nil {
		return nil, err
	}
	from := func(d Draft) Identity {
		for _, id := range added {
			if id.ID == d.Identity {
				return id
			}
		}
		return ownIdentity(a)
	}
	out := make([]Summary, 0, len(drafts))
	uploaded := map[UID]bool{}
	for _, d := range drafts {
		// The copy of a draft awaiting deletion is hidden with it.
		if d.ServerUID != 0 && d.ServerFolder == folder.Name &&
			(folder.UIDValidity == 0 || d.ServerUIDValidity == folder.UIDValidity) {
			uploaded[d.ServerUID] = true
		}
		if !d.PendingDelete && d.matches(query) {
			out = append(out, d.summary(from(d)))
		}
	}
	if folder.Name != "" {
		// The uploads left out below must not use up the rows asked for.
		msgs, err := s.store.ListMessages(ctx, accountID, folder.Name, query, max(limit, 0)+len(uploaded))
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if !uploaded[m.UID] {
				out = append(out, m)
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// cachedDraftsFolder is the account's Drafts folder as last listed, or a
// zero folder.
func (s *Service) cachedDraftsFolder(ctx context.Context, accountID string) (Folder, error) {
	folders, err := s.store.Folders(ctx, accountID)
	if err != nil {
		return Folder{}, err
	}
	for _, f := range folders {
		if f.SpecialUse == SpecialDrafts {
			return f, nil
		}
	}
	return Folder{}, nil
}

package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	gomsgmail "github.com/emersion/go-message/mail"
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
}

// Draft errors the UI explains.
var (
	// ErrDraftDiscarded is a save that came after the draft was discarded
	// or sent.
	ErrDraftDiscarded = errors.New("mail: the draft was discarded")
	// ErrDraftHasAttachments refuses to take over another client's draft
	// whose attachments this app could not keep.
	ErrDraftHasAttachments = errors.New("mail: the draft has attachments")
)

// draftIDHeader marks a server copy as the upload of a local draft. A
// search for it finds the copies an interrupted sync left behind.
const draftIDHeader = "X-Shu-Draft-ID"

// NewDraftID returns a random draft ID.
func NewDraftID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("draft%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// sameDraftContent reports whether two drafts say the same.
func sameDraftContent(a, b Draft) bool {
	return a.To == b.To && a.Cc == b.Cc && a.Bcc == b.Bcc && a.Subject == b.Subject &&
		a.Body == b.Body && a.InReplyTo == b.InReplyTo && slices.Equal(a.References, b.References)
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

// addressesText writes addresses as a recipient field holds them.
func addressesText(list []Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

// outgoing is the draft as a message, with the recipients that parse.
func (d Draft) outgoing() Outgoing {
	return Outgoing{
		To:         draftAddresses(d.To),
		Cc:         draftAddresses(d.Cc),
		Bcc:        draftAddresses(d.Bcc),
		Subject:    strings.TrimSpace(d.Subject),
		Text:       d.Body,
		InReplyTo:  d.InReplyTo,
		References: d.References,
		MessageID:  d.MessageID,
	}
}

// summary is the draft's row in the Drafts folder.
func (d Draft) summary(a Account) Summary {
	return Summary{
		DraftID:    d.ID,
		MessageID:  d.MessageID,
		InReplyTo:  d.InReplyTo,
		References: d.References,
		From:       Address{Name: a.Name, Email: a.Email},
		To:         draftAddresses(d.To),
		Cc:         draftAddresses(d.Cc),
		Subject:    strings.TrimSpace(d.Subject),
		Preview:    preview(d.Body),
		Date:       d.UpdatedAt,
		Seen:       true,
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

// draftMessage builds the server copy of a draft.
func draftMessage(a Account, d Draft) ([]byte, error) {
	out := d.outgoing()
	bcc := out.Bcc
	out.Bcc = nil
	msg, err := newMessage(a, out, d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	msg.SetGenHeaderPreformatted(gomail.Header(draftIDHeader), d.ID)
	if len(bcc) > 0 {
		// A message going out must not show its Bcc, so go-mail never
		// writes it; a draft has to keep it for the next editor.
		parts := make([]string, 0, len(bcc))
		for _, addr := range toStdAddresses(bcc) {
			parts = append(parts, addr.String())
		}
		// Preformatted headers are written as given, so the folding of a
		// long list is done here: one address a line.
		msg.SetGenHeaderPreformatted(gomail.Header("Bcc"), strings.Join(parts, ",\r\n "))
	}
	return rawMessage(msg)
}

// SaveDraft writes a draft to the cache and nothing else. No connection is
// made, so writing works offline; SyncDrafts uploads it later. A draft
// without an ID is new. The stored draft comes back, with its revision.
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
	if exists {
		if old.PendingDelete {
			return Draft{}, ErrDraftDiscarded
		}
		if sameDraftContent(old, d) {
			return old, nil
		}
		d.MessageID = old.MessageID
	} else {
		// A new row starts with nothing on the server, whatever was passed.
		d.Revision, d.SyncedRevision = 0, 0
		d.ServerFolder, d.ServerUID, d.ServerUIDValidity = "", 0, 0
		d.PendingDelete = false
		// A draft another client replaced while its window stayed open
		// comes back with the window's next save. It is another message
		// now, kept beside the other client's.
		d.Conflict = s.draftsReplaced[d.ID]
		delete(s.draftsReplaced, d.ID)
		if d.MessageID == "" || d.Conflict {
			d.MessageID = newMessageID(a.Email)
		}
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
	out := make([]Summary, 0, len(drafts))
	uploaded := map[UID]bool{}
	for _, d := range drafts {
		// The copy of a draft awaiting deletion is hidden with it.
		if d.ServerUID != 0 && d.ServerFolder == folder.Name &&
			(folder.UIDValidity == 0 || d.ServerUIDValidity == folder.UIDValidity) {
			uploaded[d.ServerUID] = true
		}
		if !d.PendingDelete && d.matches(query) {
			out = append(out, d.summary(a))
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

// lockDraftSync lets one job at a time change the server's Drafts folder:
// two uploads of the same draft would leave two copies.
func (s *Service) lockDraftSync(ctx context.Context) (func(), error) {
	select {
	case s.draftSync <- struct{}{}:
		return func() { <-s.draftSync }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// updateDraftSync changes the sync state of a draft as one step, so a save
// or a discard made meanwhile is not overwritten. The bool is false when
// the row is gone.
func (s *Service) updateDraftSync(ctx context.Context, id string, change func(*Draft)) (Draft, bool, error) {
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	d, ok, err := s.store.Draft(ctx, id)
	if err != nil || !ok {
		return Draft{}, false, err
	}
	change(&d)
	if err := s.store.SetDraftSync(ctx, d); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Draft{}, false, nil
		}
		return Draft{}, false, err
	}
	return d, true, nil
}

// draftBox is the server's Drafts folder, selected on one connection.
type draftBox struct {
	c           *imapSession
	folder      string
	uidValidity uint32
	// present is the UIDs the folder holds, kept in step with what this
	// connection appends and removes.
	present map[UID]bool
}

// holds reports whether a draft's recorded place is in this folder as it
// is now.
func (b *draftBox) holds(d Draft) bool {
	return d.ServerUID != 0 && d.ServerFolder == b.folder && d.ServerUIDValidity == b.uidValidity
}

// search returns the UIDs of the messages whose headers contain all the
// given values, or of every message.
func (b *draftBox) search(headers ...imap.SearchCriteriaHeaderField) ([]UID, error) {
	data, err := b.c.client.UIDSearch(&imap.SearchCriteria{Header: headers}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: search %s: %w", b.folder, err)
	}
	uids := data.AllUIDs()
	out := make([]UID, 0, len(uids))
	for _, uid := range uids {
		out = append(out, UID(uid))
	}
	return out, nil
}

// copies returns the uploads of a draft: the messages carrying its ID and
// its Message-ID. Both must match, because a mail client that keeps
// unknown headers passes the ID on to the message it replaces a copy
// with, and that message is not this app's to delete. A draft in conflict
// gets a new Message-ID for that reason.
func (b *draftBox) copies(d Draft) ([]UID, error) {
	// An empty value matches every message that has the header.
	if d.ID == "" || d.MessageID == "" {
		return nil, nil
	}
	found, err := b.search(
		imap.SearchCriteriaHeaderField{Key: draftIDHeader, Value: d.ID},
		imap.SearchCriteriaHeaderField{Key: "Message-ID", Value: d.MessageID},
	)
	if err != nil {
		return nil, err
	}
	return b.confirm(found, d, true)
}

// adopted returns the messages with a draft's Message-ID, whoever wrote
// them: a draft taken over from another mail client has no ID of this app
// in its copy.
func (b *draftBox) adopted(d Draft) ([]UID, error) {
	if d.MessageID == "" {
		return nil, nil
	}
	found, err := b.search(imap.SearchCriteriaHeaderField{Key: "Message-ID", Value: d.MessageID})
	if err != nil {
		return nil, err
	}
	return b.confirm(found, d, false)
}

// confirm keeps the messages a search found that really are the draft's.
// A HEADER search matches any header containing the value, and servers
// differ in how they read it, while what counts as a copy is later
// deleted for good. So the search only proposes: each message's headers
// are read and compared exactly, the Message-ID always and, for own
// copies, this app's draft ID too. A message that cannot be read is not
// confirmed, and so is left alone.
func (b *draftBox) confirm(uids []UID, d Draft, own bool) ([]UID, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"Message-ID", draftIDHeader},
		Peek:         true,
	}
	fetched, err := b.c.client.Fetch(set, &imap.FetchOptions{
		UID: true, BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: fetch headers of %s: %w", b.folder, err)
	}
	var out []UID
	for _, buf := range fetched {
		msg, err := mail.ReadMessage(bytes.NewReader(buf.FindBodySection(section)))
		if err != nil {
			continue
		}
		messageID := strings.Trim(strings.TrimSpace(msg.Header.Get("Message-ID")), "<>")
		if messageID != d.MessageID {
			continue
		}
		if own && strings.TrimSpace(msg.Header.Get(draftIDHeader)) != d.ID {
			continue
		}
		// The server answers for what it has; only UIDs asked for count.
		if uid := UID(buf.UID); slices.Contains(uids, uid) {
			out = append(out, uid)
		}
	}
	return out, nil
}

// append uploads a draft's copy and returns the UID the server gave it.
func (b *draftBox) append(raw []byte, d Draft, at time.Time) (UID, error) {
	cmd := b.c.client.Append(b.folder, int64(len(raw)), &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen},
		Time:  at,
	})
	if _, err := cmd.Write(raw); err != nil {
		cmd.Close()
		return 0, err
	}
	if err := cmd.Close(); err != nil {
		return 0, err
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, err
	}
	if (b.c.capHas(imap.CapUIDPlus) || b.c.capHas(imap.CapIMAP4rev2)) && data.UID != 0 &&
		(data.UIDValidity == 0 || data.UIDValidity == b.uidValidity) {
		b.present[UID(data.UID)] = true
		return UID(data.UID), nil
	}
	// Without UIDPLUS the server does not say which UID the copy got: it
	// is the newest of the draft's copies that was not there before. The
	// NOOP lets the server show this connection the message it just took.
	if err := b.c.client.Noop().Wait(); err != nil {
		return 0, fmt.Errorf("imap: noop: %w", err)
	}
	uids, err := b.copies(d)
	if err != nil {
		return 0, err
	}
	var newest UID
	for _, uid := range uids {
		if uid > newest && !b.present[uid] {
			newest = uid
		}
	}
	if newest == 0 {
		return 0, errors.New("imap: the server did not show the uploaded draft")
	}
	b.present[newest] = true
	return newest, nil
}

// remove deletes messages for good: IMAP has no other way to take back a
// copy, and none to edit one.
func (b *draftBox) remove(uids []UID) error {
	var set imap.UIDSet
	for _, uid := range uids {
		if b.present[uid] {
			set.AddNum(imap.UID(uid))
		}
	}
	if len(set) == 0 {
		return nil
	}
	deleted := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if b.c.capHas(imap.CapUIDPlus) || b.c.capHas(imap.CapIMAP4rev2) {
		if err := b.c.client.Store(set, deleted, nil).Close(); err != nil {
			return fmt.Errorf("imap: mark deleted: %w", err)
		}
		// UID EXPUNGE removes these messages and no others.
		if err := b.c.client.UIDExpunge(set).Close(); err != nil {
			return fmt.Errorf("imap: expunge: %w", err)
		}
	} else if err := b.expungeOnly(set, deleted); err != nil {
		return err
	}
	for _, uid := range uids {
		delete(b.present, uid)
	}
	return nil
}

// expungeOnly removes the given messages on a server without UID EXPUNGE.
// Its EXPUNGE removes every message flagged \Deleted, and another mail
// client may have flagged some it has not removed yet, which the user can
// still undelete there. Those are unflagged for the EXPUNGE and flagged
// again after it. If the connection is lost in between they stay
// unflagged, which keeps them: the safe way for this to fail.
func (b *draftBox) expungeOnly(set imap.UIDSet, deleted *imap.StoreFlags) error {
	data, err := b.c.client.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagDeleted}}, nil).Wait()
	if err != nil {
		return fmt.Errorf("imap: search %s: %w", b.folder, err)
	}
	var others imap.UIDSet
	for _, uid := range data.AllUIDs() {
		if !set.Contains(uid) {
			others.AddNum(uid)
		}
	}
	if len(others) > 0 {
		undelete := &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
		if err := b.c.client.Store(others, undelete, nil).Close(); err != nil {
			// Nothing was removed yet, and nothing will be this time.
			return fmt.Errorf("imap: unmark deleted: %w", err)
		}
	}
	err = b.c.client.Store(set, deleted, nil).Close()
	if err == nil {
		err = b.c.client.Expunge().Close()
	}
	if len(others) > 0 {
		// Whatever happened above, the other client's marks go back.
		if flagErr := b.c.client.Store(others, deleted, nil).Close(); flagErr != nil && err == nil {
			err = fmt.Errorf("imap: mark deleted again: %w", flagErr)
		}
	}
	if err != nil {
		return fmt.Errorf("imap: expunge: %w", err)
	}
	return nil
}

// openDrafts selects the account's Drafts folder and lists what it holds.
// It returns nil when the server has no Drafts folder.
func (s *Service) openDrafts(ctx context.Context, c *imapSession, accountID string) (*draftBox, error) {
	cached, err := s.store.Folders(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(cached) == 0 {
		// The account was never synced: ask the server what it has.
		if cached, err = s.listAndSaveFolders(ctx, c, accountID); err != nil {
			return nil, err
		}
	}
	folder := draftsFolderOf(cached)
	if folder == "" {
		return nil, nil
	}
	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		// The cache may name a folder deleted since. Only a server that
		// still lists it has failed; otherwise there is no Drafts folder.
		listed, listErr := s.listAndSaveFolders(ctx, c, accountID)
		if listErr != nil || slices.ContainsFunc(listed, func(f Folder) bool { return f.Name == folder }) {
			return nil, fmt.Errorf("imap: select %s: %w", folder, err)
		}
		return nil, nil
	}
	box := &draftBox{c: c, folder: folder, uidValidity: selected.UIDValidity, present: map[UID]bool{}}
	uids, err := box.search()
	if err != nil {
		return nil, err
	}
	for _, uid := range uids {
		box.present[uid] = true
	}
	return box, nil
}

// listAndSaveFolders lists the server's folders and caches them.
func (s *Service) listAndSaveFolders(ctx context.Context, c *imapSession, accountID string) ([]Folder, error) {
	folders, err := s.listFolders(c)
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveFolders(ctx, accountID, folders); err != nil {
		return nil, err
	}
	return folders, nil
}

func draftsFolderOf(folders []Folder) string {
	for _, f := range folders {
		if f.SpecialUse == SpecialDrafts {
			return f.Name
		}
	}
	return ""
}

// SyncDrafts brings the server's Drafts folder in step with the drafts on
// this device: it uploads the drafts edited since their last upload,
// removes the copies of those discarded or sent, and applies what other
// mail clients did. It can fail and be run again: a draft counts as
// uploaded only once the server took it.
func (s *Service) SyncDrafts(ctx context.Context, accountID string) (DraftSyncResult, error) {
	var result DraftSyncResult
	release, err := s.lockDraftSync(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return result, err
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return result, err
	}
	defer c.close()
	box, err := s.openDrafts(ctx, c, accountID)
	if err != nil {
		return result, err
	}
	drafts, err := s.store.Drafts(ctx, accountID)
	if err != nil {
		return result, err
	}
	if box == nil {
		// Without a Drafts folder there is nothing to upload to, and no
		// copy left of a draft discarded.
		for _, d := range drafts {
			if d.PendingDelete {
				if err := s.store.DeleteDraft(ctx, d.ID); err != nil {
					return result, err
				}
			}
		}
		return result, nil
	}
	result.Folder = box.folder
	var failed error
	for _, d := range drafts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if d.PendingDelete {
			err = s.removeDraft(ctx, box, d)
			if err == nil {
				result.Removed++
			}
		} else {
			err = s.syncDraft(ctx, box, a, d, &result)
		}
		// One draft's failure does not keep the others from syncing.
		if err != nil && failed == nil {
			failed = err
		}
	}
	s.forgetVanished(ctx, accountID, box)
	return result, failed
}

// syncDraft uploads one draft if it has edits, and finds out whether
// another mail client changed its copy. Such a client cannot edit a
// message either: it replaces it, so the UID this device uploaded
// disappears and another message appears.
func (s *Service) syncDraft(ctx context.Context, box *draftBox, a Account, d Draft, result *DraftSyncResult) error {
	switch {
	case box.holds(d) && box.present[d.ServerUID]:
		if !d.Dirty() {
			return nil
		}
		// Besides the copy on record, an interrupted sync may have left
		// uploads that were never recorded.
		stale, err := box.copies(d)
		if err != nil {
			return err
		}
		return s.uploadDraft(ctx, box, a, d, append(stale, d.ServerUID), result)
	case box.holds(d):
		if !d.Dirty() {
			replaced, err := s.dropReplacedDraft(ctx, d)
			if err != nil {
				return err
			}
			if replaced {
				result.Replaced++
				return nil
			}
			// It was edited as the sync ran: a conflict after all.
		}
		// Both sides changed the draft. This one is uploaded as a message
		// of its own, under a new Message-ID, and whatever the other
		// client put in the folder is left alone: nothing is lost.
		forked, ok, err := s.updateDraftSync(ctx, d.ID, func(d *Draft) {
			d.Conflict = true
			d.MessageID = newMessageID(a.Email)
			d.SyncedRevision = 0
			d.ServerFolder, d.ServerUID, d.ServerUIDValidity = "", 0, 0
		})
		if err != nil || !ok {
			return err
		}
		result.Conflicts++
		return s.uploadDraft(ctx, box, a, forked, nil, result)
	default:
		// Never uploaded, or recorded under another folder or UIDVALIDITY,
		// where the UID means nothing. The draft's ID finds the copies it
		// has all the same, so that it is not uploaded twice.
		copies, err := box.copies(d)
		if err != nil {
			return err
		}
		if !d.Dirty() {
			if len(copies) == 0 {
				// A draft taken over from another client has no ID in its
				// copy; its Message-ID finds it.
				copies, err = box.adopted(d)
				if err != nil {
					return err
				}
			}
			if len(copies) > 0 {
				uid := slices.Max(copies)
				_, _, err := s.updateDraftSync(ctx, d.ID, func(d *Draft) {
					d.ServerFolder, d.ServerUID, d.ServerUIDValidity = box.folder, uid, box.uidValidity
				})
				return err
			}
			copies = nil
		}
		return s.uploadDraft(ctx, box, a, d, copies, result)
	}
}

// dropReplacedDraft removes a draft whose uploaded copy another client
// replaced or deleted, unless it was edited meanwhile. What the other
// client left shows as a draft of the server.
func (s *Service) dropReplacedDraft(ctx context.Context, d Draft) (bool, error) {
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	cur, ok, err := s.store.Draft(ctx, d.ID)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if cur.Dirty() {
		return false, nil
	}
	if err := s.store.DeleteDraft(ctx, d.ID); err != nil {
		return false, err
	}
	// A window still editing the draft saves it again: SaveDraft then
	// keeps it as a draft of its own instead of losing what the window
	// shows.
	s.draftsReplaced[d.ID] = true
	return true, nil
}

// uploadDraft publishes a draft's content. IMAP cannot edit a message, so
// the new copy is appended and the copies it supersedes are deleted. The
// place and revision are recorded only once the server took the copy: a
// failure before that leaves the draft to upload again.
func (s *Service) uploadDraft(ctx context.Context, box *draftBox, a Account, d Draft, stale []UID, result *DraftSyncResult) error {
	// The drafts were read as the sync began. This one may have been
	// edited since, and the newest text is the one worth uploading; or
	// discarded, and then its removal is the next sync's work.
	d, ok, err := s.store.Draft(ctx, d.ID)
	if err != nil || !ok || d.PendingDelete {
		return err
	}
	raw, err := draftMessage(a, d)
	if err != nil {
		return err
	}
	uid, err := box.append(raw, d, s.now())
	if err != nil {
		return fmt.Errorf("imap: append to %s: %w", box.folder, err)
	}
	_, ok, err = s.updateDraftSync(ctx, d.ID, func(cur *Draft) {
		// Edits saved since this revision was read stay to upload.
		cur.SyncedRevision = d.Revision
		cur.ServerFolder, cur.ServerUID, cur.ServerUIDValidity = box.folder, uid, box.uidValidity
	})
	if err != nil {
		return err
	}
	if !ok {
		// The draft was removed while it uploaded: its copy goes too.
		stale = append(stale, uid)
	} else {
		result.Uploaded++
	}
	return s.removeCopies(ctx, a.ID, box, stale)
}

// removeCopies deletes server copies and their cached rows.
func (s *Service) removeCopies(ctx context.Context, accountID string, box *draftBox, uids []UID) error {
	slices.Sort(uids)
	uids = slices.Compact(uids)
	if err := box.remove(uids); err != nil {
		return err
	}
	for _, uid := range uids {
		// DeleteMessage lowers the folder's count, so it is only for rows
		// the cache has.
		if _, ok, err := s.store.GetMessage(ctx, accountID, box.folder, uid); err == nil && ok {
			_ = s.store.DeleteMessage(ctx, accountID, box.folder, uid)
		}
	}
	return nil
}

// removeDraft deletes the server copy of a draft discarded or sent, then
// its row.
func (s *Service) removeDraft(ctx context.Context, box *draftBox, d Draft) error {
	var uids []UID
	switch {
	case box.holds(d) && !box.present[d.ServerUID]:
		// The copy is gone already. What another client may have put in
		// its place is not this draft's to delete.
	default:
		stale, err := box.copies(d)
		if err != nil {
			return err
		}
		uids = stale
		if box.holds(d) {
			uids = append(uids, d.ServerUID)
		}
	}
	if err := s.removeCopies(ctx, d.AccountID, box, uids); err != nil {
		return err
	}
	return s.store.DeleteDraft(ctx, d.ID)
}

// forgetVanished drops the cached rows of the Drafts folder whose message
// the server no longer has. A folder sync only adds what is new, while a
// draft edited anywhere is a message deleted and another appended.
func (s *Service) forgetVanished(ctx context.Context, accountID string, box *draftBox) {
	f, ok, err := s.store.Folder(ctx, accountID, box.folder)
	if err != nil || !ok || f.UIDValidity != box.uidValidity {
		// Under another UIDVALIDITY the folder sync resets the cache.
		return
	}
	cached, err := s.store.ListMessages(ctx, accountID, box.folder, "", defaultSyncCap)
	if err != nil {
		return
	}
	for _, m := range cached {
		if !box.present[m.UID] {
			_ = s.store.DeleteMessage(ctx, accountID, box.folder, m.UID)
		}
	}
}

// DiscardDraft removes a draft from this device at once and from the
// server if it can be reached. If it cannot, the deletion is remembered
// and done by the next sync, so the copy does not come back as a draft.
func (s *Service) DiscardDraft(ctx context.Context, id string) error {
	d, ok, err := s.updateDraftSync(ctx, id, func(d *Draft) { d.PendingDelete = true })
	if err != nil || !ok {
		return err
	}
	s.flushDraftDeletions(ctx, d.AccountID)
	return nil
}

// retireDraft marks a draft that was sent as done. It reports whether
// there was one, whose server copy flushDraftDeletions then removes.
func (s *Service) retireDraft(ctx context.Context, accountID, id string) bool {
	if id == "" {
		return false
	}
	if d, ok, err := s.store.Draft(ctx, id); err != nil || !ok || d.AccountID != accountID {
		return false
	}
	_, ok, err := s.updateDraftSync(ctx, id, func(d *Draft) { d.PendingDelete = true })
	return err == nil && ok
}

// retireSentDraft retires the draft a message resent from the outbox was
// written as, if it still says what was sent.
func (s *Service) retireSentDraft(ctx context.Context, accountID string, sent Outgoing) bool {
	if sent.MessageID == "" {
		return false
	}
	drafts, err := s.store.Drafts(ctx, accountID)
	if err != nil {
		return false
	}
	for _, d := range drafts {
		if d.PendingDelete || d.MessageID != sent.MessageID {
			continue
		}
		out := d.outgoing()
		if out.Subject != sent.Subject || out.Text != sent.Text ||
			!slices.Equal(out.To, sent.To) || !slices.Equal(out.Cc, sent.Cc) {
			return false
		}
		return s.retireDraft(ctx, accountID, d.ID)
	}
	return false
}

// draftMessageID is the Message-ID of a draft about to be sent, or "".
func (s *Service) draftMessageID(ctx context.Context, accountID, id string) string {
	if id == "" {
		return ""
	}
	d, ok, err := s.store.Draft(ctx, id)
	if err != nil || !ok || d.AccountID != accountID {
		return ""
	}
	return d.MessageID
}

// flushDraftDeletions removes the server copies of the drafts discarded
// or sent, and then their rows. It is best effort: what the server could
// not be asked stays marked, and SyncDrafts does it later.
func (s *Service) flushDraftDeletions(ctx context.Context, accountID string) {
	release, err := s.lockDraftSync(ctx)
	if err != nil {
		return
	}
	defer release()
	drafts, err := s.store.Drafts(ctx, accountID)
	if err != nil {
		return
	}
	var uploaded []Draft
	for _, d := range drafts {
		switch {
		case !d.PendingDelete:
		case d.ServerUID == 0:
			// Never uploaded: the server has nothing of it.
			_ = s.store.DeleteDraft(ctx, d.ID)
		default:
			uploaded = append(uploaded, d)
		}
	}
	if len(uploaded) == 0 {
		return
	}
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return
	}
	defer c.close()
	box, err := s.openDrafts(ctx, c, accountID)
	if err != nil {
		return
	}
	for _, d := range uploaded {
		if box == nil {
			// The Drafts folder is gone, and the copy with it.
			_ = s.store.DeleteDraft(ctx, d.ID)
			continue
		}
		_ = s.removeDraft(ctx, box, d)
	}
}

// DismissDraftConflict forgets that a draft was in conflict, once the
// user has seen the notice.
func (s *Service) DismissDraftConflict(ctx context.Context, id string) error {
	_, _, err := s.updateDraftSync(ctx, id, func(d *Draft) { d.Conflict = false })
	return err
}

// AdoptDraft turns a draft another mail client wrote into a draft on this
// device, linked to that server message so that an edit here replaces it.
// A draft already adopted is returned as it is.
func (s *Service) AdoptDraft(ctx context.Context, accountID, folder string, uid UID) (Draft, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	// The sync must not judge the folder while the draft is half made.
	release, err := s.lockDraftSync(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer release()
	drafts, err := s.store.Drafts(ctx, accountID)
	if err != nil {
		return Draft{}, err
	}
	for _, d := range drafts {
		if !d.PendingDelete && d.ServerUID == uid && d.ServerFolder == folder {
			return d, nil
		}
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return Draft{}, err
	}
	defer c.close()
	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		return Draft{}, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	_, raw, err := fetchRaw(c, uid)
	if err != nil {
		return Draft{}, err
	}
	msg, err := ParseMessage(raw)
	if err != nil {
		return Draft{}, fmt.Errorf("mime: %w", err)
	}
	bcc, ownID := draftHeaders(raw)
	for _, d := range drafts {
		// An upload this device lost track of is its own draft still.
		if !d.PendingDelete && d.ID == ownID && d.MessageID == msg.MessageID {
			return d, nil
		}
	}
	if len(msg.Attachments) > 0 {
		// An edit here would replace the message with one that has no
		// attachments. Until compose has them, the draft stays where it
		// is.
		return Draft{}, ErrDraftHasAttachments
	}
	d := Draft{
		ID:         NewDraftID(),
		AccountID:  accountID,
		To:         addressesText(msg.To),
		Cc:         addressesText(msg.Cc),
		Bcc:        addressesText(bcc),
		Subject:    msg.Subject,
		Body:       msg.BodyText,
		InReplyTo:  msg.InReplyTo,
		References: msg.References,
		MessageID:  msg.MessageID,
		UpdatedAt:  s.now(),
		// The server already has this content: nothing to upload until
		// it is edited.
		SyncedRevision:    1,
		ServerFolder:      folder,
		ServerUID:         uid,
		ServerUIDValidity: selected.UIDValidity,
	}
	if d.MessageID == "" {
		d.MessageID = newMessageID(a.Email)
	}
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	return s.store.SaveDraft(ctx, d)
}

// draftHeaders reads what ParseMessage leaves out of a draft: its Bcc,
// and the ID this app gave the copy if it uploaded it.
func draftHeaders(raw []byte) ([]Address, string) {
	// An unknown charset is reported with a reader that still has the
	// header, as ParseMessage relies on too.
	r, _ := gomsgmail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return nil, ""
	}
	defer r.Close()
	return addresses(r.Header, "Bcc"), strings.TrimSpace(r.Header.Get(draftIDHeader))
}

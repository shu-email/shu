package mail

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomsg "github.com/emersion/go-message"
	gomsgmail "github.com/emersion/go-message/mail"
)

// AttachFailure is an attachment that could not be copied into a draft,
// as the message listed it, and why.
type AttachFailure struct {
	Attachment Attachment
	Err        error
}

// AttachFromMessage copies the attachments of a received message into a
// draft, as a forward of it carries them, and creates the draft as d says
// if it is not stored yet; the text of a stored draft is left alone. Each
// file is streamed into the draft file store, from the attachment cache
// where that has it and else from the server, which marks nothing read;
// none is held in memory whole. With what the draft has, they keep within
// MaxAttachmentBytes. One that cannot be copied is among the failures,
// with why, and the others are attached all the same. The error is for
// what kept every one from being copied.
func (s *Service) AttachFromMessage(ctx context.Context, d Draft, accountID, folder string, uid UID) (Draft, []AttachFailure, error) {
	a, ok, err := s.store.Account(ctx, d.AccountID)
	if err != nil {
		return Draft{}, nil, err
	}
	if !ok || accountID != d.AccountID {
		// A forward is written from the account the message is in.
		return Draft{}, nil, ErrNotFound
	}
	m, ok, err := s.store.GetMessage(ctx, accountID, folder, uid)
	if err != nil {
		return Draft{}, nil, err
	}
	if !ok {
		return Draft{}, nil, ErrNotFound
	}
	if d.ID == "" {
		d.ID = NewDraftID()
	}
	cur, _, err := s.store.Draft(ctx, d.ID)
	if err != nil {
		return Draft{}, nil, err
	}
	if cur.PendingDelete {
		return Draft{}, nil, ErrDraftDiscarded
	}
	c := &forwardCopy{svc: s, room: MaxAttachmentBytes - cur.AttachmentBytes()}
	defer c.dropRest()
	// The cache lists every attachment of a message whose body it has
	// whole; one it cut lacks those past its cap, which the server has.
	whole := m.BodyCached && !m.BodyTruncated
	need := map[string]Attachment{}
	if whole {
		for _, att := range m.Attachments {
			if !c.fromCache(ctx, att) {
				need[att.Part] = att
			}
		}
	}
	if !whole || len(need) > 0 {
		var only map[string]Attachment
		if whole {
			only = need
		}
		if err := c.fromServer(ctx, a, folder, uid, only); err != nil {
			if !whole {
				return Draft{}, nil, err
			}
			for _, att := range need {
				c.failed(att, err)
			}
		}
	}
	saved, err := c.attach(ctx, a, d)
	if err != nil {
		return Draft{}, nil, err
	}
	slices.SortStableFunc(c.failures, func(x, y AttachFailure) int {
		return partOrder(x.Attachment.Part) - partOrder(y.Attachment.Part)
	})
	return saved, c.failures, nil
}

// forwardCopy is the files of one AttachFromMessage as they are copied,
// and what could not be.
type forwardCopy struct {
	svc *Service
	// room is how many bytes more the draft may carry.
	room     int64
	files    []forwardFile
	failures []AttachFailure
}

// forwardFile is an attachment copied into the draft file store, still
// under its temporary name.
type forwardFile struct {
	att  Attachment
	file copied
}

// fromCache copies an attachment from the attachment cache, and reports
// whether it is done with it: copied, or failed for a reason the server
// would not change.
func (c *forwardCopy) fromCache(ctx context.Context, att Attachment) bool {
	if att.CachePath == "" {
		return false
	}
	f, err := os.Open(att.CachePath)
	if err != nil {
		return false
	}
	defer f.Close()
	file, err := c.svc.copyStream(ctx, f, c.room)
	if err != nil {
		if err == ErrAttachTooLarge {
			c.failed(att, err)
			return true
		}
		return false
	}
	if file.hash != att.ContentHash {
		// The cache's file is not the attachment: the server has it.
		c.svc.dropIncoming(file.path)
		return false
	}
	c.keep(att, file)
	return true
}

// fromServer streams the message from the server and copies its
// attachments as it goes: those in only, by part, or every one when only
// is nil. It fails as a whole when the message cannot be read.
func (c *forwardCopy) fromServer(ctx context.Context, a Account, folder string, uid UID, only map[string]Attachment) error {
	_, password, err := c.svc.account(ctx, a.ID)
	if err != nil {
		return err
	}
	session, err := c.svc.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer session.close()
	// Read-only, and the body peeked: a forward marks nothing read.
	if _, err := session.client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("imap: examine %s: %w", folder, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := session.client.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	found := false
	for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
		other := false
		for item := msg.Next(); item != nil; item = msg.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				// The server answers for what it has, which need not be
				// what was asked.
				other = item.UID != imap.UID(uid)
			case imapclient.FetchItemDataBodySection:
				if found || other || item.Literal == nil {
					continue
				}
				found = true
				if err := c.walk(ctx, item.Literal, only); err != nil {
					// The rest of the answer is not waited for.
					session.client.Close()
					return err
				}
			}
		}
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("imap: fetch message: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// walk reads a message part by part, as the parser numbers them, and
// copies the attachments it is to: those in only, or all.
func (c *forwardCopy) walk(ctx context.Context, literal io.Reader, only map[string]Attachment) error {
	r, err := gomsgmail.CreateReader(literal)
	if err != nil && !gomsg.IsUnknownCharset(err) {
		return fmt.Errorf("mime: %w", err)
	}
	if r == nil {
		return fmt.Errorf("mime: %w", err)
	}
	defer r.Close()
	part := 0
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomsg.IsUnknownCharset(err) {
			return fmt.Errorf("mime: %w", err)
		}
		if p == nil {
			break
		}
		part++
		contentType, params := partContentType(p.Header)
		if bodyText(p.Header, contentType) {
			continue
		}
		name, ok := attachmentName(p.Header, contentType, params)
		if !ok {
			continue
		}
		att := Attachment{Part: strconv.Itoa(part), Filename: name, ContentType: contentType}
		if only != nil {
			listed, ok := only[att.Part]
			if !ok {
				continue
			}
			att = listed
			delete(only, att.Part)
		}
		file, err := c.svc.copyStream(ctx, p.Body, c.room)
		if err != nil {
			c.failed(att, err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			continue
		}
		att.Size, att.ContentHash = file.size, file.hash
		c.keep(att, file)
	}
	for _, att := range only {
		// Listed by the cache, and not in the message the server has.
		c.failed(att, ErrNotFound)
		delete(only, att.Part)
	}
	return nil
}

// keep takes a copied file for the draft.
func (c *forwardCopy) keep(att Attachment, file copied) {
	c.room -= file.size
	c.files = append(c.files, forwardFile{att: att, file: file})
}

// failed records an attachment that could not be copied.
func (c *forwardCopy) failed(att Attachment, err error) {
	c.failures = append(c.failures, AttachFailure{Attachment: att, Err: err})
}

// dropRest removes the copies that are still under their temporary
// names: those the draft did not take.
func (c *forwardCopy) dropRest() {
	for _, f := range c.files {
		if f.file.path != "" {
			c.svc.dropIncoming(f.file.path)
		}
	}
}

// attach gives the copied files their place in the draft file store and
// adds them to the draft, in the message's order, in one step as the
// sweep sees it. A file attached meanwhile may leave no room for some.
func (c *forwardCopy) attach(ctx context.Context, a Account, d Draft) (Draft, error) {
	s := c.svc
	slices.SortStableFunc(c.files, func(x, y forwardFile) int {
		return partOrder(x.att.Part) - partOrder(y.att.Part)
	})
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	cur, exists, err := s.store.Draft(ctx, d.ID)
	if err != nil {
		return Draft{}, err
	}
	if !exists {
		if cur, err = s.createDraft(ctx, a, d); err != nil {
			return Draft{}, err
		}
	}
	if cur.PendingDelete {
		return Draft{}, ErrDraftDiscarded
	}
	list := slices.Clone(cur.Attachments)
	total := cur.AttachmentBytes()
	for i, f := range c.files {
		att := Attachment{
			Filename:    cleanFilename(f.att.Filename),
			ContentType: mediaType(f.att.ContentType),
			Size:        f.file.size,
			ContentHash: f.file.hash,
		}
		if total+att.Size > MaxAttachmentBytes {
			// dropRest removes its copy.
			c.failed(f.att, ErrAttachTooLarge)
			continue
		}
		err := s.keepIncoming(f.file.path, att.ContentHash)
		c.files[i].file.path = ""
		if err != nil {
			c.failed(f.att, err)
			continue
		}
		// The same file under the same name is attached once.
		if slices.ContainsFunc(list, func(have Attachment) bool { return sameAttachment(have, att) }) {
			continue
		}
		list = append(list, att)
		total += att.Size
	}
	if len(list) == len(cur.Attachments) {
		return cur, nil
	}
	return s.store.SetDraftAttachments(ctx, d.ID, list, s.now())
}

// partOrder is the place of a part in its message, from its number.
func partOrder(part string) int {
	n, _ := strconv.Atoi(part)
	return n
}

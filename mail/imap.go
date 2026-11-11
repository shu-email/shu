package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// imapSession is one short-lived IMAP connection.
type imapSession struct {
	client *imapclient.Client
	caps   imap.CapSet
}

// capHas reports whether the server advertised a capability.
func (s *imapSession) capHas(c imap.Cap) bool { return s.caps.Has(c) }

// dialIMAP connects, logs in and reads the capabilities.
func (s *Service) dialIMAP(ctx context.Context, a Account, password string) (*imapSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := &imapclient.Options{
		TLSConfig:   s.tlsConfigFor(a.IMAP.Host),
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}
	var (
		c   *imapclient.Client
		err error
	)
	if a.IMAP.TLS == TLSImplicit {
		c, err = imapclient.DialTLS(a.IMAP.Addr(), options)
	} else {
		c, err = imapclient.DialStartTLS(a.IMAP.Addr(), options)
	}
	if err != nil {
		return nil, fmt.Errorf("imap: connect %s: %w", a.IMAP.Addr(), err)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	ok := false
	defer func() {
		if !ok {
			stop()
			c.Close()
		}
	}()
	if err := c.WaitGreeting(); err != nil {
		return nil, fmt.Errorf("imap: greeting: %w", err)
	}
	if a.Username == "" {
		return nil, errors.New("imap: the username is empty")
	}
	if err := c.Login(a.Username, password).Wait(); err != nil {
		return nil, fmt.Errorf("imap: login: %w", err)
	}
	caps, err := c.Capability().Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: capabilities: %w", err)
	}
	ok = true
	return &imapSession{client: c, caps: caps}, nil
}

// close logs out, or just hangs up when that is not possible.
func (s *imapSession) close() {
	if s == nil || s.client == nil {
		return
	}
	if err := s.client.Logout().Wait(); err != nil {
		s.client.Close()
	}
}

// listFolders reads the account's mailboxes, their counts and special
// uses.
func (s *Service) listFolders(c *imapSession) ([]Folder, error) {
	// LIST-STATUS and SPECIAL-USE are one request when the server has
	// them; otherwise a plain LIST and a STATUS per mailbox.
	extended := c.capHas(imap.CapListStatus) || c.capHas(imap.CapIMAP4rev2) || c.capHas(imap.CapSpecialUse)
	var data []*imap.ListData
	if extended {
		cmd := c.client.List("", "*", &imap.ListOptions{
			ReturnSpecialUse: true,
			ReturnStatus: &imap.StatusOptions{
				NumMessages: true, NumUnseen: true, UIDValidity: true,
			},
		})
		var err error
		data, err = cmd.Collect()
		if err != nil {
			return nil, fmt.Errorf("imap: list: %w", err)
		}
	} else {
		cmd := c.client.List("", "*", nil)
		var err error
		data, err = cmd.Collect()
		if err != nil {
			return nil, fmt.Errorf("imap: list: %w", err)
		}
	}
	folders := make([]Folder, 0, len(data))
	for _, d := range data {
		if d == nil || d.Mailbox == "" || hasNoSelect(attrsOf(d)) {
			continue
		}
		f := Folder{
			Name:       d.Mailbox,
			SpecialUse: folderSpecialUse(d.Mailbox, attrsOf(d)),
		}
		if d.Delim != 0 {
			f.Delimiter = string(d.Delim)
		}
		if d.Status != nil {
			if d.Status.NumMessages != nil {
				f.Total = int(*d.Status.NumMessages)
			}
			if d.Status.NumUnseen != nil {
				f.Unread = int(*d.Status.NumUnseen)
			}
			f.UIDValidity = d.Status.UIDValidity
		} else if st, err := c.client.Status(d.Mailbox, &imap.StatusOptions{
			NumMessages: true, NumUnseen: true, UIDValidity: true,
		}).Wait(); err == nil {
			if st.NumMessages != nil {
				f.Total = int(*st.NumMessages)
			}
			if st.NumUnseen != nil {
				f.Unread = int(*st.NumUnseen)
			}
			f.UIDValidity = st.UIDValidity
		}
		folders = append(folders, f)
	}
	return folders, nil
}

func attrsOf(d *imap.ListData) []string {
	out := make([]string, 0, len(d.Attrs))
	for _, a := range d.Attrs {
		out = append(out, string(a))
	}
	return out
}

// fetchOptions is the header fetch of the list phase.
func fetchOptions(withPreview bool) *imap.FetchOptions {
	opts := &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		InternalDate:  true,
		RFC822Size:    true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{},
	}
	if withPreview {
		opts.BodySection = []*imap.FetchItemBodySection{{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		}}
	}
	return opts
}

// fetchSummaries reads headers (and a preview) for a number set.
func fetchSummaries(c *imapSession, set imap.NumSet, withPreview bool) ([]Message, []FlagUpdate, error) {
	opts := fetchOptions(withPreview)
	cmd := c.client.Fetch(set, opts)
	var messages []Message
	var flags []FlagUpdate
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		buf, err := data.Collect()
		if err != nil {
			cmd.Close()
			return nil, nil, fmt.Errorf("imap: fetch: %w", err)
		}
		m := summaryFromFetch(buf, withPreview)
		if m.UID == 0 {
			continue
		}
		messages = append(messages, m)
		flags = append(flags, FlagUpdate{UID: m.UID, Seen: m.Seen, Flagged: m.Flagged})
	}
	if err := cmd.Close(); err != nil {
		return nil, nil, fmt.Errorf("imap: fetch: %w", err)
	}
	return messages, flags, nil
}

func summaryFromFetch(buf *imapclient.FetchMessageBuffer, withPreview bool) Message {
	var m Message
	m.UID = UID(buf.UID)
	if buf.Envelope != nil {
		m.Subject = buf.Envelope.Subject
		m.MessageID = buf.Envelope.MessageID
		if len(buf.Envelope.InReplyTo) > 0 {
			m.InReplyTo = buf.Envelope.InReplyTo[0]
		}
		m.From = envelopeAddress(buf.Envelope.From)
		if m.From.Email == "" {
			m.From = envelopeAddress(buf.Envelope.Sender)
		}
		m.To = envelopeAddresses(buf.Envelope.To)
		m.Cc = envelopeAddresses(buf.Envelope.Cc)
		m.ReplyTo = envelopeAddresses(buf.Envelope.ReplyTo)
		m.Date = buf.Envelope.Date
	}
	if m.Date.IsZero() {
		m.Date = buf.InternalDate
	}
	m.Size = buf.RFC822Size
	for _, f := range buf.Flags {
		switch f {
		case imap.FlagSeen:
			m.Seen = true
		case imap.FlagFlagged:
			m.Flagged = true
		}
	}
	if withPreview {
		section := &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		}
		if body := buf.FindBodySection(section); len(body) > 0 {
			m.Preview = preview(normalizeText(string(body)))
		}
	}
	m.HasAttachments = attachmentsInStructure(buf.BodyStructure)
	return m
}

func envelopeAddress(list []imap.Address) Address {
	if len(list) == 0 {
		return Address{}
	}
	a := list[0]
	return Address{Name: a.Name, Email: a.Addr()}
}

func envelopeAddresses(list []imap.Address) []Address {
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Addr()})
	}
	return out
}

// attachmentsInStructure reports whether a body structure has a part
// that is not text and not a multipart container.
func attachmentsInStructure(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			if strings.HasPrefix(single.MediaType(), "text/") {
				return true
			}
			if d := single.Disposition(); d != nil && strings.EqualFold(d.Value, "inline") &&
				strings.HasPrefix(single.MediaType(), "image/") {
				return true
			}
			found = true
		}
		return true
	})
	return found
}

// recentSeqSet selects the newest limit messages by sequence number.
func recentSeqSet(total uint32, limit int) imap.NumSet {
	var set imap.SeqSet
	if total == 0 {
		return set
	}
	start := uint32(1)
	if limit > 0 && int(total) > limit {
		start = total - uint32(limit) + 1
	}
	set.AddRange(start, 0) // 0 is *: the last message
	return set
}

// syncFolder implements the changeset sync described in the docs.
func (s *Service) syncFolder(ctx context.Context, a Account, password, folder string, limit int) error {
	if limit <= 0 {
		limit = defaultSyncCap
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()

	selected, err := c.client.Select(folder, nil).Wait()
	if err != nil {
		return fmt.Errorf("imap: select %s: %w", folder, err)
	}
	status, err := c.client.Status(folder, &imap.StatusOptions{
		NumMessages: true, NumUnseen: true, UIDValidity: true,
	}).Wait()
	if err != nil {
		return fmt.Errorf("imap: status %s: %w", folder, err)
	}
	cached, hasCached, err := s.store.Folder(ctx, a.ID, folder)
	if err != nil {
		return err
	}
	reset := hasCached && cached.UIDValidity != 0 && selected.UIDValidity != 0 && cached.UIDValidity != selected.UIDValidity

	batch := SyncBatch{
		Folder: Folder{
			Name:        folder,
			UIDValidity: selected.UIDValidity,
			SyncedAt:    s.now(),
		},
		Reset: reset,
	}
	if status.NumMessages != nil {
		batch.Folder.Total = int(*status.NumMessages)
	}
	if status.NumUnseen != nil {
		batch.Folder.Unread = int(*status.NumUnseen)
	}
	total := uint32(batch.Folder.Total)

	var set imap.NumSet
	if reset || !hasCached || cached.LastUID == 0 {
		set = recentSeqSet(total, limit)
	} else {
		var uids imap.UIDSet
		uids.AddRange(imap.UID(cached.LastUID+1), 0)
		set = uids
	}
	// A newer UIDVALIDITY with an old cursor is a reset even if the
	// cached row did not have the cursor yet.
	if reset {
		batch.Reset = true
	}
	if total > 0 {
		messages, flags, err := fetchSummaries(c, set, true)
		if err != nil {
			return err
		}
		// Trim to the newest limit even when the server ignored it.
		if len(messages) > limit {
			messages = messages[len(messages)-limit:]
		}
		batch.Messages = messages
		if reset {
			batch.Flags = flags
		}
		for _, m := range messages {
			if m.UID > batch.Folder.LastUID {
				batch.Folder.LastUID = m.UID
			}
		}
	}
	if !reset && hasCached {
		if batch.Folder.LastUID < cached.LastUID {
			batch.Folder.LastUID = cached.LastUID
		}
		// Refresh the flags of the cached window, which the server may
		// have changed from another client.
		if batch.Folder.Total > 0 {
			_, flags, err := fetchSummaries(c, recentSeqSet(total, limit), false)
			if err == nil {
				batch.Flags = flags
			}
		}
	}
	return s.store.ApplySync(ctx, a.ID, batch)
}

// fetchMessage reads and parses one message's body.
func (s *Service) fetchMessage(ctx context.Context, a Account, password, folder string, uid UID, markSeen bool) (FetchResult, error) {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return FetchResult{}, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return FetchResult{}, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	section := &imap.FetchItemBodySection{Peek: true}
	cmd := c.client.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true,
		BodySection: []*imap.FetchItemBodySection{section},
	})
	var buf *imapclient.FetchMessageBuffer
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		b, err := data.Collect()
		if err != nil {
			cmd.Close()
			return FetchResult{}, fmt.Errorf("imap: fetch message: %w", err)
		}
		if buf == nil || UID(b.UID) == uid {
			buf = b
		}
	}
	if err := cmd.Close(); err != nil {
		return FetchResult{}, fmt.Errorf("imap: fetch message: %w", err)
	}
	if buf == nil {
		return FetchResult{}, ErrNotFound
	}
	raw := buf.FindBodySection(section)
	if len(raw) == 0 {
		// Some servers only answer BODY[] when it was not a .PEEK fetch.
		plain := &imap.FetchItemBodySection{}
		cmd := c.client.Fetch(set, &imap.FetchOptions{
			UID: true, BodySection: []*imap.FetchItemBodySection{plain},
		})
		for {
			data := cmd.Next()
			if data == nil {
				break
			}
			b, err := data.Collect()
			if err != nil {
				break
			}
			if UID(b.UID) == uid {
				raw = b.FindBodySection(plain)
			}
		}
		cmd.Close()
	}
	if len(raw) == 0 {
		return FetchResult{}, errors.New("imap: the server sent no message body")
	}
	msg, err := ParseMessage(raw)
	if err != nil {
		return FetchResult{}, fmt.Errorf("mime: %w", err)
	}
	msg.UID = uid
	msg.Seen = msg.Seen || bufFlag(buf, imap.FlagSeen)
	msg.Flagged = msg.Flagged || bufFlag(buf, imap.FlagFlagged)
	if buf.Envelope != nil && msg.Subject == "" {
		msg.Subject = buf.Envelope.Subject
	}
	if buf.Envelope != nil {
		if msg.MessageID == "" {
			msg.MessageID = buf.Envelope.MessageID
		}
		if len(msg.To) == 0 {
			msg.To = envelopeAddresses(buf.Envelope.To)
		}
		if len(msg.Cc) == 0 {
			msg.Cc = envelopeAddresses(buf.Envelope.Cc)
		}
		if msg.From.Email == "" {
			msg.From = envelopeAddress(buf.Envelope.From)
		}
	}
	result := FetchResult{Message: msg}
	if markSeen && !bufFlag(buf, imap.FlagSeen) {
		if err := setFlags(c, folder, uid, FlagSeen, true); err != nil {
			result.SeenErr = err
		} else {
			result.Message.Seen = true
		}
	}
	// Cache attachment bytes on disk, keyed by content hash.
	for i := range result.Message.Attachments {
		a := &result.Message.Attachments[i]
		if err := s.cacheAttachment(a); err != nil {
			// The metadata is still useful; fetch the bytes later.
			a.CachePath = ""
		}
	}
	return result, nil
}

func strips(s string) string { return strings.TrimSpace(s) }

func bufFlag(buf *imapclient.FetchMessageBuffer, flag imap.Flag) bool {
	for _, f := range buf.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

// storeFlags changes a flag on the server.
func (s *Service) storeFlags(ctx context.Context, a Account, password, folder string, uid UID, flag Flag, on bool) error {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	return setFlags(c, folder, uid, flag, on)
}

func setFlags(c *imapSession, folder string, uid UID, flag Flag, on bool) error {
	if _, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: false}).Wait(); err != nil {
		return fmt.Errorf("imap: select %s: %w", folder, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	op := imap.StoreFlagsAdd
	if !on {
		op = imap.StoreFlagsDel
	}
	cmd := c.client.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.Flag(flag)}}, nil)
	return cmd.Close()
}

// moveOnServer moves one message, preferring MOVE and falling back to
// COPY plus delete and expunge.
func (s *Service) moveOnServer(ctx context.Context, a Account, password, folder string, uid UID, dest string) error {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap: select %s: %w", folder, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	if c.capHas(imap.CapMove) {
		if _, err := c.client.Move(set, dest).Wait(); err != nil {
			return fmt.Errorf("imap: move: %w", err)
		}
		return nil
	}
	if _, err := c.client.Copy(set, dest).Wait(); err != nil {
		return fmt.Errorf("imap: copy: %w", err)
	}
	cmd := c.client.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil)
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("imap: mark deleted: %w", err)
	}
	if c.capHas(imap.CapUIDPlus) {
		if err := c.client.UIDExpunge(set).Close(); err != nil {
			return fmt.Errorf("imap: expunge: %w", err)
		}
		return nil
	}
	return c.client.Expunge().Close()
}

// appendTo appends a raw message to a folder.
func appendTo(c *imapSession, folder string, raw []byte, now time.Time) error {
	cmd := c.client.Append(folder, int64(len(raw)), &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagSeen},
		Time:  now,
	})
	if _, err := cmd.Write(raw); err != nil {
		cmd.Close()
		return err
	}
	if err := cmd.Close(); err != nil {
		return err
	}
	_, err := cmd.Wait()
	return err
}

// tlsConfig returns the TLS configuration to dial servers with.
func (s *Service) tlsConfigFor(host string) *tls.Config {
	if s.tlsConfig != nil {
		cfg := s.tlsConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
		return cfg
	}
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

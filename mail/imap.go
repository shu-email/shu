package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// imapSession is one IMAP connection: a short one for most operations, a
// lasting one for a watcher.
type imapSession struct {
	client *imapclient.Client
	caps   imap.CapSet
	// release ends the closing of the connection with the context it was
	// dialed under. Only a connection that outlives one operation needs
	// it called.
	release func()
}

// whenDone runs fn when ctx ends, until release is called. release waits
// for a call of fn that has begun, so none is left running behind it.
func whenDone(ctx context.Context, fn func()) (release func()) {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		fn()
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

// capHas reports whether the server advertised a capability.
func (s *imapSession) capHas(c imap.Cap) bool { return s.caps.Has(c) }

// dialTimeout bounds reaching a server and its TLS handshake.
const dialTimeout = 30 * time.Second

// loginError is a sign-in the server refused. Trying again does not help
// until the user changes something, which a watcher has to know.
type loginError struct{ err error }

func (e *loginError) Error() string { return "imap: login: " + e.err.Error() }
func (e *loginError) Unwrap() error { return e.err }

// connect reaches the server and starts TLS. Cancelling ctx gives up at
// once, which the library's own dialing would not: a watcher that is
// stopped must not wait out a timeout for a server that does not answer.
func connect(ctx context.Context, a Account, options *imapclient.Options, dial func(context.Context, string, int) (net.Conn, error)) (*imapclient.Client, error) {
	if a.IMAP.TLS == TLSImplicit {
		cfg := options.TLSConfig.Clone()
		if cfg.NextProtos == nil {
			cfg.NextProtos = []string{"imap"}
		}
		conn, err := dial(ctx, a.IMAP.Host, a.IMAP.Port)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return imapclient.New(tlsConn, options), nil
	}
	conn, err := dial(ctx, a.IMAP.Host, a.IMAP.Port)
	if err != nil {
		return nil, err
	}
	// The STARTTLS exchange takes no context: hanging up is what ends it.
	release := whenDone(ctx, func() { conn.Close() })
	defer release()
	return imapclient.NewStartTLS(conn, options)
}

// dialIMAP connects, logs in and reads the capabilities.
func (s *Service) dialIMAP(ctx context.Context, a Account, password string) (*imapSession, error) {
	return s.dialIMAPWith(ctx, a, password, nil)
}

// dialIMAPWith is dialIMAP for a connection that stays open: handler
// hears what the server says of the selected mailbox between commands.
// The connection is closed when ctx ends. An account that signs in with
// OAuth takes no password: it signs in with its access token, and when
// the server refuses one that had not expired, as one revoked early,
// with a renewed one once more.
func (s *Service) dialIMAPWith(ctx context.Context, a Account, password string, handler *imapclient.UnilateralDataHandler) (*imapSession, error) {
	if a.OAuth == "" {
		return s.dialIMAPAs(ctx, a, password, handler)
	}
	token, err := s.accessToken(ctx, a, "")
	if err != nil {
		return nil, err
	}
	c, err := s.dialIMAPAs(ctx, a, token, handler)
	var refused *loginError
	if !errors.As(err, &refused) {
		return c, err
	}
	if token, err = s.accessToken(ctx, a, token); err != nil {
		return nil, err
	}
	return s.dialIMAPAs(ctx, a, token, handler)
}

// dialIMAPAs connects and signs in with secret: the account's password,
// or its access token.
func (s *Service) dialIMAPAs(ctx context.Context, a Account, secret string, handler *imapclient.UnilateralDataHandler) (*imapSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := &imapclient.Options{
		TLSConfig:             s.tlsConfigFor(ctx, a.IMAP.Host),
		WordDecoder:           &mime.WordDecoder{CharsetReader: charset.Reader},
		UnilateralDataHandler: handler,
	}
	c, err := connect(ctx, a, options, s.dialNetwork)
	if err != nil {
		return nil, fmt.Errorf("imap: connect %s: %w", a.IMAP.Addr(), err)
	}
	release := whenDone(ctx, func() { c.Close() })
	ok := false
	defer func() {
		if !ok {
			release()
			c.Close()
		}
	}()
	if err := c.WaitGreeting(); err != nil {
		return nil, fmt.Errorf("imap: greeting: %w", err)
	}
	if a.Username == "" {
		return nil, errors.New("imap: the username is empty")
	}
	if err := signIn(c, a, secret); err != nil {
		// A connection that broke during the exchange is no refusal.
		var refused *imap.Error
		if errors.As(err, &refused) {
			return nil, &loginError{err}
		}
		return nil, fmt.Errorf("imap: login: %w", err)
	}
	caps, err := c.Capability().Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: capabilities: %w", err)
	}
	ok = true
	return &imapSession{client: c, caps: caps, release: release}, nil
}

// signIn logs in with the password, or authenticates with XOAUTH2 and
// the access token, which Gmail and Outlook both take.
func signIn(c *imapclient.Client, a Account, secret string) error {
	if a.OAuth == "" {
		return c.Login(a.Username, secret).Wait()
	}
	if !c.Caps().Has(imap.AuthCap(xoauth2Mechanism)) {
		return errors.New("the server does not take OAuth tokens (XOAUTH2)")
	}
	return c.Authenticate(&xoauth2Client{user: a.Username, token: secret})
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
	subscribed, knowsSubscriptions := subscribedFolders(c)
	folders := make([]Folder, 0, len(data))
	for _, d := range data {
		if d == nil || d.Mailbox == "" || hasNoSelect(attrsOf(d)) {
			continue
		}
		f := Folder{
			Name:       d.Mailbox,
			SpecialUse: folderSpecialUse(d.Mailbox, attrsOf(d)),
			Subscribed: !knowsSubscriptions || subscribed[d.Mailbox],
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

// subscribedFolders asks a server that can tell which folders the user
// subscribed to. A server without LIST-EXTENDED keeps them all, and the
// second value says so.
func subscribedFolders(c *imapSession) (map[string]bool, bool) {
	if !c.capHas(imap.CapListExtended) && !c.capHas(imap.CapIMAP4rev2) {
		return nil, false
	}
	cmd := c.client.List("", "*", &imap.ListOptions{SelectSubscribed: true})
	data, err := cmd.Collect()
	if err != nil {
		return nil, false
	}
	set := make(map[string]bool, len(data))
	for _, d := range data {
		if d != nil && d.Mailbox != "" {
			set[d.Mailbox] = true
		}
	}
	return set, true
}

func attrsOf(d *imap.ListData) []string {
	out := make([]string, 0, len(d.Attrs))
	for _, a := range d.Attrs {
		out = append(out, string(a))
	}
	return out
}

// syncHeaders are the headers a sync keeps with every message it reads:
// the threading headers the envelope lacks, which has no References or
// Thread-Index and parses In-Reply-To strictly, and what tells the
// sender's authentication and how the message's list is left (see Trust
// and Security), so that a cache read of an unopened message has them.
var syncHeaders = slices.Concat(
	[]string{"References", "In-Reply-To", "Thread-Index"},
	securityHeaders, unsubscribeHeaders,
)

// threadSection is the part of the header a sync reads besides the
// envelope (see syncHeaders). PEEK, as reading must never mark mail read.
func threadSection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: syncHeaders,
		Peek:         true,
	}
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
		BodySection:   []*imap.FetchItemBodySection{threadSection()},
	}
	if withPreview {
		opts.BodySection = append(opts.BodySection, &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		})
	}
	return opts
}

// threadHeaders reads the threading headers of a fetch into m.
func threadHeaders(m *Message, raw []byte) {
	if len(raw) == 0 {
		return
	}
	// The blank line that ends a header block may be missing.
	block := append(append([]byte(nil), raw...), "\r\n\r\n"...)
	msg, err := mail.ReadMessage(bytes.NewReader(block))
	if err != nil {
		return
	}
	m.References = msgIDs(msg.Header.Get("References"))
	if ids := msgIDs(msg.Header.Get("In-Reply-To")); m.InReplyTo == "" && len(ids) > 0 {
		m.InReplyTo = ids[0]
	}
	m.ThreadIndex = cleanThreadIndex(msg.Header.Get("Thread-Index"))
	m.SetRawHeaders(headerLines(raw, syncHeaders))
}

// fetchSummaries reads headers (and a preview) for a number set.
//
// An update the server sends unasked while the fetch runs, as it may on
// a connection that stays on a folder, is taken by the IMAP library for
// the fetch's answer about that message, and the answer proper is then
// dropped: the message comes with its flags and without its headers. A
// fetch that got such an answer is made once more, when the update is
// out of the way. A message still without its envelope then is kept as
// it is, as it always was: one odd message must not stop a folder from
// syncing, and opening it reads its headers with its body.
func fetchSummaries(c *imapSession, set imap.NumSet, withPreview bool) ([]Message, []FlagUpdate, error) {
	messages, flags, whole, err := fetchSummariesOnce(c, set, withPreview, false)
	if err == nil && !whole {
		messages, flags, _, err = fetchSummariesOnce(c, set, withPreview, true)
	}
	return messages, flags, err
}

// fetchSummariesOnce is one fetch of fetchSummaries. whole is false when
// an answer lacked the UID or the envelope that were asked for. A message
// without a UID is left out; one without an envelope is too, unless
// lenient.
func fetchSummariesOnce(c *imapSession, set imap.NumSet, withPreview, lenient bool) (messages []Message, flags []FlagUpdate, whole bool, err error) {
	opts := fetchOptions(withPreview)
	cmd := c.client.Fetch(set, opts)
	whole = true
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		buf, err := data.Collect()
		if err != nil {
			cmd.Close()
			return nil, nil, false, fmt.Errorf("imap: fetch: %w", err)
		}
		m := summaryFromFetch(buf, withPreview)
		if m.UID == 0 || buf.Envelope == nil {
			whole = false
			if m.UID == 0 || !lenient {
				continue
			}
		}
		messages = append(messages, m)
		flags = append(flags, FlagUpdate{UID: m.UID, Seen: m.Seen, Flagged: m.Flagged, Keywords: m.Keywords})
	}
	if err := cmd.Close(); err != nil {
		return nil, nil, false, fmt.Errorf("imap: fetch: %w", err)
	}
	return messages, flags, whole, nil
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
	threadHeaders(&m, buf.FindBodySection(threadSection()))
	if m.Date.IsZero() {
		m.Date = buf.InternalDate
	}
	m.Size = buf.RFC822Size
	m.Seen, m.Flagged, m.Keywords = readFlags(buf.Flags)
	if withPreview {
		section := &imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Partial:   &imap.SectionPartial{Offset: 0, Size: 256},
			Peek:      true,
		}
		if body := buf.FindBodySection(section); len(body) > 0 {
			m.Preview = preview(previewText(body))
		}
	}
	m.HasAttachments = attachmentsInStructure(buf.BodyStructure)
	return m
}

// readFlags reads the flags a fetch gave a message: whether it is read
// and flagged, and its keywords, which are never nil.
func readFlags(flags []imap.Flag) (seen, flagged bool, keywords []string) {
	keywords = []string{}
	for _, f := range flags {
		switch {
		case f == imap.FlagSeen:
			seen = true
		case f == imap.FlagFlagged:
			flagged = true
		case !strings.HasPrefix(string(f), `\`):
			keywords = append(keywords, string(f))
		}
	}
	return seen, flagged, keywords
}

// previewText turns a partial BODY[TEXT] into readable text. A multipart
// body starts at its first boundary, so the first text part is read
// directly instead of handing a truncated message to the MIME parser.
func previewText(raw []byte) string {
	s := string(raw)
	trimmed := strings.TrimLeft(s, "\r\n \t")
	if strings.HasPrefix(trimmed, "--") {
		type part struct {
			ctype string
			body  string
		}
		var (
			parts     []part
			cur       *part
			inHeaders bool
		)
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "--") {
				if cur != nil {
					parts = append(parts, *cur)
					cur = nil
				}
				inHeaders = true
				continue
			}
			if cur == nil {
				cur = &part{}
				inHeaders = true
			}
			if inHeaders {
				if strings.TrimSpace(line) == "" {
					inHeaders = false
					continue
				}
				if strings.HasPrefix(strings.ToLower(line), "content-type:") {
					cur.ctype = strings.ToLower(strings.TrimSpace(line[len("content-type:"):]))
				}
				continue
			}
			cur.body += line + "\n"
		}
		if cur != nil {
			parts = append(parts, *cur)
		}
		for _, p := range parts {
			if strings.HasPrefix(p.ctype, "text/plain") {
				return normalizeText(p.body)
			}
		}
		for _, p := range parts {
			if strings.HasPrefix(p.ctype, "text/html") {
				return HTMLToText(p.body)
			}
		}
		return ""
	}
	if strings.HasPrefix(trimmed, "<") {
		return HTMLToText(s)
	}
	return normalizeText(s)
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

// syncFolder implements the changeset sync described in the docs. A
// limit above zero is how many of the newest messages a first sync
// fetches and the sync refreshes; else the settings' window is.
func (s *Service) syncFolder(ctx context.Context, a Account, password, folder string, limit int) error {
	if limit <= 0 {
		limit = s.firstSyncWindow(ctx)
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
	_, err = s.syncSelected(ctx, c, a.ID, folder, selected.UIDValidity, permanentFlags(selected), windowSpan(limit))
	return err
}

// syncSpan is how much of a folder a sync reads: the newest first
// messages when it has none cached or they were reset, the flags and
// threading headers of the newest refresh, and of the messages that
// arrived since the last sync the newest keep. What keep leaves out is a
// gap FetchOlder fills. sorts has the rules sort what arrived (see
// arrivals.go).
type syncSpan struct {
	first, refresh, keep int
	sorts                bool
}

// windowSpan is the span of a sync that caches and refreshes the newest
// window messages and keeps up to defaultSyncCap that arrived, which the
// rules sort.
func windowSpan(window int) syncSpan {
	return syncSpan{first: window, refresh: window, keep: max(window, defaultSyncCap), sorts: true}
}

// syncSelected syncs the folder a connection has selected, under the
// UIDVALIDITY the server gave when it was, and returns how many messages
// it wrote. It only reads, so it works as well on a folder opened
// read-only. permanent is what a SELECT said of the flags the folder
// keeps, which the batch records; it is nil for a folder opened
// read-only, which says nothing of them.
func (s *Service) syncSelected(ctx context.Context, c *imapSession, accountID, folder string, uidValidity uint32, permanent []string, span syncSpan) (int, error) {
	status, err := c.client.Status(folder, &imap.StatusOptions{
		NumMessages: true, NumUnseen: true, UIDValidity: true,
	}).Wait()
	if err != nil {
		return 0, fmt.Errorf("imap: status %s: %w", folder, err)
	}
	cached, hasCached, err := s.store.Folder(ctx, accountID, folder)
	if err != nil {
		return 0, err
	}
	reset := hasCached && cached.UIDValidity != 0 && uidValidity != 0 && cached.UIDValidity != uidValidity

	batch := SyncBatch{
		Folder: Folder{
			Name:           folder,
			UIDValidity:    uidValidity,
			SyncedAt:       s.now(),
			PermanentFlags: permanent,
		},
		Reset: reset,
	}
	setCounts(&batch.Folder, status)
	total := uint32(batch.Folder.Total)

	var set imap.NumSet
	keep := span.keep
	if reset || !hasCached || cached.LastUID == 0 {
		set = recentSeqSet(total, span.first)
		keep = span.first
		// The cache is complete when this reads the folder whole.
		batch.Folder.Complete = int(total) <= span.first
	} else {
		var uids imap.UIDSet
		uids.AddRange(imap.UID(cached.LastUID+1), 0)
		set = uids
		batch.Folder.Complete = cached.Complete
	}
	// A newer UIDVALIDITY with an old cursor is a reset even if the
	// cached row did not have the cursor yet.
	if reset {
		batch.Reset = true
	}
	if total > 0 {
		messages, flags, err := fetchSummaries(c, set, false)
		if err != nil {
			return 0, err
		}
		// Trim to the newest ones even when the server ignored the limit.
		// What goes is mail the cache lacks.
		if len(messages) > keep {
			messages = messages[len(messages)-keep:]
			batch.Folder.Complete = false
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
	if len(batch.Messages) > 0 {
		// Mail that reached the folder after the STATUS above was fetched
		// with the rest, and the counts it gave leave that out: the batch
		// would write an unread message its folder does not count, and a
		// watcher announce it beside that count. They are read again now
		// that the new mail is in.
		status, err := c.client.Status(folder, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err != nil {
			return 0, fmt.Errorf("imap: status %s: %w", folder, err)
		}
		setCounts(&batch.Folder, status)
		total = uint32(batch.Folder.Total)
	}
	if !reset && hasCached {
		if batch.Folder.LastUID < cached.LastUID {
			batch.Folder.LastUID = cached.LastUID
		}
		// Refresh the flags of the cached window, which the server may
		// have changed from another client, and the headers that thread
		// it, which a cache from before conversations lacks.
		if batch.Folder.Total > 0 {
			refreshed, flags, err := fetchSummaries(c, recentSeqSet(total, span.refresh), false)
			if err == nil {
				batch.Flags = flags
				for _, m := range refreshed {
					batch.Threading = append(batch.Threading, ThreadHeaders{
						UID: m.UID, MessageID: m.MessageID, InReplyTo: m.InReplyTo,
						References: m.References, ThreadIndex: m.ThreadIndex,
					})
				}
			}
		}
	}
	if total > 0 {
		addPreviews(c, set, batch.Messages)
	}
	if span.sorts && !reset && hasCached && !cached.SyncedAt.IsZero() && len(batch.Messages) > 0 {
		s.addRuleHeaders(ctx, c, accountID, cached, batch.Messages)
	}
	return len(batch.Messages), s.writeSync(ctx, accountID, batch, span.sorts)
}

// setCounts takes a folder's counts from what a STATUS said of it.
func setCounts(f *Folder, status *imap.StatusData) {
	if status.NumMessages != nil {
		f.Total = int(*status.NumMessages)
	}
	if status.NumUnseen != nil {
		f.Unread = int(*status.NumUnseen)
	}
}

// addPreviews fetches the start of the body of each message of set and
// puts it in that message's preview, among messages. Previews are a
// nicety: a server that cannot do a partial body fetch does not fail the
// fetch they are part of.
func addPreviews(c *imapSession, set imap.NumSet, messages []Message) {
	if len(messages) == 0 {
		return
	}
	previews, _, err := fetchSummaries(c, set, true)
	if err != nil {
		return
	}
	byUID := make(map[UID]string, len(previews))
	for _, p := range previews {
		byUID[p.UID] = p.Preview
	}
	for i := range messages {
		if p := byUID[messages[i].UID]; p != "" {
			messages[i].Preview = p
		}
	}
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
	buf, raw, err := fetchRaw(c, uid)
	if err != nil {
		return FetchResult{}, err
	}
	msg, err := ParseMessage(raw)
	if err != nil {
		return FetchResult{}, fmt.Errorf("mime: %w", err)
	}
	msg.UID = uid
	msg.SetRawHeaders(s.withRuleHeaders(ctx, a.ID, msg.RawHeaders, raw))
	seen, flagged, keywords := readFlags(buf.Flags)
	msg.Seen = msg.Seen || seen
	msg.Flagged = msg.Flagged || flagged
	msg.Keywords = keywords
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

// fetchRaw reads one message of the selected folder whole, with its
// flags and envelope.
func fetchRaw(c *imapSession, uid UID) (*imapclient.FetchMessageBuffer, []byte, error) {
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
			return nil, nil, fmt.Errorf("imap: fetch message: %w", err)
		}
		if buf == nil || UID(b.UID) == uid {
			buf = b
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, nil, fmt.Errorf("imap: fetch message: %w", err)
	}
	if buf == nil {
		return nil, nil, ErrNotFound
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
		return nil, nil, errors.New("imap: the server sent no message body")
	}
	return buf, raw, nil
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

// errValidityChanged is a folder whose UIDs no longer mean what the cache
// says they do: the server gave it a new UIDVALIDITY.
var errValidityChanged = errors.New("imap: the folder changed on the server; sync it first")

// storeFlagsIn changes a flag on messages of one folder, in one STORE,
// after checking that the folder's UIDVALIDITY is still validity.
func storeFlagsIn(c *imapSession, folder string, validity uint32, uids []UID, flag Flag, on bool) error {
	selected, err := c.client.Select(folder, &imap.SelectOptions{ReadOnly: false}).Wait()
	if err != nil {
		return fmt.Errorf("imap: select %s: %w", folder, err)
	}
	if validity != 0 && selected.UIDValidity != validity {
		return fmt.Errorf("%w (%s)", errValidityChanged, folder)
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	op := imap.StoreFlagsAdd
	if !on {
		op = imap.StoreFlagsDel
	}
	if err := c.client.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.Flag(flag)}}, nil).Close(); err != nil {
		return fmt.Errorf("imap: store %s: %w", folder, err)
	}
	return nil
}

// moveOnServer moves messages of one folder in one command, preferring
// MOVE and falling back to COPY plus delete and expunge. The client's own
// Move would fall back with a bare EXPUNGE, so it is only called on a
// server that has MOVE.
func (s *Service) moveOnServer(ctx context.Context, a Account, password, folder string, uids []UID, dest string) ([]MoveResult, error) {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return nil, err
	}
	defer c.close()
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap: select %s: %w", folder, err)
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	forget := s.expectArrivals(ctx, c, a.ID, uids, dest)
	if c.capHas(imap.CapMove) {
		data, err := c.client.Move(set, dest).Wait()
		if err != nil {
			forget()
			return nil, fmt.Errorf("imap: move: %w", err)
		}
		return movedTo(folder, dest, uids, data.SourceUIDs, data.DestUIDs), nil
	}
	data, err := c.client.Copy(set, dest).Wait()
	if err != nil {
		forget()
		return nil, fmt.Errorf("imap: copy: %w", err)
	}
	results := movedTo(folder, dest, uids, data.SourceUIDs, data.DestUIDs)
	return results, expunge(c, folder, set)
}

// movedTo pairs the messages moved with the UIDs the server gave them in
// dest. Its COPYUID answer lists the source and destination UIDs in the
// same order; a server without UIDPLUS gives none, and those moves cannot
// be undone.
func movedTo(from, dest string, uids []UID, source, dests imap.NumSet) []MoveResult {
	src, dst := uidList(source), uidList(dests)
	destOf := map[UID]UID{}
	if len(src) == len(dst) {
		for i := range src {
			destOf[UID(src[i])] = UID(dst[i])
		}
	}
	out := make([]MoveResult, 0, len(uids))
	for _, uid := range uids {
		out = append(out, MoveResult{Folder: dest, DestUID: destOf[uid], From: from, UID: uid})
	}
	return out
}

// uidList reads the UIDs of a COPYUID answer in their order.
func uidList(set imap.NumSet) []imap.UID {
	uids, ok := set.(imap.UIDSet)
	if !ok {
		return nil
	}
	nums, ok := uids.Nums()
	if !ok {
		return nil
	}
	return nums
}

// expunge removes messages of the selected folder for good. UID EXPUNGE
// removes those and no others; a server without it gets expungeOnly.
func expunge(c *imapSession, folder string, set imap.UIDSet) error {
	deleted := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if !c.capHas(imap.CapUIDPlus) && !c.capHas(imap.CapIMAP4rev2) {
		return expungeOnly(c, folder, set, deleted)
	}
	if err := c.client.Store(set, deleted, nil).Close(); err != nil {
		return fmt.Errorf("imap: mark deleted: %w", err)
	}
	if err := c.client.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("imap: expunge: %w", err)
	}
	return nil
}

// expungeOnly removes the given messages on a server without UID EXPUNGE.
// Its EXPUNGE removes every message flagged \Deleted, and another mail
// client may have flagged some it has not removed yet, which the user can
// still undelete there. Those are unflagged for the EXPUNGE and flagged
// again after it. If the connection is lost in between they stay
// unflagged, which keeps them: the safe way for this to fail.
func expungeOnly(c *imapSession, folder string, set imap.UIDSet, deleted *imap.StoreFlags) error {
	data, err := c.client.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagDeleted}}, nil).Wait()
	if err != nil {
		return fmt.Errorf("imap: search %s: %w", folder, err)
	}
	var others imap.UIDSet
	for _, uid := range data.AllUIDs() {
		if !set.Contains(uid) {
			others.AddNum(uid)
		}
	}
	if len(others) > 0 {
		undelete := &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
		if err := c.client.Store(others, undelete, nil).Close(); err != nil {
			// Nothing was removed yet, and nothing will be this time.
			return fmt.Errorf("imap: unmark deleted: %w", err)
		}
	}
	err = c.client.Store(set, deleted, nil).Close()
	if err == nil {
		err = c.client.Expunge().Close()
	}
	if len(others) > 0 {
		// Whatever happened above, the other client's marks go back.
		if flagErr := c.client.Store(others, deleted, nil).Close(); flagErr != nil && err == nil {
			err = fmt.Errorf("imap: mark deleted again: %w", flagErr)
		}
	}
	if err != nil {
		return fmt.Errorf("imap: expunge: %w", err)
	}
	return nil
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

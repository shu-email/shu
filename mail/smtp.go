package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	gomail "github.com/wneessen/go-mail"
	"github.com/wneessen/go-mail/smtp"
)

// ErrDeliveryUnknown is a send that stopped once the message had begun
// to leave, without the server's answer to it: one cancelled or cut off
// on the way. The server may or may not have the message. The outbox
// keeps it as StateUnknown, and nothing sends it again on its own.
var ErrDeliveryUnknown = errors.New("mail: the message may or may not have been delivered")

// RejectedError is the SMTP server refusing a message, in its own words:
// too large, a recipient it does not know, a policy. The message was not
// delivered.
type RejectedError struct {
	Code  int
	Reply string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("smtp: the server refused the message: %d %s", e.Code, e.Reply)
}

// TooLarge reports whether the server gave the message's size as its
// reason, by the codes RFC 5321 and RFC 3463 have for that.
func (e *RejectedError) TooLarge() bool {
	return e.Code == 552 || strings.HasPrefix(e.Reply, "5.3.4") || strings.HasPrefix(e.Reply, "5.2.3")
}

// SizeLimitError is a message larger than the server announced it takes.
// It is not sent: the server would read all of it and then refuse it.
type SizeLimitError struct {
	Size  int64
	Limit int64
}

func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("smtp: the message is %d bytes and the server takes at most %d", e.Size, e.Limit)
}

// The time a send may wait. go-mail gives the whole exchange one deadline,
// its connection timeout, which a message with attachments on a slow line
// outlives; here each block written gets its own, and the server gets the
// ten minutes RFC 5321 asks for to answer the end of the message, as
// giving up early there is what delivers a message twice.
const (
	smtpBlock         = 32 << 10
	smtpAnswerTimeout = 10 * time.Minute
)

// smtpBlockTimeout is how long one block of a message may take to be
// written. A test replaces it.
var smtpBlockTimeout = time.Minute

type sendProgressKey struct{}

// WithSendProgress returns a context under which Send and RetryPending
// report how much of the message they have handed to the SMTP connection,
// of how much. The counts never go back, and reach the total once the
// server has taken the message. report runs on the sending goroutine.
func WithSendProgress(ctx context.Context, report func(sent, total int64)) context.Context {
	return context.WithValue(ctx, sendProgressKey{}, report)
}

// SendProgress is the function WithSendProgress put in ctx, or nil.
func SendProgress(ctx context.Context) func(sent, total int64) {
	report, _ := ctx.Value(sendProgressKey{}).(func(sent, total int64))
	return report
}

// Send builds the raw message, writes the outbox row, sends over SMTP and
// appends a copy to Sent. The outbox makes a crash recoverable and keeps
// the client from sending the same message twice on its own. Cancelling
// ctx stops a send under way; the error then tells whether anything left
// (ErrDeliveryUnknown) or not. It sends at once: Schedule is the send
// with a delay.
func (s *Service) Send(ctx context.Context, accountID string, out Outgoing) (SendResult, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return SendResult{}, err
	}
	now := s.now()
	if out, err = s.prepare(ctx, a, out); err != nil {
		return SendResult{}, err
	}
	msg, raw, err := s.buildMessage(a, out, now)
	if err != nil {
		return SendResult{}, err
	}
	pending := PendingSend{
		ID:        newOutboxID(),
		AccountID: accountID,
		Raw:       raw,
		Envelope:  out,
		State:     StatePending,
		CreatedAt: now,
	}
	if err := s.enqueue(ctx, &pending); err != nil {
		return SendResult{}, err
	}
	if err := s.deliver(ctx, a, password, pending.ID, msg, raw); err != nil {
		return SendResult{}, err
	}
	pending.State = StateSent
	pending.SentAt = now
	result := SendResult{Outbox: pending}
	// The message is out, so its draft is done: it leaves this device
	// now, and the server when the server can be reached. A context
	// cancelled since does not undo that.
	sentDraft := s.retireDraft(context.WithoutCancel(ctx), accountID, out.DraftID)
	superseded := s.supersede(context.WithoutCancel(ctx), accountID, out.MessageID, pending.ID)
	s.afterDelivery(ctx, a, password, raw, out.Bcc, sentDraft || superseded || len(out.Attachments) > 0, &result)
	if sentDraft {
		s.flushDraftDeletions(ctx, accountID)
	}
	return result, nil
}

// prepare readies a message to be built: it has someone to go to, the
// Message-ID of the draft it was written as or a new one, and the address
// of the identity it goes from.
func (s *Service) prepare(ctx context.Context, a Account, out Outgoing) (Outgoing, error) {
	if len(out.To) == 0 {
		return out, errors.New("mail: the message has no recipients")
	}
	from, err := s.identity(ctx, a, out.Identity)
	if err != nil {
		return out, err
	}
	out.Identity, out.From = from.ID, from.Address()
	if out.MessageID == "" {
		// A draft goes out under the Message-ID its server copies carry.
		out.MessageID = s.draftMessageID(ctx, a.ID, out.DraftID)
	}
	if out.MessageID == "" {
		// Under the domain the message is from, which an alias may not
		// share with the account.
		out.MessageID = newMessageID(from.Email)
	}
	return out, nil
}

// supersede closes the outbox entries of the earlier tries of a message
// that is now delivered: the same draft, sent before and refused or cut
// off, or still waiting out its delay, under the same Message-ID. Sending
// one of those again would deliver the message twice, in a form the user
// has since changed, and until it is closed it holds on to the files that
// form had attached. It reports whether there was one.
func (s *Service) supersede(ctx context.Context, accountID, messageID, delivered string) bool {
	if messageID == "" {
		return false
	}
	closed := s.cancelScheduled(ctx, accountID, messageID, delivered)
	pending, err := s.store.PendingSends(ctx, accountID)
	if err != nil {
		return closed
	}
	for _, p := range pending {
		if p.ID == delivered || p.Envelope.MessageID != messageID {
			continue
		}
		if s.store.SetOutboxState(ctx, p.ID, StateCancelled, "") == nil {
			closed = true
		}
	}
	return closed
}

// enqueue writes the outbox row of a message about to be sent. One with
// attachments is kept as its envelope alone, which names the files: their
// bytes do not go into the database, and the message is built again if
// it is ever resent. The row is what keeps those files from the sweep
// from here on, so it is written in one step with the check that they
// are there.
func (s *Service) enqueue(ctx context.Context, pending *PendingSend) error {
	if len(pending.Envelope.Attachments) == 0 {
		return s.store.EnqueueOutbox(ctx, *pending)
	}
	pending.Raw = nil
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if err := s.checkAttachments(pending.Envelope.Attachments); err != nil {
		return err
	}
	return s.store.EnqueueOutbox(ctx, *pending)
}

// deliver sends a message whose outbox row is written, and records in
// the row how it ended.
func (s *Service) deliver(ctx context.Context, a Account, password, id string, msg *gomail.Msg, raw []byte) error {
	if err := s.store.SetOutboxState(context.WithoutCancel(ctx), id, StateSending, ""); err != nil {
		return err
	}
	return s.deliverClaimed(ctx, a, password, id, msg, raw)
}

// deliverClaimed sends a message whose outbox row is in the sending
// state, and records in the row how it ended.
func (s *Service) deliverClaimed(ctx context.Context, a Account, password, id string, msg *gomail.Msg, raw []byte) error {
	// A send that was cancelled still has an outcome to record.
	record := context.WithoutCancel(ctx)
	left, err := s.transmit(ctx, a, password, msg, raw)
	switch {
	case err == nil:
		return s.store.SetOutboxState(record, id, StateSent, "")
	case left:
		_ = s.store.SetOutboxState(record, id, StateUnknown, err.Error())
		return fmt.Errorf("%w: %w", ErrDeliveryUnknown, err)
	case errors.Is(ctx.Err(), context.Canceled):
		// Stopped by the user before any of it left.
		_ = s.store.SetOutboxState(record, id, StateCancelled, "")
	default:
		_ = s.store.SetOutboxState(record, id, StateFailed, err.Error())
	}
	return err
}

// transmit hands a message to the account's SMTP server. go-mail connects,
// secures the connection and logs in; the message itself is written here,
// from the raw form already built, so that it is encoded once, its way
// out can be counted, and a cancelled context stops it. left reports that
// part of the message went out and the server did not answer to it: it
// may or may not have been delivered.
func (s *Service) transmit(ctx context.Context, a Account, password string, msg *gomail.Msg, raw []byte) (left bool, err error) {
	// The envelope's sender (MAIL FROM) is the From's address: the
	// identity's, an alias's too, as Thunderbird sends. Its domain is the
	// one checks of the sender's domain (SPF, DMARC) look at against the
	// From, a bounce goes to the address the recipient saw, and a server
	// that does not let the account use the alias refuses it here, before
	// any of the message leaves, rather than pass it on under an address
	// the recipient never saw. The recipients are To, Cc and Bcc alike:
	// Bcc is in the envelope only, never in the message.
	from, err := msg.GetSender(false)
	if err != nil {
		return false, fmt.Errorf("smtp: %w", err)
	}
	rcpts, err := msg.GetRecipients()
	if err != nil {
		return false, fmt.Errorf("smtp: %w", err)
	}
	conn, err := s.dialSMTP(ctx, a, password)
	if err != nil {
		return false, stopped(ctx, err)
	}
	defer conn.Close()
	// go-mail takes the context for connecting alone. Hanging up is what
	// stops a send under way: a write waiting on a slow line fails at
	// once.
	stop := context.AfterFunc(ctx, func() { conn.Text.Close() })
	defer stop()

	total := int64(len(raw))
	if ok, param := conn.Extension("SIZE"); ok {
		limit, _ := strconv.ParseInt(strings.TrimSpace(param), 10, 64)
		if limit > 0 && total > limit {
			_ = conn.Quit()
			return false, &SizeLimitError{Size: total, Limit: limit}
		}
	}
	if err := conn.Mail(from); err != nil {
		return false, refused(ctx, err)
	}
	for _, rcpt := range rcpts {
		if err := conn.Rcpt(rcpt); err != nil {
			return false, refused(ctx, err)
		}
	}
	w, err := conn.Data()
	if err != nil {
		return false, refused(ctx, err)
	}
	// The progress is told as it changes by a two-hundredth of the whole,
	// and says the whole only once the server took the message.
	report := SendProgress(ctx)
	step, told := max(total/200, 1), int64(-1)
	tell := func(sent int64) {
		if report != nil && (told < 0 || sent-told >= step || sent == total) && sent > told {
			told = sent
			report(sent, total)
		}
	}
	tell(0)
	for sent := 0; sent < len(raw); {
		if err := ctx.Err(); err != nil {
			return left, stopped(ctx, err)
		}
		if err := conn.UpdateDeadline(smtpBlockTimeout); err != nil {
			return left, stopped(ctx, err)
		}
		n := min(smtpBlock, len(raw)-sent)
		left = true
		if _, err := w.Write(raw[sent : sent+n]); err != nil {
			return true, stopped(ctx, err)
		}
		sent += n
		tell(min(int64(sent), total-1))
	}
	if err := conn.UpdateDeadline(smtpAnswerTimeout); err != nil {
		return left, stopped(ctx, err)
	}
	// Closing sends the end of the message and reads the server's answer.
	if err := w.Close(); err != nil {
		var reply *textproto.Error
		if errors.As(err, &reply) {
			// It answered, and the answer is no.
			return false, &RejectedError{Code: reply.Code, Reply: reply.Msg}
		}
		return true, stopped(ctx, err)
	}
	tell(total)
	_ = conn.Quit()
	return false, nil
}

// stopped names what ended a send: the context, where that was it, not
// the closed connection it left behind.
func stopped(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return fmt.Errorf("smtp: %w", cause)
	}
	return fmt.Errorf("smtp: %w", err)
}

// refused turns the server's answer to a command into a RejectedError,
// and leaves any other failure as it is.
func refused(ctx context.Context, err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) && ctx.Err() == nil {
		return &RejectedError{Code: reply.Code, Reply: reply.Msg}
	}
	return stopped(ctx, err)
}

// afterDelivery saves the sent copy in the server's Sent folder and, where
// the message carried files or was a draft, removes the files nothing
// names any more.
func (s *Service) afterDelivery(ctx context.Context, a Account, password string, raw []byte, bcc []Address, sweep bool, result *SendResult) {
	if err := s.appendSent(ctx, a, password, raw, bcc); err != nil {
		result.Warning = err
	} else {
		result.Copied = true
	}
	if sweep {
		_ = s.SweepDraftFiles(context.WithoutCancel(ctx))
	}
}

// appendSent saves the sent copy in the server's Sent folder.
func (s *Service) appendSent(ctx context.Context, a Account, password string, raw []byte, bcc []Address) error {
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return err
	}
	defer c.close()
	folders, err := s.listFolders(c)
	if err != nil {
		return err
	}
	sent := ""
	for _, f := range folders {
		if f.SpecialUse == SpecialSent {
			sent = f.Name
			break
		}
	}
	if sent == "" {
		return errors.New("the server has no Sent folder")
	}
	if err := appendSentCopy(c, sent, sentCopy(raw, bcc), s.now()); err != nil {
		return fmt.Errorf("append to %s: %w", sent, err)
	}
	return nil
}

// sentCopy is a message as the Sent folder keeps it: the bytes sent and,
// where it had blind copies, a Bcc header naming them at the end of its
// header. The recipients never saw it; this copy, the user's own, is the
// one place that says whom the message went to. It comes in parts, so
// that a large message is not copied whole to make room for one line.
func sentCopy(raw []byte, bcc []Address) [][]byte {
	value := headerAddresses(bcc)
	if value == "" {
		return [][]byte{raw}
	}
	header := []byte("Bcc: " + value + "\r\n")
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		return [][]byte{header, raw}
	}
	return [][]byte{raw[:end+2], header, raw[end+2:]}
}

// appendSentCopy saves a message, which comes in parts, in a folder,
// read.
func appendSentCopy(c *imapSession, folder string, parts [][]byte, now time.Time) error {
	var size int64
	for _, part := range parts {
		size += int64(len(part))
	}
	cmd := c.client.Append(folder, size, &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: now})
	for _, part := range parts {
		if _, err := cmd.Write(part); err != nil {
			cmd.Close()
			return err
		}
	}
	if err := cmd.Close(); err != nil {
		return err
	}
	_, err := cmd.Wait()
	return err
}

// headerAddresses writes addresses for a header go-mail does not write,
// each on a line of its own: folding a long list is done here. An address
// that does not read back as itself is left out, as it could carry a line
// break into the header.
func headerAddresses(list []Address) string {
	parts := make([]string, 0, len(list))
	for _, addr := range toStdAddresses(list) {
		written := addr.String()
		if parsed, err := mail.ParseAddress(written); err != nil || parsed.Address != addr.Address {
			continue
		}
		parts = append(parts, written)
	}
	return strings.Join(parts, ",\r\n ")
}

// buildMessage makes the message and its raw form, which is what is sent
// and what APPEND saves in Sent. The outbox keeps it too, for a message
// without attachments.
func (s *Service) buildMessage(a Account, out Outgoing, now time.Time) (*gomail.Msg, []byte, error) {
	msg, err := s.newMessage(a, out, now)
	if err != nil {
		return nil, nil, err
	}
	var files int64
	for _, att := range out.Attachments {
		files += att.Size
	}
	raw, err := rawMessage(msg, files)
	if err != nil {
		return nil, nil, err
	}
	return msg, raw, nil
}

// newMessage fills a message from what was written. A draft has headers
// of its own to add before the raw form is made, and may have nobody to
// go to yet. It goes from out.From, or from the account's own address
// where that is not set, as in an outbox entry from before identities.
func (s *Service) newMessage(a Account, out Outgoing, now time.Time) (*gomail.Msg, error) {
	msg := gomail.NewMsg()
	from := out.From
	if from.Email == "" {
		from = Address{Name: a.Name, Email: a.Email}
	}
	if from.Name != "" {
		if err := msg.FromFormat(from.Name, from.Email); err != nil {
			return nil, err
		}
	} else if err := msg.From(from.Email); err != nil {
		return nil, err
	}
	if len(out.To) > 0 {
		msg.ToMailAddress(toStdAddresses(out.To)...)
	}
	if len(out.Cc) > 0 {
		msg.CcMailAddress(toStdAddresses(out.Cc)...)
	}
	if len(out.Bcc) > 0 {
		msg.BccMailAddress(toStdAddresses(out.Bcc)...)
	}
	msg.Subject(out.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, out.Text)
	msg.SetDateWithValue(now)
	msg.SetMessageIDWithValue(out.MessageID)
	if out.InReplyTo != "" {
		msg.SetGenHeader(gomail.HeaderInReplyTo, "<"+strings.Trim(out.InReplyTo, "<>")+">")
	}
	if len(out.References) > 0 {
		var refs []string
		for _, r := range out.References {
			refs = append(refs, "<"+strings.Trim(r, "<>")+">")
		}
		msg.SetGenHeader(gomail.HeaderReferences, strings.Join(refs, " "))
	}
	if out.Calendar != "" {
		msg.AddAlternativeString(gomail.ContentType("text/calendar; method=REPLY"), out.Calendar)
	}
	if len(out.Attachments) > 0 {
		files := make([]*gomail.File, 0, len(out.Attachments))
		for _, att := range out.Attachments {
			file, err := s.attachmentFile(att)
			if err != nil {
				return nil, err
			}
			files = append(files, file)
		}
		msg.SetAttachments(files)
	}
	return msg, nil
}

// rawMessage writes a message as RFC 822 bytes. attachments is the size
// of the files it carries: the buffer starts with room for them, so that
// a large message is not copied again and again as it grows.
func rawMessage(msg *gomail.Msg, attachments int64) ([]byte, error) {
	var buf bytes.Buffer
	if attachments > 0 {
		buf.Grow(int(EncodedSize(attachments)) + 16<<10)
	}
	if _, err := msg.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func toStdAddresses(list []Address) []*mail.Address {
	out := make([]*mail.Address, 0, len(list))
	for _, a := range list {
		out = append(out, &mail.Address{Name: a.Name, Address: a.Email})
	}
	return out
}

// dialSMTP connects to the account's SMTP server, secures the connection
// and signs in. An account that signs in with OAuth takes no password: it
// signs in with its access token, and when the server refuses one that
// had not expired, with a renewed one once more.
func (s *Service) dialSMTP(ctx context.Context, a Account, password string) (*smtp.Client, error) {
	if a.OAuth == "" {
		return s.dialSMTPAs(ctx, a, password)
	}
	token, err := s.accessToken(ctx, a, "")
	if err != nil {
		return nil, err
	}
	conn, err := s.dialSMTPAs(ctx, a, token)
	if !smtpRefused(err) {
		return conn, err
	}
	if token, err = s.accessToken(ctx, a, token); err != nil {
		return nil, err
	}
	return s.dialSMTPAs(ctx, a, token)
}

// dialSMTPAs connects and signs in with secret: the account's password,
// or its access token.
func (s *Service) dialSMTPAs(ctx context.Context, a Account, secret string) (*smtp.Client, error) {
	client, err := s.smtpClient(ctx, a, secret)
	if err != nil {
		return nil, err
	}
	return client.DialToSMTPClientWithContext(ctx)
}

// smtpRefused reports whether a server refused the credentials it was
// given (535, RFC 4954, section 6).
func smtpRefused(err error) bool {
	var reply *textproto.Error
	return errors.As(err, &reply) && reply.Code == 535
}

// smtpClient configures go-mail for implicit TLS or STARTTLS. An account
// that signs in with OAuth gives its access token for the password, by
// XOAUTH2.
func (s *Service) smtpClient(ctx context.Context, a Account, password string) (*gomail.Client, error) {
	auth := gomail.SMTPAuthAutoDiscover
	if a.OAuth != "" {
		auth = gomail.SMTPAuthXOAUTH2
	}
	tlsConfig := s.tlsConfigFor(ctx, a.SMTP.Host)
	opts := []gomail.Option{
		gomail.WithPort(a.SMTP.Port),
		gomail.WithUsername(a.Username),
		gomail.WithPassword(password),
		gomail.WithSMTPAuth(auth),
		gomail.WithTLSPolicy(gomail.TLSMandatory),
		gomail.WithTLSConfig(tlsConfig),
		gomail.WithTimeout(30 * time.Second),
		// The connection goes through the network settings' proxy, and
		// an implicit TLS connection is wrapped here, since go-mail uses
		// this dialer as it is.
		gomail.WithDialContextFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := s.dialNetwork(ctx, a.SMTP.Host, a.SMTP.Port)
			if err != nil {
				return nil, err
			}
			if a.SMTP.TLS != TLSImplicit {
				return conn, nil
			}
			tlsConn := tls.Client(conn, tlsConfig.Clone())
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tlsConn, nil
		}),
	}
	if a.SMTP.TLS == TLSImplicit {
		opts = append(opts, gomail.WithSSL())
	}
	return gomail.NewClient(a.SMTP.Host, opts...)
}

// newMessageID makes a unique Message-ID for a new message.
func newMessageID(email string) string {
	domain := "shu.local"
	if at := strings.LastIndexByte(email, '@'); at >= 0 && at+1 < len(email) {
		domain = email[at+1:]
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("shu.%d@%s", time.Now().UnixNano(), domain)
	}
	return hex.EncodeToString(b[:]) + "@" + domain
}

func newOutboxID() string {
	return randomID(func() string { return fmt.Sprintf("outbox-%d", time.Now().UnixNano()) })
}

// RetryPending resends an outbox entry the user asked to retry. It is
// only for the manual retry surface: nothing calls it on its own. An
// entry with attachments is built again from the files it names.
func (s *Service) RetryPending(ctx context.Context, id string) (SendResult, error) {
	pending, err := s.store.Outbox(ctx, id)
	if err != nil {
		return SendResult{}, err
	}
	switch pending.State {
	case StateSent:
		return SendResult{Outbox: pending, Copied: true}, nil
	case StateScheduled:
		// It leaves when it is due, and until then can be taken back.
		return SendResult{}, errors.New("mail: the message is waiting out its send delay")
	}
	a, password, err := s.account(ctx, pending.AccountID)
	if err != nil {
		return SendResult{}, err
	}
	now := s.now()
	if pending.Envelope.MessageID == "" {
		pending.Envelope.MessageID = newMessageID(a.Email)
	}
	msg, raw, err := s.buildMessage(a, pending.Envelope, now)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.deliver(ctx, a, password, id, msg, raw); err != nil {
		return SendResult{}, err
	}
	result := SendResult{Outbox: pending}
	// The outbox does not know which draft a message was. The draft is
	// found by its Message-ID, and goes only if it still says what was
	// sent: one edited since the failed send keeps the newer text.
	sentDraft := s.retireSentDraft(context.WithoutCancel(ctx), pending.AccountID, pending.Envelope)
	// The copy for Sent is the message as the outbox kept it. One with
	// attachments was not kept: it is the one just built.
	sent := pending.Raw
	if len(sent) == 0 {
		sent = raw
	}
	s.afterDelivery(ctx, a, password, sent, pending.Envelope.Bcc, sentDraft || len(pending.Envelope.Attachments) > 0, &result)
	if sentDraft {
		s.flushDraftDeletions(ctx, pending.AccountID)
	}
	return result, nil
}

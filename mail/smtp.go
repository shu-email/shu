package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// Send builds the raw message, writes the outbox row, sends over SMTP and
// appends a copy to Sent. The outbox makes a crash recoverable and keeps
// the client from sending the same message twice on its own.
func (s *Service) Send(ctx context.Context, accountID string, out Outgoing) (SendResult, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return SendResult{}, err
	}
	if len(out.To) == 0 {
		return SendResult{}, errors.New("mail: the message has no recipients")
	}
	now := s.now()
	if out.MessageID == "" {
		// A draft goes out under the Message-ID its server copies carry.
		out.MessageID = s.draftMessageID(ctx, accountID, out.DraftID)
	}
	if out.MessageID == "" {
		out.MessageID = newMessageID(a.Email)
	}
	msg, raw, err := buildMessage(a, out, now)
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
	if err := s.store.EnqueueOutbox(ctx, pending); err != nil {
		return SendResult{}, err
	}
	if err := s.store.SetOutboxState(ctx, pending.ID, StateSending, ""); err != nil {
		return SendResult{}, err
	}
	client, err := s.smtpClient(a, password)
	if err != nil {
		_ = s.store.SetOutboxState(ctx, pending.ID, StateFailed, err.Error())
		return SendResult{}, err
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		_ = s.store.SetOutboxState(ctx, pending.ID, StateFailed, err.Error())
		return SendResult{}, fmt.Errorf("smtp: %w", err)
	}
	if err := s.store.SetOutboxState(ctx, pending.ID, StateSent, ""); err != nil {
		return SendResult{}, err
	}
	pending.State = StateSent
	pending.SentAt = now
	result := SendResult{Outbox: pending}
	// The message is out, so its draft is done: it leaves this device
	// now, and the server when the server can be reached.
	sentDraft := s.retireDraft(ctx, accountID, out.DraftID)
	if err := s.appendSent(ctx, a, password, raw); err != nil {
		result.Warning = err
	} else {
		result.Copied = true
	}
	if sentDraft {
		s.flushDraftDeletions(ctx, accountID)
	}
	return result, nil
}

// appendSent saves the sent copy in the server's Sent folder.
func (s *Service) appendSent(ctx context.Context, a Account, password string, raw []byte) error {
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
	if err := appendTo(c, sent, raw, s.now()); err != nil {
		return fmt.Errorf("append to %s: %w", sent, err)
	}
	return nil
}

// buildMessage makes the message and the raw form the outbox keeps and
// APPEND reuses.
func buildMessage(a Account, out Outgoing, now time.Time) (*gomail.Msg, []byte, error) {
	msg, err := newMessage(a, out, now)
	if err != nil {
		return nil, nil, err
	}
	raw, err := rawMessage(msg)
	if err != nil {
		return nil, nil, err
	}
	return msg, raw, nil
}

// newMessage fills a message from what was written. A draft has headers
// of its own to add before the raw form is made, and may have nobody to
// go to yet.
func newMessage(a Account, out Outgoing, now time.Time) (*gomail.Msg, error) {
	msg := gomail.NewMsg()
	if a.Name != "" {
		if err := msg.FromFormat(a.Name, a.Email); err != nil {
			return nil, err
		}
	} else if err := msg.From(a.Email); err != nil {
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
	return msg, nil
}

// rawMessage writes a message as RFC 822 bytes.
func rawMessage(msg *gomail.Msg) ([]byte, error) {
	var buf bytes.Buffer
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

// smtpClient configures go-mail for implicit TLS or STARTTLS.
func (s *Service) smtpClient(a Account, password string) (*gomail.Client, error) {
	opts := []gomail.Option{
		gomail.WithPort(a.SMTP.Port),
		gomail.WithUsername(a.Username),
		gomail.WithPassword(password),
		gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
		gomail.WithTLSPolicy(gomail.TLSMandatory),
		gomail.WithTLSConfig(s.tlsConfigFor(a.SMTP.Host)),
		gomail.WithTimeout(30 * time.Second),
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
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("outbox-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// RetryPending resends an outbox entry the user asked to retry. It is
// only for the manual retry surface: nothing calls it on its own.
func (s *Service) RetryPending(ctx context.Context, id string) (SendResult, error) {
	pending, err := s.store.Outbox(ctx, id)
	if err != nil {
		return SendResult{}, err
	}
	if pending.State == StateSent {
		return SendResult{Outbox: pending, Copied: true}, nil
	}
	a, password, err := s.account(ctx, pending.AccountID)
	if err != nil {
		return SendResult{}, err
	}
	now := s.now()
	if pending.Envelope.MessageID == "" {
		pending.Envelope.MessageID = newMessageID(a.Email)
	}
	msg, _, err := buildMessage(a, pending.Envelope, now)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.store.SetOutboxState(ctx, id, StateSending, ""); err != nil {
		return SendResult{}, err
	}
	client, err := s.smtpClient(a, password)
	if err == nil {
		err = client.DialAndSendWithContext(ctx, msg)
	}
	if err != nil {
		_ = s.store.SetOutboxState(ctx, id, StateFailed, err.Error())
		return SendResult{}, fmt.Errorf("smtp: %w", err)
	}
	if err := s.store.SetOutboxState(ctx, id, StateSent, ""); err != nil {
		return SendResult{}, err
	}
	result := SendResult{Outbox: pending}
	// The outbox does not know which draft a message was. The draft is
	// found by its Message-ID, and goes only if it still says what was
	// sent: one edited since the failed send keeps the newer text.
	sentDraft := s.retireSentDraft(ctx, pending.AccountID, pending.Envelope)
	if err := s.appendSent(ctx, a, password, pending.Raw); err != nil {
		result.Warning = err
	} else {
		result.Copied = true
	}
	if sentDraft {
		s.flushDraftDeletions(ctx, pending.AccountID)
	}
	return result, nil
}

// SentFolder is the name of the account's Sent folder, or "".
func (s *Service) SentFolder(ctx context.Context, accountID string) string {
	return s.findSent(ctx, accountID)
}

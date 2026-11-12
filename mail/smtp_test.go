package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

// fakeSMTP is a local server that records what it receives.
type fakeSMTP struct {
	addr     string
	server   *smtp.Server
	listener net.Listener
	mu       sync.Mutex
	messages []string
	rcpts    []string
	user     string
	pass     string
	// refuse is the server's answer to a message it has read whole, as a
	// provider refusing one too large gives it. onData takes the place of
	// reading the message: a server that stalls, or does anything else.
	refuse error
	onData func(r io.Reader) error
}

type smtpBackend struct{ f *fakeSMTP }

func (b smtpBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &smtpSession{f: b.f}, nil
}

type smtpSession struct {
	f    *fakeSMTP
	auth bool
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, fmt.Errorf("unsupported mechanism %q", mech)
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != s.f.user || password != s.f.pass {
			return errors.New("invalid credentials")
		}
		s.auth = true
		return nil
	}), nil
}

func (s *smtpSession) Mail(from string, opts *smtp.MailOptions) error {
	if !s.auth {
		return smtp.ErrAuthRequired
	}
	return nil
}

func (s *smtpSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	if !s.auth {
		return smtp.ErrAuthRequired
	}
	s.f.mu.Lock()
	s.f.rcpts = append(s.f.rcpts, to)
	s.f.mu.Unlock()
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	if !s.auth {
		return smtp.ErrAuthRequired
	}
	s.f.mu.Lock()
	onData, refuse := s.f.onData, s.f.refuse
	s.f.mu.Unlock()
	if onData != nil {
		return onData(r)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if refuse != nil {
		return refuse
	}
	s.f.mu.Lock()
	s.f.messages = append(s.f.messages, string(b))
	s.f.mu.Unlock()
	return nil
}

func (s *smtpSession) Reset()        {}
func (s *smtpSession) Logout() error { return nil }

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	return startFakeSMTPWith(t, nil)
}

// startFakeSMTPWith starts the server after configure has set it up, as
// with the size limit a provider announces.
func startFakeSMTPWith(t *testing.T, configure func(*smtp.Server)) *fakeSMTP {
	t.Helper()
	cert, err := selfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{user: "ann", pass: "secret"}
	server := smtp.NewServer(smtpBackend{f: f})
	server.Domain = "localhost"
	server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.ErrorLog = testSMTPLogger{t}
	if configure != nil {
		configure(server)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.server, f.listener, f.addr = server, ln, ln.Addr().String()
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		server.Close()
		ln.Close()
	})
	return f
}

type testSMTPLogger struct{ t *testing.T }

func (l testSMTPLogger) Printf(format string, args ...any) {
	l.t.Logf("smtpserver: "+format, args...)
}
func (l testSMTPLogger) Println(args ...any) { l.t.Log(args...) }

func (f *fakeSMTP) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...)
}

// combinedAccount returns an account whose IMAP points at the fake IMAP
// server and whose SMTP points at the fake SMTP server.
func combinedAccount(t *testing.T, imap *fakeIMAP, smtp *fakeSMTP) Account {
	a := fakeAccount(t, imap.addr)
	host, port, err := net.SplitHostPort(smtp.addr)
	if err != nil {
		t.Fatal(err)
	}
	a.SMTP = Server{Host: host, Port: mustPort(t, port), TLS: TLSStartTLS}
	return a
}

func TestSendRecordsOutboxAndSentCopy(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent")
	smtpServer := startFakeSMTP(t)
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	out := Outgoing{
		To:         []Address{{Name: "Bob", Email: "bob@example.com"}},
		Cc:         []Address{{Email: "carol@example.com"}},
		Subject:    "Hello",
		Text:       "Body text",
		InReplyTo:  "previous@example.com",
		References: []string{"root@example.com", "previous@example.com"},
	}
	result, err := svc.Send(ctx, a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Copied || result.Warning != nil {
		t.Errorf("copy = %v, warning %v", result.Copied, result.Warning)
	}
	msgs := smtpServer.received()
	if len(msgs) != 1 {
		t.Fatalf("smtp messages = %d", len(msgs))
	}
	raw := msgs[0]
	for _, want := range []string{"bob@example.com", "carol@example.com", "Subject: Hello", "In-Reply-To: <previous@example.com>", "References: <root@example.com> <previous@example.com>", "Body text"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw message lacks %q:\n%s", want, raw)
		}
	}
	pending, err := store.Outbox(ctx, result.Outbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StateSent {
		t.Errorf("outbox state = %q", pending.State)
	}
	if string(pending.Raw) == "" {
		t.Error("outbox kept no raw message")
	}
	if got, _ := secrets.Get(PasswordKey(a.ID)); string(got) != "secret" {
		t.Errorf("password = %q", got)
	}
	// The copy is on the IMAP server's Sent folder.
	if err := svc.SyncFolder(ctx, a.ID, "Sent", 50); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := svc.CachedMessages(ctx, a.ID, "Sent", "", 50); len(msgs) != 1 {
		t.Errorf("sent copy = %+v", msgs)
	}
}

func TestSendAuthFailureLeavesFailedOutbox(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent")
	smtpServer := startFakeSMTP(t)
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(PasswordKey(a.ID), []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Send(ctx, a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "x", Text: "y"})
	if err == nil {
		t.Fatal("send with a wrong password succeeded")
	}
	if len(smtpServer.received()) != 0 {
		t.Error("a message was delivered despite auth failure")
	}
	pending, err := store.PendingSends(ctx, a.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if pending[0].State != StateFailed || pending[0].LastError == "" {
		t.Errorf("failed entry = %+v", pending[0])
	}
}

func TestSendWithoutSentFolderReportsWarning(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX")
	smtpServer := startFakeSMTP(t)
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Send(ctx, a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "x", Text: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Copied || result.Warning == nil {
		t.Errorf("copy/warning = %v/%v", result.Copied, result.Warning)
	}
	if len(smtpServer.received()) != 1 {
		t.Error("message was not sent")
	}
}

func TestSendRequiresRecipients(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX")
	smtpServer := startFakeSMTP(t)
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, a.ID, Outgoing{Subject: "x", Text: "y"}); err == nil {
		t.Error("send without recipients succeeded")
	}
}

func TestTestConnectionSucceedsWithBothServers(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX")
	smtpServer := startFakeSMTP(t)
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.TestConnection(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := svc.TestConnection(ctx, a, "wrong"); err == nil {
		t.Error("wrong password accepted")
	}
}

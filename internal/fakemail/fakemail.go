// Package fakemail runs IMAP and SMTP servers on 127.0.0.1 that keep
// their mail in memory. The end-to-end tests run the engine against them,
// and the dev build's seeded account (seed_e2e.go) connects to them, so
// that no check of Shu ever needs a real mailbox.
//
// Only test files and files built with the e2e tag import this package:
// release builds do not contain it.
package fakemail

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

// Logger receives the servers' logs.
type Logger interface {
	Printf(format string, args ...any)
	Println(args ...any)
}

// Cert returns a new self-signed certificate for 127.0.0.1, valid for a
// month (as long as a dev build may stay open), and a pool that trusts it alone. Shu speaks only TLS to mail
// servers, so the local ones need a certificate; the pool lets a client
// verify it rather than skip verification.
func Cert() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool, nil
}

// PromptSession is the in-memory server's session with an Idle that holds
// nothing back. The server's own reports only what happens once an IDLE
// has begun: a change made between the client's last command and its IDLE
// waits for the client's next command. Servers in use do not do that
// (they sync as an IDLE begins), and a test of pushed mail would pass or
// fail by which side won that race. This one looks at short intervals.
type PromptSession struct {
	imapserver.SessionIMAP4rev2
	// noKeywords is IMAPOptions.KeepsNoKeywords.
	noKeywords bool
}

// Select selects a mailbox as the in-memory server does, which keeps any
// keyword, but for a server that keeps none: its PERMANENTFLAGS name the
// system's flags alone.
func (s PromptSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	data, err := s.SessionIMAP4rev2.Select(mailbox, options)
	if err == nil && s.noKeywords {
		data.PermanentFlags = slices.DeleteFunc(data.PermanentFlags, func(f imap.Flag) bool { return !systemFlag(f) })
	}
	return data, err
}

// Store changes flags as the in-memory server does, but a server that
// keeps no keywords answers NO to a STORE that would add one.
func (s PromptSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	if s.noKeywords && flags.Op != imap.StoreFlagsDel && slices.ContainsFunc(flags.Flags, func(f imap.Flag) bool { return !systemFlag(f) }) {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "This mailbox keeps no keywords"}
	}
	return s.SessionIMAP4rev2.Store(w, numSet, flags, options)
}

// systemFlag reports whether a flag is one of the system's, as \Seen, and
// not \*, which stands for any keyword.
func systemFlag(f imap.Flag) bool { return strings.HasPrefix(string(f), `\`) && f != imap.FlagWildcard }

func (s PromptSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.SessionIMAP4rev2.Poll(w, true); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
	}
}

// IMAPOptions configures StartIMAP.
type IMAPOptions struct {
	Cert               tls.Certificate
	Username, Password string
	// Mailboxes are created empty, in this order.
	Mailboxes []string
	// Session, if set, wraps each session, as to sign in another way.
	Session func(PromptSession) imapserver.Session
	// KeepsNoKeywords has every mailbox keep no keyword, as Outlook's: a
	// SELECT's PERMANENTFLAGS name the system's flags alone, and a STORE
	// that would add a keyword is answered NO.
	KeepsNoKeywords bool
	Logger          Logger
}

// IMAP is a running IMAP server.
type IMAP struct {
	Addr string
	// User is the one user, whose mailboxes a caller fills with Append.
	User   *imapmemserver.User
	server *imapserver.Server
	ln     net.Listener
}

// StartIMAP starts an IMAP server on a free port of 127.0.0.1.
func StartIMAP(o IMAPOptions) (*IMAP, error) {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(o.Username, o.Password)
	mem.AddUser(user)
	for _, name := range o.Mailboxes {
		if err := user.Create(name, nil); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			session := PromptSession{SessionIMAP4rev2: mem.NewSession().(imapserver.SessionIMAP4rev2), noKeywords: o.KeepsNoKeywords}
			if o.Session != nil {
				return o.Session(session), &imapserver.GreetingData{}, nil
			}
			return session, &imapserver.GreetingData{}, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {},
			imap.CapListStatus: {}, imap.CapSpecialUse: {},
		},
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{o.Cert}, MinVersion: tls.VersionTLS12},
		Logger:    o.Logger,
	})
	go func() { _ = server.Serve(ln) }()
	return &IMAP{Addr: ln.Addr().String(), User: user, server: server, ln: ln}, nil
}

// Append adds a message to a mailbox.
func (s *IMAP) Append(mailbox string, raw []byte, o *imap.AppendOptions) error {
	_, err := s.User.Append(mailbox, Literal(raw), o)
	return err
}

// Close stops the server.
func (s *IMAP) Close() error {
	err := s.server.Close()
	_ = s.ln.Close()
	return err
}

// Literal is raw as the literal of an APPEND.
func Literal(raw []byte) imap.LiteralReader { return literal{bytes.NewReader(raw)} }

type literal struct{ *bytes.Reader }

func (b literal) Size() int64 { return int64(b.Len()) }

// SMTPOptions configures StartSMTP.
type SMTPOptions struct {
	Cert               tls.Certificate
	Username, Password string
	// OnMessage, if set, is given each message the server takes, as it
	// takes it.
	OnMessage func(raw []byte)
	Logger    Logger
}

// SMTP is a running SMTP server that keeps the messages it takes and
// delivers none of them.
type SMTP struct {
	Addr   string
	opts   SMTPOptions
	server *smtp.Server
	ln     net.Listener

	mu        sync.Mutex
	got       []string
	envelopes []Envelope
	sessions  int
	refuse    error
}

// Envelope is the sender and the recipients of a message the server was
// given, as its MAIL FROM and RCPT TO commands named them.
type Envelope struct {
	From string
	To   []string
}

// StartSMTP starts an SMTP server on a free port of 127.0.0.1. It speaks
// STARTTLS and takes mail only after PLAIN authentication.
func StartSMTP(o SMTPOptions) (*SMTP, error) {
	s := &SMTP{opts: o}
	server := smtp.NewServer(s)
	server.Domain = "localhost"
	server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{o.Cert}, MinVersion: tls.VersionTLS12}
	server.ErrorLog = o.Logger
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.Addr, s.server, s.ln = ln.Addr().String(), server, ln
	go func() { _ = server.Serve(ln) }()
	return s, nil
}

// SetRefuse has the server answer each message with err until it is set
// to nil.
func (s *SMTP) SetRefuse(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = err
}

// Envelopes returns the envelope of each message taken, in order.
func (s *SMTP) Envelopes() []Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Envelope(nil), s.envelopes...)
}

// Connections counts the sessions the server opened.
func (s *SMTP) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// Received returns the messages taken so far.
func (s *SMTP) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// Close stops the server.
func (s *SMTP) Close() error {
	err := s.server.Close()
	_ = s.ln.Close()
	return err
}

func (s *SMTP) NewSession(*smtp.Conn) (smtp.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions++
	return &smtpSession{server: s}, nil
}

type smtpSession struct {
	server *SMTP
	authed bool
	// from and to are the envelope of the message under way.
	from string
	to   []string
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != s.server.opts.Username || password != s.server.opts.Password {
			return errors.New("invalid credentials")
		}
		s.authed = true
		return nil
	}), nil
}

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return smtp.ErrAuthRequired
	}
	s.from, s.to = from, nil
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.server.mu.Lock()
	if s.server.refuse != nil {
		err := s.server.refuse
		s.server.mu.Unlock()
		return err
	}
	s.server.got = append(s.server.got, string(b))
	s.server.envelopes = append(s.server.envelopes, Envelope{From: s.from, To: s.to})
	s.server.mu.Unlock()
	if s.server.opts.OnMessage != nil {
		s.server.opts.OnMessage(b)
	}
	return nil
}

func (s *smtpSession) Reset()        { s.from, s.to = "", nil }
func (s *smtpSession) Logout() error { return nil }

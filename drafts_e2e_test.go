package main

import (
	"bytes"
	"context"
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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// The tests in this file run the window on the real engine and the real
// database against mail servers on 127.0.0.1, so that the layers are
// checked together and not only against each other's stand-ins.

// localCert is a certificate for the local servers.
func localCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// localIMAP is an in-memory IMAP server the test can also reach as a
// second mail client.
type localIMAP struct {
	addr string
	user *imapmemserver.User
}

type serverLog struct{ t *testing.T }

func (l serverLog) Printf(format string, args ...any) { l.t.Logf("server: "+format, args...) }
func (l serverLog) Println(args ...any)               { l.t.Log(args...) }

// promptSession is the in-memory server's session with an Idle that holds
// nothing back. The server's own reports only what happens once an IDLE
// has begun: a change made between the client's last command and its IDLE
// waits for the client's next command. Servers in use do not do that
// (they sync as an IDLE begins), and a test of pushed mail would pass or
// fail by which side won that race. This one looks at short intervals.
type promptSession struct{ imapserver.SessionIMAP4rev2 }

func (s promptSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
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

func startLocalIMAP(t *testing.T, mailboxes ...string) *localIMAP {
	t.Helper()
	return startLocalIMAPWith(t, nil, mailboxes...)
}

// startLocalIMAPWith is startLocalIMAP for a server that, with xoauth
// set, signs in by XOAUTH2 with the access tokens xoauth holds valid,
// and takes no password (see oauth_e2e_test.go).
func startLocalIMAPWith(t *testing.T, xoauth func(user, token string) bool, mailboxes ...string) *localIMAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("ann", "secret")
	mem.AddUser(user)
	for _, name := range mailboxes {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			session := promptSession{mem.NewSession().(imapserver.SessionIMAP4rev2)}
			if xoauth != nil {
				return xoauthSession{session, xoauth}, &imapserver.GreetingData{}, nil
			}
			return session, &imapserver.GreetingData{}, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {},
			imap.CapListStatus: {}, imap.CapSpecialUse: {},
		},
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{localCert(t)}, MinVersion: tls.VersionTLS12},
		Logger:    serverLog{t},
	})
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		server.Close()
		ln.Close()
	})
	return &localIMAP{addr: ln.Addr().String(), user: user}
}

// serverDraft is a message of the server's Drafts mailbox.
type serverDraft struct {
	UID   imap.UID
	Draft bool
	Raw   string
}

// connect logs in as another mail client would.
func (s *localIMAP) connect(t *testing.T, mailbox string) *imapclient.Client {
	t.Helper()
	c, _ := s.open(t, mailbox)
	return c
}

// open is connect, with what the SELECT said of the mailbox. The client's
// Mailbox() is no substitute: the library lets the SELECT's Wait return
// before it records the selected mailbox there.
func (s *localIMAP) open(t *testing.T, mailbox string) (*imapclient.Client, *imap.SelectData) {
	t.Helper()
	c, err := imapclient.DialStartTLS(s.addr, &imapclient.Options{
		TLSConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, // #nosec G402 -- a local test server
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login("ann", "secret").Wait(); err != nil {
		t.Fatal(err)
	}
	selected, err := c.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	return c, selected
}

// messages reads a mailbox as another mail client sees it.
func (s *localIMAP) messages(t *testing.T, mailbox string) []serverDraft {
	t.Helper()
	c, selected := s.open(t, mailbox)
	defer c.Close()
	if selected.NumMessages == 0 {
		return nil
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var out []serverDraft
	for _, m := range fetched {
		d := serverDraft{UID: m.UID, Raw: string(m.FindBodySection(section))}
		for _, flag := range m.Flags {
			if strings.EqualFold(string(flag), string(imap.FlagDraft)) {
				d.Draft = true
			}
		}
		out = append(out, d)
	}
	return out
}

// replace deletes a draft and appends another message in its place, which
// is how a mail client edits a draft over IMAP.
func (s *localIMAP) replace(t *testing.T, uid imap.UID, raw string) {
	t.Helper()
	c := s.connect(t, "Drafts")
	defer c.Close()
	var set imap.UIDSet
	set.AddNum(uid)
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Expunge().Close(); err != nil {
		t.Fatal(err)
	}
	literal := appendBody{bytes.NewReader([]byte(raw))}
	if _, err := s.user.Append("Drafts", literal, &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

type appendBody struct{ *bytes.Reader }

func (b appendBody) Size() int64 { return int64(b.Len()) }

// localSMTP is a server that records the messages it is given: their
// data, and the envelope each came in, and how many connections it took.
type localSMTP struct {
	addr      string
	mu        sync.Mutex
	got       []string
	envelopes []smtpEnvelope
	sessions  int
	// refuse is the server's answer to a message it does not take.
	refuse error
}

// smtpEnvelope is the sender and the recipients of a message the server
// was given, as its MAIL FROM and RCPT TO commands named them.
type smtpEnvelope struct {
	from string
	to   []string
}

func (s *localSMTP) setRefuse(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = err
}

func (s *localSMTP) NewSession(*smtp.Conn) (smtp.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions++
	return &smtpSession{server: s}, nil
}

func (s *localSMTP) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// envelopesGot is the envelope of each message received, in order.
func (s *localSMTP) envelopesGot() []smtpEnvelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]smtpEnvelope(nil), s.envelopes...)
}

// connections counts the sessions the server opened.
func (s *localSMTP) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

type smtpSession struct {
	server *localSMTP
	authed bool
	// from and to are the envelope of the message under way.
	from string
	to   []string
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != "ann" || password != "secret" {
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
	defer s.server.mu.Unlock()
	if s.server.refuse != nil {
		return s.server.refuse
	}
	s.server.got = append(s.server.got, string(b))
	s.server.envelopes = append(s.server.envelopes, smtpEnvelope{from: s.from, to: s.to})
	return nil
}

func (s *smtpSession) Reset()        { s.from, s.to = "", nil }
func (s *smtpSession) Logout() error { return nil }

func startLocalSMTP(t *testing.T) *localSMTP {
	t.Helper()
	s := &localSMTP{}
	server := smtp.NewServer(s)
	server.Domain = "localhost"
	server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{localCert(t)}, MinVersion: tls.VersionTLS12}
	server.ErrorLog = serverLog{t}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		server.Close()
		ln.Close()
	})
	return s
}

// sendAtOnce has the engine send without a delay, as it did before there
// was one: these tests are of what a send does once it begins, and the
// delay has a test of its own end to end.
func sendAtOnce(t *testing.T, svc *mail.Service) {
	t.Helper()
	set := mail.DefaultComposeSettings()
	set.SendDelay = 0
	if err := svc.SaveComposeSettings(context.Background(), set); err != nil {
		t.Fatal(err)
	}
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := net.LookupPort("tcp", port)
	if err != nil {
		t.Fatal(err)
	}
	return host, n
}

// The acceptance criteria end to end: the window, the real engine, a
// database on disk and local IMAP and SMTP servers. A draft is written,
// uploaded, edited, replaced by another mail client, edited offline,
// discarded and sent, and after each step the server's Drafts folder and
// the Drafts list are what they should be.
func TestDraftsEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Drafts", "Sent", "Trash")
	smtpServer := startLocalSMTP(t)
	imapHost, imapPort := hostPort(t, imapServer.addr)
	smtpHost, smtpPort := hostPort(t, smtpServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: imapHost, Port: imapPort, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: smtpHost, Port: smtpPort, TLS: mail.TLSStartTLS},
	}
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	sendAtOnce(t, svc)
	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" {
		t.Fatalf("start: %s", a.err)
	}
	main := ui.NewTester(a.view, 1220, 800)
	// rows reads the Drafts folder as the window shows it.
	rows := func() []mail.Summary {
		t.Helper()
		click(t, main, locales.Drafts())
		a.refresh()
		main.Frame()
		return a.all
	}
	only := func(what string) serverDraft {
		t.Helper()
		msgs := imapServer.messages(t, "Drafts")
		if len(msgs) != 1 {
			t.Fatalf("%s: the server's Drafts folder has %d messages, want one", what, len(msgs))
		}
		return msgs[0]
	}

	// Written and closed: the draft is on the server, flagged as one.
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	typeIn(t, tt, locales.Body(), "First thoughts")
	if msgs := imapServer.messages(t, "Drafts"); len(msgs) != 0 {
		t.Fatalf("typing alone uploaded %d messages", len(msgs))
	}
	cs.close()
	first := only("after closing the window")
	if !first.Draft || !strings.Contains(first.Raw, "Subject: Quarterly plan") || !strings.Contains(first.Raw, "First thoughts") {
		t.Fatalf("uploaded draft = %+v", first)
	}
	// The folder shows it once: the cached server copy is no second row.
	if list := rows(); len(list) != 1 || list[0].DraftID != cs.draftID {
		t.Fatalf("drafts = %+v", list)
	}

	// Edited: the server holds exactly the new copy.
	click(t, main, locales.EditDraft())
	edit := a.composes[len(a.composes)-1]
	et := ui.NewTester(edit.view, 720, 620)
	typeIn(t, et, locales.Body(), ", second thoughts")
	edit.close()
	second := only("after an edit")
	if second.UID == first.UID || !strings.Contains(second.Raw, "First thoughts, second thoughts") {
		t.Fatalf("edited draft on the server = %+v", second)
	}
	if list := rows(); len(list) != 1 || list[0].Preview != "First thoughts, second thoughts" {
		t.Fatalf("drafts after the edit = %+v", list)
	}

	// Another mail client replaces it while this device has no edits: the
	// next sync shows the other client's version, as a server draft.
	imapServer.replace(t, second.UID, "From: Ann <ann@example.com>\r\nTo: bob@example.com\r\n"+
		"Subject: Quarterly plan\r\nMessage-ID: <phone-1@example.com>\r\n"+
		"Date: Tue, 03 Jan 2023 10:20:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nEdited on the phone\r\n")
	list := rows()
	if len(list) != 1 || list[0].DraftID != "" || list[0].Preview != "Edited on the phone" {
		t.Fatalf("drafts after another client's edit = %+v", list)
	}

	// Opening that one adopts it, and an offline edit of it waits.
	click(t, main, locales.EditDraft())
	adopted := a.composes[len(a.composes)-1]
	if adopted.body != "Edited on the phone" || adopted.to != "bob@example.com" || adopted.closed {
		t.Fatalf("adopted window = to %q body %q", adopted.to, adopted.body)
	}
	at := ui.NewTester(adopted.view, 720, 620)
	offline := account
	offline.IMAP.Port = func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}()
	if err := svc.UpdateAccount(ctx, offline, ""); err != nil {
		t.Fatal(err)
	}
	typeIn(t, at, locales.Body(), ", then at the desk")
	a.syncDrafts("a1")
	at.Frame()
	if !at.HasText(locales.DraftOffline()) {
		t.Fatalf("status offline = %q", at.Texts())
	}
	if got := only("while offline"); !strings.Contains(got.Raw, "Edited on the phone") || strings.Contains(got.Raw, "at the desk") {
		t.Fatalf("server draft while offline = %+v", got)
	}

	// Connectivity returns: the edit goes up and replaces the phone's.
	if err := svc.UpdateAccount(ctx, account, ""); err != nil {
		t.Fatal(err)
	}
	a.networkBack()
	at.Frame()
	if !at.HasText(locales.DraftSynced()) {
		t.Fatalf("status back online = %q", at.Texts())
	}
	if got := only("back online"); !strings.Contains(got.Raw, "Edited on the phone, then at the desk") {
		t.Fatalf("server draft back online = %+v", got)
	}

	// A conflict: edited here, replaced there. Both versions stay, and
	// the window says so.
	typeIn(t, at, locales.Body(), ". Mine")
	imapServer.replace(t, only("before the conflict").UID, "From: Ann <ann@example.com>\r\nTo: bob@example.com\r\n"+
		"Subject: Quarterly plan\r\nMessage-ID: <phone-2@example.com>\r\n"+
		"Date: Tue, 03 Jan 2023 11:20:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nTheirs\r\n")
	a.syncDrafts("a1")
	at.Frame()
	if !at.HasText(locales.DraftConflict()) {
		t.Fatalf("no conflict notice: %q", at.Texts())
	}
	both := imapServer.messages(t, "Drafts")
	if len(both) != 2 {
		t.Fatalf("server drafts after the conflict = %d, want both versions", len(both))
	}
	var mine, theirs bool
	for _, m := range both {
		mine = mine || strings.Contains(m.Raw, "then at the desk. Mine")
		theirs = theirs || strings.Contains(m.Raw, "Theirs")
	}
	if !mine || !theirs {
		t.Fatalf("server drafts after the conflict = %+v", both)
	}
	if list := rows(); len(list) != 2 || list[0].DraftID != adopted.draftID || list[1].DraftID != "" {
		t.Fatalf("drafts after the conflict = %+v", list)
	}

	// Discarded, after the confirmation: gone here and on the server; the
	// other client's version is not this draft's to remove.
	asked := 0
	a.confirmDiscard = func(*mygo.Window, int) bool { asked++; return true }
	click(t, at, locales.DiscardDraft())
	if asked != 1 || !adopted.closed {
		t.Fatalf("discard: asked %d, closed %v", asked, adopted.closed)
	}
	if got := only("after the discard"); !strings.Contains(got.Raw, "Theirs") {
		t.Fatalf("server drafts after the discard = %+v", got)
	}
	if list := rows(); len(list) != 1 || list[0].DraftID != "" {
		t.Fatalf("drafts after the discard = %+v", list)
	}

	// Sent: a new draft goes out, and leaves no copy and no row behind.
	out := a.openCompose(nil)
	ot := ui.NewTester(out.view, 720, 620)
	typeIn(t, ot, locales.To(), "bob@example.com")
	typeIn(t, ot, locales.Subject(), "Ready to send")
	typeIn(t, ot, locales.Body(), "Here it is.")
	a.syncDrafts("a1")
	if msgs := imapServer.messages(t, "Drafts"); len(msgs) != 2 {
		t.Fatalf("server drafts before sending = %d", len(msgs))
	}
	click(t, ot, locales.Send())
	if out.err != "" || !out.closed {
		t.Fatalf("send: err %q, closed %v", out.err, out.closed)
	}
	sent := smtpServer.received()
	if len(sent) != 1 || !strings.Contains(sent[0], "Subject: Ready to send") || strings.Contains(sent[0], "X-Shu-Draft-ID") {
		t.Fatalf("delivered = %q", sent)
	}
	if got := only("after sending"); !strings.Contains(got.Raw, "Theirs") {
		t.Fatalf("server drafts after sending = %+v", got)
	}
	if msgs := imapServer.messages(t, "Sent"); len(msgs) != 1 {
		t.Errorf("sent copies = %d", len(msgs))
	}
	list = rows()
	if len(list) != 1 || list[0].DraftID != "" || main.HasText("Ready to send") {
		t.Fatalf("drafts after sending = %+v", list)
	}
	if drafts, err := db.Drafts(ctx, "a1"); err != nil || len(drafts) != 0 {
		t.Errorf("draft rows left in the database = %+v, %v", drafts, err)
	}
}

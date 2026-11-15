package fakemail_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	sasl "github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"shu/internal/fakemail"
)

const (
	testUser     = "ann"
	testPassword = "secret"
)

// testGmail is a running Gmail-shaped server.
type testGmail struct {
	*fakemail.IMAP
	pool *x509.CertPool
	cert tls.Certificate
}

// startGmail starts a Gmail-shaped IMAP server.
func startGmail(t *testing.T, profile fakemail.GmailProfile, mailboxes ...string) *testGmail {
	t.Helper()
	cert, pool, err := fakemail.Cert()
	if err != nil {
		t.Fatal(err)
	}
	server, err := fakemail.StartIMAP(fakemail.IMAPOptions{
		Cert:      cert,
		Username:  testUser,
		Password:  testPassword,
		Mailboxes: mailboxes,
		Gmail:     &profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return &testGmail{IMAP: server, pool: pool, cert: cert}
}

// tlsConfig trusts the server's certificate.
func (g *testGmail) tlsConfig() *tls.Config {
	return &tls.Config{RootCAs: g.pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
}

// dial opens a connection and signs in.
func (g *testGmail) dial(t *testing.T) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialStartTLS(g.Addr, &imapclient.Options{TLSConfig: g.tlsConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login(testUser, testPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGmailProfileListsSystemFolders checks the system folders, their
// attributes and their localized names.
func TestGmailProfileListsSystemFolders(t *testing.T) {
	g := startGmail(t, fakemail.GmailProfile{Language: "zh-Hans"}, "Receipts")
	c := g.dial(t)

	data, err := c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]imap.ListData, len(data))
	for _, d := range data {
		got[d.Mailbox] = *d
		if d.Delim != '/' {
			t.Errorf("%s: delimiter = %q", d.Mailbox, d.Delim)
		}
	}
	for name, attr := range map[string]imap.MailboxAttr{
		"INBOX":        "",
		"[Gmail]/所有邮件": imap.MailboxAttrAll,
		"[Gmail]/重要":   imap.MailboxAttrImportant,
		"[Gmail]/已发邮件": imap.MailboxAttrSent,
		"[Gmail]/草稿":   imap.MailboxAttrDrafts,
		"[Gmail]/垃圾邮件": imap.MailboxAttrJunk,
		"[Gmail]/已加星标": imap.MailboxAttrFlagged,
		"[Gmail]/回收站":  imap.MailboxAttrTrash,
		"Receipts":     "",
	} {
		d, ok := got[name]
		if !ok {
			t.Errorf("LIST did not report %q, got %v", name, listed(data))
			continue
		}
		if attr != "" && !hasAttr(d.Attrs, attr) {
			t.Errorf("%s: attributes %v lack %s", name, d.Attrs, attr)
		}
	}
	if d, ok := got["[Gmail]"]; !ok {
		t.Errorf("LIST did not report [Gmail], got %v", listed(data))
	} else if !hasAttr(d.Attrs, imap.MailboxAttrNoSelect) {
		t.Errorf("[Gmail]: attributes %v lack noselect", d.Attrs)
	}
}

func listed(data []*imap.ListData) []string {
	out := make([]string, 0, len(data))
	for _, d := range data {
		out = append(out, d.Mailbox)
	}
	return out
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

const invoiceRaw = "From: bob@example.com\r\n" +
	"To: ann@example.com\r\n" +
	"Subject: Invoice\r\n" +
	"Message-ID: <invoice@example.com>\r\n" +
	"Date: Mon, 2 Jan 2023 15:04:05 +0000\r\n" +
	"\r\n" +
	"Attached.\r\n"

// TestGmailProfileShowsOneMessageUnderEachLabel checks that a message
// added under several labels is the same message in each, with a UID of
// its own.
func TestGmailProfileShowsOneMessageUnderEachLabel(t *testing.T) {
	g := startGmail(t, fakemail.GmailProfile{}, "Receipts")
	const all = "[Gmail]/All Mail"
	// Inbox has mail of its own, so the same message is not the first of
	// each mailbox: a UID is the mailbox's, not the message's.
	if _, err := g.AppendLabels([]string{"INBOX"}, []byte("From: carol@example.com\r\nSubject: Hi\r\nMessage-ID: <hi@example.com>\r\n\r\nHello.\r\n"), nil); err != nil {
		t.Fatal(err)
	}
	uids, err := g.AppendLabels([]string{"INBOX", all, "Receipts"}, []byte(invoiceRaw), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := g.dial(t)
	for i, mailbox := range []string{"INBOX", all, "Receipts"} {
		if uid := uidByMessageID(t, c, mailbox, "invoice@example.com"); uid != uids[i] {
			t.Errorf("%s: UID = %d, appended %d", mailbox, uid, uids[i])
		}
	}
}

// uidByMessageID returns the UID of the message of a mailbox whose
// Message-ID is id, which the server reports without its angle brackets.
func uidByMessageID(t *testing.T, c *imapclient.Client, mailbox, id string) imap.UID {
	t.Helper()
	sel, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var set imap.SeqSet
	set.AddRange(1, sel.NumMessages)
	msgs, err := c.Fetch(set, &imap.FetchOptions{Envelope: true, UID: true}).Collect()
	if err != nil {
		t.Fatalf("fetch %s: %v", mailbox, err)
	}
	for _, m := range msgs {
		if m.Envelope != nil && m.Envelope.MessageID == id {
			return m.UID
		}
	}
	t.Fatalf("%s: no message with Message-ID %q (%d messages)", mailbox, id, len(msgs))
	return 0
}

// TestGmailProfileSavesDeliveredMailInSent checks that the server keeps
// its own copy of what SMTP delivers, as Gmail does.
func TestGmailProfileSavesDeliveredMailInSent(t *testing.T) {
	g := startGmail(t, fakemail.GmailProfile{})
	smtpServer, err := fakemail.StartSMTP(fakemail.SMTPOptions{
		Cert:     g.cert,
		Username: testUser,
		Password: testPassword,
		SaveSent: g.IMAP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { smtpServer.Close() })

	conn, err := net.Dial("tcp", smtpServer.Addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := smtp.NewClientStartTLS(conn, g.tlsConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", testUser, testPassword)); err != nil {
		t.Fatal(err)
	}
	if err := c.SendMail("ann@example.com", []string{"bob@example.com"}, strings.NewReader(invoiceRaw)); err != nil {
		t.Fatal(err)
	}
	if err := c.Quit(); err != nil {
		t.Fatal(err)
	}

	imap := g.dial(t)
	uidByMessageID(t, imap, "[Gmail]/Sent Mail", "invoice@example.com")
}

// TestGmailProfileRefusesMoreThanItsConnections checks Gmail's cap on how
// many connections an account may have at once.
func TestGmailProfileRefusesMoreThanItsConnections(t *testing.T) {
	g := startGmail(t, fakemail.GmailProfile{MaxConnections: 2})
	one, err := imapclient.DialStartTLS(g.Addr, &imapclient.Options{TLSConfig: g.tlsConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if err := one.Login(testUser, testPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	two, err := imapclient.DialStartTLS(g.Addr, &imapclient.Options{TLSConfig: g.tlsConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if err := two.Login(testUser, testPassword).Wait(); err != nil {
		t.Fatal(err)
	}

	over, err := imapclient.DialStartTLS(g.Addr, &imapclient.Options{TLSConfig: g.tlsConfig()})
	if err != nil {
		t.Fatalf("the connection itself should open: %v", err)
	}
	defer over.Close()
	err = over.Login(testUser, testPassword).Wait()
	if err == nil {
		t.Fatal("the third connection signed in")
	}
	var imapErr *imap.Error
	if !errors.As(err, &imapErr) {
		t.Fatalf("error = %v, want an IMAP NO", err)
	}
	if imapErr.Type != imap.StatusResponseTypeNo || imapErr.Code != imap.ResponseCodeUnavailable {
		t.Errorf("answer = %s %s, want NO [UNAVAILABLE]", imapErr.Type, imapErr.Code)
	}
	if !strings.Contains(imapErr.Text, "Too many simultaneous connections") {
		t.Errorf("text = %q", imapErr.Text)
	}

	// The refused connection ends and one more ends: the account has room
	// again.
	over.Close()
	one.Close()
	third, err := imapclient.DialStartTLS(g.Addr, &imapclient.Options{TLSConfig: g.tlsConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	// The server counts the connection out when it notices it ended,
	// which is after the client closed it.
	for i := 0; i < 100; i++ {
		err = third.Login(testUser, testPassword).Wait()
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("after one ended: %v", err)
	}
}

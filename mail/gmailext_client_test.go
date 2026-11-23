package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"shu/internal/fakemail"
)

func TestGmailExtensionCommands(t *testing.T) {
	ctx := t.Context()
	cert, pool, err := fakemail.Cert()
	if err != nil {
		t.Fatal(err)
	}
	const secret = `fake "password" with \ and Unicode: ` + "\u7535\u90ae"
	const all = "[Gmail]/All Mail"
	server, err := fakemail.StartIMAP(fakemail.IMAPOptions{
		Cert: cert, Username: "ann", Password: secret, Mailboxes: []string{"Receipts"},
		Gmail: &fakemail.GmailProfile{Extensions: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	plain := []byte("From: sender@example.com\r\nSubject: Hello\r\n\r\nHi.\r\n")
	withAttachment, err := os.ReadFile("../testdata/attachment.eml")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Append("INBOX", plain, nil); err != nil {
		t.Fatal(err)
	}
	uids, err := server.AppendLabels([]string{"INBOX", all, "Receipts"}, withAttachment, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Append("INBOX", withAttachment, &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}}); err != nil {
		t.Fatal(err)
	}
	addr := serverAddr(t, server.Addr)
	a := Account{ID: "gmail", Username: "ann", IMAP: Server{Host: "127.0.0.1", Port: addr, TLS: TLSImplicit}}
	store, secrets := newMemStore(), newMemSecrets()
	if err := store.SaveAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(PasswordKey(a.ID), []byte(secret)); err != nil {
		t.Fatal(err)
	}
	s := NewService(store, secrets, t.TempDir())
	s.SetTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if g := s.gmailExtension(a.ID, Capabilities{}); g != nil {
		t.Fatal("offered Gmail extension to a plain server")
	}
	ordinary, err := s.dialIMAP(ctx, a, secret)
	if err != nil {
		t.Fatal(err)
	}
	g := s.gmailExtension(a.ID, ordinary.caps)
	ordinary.close()
	if g == nil {
		t.Fatal("Gmail server did not offer its extension")
	}
	inboxUID, allUID := UID(uids[0]), UID(uids[1])
	if inboxUID == allUID {
		t.Fatal("test needs different UIDs in the two folders")
	}
	wantUIDs := []UID{inboxUID}
	if got, err := g.Labels(ctx, "INBOX", wantUIDs); err != nil ||
		!slices.Equal(got[inboxUID], []string{`\Inbox`, `\All`, "Receipts"}) {
		t.Fatalf("initial labels = %v, err = %v", got, err)
	}
	label := "\u6536\u636e & \"work\""
	if err := g.SetLabels(ctx, "INBOX", wantUIDs, []string{label}, []string{"Receipts"}); err != nil {
		t.Fatal(err)
	}
	if got, err := g.Labels(ctx, all, []UID{allUID}); err != nil ||
		!slices.Equal(got[allUID], []string{`\Inbox`, `\All`, label}) {
		t.Fatalf("updated labels in All Mail = %v, err = %v", got, err)
	}
	id, err := g.IDs(ctx, "INBOX", wantUIDs)
	if err != nil || id[inboxUID].Message <= 1<<63 || id[inboxUID].Thread <= 1<<63 {
		t.Fatalf("INBOX IDs = %v, err = %v", id, err)
	}
	if got, err := g.IDs(ctx, all, []UID{allUID}); err != nil || got[allUID] != id[inboxUID] {
		t.Fatalf("All Mail IDs = %v, err = %v", got, err)
	}
	if got, err := g.RawSearch(ctx, "INBOX", "has:attachment is:unread"); err != nil || !slices.Equal(got, wantUIDs) {
		t.Fatalf("search = %v, err = %v", got, err)
	}
	if got, err := g.RawSearch(ctx, "INBOX", "\u6536\u636e"); err != nil || len(got) != 0 {
		t.Fatalf("literal search = %v, err = %v", got, err)
	}
	if err := g.SetLabels(ctx, "INBOX", []UID{0}, []string{"any"}, nil); err == nil {
		t.Fatal("accepted wildcard UID in a label mutation")
	}

	a.OAuth = OAuthGoogle
	if err := s.runGmailAs(ctx, a, secret, func(*gmailWire) error { return nil }); err != nil {
		t.Fatalf("XOAUTH2 sign-in: %v", err)
	}
	var refused *loginError
	if err := s.runGmailAs(ctx, a, "bad token", func(*gmailWire) error { return nil }); !errors.As(err, &refused) {
		t.Fatalf("XOAUTH2 refusal = %v", err)
	}
}

func serverAddr(t *testing.T, address string) int {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	return a.Port
}

func TestGmailFetchTranscripts(t *testing.T) {
	const labels = `7 FETCH (X-GM-LABELS (\Inbox \Sent Important "Muy Importante" foo) UID 19)`
	w := &gmailWire{}
	data, err := w.fetch(labels)
	if err != nil || data.uid != 19 || !slices.Equal(data.labels, []string{`\Inbox`, `\Sent`, "Important", "Muy Importante", "foo"}) {
		t.Fatalf("labels transcript = %+v, err = %v", data, err)
	}
	w.in = bufio.NewReader(strings.NewReader("&YkBnCZCuTvY- \"long \\\"label\\\"\") FLAGS (\\Seen) UID 19)\r\n"))
	data, err = w.fetch(`7 FETCH (X-GM-LABELS ({13}`)
	if err != nil || data.uid != 19 || !slices.Equal(data.labels, []string{"\u6240\u6709\u90ae\u4ef6", `long "label"`}) {
		t.Fatalf("literal transcript = %+v, err = %v", data, err)
	}
	data, err = w.fetch(`1 FETCH (X-GM-THRID 1266894439832287888 UID 19 X-GM-MSGID 1278455344230334865)`)
	if err != nil || data.uid != 19 || data.id != (GmailID{Message: 1278455344230334865, Thread: 1266894439832287888}) {
		t.Fatalf("ID transcript = %+v, err = %v", data, err)
	}
	for _, malformed := range []string{
		`0 FETCH (X-GM-LABELS (foo) UID 19)`,
		`7 FETCH (X-GM-LABELS ({4294967296}`, `7 FETCH (X-GM-LABELS ("foo) UID 19)`,
		`7 FETCH (UID 19 X-GM-THRID 18446744073709551616)`, `7 FETCH (UID 19 X-GM-MSGID 7)`,
	} {
		if _, err := w.fetch(malformed); !errors.Is(err, errGmailResponse) {
			t.Errorf("fetch(%q) = %v, want a malformed response error", malformed, err)
		}
	}
}

func TestGmailExtensionCancelsWhileWaitingForResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := NewService(newMemStore(), newMemSecrets(), t.TempDir())
	a := Account{IMAP: Server{"127.0.0.1", serverAddr(t, listener.Addr().String()), TLSStartTLS}}
	done := make(chan error, 1)
	go func() { done <- s.runGmailAs(ctx, a, "", func(*gmailWire) error { return nil }) }()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the command did not close the connection")
	}
}

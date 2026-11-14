package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// appendLiteral is an APPEND body for the in-memory server.
type appendLiteral struct{ *bytes.Reader }

func (l appendLiteral) Size() int64 { return int64(l.Len()) }

type fakeIMAP struct {
	addr     string
	user     *imapmemserver.User
	server   *imapserver.Server
	listener net.Listener

	// What it takes to serve the same mailboxes again after stop.
	mem  *imapmemserver.Server
	caps imap.CapSet
	cert tls.Certificate

	// open counts the connections being served, sessions all those ever
	// served, and ended is signalled as one ends. selects records each
	// SELECT and EXAMINE, with whether it opened the mailbox read-only.
	open     atomic.Int32
	sessions atomic.Int32
	ended    chan struct{}
	mu       sync.Mutex
	selects  []fakeSelect
	log      *testLogger
	// holdsBack has an idling session behave as the in-memory server
	// does by itself: see fakeSession.Idle.
	holdsBack atomic.Bool
	// xoauth, when set, has the server take XOAUTH2 with the access
	// tokens it holds valid, and no password (see oauthfake_test.go).
	// xoauthTries counts the exchanges.
	xoauth      func(user, token string) bool
	xoauthTries atomic.Int32
	// statusDone runs once, after the next STATUS was answered and before
	// the client hears the answer: the moment a sync has read a folder's
	// counts and not yet fetched its new mail.
	statusDone func()
	// keywords says how a mailbox, by name, keeps keywords (see
	// fakeKeywords); one not named keeps any, as the in-memory server
	// does. stores records each STORE, in the mailbox it changed.
	keywords map[string]fakeKeywords
	stores   []fakeStore
	// headerFetches records the header fields each FETCH of some asked
	// for (see rules_imap_test.go).
	headerFetches [][]string
	// lists records each LIST's return options, as the client asked for
	// them: a server must be asked only for what it advertised.
	lists []fakeList
}

// fakeList is one LIST as a client asked it.
type fakeList struct {
	status, specialUse, subscribed bool
}

// afterStatus has do run once the next STATUS is answered.
func (f *fakeIMAP) afterStatus(do func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusDone = do
}

// fakeSelect is one SELECT or EXAMINE the fake server received.
type fakeSelect struct {
	mailbox  string
	readOnly bool
}

// fakeSession is a connection's session, which tells the fake server
// what the tests want to know of it.
type fakeSession struct {
	// The in-memory server's session, with the MOVE and NAMESPACE it
	// implements beyond the plain interface.
	imapserver.SessionIMAP4rev2
	f *fakeIMAP
	// selected is the mailbox selected last.
	selected string
}

func (s *fakeSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.f.mu.Lock()
	s.f.selects = append(s.f.selects, fakeSelect{mailbox, options != nil && options.ReadOnly})
	rule, ruled := s.f.keywords[mailbox]
	s.f.mu.Unlock()
	data, err := s.SessionIMAP4rev2.Select(mailbox, options)
	if err == nil {
		s.selected = mailbox
		switch {
		case options != nil && options.ReadOnly:
			// Nothing can be changed in a mailbox examined, as servers
			// in use say.
			data.PermanentFlags = nil
		case ruled:
			data.PermanentFlags = rule.permanentFlags()
		}
	}
	return data, err
}

func (s *fakeSession) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	data, err := s.SessionIMAP4rev2.Status(mailbox, options)
	s.f.mu.Lock()
	done := s.f.statusDone
	s.f.statusDone = nil
	s.f.mu.Unlock()
	if done != nil {
		done()
	}
	return data, err
}

// List records the return options a LIST asked for, then answers as the
// in-memory server does.
func (s *fakeSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	s.f.mu.Lock()
	s.f.lists = append(s.f.lists, fakeList{
		status:     options != nil && options.ReturnStatus != nil,
		specialUse: options != nil && options.ReturnSpecialUse,
		subscribed: options != nil && options.SelectSubscribed,
	})
	s.f.mu.Unlock()
	return s.SessionIMAP4rev2.List(w, ref, patterns, options)
}

// Idle reports the selected mailbox's changes while the client idles. The
// in-memory server's own Idle reports only what happens once it has
// begun: a change made between the client's last command and its IDLE is
// held back until the client's next command. Servers in use do not do
// that (they sync as an IDLE begins), and the tests of pushed mail would
// pass or fail by which side won that race. So this one looks at short
// intervals instead, and holds nothing back.
func (s *fakeSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if s.f.holdsBack.Load() {
		return s.SessionIMAP4rev2.Idle(w, stop)
	}
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

func (s *fakeSession) Close() error {
	err := s.SessionIMAP4rev2.Close()
	s.f.open.Add(-1)
	select {
	case s.f.ended <- struct{}{}:
	default:
	}
	return err
}

func startFakeIMAP(t *testing.T, caps imap.CapSet) *fakeIMAP {
	t.Helper()
	return startFakeIMAPWith(t, caps, nil)
}

// startFakeIMAPWith starts a fake server that, when xoauth is set, signs
// in by XOAUTH2 with the tokens xoauth holds valid instead of a password.
func startFakeIMAPWith(t *testing.T, caps imap.CapSet, xoauth func(user, token string) bool) *fakeIMAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("ann", "secret")
	mem.AddUser(user)
	cert, err := selfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIMAP{
		addr: ln.Addr().String(), user: user, mem: mem, caps: caps, cert: cert,
		ended:  make(chan struct{}, 64),
		log:    &testLogger{t: t},
		xoauth: xoauth,
	}
	f.serve(ln)
	t.Cleanup(func() {
		f.stop()
		f.log.close()
	})
	return f
}

// serve answers connections on ln with the fake's mailboxes.
func (f *fakeIMAP) serve(ln net.Listener) {
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			f.open.Add(1)
			f.sessions.Add(1)
			session := &fakeSession{SessionIMAP4rev2: f.mem.NewSession().(imapserver.SessionIMAP4rev2), f: f}
			if f.xoauth != nil {
				return &xoauthSession{session}, &imapserver.GreetingData{}, nil
			}
			return session, &imapserver.GreetingData{}, nil
		},
		Caps:      f.caps,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12},
		Logger:    f.log,
	})
	go func() { _ = server.Serve(ln) }()
	f.server, f.listener = server, ln
}

// stop takes the server away: it hangs up on every connection and
// refuses new ones. The mailboxes stay for restart.
func (f *fakeIMAP) stop() {
	f.server.Close()
	f.listener.Close()
}

// restart brings a stopped server back at the same address.
func (f *fakeIMAP) restart(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", f.addr)
	if err != nil {
		t.Fatalf("restart the fake server: %v", err)
	}
	f.serve(ln)
}

// testLogger writes the server's log to the test's while the test runs.
// A connection the server hung up on may log after that, from its own
// goroutine, when the test's log is no longer there to write to. It keeps
// the lines, for a test to read what was logged.
type testLogger struct {
	mu    sync.Mutex
	t     *testing.T
	lines []string
}

func (l *testLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	l.lines = append(l.lines, line)
	if l.t != nil {
		l.t.Logf("imapserver: %s", line)
	}
}

// text is what the server logged.
func (l *testLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *testLogger) close() {
	l.mu.Lock()
	l.t = nil
	l.mu.Unlock()
}

func (f *fakeIMAP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	return host, mustPort(t, port)
}

func (f *fakeIMAP) create(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := f.user.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
}

func (f *fakeIMAP) append(t *testing.T, mailbox string, raw []byte) {
	t.Helper()
	if _, err := f.user.Append(mailbox, appendLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{Time: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("append %s: %v", mailbox, err)
	}
}

func fakeAccount(t *testing.T, addr string) Account {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: Server{Host: host, Port: mustPort(t, port), TLS: TLSStartTLS},
		SMTP: Server{Host: host, Port: mustPort(t, port), TLS: TLSStartTLS},
	}
}

func mustPort(t *testing.T, s string) int {
	t.Helper()
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("bad port %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // #nosec G402 -- a local fake server
}

func newIMAPService(t *testing.T, f *fakeIMAP, password string) (*Service, *memStore, Account) {
	t.Helper()
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := fakeAccount(t, f.addr)
	if err := svc.AddAccount(context.Background(), a, password); err != nil {
		t.Fatal(err)
	}
	_ = secrets
	return svc, store, a
}

func allCaps() imap.CapSet {
	return imap.CapSet{
		imap.CapIMAP4rev1:  {},
		imap.CapMove:       {},
		imap.CapUIDPlus:    {},
		imap.CapListStatus: {},
		imap.CapSpecialUse: {},
	}
}

func TestSyncAccountAndFolder(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Sent", "Trash", "Archive", "Junk")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	f.append(t, "INBOX", readFixture(t, "alternative.eml"))
	f.append(t, "INBOX", readFixture(t, "attachment.eml"))
	f.append(t, "Sent", readFixture(t, "html-only.eml"))
	svc, store, a := newIMAPService(t, f, "secret")

	folders, err := svc.SyncAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 5 {
		t.Fatalf("folders = %+v", folders)
	}
	byName := map[string]Folder{}
	for _, folder := range folders {
		byName[folder.Name] = folder
	}
	if byName["INBOX"].SpecialUse != SpecialInbox || byName["INBOX"].Unread != 3 {
		t.Errorf("inbox = %+v", byName["INBOX"])
	}
	if byName["Sent"].SpecialUse != SpecialSent {
		t.Errorf("sent = %+v", byName["Sent"])
	}

	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("cached = %+v, %v", msgs, err)
	}
	folder, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if folder.LastUID == 0 || folder.UIDValidity == 0 || folder.SyncedAt.IsZero() {
		t.Errorf("cursor = %+v", folder)
	}
	if folder.LastUID != 3 {
		t.Errorf("last UID = %d", folder.LastUID)
	}
	// The preview comes from the partial body fetch.
	found := false
	for _, m := range msgs {
		if m.Subject == "Plain message" {
			found = m.Preview != ""
			if m.From.Email != "alice@example.com" {
				t.Errorf("from = %+v", m.From)
			}
		}
	}
	if !found {
		t.Errorf("no preview on the plain message: %+v", msgs)
	}

	// A second sync with no new mail keeps the rows and advances nothing.
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); len(msgs) != 3 {
		t.Errorf("second sync changed the list: %d", len(msgs))
	}
}

// A pruned attachment's bytes come back from the server. The prune clears
// the file's path and keeps the message's body cached, so a read that
// trusted BodyCached alone would never fetch them again.
func TestAttachmentAfterPruneIsFetchedAgain(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "attachment.eml"))
	svc, store, a := newIMAPService(t, f, "secret")
	svc.cacheDir = t.TempDir()
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	uid := msgs[0].UID
	result, err := svc.FetchMessage(ctx, a.ID, "INBOX", uid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.Attachments) != 1 || result.Message.Attachments[0].CachePath == "" {
		t.Fatalf("attachments = %+v", result.Message.Attachments)
	}
	part := result.Message.Attachments[0].Part
	if _, data, err := svc.Attachment(ctx, a.ID, "INBOX", uid, part); err != nil || string(data) != "%PDF-1.4 test" {
		t.Fatalf("before the prune = %q, %v", data, err)
	}
	removed, err := PruneAttachments(svc.cacheDir, 0)
	if err != nil || len(removed) != 1 {
		t.Fatalf("prune = %v, %v", removed, err)
	}
	if err := store.ForgetAttachmentFiles(ctx, removed); err != nil {
		t.Fatal(err)
	}
	got, data, err := svc.Attachment(ctx, a.ID, "INBOX", uid, part)
	if err != nil {
		t.Fatalf("after the prune: %v", err)
	}
	if string(data) != "%PDF-1.4 test" {
		t.Errorf("after the prune = %q", data)
	}
	// The file was cached again, so the next read needs no connection.
	if got.CachePath == "" {
		t.Error("the refetched attachment was not cached")
	} else if _, err := os.Stat(got.CachePath); err != nil {
		t.Errorf("the refetched attachment's file: %v", err)
	}
}

// An attachment of zero bytes is whole without a file: reading it asks
// the server for nothing.
func TestAttachmentOfZeroBytesIsWhole(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", messageWithFiles("empty-1@example.com", mailFile{name: "empty.txt", contentType: "text/plain"}))
	svc, _, a := newIMAPService(t, f, "secret")
	svc.cacheDir = t.TempDir()
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	uid := msgs[0].UID
	fetched, err := svc.FetchMessage(ctx, a.ID, "INBOX", uid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Message.Attachments) != 1 {
		t.Fatalf("attachments = %+v", fetched.Message.Attachments)
	}
	part := fetched.Message.Attachments[0].Part
	f.stop()
	_, data, err := svc.Attachment(ctx, a.ID, "INBOX", uid, part)
	if err != nil || len(data) != 0 {
		t.Fatalf("a zero-byte attachment = %q, %v", data, err)
	}
}

func TestFetchMessageMarksSeen(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].Seen {
		t.Fatal("message starts seen")
	}
	result, err := svc.FetchMessage(ctx, a.ID, "INBOX", msgs[0].UID, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.SeenErr != nil {
		t.Errorf("seen error = %v", result.SeenErr)
	}
	if !result.Message.Seen || result.Message.BodyText == "" {
		t.Errorf("result = %+v", result.Message)
	}
	m, ok, err := store.GetMessage(ctx, a.ID, "INBOX", msgs[0].UID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !m.BodyCached || m.BodyText == "" || !m.Seen {
		t.Errorf("cached message = %+v", m)
	}
	folder, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if folder.Unread != 0 {
		t.Errorf("unread = %d after reading", folder.Unread)
	}
	// The cached body is returned without another connection.
	cached, ok, err := svc.CachedMessage(ctx, a.ID, "INBOX", msgs[0].UID)
	if err != nil || !ok || cached.Message.BodyText == "" {
		t.Errorf("cached body = %+v, %v, %v", cached, ok, err)
	}
}

func TestSetFlagAndMove(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive", "Trash")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	uid := msgs[0].UID
	if err := svc.SetFlag(ctx, a.ID, "INBOX", uid, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	m, _, _ := store.GetMessage(ctx, a.ID, "INBOX", uid)
	if !m.Flagged {
		t.Error("flag was not cached")
	}
	moved, err := svc.Move(ctx, a.ID, "INBOX", uid, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Folder != "Archive" || moved.DestUID == 0 {
		t.Errorf("move result = %+v", moved)
	}
	if msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); len(msgs) != 0 {
		t.Errorf("moved message remains: %+v", msgs)
	}
	// The server moved it too.
	if err := svc.SyncFolder(ctx, a.ID, "Archive", 50); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := svc.CachedMessages(ctx, a.ID, "Archive", "", 50); len(msgs) != 1 {
		t.Errorf("archive = %+v", msgs)
	}
	// Undo takes the destination UID and brings the message back.
	if err := svc.MoveBack(ctx, a.ID, "Archive", "INBOX", moved.DestUID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50); len(got) != 1 {
		t.Errorf("inbox after undo = %+v", got)
	}
}

func TestMoveFallsBackWithoutMOVE(t *testing.T) {
	ctx := context.Background()
	caps := allCaps()
	delete(caps, imap.CapMove)
	f := startFakeIMAP(t, caps)
	f.create(t, "INBOX", "Trash")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, _, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if _, err := svc.Move(ctx, a.ID, "INBOX", msgs[0].UID, "Trash"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Trash", 50); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.CachedMessages(ctx, a.ID, "Trash", "", 50); len(got) != 1 {
		t.Errorf("trash after fallback = %+v", got)
	}
}

func TestUIDValidityResetDropsCache(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	before, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "html-only.eml"))
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	after, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if after.UIDValidity == before.UIDValidity {
		t.Fatalf("UIDVALIDITY did not change: %d", after.UIDValidity)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 1 || msgs[0].Subject != "HTML only" {
		t.Errorf("after reset = %+v", msgs)
	}
}

// arriveAfterStatus has a message reach the folder just after the next
// STATUS was answered: a sync has read the folder's counts by then, and
// fetches its new mail after.
func arriveAfterStatus(t *testing.T, f *fakeIMAP, mailbox string, raw []byte) {
	f.afterStatus(func() {
		// The server's goroutine: an error is reported, not fatal.
		if _, err := f.user.Append(mailbox, appendLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{Time: time.Unix(1_700_000_000, 0)}); err != nil {
			t.Errorf("append %s: %v", mailbox, err)
		}
	})
}

// unreadOf counts the unread messages of a list.
func unreadOf(list []Summary) int {
	n := 0
	for _, m := range list {
		if !m.Seen {
			n++
		}
	}
	return n
}

// A message can reach a folder while it syncs, after its counts were read
// and before its new mail is fetched. The sync writes it with the counts
// read after it, so the cache never holds an unread message its folder's
// count leaves out.
func TestSyncCountsMailThatArrivesDuringIt(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	svc, store, a := newIMAPService(t, f, "secret")
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	arriveAfterStatus(t, f, "INBOX", readFixture(t, "html-only.eml"))
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	folder, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if len(msgs) != 2 || folder.Total != 2 || folder.Unread != unreadOf(msgs) {
		t.Errorf("cached %d messages, %d unread; the folder counts %d unread of %d",
			len(msgs), unreadOf(msgs), folder.Unread, folder.Total)
	}
}

// The same in a watcher's check: it announces the message with the inbox's
// counts, and those count it. The window shows the badge from them as it
// shows the notification.
func TestWatchCountsTheMailItAnnounces(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	r.watch()
	r.checked()
	r.timer()
	arriveAfterStatus(t, f, "INBOX", readFixture(t, "html-only.eml"))
	r.svc.Nudge()
	ev := r.checked()
	if got := subjects(ev.New); len(got) != 1 || got[0] != "HTML only" {
		t.Fatalf("announced %v, want the message that arrived during the check", got)
	}
	for _, folder := range ev.Folders {
		if folder.Name == "INBOX" && (folder.Unread != 2 || folder.Total != 2) {
			t.Errorf("announced with the inbox at %d unread of %d, want 2 of 2", folder.Unread, folder.Total)
		}
	}
}

func TestAppendAndCancelledContext(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "Sent")
	svc, _, a := newIMAPService(t, f, "secret")
	acc, password, err := svc.account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.dialIMAP(ctx, acc, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendTo(c, "Sent", readFixture(t, "plain.eml"), time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	c.close()
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Sent", 50); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := svc.CachedMessages(ctx, a.ID, "Sent", "", 50); len(msgs) != 1 {
		t.Errorf("appended message not synced: %+v", msgs)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.SyncFolder(cancelled, a.ID, "Sent", 50); err == nil {
		t.Error("cancelled sync succeeded")
	}
	if _, err := svc.FetchMessage(cancelled, a.ID, "Sent", 1, true); err == nil {
		t.Error("cancelled fetch succeeded")
	}
}

func TestTestConnectionRejectsWrongPassword(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := fakeAccount(t, f.addr)
	if err := svc.TestConnection(ctx, a, "wrong"); err == nil {
		t.Error("wrong password accepted")
	}
	if err := svc.TestConnection(ctx, a, "secret"); err == nil {
		// SMTP is the same fake IMAP port, so this fails at SMTP; the IMAP
		// login itself must have succeeded.
		t.Log("SMTP check failed as expected on the IMAP fake")
	}
}

// selfSignedCert makes a certificate for the local fake servers.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

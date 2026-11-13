package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	smtp "github.com/emersion/go-smtp"
)

// attachEnv is a service with a draft file store, on fake IMAP and SMTP
// servers.
type attachEnv struct {
	svc     *Service
	store   *memStore
	secrets *memSecrets
	imap    *fakeIMAP
	smtp    *fakeSMTP
	a       Account
	// dataDir is the service's data directory, and files a directory of
	// the user's, where the files to attach are.
	dataDir string
	files   string
}

func newAttachEnv(t *testing.T, configure func(*smtp.Server)) *attachEnv {
	t.Helper()
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent", "Drafts")
	smtpServer := startFakeSMTPWith(t, configure)
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	env := &attachEnv{
		svc: svc, store: store, secrets: secrets, imap: imapServer, smtp: smtpServer,
		a: combinedAccount(t, imapServer, smtpServer), dataDir: t.TempDir(), files: t.TempDir(),
	}
	svc.SetDataDir(env.dataDir)
	if err := svc.AddAccount(ctx, env.a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, env.a.ID); err != nil {
		t.Fatal(err)
	}
	return env
}

// restart is a new service on the same cache, keychain and directories,
// as the app has after it was quit and launched again.
func (e *attachEnv) restart() *Service {
	svc := NewService(e.store, e.secrets, "")
	svc.SetTLSConfig(insecureTLS())
	svc.SetDataDir(e.dataDir)
	return svc
}

// file writes a file of the user's and returns its path.
func (e *attachEnv) file(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(e.files, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stored lists the files of the draft file store, sorted.
func (e *attachEnv) stored(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dataDir, draftFilesFolder))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	slices.Sort(out)
	return out
}

// attach attaches a file to a draft and fails the test if it cannot.
func (e *attachEnv) attach(t *testing.T, d Draft, path string) (Draft, Attachment) {
	t.Helper()
	saved, att, err := e.svc.AttachFile(context.Background(), d, path)
	if err != nil {
		t.Fatalf("attach %s: %v", path, err)
	}
	return saved, att
}

// sameFiles reports whether a draft's attachments are exactly the wanted
// ones, in order: name, type, size and content. An Attachment may carry
// bytes, so == does not compare it.
func sameFiles(got []Attachment, want ...Attachment) bool {
	return slices.EqualFunc(got, want, func(a, b Attachment) bool {
		return a.Filename == b.Filename && a.ContentType == b.ContentType && a.Size == b.Size &&
			a.ContentHash == b.ContentHash
	})
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sentPart is one part of a multipart message as Go's own mime packages
// read it, which share no code with the parser the app uses.
type sentPart struct {
	header textproto.MIMEHeader
	body   []byte
}

func sentParts(t *testing.T, raw string) []sentPart {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		t.Fatalf("content type %q: %v", msg.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	var parts []sentPart
	for {
		p, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
			body, err = base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "").Replace(string(body)))
			if err != nil {
				t.Fatal(err)
			}
		}
		parts = append(parts, sentPart{header: p.Header, body: body})
	}
	return parts
}

// names reads a part's file name from both headers that carry it, with
// Go's mime package.
func (p sentPart) names(t *testing.T) (disposition, contentType string) {
	t.Helper()
	kind, params, err := mime.ParseMediaType(p.header.Get("Content-Disposition"))
	if err != nil || kind != "attachment" {
		t.Fatalf("Content-Disposition %q: %q, %v", p.header.Get("Content-Disposition"), kind, err)
	}
	disposition = params["filename"]
	_, params, err = mime.ParseMediaType(p.header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type %q: %v", p.header.Get("Content-Type"), err)
	}
	contentType, err = new(mime.WordDecoder).DecodeHeader(params["name"])
	if err != nil {
		t.Fatal(err)
	}
	return disposition, contentType
}

// Attaching copies the file at once, under its content hash, so the draft
// needs the original no longer. The same file is attached once, and the
// same bytes are stored once however many drafts have them.
func TestAttachFileCopiesHashesAndDedupes(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("report "), 200)...)
	path := env.file(t, "report.pdf", pdf)

	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@exa", Subject: "Files", Body: "typed so far"}, path)
	if att.Filename != "report.pdf" || att.ContentType != "application/pdf" || att.Size != int64(len(pdf)) ||
		att.ContentHash != hashOf(pdf) {
		t.Fatalf("attachment = %+v", att)
	}
	// Attaching made the draft, with the text the window held.
	if d.ID == "" || d.To != "bob@exa" || d.Body != "typed so far" || !sameFiles(d.Attachments, att) ||
		!d.Dirty() {
		t.Fatalf("draft = %+v", d)
	}
	stored, err := os.ReadFile(filepath.Join(env.dataDir, draftFilesFolder, att.ContentHash))
	if err != nil || !bytes.Equal(stored, pdf) {
		t.Fatalf("stored copy: %d bytes, %v", len(stored), err)
	}
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Fatalf("draft file store = %v", got)
	}
	// It is no file of the cache, which is pruned.
	if strings.Contains(filepath.Join(env.dataDir, draftFilesFolder), "attachments") {
		t.Error("draft files share the attachment cache's directory")
	}

	// The original changes and then goes: the draft has what was attached.
	if err := os.WriteFile(path, []byte("rewritten since"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, changed := env.attach(t, d, path)
	if len(again.Attachments) != 2 || changed.ContentHash == att.ContentHash || changed.Filename != "report.pdf" {
		t.Fatalf("the changed file attached as %+v", again.Attachments)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := mustDraft(t, env.svc, d.ID); !sameFiles(got.Attachments, att, changed) {
		t.Fatalf("draft after the original went = %+v", got.Attachments)
	}

	// The same file under the same name is there once, however often it
	// is attached; the same bytes under another name are another
	// attachment of the one stored file.
	same := env.file(t, "report.pdf", pdf)
	revision := again.Revision
	twice, dup := env.attach(t, d, same)
	if len(twice.Attachments) != 2 || !sameFiles([]Attachment{dup}, att) || twice.Revision != revision {
		t.Fatalf("attached twice = %+v at revision %d, was %d", twice.Attachments, twice.Revision, revision)
	}
	renamed, copyOf := env.attach(t, d, env.file(t, "copy of report.pdf", pdf))
	if len(renamed.Attachments) != 3 || copyOf.ContentHash != att.ContentHash || copyOf.Filename != "copy of report.pdf" {
		t.Fatalf("renamed copy = %+v", renamed.Attachments)
	}
	other, shared := env.attach(t, Draft{AccountID: env.a.ID, Subject: "Another draft"}, same)
	if other.ID == d.ID || shared.ContentHash != att.ContentHash {
		t.Fatalf("second draft = %+v", other)
	}
	if got := env.stored(t); len(got) != 2 {
		t.Errorf("draft file store = %v, want the two contents once each", got)
	}

	// A save of the text, which is all a keystroke saves, leaves the
	// attachments alone whatever list comes with it.
	typed := mustDraft(t, env.svc, d.ID)
	typed.Body, typed.Attachments = "typed some more", nil
	saved := mustSaveDraft(t, env.svc, typed)
	if saved.Body != "typed some more" || len(saved.Attachments) != 3 {
		t.Fatalf("draft after a save of its text = %+v", saved)
	}

	// Removing takes it off the draft; removing it again is no error.
	removed, err := env.svc.RemoveAttachment(ctx, d.ID, att)
	if err != nil || !sameFiles(removed.Attachments, changed, copyOf) || removed.Revision <= saved.Revision {
		t.Fatalf("after removing = %+v, %v", removed, err)
	}
	if again, err := env.svc.RemoveAttachment(ctx, d.ID, att); err != nil || again.Revision != removed.Revision {
		t.Errorf("removing twice = revision %d, %v", again.Revision, err)
	}
	if _, err := env.svc.RemoveAttachment(ctx, "missing", att); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing from a draft that is not there = %v", err)
	}
}

// What cannot be attached is refused with an error that says why, and
// leaves nothing behind: no draft, no half-copied file.
func TestAttachFileRefusesWhatItCannotCopy(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	draft := Draft{ID: "d1", AccountID: env.a.ID, Subject: "Files"}

	if _, _, err := env.svc.AttachFile(ctx, draft, filepath.Join(env.files, "gone.pdf")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file = %v", err)
	}
	if _, _, err := env.svc.AttachFile(ctx, draft, env.files); !errors.Is(err, ErrAttachDirectory) {
		t.Errorf("a folder = %v", err)
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		locked := env.file(t, "locked.txt", []byte("no"))
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.svc.AttachFile(ctx, draft, locked); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("an unreadable file = %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := env.svc.AttachFile(cancelled, draft, env.file(t, "late.txt", []byte("late"))); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled copy = %v", err)
	}
	if _, _, err := env.svc.AttachFile(ctx, Draft{AccountID: "nobody"}, env.file(t, "a.txt", []byte("a"))); !errors.Is(err, ErrNotFound) {
		t.Errorf("for an unknown account = %v", err)
	}
	if _, ok, _ := env.store.Draft(ctx, "d1"); ok {
		t.Error("a refused attachment made a draft")
	}
	if got := env.stored(t); len(got) != 0 {
		t.Errorf("refused attachments left files: %v", got)
	}

	// A draft discarded, whose server copy is still to delete, takes no
	// more files.
	d, _ := env.attach(t, draft, env.file(t, "ok.txt", []byte("ok")))
	if _, ok, err := env.svc.updateDraftSync(ctx, d.ID, func(d *Draft) { d.PendingDelete = true }); err != nil || !ok {
		t.Fatalf("marking the draft discarded: %v, %v", ok, err)
	}
	if _, _, err := env.svc.AttachFile(ctx, d, env.file(t, "more.txt", []byte("more"))); !errors.Is(err, ErrDraftDiscarded) {
		t.Errorf("attaching to a discarded draft = %v", err)
	}

	// Without a directory for them, nothing is attached and nothing
	// pretends to be.
	bare, _, _ := newTestService()
	if err := bare.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bare.AttachFile(ctx, Draft{AccountID: "a1"}, env.file(t, "b.txt", []byte("b"))); err == nil {
		t.Error("a service without a data directory attached a file")
	}
	if err := bare.SweepDraftFiles(ctx); err != nil {
		t.Errorf("sweep without a data directory = %v", err)
	}
}

// A draft's attachments may total MaxAttachmentBytes and no more: the
// message is built in memory.
func TestAttachFileHardCap(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	// A file past the cap by itself is refused by its size, before any
	// of it is copied. It is sparse: nothing that large is written.
	huge := filepath.Join(env.files, "huge.bin")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, _, err := env.svc.AttachFile(ctx, Draft{ID: "d1", AccountID: env.a.ID}, huge); !errors.Is(err, ErrAttachTooLarge) {
		t.Fatalf("a file past the cap = %v", err)
	}
	if got := env.stored(t); len(got) != 0 {
		t.Errorf("the refused file left %v", got)
	}

	// A draft ten bytes short of the cap takes ten bytes and not eleven.
	// Its rows say so; no file of that size is made for the test.
	d := mustSaveDraft(t, env.svc, Draft{ID: "d1", AccountID: env.a.ID, Subject: "Nearly full"})
	if _, err := env.store.SetDraftAttachments(ctx, d.ID, []Attachment{
		{Filename: "big.bin", Size: MaxAttachmentBytes - 10, ContentHash: hashOf([]byte("big"))},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.AttachFile(ctx, d, env.file(t, "eleven.txt", []byte("12345678901"))); !errors.Is(err, ErrAttachTooLarge) {
		t.Fatalf("eleven bytes into ten = %v", err)
	}
	full, att := env.attach(t, d, env.file(t, "ten.txt", []byte("1234567890")))
	if full.AttachmentBytes() != MaxAttachmentBytes || att.Size != 10 {
		t.Fatalf("draft at the cap = %d bytes", full.AttachmentBytes())
	}
	if _, _, err := env.svc.AttachFile(ctx, d, env.file(t, "one.txt", []byte("1"))); !errors.Is(err, ErrAttachTooLarge) {
		t.Fatalf("one byte past the cap = %v", err)
	}
	if got := mustDraft(t, env.svc, d.ID); len(got.Attachments) != 2 {
		t.Errorf("draft = %+v", got.Attachments)
	}
	// The encoded size the warning is about is the files' and a third.
	if got := EncodedSize(3 << 20); got < 4<<20 || got > 4<<20+120_000 {
		t.Errorf("EncodedSize(3 MiB) = %d", got)
	}
	if EncodedSize(0) != 0 || EncodedSize(1) != 6 {
		t.Errorf("EncodedSize(0, 1) = %d, %d", EncodedSize(0), EncodedSize(1))
	}
}

// The sweep removes the files nothing names and no others: a file stays
// while a draft has it or an unsent message does.
func TestSweepDraftFiles(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	dir := filepath.Join(env.dataDir, draftFilesFolder)
	kept, keptAtt := env.attach(t, Draft{AccountID: env.a.ID, Subject: "Kept"}, env.file(t, "kept.txt", []byte("kept by a draft")))
	_, sharedAtt := env.attach(t, kept, env.file(t, "shared.txt", []byte("shared by two drafts")))
	gone, _ := env.attach(t, Draft{AccountID: env.a.ID, Subject: "Discarded"}, env.file(t, "gone.txt", []byte("only the discarded draft's")))
	gone, _ = env.attach(t, gone, env.file(t, "shared.txt", []byte("shared by two drafts")))
	goneHash := gone.Attachments[0].ContentHash

	// A file nothing names, as a removed attachment leaves, and a copy a
	// crash left half made.
	orphan := filepath.Join(dir, hashOf([]byte("orphan")))
	stale := filepath.Join(dir, incomingPrefix+"12345")
	for _, path := range []string{orphan, stale} {
		if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// An unsent message's file, which no draft has any more.
	queued := []byte("named by the outbox")
	queuedHash, err := func() (string, error) {
		env.svc.fileMu.Lock()
		defer env.svc.fileMu.Unlock()
		return env.svc.storeBytes(queued)
	}()
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingSend{
		ID: "o1", AccountID: env.a.ID, State: StateFailed, CreatedAt: time.Now(),
		Envelope: Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Queued",
			Attachments: []Attachment{{Filename: "queued.txt", Size: int64(len(queued)), ContentHash: queuedHash}}},
	}
	if err := env.store.EnqueueOutbox(ctx, pending); err != nil {
		t.Fatal(err)
	}

	// If what is in use cannot be listed, nothing is removed.
	env.store.failFileRefs = errors.New("disk error")
	before := env.stored(t)
	if err := env.svc.SweepDraftFiles(ctx); err == nil {
		t.Error("a sweep that could not list the files in use succeeded")
	}
	if got := env.stored(t); !slices.Equal(got, before) {
		t.Fatalf("a failed sweep removed files: %v, was %v", got, before)
	}
	env.store.failFileRefs = nil

	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{keptAtt.ContentHash, sharedAtt.ContentHash, goneHash, queuedHash}
	slices.Sort(want)
	if got := env.stored(t); !slices.Equal(got, want) {
		t.Fatalf("after the sweep = %v, want %v", got, want)
	}

	// Discarding a draft takes its files, except one another draft has.
	if err := env.svc.DiscardDraft(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	want = []string{keptAtt.ContentHash, sharedAtt.ContentHash, queuedHash}
	slices.Sort(want)
	if got := env.stored(t); !slices.Equal(got, want) {
		t.Fatalf("after the discard = %v, want %v", got, want)
	}
	// A removed attachment's file waits for the next sweep, which a new
	// run of the app makes as it starts.
	if _, err := env.svc.RemoveAttachment(ctx, kept.ID, sharedAtt); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); len(got) != 3 {
		t.Fatalf("removing an attachment swept: %v", got)
	}
	if err := env.restart().SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	want = []string{keptAtt.ContentHash, queuedHash}
	slices.Sort(want)
	if got := env.stored(t); !slices.Equal(got, want) {
		t.Fatalf("after the restart = %v, want %v", got, want)
	}
	// Once the message is delivered, or was cancelled before any of it
	// left, the outbox holds its file no longer.
	for _, state := range []string{StateUnknown, StateSending, StatePending, StateCancelled} {
		if err := env.store.SetOutboxState(ctx, "o1", state, ""); err != nil {
			t.Fatal(err)
		}
		if err := env.svc.SweepDraftFiles(ctx); err != nil {
			t.Fatal(err)
		}
		if has := slices.Contains(env.stored(t), queuedHash); has != (state != StateCancelled) {
			t.Fatalf("outbox %s: its file is there = %v", state, has)
		}
	}
	if got := env.stored(t); !slices.Equal(got, []string{keptAtt.ContentHash}) {
		t.Errorf("draft file store = %v", got)
	}
}

// A file being copied in while the sweep runs is not the sweep's to take.
func TestSweepSparesACopyInProgress(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	d, _ := env.attach(t, Draft{AccountID: env.a.ID}, env.file(t, "first.txt", []byte("first")))
	_, incoming, err := env.svc.copyIn(ctx, env.file(t, "second.txt", []byte("second")), MaxAttachmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(incoming); err != nil {
		t.Fatalf("the sweep took a copy in progress: %v", err)
	}
	env.svc.dropIncoming(incoming)
	if got := env.stored(t); !slices.Equal(got, []string{d.Attachments[0].ContentHash}) {
		t.Errorf("draft file store = %v", got)
	}
}

// The first acceptance criterion against the fake servers: a message with
// two attachments, one named in Chinese, is taken by the SMTP server and
// reads back with both names, types and contents. The outbox row has the
// envelope and names the files, and none of their bytes.
func TestSendWithAttachments(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x00, 0xff, 0x10, 0x80}, 5000)...)
	notes := []byte("第一季度的数字都在这里。\n")
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "Bob <bob@example.com>", Subject: "Quarterly files", Body: "Both attached."},
		env.file(t, "report.pdf", pdf))
	d, _ = env.attach(t, d, env.file(t, "季度计划 第一版.txt", notes))
	mustSyncDrafts(t, env.svc, env.a.ID)

	out := d.outgoing()
	out.DraftID = d.ID
	result, err := env.svc.Send(ctx, env.a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Copied || result.Warning != nil {
		t.Errorf("sent copy = %v, %v", result.Copied, result.Warning)
	}
	received := env.smtp.received()
	if len(received) != 1 {
		t.Fatalf("smtp messages = %d", len(received))
	}
	check := func(what, raw string) {
		t.Helper()
		m, err := ParseMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(m.BodyText, "Both attached.") || m.Subject != "Quarterly files" || m.MessageID != d.MessageID {
			t.Errorf("%s: message = %+v", what, m.Summary)
		}
		if len(m.Attachments) != 2 {
			t.Fatalf("%s: attachments = %+v", what, m.Attachments)
		}
		for i, want := range []struct {
			name, kind string
			data       []byte
		}{{"report.pdf", "application/pdf", pdf}, {"季度计划 第一版.txt", "text/plain", notes}} {
			got := m.Attachments[i]
			if got.Filename != want.name || got.ContentType != want.kind || !bytes.Equal(got.Data, want.data) {
				t.Errorf("%s: attachment %d = %q %q, %d bytes", what, i, got.Filename, got.ContentType, len(got.Data))
			}
		}
	}
	check("delivered", received[0])
	if strings.Contains(received[0], draftIDHeader) {
		t.Error("the draft header left the device")
	}
	sent := env.imap.messages(t, "Sent")
	if len(sent) != 1 {
		t.Fatalf("sent copies = %d", len(sent))
	}
	check("sent copy", sent[0].Raw)

	// The outbox kept the envelope, with what names the files, and no
	// raw message: that would be their bytes, encoded.
	row, err := env.store.Outbox(ctx, result.Outbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateSent || len(row.Raw) != 0 {
		t.Errorf("outbox row = state %q, %d bytes of raw message", row.State, len(row.Raw))
	}
	if !sameAttachments(row.Envelope.Attachments, d.Attachments) || row.Envelope.Attachments[0].Size != int64(len(pdf)) {
		t.Errorf("outbox envelope attachments = %+v", row.Envelope.Attachments)
	}
	envelope, err := json.Marshal(row.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope) > 1024 || bytes.Contains(envelope, []byte(base64.StdEncoding.EncodeToString(pdf[:30]))) ||
		bytes.Contains(envelope, notes[:12]) {
		t.Errorf("the outbox envelope holds file contents: %d bytes", len(envelope))
	}

	// The draft is gone, here and on the server, and its files with it.
	if _, ok, _ := env.store.Draft(ctx, d.ID); ok {
		t.Error("the sent draft's row remains")
	}
	if msgs := env.imap.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("server drafts after sending = %d", len(msgs))
	}
	if got := env.stored(t); len(got) != 0 {
		t.Errorf("files left after sending: %v", got)
	}
}

// An attachment removed before the send is no part of what is delivered.
func TestRemovedAttachmentIsNotSent(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	d, keep := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "One of two"},
		env.file(t, "keep.txt", []byte("this one goes")))
	d, drop := env.attach(t, d, env.file(t, "secret-plan.txt", []byte("this one must not leave")))
	d, err := env.svc.RemoveAttachment(ctx, d.ID, drop)
	if err != nil {
		t.Fatal(err)
	}
	out := d.outgoing()
	out.DraftID = d.ID
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	raw := env.smtp.received()[0]
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != keep.Filename || string(m.Attachments[0].Data) != "this one goes" {
		t.Fatalf("delivered attachments = %+v", m.Attachments)
	}
	if strings.Contains(raw, "secret-plan") || strings.Contains(raw, base64.StdEncoding.EncodeToString([]byte("this one must not leave"))) {
		t.Errorf("the removed attachment was delivered:\n%s", raw)
	}
	if sent := env.imap.messages(t, "Sent"); len(sent) != 1 || len(sent[0].Attachments) != 1 {
		t.Errorf("sent copy attachments = %+v", sent)
	}
	if got := env.stored(t); len(got) != 0 {
		t.Errorf("files left after sending: %v", got)
	}
}

// The server's verdict is the limit that counts. When it refuses the
// message, its words come back, and the draft is as it was, files and
// all, to change and send again.
func TestServerRejectionKeepsDraftAndAttachments(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	data := bytes.Repeat([]byte("big file "), 4000)
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Too big", Body: "See attached."},
		env.file(t, "big.bin", data))
	env.smtp.mu.Lock()
	env.smtp.refuse = &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Message size exceeds fixed limit"}
	env.smtp.mu.Unlock()

	out := d.outgoing()
	out.DraftID = d.ID
	_, err := env.svc.Send(ctx, env.a.ID, out)
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("send = %v, want the server's refusal", err)
	}
	if rejected.Code != 552 || !strings.Contains(rejected.Reply, "Message size exceeds fixed limit") || !rejected.TooLarge() {
		t.Errorf("refusal = %+v", rejected)
	}
	if errors.Is(err, ErrDeliveryUnknown) {
		t.Error("a message the server refused is reported as possibly delivered")
	}
	if len(env.smtp.received()) != 0 {
		t.Fatal("the refused message was delivered")
	}
	got := mustDraft(t, env.svc, d.ID)
	if got.Body != "See attached." || !sameFiles(got.Attachments, att) {
		t.Fatalf("draft after the refusal = %+v", got)
	}
	if stored := env.stored(t); !slices.Equal(stored, []string{att.ContentHash}) {
		t.Fatalf("files after the refusal = %v", stored)
	}
	pending, _ := env.svc.PendingSends(ctx, env.a.ID)
	if len(pending) != 1 || pending[0].State != StateFailed || !strings.Contains(pending[0].LastError, "552") || len(pending[0].Raw) != 0 {
		t.Fatalf("outbox = %+v", pending)
	}
	if sent := env.imap.messages(t, "Sent"); len(sent) != 0 {
		t.Errorf("a refused message was saved to Sent: %d", len(sent))
	}

	// A refusal that is not about the size says so too.
	env.smtp.mu.Lock()
	env.smtp.refuse = &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Attachment type not allowed"}
	env.smtp.mu.Unlock()
	if _, err := env.svc.Send(ctx, env.a.ID, out); !errors.As(err, &rejected) || rejected.Code != 554 || rejected.TooLarge() {
		t.Errorf("policy refusal = %v", err)
	}

	// The server takes it after all, as once the user made it smaller:
	// the same draft goes out.
	env.smtp.mu.Lock()
	env.smtp.refuse = nil
	env.smtp.mu.Unlock()
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	if received := env.smtp.received(); len(received) != 1 {
		t.Fatalf("delivered = %d", len(received))
	}
	if _, ok, _ := env.svc.Draft(ctx, d.ID); ok {
		t.Error("the sent draft remains")
	}
	// The two tries the server refused were of this message. Delivered,
	// it is not offered to send again, and nothing holds its file.
	if pending, _ := env.svc.PendingSends(ctx, env.a.ID); len(pending) != 0 {
		t.Errorf("outbox after the delivery = %+v", pending)
	}
	if stored := env.stored(t); len(stored) != 0 {
		t.Errorf("files after the delivery = %v", stored)
	}
}

// A message delivered closes the earlier tries of that message alone:
// another message that failed stays to be sent again, with its file.
func TestDeliveryLeavesOtherFailedSendsAlone(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	env.smtp.mu.Lock()
	env.smtp.refuse = &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Not now"}
	env.smtp.mu.Unlock()
	other, otherAtt := env.attach(t, Draft{AccountID: env.a.ID, To: "carol@example.com", Subject: "Other"},
		env.file(t, "other.txt", []byte("the other message's file")))
	otherOut := other.outgoing()
	otherOut.DraftID = other.ID
	if _, err := env.svc.Send(ctx, env.a.ID, otherOut); err == nil {
		t.Fatal("the refused message was sent")
	}
	// Its draft is discarded: only the outbox has the message now.
	if err := env.svc.DiscardDraft(ctx, other.ID); err != nil {
		t.Fatal(err)
	}

	env.smtp.mu.Lock()
	env.smtp.refuse = nil
	env.smtp.mu.Unlock()
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "This one"},
		env.file(t, "this.txt", []byte("this message's file")))
	out := d.outgoing()
	out.DraftID = d.ID
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	pending, _ := env.svc.PendingSends(ctx, env.a.ID)
	if len(pending) != 1 || pending[0].State != StateFailed || pending[0].Envelope.Subject != "Other" {
		t.Fatalf("outbox = %+v", pending)
	}
	if stored := env.stored(t); !slices.Equal(stored, []string{otherAtt.ContentHash}) {
		t.Fatalf("files = %v, want the failed message's", stored)
	}
	// It can still be sent, from its envelope and its file.
	if _, err := env.svc.RetryPending(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	received := env.smtp.received()
	if len(received) != 2 {
		t.Fatalf("delivered = %d", len(received))
	}
	if parts := sentParts(t, received[1]); len(parts) != 2 || string(parts[1].body) != "the other message's file" {
		t.Fatalf("the retried message has %d parts", len(parts))
	}
	if stored := env.stored(t); len(stored) != 0 {
		t.Errorf("files after the retry = %v", stored)
	}
}

// A server that announces how much it takes is not handed more: the
// message stays, and nothing of it is uploaded to be refused.
func TestSendOverAnnouncedSizeLimit(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, func(server *smtp.Server) { server.MaxMessageBytes = 8 << 10 })
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Too big"},
		env.file(t, "big.bin", bytes.Repeat([]byte("x"), 16<<10)))
	out := d.outgoing()
	out.DraftID = d.ID
	var told []int64
	_, err := env.svc.Send(WithSendProgress(ctx, func(sent, total int64) { told = append(told, sent) }), env.a.ID, out)
	var limit *SizeLimitError
	if !errors.As(err, &limit) || limit.Limit != 8<<10 || limit.Size < EncodedSize(16<<10) {
		t.Fatalf("send = %v", err)
	}
	if len(told) != 0 {
		t.Errorf("progress was reported for a message that was not sent: %v", told)
	}
	if got := mustDraft(t, env.svc, d.ID); !sameFiles(got.Attachments, att) {
		t.Errorf("draft = %+v", got)
	}
	if pending, _ := env.svc.PendingSends(ctx, env.a.ID); len(pending) != 1 || pending[0].State != StateFailed {
		t.Errorf("outbox = %+v", pending)
	}
	// Without the attachment it fits.
	d, err = env.svc.RemoveAttachment(ctx, d.ID, att)
	if err != nil {
		t.Fatal(err)
	}
	out = d.outgoing()
	out.DraftID = d.ID
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	if len(env.smtp.received()) != 1 {
		t.Error("the smaller message was not delivered")
	}
}

// A message that could not be sent is resent from the outbox after a
// restart: built again from its envelope and the files it names, since
// the row holds no raw message.
func TestRetryPendingRebuildsWithAttachments(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	image := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1, 2, 3, 250}, 3000)...)
	text := []byte("二〇二六年预算\n")
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Cc: "carol@example.com", Subject: "Budget", Body: "Attached."},
		env.file(t, "chart.png", image))
	d, _ = env.attach(t, d, env.file(t, "预算.txt", text))
	out := d.outgoing()
	out.DraftID = d.ID

	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Send(ctx, env.a.ID, out); err == nil {
		t.Fatal("send with a wrong password succeeded")
	}
	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("secret")); err != nil {
		t.Fatal(err)
	}
	pending, _ := env.svc.PendingSends(ctx, env.a.ID)
	if len(pending) != 1 || pending[0].State != StateFailed || len(pending[0].Raw) != 0 ||
		!sameAttachments(pending[0].Envelope.Attachments, d.Attachments) {
		t.Fatalf("outbox = %+v", pending)
	}

	// The user discards the draft; the unsent message still needs its
	// files, and keeps them through the sweeps of a restart.
	if err := env.svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	restarted := env.restart()
	if err := restarted.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); len(got) != 2 {
		t.Fatalf("files of the unsent message = %v", got)
	}

	result, err := restarted.RetryPending(ctx, pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Copied {
		t.Errorf("sent copy warning = %v", result.Warning)
	}
	received := env.smtp.received()
	if len(received) != 1 {
		t.Fatalf("smtp messages = %d", len(received))
	}
	for what, raw := range map[string]string{"delivered": received[0], "sent copy": env.imap.messages(t, "Sent")[0].Raw} {
		m, err := ParseMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if m.MessageID != d.MessageID || len(m.Cc) != 1 || !strings.Contains(m.BodyText, "Attached.") {
			t.Errorf("%s: message = %+v", what, m.Summary)
		}
		if len(m.Attachments) != 2 || m.Attachments[0].Filename != "chart.png" || m.Attachments[0].ContentType != "image/png" ||
			!bytes.Equal(m.Attachments[0].Data, image) || m.Attachments[1].Filename != "预算.txt" ||
			!bytes.Equal(m.Attachments[1].Data, text) {
			t.Errorf("%s: attachments = %+v", what, m.Attachments)
		}
	}
	// Delivered, the message holds its files no longer.
	if got := env.stored(t); len(got) != 0 {
		t.Errorf("files left after the retry: %v", got)
	}

	// An entry whose file is gone is not sent without it.
	lost, lostAtt := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Lost"}, env.file(t, "lost.txt", []byte("lost")))
	if err := env.store.EnqueueOutbox(ctx, PendingSend{
		ID: "o-lost", AccountID: env.a.ID, State: StateFailed, CreatedAt: time.Now(), Envelope: lost.outgoing(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.dataDir, draftFilesFolder, lostAtt.ContentHash), []byte("LOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.RetryPending(ctx, "o-lost"); !errors.Is(err, ErrAttachmentMissing) {
		t.Errorf("retry with a damaged file = %v", err)
	}
	if err := os.Remove(filepath.Join(env.dataDir, draftFilesFolder, lostAtt.ContentHash)); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.RetryPending(ctx, "o-lost"); !errors.Is(err, ErrAttachmentMissing) {
		t.Errorf("retry with a missing file = %v", err)
	}
	out = lost.outgoing()
	out.DraftID = lost.ID
	if _, err := restarted.Send(ctx, env.a.ID, out); !errors.Is(err, ErrAttachmentMissing) {
		t.Errorf("send with a missing file = %v", err)
	}
	if len(env.smtp.received()) != 1 {
		t.Error("a message was sent without its attachment")
	}
}

// A message resent from the outbox retires its draft only if the draft
// still has the files that were sent.
func TestRetryKeepsADraftWhoseAttachmentsChanged(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Files", Body: "x"},
		env.file(t, "first.txt", []byte("first")))
	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	out := d.outgoing()
	out.DraftID = d.ID
	if _, err := env.svc.Send(ctx, env.a.ID, out); err == nil {
		t.Fatal("send with a wrong password succeeded")
	}
	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("secret")); err != nil {
		t.Fatal(err)
	}
	// Since the failed send, the user swapped the file.
	if _, err := env.svc.RemoveAttachment(ctx, d.ID, att); err != nil {
		t.Fatal(err)
	}
	env.attach(t, d, env.file(t, "second.txt", []byte("second")))
	pending, _ := env.svc.PendingSends(ctx, env.a.ID)
	if _, err := env.svc.RetryPending(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	m, err := ParseMessage([]byte(env.smtp.received()[0]))
	if err != nil || len(m.Attachments) != 1 || m.Attachments[0].Filename != "first.txt" {
		t.Fatalf("resent attachments = %+v, %v", m.Attachments, err)
	}
	got, ok, _ := env.svc.Draft(ctx, d.ID)
	if !ok || len(got.Attachments) != 1 || got.Attachments[0].Filename != "second.txt" {
		t.Fatalf("draft after the retry = %+v, found %v", got, ok)
	}
	if stored := env.stored(t); !slices.Equal(stored, []string{got.Attachments[0].ContentHash}) {
		t.Errorf("files = %v", stored)
	}
}

// The copy in the server's Drafts folder is the whole draft, attachments
// included, so another mail client can go on with it.
func TestDraftServerCopyCarriesAttachments(t *testing.T) {
	env := newAttachEnv(t, nil)
	data := bytes.Repeat([]byte{7, 0, 255, 13, 10}, 2000)
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Bcc: "erin@example.com", Subject: "With a file", Body: "Draft text"},
		env.file(t, "数据 data.bin", data))
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Uploaded != 1 || result.Deferred != 0 {
		t.Fatalf("result = %+v", result)
	}
	msgs := env.imap.messages(t, "Drafts")
	if len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	m := msgs[0]
	if !m.hasFlag(imap.FlagDraft) || m.Subject != "With a file" || !strings.Contains(m.BodyText, "Draft text") {
		t.Errorf("server copy = %+v", m.Summary)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "数据 data.bin" || !bytes.Equal(m.Attachments[0].Data, data) {
		t.Fatalf("server copy attachments = %+v", m.Attachments)
	}
	if !strings.Contains(m.Raw, draftIDHeader+": "+d.ID) || !strings.Contains(m.Raw, "erin@example.com") {
		t.Errorf("server copy lost a draft header:\n%.600s", m.Raw)
	}
	// The list's row says the draft has attachments.
	list, _ := env.svc.DraftList(context.Background(), env.a.ID, "", 50)
	if len(list) != 1 || !list[0].HasAttachments {
		t.Errorf("draft list = %+v", list)
	}

	// Adding a file is an edit: the copy is replaced by one with both.
	env.attach(t, d, env.file(t, "notes.txt", []byte("more")))
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	msgs = env.imap.messages(t, "Drafts")
	if len(msgs) != 1 || len(msgs[0].Attachments) != 2 || msgs[0].UID == m.UID {
		t.Fatalf("server drafts after attaching = %+v", msgs)
	}
	// So is removing one.
	if _, err := env.svc.RemoveAttachment(context.Background(), d.ID, msgs[0].Attachments[0].stored()); err != nil {
		t.Fatal(err)
	}
	mustSyncDrafts(t, env.svc, env.a.ID)
	msgs = env.imap.messages(t, "Drafts")
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].Filename != "notes.txt" {
		t.Fatalf("server drafts after removing = %+v", msgs)
	}
}

// stored is a parsed attachment as a draft names it.
func (a Attachment) stored() Attachment {
	return Attachment{Filename: a.Filename, ContentType: a.ContentType, Size: a.Size, ContentHash: a.ContentHash}
}

// A draft with attachments written in another mail client is taken over
// with them, and an edit here publishes them again: the replacement is
// the whole draft, not one that lost its files.
func TestAdoptDraftKeepsAttachments(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	// The other client's draft: a file named in Chinese, the name in the
	// forms mail clients write, and a second one.
	photo := bytes.Repeat([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 16}, 700)
	raw := "From: Ann <ann@example.com>\r\nTo: Bob <bob@example.com>\r\n" +
		"Subject: From the phone\r\nMessage-ID: <phone-9@example.com>\r\n" +
		"Date: Tue, 03 Jan 2023 10:20:00 +0000\r\n" +
		"Content-Type: multipart/mixed; boundary=\"mix\"\r\n\r\n" +
		"--mix\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nTyped on the phone.\r\n" +
		"--mix\r\nContent-Type: image/jpeg; name=\"=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("照片 1.jpg")) + "?=\"\r\n" +
		"Content-Disposition: attachment; filename*=UTF-8''%E7%85%A7%E7%89%87%201.jpg\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(photo) + "\r\n" +
		"--mix\r\nContent-Type: application/pdf; name=\"report.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQgdGVzdA==\r\n" +
		"--mix--\r\n"
	env.imap.append(t, "Drafts", []byte(raw))
	if err := env.svc.SyncFolder(ctx, env.a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := env.svc.DraftList(ctx, env.a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != "" {
		t.Fatalf("draft list = %+v", list)
	}
	d, err := env.svc.AdoptDraft(ctx, env.a.ID, "Drafts", list[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Attachment{
		{Filename: "照片 1.jpg", ContentType: "image/jpeg", Size: int64(len(photo)), ContentHash: hashOf(photo)},
		{Filename: "report.pdf", ContentType: "application/pdf", Size: 13, ContentHash: hashOf([]byte("%PDF-1.4 test"))},
	}
	if !sameFiles(d.Attachments, want...) || d.Body != "Typed on the phone." || d.Dirty() {
		t.Fatalf("adopted draft = %+v", d)
	}
	// The bytes are in the draft file store, for good: not in the cache.
	wantFiles := []string{want[0].ContentHash, want[1].ContentHash}
	slices.Sort(wantFiles)
	if got := env.stored(t); !slices.Equal(got, wantFiles) {
		t.Fatalf("draft file store = %v", got)
	}
	if stored, err := os.ReadFile(filepath.Join(env.dataDir, draftFilesFolder, want[0].ContentHash)); err != nil || !bytes.Equal(stored, photo) {
		t.Fatalf("stored photo: %d bytes, %v", len(stored), err)
	}
	// Nothing was edited, so the other client's message stays.
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Uploaded != 0 {
		t.Errorf("result = %+v", result)
	}
	if msgs := env.imap.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].UID != d.ServerUID {
		t.Fatalf("server drafts = %+v", msgs)
	}
	// Adopting again makes no second draft and no second copy.
	if again, err := env.svc.AdoptDraft(ctx, env.a.ID, "Drafts", list[0].UID); err != nil || again.ID != d.ID {
		t.Fatalf("second adoption = %+v, %v", again, err)
	}

	d.Body = "Typed on the phone, finished at the desk."
	mustSaveDraft(t, env.svc, d)
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	msgs := env.imap.messages(t, "Drafts")
	if len(msgs) != 1 {
		t.Fatalf("server drafts after the edit = %d, want the replacement alone", len(msgs))
	}
	m := msgs[0]
	if m.MessageID != "phone-9@example.com" || !strings.Contains(m.BodyText, "finished at the desk") {
		t.Errorf("replacement = %+v", m.Summary)
	}
	if len(m.Attachments) != 2 || m.Attachments[0].Filename != "照片 1.jpg" || m.Attachments[0].ContentType != "image/jpeg" ||
		!bytes.Equal(m.Attachments[0].Data, photo) || m.Attachments[1].Filename != "report.pdf" ||
		string(m.Attachments[1].Data) != "%PDF-1.4 test" {
		t.Fatalf("replacement attachments = %+v", m.Attachments)
	}
}

// A draft another mail client replaced while its window stayed open comes
// back with the window's next save, and with the files it had: the sweep
// in between does not take them.
func TestReplacedDraftComesBackWithItsAttachments(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Plan", Body: "written here"},
		env.file(t, "plan.txt", []byte("the plan")))
	mustSyncDrafts(t, env.svc, env.a.ID)
	uploaded := mustDraft(t, env.svc, d.ID)
	env.imap.remove(t, "Drafts", uploaded.ServerUID)
	env.imap.append(t, "Drafts", foreignDraft("elsewhere-7@example.com", "Plan", "edited on the phone"))
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Replaced != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, ok, _ := env.svc.Draft(ctx, d.ID); ok {
		t.Fatal("the local draft outlived its replacement")
	}
	// Another draft is sent meanwhile, which sweeps.
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if got := env.stored(t); !slices.Equal(got, []string{att.ContentHash}) {
		t.Fatalf("the sweep took the files of a draft whose window is open: %v", got)
	}
	back := mustSaveDraft(t, env.svc, uploaded)
	if !back.Conflict || back.Body != "written here" || !sameFiles(back.Attachments, att) {
		t.Fatalf("draft saved after its replacement = %+v", back)
	}
	mustSyncDrafts(t, env.svc, env.a.ID)
	var kept *serverMessage
	msgs := env.imap.messages(t, "Drafts")
	for i := range msgs {
		if msgs[i].MessageID == back.MessageID {
			kept = &msgs[i]
		}
	}
	if len(msgs) != 2 || kept == nil || len(kept.Attachments) != 1 || string(kept.Attachments[0].Data) != "the plan" {
		t.Fatalf("server drafts = %+v", msgs)
	}
}

// A draft heavy with attachments is not uploaded at the pauses of typing,
// where every upload is the whole draft again. It goes up with the sync
// that closing its window or syncing the account asks for.
func TestHeavyDraftWaitsForAFullSync(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	big := bytes.Repeat([]byte("0123456789abcdef"), DraftAutoUploadLimit/16+1)
	heavy, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Heavy", Body: "v1"},
		env.file(t, "big.bin", big))
	light, _ := env.attach(t, Draft{AccountID: env.a.ID, Subject: "Light", Body: "v1"},
		env.file(t, "small.txt", bytes.Repeat([]byte("s"), 2000)))
	if heavy.AttachmentBytes() <= DraftAutoUploadLimit || light.AttachmentBytes() > DraftAutoUploadLimit {
		t.Fatalf("sizes = %d, %d", heavy.AttachmentBytes(), light.AttachmentBytes())
	}

	result, err := env.svc.SyncDraftEdits(ctx, env.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Uploaded != 1 || result.Deferred != 1 {
		t.Fatalf("typing-pause sync = %+v", result)
	}
	msgs := env.imap.messages(t, "Drafts")
	if len(msgs) != 1 || msgs[0].Subject != "Light" || len(msgs[0].Attachments) != 1 {
		t.Fatalf("server drafts after the pause = %+v", msgs)
	}
	if got := mustDraft(t, env.svc, heavy.ID); !got.Dirty() || got.ServerUID != 0 {
		t.Fatalf("heavy draft after the pause = %+v", got)
	}
	// Typing on changes nothing about that, however often it pauses.
	heavy.Body = "v2"
	mustSaveDraft(t, env.svc, heavy)
	for i := 0; i < 2; i++ {
		if result, err := env.svc.SyncDraftEdits(ctx, env.a.ID); err != nil || result.Uploaded != 0 || result.Deferred != 1 {
			t.Fatalf("pause %d = %+v, %v", i, result, err)
		}
	}
	if msgs := env.imap.messages(t, "Drafts"); len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}

	// The full sync uploads it, whole.
	if result := mustSyncDrafts(t, env.svc, env.a.ID); result.Uploaded != 1 || result.Deferred != 0 {
		t.Fatalf("full sync = %+v", result)
	}
	msgs = env.imap.messages(t, "Drafts")
	var up *serverMessage
	for i := range msgs {
		if msgs[i].Subject == "Heavy" {
			up = &msgs[i]
		}
	}
	if len(msgs) != 2 || up == nil || !strings.Contains(up.BodyText, "v2") || len(up.Attachments) != 1 ||
		!bytes.Equal(up.Attachments[0].Data, big) {
		t.Fatalf("server drafts after the full sync = %d", len(msgs))
	}
	if got := mustDraft(t, env.svc, heavy.ID); got.Dirty() {
		t.Errorf("heavy draft after the full sync = %+v", got)
	}
	// A discard is not held back by the size: its copy goes at once.
	if err := env.svc.DiscardDraft(ctx, heavy.ID); err != nil {
		t.Fatal(err)
	}
	if msgs := env.imap.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].Subject != "Light" {
		t.Errorf("server drafts after the discard = %+v", msgs)
	}
}

// The progress of a send never goes back, starts below the message's
// size and reaches it once the server took the message.
func TestSendReportsProgress(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	data := bytes.Repeat([]byte("progress "), 150_000)
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Progress", Body: "x"},
		env.file(t, "big.txt", data))
	out := d.outgoing()
	out.DraftID = d.ID
	type step struct{ sent, total int64 }
	var steps []step
	ctx = WithSendProgress(ctx, func(sent, total int64) { steps = append(steps, step{sent, total}) })
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 20 {
		t.Fatalf("progress was reported %d times", len(steps))
	}
	total := steps[0].total
	if steps[0].sent != 0 || total < EncodedSize(int64(len(data))) {
		t.Errorf("first report = %+v", steps[0])
	}
	for i, s := range steps {
		if s.total != total || s.sent > total || (i > 0 && s.sent <= steps[i-1].sent) {
			t.Fatalf("report %d = %+v after %+v", i, s, steps[i-1])
		}
		if i < len(steps)-1 && s.sent >= total {
			t.Fatalf("report %d of %d says the whole message was sent", i, len(steps))
		}
	}
	if last := steps[len(steps)-1]; last.sent != total {
		t.Errorf("last report = %+v, want the total", last)
	}
	// The total is the message as the server got it.
	if got := int64(len(env.smtp.received()[0])); got < total-2 || got > total+2 {
		t.Errorf("total %d, server received %d bytes", total, got)
	}
	// A message without attachments reports the same way.
	steps = nil
	if _, err := env.svc.Send(ctx, env.a.ID, Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Small", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 || steps[0].sent != 0 || steps[len(steps)-1].sent != steps[0].total {
		t.Errorf("small message progress = %+v", steps)
	}
}

// Cancelling stops a send that is under way. Once part of the message
// left, nobody can tell whether the server has it: the outbox says so,
// and keeps the entry from being resent on its own. The draft and its
// files are as they were.
func TestCancelSendMidTransfer(t *testing.T) {
	env := newAttachEnv(t, nil)
	data := bytes.Repeat([]byte("mid-transfer "), 300_000)
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Cancelled", Body: "x"},
		env.file(t, "big.bin", data))
	out := d.outgoing()
	out.DraftID = d.ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last, total int64
	ctx = WithSendProgress(ctx, func(sent, all int64) {
		last, total = sent, all
		if sent > 0 {
			// Some of it is out: the user presses Cancel.
			cancel()
		}
	})
	_, err := env.svc.Send(ctx, env.a.ID, out)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("send = %v, want cancelled with an unknown outcome", err)
	}
	if last <= 0 || last >= total/2 {
		t.Errorf("the send went on to %d of %d after the cancel", last, total)
	}
	if got := env.smtp.received(); len(got) != 0 {
		t.Fatalf("the cancelled message was delivered: %d", len(got))
	}
	pending, _ := env.svc.PendingSends(context.Background(), env.a.ID)
	if len(pending) != 1 || pending[0].State != StateUnknown || pending[0].LastError == "" || len(pending[0].Raw) != 0 {
		t.Fatalf("outbox = %+v", pending)
	}
	got := mustDraft(t, env.svc, d.ID)
	if got.Body != "x" || !sameFiles(got.Attachments, att) {
		t.Fatalf("draft after the cancel = %+v", got)
	}
	if stored := env.stored(t); !slices.Equal(stored, []string{att.ContentHash}) {
		t.Errorf("files after the cancel = %v", stored)
	}
	if sent := env.imap.messages(t, "Sent"); len(sent) != 0 {
		t.Errorf("a cancelled message was saved to Sent")
	}
	// The same draft can be sent after all.
	if _, err := env.svc.Send(context.Background(), env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	if len(env.smtp.received()) != 1 {
		t.Error("the draft was not sent after the cancel")
	}
}

// A send cancelled before any of the message left was not delivered, and
// the outbox does not offer it again: the draft is still there to send.
func TestCancelSendBeforeAnythingLeft(t *testing.T) {
	env := newAttachEnv(t, nil)
	d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Not sent", Body: "x"},
		env.file(t, "a.txt", []byte("a")))
	out := d.outgoing()
	out.DraftID = d.ID
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := env.svc.Send(ctx, env.a.ID, out)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("send = %v, want cancelled with nothing sent", err)
	}
	if got := env.smtp.received(); len(got) != 0 {
		t.Fatalf("delivered = %d", len(got))
	}
	if pending, _ := env.svc.PendingSends(context.Background(), env.a.ID); len(pending) != 0 {
		t.Errorf("a send cancelled before it began is offered for retry: %+v", pending)
	}
	env.store.mu.Lock()
	states := map[string]int{}
	for _, p := range env.store.outbox {
		states[p.State]++
	}
	env.store.mu.Unlock()
	if len(states) != 1 || states[StateCancelled] != 1 {
		t.Errorf("outbox states = %v", states)
	}
	if got := mustDraft(t, env.svc, d.ID); !sameFiles(got.Attachments, att) {
		t.Errorf("draft = %+v", got)
	}
	// Its file is the draft's still, though the cancelled entry names it.
	if err := env.svc.SweepDraftFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored := env.stored(t); !slices.Equal(stored, []string{att.ContentHash}) {
		t.Errorf("files = %v", stored)
	}
}

// A server that stops reading leaves the send waiting in a write. A
// cancel must end that wait at once, not when the write times out.
func TestCancelUnblocksAStalledSend(t *testing.T) {
	env := newAttachEnv(t, nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	env.smtp.mu.Lock()
	env.smtp.onData = func(io.Reader) error {
		<-release
		return errors.New("the test is over")
	}
	env.smtp.mu.Unlock()
	// More than the sockets between the two can hold, so that the send
	// has to wait for a server that reads nothing.
	path := filepath.Join(env.files, "stall.bin")
	if err := os.WriteFile(path, make([]byte, 24<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Stalled"}, path)
	out := d.outgoing()
	out.DraftID = d.ID

	// With no time limit of its own on a write, only the cancel can end
	// the wait: if it does not, the test never ends, and fails for that.
	blockTimeout := smtpBlockTimeout
	smtpBlockTimeout = 24 * time.Hour
	t.Cleanup(func() { smtpBlockTimeout = blockTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.svc.Send(ctx, env.a.ID, out)
		done <- err
	}()
	// The wait in the write is what says the send began: how much of the
	// message it has told of by then depends on the sockets' buffers, and
	// a wait that begins before the first report is still a stall.
	for !sendWaitsOnNetwork() {
		select {
		case err := <-done:
			t.Fatalf("the send ended before it had to wait: %v", err)
		default:
			runtime.Gosched()
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatalf("send = %v", err)
	}
	if pending, _ := env.svc.PendingSends(context.Background(), env.a.ID); len(pending) != 1 || pending[0].State != StateUnknown {
		t.Errorf("outbox = %+v", pending)
	}
}

// sendWaitsOnNetwork reports whether a goroutine is parked in a network
// write of a message being sent, by the goroutines' own account: no clock
// can tell a slow write from one that waits.
func sendWaitsOnNetwork() bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for _, goroutine := range strings.Split(string(buf), "\n\n") {
		header, _, _ := strings.Cut(goroutine, "\n")
		// The socket's own write, not a write that waits to read, as
		// the first one of a TLS connection does.
		if strings.Contains(header, "[IO wait") && strings.Contains(goroutine, "(*Service).transmit") &&
			strings.Contains(goroutine, "internal/poll.(*FD).Write(") {
			return true
		}
	}
	return false
}

// File names reach the recipient as they were: outside ASCII, with
// spaces, quotes, a semicolon, and long. Each is read back by the app's
// parser and by Go's mime package, from both headers that carry a name,
// and no line of the message is longer than a message's lines may be.
func TestAttachmentNamesSurviveMIME(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	names := []string{
		"report.pdf",
		"Q1 report (final) v2.pdf",
		"季度计划 第一版.pdf",
		"party 🎉 photos.zip",
		`she said "hi".txt`,
		"notes; draft 2.txt",
		`back\slash and 100% & more=?.txt`,
		"Ünïcödé und Umlaute — résumé.docx",
		strings.Repeat("a", 196) + ".txt",
		strings.Repeat("长", 196) + ".txt",
		"=?UTF-8?B?bm90IGVuY29kZWQ=?=.txt",
	}
	// Some of these no file system would take as a name, so the rows are
	// written as a draft taken over from another client would have them.
	data := []byte("the same bytes under every name")
	d, _ := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Names", Body: "x"},
		env.file(t, "seed.txt", data))
	hash := d.Attachments[0].ContentHash
	var list []Attachment
	for _, name := range names {
		list = append(list, Attachment{Filename: name, ContentType: "application/octet-stream", Size: int64(len(data)), ContentHash: hash})
	}
	d, err := env.store.SetDraftAttachments(ctx, d.ID, list, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out := d.outgoing()
	out.DraftID = d.ID
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	raw := env.smtp.received()[0]
	for _, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Errorf("a line of %d bytes: %.80s…", len(line), line)
		}
		lower := strings.ToLower(line)
		// Encoded names are folded to the 78 characters a line should
		// keep to; a plain name is one quoted string, which is not split.
		if (strings.Contains(lower, "*=") || strings.Contains(line, "=?UTF-8?B?")) && len(line) > 78 {
			t.Errorf("an encoded name on a line of %d bytes: %.80s…", len(line), line)
		}
	}
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != len(names) {
		t.Fatalf("attachments = %d, want %d", len(m.Attachments), len(names))
	}
	parts := sentParts(t, raw)[1:]
	if len(parts) != len(names) {
		t.Fatalf("parts = %d, want %d", len(parts), len(names))
	}
	for i, name := range names {
		// The app's parser reads encoded words in a decoded name too, as
		// lenient readers do for the clients that write them there. A
		// name that is one to begin with it reads decoded: no sender
		// can prevent that, and the strict reader below gets it right.
		if got := m.Attachments[i].Filename; got != name && !strings.Contains(name, "=?") {
			t.Errorf("the app's parser read %q, want %q", got, name)
		}
		if !bytes.Equal(m.Attachments[i].Data, data) || !bytes.Equal(parts[i].body, data) {
			t.Errorf("%q: contents differ", name)
		}
		fromDisposition, fromType := parts[i].names(t)
		if fromDisposition != name {
			t.Errorf("Content-Disposition filename read %q, want %q\n%s", fromDisposition, name, parts[i].header.Get("Content-Disposition"))
		}
		if fromType != name {
			t.Errorf("Content-Type name read %q, want %q\n%s", fromType, name, parts[i].header.Get("Content-Type"))
		}
	}
}

// What is written for a name, exactly: a quoted string where that is
// safe, else RFC 2231 in the disposition and RFC 2047 words in the type.
func TestAttachmentHeader(t *testing.T) {
	for _, tc := range []struct {
		name, kind         string
		wantType, wantDisp string
	}{
		{"report.pdf", "application/pdf",
			"application/pdf;\r\n name=\"report.pdf\"",
			"attachment;\r\n filename=\"report.pdf\""},
		{"my notes.txt", "text/plain; charset=utf-8",
			"text/plain;\r\n name=\"my notes.txt\"",
			"attachment;\r\n filename=\"my notes.txt\""},
		{"季度计划.pdf", "application/pdf",
			"application/pdf;\r\n name=\"=?UTF-8?B?5a2j5bqm6K6h5YiSLnBkZg==?=\"",
			"attachment;\r\n filename*=UTF-8''%E5%AD%A3%E5%BA%A6%E8%AE%A1%E5%88%92.pdf"},
		{`a "b";c.txt`, "",
			"application/octet-stream;\r\n name=\"=?UTF-8?B?YSAiYiI7Yy50eHQ=?=\"",
			"attachment;\r\n filename*=UTF-8''a%20%22b%22%3Bc.txt"},
		{"line\r\nbreak\x00.txt", "not a type",
			"application/octet-stream;\r\n name=\"linebreak.txt\"",
			"attachment;\r\n filename=\"linebreak.txt\""},
		{"   ", "message/rfc822",
			"application/octet-stream;\r\n name=\"attachment\"",
			"attachment;\r\n filename=\"attachment\""},
	} {
		gotType, gotDisp := attachmentHeader(tc.name, tc.kind)
		if gotType != tc.wantType || gotDisp != tc.wantDisp {
			t.Errorf("attachmentHeader(%q, %q) =\n%q\n%q\nwant\n%q\n%q", tc.name, tc.kind, gotType, gotDisp, tc.wantType, tc.wantDisp)
		}
	}
	// A long name is continued, no line splitting a character.
	_, disp := attachmentHeader(strings.Repeat("长", 30)+".txt", "text/plain")
	lines := strings.Split(disp, "\r\n")
	if len(lines) < 5 || lines[1] != " filename*0*=UTF-8''"+strings.Repeat("%E9%95%BF", 6)+";" ||
		!strings.HasPrefix(lines[2], " filename*1*=%E9%95%BF") {
		t.Errorf("continued name = %q", lines)
	}
	for _, line := range lines {
		if len(line) > 76 || strings.Count(line, "%")%3 != 0 {
			t.Errorf("line %q", line)
		}
	}
}

// The type an attachment is sent as: by its name's extension, else by its
// first bytes, else as bytes of no known type.
func TestDetectContentType(t *testing.T) {
	for _, tc := range []struct {
		name string
		head []byte
		want string
	}{
		{"report.pdf", []byte("anything"), "application/pdf"},
		{"PHOTO.PNG", nil, "image/png"},
		{"page.html", []byte("plain text really"), "text/html"},
		{"no-extension", []byte("%PDF-1.7\n"), "application/pdf"},
		{"picture.unknownext", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "image/png"},
		{"readme", []byte("Just some words.\n"), "text/plain"},
		{"blob.unknownext", []byte{0, 1, 2, 3, 0xff, 0xfe}, "application/octet-stream"},
		{"empty.unknownext", nil, "application/octet-stream"},
	} {
		if got := detectContentType(tc.name, tc.head); got != tc.want {
			t.Errorf("detectContentType(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

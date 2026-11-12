package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// mailFile is a file of a message the tests forward.
type mailFile struct {
	name, contentType string
	data              []byte
}

// messageWithFiles is a received message with a text and files.
func messageWithFiles(id string, files ...mailFile) []byte {
	var b strings.Builder
	b.WriteString("From: Bob <bob@example.com>\r\nTo: Ann <ann@example.com>\r\nSubject: Files\r\n")
	b.WriteString("Message-ID: <" + id + ">\r\nDate: Mon, 02 Mar 2026 09:00:00 +0000\r\nMIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n")
	b.WriteString("--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee attached.\r\n")
	for _, f := range files {
		contentType, disposition := attachmentHeader(f.name, f.contentType)
		b.WriteString("--b1\r\nContent-Type: " + contentType + "\r\nContent-Disposition: " + disposition +
			"\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		encoded := base64.StdEncoding.EncodeToString(f.data)
		for len(encoded) > 76 {
			b.WriteString(encoded[:76] + "\r\n")
			encoded = encoded[76:]
		}
		b.WriteString(encoded + "\r\n")
	}
	b.WriteString("--b1--\r\n")
	return []byte(b.String())
}

// forwardEnv is an attachment test's environment whose service keeps an
// attachment cache, with a message of three files in the inbox, synced.
func forwardEnv(t *testing.T) (*attachEnv, []mailFile, UID) {
	t.Helper()
	env := newAttachEnv(t, nil)
	env.svc = NewService(env.store, env.secrets, t.TempDir())
	env.svc.SetTLSConfig(insecureTLS())
	env.svc.SetDataDir(env.dataDir)
	// The largest is in the middle, for the test of the room a draft has.
	files := []mailFile{
		{"photo.png", "image/png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9, 8, 7}, 700)...)},
		{"report.pdf", "application/pdf", append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0, 1, 2, 250, 13, 10}, 900)...)},
		{"数据 2026.csv", "text/csv", []byte("a,b\n1,2\n")},
	}
	env.imap.append(t, "INBOX", messageWithFiles("files-1@example.com", files...))
	if err := env.svc.SyncFolder(context.Background(), env.a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	return env, files, 1
}

// sameAsFiles reports whether a draft's attachments are the files, in
// order, and their bytes are in the draft file store.
func sameAsFiles(t *testing.T, env *attachEnv, got []Attachment, files ...mailFile) bool {
	t.Helper()
	if len(got) != len(files) {
		return false
	}
	for i, f := range files {
		a := got[i]
		data, err := os.ReadFile(filepath.Join(env.dataDir, draftFilesFolder, a.ContentHash))
		if err != nil || a.Filename != f.name || a.ContentType != f.contentType || a.Size != int64(len(f.data)) ||
			a.ContentHash != hashOf(f.data) || !bytes.Equal(data, f.data) {
			return false
		}
	}
	return true
}

// A forward carries the attachments of the message it forwards: copied
// into the draft, in their order, from the attachment cache where it has
// them, which needs no connection.
func TestAttachFromMessageCopiesFromTheCache(t *testing.T) {
	ctx := context.Background()
	env, files, uid := forwardEnv(t)
	if _, err := env.svc.FetchMessage(ctx, env.a.ID, "INBOX", uid, false); err != nil {
		t.Fatal(err)
	}
	env.imap.stop()
	d, failures, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID, Subject: "Fwd: Files"}, env.a.ID, "INBOX", uid)
	if err != nil || len(failures) != 0 {
		t.Fatalf("attach = %v, failures %+v", err, failures)
	}
	if d.ID == "" || d.Subject != "Fwd: Files" || !sameAsFiles(t, env, d.Attachments, files...) {
		t.Fatalf("draft = %+v", d)
	}
	if stored := env.stored(t); len(stored) != 3 {
		t.Errorf("files stored = %v", stored)
	}
	// Again: the same files under the same names are attached once.
	again, failures, err := env.svc.AttachFromMessage(ctx, d, env.a.ID, "INBOX", uid)
	if err != nil || len(failures) != 0 || !sameAsFiles(t, env, again.Attachments, files...) {
		t.Errorf("attached twice: %+v, %+v, %v", again.Attachments, failures, err)
	}
	// They are the draft's now, kept through a sweep, and sent with it.
	if err := env.svc.SweepDraftFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if !sameAsFiles(t, env, mustDraft(t, env.svc, d.ID).Attachments, files...) {
		t.Error("the files did not outlast a sweep")
	}
}

// What the cache lacks the server has: a message whose body was never
// fetched, a message the cache cut, or a cached file that is damaged.
// The server is read without marking the message read.
func TestAttachFromMessageStreamsWhatTheCacheLacks(t *testing.T) {
	ctx := context.Background()
	t.Run("never fetched", func(t *testing.T) {
		env, files, uid := forwardEnv(t)
		d, failures, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", uid)
		if err != nil || len(failures) != 0 || !sameAsFiles(t, env, d.Attachments, files...) {
			t.Fatalf("attached %+v, failures %+v, %v", d.Attachments, failures, err)
		}
		for _, m := range env.imap.messages(t, "INBOX") {
			if m.hasFlag(imap.FlagSeen) {
				t.Error("forwarding marked the message read")
			}
		}
	})
	t.Run("cut by the cache", func(t *testing.T) {
		env, files, uid := forwardEnv(t)
		if _, err := env.svc.FetchMessage(ctx, env.a.ID, "INBOX", uid, false); err != nil {
			t.Fatal(err)
		}
		m, _, _ := env.store.GetMessage(ctx, env.a.ID, "INBOX", uid)
		m.BodyTruncated, m.Attachments = true, m.Attachments[:1]
		if err := env.store.SaveMessage(ctx, env.a.ID, "INBOX", m); err != nil {
			t.Fatal(err)
		}
		d, failures, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", uid)
		if err != nil || len(failures) != 0 || !sameAsFiles(t, env, d.Attachments, files...) {
			t.Fatalf("attached %+v, failures %+v, %v", d.Attachments, failures, err)
		}
	})
	t.Run("a damaged file in the cache", func(t *testing.T) {
		env, files, uid := forwardEnv(t)
		fetched, err := env.svc.FetchMessage(ctx, env.a.ID, "INBOX", uid, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fetched.Message.Attachments[1].CachePath, []byte("not the file"), 0o600); err != nil {
			t.Fatal(err)
		}
		d, failures, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", uid)
		if err != nil || len(failures) != 0 || !sameAsFiles(t, env, d.Attachments, files...) {
			t.Fatalf("attached %+v, failures %+v, %v", d.Attachments, failures, err)
		}
	})
}

// One attachment that cannot be copied is reported, with why, and the
// others are attached all the same: one past the room the draft has left,
// and one the cache damaged while the server cannot be reached.
func TestAttachFromMessageReportsEachFailure(t *testing.T) {
	ctx := context.Background()
	t.Run("too large", func(t *testing.T) {
		env, files, uid := forwardEnv(t)
		// The draft has room for the first and the last file, and not for
		// all three.
		d := mustSaveDraft(t, env.svc, Draft{AccountID: env.a.ID, Subject: "Fwd: Files"})
		room := int64(len(files[0].data)+len(files[2].data)) + 10
		big := Attachment{Filename: "big.bin", ContentType: "application/octet-stream", Size: MaxAttachmentBytes - room, ContentHash: strings.Repeat("a", 64)}
		if _, err := env.store.SetDraftAttachments(ctx, d.ID, []Attachment{big}, d.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		got, failures, err := env.svc.AttachFromMessage(ctx, d, env.a.ID, "INBOX", uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 || failures[0].Attachment.Filename != files[1].name || !errors.Is(failures[0].Err, ErrAttachTooLarge) {
			t.Errorf("failures = %+v", failures)
		}
		if len(got.Attachments) != 3 || !sameFiles(got.Attachments[:1], big) || !sameAsFiles(t, env, got.Attachments[1:], files[0], files[2]) {
			t.Errorf("attachments = %+v", got.Attachments)
		}
		if got.AttachmentBytes() > MaxAttachmentBytes {
			t.Errorf("the draft carries %d bytes", got.AttachmentBytes())
		}
	})
	t.Run("damaged and offline", func(t *testing.T) {
		env, files, uid := forwardEnv(t)
		fetched, err := env.svc.FetchMessage(ctx, env.a.ID, "INBOX", uid, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fetched.Message.Attachments[0].CachePath, []byte("damaged"), 0o600); err != nil {
			t.Fatal(err)
		}
		env.imap.stop()
		d, failures, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 || failures[0].Attachment.Filename != files[0].name || failures[0].Err == nil {
			t.Errorf("failures = %+v", failures)
		}
		if !sameAsFiles(t, env, d.Attachments, files[1], files[2]) {
			t.Errorf("attachments = %+v", d.Attachments)
		}
		// No copy is left behind under a temporary name.
		for _, name := range env.stored(t) {
			if strings.HasPrefix(name, incomingPrefix) {
				t.Errorf("a temporary copy stayed: %s", name)
			}
		}
	})
	t.Run("nothing to copy from", func(t *testing.T) {
		env, _, uid := forwardEnv(t)
		// A draft discarded while the server cannot be reached stays,
		// marked, until its copy there is removed.
		d := mustSaveDraft(t, env.svc, Draft{AccountID: env.a.ID, Subject: "Gone"})
		mustSyncDrafts(t, env.svc, env.a.ID)
		env.imap.stop()
		if err := env.svc.DiscardDraft(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.svc.AttachFromMessage(ctx, d, env.a.ID, "INBOX", uid); !errors.Is(err, ErrDraftDiscarded) {
			t.Errorf("into a discarded draft = %v", err)
		}
		if _, _, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", uid); err == nil {
			t.Error("a message neither cached nor on a server that answers was attached")
		}
		if _, _, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: env.a.ID}, env.a.ID, "INBOX", 99); !errors.Is(err, ErrNotFound) {
			t.Errorf("a message not in the cache = %v", err)
		}
		if _, _, err := env.svc.AttachFromMessage(ctx, Draft{AccountID: "other"}, env.a.ID, "INBOX", uid); !errors.Is(err, ErrNotFound) {
			t.Errorf("into another account's draft = %v", err)
		}
		if drafts, _ := env.store.Drafts(ctx, env.a.ID); slices.ContainsFunc(drafts, func(d Draft) bool { return len(d.Attachments) > 0 }) {
			t.Errorf("drafts = %+v", drafts)
		}
		if stored := env.stored(t); len(stored) != 0 {
			t.Errorf("files stored = %v", stored)
		}
	})
}

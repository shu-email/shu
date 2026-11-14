package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// loadHTMLFirst gives the first message an HTML body.
func loadHTMLFirst(t *testing.T, fs *fakeService, a *app, html string, attachments []mail.Attachment) mail.Summary {
	t.Helper()
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Body", BodyHTML: html, BodyCached: true, Attachments: attachments,
	}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	return m
}

// Remote pictures start blocked; Load images shows them for the message,
// Always for this sender persists the choice, and Block images takes it
// back.
func TestRemoteImagesStayBlockedUntilAsked(t *testing.T) {
	a, fs := sampleMessageApp(t)
	m := loadHTMLFirst(t, fs, a, `<p>Hello <img src="https://example.com/a.png"></p>`, nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.ImagesBlocked()) {
		t.Fatalf("no blocked notice: %q", tt.Texts())
	}
	if a.imageAllowed(m) {
		t.Fatal("remote images are allowed by default")
	}
	click(t, tt, locales.LoadImages())
	if !a.imageAllowed(m) {
		t.Errorf("the message is not allowed after Load images: %+v", a.imagesShown)
	}
	if !tt.HasText(locales.ImagesShownDetail()) {
		t.Errorf("no shown notice: %q", tt.Texts())
	}
	click(t, tt, locales.BlockImages())
	if a.imageAllowed(m) {
		t.Error("the message stayed allowed after Block images")
	}
	if !tt.HasText(locales.ImagesBlocked()) {
		t.Errorf("the blocked notice did not return: %q", tt.Texts())
	}

	// Always for this sender is kept by the engine, by address without
	// case.
	click(t, tt, locales.LoadImagesAlways())
	if !fs.remote["maya@example.com"] {
		t.Errorf("policy = %+v", fs.remote)
	}
	if !a.imageAllowed(m) {
		t.Error("the sender's images are not allowed")
	}
	click(t, tt, locales.BlockImages())
	if fs.remote["maya@example.com"] {
		t.Errorf("policy after blocking = %+v", fs.remote)
	}
}

func TestRemoteImagesPolicyFailureIsSaid(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadHTMLFirst(t, fs, a, `<img src="https://example.com/a.png">`, nil)
	fs.remoteErr = errors.New("the database is locked")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.LoadImagesAlways())
	if a.err == "" {
		t.Error("the failure was not said")
	}
}

// A text attachment previews in the app, and Save all writes each file
// with a name of its own.
func TestAttachmentPreviewAndSaveAll(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "notes.txt", ContentType: "text/plain", Size: 13}
	m := loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	fs.attachmentBytes = []byte("hello preview")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("notes.txt"))
	if !a.preview.open || a.preview.kind != "text" || a.preview.text != "hello preview" {
		t.Fatalf("preview = %+v", a.preview)
	}
	if !tt.HasText("hello preview") {
		t.Errorf("the preview does not show the text: %q", tt.Texts())
	}
	click(t, tt, locales.Done())
	if a.preview.open {
		t.Error("the preview stayed open")
	}

	dir := t.TempDir()
	a.pickFolder = func(*mygo.Window) (string, error) { return dir, nil }
	list := []mail.Attachment{att, att}
	a.saveAllAttachments(m, list)
	for _, name := range []string{"notes.txt", "notes (2).txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if a.folderNote != locales.SaveAllDone(2) {
		t.Errorf("note = %q", a.folderNote)
	}
}

// An image attachment decodes into the preview.
func TestImageAttachmentPreview(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "dot.png", ContentType: "image/png"}
	loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	fs.attachmentBytes = png
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("dot.png"))
	if !a.preview.open || a.preview.kind != "image" || a.preview.image == nil {
		t.Fatalf("preview = %+v", a.preview)
	}
}

func TestSaveAllWithoutFolderSaysSo(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "a.txt", ContentType: "text/plain"}
	m := loadHTMLFirst(t, fs, a, `<p>x</p>`, []mail.Attachment{att, att})
	fs.attachment = att
	a.pickFolder = func(*mygo.Window) (string, error) { return "", nil }
	a.saveAllAttachments(m, []mail.Attachment{att, att})
	if a.err != locales.SaveAllNoFolder() {
		t.Errorf("error = %q", a.err)
	}
}

// Opening a preview writes the file Open in the default app opens once,
// before the dialog draws, whatever the number of draws.
func TestAttachmentPreviewWritesItsFileOnce(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "notes.txt", ContentType: "text/plain"}
	loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	fs.attachmentBytes = []byte("hello preview")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("notes.txt"))
	tt.Frame()
	tt.Frame()
	if fs.tempFiles != 1 {
		t.Errorf("the preview wrote its file %d times, want once", fs.tempFiles)
	}
	if string(fs.tempData) != "hello preview" {
		t.Errorf("the file holds %q", fs.tempData)
	}
	path := a.preview.path
	if path == "" {
		t.Fatal("the preview has no file to open")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file is not there: %v", err)
	}
	if !tt.HasText("hello preview") {
		t.Errorf("the preview does not show the text: %q", tt.Texts())
	}
}

// A preview the modal dismissed, by Escape or a click outside, goes with
// its file as one closed from the dialog does.
func TestAttachmentPreviewDismissedRemovesItsFile(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "notes.txt", ContentType: "text/plain"}
	loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	fs.attachmentBytes = []byte("bye")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("notes.txt"))
	path := a.preview.path
	if path == "" {
		t.Fatal("the preview has no file to open")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.preview.open {
		t.Error("the preview stayed open")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file stayed: %v", err)
	}
	// It opens again once dismissed, with a file of its own.
	click(t, tt, locales.PreviewAttachment("notes.txt"))
	if !a.preview.open || a.preview.path == "" || a.preview.path == path {
		t.Errorf("the preview opened again as %+v", a.preview)
	}
	t.Cleanup(func() { _ = os.Remove(a.preview.path) })
}

// A file that cannot be written is said, and the preview still shows
// the message without it: there is nothing to open.
func TestAttachmentPreviewSaysWhenItsFileCannotBeWritten(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "notes.txt", ContentType: "text/plain"}
	loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	fs.attachmentBytes = []byte("hello preview")
	fs.tempFileErr = errors.New("the disk is full")
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("notes.txt"))
	want := locales.OpenAttachmentFailed("the disk is full")
	if a.err != want {
		t.Errorf("error = %q, want %q", a.err, want)
	}
	if a.preview.path != "" {
		t.Errorf("the preview names a file: %q", a.preview.path)
	}
	if !tt.HasText("hello preview") {
		t.Errorf("the preview shows nothing: %q", tt.Texts())
	}
}

// An empty attachment has nothing to open, and writes no file for it.
func TestAttachmentPreviewOfAnEmptyFileWritesNone(t *testing.T) {
	a, fs := sampleMessageApp(t)
	att := mail.Attachment{Part: "2", Filename: "empty.txt", ContentType: "text/plain"}
	loadHTMLFirst(t, fs, a, `<p>See attached.</p>`, []mail.Attachment{att})
	fs.attachment = att
	// The fake gives "data" to an attachment it has no bytes for.
	fs.attachmentBytes = []byte{}
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.PreviewAttachment("empty.txt"))
	if fs.tempFiles != 0 {
		t.Errorf("an empty attachment wrote %d files", fs.tempFiles)
	}
	if a.preview.path != "" || a.preview.kind != "text" {
		t.Errorf("preview = %+v", a.preview)
	}
}

// A preview the user has left behind by the time its bytes come writes
// no file that stays: the message it was for is no longer the one shown.
func TestAttachmentPreviewTheUserLeftRemovesItsFile(t *testing.T) {
	a, fs := sampleMessageApp(t)
	m := a.all[0]
	att := mail.Attachment{Part: "2", Filename: "notes.txt", ContentType: "text/plain"}
	fs.attachment = att
	fs.attachmentBytes = []byte("late")
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	a.previewAttachment(m, att)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want the one that reads the attachment", len(jobs))
	}
	// The user is on another message by the time it ends.
	a.gen++
	jobs[0]()
	if a.preview.open {
		t.Error("a preview the user left opened")
	}
	if fs.tempFiles != 1 || len(fs.tempPaths) != 1 {
		t.Fatalf("files written = %d, %q", fs.tempFiles, fs.tempPaths)
	}
	if _, err := os.Stat(fs.tempPaths[0]); !os.IsNotExist(err) {
		t.Errorf("the file stayed: %v", err)
	}
}

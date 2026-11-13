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

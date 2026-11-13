package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	smtp "github.com/emersion/go-smtp"

	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// deliveredFiles reads the attachments of a message as a recipient's
// mail client would: each name with its bytes.
func deliveredFiles(t *testing.T, raw string) map[string][]byte {
	t.Helper()
	m, err := mail.ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, att := range m.Attachments {
		out[att.Filename] = att.Data
	}
	return out
}

// storedDraftFiles lists the files the engine keeps for drafts under a
// data directory.
func storedDraftFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "draft-files"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The acceptance criteria end to end: the window, the real engine, a
// database on disk and local IMAP and SMTP servers. Files are attached
// to a draft, which is closed, uploaded, and opened again after the app
// restarts; one is removed; the server refuses the message and then
// takes it; and what it was given is the files still attached, by their
// names and to the byte.
func TestAttachmentsEndToEnd(t *testing.T) {
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
	// The data directory holds the database and, beside it, the files of
	// the drafts; the cache is elsewhere, as on a user's disk.
	dataDir, cacheDir, keys := t.TempDir(), t.TempDir(), memKeychain{}
	launch := func() (*app, *store.Store) {
		t.Helper()
		db, err := store.Open(ctx, filepath.Join(dataDir, "shu.db"))
		if err != nil {
			t.Fatal(err)
		}
		svc := mail.NewService(db, keys, cacheDir)
		svc.SetDataDir(dataDir)
		svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
		if accounts, _, err := svc.Accounts(ctx); err != nil {
			t.Fatal(err)
		} else if len(accounts) == 0 {
			if err := svc.AddAccount(ctx, account, "secret"); err != nil {
				t.Fatal(err)
			}
		}
		sendAtOnce(t, svc)
		a := newTestApp(svc)
		a.setLocale = func(string) {}
		a.start()
		if a.err != "" {
			t.Fatalf("start: %s", a.err)
		}
		return a, db
	}

	// The user's files: one with a name in Chinese, one a PDF by its
	// bytes, one to add later.
	files := t.TempDir()
	// Large enough that the database, which grows with the schema, stays
	// clearly smaller than the attachment it must not hold.
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0, 1, 2, 3, 250, 251, 252, 253}, 80<<10)...)
	plan := []byte("第一季度计划：按时发布。\r\nline two\n")
	notes := []byte("notes, added after the restart")
	const planName, pdfName, notesName = "季度计划 第一版.txt", "report.pdf", "notes; v2.txt"
	pdfPath := userFile(t, files, pdfName, pdf)
	planPath := userFile(t, files, planName, plan)
	notesPath := userFile(t, files, notesName, notes)

	first, db := launch()
	cs := first.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	typeIn(t, tt, locales.Body(), "Both files attached.")
	if !tt.DropFiles(360, 300, pdfPath, planPath) {
		t.Fatal("the window did not take the files")
	}
	for _, text := range []string{pdfName, planName, "application/pdf · " + formatSize(int64(len(pdf))),
		locales.AttachmentsTotal(2, formatSize(int64(len(pdf)+len(plan))))} {
		if !tt.HasText(text) {
			t.Fatalf("the window lacks %q: %q", text, tt.Texts())
		}
	}
	if stored := storedDraftFiles(t, dataDir); len(stored) != 2 {
		t.Fatalf("files kept for the draft = %v", stored)
	}
	// The files are copies: the draft does not depend on the originals.
	if err := os.Remove(pdfPath); err != nil {
		t.Fatal(err)
	}

	// Closed: the server's copy of the draft carries both files.
	cs.close()
	onServer := imapServer.messages(t, "Drafts")
	if len(onServer) != 1 || !onServer[0].Draft {
		t.Fatalf("the server's Drafts folder = %+v", onServer)
	}
	if got := deliveredFiles(t, onServer[0].Raw); len(got) != 2 || !bytes.Equal(got[pdfName], pdf) || !bytes.Equal(got[planName], plan) {
		t.Fatalf("the server's copy of the draft has %d attachments", len(got))
	}

	// The app quits and starts again: the draft is in Drafts with its
	// attachments, and opens with them.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, db := launch()
	defer db.Close()
	main := ui.NewTester(second.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(second.all) != 1 || second.all[0].DraftID != cs.draftID || !second.all[0].HasAttachments {
		t.Fatalf("drafts after the restart = %+v", second.all)
	}
	for _, text := range []string{pdfName, planName} {
		if !main.HasText(text) {
			t.Errorf("the reading pane lacks %q after the restart: %q", text, main.Texts())
		}
	}
	if stored := storedDraftFiles(t, dataDir); len(stored) != 2 {
		t.Fatalf("files after the restart = %v", stored)
	}
	click(t, main, locales.EditDraft())
	open := openComposes(second)
	if len(open) != 1 || attachedNames(open[0].attachments) != pdfName+", "+planName {
		t.Fatalf("the reopened window = %+v", open)
	}
	edit := open[0]
	et := ui.NewTester(edit.view, 720, 620)
	for _, text := range []string{pdfName, planName} {
		if !et.HasText(text) {
			t.Errorf("the reopened window lacks %q: %q", text, et.Texts())
		}
	}

	// One file is removed and another added.
	click(t, et, locales.RemoveAttachment(pdfName))
	if !et.DropFiles(360, 300, notesPath) {
		t.Fatal("the reopened window did not take a file")
	}
	if got := attachedNames(edit.attachments); got != planName+", "+notesName {
		t.Fatalf("attachments after the change = %s", got)
	}

	// The server refuses the message for its size: the window quotes it
	// and keeps everything.
	smtpServer.setRefuse(&smtp.SMTPError{
		Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Message size exceeds fixed limit",
	})
	click(t, et, locales.Send())
	if !et.HasText(locales.SendRejectedSize("5.3.4 Message size exceeds fixed limit")) {
		t.Fatalf("no word of the refusal: %q", et.Texts())
	}
	if edit.closed || edit.sending || len(smtpServer.received()) != 0 {
		t.Fatalf("after the refusal: closed %v, sending %v", edit.closed, edit.sending)
	}
	d, ok, err := second.svc.Draft(ctx, cs.draftID)
	if err != nil || !ok || attachedNames(d.Attachments) != planName+", "+notesName || d.Body != "Both files attached." {
		t.Fatalf("draft after the refusal = %+v, %v, %v", d, ok, err)
	}
	if !et.HasText(planName) || !et.HasText(notesName) {
		t.Errorf("the attachments left the window: %q", et.Texts())
	}
	// The window still takes edits.
	typeIn(t, et, locales.Body(), " Smaller now.")

	// The server takes it: what it was given is the two files attached
	// now, under their names and to the byte, and not the one removed.
	smtpServer.setRefuse(nil)
	click(t, et, locales.Send())
	if !edit.closed {
		t.Fatalf("the window stays open after the send: %q", et.Texts())
	}
	got := smtpServer.received()
	if len(got) != 1 {
		t.Fatalf("the server was given %d messages", len(got))
	}
	delivered := deliveredFiles(t, got[0])
	names := make([]string, 0, len(delivered))
	for name := range delivered {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{planName, notesName}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("delivered attachments = %q, want %q", names, want)
	}
	if !bytes.Equal(delivered[planName], plan) || !bytes.Equal(delivered[notesName], notes) {
		t.Error("the delivered attachments differ from the files")
	}
	if m, err := mail.ParseMessage([]byte(got[0])); err != nil || m.BodyText == "" || m.Subject != "Quarterly plan" {
		t.Errorf("the delivered message = %+v, %v", m.Summary, err)
	}

	// Sent, the draft is gone, here and on the server, and with it the
	// files nothing needs any more.
	if _, ok, _ := second.svc.Draft(ctx, cs.draftID); ok {
		t.Error("the draft remains after the send")
	}
	if left := imapServer.messages(t, "Drafts"); len(left) != 0 {
		t.Errorf("the server's Drafts folder still has %d messages", len(left))
	}
	if stored := storedDraftFiles(t, dataDir); len(stored) != 0 {
		t.Errorf("files left after the send: %v", stored)
	}
	// The copy in Sent has the attachments too.
	sent := imapServer.messages(t, "Sent")
	if len(sent) != 1 {
		t.Fatalf("the Sent folder has %d messages", len(sent))
	}
	if copyOf := deliveredFiles(t, sent[0].Raw); len(copyOf) != 2 || !bytes.Equal(copyOf[planName], plan) {
		t.Errorf("the copy in Sent has %d attachments", len(copyOf))
	}
	// Nothing about the attachments went into the database but their
	// names and sizes: the file is far smaller than what was attached.
	if info, err := os.Stat(filepath.Join(dataDir, "shu.db")); err != nil || info.Size() > int64(len(pdf)) {
		t.Errorf("the database is %d bytes after attaching %d: %v", info.Size(), len(pdf), err)
	}
}

package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestReadMbox(t *testing.T) {
	mbox := strings.Join([]string{
		"From alice@example.com Mon Jan  2 15:04:05 2006",
		"From: Alice <alice@example.com>",
		"Subject: One",
		"",
		"Hello",
		">From the other side",
		"From alice@example.com Tue Jan  3 15:04:05 2006",
		"From: Alice <alice@example.com>",
		"Subject: Two",
		"",
		"Goodbye",
		"",
	}, "\n")
	var got []string
	if err := readMbox(strings.NewReader(mbox), func(raw []byte) error {
		m, err := ParseMessage(raw)
		if err != nil {
			return err
		}
		got = append(got, m.Subject)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "One" || got[1] != "Two" {
		t.Errorf("subjects = %q", got)
	}
}

// A message imported into a folder is on the server, unread, and the
// result counts it.
func TestImportMboxAppendsToTheFolder(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Imported")
	svc, _, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	mbox := "From a@example.com Mon Jan  2 15:04:05 2006\nFrom: Alice <alice@example.com>\nSubject: Imported one\n\nHello\n" +
		"From a@example.com Mon Jan  2 15:04:05 2006\nFrom: Alice <alice@example.com>\nSubject: Imported two\n\nBye\n"
	var progress []int
	result, err := svc.ImportMbox(ctx, a.ID, "Imported", strings.NewReader(mbox), func(r ImportResult) {
		progress = append(progress, r.Imported)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Skipped != 0 {
		t.Errorf("result = %+v", result)
	}
	if len(progress) != 2 || progress[1] != 2 {
		t.Errorf("progress = %v", progress)
	}
	subjects := f.subjects(t, "Imported")
	if len(subjects) != 2 || subjects[0] != "Imported one" || subjects[1] != "Imported two" {
		t.Errorf("subjects = %q", subjects)
	}
	status, err := f.user.Status("Imported", &imap.StatusOptions{NumUnseen: true})
	if err != nil || status.NumUnseen == nil || *status.NumUnseen != 2 {
		t.Errorf("unread = %+v, %v", status, err)
	}
	if _, err := svc.ImportMbox(ctx, a.ID, "Missing", strings.NewReader(mbox), nil); err != ErrNotFound {
		t.Errorf("missing folder: %v", err)
	}
}

// Exporting a folder writes an mbox another reader can parse, attachments
// included or left out.
func TestExportFolder(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	f.append(t, "Archive", readFixture(t, "plain.eml"))
	f.append(t, "Archive", readFixture(t, "attachment.eml"))
	svc, _, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Archive", 50); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := svc.Export(ctx, ExportRequest{AccountID: a.ID, Folder: "Archive", Format: FormatMbox, IncludeAttachments: true}, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 2 {
		t.Errorf("messages = %d", result.Messages)
	}
	var subjects []string
	if err := readMbox(bytes.NewReader(out.Bytes()), func(raw []byte) error {
		m, err := ParseMessage(raw)
		if err != nil {
			return err
		}
		subjects = append(subjects, m.Subject)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 {
		t.Errorf("exported subjects = %q", subjects)
	}

	out.Reset()
	if _, err := svc.Export(ctx, ExportRequest{AccountID: a.ID, Folder: "Archive", Format: FormatMbox}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("Content-Type: application/octet-stream")) {
		t.Error("an attachment was exported without them")
	}
}

func TestExportOneMessageAsEML(t *testing.T) {
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
	uids, err := store.FolderUIDs(ctx, a.ID, "INBOX")
	if err != nil || len(uids) != 1 {
		t.Fatalf("uids = %v, %v", uids, err)
	}
	var out bytes.Buffer
	if _, err := svc.Export(ctx, ExportRequest{AccountID: a.ID, Refs: []MessageRef{{Folder: "INBOX", UID: uids[0]}}, Format: FormatEML, IncludeAttachments: true}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMessage(out.Bytes()); err != nil {
		t.Errorf("the exported message does not parse: %v", err)
	}
}

// A backup holds the accounts and compose settings, and no secret.
func TestBackupConfigHoldsNoSecrets(t *testing.T) {
	svc, store, secrets := newTestService()
	ctx := context.Background()
	a := Account{ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit},
		SMTP: Server{Host: "smtp.example.com", Port: 465, TLS: TLSImplicit}}
	if err := svc.AddAccount(ctx, a, "super-secret-password"); err != nil {
		t.Fatal(err)
	}
	_ = store
	data, err := svc.BackupConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("super-secret-password")) {
		t.Errorf("the backup holds the password: %q", data)
	}
	var backup ConfigBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatal(err)
	}
	if len(backup.Accounts) != 1 || backup.Accounts[0].ID != "a1" || backup.Version != 1 {
		t.Errorf("backup = %+v", backup)
	}
	// A second machine restores the account, without its password.
	other, otherStore, _ := newTestService()
	added, err := other.RestoreConfig(ctx, data)
	if err != nil || added != 1 {
		t.Fatalf("added = %d, %v", added, err)
	}
	if _, ok, _ := otherStore.Account(ctx, "a1"); !ok {
		t.Error("the account was not restored")
	}
	if added, err := other.RestoreConfig(ctx, data); err != nil || added != 0 {
		t.Errorf("restoring twice added %d, %v", added, err)
	}
	if _, err := other.RestoreConfig(ctx, []byte(`{"version":9}`)); err == nil {
		t.Error("an unknown backup version was accepted")
	}
	_ = secrets
}

func TestWithoutAttachmentsKeepsTheText(t *testing.T) {
	raw := readFixture(t, "attachment.eml")
	stripped, err := withoutAttachments(raw)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseMessage(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("attachments stayed: %+v", m.Attachments)
	}
	if strings.TrimSpace(m.BodyText) == "" {
		t.Error("the text went")
	}
}

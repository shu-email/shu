package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
)

func transferApp(t *testing.T) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	a, fs := sampleMessageApp(t)
	a.openSettings(settingsTabGeneral)
	return a, fs, ui.NewTester(a.view, 1220, 1400)
}

func writeTempMbox(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mail.mbox")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportMboxFromSettings(t *testing.T) {
	a, fs, tt := transferApp(t)
	path := writeTempMbox(t, "From a@example.com Mon Jan  2 15:04:05 2006\nFrom: Alice <alice@example.com>\nSubject: One\n\nHello\n"+
		"From a@example.com Mon Jan  2 15:04:05 2006\nFrom: Alice <alice@example.com>\nSubject: Two\n\nBye\n")
	a.pickFiles = func(*mygo.Window) ([]string, error) { return []string{path}, nil }
	click(t, tt, locales.ImportMbox())
	if len(fs.imported) != 1 || !strings.HasPrefix(fs.imported[0], "a1/INBOX/2") {
		t.Fatalf("imported = %v", fs.imported)
	}
	if a.transfer.note != locales.TransferImported(2) || a.transfer.err != "" {
		t.Errorf("note = %q, error = %q", a.transfer.note, a.transfer.err)
	}
	if a.transfer.busy {
		t.Error("the transfer stayed busy")
	}
}

func TestImportNeedsAFolder(t *testing.T) {
	a, _, tt := transferApp(t)
	a.folder = "starred"
	click(t, tt, locales.ImportMbox())
	if a.transfer.err != locales.TransferNoFolder() {
		t.Errorf("error = %q", a.transfer.err)
	}
}

func TestExportFolderFromSettings(t *testing.T) {
	a, fs, tt := transferApp(t)
	out := filepath.Join(t.TempDir(), "mail.mbox")
	a.saveFile = func(*mygo.Window, string) (string, error) { return out, nil }
	click(t, tt, locales.ExportFolder())
	if len(fs.exported) != 1 || !strings.HasPrefix(fs.exported[0], "a1/INBOX/mbox/true") {
		t.Fatalf("exported = %v", fs.exported)
	}
	if data, err := os.ReadFile(out); err != nil || !strings.Contains(string(data), "Subject: Exported") {
		t.Errorf("file = %q, %v", data, err)
	}
	if a.transfer.note != locales.TransferExported(1) {
		t.Errorf("note = %q", a.transfer.note)
	}
}

func TestExportFailureIsShown(t *testing.T) {
	a, fs, tt := transferApp(t)
	fs.transferErr = os.ErrPermission
	a.saveFile = func(*mygo.Window, string) (string, error) { return filepath.Join(t.TempDir(), "mail.mbox"), nil }
	click(t, tt, locales.ExportAccount())
	if a.transfer.err == "" || a.transfer.busy {
		t.Errorf("state = %+v", a.transfer)
	}
}

func TestBackupAndRestoreSettings(t *testing.T) {
	a, fs, tt := transferApp(t)
	backup := filepath.Join(t.TempDir(), "shu-settings.json")
	a.saveFile = func(*mygo.Window, string) (string, error) { return backup, nil }
	click(t, tt, locales.BackupConfig())
	data, err := os.ReadFile(backup)
	if err != nil || !strings.Contains(string(data), `"version":1`) {
		t.Fatalf("backup = %q, %v", data, err)
	}
	if a.transfer.note != locales.BackupDone() {
		t.Errorf("note = %q", a.transfer.note)
	}
	a.pickFiles = func(*mygo.Window) ([]string, error) { return []string{backup}, nil }
	click(t, tt, locales.RestoreConfig())
	if len(fs.restored) != 1 || !strings.Contains(fs.restored[0], `"version":1`) {
		t.Fatalf("restored = %v", fs.restored)
	}
	if !strings.Contains(a.transfer.note, locales.RestoreNeedsSignIn()) {
		t.Errorf("note = %q", a.transfer.note)
	}
}

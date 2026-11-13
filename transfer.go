package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Import, export and the settings backup, from the General tab of the
// settings. A transfer runs in a job with a cancel action; the window
// shows how far it got and what it did.

// transferState is the transfer running, or the last one's result.
type transferState struct {
	busy   bool
	label  string
	done   int
	note   string
	err    string
	cancel context.CancelFunc
}

// transferSettingsView draws the import and export section.
func (a *app) transferSettingsView(c *ui.Context, t *ui.Theme) {
	ui.Divider(c)
	ui.Text(c, locales.TransferTitle()).FontSize(12).Bold().TextColor(t.TextMuted)
	ui.Text(c, locales.TransferIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(4)

	ui.Row(c).Wrap().Gap(8).Children(func() {
		ui.Button(c, locales.ImportMbox()).Disabled(a.transfer.busy).OnClick(func() { a.startImport(false) })
		ui.Button(c, locales.ImportEML()).Disabled(a.transfer.busy).OnClick(func() { a.startImport(true) })
		ui.Button(c, locales.ExportFolder()).Disabled(a.transfer.busy).OnClick(func() { a.startExport(false) })
		ui.Button(c, locales.ExportAccount()).Disabled(a.transfer.busy).OnClick(func() { a.startExport(true) })
	})
	ui.Row(c).Wrap().Gap(8).Children(func() {
		ui.Button(c, locales.BackupConfig()).Disabled(a.transfer.busy).OnClick(a.backupSettings)
		ui.Button(c, locales.RestoreConfig()).Disabled(a.transfer.busy).OnClick(a.restoreSettings)
	})

	if a.transfer.busy {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, a.transfer.label).FontSize(12).TextColor(t.Text).Grow(1)
			if a.transfer.cancel != nil {
				ui.Button(c, locales.TransferCancel()).OnClick(func() { a.transfer.cancel() })
			}
		})
	}
	if a.transfer.err != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, a.transfer.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
		})
	}
	if a.transfer.note != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
			ui.Text(c, a.transfer.note).FontSize(12).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
		})
	}
}

// realFolder reports whether the folder on screen is a server folder mail
// can be imported into or exported from.
func (a *app) realFolder() bool {
	name := a.folder
	return name != "" && name != "starred" && name != draftsFolder && name != allInboxes && labelOf(name) == 0
}

// startImport reads an mbox or eml file into the folder on screen. The
// file dialog and the upload run in a job; nothing reaches another server.
func (a *app) startImport(eml bool) {
	if a.transfer.busy {
		return
	}
	if !a.realFolder() || a.active == "" {
		a.transfer.note, a.transfer.err = "", locales.TransferNoFolder()
		return
	}
	g, account, folder := a.gen, a.active, a.folder
	a.run(func() {
		paths, err := a.pickFiles(a.win)
		if err != nil || len(paths) == 0 {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.post(func() {
			a.transfer = transferState{busy: true, label: locales.TransferWorking(), cancel: cancel}
		})
		defer cancel()
		result, err := a.importPaths(ctx, account, folder, paths, eml)
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.transfer.busy, a.transfer.cancel = false, nil
			switch {
			case err == context.Canceled || ctx.Err() == context.Canceled:
				a.transfer.note = locales.TransferCancelled()
			case err != nil:
				a.transfer.err = locales.TransferFailed(failureText(err))
			case result.Skipped > 0:
				a.transfer.note = locales.TransferImportedSkipped(result.Imported, result.Skipped)
			default:
				a.transfer.note = locales.TransferImported(result.Imported)
			}
			if result.Imported > 0 {
				a.reloadFolders()
				a.loadMessages(g)
			}
		})
	})
}

// importPaths imports every chosen file, which one eml file may hold one
// message and one mbox file many.
func (a *app) importPaths(ctx context.Context, account, folder string, paths []string, eml bool) (mail.ImportResult, error) {
	var total mail.ImportResult
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if eml || strings.EqualFold(filepath.Ext(path), ".eml") {
			data, err := os.ReadFile(path)
			if err != nil {
				total.Skipped++
				continue
			}
			result, err := a.svc.ImportEML(ctx, account, folder, data)
			total.Imported += result.Imported
			total.Skipped += result.Skipped
			if err != nil {
				return total, err
			}
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			total.Skipped++
			continue
		}
		result, err := a.svc.ImportMbox(ctx, account, folder, f, func(r mail.ImportResult) {
			a.post(func() {
				if !a.transfer.busy {
					return
				}
				a.transfer.done = total.Imported + r.Imported
			})
		})
		f.Close()
		total.Imported += result.Imported
		total.Skipped += result.Skipped
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// startExport writes the folder on screen, the whole account, to an mbox
// file, or the chosen messages as eml.
func (a *app) startExport(account bool) {
	if a.transfer.busy {
		return
	}
	if !account && !a.realFolder() {
		a.transfer.note, a.transfer.err = "", locales.TransferNoFolder()
		return
	}
	req := mail.ExportRequest{AccountID: a.active, IncludeAttachments: true}
	req.Folder = a.folder
	if account {
		req.Folder = ""
	}
	g := a.gen
	a.run(func() {
		name := "mail.mbox"
		if !account {
			name = safeFileName(a.folder) + ".mbox"
		}
		path, err := a.saveFile(a.win, name)
		if err != nil || path == "" {
			return
		}
		req.Format = mail.FormatMbox
		if strings.EqualFold(filepath.Ext(path), ".eml") {
			req.Format = mail.FormatEML
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.post(func() {
			a.transfer = transferState{busy: true, label: locales.TransferWorking(), cancel: cancel}
		})
		defer cancel()
		f, err := os.Create(path)
		if err != nil {
			a.post(func() {
				a.transfer.busy, a.transfer.cancel = false, nil
				a.transfer.err = locales.TransferFailed(failureText(err))
			})
			return
		}
		result, err := a.svc.Export(ctx, req, f, func(r mail.ExportResult) {
			a.post(func() {
				if a.transfer.busy {
					a.transfer.done = r.Messages
				}
			})
		})
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.transfer.busy, a.transfer.cancel = false, nil
			if err != nil {
				if err == context.Canceled || ctx.Err() == context.Canceled {
					a.transfer.note = locales.TransferCancelled()
					return
				}
				a.transfer.err = locales.TransferFailed(failureText(err))
				return
			}
			a.transfer.note = locales.TransferExported(result.Messages)
		})
	})
}

// backupSettings writes the accounts and compose settings, without any
// secret, where the user chooses.
func (a *app) backupSettings() {
	if a.transfer.busy {
		return
	}
	g := a.gen
	a.run(func() {
		path, err := a.saveFile(a.win, "shu-settings.json")
		if err != nil || path == "" {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		data, err := a.svc.BackupConfig(ctx)
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.transfer.err = locales.TransferFailed(failureText(err))
				return
			}
			a.transfer.note = locales.BackupDone()
		})
	})
}

// restoreSettings adds the accounts of a backup, without their secrets.
func (a *app) restoreSettings() {
	if a.transfer.busy {
		return
	}
	g := a.gen
	a.run(func() {
		paths, err := a.pickFiles(a.win)
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err == nil {
			ctx, cancel := a.ctx()
			defer cancel()
			var added int
			added, err = a.svc.RestoreConfig(ctx, data)
			a.post(func() {
				if a.stale(g) {
					return
				}
				if err != nil {
					a.transfer.err = locales.TransferFailed(failureText(err))
					return
				}
				a.transfer.note = locales.RestoreDone(added) + " " + locales.RestoreNeedsSignIn()
				a.start()
			})
			return
		}
		a.post(func() {
			if !a.stale(g) {
				a.transfer.err = locales.TransferFailed(failureText(err))
			}
		})
	})
}

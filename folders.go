package main

import (
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The folder actions: a folder made, renamed, deleted or hidden from the
// sidebar, and messages moved into one by its menu or by a drag. Each
// reaches the server first; a failure leaves the folder list as it was
// and says why.

// folderForm is the dialog of a folder made or renamed. The delete
// confirmation uses its own dialog and the same state for its target.
type folderForm struct {
	open    bool
	mode    string // "create" or "rename"
	account string
	parent  string
	old     string
	name    string
	busy    bool
	err     string

	// Deleting a folder asks first: target is the folder, and its count
	// the messages the confirmation names.
	target     string
	count      int
	confirming bool
	deleting   bool
}

// folderByName returns the active account's folder of a name.
func (a *app) folderByName(name string) *mail.Folder {
	for i := range a.folders {
		if a.folders[i].Name == name {
			return &a.folders[i]
		}
	}
	return nil
}

// openNewFolder opens the form for a folder under parent, with the
// parent's delimiter already typed.
func (a *app) openNewFolder(parent string) {
	if a.active == "" {
		return
	}
	name := ""
	if parent != "" {
		if f := a.folderByName(parent); f != nil {
			name = parent + f.Delimiter
		} else {
			name = parent + "/"
		}
	}
	a.folderForm = folderForm{open: true, mode: "create", account: a.active, parent: parent, name: name}
}

// openRenameFolder opens the form for a folder's new name.
func (a *app) openRenameFolder(name string) {
	f := a.folderByName(name)
	if a.active == "" || f == nil || f.IsSystem() {
		return
	}
	a.folderForm = folderForm{open: true, mode: "rename", account: a.active, old: name, name: name}
}

// submitFolderForm makes or renames the folder the form holds. A failure
// stays in the dialog, so the name can be corrected without starting
// over.
func (a *app) submitFolderForm() {
	f := &a.folderForm
	name := strings.TrimSpace(f.name)
	if !f.open || f.busy || name == "" {
		return
	}
	g, account, mode, old := a.gen, f.account, f.mode, f.old
	f.busy, f.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		if mode == "rename" {
			err = a.svc.RenameFolder(ctx, account, old, name)
		} else {
			_, err = a.svc.CreateFolder(ctx, account, name)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			f.busy = false
			if err != nil {
				f.err = folderErrorText(err)
				return
			}
			f.open = false
			if mode == "rename" {
				a.folderNote = locales.FolderRenamed(old, name)
			} else {
				a.folderNote = locales.FolderCreated(name)
			}
			a.reloadFolders()
		})
	})
}

// askDeleteFolder asks before a folder and the mail in it go. Nothing is
// deleted without the answer.
func (a *app) askDeleteFolder(name string) {
	f := a.folderByName(name)
	if a.active == "" || f == nil || f.IsSystem() {
		return
	}
	a.folderForm = folderForm{target: name, count: f.Total, confirming: true}
}

// deleteFolder deletes the folder the confirmation named, and its mail,
// once the user said so.
func (a *app) deleteFolder() {
	f := &a.folderForm
	if f.deleting || f.target == "" {
		return
	}
	g, account, name := a.gen, a.active, f.target
	f.deleting = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.DeleteFolder(ctx, account, name)
		a.post(func() {
			if a.stale(g) {
				return
			}
			f.deleting = false
			if err != nil {
				a.err = locales.FolderActionFailed(folderErrorText(err))
				return
			}
			a.folderNote = locales.FolderDeleted(name)
			a.reloadFolders()
		})
	})
}

// setFolderSubscribed hides a folder from the sidebar, or shows it
// again. Its mail and its cursor stay either way.
func (a *app) setFolderSubscribed(name string, on bool) {
	account := a.active
	if account == "" {
		return
	}
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SetFolderSubscribed(ctx, account, name, on)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.FolderActionFailed(folderErrorText(err))
				return
			}
			if on {
				a.folderNote = locales.FolderShown(name)
			} else {
				a.folderNote = locales.FolderHidden(name)
			}
			a.reloadFolders()
		})
	})
}

// reloadFolders reads the folder list again after an action, and the
// list of a folder that went.
func (a *app) reloadFolders() {
	g, account := a.gen, a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, account)
		a.post(func() {
			if a.stale(g) || a.active != account {
				return
			}
			if err == nil {
				a.setFolders(folders)
				a.ensureFolder()
			}
			a.loadMessages(g)
		})
	})
}

// folderErrorText says why a folder action failed, in the user's words.
func folderErrorText(err error) string {
	switch {
	case errors.Is(err, mail.ErrFolderExists):
		return locales.FolderExists()
	case errors.Is(err, mail.ErrFolderName):
		return locales.FolderBadName()
	case errors.Is(err, mail.ErrFolderSystem):
		return locales.FolderSystem()
	case errors.Is(err, mail.ErrNotFound):
		return locales.FolderGone()
	}
	return failureText(err)
}

// folderMenu is the menu of one of the sidebar's folders.
func (a *app) folderMenu(m *ui.Menu, name string, system bool) {
	f := a.folderByName(name)
	if f == nil {
		return
	}
	if !system {
		if m.Item(locales.NewSubfolder()).Chosen() {
			a.openNewFolder(name)
		}
		if m.Item(locales.RenameFolder()).Chosen() {
			a.openRenameFolder(name)
		}
		if m.Item(locales.DeleteFolder()).Chosen() {
			a.askDeleteFolder(name)
		}
		m.Separator()
	}
	if f.Subscribed {
		if m.Item(locales.HideFromSidebar()).Chosen() {
			a.setFolderSubscribed(name, false)
		}
	} else if m.Item(locales.ShowInSidebar()).Chosen() {
		a.setFolderSubscribed(name, true)
	}
}

// moveFolderMenu lists the account's folders a move may go to.
func (a *app) moveFolderMenu(m *ui.Menu) {
	added := 0
	for _, f := range a.folders {
		if f.Name == a.folder || f.SpecialUse == mail.SpecialFlagged {
			continue
		}
		if m.Item(f.Name).Chosen() {
			a.moveToFolder(f.Name)
		}
		added++
	}
	if added == 0 {
		m.Item(locales.NoOtherFolders()).Disabled(true)
	}
}

// folderDialog draws the create, rename and delete surfaces.
func (a *app) folderDialog(c *ui.Context) {
	f := &a.folderForm
	t := c.Theme()
	if f.confirming {
		title := locales.DeleteFolderTitle(f.target)
		if ui.AlertDialog(c, &f.confirming, title,
			locales.DeleteFolderConfirm(f.target, f.count),
			locales.Cancel(), locales.DeleteFolder()) == 1 {
			a.deleteFolder()
		}
		if !f.confirming {
			f.target = ""
		}
	}
	if !f.open {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(14).Children(func() {
			title, save := locales.NewFolder(), locales.CreateFolder()
			if f.mode == "rename" {
				title, save = locales.RenameFolderTitle(f.old), locales.Save()
			}
			ui.Text(c, title).FontSize(17).Bold()
			var name ui.Element
			ui.Column(c).Gap(4).Children(func() {
				caption := ui.Text(c, locales.FolderNameLabel()).FontSize(11).Bold().TextColor(t.TextMuted)
				name = ui.TextInput(c, &f.name).LabelledBy(caption).
					FillWidth().AutoFocus().Invalid(f.err != "").OnSubmit(a.submitFolderForm)
			})
			if f.err != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
					name.ErrorMessage(ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus))
				})
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.busy {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				ui.Button(c, locales.Cancel()).Label(locales.CancelLabelForm()).OnClick(func() { f.open = false })
				ui.PrimaryButton(c, save).Disabled(f.busy || strings.TrimSpace(f.name) == "").
					OnClick(a.submitFolderForm)
			})
		})
	})
}

// folderDrop is what a drag of rows carries: the messages, or the
// conversations, and the account whose folders take it.
type folderDrop struct {
	account string
	msgs    []mail.Summary
	convs   []mail.Conversation
}

// dragRows is the list's DragRows: the rows the pointer drags, as the
// messages or conversations they show.
func (a *app) dragRows(rows []int) any {
	d := &folderDrop{account: a.active}
	if a.threaded() {
		for _, i := range rows {
			if i >= 0 && i < len(a.convs) {
				d.convs = append(d.convs, a.convs[i])
			}
		}
	} else {
		for _, i := range rows {
			if i >= 0 && i < len(a.all) {
				d.msgs = append(d.msgs, a.all[i])
			}
		}
	}
	if len(d.msgs) == 0 && len(d.convs) == 0 {
		return nil
	}
	return d
}

// dropOnFolder moves what a drag carried to the folder it was let go on.
func (a *app) dropOnFolder(d *folderDrop, dest string) {
	if d == nil || d.account != a.active || dest == "" {
		return
	}
	if len(d.convs) > 0 {
		a.moveConvsToFolder(d.convs, dest)
		return
	}
	a.moveMessagesToFolder(d.msgs, dest)
}

// moveToFolder moves the chosen messages to one folder of their account,
// as the toolbar's moves do, and offers the same undo.
func (a *app) moveToFolder(dest string) {
	if a.threaded() {
		a.moveThreadsToFolder(dest)
		return
	}
	a.moveMessagesToFolder(a.targets(), dest)
}

// moveMessagesToFolder moves messages to one folder of the account on
// screen.
func (a *app) moveMessagesToFolder(all []mail.Summary, dest string) {
	var targets []mail.Summary
	for _, m := range all {
		if m.Account == a.active && m.Folder != dest && m.DraftID == "" {
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		return
	}
	chosen := map[string]bool{}
	for _, m := range targets {
		chosen[summaryKey(m)] = true
		if !m.Seen {
			a.addUnread(m.Account, m.Folder, -1)
		}
	}
	kept := make([]mail.Summary, 0, len(a.all)-len(targets))
	for _, m := range a.all {
		if !chosen[summaryKey(m)] {
			kept = append(kept, m)
		}
	}
	a.all = kept
	a.sel.Clear()
	a.setUndo(nil)
	if a.selected >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	a.updateBadge()
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			first mail.MoveResult
			err   error
		)
		for i, m := range targets {
			result, moveErr := a.svc.Move(ctx, m.Account, m.Folder, m.UID, dest)
			if moveErr != nil {
				err = moveErr
				break
			}
			if i == 0 {
				first = result
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = failureText(err)
			} else if len(targets) == 1 && (first.DestUID != 0 || first.Pending != 0) {
				m := targets[0]
				a.setUndo(undoOfMove(m.Account, m.Folder, dest, locales.MovedToFolder(dest), first))
			}
			a.loadMessages(g)
			a.loadFollowUps(g)
		})
	})
}

// moveThreadsToFolder moves the chosen conversations' messages, in the
// list's scope, to one folder of their account.
func (a *app) moveThreadsToFolder(dest string) {
	a.moveConvsToFolder(a.threadTargets(), dest)
}

// moveConvsToFolder moves the conversations' messages in the list's scope
// to one folder of the account on screen.
func (a *app) moveConvsToFolder(all []mail.Conversation, dest string) {
	var targets []mail.Conversation
	for _, c := range all {
		if c.Account == a.active {
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		return
	}
	// A list of several folders has the user's own messages in it: as the
	// toolbar's move does, this one leaves Sent and Drafts alone.
	stay := map[string]bool{}
	if a.spansFolders() {
		for _, c := range targets {
			for _, special := range []string{mail.SpecialSent, mail.SpecialDrafts} {
				if name := a.specialFolder(c.Account, special); name != "" {
					stay[folderKey(c.Account, name)] = true
				}
			}
		}
	}
	shown := a.shownMembers(targets)
	chosen := threadSet(targets)
	keptAll, keptConvs := a.all[:0:0], a.convs[:0:0]
	for i, c := range a.convs {
		if !chosen[refOf(c)] {
			keptAll, keptConvs = append(keptAll, a.all[i]), append(keptConvs, c)
		}
	}
	a.all, a.convs = keptAll, keptConvs
	a.sel.Clear()
	a.setUndo(nil)
	if a.selected >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	if a.conv != nil && chosen[a.conv.ref()] {
		a.conv = nil
	}
	g, scope := a.gen, a.listScope()
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		members, err := a.scopeMessages(ctx, scope, targets, shown)
		members = slices.DeleteFunc(members, func(m mail.Summary) bool { return stay[folderKey(m.Account, m.Folder)] })
		var moved []mail.MoveResult
		if err == nil {
			moved, err = a.svc.MoveMessages(ctx, a.active, refsOf(members), dest)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if len(moved) > 0 {
				a.movedOut(members, map[string][]mail.MoveResult{a.active: moved})
			}
			if err != nil {
				a.err = failureText(err)
			}
			if canUndo(map[string][]mail.MoveResult{a.active: moved}) {
				a.setUndo(&undoAction{label: locales.MovedToFolder(dest), moved: map[string][]mail.MoveResult{a.active: moved}})
			}
			a.loadMessages(g)
			a.loadFollowUps(g)
		})
	})
}

// folderNoteBanner says what the last folder action did, and lets the
// note go.
func (a *app) folderNoteBanner(c *ui.Context, t *ui.Theme) {
	bannerRow(c, "banner/folder-note", t.Accent.Alpha(0.08)).Children(func() {
		ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Accent)
		ui.Text(c, a.folderNote).FontSize(12).TextColor(t.Text).Grow(1).MinWidth(0).MaxLines(2).Role(ui.RoleStatus)
		a.iconButton(c, closeIcon, locales.DismissFolderNote(), locales.DismissFolderNote()).
			OnClick(func() { a.folderNote = "" })
	})
}

// folderLeaf is how a folder is written in the sidebar: its own name,
// indented by its depth under the hierarchy's delimiter.
func folderLeaf(f mail.Folder) string {
	delimiter := f.Delimiter
	if delimiter == "" {
		return f.Name
	}
	depth := strings.Count(f.Name, delimiter)
	if depth == 0 {
		return f.Name
	}
	leaf := f.Name
	if i := strings.LastIndex(f.Name, delimiter); i >= 0 {
		leaf = f.Name[i+len(delimiter):]
	}
	return strings.Repeat("  ", depth) + leaf
}

// moveMessageTo moves one message of an open conversation to a folder,
// as its other moves do, and offers the undo.
func (a *app) moveMessageTo(m mail.Summary, dest string) {
	cv := a.conv
	if cv == nil || dest == "" || m.Folder == dest {
		return
	}
	a.moveMessageToFolder(m, dest)
}

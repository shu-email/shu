package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// folderByName returns the active account's folder of a name.
func (a *app) folderByName(name string) *mail.Folder {
	for i := range a.folders {
		if a.folders[i].Name == name {
			return &a.folders[i]
		}
	}
	return nil
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

// moveFolderMenu lists the account's folders a move may go to. The
// folders are still where mail goes on a server, and an action still
// names one, though the sidebar no longer lists them.
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

func (a *app) moveMessageTo(m mail.Summary, dest string) {
	cv := a.conv
	if cv == nil || dest == "" || m.Folder == dest {
		return
	}
	a.moveMessageToFolder(m, dest)
}

// selectFolder switches the list to a folder and syncs it.
func (a *app) selectFolder() {
	a.gen++
	a.selected = -1
	a.setUndo(nil)
	a.setLabelNote("")
	a.labels.picker = false
	a.list = ui.ListState{}
	a.sel.Clear()
	a.clearTriage()
	a.clearRows()
	a.query = ""
	a.scopeSet = false
	if mb := a.shownSmart(); mb != nil {
		// A smart mailbox opens the search it saved, as it was saved.
		a.query, a.scope, a.scopeSet = mb.Query, scopeOfName(mb.Scope), true
	}
	a.loadMessages(a.gen)
	a.syncFolder(a.gen)
}

// switchAccount makes another account active.
func (a *app) switchAccount(id string) {
	if id == a.active {
		return
	}
	a.active = id
	a.beginAccount()
	accountID := id
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.SetActive(ctx, accountID)
	})
}

// refresh is the Sync button. It syncs the account on screen whatever
// state its watcher is in, and has every watcher check now.
func (a *app) refresh() {
	a.gen++
	a.syncAccount(a.gen)
	a.nudge()
}

func (a *app) ensureFolder() {
	if a.folder != "" && a.folderExists(a.folder) {
		return
	}
	a.folder = ""
	for _, f := range a.folders {
		if f.SpecialUse == mail.SpecialInbox {
			a.folder = f.Name
			return
		}
	}
	if len(a.folders) > 0 {
		a.folder = a.folders[0].Name
	}
}

func (a *app) folderExists(id string) bool {
	if id == "starred" || id == draftsFolder || id == remindersFolder || id == followUpFolder || id == allInboxes {
		return true
	}
	if smart := smartOf(id); smart != 0 {
		return a.smartByID(smart) != nil
	}
	if label := labelOf(id); label != 0 {
		return a.findLabel(a.active, label) != nil
	}
	for _, f := range a.folders {
		if f.Name == id {
			return true
		}
	}
	return false
}

func (a *app) folderBySpecial(special string) string {
	for _, f := range a.folders {
		if f.SpecialUse == special {
			return f.Name
		}
	}
	return ""
}

// specialFolder is an account's folder of a special use, where an action
// on its mail moves it, or "" while the account's folders are not known.
func (a *app) specialFolder(account, special string) string {
	for _, f := range a.foldersFor(account) {
		if f.SpecialUse == special {
			return f.Name
		}
	}
	return ""
}

// foldersFor is the folders of an account as the window has them.
func (a *app) foldersFor(account string) []mail.Folder {
	if account != "" && account == a.foldersOf {
		return a.folders
	}
	return a.otherFolders[account]
}

// listScope is the part of the cache the list shows.
func (a *app) listScope() mail.Scope { return a.listQuery().mailScope() }

func (a *app) folderLabel(id string) string { return a.folderLabelOf(a.foldersOf, id) }

// folderLabelOf is the name the window gives an account's folder: its
// special use in the user's language, else the server's name. A label's
// list goes by the label's name.
func (a *app) folderLabelOf(account, id string) string {
	switch id {
	case "starred":
		return locales.Starred()
	case draftsFolder:
		return locales.Drafts()
	case remindersFolder:
		return locales.Reminders()
	case followUpFolder:
		return locales.FollowUp()
	case allInboxes:
		return locales.AllInboxes()
	}
	if smart := smartOf(id); smart != 0 {
		if mb := a.smartByID(smart); mb != nil {
			return mb.Name
		}
		return ""
	}
	if label := labelOf(id); label != 0 {
		if l := a.findLabel(account, label); l != nil {
			return l.Name
		}
		return ""
	}
	for _, f := range a.foldersFor(account) {
		if f.Name != id {
			continue
		}
		switch f.SpecialUse {
		case mail.SpecialInbox:
			return locales.Inbox()
		case mail.SpecialSent:
			return locales.Sent()
		case mail.SpecialDrafts:
			return locales.Drafts()
		case mail.SpecialArchive:
			return locales.Archive()
		case mail.SpecialJunk:
			return locales.Junk()
		case mail.SpecialTrash:
			return locales.Trash()
		}
		return f.Name
	}
	return id
}

func (a *app) folderUnread(id string) int {
	for _, f := range a.folders {
		if f.Name == id {
			return f.Unread
		}
	}
	return 0
}

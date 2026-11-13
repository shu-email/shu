package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// allInboxes is the sidebar's entry of the unified inbox: the inbox of
// every account in one list. Like Starred it is no folder of the server.
const allInboxes = "all-inboxes"

// isUnified reports whether the list is the unified inbox.
func (a *app) isUnified() bool { return a.folder == allInboxes }

// unifiedUnread is the unread count of every account's inbox together.
func (a *app) unifiedUnread() int {
	n := 0
	for _, acc := range a.accounts {
		n += a.inboxes[acc.ID]
	}
	return n
}

// unifiedCounts is the line under the unified inbox's title: each
// account's unread count, by its name.
func (a *app) unifiedCounts() string {
	parts := make([]string, 0, len(a.accounts))
	for _, acc := range a.accounts {
		parts = append(parts, locales.AccountUnread(acc.Label(), a.inboxes[acc.ID]))
	}
	return strings.Join(parts, " · ")
}

// accountLabel is an account's name as the window shows it.
func (a *app) accountLabel(id string) string {
	if acc := a.account(id); acc != nil {
		return acc.Label()
	}
	return id
}

// setAccountFolders takes an account's folders as read from the cache or
// the server: the sidebar's when the account is on screen.
func (a *app) setAccountFolders(id string, folders []mail.Folder) {
	if id == a.active {
		a.setFolders(folders)
		return
	}
	if a.account(id) != nil {
		a.otherFolders[id] = folders
	}
}

// syncInboxes syncs the inbox of every account, as opening the unified
// inbox, or Sync while it shows, does for the folder on show. An account
// whose folders the cache does not have yet, never opened here, has them
// listed first.
func (a *app) syncInboxes(g uint64) {
	type inbox struct{ account, folder, label string }
	var inboxes []inbox
	for _, acc := range a.accounts {
		inboxes = append(inboxes, inbox{acc.ID, a.specialFolder(acc.ID, mail.SpecialInbox), acc.Label()})
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var failed error
		folders := map[string][]mail.Folder{}
		// An account that has to sign in again is no failure: its banner
		// says so.
		var signedOut []string
		fail := func(in inbox, err error) {
			switch {
			case errors.Is(err, mail.ErrSignInRequired):
				signedOut = append(signedOut, in.account)
			case failed == nil:
				failed = fmt.Errorf("%s: %w", in.label, err)
			}
		}
		for _, in := range inboxes {
			if in.folder == "" {
				list, err := a.svc.SyncAccount(ctx, in.account)
				if err != nil {
					fail(in, err)
					continue
				}
				folders[in.account] = list
				for _, f := range list {
					if f.IsInbox() {
						in.folder = f.Name
					}
				}
				if in.folder == "" {
					continue
				}
			}
			if err := a.svc.SyncFolder(ctx, in.account, in.folder, 0); err != nil {
				fail(in, err)
				continue
			}
			if list, err := a.svc.CachedFolders(ctx, in.account); err == nil {
				folders[in.account] = list
			}
		}
		a.post(func() {
			for _, id := range signedOut {
				a.markSignIn(id)
			}
			if a.stale(g) || !a.isUnified() {
				return
			}
			for id, list := range folders {
				a.setAccountFolders(id, list)
			}
			if failed != nil {
				a.err = locales.SyncFailed(failed.Error())
			} else {
				a.err = ""
			}
			a.updateBadge()
			if a.aim != nil {
				// As after the sync of a folder: the read that follows is
				// the last chance of the message a notification asked for.
				a.aim.last = true
			}
			a.loadMessages(g)
			a.loadFollowUps(g)
		})
	})
}

// syncMovedBack syncs the folder an undo moved a message back to, of
// whichever account, so that its row returns, and reads that account's
// counts. The folder on show syncs as Sync does.
func (a *app) syncMovedBack(g uint64, account, folder string) {
	if account == a.active && folder == a.folder {
		a.syncFolder(g)
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SyncFolder(ctx, account, folder, 0)
		folders, foldersErr := a.svc.CachedFolders(ctx, account)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil && !a.signInRequired(account, err) {
				a.err = locales.SyncFailed(err.Error())
			}
			if foldersErr == nil {
				a.setAccountFolders(account, folders)
				a.updateBadge()
			}
			a.loadMessages(g)
			a.loadFollowUps(g)
		})
	})
}

// mailPlace is where a chosen row is: its account, and its folder, or ""
// for a conversation of a list of several folders.
type mailPlace struct{ account, folder string }

// moves reports whether an action that moves mail to its account's folder
// of a special use moves mail from this place: the account has that
// folder, and it is not the folder the mail is already in. Shu deletes
// nothing for good, and a move to the same folder would only give the
// message another UID on the server.
func (a *app) moves(p mailPlace, special string) bool {
	dest := a.specialFolder(p.account, special)
	return dest != "" && dest != p.folder
}

// canMove reports whether such an action moves mail from any of these
// places: rows of an account without that folder, or already in it, stay
// where they are.
func (a *app) canMove(places []mailPlace, special string) bool {
	for _, p := range places {
		if a.moves(p, special) {
			return true
		}
	}
	return false
}

// threadPlace is where the chosen conversations of an account are: the
// list's folder in a list of one folder of the server, which a move to
// it would leave as it is. In a list of several, the move leaves the
// messages already in its folder (see Service.MoveMessages) and takes
// the rest.
func (a *app) threadPlace(account string) mailPlace {
	if a.listQuery().isFolder() {
		return mailPlace{account, a.folder}
	}
	return mailPlace{account: account}
}

// isInboxOf reports whether a folder is an account's inbox.
func (a *app) isInboxOf(account, folder string) bool {
	return folder != "" && folder == a.specialFolder(account, mail.SpecialInbox)
}

// aimShown reports whether the list on screen is where the message a
// notification asked for is: its folder, or the unified inbox for an
// inbox's.
func (a *app) aimShown(aim *noteAim) bool {
	if a.isUnified() {
		return a.isInboxOf(aim.account, aim.folder)
	}
	return aim.account == a.active && aim.folder == a.folder
}

// rowPlace is what a row says of where its message is: its account, in a
// list of several, and its folder, when that is not the list's own.
func (a *app) rowPlace(m mail.Summary) (account, folder string) {
	if m.DraftID != "" {
		return "", ""
	}
	if a.spansAccounts() {
		account = a.accountLabel(m.Account)
	}
	switch {
	case a.draftsList():
		// Every row is a draft: the list's title says where.
	case a.isUnified():
		if !a.isInboxOf(m.Account, m.Folder) {
			folder = a.folderLabelOf(m.Account, m.Folder)
		}
	case m.Folder != a.folder:
		folder = a.folderLabelOf(m.Account, m.Folder)
	}
	return account, folder
}

// accountTag is the small name of an account on a row, which tells rows of
// several accounts apart by their words rather than by a color.
func accountTag(c *ui.Context, name string, color ui.Color) {
	ui.Text(c, name).FontSize(10).Bold().TextColor(color).Shrink(0).MaxWidth(110).SingleLine().
		Padding(0, 5).Radius(7).Border(1, color.Alpha(0.55)).Label(locales.InAccount(name))
}

// placeLine is the reading pane's line of where the open message is, in a
// list of several accounts or folders: "" elsewhere, where the sidebar
// and the list's title say it.
func (a *app) placeLine(m mail.Summary) string {
	if m.DraftID != "" || !a.spansAccounts() && !a.spansFolders() {
		return ""
	}
	folder := a.folderLabelOf(m.Account, m.Folder)
	if !a.spansAccounts() {
		return locales.InFolder(folder)
	}
	return locales.InAccountFolder(a.accountLabel(m.Account), folder)
}

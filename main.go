package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
	"shu/store"
)

// app is the state the windows show.
// undoMove remembers the last move so the list can offer to take it
// back. The destination UID comes from the server's MOVE/COPY response.
type undoMove struct {
	accountID string
	source    string
	dest      string
	destUID   mail.UID
	label     string
}

type app struct {
	svc mailService

	win    *mygo.Window
	run    func(func())
	update func(func())
	closed bool

	started bool
	gen     uint64

	accounts []mail.Account
	active   string
	folders  []mail.Folder
	folder   string
	query    string

	all         []mail.Summary
	selected    int
	list        ui.ListState
	sel         ui.Selection[string]
	searchField ui.Handle
	focusList   bool
	bodies      map[string]mail.FetchResult
	bodyLocked  bool
	bodyErr     string
	loading     bool
	err         string

	pending          []mail.PendingSend
	pendingDismissed bool
	undo             *undoMove

	showAccounts bool
	showSettings bool
	accountForm  accountForm
	accountBusy  bool
	accountErr   string
	accountNote  string

	htmlOn   bool
	webview  *mygo.WebView
	htmlKey  string
	services ui.Services

	composes []*composeState

	// Hooks the window sets; tests leave them nil.
	newWindow func(mygo.WindowOptions) *mygo.Window
	saveFile  func(parent *mygo.Window, name string) (string, error)
	setLocale func(string)
	setBadge  func(int)
	lang      int
}

// newApp makes the state the tests and main use.
func newApp(svc mailService) *app {
	return &app{
		svc:       svc,
		run:       func(f func()) { go f() },
		selected:  -1,
		focusList: true,
		bodies:    map[string]mail.FetchResult{},
	}
}

// newTestApp runs every job and update inline, so tests never sleep or
// race.
func newTestApp(svc mailService) *app {
	a := newApp(svc)
	a.run = func(f func()) { f() }
	return a
}

// ctx is the timeout of one network or cache job.
func (a *app) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// post publishes a result on the UI thread, or right away in tests.
func (a *app) post(f func()) {
	if a.closed {
		return
	}
	if a.update != nil {
		a.update(f)
		return
	}
	f()
}

func (a *app) stale(g uint64) bool { return a.closed || g != a.gen }

// attach connects the window, its update channel and its hooks.
func (a *app) attach(win *mygo.Window) {
	a.win = win
	a.update = win.Update
	a.newWindow = func(opts mygo.WindowOptions) *mygo.Window { return mygo.NewWindow(opts) }
	a.saveFile = func(parent *mygo.Window, name string) (string, error) {
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: parent, DefaultPath: name})
	}
	a.setLocale = mygo.App.SetLocale
	a.setBadge = mygo.App.SetBadgeCount
	win.OnClosed(func() { a.closed = true })
	mygo.App.OnLocaleChanged(func(string) { win.SetTitle(locales.AppName()) })
}

// start reads the accounts before the first sync.
func (a *app) start() {
	if a.started {
		return
	}
	a.started = true
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		accounts, active, err := a.svc.Accounts(ctx)
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.accounts = accounts
			if err != nil {
				a.err = err.Error()
			}
			if len(accounts) == 0 {
				a.openAccountForm(nil)
				return
			}
			a.active = active
			if a.account(a.active) == nil {
				a.active = accounts[0].ID
			}
			a.beginAccount()
		})
	})
}

func (a *app) account(id string) *mail.Account {
	for i := range a.accounts {
		if a.accounts[i].ID == id {
			return &a.accounts[i]
		}
	}
	return nil
}

func (a *app) activeAccount() *mail.Account { return a.account(a.active) }

// beginAccount loads one account's cache, then syncs it in the background.
func (a *app) beginAccount() {
	a.gen++
	a.folder = ""
	a.query = ""
	a.selected = -1
	a.undo = nil
	a.list = ui.ListState{}
	a.sel.Clear()
	a.bodies = map[string]mail.FetchResult{}
	a.all = nil
	a.loadCache(a.gen)
}

// loadCache shows cached folders and messages at once.
func (a *app) loadCache(g uint64) {
	accountID := a.active
	a.loading = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, accountID)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err == nil {
				a.folders = folders
				a.ensureFolder()
				a.updateBadge()
			}
			a.loadMessages(g)
			a.syncAccount(g)
		})
	})
}

// syncAccount refreshes the folder list and then the selected folder.
func (a *app) syncAccount(g uint64) {
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.SyncAccount(ctx, accountID)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.SyncFailed(err.Error())
			} else {
				a.err = ""
				if folders != nil {
					a.folders = folders
				}
				a.ensureFolder()
				a.updateBadge()
			}
			a.loadMessages(g)
			a.syncFolder(g)
			a.loadPending(g)
		})
	})
}

// syncFolder refreshes the selected folder and its unread counts.
func (a *app) syncFolder(g uint64) {
	accountID, folder := a.active, a.folder
	if folder == "" || folder == "starred" {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SyncFolder(ctx, accountID, folder, 50)
		var folders []mail.Folder
		if err == nil {
			folders, _ = a.svc.CachedFolders(ctx, accountID)
		}
		a.post(func() {
			if a.stale(g) || a.folder != folder {
				return
			}
			if err != nil {
				a.err = locales.SyncFailed(err.Error())
			} else {
				a.err = ""
				if folders != nil {
					a.folders = folders
				}
				a.updateBadge()
			}
			a.loadMessages(g)
		})
	})
}

// loadMessages reads the current folder and query from the cache.
func (a *app) loadMessages(g uint64) {
	accountID, folder, query := a.active, a.folder, strings.TrimSpace(a.query)
	key := a.selectedKey()
	if folder == "" {
		a.all = nil
		a.loading = false
		return
	}
	a.loading = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		msgs, err := a.svc.CachedMessages(ctx, accountID, folder, query, 50)
		a.post(func() {
			if a.stale(g) || a.folder != folder || strings.TrimSpace(a.query) != query {
				return
			}
			a.loading = false
			if err != nil {
				a.err = err.Error()
				return
			}
			a.all = msgs
			a.pruneSelection()
			a.restoreSelection(key)
			if a.selected < 0 && len(a.all) > 0 && query == "" && a.sel.Len() == 0 {
				a.selected = 0
			}
			a.reloadBody()
		})
	})
}

// pruneSelection forgets rows the list no longer shows, so the count and
// actions match what the user sees.
func (a *app) pruneSelection() {
	if a.sel.Len() == 0 {
		return
	}
	present := make(map[string]bool, len(a.all))
	for _, m := range a.all {
		present[summaryKey(m)] = true
	}
	for k := range a.sel.All() {
		if !present[k] {
			a.sel.Remove(k)
		}
	}
}

// selectedKey is the identity of the chosen message, or "".
func (a *app) selectedKey() string {
	if a.selected < 0 || a.selected >= len(a.all) {
		return ""
	}
	return summaryKey(a.all[a.selected])
}

// restoreSelection finds the chosen message again by its key.
func (a *app) restoreSelection(key string) {
	a.selected = -1
	if key == "" {
		return
	}
	for i, m := range a.all {
		if summaryKey(m) == key {
			a.selected = i
			return
		}
	}
}

func summaryKey(m mail.Summary) string {
	return fmt.Sprintf("%s/%d", m.Folder, m.UID)
}

// reloadBody shows the body of the chosen row, if any.
func (a *app) reloadBody() {
	if a.selected < 0 || a.selected >= len(a.all) {
		a.bodyLocked = false
		a.bodyErr = ""
		return
	}
	a.loadBody(a.gen)
}

// loadBody reads a cached body, else fetches it, then marks the message
// read.
func (a *app) loadBody(g uint64) {
	idx := a.selected
	if idx < 0 || idx >= len(a.all) {
		return
	}
	m := a.all[idx]
	key := summaryKey(m)
	accountID, folder, uid := a.active, m.Folder, m.UID
	if result, ok := a.bodies[key]; ok {
		a.bodyLocked = false
		a.bodyErr = ""
		if !result.Message.Seen {
			result.Message.Seen = true
			a.bodies[key] = result
			a.applySeen(key)
			a.markSeen(accountID, folder, uid)
		}
		a.syncHTMLView()
		return
	}
	a.bodyLocked = true
	a.bodyErr = ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		result, cached, err := a.svc.CachedMessage(ctx, accountID, folder, uid)
		if err == nil && !cached {
			result, err = a.svc.FetchMessage(ctx, accountID, folder, uid, true)
		}
		a.post(func() {
			if a.stale(g) || a.selectedKey() != key {
				return
			}
			a.bodyLocked = false
			if err != nil {
				a.bodyErr = locales.BodyFailed(err.Error())
				return
			}
			if !result.Message.Seen {
				result.Message.Seen = true
				a.markSeen(accountID, folder, uid)
			}
			a.bodies[key] = result
			a.applySeen(key)
			a.syncHTMLView()
		})
	})
}

// markSeen asks the server to mark a message read; the UI already shows
// it.
func (a *app) markSeen(accountID, folder string, uid mail.UID) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.SetFlag(ctx, accountID, folder, uid, mail.FlagSeen, true)
	})
}

// applySeen marks the row read and lowers the folder badge in step.
func (a *app) applySeen(key string) {
	for i := range a.all {
		if summaryKey(a.all[i]) != key || a.all[i].Seen {
			continue
		}
		a.all[i].Seen = true
		for j := range a.folders {
			if a.folders[j].Name == a.all[i].Folder {
				a.folders[j].Unread = max(0, a.folders[j].Unread-1)
			}
		}
	}
	a.updateBadge()
}

// updateBadge puts the active account's inbox unread count on the Dock.
func (a *app) updateBadge() {
	if a.setBadge == nil {
		return
	}
	n := 0
	for _, f := range a.folders {
		if f.SpecialUse == mail.SpecialInbox {
			n = f.Unread
			break
		}
	}
	a.setBadge(n)
}

// loadPending reads the outbox's leftovers for the delivery banner.
func (a *app) loadPending(g uint64) {
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		pending, err := a.svc.PendingSends(ctx, accountID)
		a.post(func() {
			if a.stale(g) || a.active != accountID {
				return
			}
			if err == nil {
				a.pending = pending
			}
		})
	})
}

// search re-reads the list with the search field's text.
func (a *app) search() {
	a.sel.Clear()
	a.loadMessages(a.gen)
}

// selectFolder switches the list to a folder and syncs it.
func (a *app) selectFolder() {
	a.gen++
	a.selected = -1
	a.undo = nil
	a.list = ui.ListState{}
	a.sel.Clear()
	a.all = nil
	a.query = ""
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

// refresh is the Sync button.
func (a *app) refresh() {
	a.gen++
	a.syncAccount(a.gen)
}

// folderEntry is one row of the sidebar.
type folderEntry struct {
	ID    string
	Label string
	Icon  *ui.SVG
}

// folderEntries turns the cached folders into special-use first entries
// and the rest after them.
func (a *app) folderEntries() []folderEntry {
	entries := []folderEntry{{ID: "starred", Label: locales.Starred(), Icon: starIcon}}
	seen := map[string]bool{}
	add := func(special string, icon *ui.SVG, label string) {
		for _, f := range a.folders {
			if f.SpecialUse == special && !seen[f.Name] {
				seen[f.Name] = true
				entries = append(entries, folderEntry{ID: f.Name, Label: label, Icon: icon})
				return
			}
		}
	}
	add(mail.SpecialInbox, inboxIcon, locales.Inbox())
	add(mail.SpecialSent, sentIcon, locales.Sent())
	add(mail.SpecialDrafts, draftIcon, locales.Drafts())
	add(mail.SpecialArchive, archiveIcon, locales.Archive())
	add(mail.SpecialJunk, spamIcon, locales.Junk())
	add(mail.SpecialTrash, trashIcon, locales.Trash())
	for _, f := range a.folders {
		if seen[f.Name] || f.SpecialUse == mail.SpecialFlagged {
			continue
		}
		seen[f.Name] = true
		entries = append(entries, folderEntry{ID: f.Name, Label: f.Name, Icon: tagIcon})
	}
	return entries
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
	if id == "starred" {
		return true
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

func (a *app) folderLabel(id string) string {
	if id == "starred" {
		return locales.Starred()
	}
	for _, f := range a.folders {
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

// messageTime writes a list timestamp the way mail clients do: a time
// today, "yesterday", a weekday this week, a date this year.
func (a *app) messageTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := c.Now()
	y, m, d := now.Date()
	ty, tm, td := t.Date()
	if y == ty && m == tm && d == td {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Time: i18n.ShortStyle})
	}
	yesterday := now.AddDate(0, 0, -1)
	yy, ym, yd := yesterday.Date()
	if ty == yy && tm == ym && td == yd {
		return i18n.FormatRelativeTime(c.Locale(), -1, i18n.RelativeDay, i18n.RelativeTimeOptions{Auto: true})
	}
	if now.Sub(t) < 7*24*time.Hour && now.Sub(t) > 0 {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "EEE"})
	}
	if y == ty {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "MMMd"})
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "yMMMd"})
}

// longTime is the reading pane's full timestamp.
func (a *app) longTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
}

// view is the window's root: settings, account setup or the three panes.
func (a *app) view(c *ui.Context) {
	switch {
	case a.showSettings:
		a.settingsView(c)
	case a.showAccounts || len(a.accounts) == 0:
		a.accountsView(c)
	default:
		a.mailView(c)
	}
}

// mailView draws the three-pane window.
func (a *app) mailView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	c.OnShortcut(ui.Cmd, ui.KeyK, func() { a.searchField.Focus(c) })
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.openCompose(nil) })
	c.OnShortcut(ui.Cmd, ui.KeyR, func() { a.refresh() })
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.titleBar(c, t, bar, top)
		panes := ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c, t)
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			a.messageList(c, t)
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			a.readingPane(c, t)
		})
		// Unmodified shortcuts live in the three panes' focus subtree, so
		// typing in the search field is never taken for a command.
		panes.OnShortcut(0, ui.KeyJ, func() { a.stepMessage(1) })
		panes.OnShortcut(0, ui.KeyK, func() { a.stepMessage(-1) })
		panes.OnShortcut(0, ui.KeyU, func() { a.toggleRead() })
		panes.OnShortcut(0, ui.KeyS, func() { a.toggleStar() })
		panes.OnShortcut(0, ui.KeyE, func() { a.moveSelected(mail.SpecialArchive) })
		panes.OnShortcut(0, ui.KeyR, func() { a.reply(false) })
		panes.OnShortcut(0, ui.KeyF, func() { a.forward() })
		panes.OnShortcut(0, ui.KeyDelete, func() { a.moveSelected(mail.SpecialTrash) })
	})
}

// titleBar draws the search field, sync and compose.
func (a *app) titleBar(c *ui.Context, t *ui.Theme, bar ui.TitleBar, height float32) {
	ui.Row(c).Height(height).Shrink(0).AlignItems(ui.Center).Gap(10).
		Padding(0, max(bar.Right, 0)+14, 0, max(bar.Left, 0)+14).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {
		ui.Box(c).Size(26, 26).Radius(7).Center().
			Gradient(ui.Hex("#5b9cff"), ui.Hex("#2563eb"), 135).Children(func() {
			ui.Text(c, "S").FontSize(14).Bold().TextColor(ui.RGB(255, 255, 255))
		})
		ui.Text(c, locales.AppName()).FontSize(15).Bold()
		ui.Spacer(c)
		ui.SearchField(c, &a.query).Width(300).
			Placeholder(locales.SearchFolder(a.folderLabel(a.folder))).
			Label(locales.SearchMailLabel()).Bind(&a.searchField).
			OnClick(func() { a.focusList = false }).
			OnChange(func() { a.search() })
		if a.loading {
			ui.Spinner(c).Size(14, 14)
		}
		a.iconButton(c, refreshIcon, locales.Sync(), locales.SyncMail()).
			OnClick(func() { a.refresh() })
		a.iconButton(c, settingsIcon, locales.Settings(), locales.Settings()).
			OnClick(func() { a.showSettings = true })
		ui.PrimaryButton(c, "").Children(func() {
			ui.Icon(c, composeIcon).FontSize(14)
			ui.Text(c, locales.Compose())
		}).Label(locales.Compose()).Tooltip(locales.NewMessage()).
			OnClick(func() { a.openCompose(nil) })
	})
}

// sidebar draws the account switcher and the folders.
func (a *app) sidebar(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(222).Shrink(0).Background(t.Surface).Children(func() {
		ui.Row(c).Padding(10, 12, 4, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			acc := a.activeAccount()
			name, email := "", ""
			if acc != nil {
				name, email = acc.Label(), acc.Email
			}
			if name == "" {
				name = locales.AccountsTitle()
			}
			ui.Avatar(c, name, nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, name).FontSize(12).Bold().SingleLine()
				ui.Text(c, email).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			ui.PopoverMenuButton(c, "⋯", func(m *ui.Menu) {
				for _, acc := range a.accounts {
					label := acc.Label()
					if m.Item(label + " · " + acc.Email).Radio(acc.ID == a.active).Chosen() {
						a.switchAccount(acc.ID)
					}
				}
				m.Separator()
				if m.Item(locales.AddAccount()).Chosen() {
					a.openAccountForm(nil)
				}
				if m.Item(locales.ManageAccounts()).Chosen() {
					if acc := a.activeAccount(); acc != nil {
						a.openAccountForm(acc)
					} else {
						a.openAccountForm(nil)
					}
				}
				if m.Item(locales.Settings()).Chosen() {
					a.showSettings = true
				}
			}).Label(locales.AccountsMenu()).Tooltip(locales.AccountsMenu())
		})

		entries := a.folderEntries()
		changed := ui.Sidebar(c, &a.folder, func() {
			ui.SidebarSection(c, locales.Mailboxes(), nil, func() {
				for _, e := range entries {
					item := ui.SidebarItem(c, e.ID, e.Icon, e.Label)
					if e.ID != "starred" {
						if n := a.folderUnread(e.ID); n > 0 {
							item.Children(func() {
								ui.Badge(c, fmt.Sprint(n)).Background(t.Accent).TextColor(t.AccentText)
							})
						}
					}
				}
			})
		}).Grow(1).Label(locales.Mailboxes())
		if changed.Changed() {
			a.selectFolder()
		}
	})
}

// iconButton is an icon-only button named for assistive technology.
func (a *app) iconButton(c *ui.Context, svg *ui.SVG, label, tip string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(tip).Padding(6, 8).
		Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolButton is a flat toolbar button showing only an icon.
func (a *app) toolButton(c *ui.Context, svg *ui.SVG, label string, on bool, fn func()) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(label).Disabled(!on)
	if on && fn != nil {
		b = b.OnClick(fn)
	}
	return b.Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolDivider separates groups of toolbar buttons.
func (a *app) toolDivider(c *ui.Context) {
	ui.Box(c).Width(1).Height(18).Shrink(0).Margin(0, 4).Background(c.Theme().Border)
}

// keychain adapts mygo.Secrets to the engine's SecretStore.
type keychain struct{}

func (keychain) Set(key string, secret []byte) error { return mygo.Secrets.Set(key, secret) }
func (keychain) Get(key string) ([]byte, error) {
	data, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, mail.ErrNotFound
	}
	return data, err
}
func (keychain) Delete(key string) error { return mygo.Secrets.Delete(key) }

func main() {
	mygo.App.WhenReady(func() {
		dataDir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		cacheDir, err := mygo.App.Path(mygo.PathCache)
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sqlite.Load(); err != nil {
			log.Fatalf("sqlite: %v (run go tool mygo generate)", err)
		}
		db, err := store.Open(ctx, filepath.Join(dataDir, "shu.db"))
		if err != nil {
			log.Fatal(err)
		}
		a := newApp(mail.NewService(db, keychain{}, cacheDir))
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           locales.AppName(),
			Width:           1220,
			Height:          800,
			MinWidth:        1000,
			MinHeight:       640,
			StateKey:        "main",
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			BackgroundColor: "light-dark(#ffffff, #18181b)",
			Content:         ui.View(a.view),
		})
		a.attach(win)
		chosen := mygo.App.ChosenLocale()
		switch {
		case chosen == "en":
			a.lang = 1
		case chosen == "zh-Hans":
			a.lang = 2
		}
		mygo.App.OnQuit(func() { db.Close() })
		a.start()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

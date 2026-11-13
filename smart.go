package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Smart mailboxes in the window. A smart mailbox is a search the user
// saved: a name, the list it was saved from and the scope it covered, with
// the typed text as it was. Its entry in the sidebar opens that list as
// the search did, read from the cache alone (paging.go), and shows how
// many unread messages it finds. The search field's text is saved from
// the sidebar's "Save search…", and a mailbox is renamed, changed and
// deleted from its menu, which the keyboard reaches in the list's header.

// smartEntryPrefix begins the ID of a smart mailbox's entry in the
// sidebar, which a.folder holds while its list shows. No folder's name has
// a NUL.
const smartEntryPrefix = "\x00smart/"

// smartEntry is the sidebar's entry of a smart mailbox's list.
func smartEntry(id int64) string { return smartEntryPrefix + strconv.FormatInt(id, 10) }

// smartOf is the smart mailbox whose list an entry of the sidebar is, or 0.
func smartOf(entry string) int64 {
	rest, ok := strings.CutPrefix(entry, smartEntryPrefix)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// smartState is what the window has of the account on screen's smart
// mailboxes.
type smartState struct {
	// list are the account's, in their order, with how many unread
	// messages each finds as the window last counted them. loaded says
	// they were read; seq tells the reads apart, of which the one begun
	// last is kept.
	list   []mail.SmartMailbox
	loaded bool
	seq    uint64
	// open is whether the sidebar's section shows them, and form the
	// dialog that saves or changes one.
	open bool
	form smartForm
	del  smartDeletion
}

func newSmartState() smartState { return smartState{open: true} }

// smartByID is one of the account's smart mailboxes as the window has
// them, or nil.
func (a *app) smartByID(id int64) *mail.SmartMailbox {
	for i := range a.smarts.list {
		if a.smarts.list[i].ID == id && a.smarts.list[i].Account == a.active {
			return &a.smarts.list[i]
		}
	}
	return nil
}

// shownSmart is the smart mailbox whose list shows, or nil.
func (a *app) shownSmart() *mail.SmartMailbox {
	if id := smartOf(a.folder); id != 0 {
		return a.smartByID(id)
	}
	return nil
}

// scopeOfName is a scope as SaveSmartMailbox stores it, read back.
func scopeOfName(name string) searchScope {
	switch name {
	case mail.SmartScopeAccount:
		return scopeAccount
	case mail.SmartScopeAll:
		return scopeAll
	}
	return scopeHere
}

// scopeName writes a scope as SaveSmartMailbox stores it.
func scopeName(s searchScope) string {
	switch s {
	case scopeAccount:
		return mail.SmartScopeAccount
	case scopeAll:
		return mail.SmartScopeAll
	}
	return mail.SmartScopeHere
}

// smartListQuery is what a smart mailbox's list reads, whether it is open
// or not: the search saved, in the account and scope saved, as
// listQuery reads the list on screen.
func (a *app) smartListQuery(mb mail.SmartMailbox) listQuery {
	entry := smartEntry(mb.ID)
	q := listQuery{account: mb.Account, folder: entry, search: strings.TrimSpace(mb.Query),
		smartFolder: mb.Folder, threaded: a.threaded()}
	if q.search != "" {
		q.scope = a.scopeFor(entry, scopeOfName(mb.Scope), true, mb.Query)
		ids, narrowed := a.queryAccounts(parseQuery(q.search))
		q.narrowed, q.accounts = narrowed, strings.Join(ids, "\n")
	}
	return q
}

// loadSmarts reads the account's smart mailboxes and counts the unread
// mail each finds, for the sidebar.
func (a *app) loadSmarts(g uint64) {
	accountID := a.active
	if accountID == "" {
		return
	}
	a.smarts.seq++
	seq := a.smarts.seq
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		list, err := a.svc.SmartMailboxes(ctx, accountID)
		a.post(func() {
			if a.stale(g) || a.active != accountID || seq < a.smarts.seq || err != nil {
				return
			}
			a.smarts.list, a.smarts.loaded = list, true
			a.smartGone(accountID)
			queries := make([]mail.ListQuery, len(list))
			for i := range list {
				queries[i] = a.smartListQuery(list[i]).read("", 1)
			}
			if len(queries) == 0 {
				return
			}
			a.run(func() {
				ctx, cancel := a.ctx()
				defer cancel()
				counts := make([]int, len(queries))
				for i := range queries {
					if n, err := a.svc.CountUnread(ctx, queries[i]); err == nil {
						counts[i] = n
					}
				}
				a.post(func() {
					if a.stale(g) || a.active != accountID || seq < a.smarts.seq {
						return
					}
					for i := range a.smarts.list {
						for k := range list {
							if a.smarts.list[i].ID == list[k].ID {
								a.smarts.list[i].Unread = counts[k]
							}
						}
					}
				})
			})
		})
	})
}

// smartGone gives way to the inbox when the smart mailbox on show is no
// longer one the account has.
func (a *app) smartGone(accountID string) {
	if accountID != a.active || smartOf(a.folder) == 0 || a.shownSmart() != nil {
		return
	}
	a.folder = ""
	a.ensureFolder()
	a.selectFolder()
}

// smartFolderKnown reports whether the list a smart mailbox was saved from
// still exists, which its own scope needs: a folder of the server may have
// been renamed or deleted, and a label may be gone. A search of every
// folder, or of every account, needs none.
func (a *app) smartFolderKnown(mb mail.SmartMailbox) bool {
	if mb.Scope != mail.SmartScopeHere {
		return true
	}
	switch mb.Folder {
	case "", "starred", draftsFolder, remindersFolder, followUpFolder, allInboxes:
		return true
	}
	if label := labelOf(mb.Folder); label != 0 {
		return a.findLabel(mb.Account, label) != nil
	}
	for _, f := range a.foldersFor(mb.Account) {
		if f.Name == mb.Folder {
			return true
		}
	}
	return false
}

// smartFolderGone reports whether the smart mailbox on show reads a list
// that is no longer there.
func (a *app) smartFolderGone() bool {
	mb := a.shownSmart()
	return mb != nil && !a.smartFolderKnown(*mb)
}

// smartWhere says what a smart mailbox's list covers, in the dialog and as
// the entry's description.
func (a *app) smartWhere(mb mail.SmartMailbox) string {
	switch scopeOfName(mb.Scope) {
	case scopeAll:
		return locales.SmartSearchAllAccounts()
	case scopeAccount:
		return locales.SmartSearchAllFolders()
	}
	return locales.SmartSearchIn(a.folderLabelOf(mb.Account, mb.Folder))
}

// smartSection is the sidebar's section of the account's smart mailboxes,
// with the button that saves the search on screen as one.
func (a *app) smartSection(c *ui.Context, t *ui.Theme) {
	if a.active == "" {
		return
	}
	list := a.smarts.list
	ui.SidebarSection(c, locales.SmartMailboxes(), &a.smarts.open, func() {
		if a.smarts.loaded && len(list) == 0 {
			ui.Text(c, locales.NoSmartMailboxesYet()).FontSize(11).TextColor(t.TextMuted).Padding(2, 8, 2, 8)
		}
		for _, mb := range list {
			a.smartItem(c, t, mb)
		}
		a.saveSearchButton(c, t)
	})
}

// smartItem is a smart mailbox's entry in the sidebar: its icon, its name,
// the unread count of what it finds, and its menu.
func (a *app) smartItem(c *ui.Context, t *ui.Theme, mb mail.SmartMailbox) {
	item := ui.SidebarItem(c, smartEntry(mb.ID), listFilterIcon, mb.Name).
		Description(a.smartWhere(mb)).
		PopoverMenu(func(m *ui.Menu) { a.smartActions(m, mb) })
	item.Children(func() {
		switch {
		case a.smarts.del.busy && a.smarts.del.mailbox.ID == mb.ID:
			ui.Spinner(c).Size(12, 12)
		case mb.Unread > 0:
			ui.Badge(c, strconv.Itoa(mb.Unread)).Background(t.Accent).TextColor(t.AccentText)
		}
	})
}

// saveSearchButton saves the search on screen as a smart mailbox.
func (a *app) saveSearchButton(c *ui.Context, t *ui.Theme) {
	b := ui.ButtonBase(c).Label(locales.SaveSearchEllipsis()).Tooltip(locales.SaveSearchTip()).
		Padding(t.Space(1.5), t.Space(2)).Radius(t.Radius).Justify(ui.Start).
		OnClick(func() { a.openSmartForm(nil) })
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, plusIcon).FontSize(16).TextColor(t.TextMuted)
			ui.Text(c, locales.SaveSearchEllipsis()).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// smartActions are a smart mailbox's menu, in the sidebar and in its
// list's header: change its name and search, or delete it.
func (a *app) smartActions(m *ui.Menu, mb mail.SmartMailbox) {
	if m.Item(locales.EditSmartMailboxEllipsis()).Chosen() {
		a.openSmartForm(&mb)
	}
	if m.Item(locales.DeleteSmartMailboxEllipsis()).Disabled(a.smarts.del.busy).Chosen() {
		a.askDeleteSmart(mb)
	}
}

// smartHeaderMenu is the menu of the smart mailbox whose list shows, in
// the list's header, where the keyboard reaches it.
func (a *app) smartHeaderMenu(c *ui.Context, mb mail.SmartMailbox) {
	label := locales.SmartMailboxActions(mb.Name)
	ui.Button(c, "").Label(label).Tooltip(label).Padding(barPad, barPad).Radius(barButton).
		Children(func() { ui.Icon(c, moreIcon).FontSize(16) }).
		PopoverMenuButton(func(m *ui.Menu) { a.smartActions(m, mb) })
}

// smartForm is the dialog that saves a search as a smart mailbox, or
// changes the name and search of one.
type smartForm struct {
	open    bool
	account string
	// editing is the mailbox being changed, 0 for a new one; folder and
	// scope are where its list is, which changing it keeps.
	editing int64
	folder  string
	scope   string
	name    string
	query   string
	err     string
	busy    bool
	// seq tells the dialog opened from those before it.
	seq uint64
}

// openSmartForm opens the dialog of a new smart mailbox, of the search on
// screen, or of one to change.
func (a *app) openSmartForm(mb *mail.SmartMailbox) {
	if a.active == "" {
		return
	}
	f := smartForm{open: true, account: a.active, seq: a.smarts.form.seq + 1}
	if mb != nil {
		f.editing, f.folder, f.scope = mb.ID, mb.Folder, mb.Scope
		f.name, f.query = mb.Name, mb.Query
	} else {
		f.query = strings.TrimSpace(a.query)
		f.folder = a.smartSaveFolder()
		f.scope = scopeName(a.searchScope())
	}
	a.smarts.form = f
}

// smartSaveFolder is the list a search on screen is saved from: the folder
// on show, or the folder of the smart mailbox on show, never that mailbox
// itself.
func (a *app) smartSaveFolder() string {
	if mb := a.shownSmart(); mb != nil {
		return mb.Folder
	}
	return a.folder
}

// saveSmartForm saves the smart mailbox of the dialog, or changes it. What
// the engine refuses is said in the dialog, which stays open; one saved
// opens, so that the search on screen shows as the mailbox it now is.
func (a *app) saveSmartForm() {
	f := &a.smarts.form
	if !f.open || f.busy || strings.TrimSpace(f.name) == "" {
		return
	}
	f.busy, f.err = true, ""
	seq := f.seq
	mb := mail.SmartMailbox{ID: f.editing, Account: f.account, Name: f.name,
		Folder: f.folder, Query: f.query, Scope: f.scope}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		saved, err := a.svc.SaveSmartMailbox(ctx, mb)
		a.post(func() {
			current := f.seq == seq
			if current {
				f.busy = false
			}
			if err != nil {
				if current {
					f.err = smartFormError(err, mb.Name)
				}
				return
			}
			if current && f.open {
				f.open = false
			}
			a.putSmart(saved)
			a.folder = smartEntry(saved.ID)
			a.selectFolder()
			a.loadSmarts(a.gen)
		})
	})
}

// putSmart keeps the window's list of smart mailboxes in step with one
// saved, before the sidebar is read again.
func (a *app) putSmart(mb mail.SmartMailbox) {
	if mb.Account != a.active {
		return
	}
	for i := range a.smarts.list {
		if a.smarts.list[i].ID == mb.ID {
			a.smarts.list[i] = mb
			return
		}
	}
	a.smarts.list = append(a.smarts.list, mb)
	a.smarts.loaded = true
}

// smartFormError says why the engine refused a smart mailbox's name or
// search.
func smartFormError(err error, name string) string {
	switch {
	case errors.Is(err, mail.ErrSmartExists):
		return locales.SmartMailboxExists(strings.TrimSpace(name))
	case errors.Is(err, mail.ErrSmartName):
		return locales.SmartMailboxNameInvalid(mail.MaxSmartName)
	case errors.Is(err, mail.ErrSmartQuery):
		return locales.SmartMailboxQueryLong(mail.MaxSmartQuery)
	}
	return locales.SmartMailboxSaveFailed(err.Error())
}

// smartDialog is the dialog of a smart mailbox saved or changed: its name,
// its search, and what it covers.
func (a *app) smartDialog(c *ui.Context) {
	f := &a.smarts.form
	if !f.open {
		return
	}
	t := c.Theme()
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(14).Children(func() {
			title, save := locales.SaveSearchTitle(), locales.SaveSmartMailbox()
			if f.editing != 0 {
				title, save = locales.EditSmartMailbox(), locales.Save()
			}
			ui.Text(c, title).FontSize(17).Bold()
			var name ui.Element
			ui.Column(c).Gap(4).Children(func() {
				// The caption names the input, so a click on it focuses it.
				caption := ui.Text(c, locales.SmartMailboxNameCaption()).FontSize(11).Bold().TextColor(t.TextMuted)
				name = ui.TextInput(c, &f.name).LabelledBy(caption).Placeholder(locales.SmartMailboxNamePlaceholder()).
					FillWidth().AutoFocus().MaxLength(mail.MaxSmartName).Invalid(f.err != "").OnSubmit(a.saveSmartForm)
			})
			ui.Column(c).Gap(4).Children(func() {
				caption := ui.Text(c, locales.SmartSearchCaption()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.TextInput(c, &f.query).LabelledBy(caption).Placeholder(locales.SmartSearchPlaceholder()).
					FillWidth().MaxLength(mail.MaxSmartQuery).Invalid(f.err != "").OnSubmit(a.saveSmartForm)
			})
			ui.Text(c, a.smartWhere(mail.SmartMailbox{Account: f.account, Folder: f.folder, Scope: f.scope})).
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
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
				ui.Button(c, locales.Cancel()).Label(locales.CancelSmartForm()).OnClick(func() { f.open = false })
				ui.PrimaryButton(c, save).Disabled(f.busy || strings.TrimSpace(f.name) == "").OnClick(a.saveSmartForm)
			})
		})
	})
}

// smartDeletion is the question before a smart mailbox is deleted, and
// the deletion running.
type smartDeletion struct {
	asking  bool
	mailbox mail.SmartMailbox
	busy    bool
}

// askDeleteSmart asks before deleting a smart mailbox.
func (a *app) askDeleteSmart(mb mail.SmartMailbox) {
	d := &a.smarts.del
	d.mailbox, d.asking = mb, true
}

// deleteSmart deletes a smart mailbox the user said to delete. It touches
// no mail: the mailbox is a read of the cache.
func (a *app) deleteSmart(mb mail.SmartMailbox) {
	d := &a.smarts.del
	if d.busy {
		return
	}
	d.busy, d.mailbox = true, mb
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.DeleteSmartMailbox(ctx, mb.Account, mb.ID)
		a.post(func() {
			d.busy = false
			if err != nil {
				a.err = locales.SmartMailboxDeleteFailed(mb.Name, failureText(err))
			}
			a.loadSmarts(a.gen)
		})
	})
}

// smartAlerts are the questions about deleting a smart mailbox.
func (a *app) smartAlerts(c *ui.Context) {
	d := &a.smarts.del
	if ui.AlertDialog(c, &d.asking, locales.DeleteSmartMailboxTitle(d.mailbox.Name), locales.DeleteSmartMailboxMessage(),
		locales.Cancel(), locales.DeleteSmartMailboxButton()) == 1 {
		a.deleteSmart(d.mailbox)
	}
}

package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// sidebarHead is the top of the sidebar, under the window controls: the
// sync, the menu of the app's screens beside the mail, and the button that
// hides the sidebar. They are flat, as a sidebar's are: glass there would
// show the window's material through it, which it cannot see.
func (a *app) sidebarHead(c *ui.Context, head float32) {
	ui.Row(c).Height(head).Shrink(0).AlignItems(ui.Center).Padding(0, 8, 0, 8).
		DragWindow().Children(func() {
		ui.Spacer(c)
		ui.Themed(c, barTheme(c.Theme())).Background(ui.Transparent).Shrink(0).Children(func() {
			ui.Toolbar(c, func() {
				a.iconButton(c, refreshIcon, locales.Sync(), locales.SyncMail()).
					OnClick(func() { a.refresh() })
				a.moreMenuButton(c)
				a.sidebarToggleButton(c)
			}).Gap(1).Padding(0).Label(locales.MailboxActions())
		})
	})
}

// moreMenuButton is the sidebar's overflow: contacts, rules and settings
// are screens of the app beside the mail, shown when they are wanted, so
// a button each at the top of the sidebar only crowded the mail it lists.
func (a *app) moreMenuButton(c *ui.Context) ui.Element {
	return a.iconButton(c, moreIcon, locales.More(), locales.More()).
		PopoverMenuButton(func(m *ui.Menu) {
			if m.Item(locales.Contacts()).Chosen() {
				a.openContacts()
			}
			if m.Item(locales.Rules()).Chosen() {
				a.openRules(rulesTabList)
			}
			m.Separator()
			if m.Item(locales.Settings()).Chosen() {
				a.openSettings(settingsTabGeneral)
			}
		})
}

// sidebarToggle shows the sidebar, from the top of the list while it is
// hidden.
func (a *app) sidebarToggle(c *ui.Context) {
	a.barCapsule(c, func() ui.Element { return a.sidebarToggleButton(c) })
}

// sidebarToggleButton hides the sidebar or shows it.
func (a *app) sidebarToggleButton(c *ui.Context) ui.Element {
	label := locales.HideSidebar()
	if !a.sidebarOpen {
		label = locales.ShowSidebar()
	}
	return a.iconButton(c, panelLeftIcon, label, label).
		OnClick(func() { a.sidebarOpen = !a.sidebarOpen })
}

// sidebar draws the account switcher and the folders.
func (a *app) sidebar(c *ui.Context, t *ui.Theme, vibrant bool) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
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
			ui.PopoverMenuButton(c, "", func(m *ui.Menu) {
				for _, acc := range a.accounts {
					label := acc.Label()
					item := m.Item(label + " · " + acc.Email).ID(acc.ID).Radio(acc.ID == a.active)
					// The sidebar shows the active account's unread mail;
					// the other accounts' is said here, and so is that one
					// has to sign in again, which its banner says when it
					// is on screen.
					if n := a.inboxes[acc.ID]; n > 0 && acc.ID != a.active {
						item.Trailing(fmt.Sprint(n))
					}
					if a.needSignIn[acc.ID] && acc.ID != a.active {
						item.Subtitle(locales.MenuNeedsSignIn())
					}
					if item.Chosen() {
						a.aim = nil
						a.switchAccount(acc.ID)
					}
				}
				m.Separator()
				if m.Item(locales.NewFolder()).Chosen() {
					a.openNewFolder("")
				}
				if m.Item(locales.ShortcutsTitle()).Chosen() {
					a.shortcutsOpen = true
				}
				var hidden []string
				for _, f := range a.folders {
					if !f.Subscribed && !f.IsSystem() {
						hidden = append(hidden, f.Name)
					}
				}
				if len(hidden) > 0 {
					m.Submenu(locales.HiddenFolders(), func(m *ui.Menu) {
						for _, name := range hidden {
							if m.Item(name).Chosen() {
								a.setFolderSubscribed(name, true)
							}
						}
					})
				}
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
					a.openSettings(settingsTabGeneral)
				}
			}).Label(locales.AccountsMenu()).Tooltip(locales.AccountsMenu())
		})

		entries := a.folderEntries()
		changed := ui.Sidebar(c, &a.folder, func() {
			if len(a.accounts) > 1 {
				item := ui.SidebarItem(c, allInboxes, mailsIcon, locales.AllInboxes())
				if n := a.unifiedUnread(); n > 0 {
					item.Children(func() {
						a.sidebarCount(c, "side/"+allInboxes, n)
					})
				} else {
					a.noteCount("side/"+allInboxes, n)
				}
			}
			if item := ui.SidebarItem(c, remindersFolder, clockIcon, locales.Reminders()); len(a.reminders) > 0 {
				item.Children(func() {
					a.sidebarCount(c, "side/"+remindersFolder, len(a.reminders))
				})
			} else {
				a.noteCount("side/"+remindersFolder, 0)
			}
			if item := ui.SidebarItem(c, followUpFolder, replyIcon, locales.FollowUp()); a.followUps > 0 {
				item.Children(func() {
					a.sidebarCount(c, "side/"+followUpFolder, a.followUps)
				})
			} else {
				a.noteCount("side/"+followUpFolder, 0)
			}
			ui.SidebarSection(c, locales.Mailboxes(), &a.mailboxesOpen, func() {
				for _, e := range entries {
					item := ui.SidebarItem(c, e.ID, e.Icon, e.Label)
					if e.ID != "starred" && e.ID != draftsFolder {
						if n := a.folderUnread(e.ID); n > 0 {
							item.Children(func() {
								a.sidebarCount(c, "side/"+e.ID, n)
							})
						} else {
							a.noteCount("side/"+e.ID, n)
						}
					}
					if f := a.folderByName(e.ID); f != nil {
						name := f.Name
						item.ContextMenu(func(m *ui.Menu) { a.folderMenu(m, name, f.IsSystem()) })
						if _, over := ui.DragOver[*folderDrop](item); over {
							item.Background(t.Accent.Alpha(0.16))
						}
						if drop, ok := ui.Drop[*folderDrop](item); ok {
							a.dropOnFolder(drop, name)
						}
					}
				}
			})
			a.labelsSection(c, t)
			a.smartSection(c, t)
		}).Grow(1).Label(locales.Mailboxes())
		if vibrant {
			changed.Background(ui.Transparent)
		}
		if changed.Changed() {
			a.aim = nil
			a.selectFolder()
		}
		a.sidebarFooter(c, t)
	})
}

// sidebarFooter shows the account's cached totals and whether a sync is
// running, as mail apps keep their quota at the bottom, and under them
// how receiving mail in the background is going.
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
	// Drafts always shows: it holds the drafts on this device, whether or
	// not the server has a Drafts folder. The server's folder is listed
	// inside it, so it gets no entry of its own.
	entries = append(entries, folderEntry{ID: draftsFolder, Label: locales.Drafts(), Icon: draftIcon})
	seen[a.folderBySpecial(mail.SpecialDrafts)] = true
	add(mail.SpecialArchive, archiveIcon, locales.Archive())
	add(mail.SpecialJunk, spamIcon, locales.Junk())
	add(mail.SpecialTrash, trashIcon, locales.Trash())
	for _, f := range a.folders {
		// A folder named as an entry above would be chosen with it.
		if seen[f.Name] || f.SpecialUse == mail.SpecialFlagged || f.Name == draftsFolder {
			continue
		}
		// A folder the user hid stays out of the sidebar, unless it is
		// the one on screen, so that hiding it does not close it.
		if !f.Subscribed && f.Name != a.folder {
			continue
		}
		seen[f.Name] = true
		entries = append(entries, folderEntry{ID: f.Name, Label: folderLeaf(f), Icon: tagIcon})
	}
	return entries
}

func (a *app) sidebarFooter(c *ui.Context, t *ui.Theme) {
	unread, total := a.sidebarTotals()
	ui.Column(c).Shrink(0).Padding(10, 12, 10, 12).Gap(4).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.loading {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, locales.Syncing()).FontSize(11).TextColor(t.TextMuted)
			})
		} else {
			ui.Text(c, locales.SidebarSummary(unread, total)).FontSize(11).TextColor(t.TextMuted)
		}
		a.watchLine(c, t)
		a.queueLine(c, t)
	})
}

// sidebarTotals counts the account's cached folders, without the ones
// whose mail is already dealt with.
func (a *app) sidebarTotals() (unread, total int) {
	for _, f := range a.folders {
		switch f.SpecialUse {
		case mail.SpecialJunk, mail.SpecialTrash, mail.SpecialDrafts:
			continue
		}
		unread += f.Unread
		total += f.Total
	}
	return unread, total
}

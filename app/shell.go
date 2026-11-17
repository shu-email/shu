package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The window has no sidebar. It is one stream of mail with one field
// above it: the field is where a mail app's folder tree used to be,
// because the user's question is rarely "which folder" — it is "what
// needs me", or a name, or a sentence. Everything else the window used to
// keep in a rail is a command in the palette, a key, or a line at the
// bottom.
//
// Reading opens over the stream rather than beside it: the column the
// stream held is the column what is read takes, and mail is one thing at
// a time. A list that keeps its width is a list the user keeps their
// place in, and a conversation that takes the whole column is one worth
// reading. Choosing a row opens it; going back, and moving, deleting or
// archiving what is open, gives the column back to the stream.

const (
	// shellMeasure is the width of the stream's column, and of what is
	// read. A mail list read at the window's width is a table, but the
	// rows' previews and chips want room to say what the mail asks of
	// the user; one measure for both keeps the column still as reading
	// opens.
	shellMeasure = 860
	// shellBarHeight is the top bar's height, and the least the window's
	// controls need above it.
	shellBarHeight = 56
	// shellStatusHeight is the line at the bottom that says how receiving
	// mail and the queue are going.
	shellStatusHeight = 30
)

// shellReading reports whether the stream's column is reading: a row is
// chosen, and what it opened is still there to read. Anything that takes
// the choice away — another folder, a row that moved — finds the stream
// back in the column.
func (a *app) shellReading() bool {
	return a.readingOpen && a.selected >= 0 && a.selected < len(a.all)
}

// openReading gives the stream's column to what the chosen row reads. It
// is called wherever mail is chosen, and does nothing but in the shell.
func (a *app) openReading() {
	if a.selected >= 0 && a.selected < len(a.all) {
		a.readingOpen = true
	}
}

// closeReading gives the column back to the stream while the choice stays
// where the shared code left it, as after the open conversation moved.
func (a *app) closeReading() {
	a.readingOpen = false
}

// shellBack is Escape and the toolbar's back button: what is read gives
// the column back to the stream, and the choice goes with it.
func (a *app) shellBack() {
	if !a.shellReading() {
		return
	}
	// The reading toolbar's search field, open and empty, takes Escape
	// for itself first; the window's Escape is then closing that field,
	// not the reading.
	if a.searchOpen && strings.TrimSpace(a.query) == "" {
		return
	}
	a.readingOpen = false
	a.clearSelection()
}

// shellView is the window: the bar, the stream, the line under it, and
// the palette over them.
func (a *app) shellView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	head := max(bar.Bottom, shellBarHeight)
	// The window's keys, at the window: the reading place holds no list
	// for its focus subtree to lean on, and a window key runs while the
	// focus is anywhere else, or nowhere. A field the focus is in takes
	// its keys first, and the palette and the dialogs, over the window,
	// take them while they show.
	c.OnShortcut(ui.Cmd, ui.KeyK, a.openPalette)
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() { a.openSettings(settingsTabGeneral) })
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.openCompose(nil) })
	c.OnShortcut(ui.Cmd, ui.KeyR, func() { a.refresh() })
	c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyK, func() { a.showFolder("") })
	c.OnShortcut(0, ui.KeyJ, func() { a.stepMessage(1) })
	c.OnShortcut(0, ui.KeyK, func() { a.stepMessage(-1) })
	c.OnShortcut(0, ui.KeyU, a.toggleRead)
	c.OnShortcut(0, ui.KeyS, a.toggleStar)
	c.OnShortcut(0, ui.KeyE, func() { a.moveSelected(mail.SpecialArchive) })
	c.OnShortcut(0, ui.KeyR, func() { a.reply(a.replyOrder()[0]) })
	c.OnShortcut(ui.Shift, ui.KeyR, func() { a.reply(a.replyOrder()[1]) })
	c.OnShortcut(0, ui.KeyF, a.forward)
	c.OnShortcut(0, ui.KeyDelete, a.deleteSelected)
	c.OnShortcut(0, ui.KeyL, a.openLabelPicker)
	c.OnShortcut(0, ui.KeyEscape, a.shellBack)
	ui.Column(c.Key("shell")).Fill().Background(t.Background).Children(func() {
		a.shellBar(c, t, bar, head)
		a.shellStream(c, t)
		a.shellStatus(c, t)
	})

	a.paletteView(c, t)
	// The dialogs the mail window keeps: none of them is chrome, all of
	// them are what an action opens.
	a.accountModal(c)
	a.labelDialog(c)
	a.labelAlerts(c)
	a.smartDialog(c)
	a.smartAlerts(c)
	aiReviewModal(c, &a.review)
	a.linkDialog(c)
	a.unsubscribeDialog(c)
	a.blockSenderDialog(c)
	a.reminderDialog(c)
	a.attachmentPreviewDialog(c)
	a.shortcutsDialog(c)
}

// shellBar is the top of the window: the one field, and the three things
// a user reaches for without leaving the stream.
func (a *app) shellBar(c *ui.Context, t *ui.Theme, bar ui.TitleBar, head float32) {
	ui.Row(c.Key("shell-bar")).Height(head).Shrink(0).AlignItems(ui.Center).
		Padding(0, 14, 0, 14+max(bar.Left, 0)).Gap(10).DragWindow().Children(func() {
		a.shellAccount(c, t)
		// The field is a button: it opens the palette, where the keys go
		// and what is typed is run. It is not an input of its own, so that
		// the window's focus stays on the mail.
		width := float32(min(shellMeasure, 520))
		ui.ButtonBase(c.Key("shell/ask")).Label(locales.AskMailPlaceholder()).
			Width(width).Height(32).Radius(999).Padding(0, 12).
			Background(t.Surface).Border(1, t.Border).
			Tooltip(locales.AskMailPlaceholder()).
			OnClick(a.openPalette).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Icon(c, searchIcon).FontSize(13).TextColor(t.TextMuted)
				ui.Text(c, locales.AskMailPlaceholder()).FontSize(13).TextColor(t.TextMuted).
					Grow(1).MinWidth(0).SingleLine()
				ui.Text(c, "⌘K").FontSize(10).TextColor(t.TextMuted.Alpha(0.7)).Shrink(0)
			})
		})
		ui.Spacer(c)
		a.shellState(c, t)
		a.iconButton(c, refreshIcon, locales.Sync(), locales.SyncMail()).
			OnClick(func() { a.refresh() })
		a.iconButton(c, composeIcon, locales.Compose(), locales.NewMessage()).
			OnClick(func() { a.openCompose(nil) })
		a.overflowMenu(c)
	})
}

// shellAccount is who the window sends as: the account's picture, and the
// menu that switches it or adds another.
func (a *app) shellAccount(c *ui.Context, t *ui.Theme) {
	acc := a.activeAccount()
	name, email := "", ""
	if acc != nil {
		name, email = acc.Label(), acc.Email
	}
	if name == "" {
		name = locales.AccountsTitle()
	}
	ui.Button(c.Key("shell/account"), "").Label(locales.AccountsMenu()).Tooltip(locales.AccountsMenu()).
		Padding(2).Radius(999).PopoverMenuButton(func(m *ui.Menu) {
		for _, other := range a.accounts {
			item := m.Item(other.Label() + " · " + other.Email).ID(other.ID).Radio(other.ID == a.active)
			if n := a.inboxes[other.ID]; n > 0 && other.ID != a.active {
				item.Trailing(countLabel(n))
			}
			if a.needSignIn[other.ID] && other.ID != a.active {
				item.Subtitle(locales.MenuNeedsSignIn())
			}
			if item.Chosen() {
				a.aim = nil
				a.switchAccount(other.ID)
			}
		}
		m.Separator()
		if m.Item(locales.AddAccount()).Chosen() {
			a.openAccountForm(nil)
		}
		if m.Item(locales.ManageAccounts()).Chosen() {
			if acc != nil {
				a.openAccountForm(acc)
			} else {
				a.openAccountForm(nil)
			}
		}
	}).Children(func() {
		ui.Avatar(c, name, a.senderAvatar(mail.Address{Email: email}))
	})
}

// shellState is the dot that says receiving mail is going well, or the
// word that says it is not.
func (a *app) shellState(c *ui.Context, t *ui.Theme) {
	status, ok := a.watch[a.active]
	if !ok {
		return
	}
	color, label := t.Success, locales.WatchUpToDate()
	switch {
	case a.needSignIn[a.active] || status.State == mail.WatchSignIn:
		color, label = t.Warning, locales.WatchSignIn()
	case status.State == mail.WatchOffline:
		color, label = t.Warning, locales.WatchOffline()
	case status.State == mail.WatchRetrying:
		color, label = t.Warning, locales.WatchRetryOther()
	case status.State != mail.WatchUpToDate:
		color, label = t.TextMuted, locales.WatchConnecting()
	}
	ui.Box(c).Size(7, 7).Radius(4).Shrink(0).Background(color).Label(label).Tooltip(label)
}

// mailTotals counts the account's cached folders, without the ones whose
// mail is already dealt with.
func (a *app) mailTotals() (unread, total int) {
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

// shellStream is the middle of the window: the mail, in a column of a
// readable width, in the middle of the window. The column is the stream's
// until a row is chosen to be read, and what is read then takes it whole —
// the same column, at the same width, so the window keeps its place. The
// column holds its width and the row centers it: a Grow here would share
// the window out between the growers, and the column would end a third of
// the window wide.
func (a *app) shellStream(c *ui.Context, t *ui.Theme) {
	ui.Row(c.Key("shell-stream")).Grow(1).MinHeight(0).AlignItems(ui.Stretch).
		Justify(ui.Center).Children(func() {
		if a.shellReading() {
			ui.Column(c.Key("stream-reading")).Width(shellMeasure).MinWidth(0).
				MinHeight(0).ClipY().Transition(readingMotion).Children(func() {
				a.readingPane(c, t, shellBarHeight)
			})
			return
		}
		ui.Column(c.Key("stream-list")).Width(shellMeasure).MinWidth(0).
			MinHeight(0).ClipY().Transition(readingMotion).Children(func() {
			a.shellHead(c, t)
			a.listBody(c, t)
		})
	})
}

// shellHead is what the stream shows: where it is, what it holds, and the
// two switches a list has.
func (a *app) shellHead(c *ui.Context, t *ui.Theme) {
	title := a.folderLabel(a.folder)
	if a.streaming() {
		title = locales.Stream()
	}
	if strings.TrimSpace(a.query) != "" && a.shownSmart() == nil {
		title = locales.SearchResults()
	}
	switch n := a.sel.Len(); {
	case n > 1 && a.threaded():
		title = locales.ConversationsSelected(n)
	case n > 1:
		title = locales.SelectedCount(n)
	}
	ui.Column(c.Key("shell-head")).Shrink(0).Padding(6, 4).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(22).Bold().SingleLine().Shrink(1).MinWidth(0)
			if n, more := a.unread(), a.pager.more; n > 0 {
				label := countLabel(n)
				if more {
					label += "+"
				}
				a.countBadge(c, "unread/"+a.folder, label, n).
					Background(t.Accent).TextColor(t.AccentText)
			}
			ui.Spacer(c)
			if strings.TrimSpace(a.query) != "" && a.shownSmart() == nil {
				ui.Button(c, locales.SaveSearchEllipsis()).Tooltip(locales.SaveSearchTip()).
					Padding(barPad, 10).Radius(barButton).OnClick(func() { a.openSmartForm(nil) })
			}
			if n := a.sel.Len(); n > 1 {
				ui.Button(c, locales.ClearSelection()).Padding(barPad, 12).
					Radius(barButton).OnClick(func() { a.clearSelection() })
				return
			}
			if l := a.shownLabel(); l != nil {
				a.labelHeaderMenu(c, *l)
			}
			if mb := a.shownSmart(); mb != nil {
				a.smartHeaderMenu(c, *mb)
			}
			// A draft is the user's own text, so the stream's head carries
			// the way into it: the reading pane's banner is under the sheet
			// a narrow window may fold away.
			if a.draftsList() && a.selected >= 0 && a.selected < len(a.all) {
				ui.Button(c, locales.EditDraft()).Padding(barPad, 10).Radius(barButton).
					OnClick(a.editDraft)
			}
			a.viewToggle(c)
			if a.ai != nil && !a.draftsList() && len(a.all) > 0 {
				ui.Button(c, "").Label(locales.TriageInbox()).Tooltip(locales.TriageInbox()).
					Padding(barPad, barPad).Radius(barButton).OnClick(a.triageList).
					Children(func() { ui.Icon(c, sparklesIcon).FontSize(15) })
			}
		})
		ui.Text(c, a.listSummary()).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
}

// overflowMenu is the window's ⋯: the commands a session reaches for
// that the bar does not keep, and the accounts, and the app's screens.
func (a *app) overflowMenu(c *ui.Context) ui.Element {
	return a.iconButton(c, moreIcon, locales.More(), locales.More()).
		PopoverMenuButton(func(m *ui.Menu) {
			if m.Item(locales.SyncMail()).Chosen() {
				a.refresh()
			}
			if m.Item(locales.Compose()).Chosen() {
				a.openCompose(nil)
			}
			m.Separator()
			if m.Item(locales.Contacts()).Chosen() {
				a.openContacts()
			}
			if m.Item(locales.Rules()).Chosen() {
				a.openRules(rulesTabList)
			}
			if m.Item(locales.Settings()).Chosen() {
				a.openSettings(settingsTabGeneral)
			}
			m.Separator()
			if acc := a.activeAccount(); acc != nil {
				for _, other := range a.accounts {
					if other.ID == a.active {
						continue
					}
					if m.Item(other.Label() + " · " + other.Email).Chosen() {
						a.aim = nil
						a.switchAccount(other.ID)
					}
				}
				if m.Item(locales.AddAccount()).Chosen() {
					a.openAccountForm(nil)
				}
				if m.Item(locales.ManageAccounts()).Chosen() {
					a.openAccountForm(acc)
				}
			}
		})
}

// mailTotals counts the account's cached folders, without the ones whose
// mail is already dealt with.

// shellStatus is the line under the stream: how receiving mail and the
// queue are going, and what the account holds.
func (a *app) shellStatus(c *ui.Context, t *ui.Theme) {
	ui.Row(c.Key("shell-status")).Height(shellStatusHeight).Shrink(0).
		Padding(0, 16).Gap(10).AlignItems(ui.Center).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border.Alpha(0.6)).Children(func() {
		a.watchLine(c, t)
		a.queueLine(c, t)
		ui.Spacer(c)
		unread, total := a.mailTotals()
		ui.Text(c, locales.SidebarSummary(unread, total)).FontSize(10.5).
			TextColor(t.TextMuted).SingleLine().Shrink(0)
		if acc := a.activeAccount(); acc != nil {
			ui.Text(c, "·").FontSize(10.5).TextColor(t.TextMuted.Alpha(0.6))
			ui.Text(c, acc.Label()).FontSize(10.5).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
	})
}

// countLabel is a count as a label, without importing fmt for one call.
func countLabel(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

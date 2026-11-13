package main

import (
	"runtime"

	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// The keyboard reference: every shortcut the mail window takes, in the
// groups the window uses them, reachable with Cmd+/ or the account menu.
// Plain keys live in the panes' focus subtree, so typing never takes
// them; the reference says so.

// shortcutRow is one entry: the keys as the system writes them, and what
// they do.
type shortcutRow struct {
	keys  string
	label string
}

type shortcutGroup struct {
	title string
	rows  []shortcutRow
}

// shortcuts lists the implemented shortcuts. Keep it in step with the
// OnShortcut calls in the window.
func shortcuts() []shortcutGroup {
	cmd := primaryKey()
	return []shortcutGroup{
		{locales.ShortcutsReading(), []shortcutRow{
			{"J", locales.ShortcutNext()},
			{"K", locales.ShortcutPrevious()},
			{"U", locales.ShortcutMarkRead()},
			{"S", locales.ShortcutStar()},
			{"E", locales.ShortcutArchive()},
			{"R", locales.ShortcutReply()},
			{shiftKey() + "R", locales.ShortcutReplyAll()},
			{"F", locales.ShortcutForward()},
			{deleteKey(), locales.ShortcutDelete()},
			{"L", locales.ShortcutLabels()},
		}},
		{locales.ShortcutsWriting(), []shortcutRow{
			{cmd + "N", locales.ShortcutCompose()},
			{cmd + enterKey(), locales.ShortcutSend()},
			{cmd + shiftKey() + "A", locales.ShortcutAttach()},
		}},
		{locales.ShortcutsApp(), []shortcutRow{
			{cmd + "K", locales.ShortcutSearch()},
			{cmd + "R", locales.ShortcutSync()},
			{cmd + ",", locales.ShortcutSettings()},
			{cmd + "/", locales.ShortcutShortcuts()},
			{"Esc", locales.ShortcutClose()},
		}},
	}
}

func primaryKey() string {
	if runtime.GOOS == "darwin" {
		return "⌘"
	}
	return "Ctrl+"
}

func shiftKey() string {
	if runtime.GOOS == "darwin" {
		return "⇧"
	}
	return "Shift+"
}

func enterKey() string {
	if runtime.GOOS == "darwin" {
		return "↵"
	}
	return "Enter"
}

func deleteKey() string {
	if runtime.GOOS == "darwin" {
		return "⌫"
	}
	return "Del"
}

// shortcutsDialog draws the keyboard reference.
func (a *app) shortcutsDialog(c *ui.Context) {
	if !a.shortcutsOpen {
		return
	}
	t := c.Theme()
	ui.Modal(c, &a.shortcutsOpen, func() {
		ui.Column(c).Width(560).Gap(14).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.ShortcutsTitle()).FontSize(17).Bold().Grow(1)
				a.iconButton(c, closeIcon, locales.Cancel(), locales.Cancel()).OnClick(func() { a.shortcutsOpen = false })
			})
			ui.Text(c, locales.ShortcutsIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			ui.Scroll(c).Height(460).Children(func() {
				ui.Column(c).Gap(14).PaddingEnd(8).Children(func() {
					for _, group := range shortcuts() {
						ui.Column(c).Gap(6).Children(func() {
							ui.Text(c, group.title).FontSize(12).Bold().TextColor(t.TextMuted)
							for _, row := range group.rows {
								ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
									ui.Box(c).Shrink(0).MinWidth(74).Padding(3, 7).Radius(5).
										Background(t.Surface).Border(1, t.Border).Children(func() {
										ui.Text(c, row.keys).FontSize(11).Bold().TextColor(t.Text)
									})
									ui.Text(c, row.label).FontSize(12).Grow(1).MinWidth(0)
								})
							}
						})
					}
				})
			})
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				ui.PrimaryButton(c, locales.Done()).OnClick(func() { a.shortcutsOpen = false })
			})
		})
	})
}

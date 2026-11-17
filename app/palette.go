package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// The command palette is the window's one way to change what it shows:
// where a mail app used to keep a tree of folders, Shu keeps a field. It
// opens with the keys, or with the dot in the top bar, lists the ways into
// the mail, the account's labels and pinned searches, and the app's
// screens, and runs what is typed as a search when nothing matches by
// name. Nothing it lists is a place on a server: a folder is where mail
// sits, not a view the user keeps.

// paletteState is the palette as it shows.
type paletteState struct {
	open   bool
	query  string
	chosen int
	// entries are the rows the last build showed, filtered by the query,
	// which Enter takes.
	entries []paletteEntry
}

// paletteEntry is one row of the palette.
type paletteEntry struct {
	ID    string
	Label string
	// Hint says what kind of thing the row is, in the user's words: a
	// list, a label, a pinned search, a screen.
	Hint string
	Icon *ui.SVG
	Run  func()
}

// openPalette shows the palette with its query cleared.
func (a *app) openPalette() {
	a.palette = paletteState{open: true}
}

// closePalette hides it.
func (a *app) closePalette() { a.palette.open = false }

// paletteEntries are every row the palette can show, in the order it
// shows them: the ways into the mail, then the account's labels, then the
// pinned searches, then the app's screens.
func (a *app) paletteEntries() []paletteEntry {
	entries := make([]paletteEntry, 0, len(a.navEntries())+len(a.smarts.list)+12)
	for _, e := range a.navEntries() {
		id := e.ID
		entries = append(entries, paletteEntry{
			ID: id, Label: e.Label, Hint: locales.PaletteList(), Icon: e.Icon,
			Run: func() { a.showFolder(id) },
		})
	}
	for _, l := range a.labels.of[a.active] {
		id := labelEntry(l.ID)
		entries = append(entries, paletteEntry{
			ID: id, Label: l.Name, Hint: locales.PaletteLabel(), Icon: tagIcon,
			Run: func() { a.showFolder(id) },
		})
	}
	for _, mb := range a.smarts.list {
		id := smartEntry(mb.ID)
		entries = append(entries, paletteEntry{
			ID: id, Label: mb.Name, Hint: locales.PalettePinned(), Icon: listFilterIcon,
			Run: func() { a.showFolder(id) },
		})
	}
	entries = append(entries,
		paletteEntry{ID: "compose", Label: locales.Compose(), Hint: locales.PaletteCommand(), Icon: composeIcon,
			Run: func() { a.openCompose(nil) }},
		paletteEntry{ID: "settings", Label: locales.Settings(), Hint: locales.PaletteCommand(), Icon: settingsIcon,
			Run: func() { a.openSettings(settingsTabGeneral) }},
		paletteEntry{ID: "rules", Label: locales.Rules(), Hint: locales.PaletteCommand(), Icon: filterIcon,
			Run: func() { a.openRules(rulesTabList) }},
		paletteEntry{ID: "contacts", Label: locales.Contacts(), Hint: locales.PaletteCommand(), Icon: mailIcon,
			Run: func() { a.openContacts() }},
		paletteEntry{ID: "sync", Label: locales.SyncMail(), Hint: locales.PaletteCommand(), Icon: refreshIcon,
			Run: func() { a.refresh() }},
		paletteEntry{ID: "threads", Label: locales.GroupByConversation(), Hint: locales.PaletteCommand(), Icon: conversationsIcon,
			Run: func() { a.toggleThreaded() }},
	)
	if a.ai != nil {
		entries = append(entries, paletteEntry{ID: "topics", Label: locales.GroupByTopic(),
			Hint: locales.PaletteCommand(), Icon: sparklesIcon, Run: a.groupTopics})
	}
	return entries
}

// filtered are the palette's rows for the query: those whose label holds
// what is typed, whatever its case. A query that names nothing by itself
// is offered as a search of the list on screen.
func (a *app) filteredPalette() []paletteEntry {
	q := strings.ToLower(strings.TrimSpace(a.palette.query))
	all := a.paletteEntries()
	if q == "" {
		return all
	}
	out := make([]paletteEntry, 0, len(all))
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Label), q) {
			out = append(out, e)
		}
	}
	return out
}

// paletteKey is the keys the palette takes: Enter chooses the row, the
// arrows move by one, Escape closes it.
func (a *app) paletteKeys(c *ui.Context) {
	c.OnShortcut(ui.Cmd, ui.KeyK, a.openPalette)
	c.OnShortcut(0, ui.KeyEscape, a.closePalette)
}

// palette draws the palette over the window while it is open.
func (a *app) paletteView(c *ui.Context, t *ui.Theme) {
	p := &a.palette
	if !p.open {
		return
	}
	p.entries = a.filteredPalette()
	if p.chosen >= len(p.entries) {
		p.chosen = max(len(p.entries)-1, 0)
	}
	ui.Modal(c, &p.open, func() {
		ui.Column(c).Width(620).Gap(10).Children(func() {
			field := ui.TextInput(c, &p.query).
				Placeholder(locales.AskMailPlaceholder()).
				FillWidth().AutoFocus().
				Label(locales.AskMailPlaceholder()).
				OnSubmit(func() {
					if len(p.entries) > 0 {
						p.entries[p.chosen].Run()
						p.open = false
					}
				})
			field.Label(locales.AskMailPlaceholder())
			ui.Column(c).MaxHeight(420).Shrink(0).Children(func() {
				if len(p.entries) == 0 {
					ui.Text(c, locales.PaletteNothing()).FontSize(12).TextColor(t.TextMuted).Padding(4, 6)
					if strings.TrimSpace(p.query) != "" {
						a.paletteSearchRow(c, t)
					}
					return
				}
				for i, e := range p.entries {
					a.paletteRow(c, t, i, e)
				}
			})
		})
	})
}

// paletteRow is one row of the palette: what it does, what kind of thing
// it is, and the icon of its kind. The row the query first matches draws
// as chosen, which is what Enter takes.
func (a *app) paletteRow(c *ui.Context, t *ui.Theme, i int, e paletteEntry) {
	chosen := i == a.palette.chosen
	b := ui.ButtonBase(c.Key("palette/"+e.ID)).Label(e.Label).
		Padding(7, 10).Radius(t.Radius).Gap(10).AlignItems(ui.Center).Justify(ui.Start)
	switch {
	case chosen:
		b.Background(t.Accent.Alpha(0.12).Over(t.Background))
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	b.OnClick(func() {
		e.Run()
		a.palette.open = false
	})
	b.Children(func() {
		if e.Icon != nil {
			ui.Icon(c, e.Icon).FontSize(14).TextColor(t.TextMuted)
		}
		ui.Text(c, e.Label).FontSize(13).Grow(1).MinWidth(0).SingleLine()
		ui.Text(c, e.Hint).FontSize(11).TextColor(t.TextMuted).Shrink(0).SingleLine()
	})
}

// paletteSearchRow offers what is typed as a search of the list on
// screen, which is what a query that names nothing by itself is for.
func (a *app) paletteSearchRow(c *ui.Context, t *ui.Theme) {
	q := strings.TrimSpace(a.palette.query)
	b := ui.ButtonBase(c.Key("palette/search")).Padding(7, 10).Radius(t.Radius).
		AlignItems(ui.Center).Gap(10).Justify(ui.Start).
		Label(locales.SearchFor(q)).
		OnClick(func() {
			a.palette.open = false
			a.query = q
			a.sel.Clear()
			a.search()
		})
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Icon(c, searchIcon).FontSize(14).TextColor(t.TextMuted)
		ui.Text(c, locales.SearchFor(q)).FontSize(13).Grow(1).MinWidth(0).SingleLine()
	})
}

// showFolder shows one of the lists a.folder names, from the palette.
func (a *app) showFolder(id string) {
	a.aim = nil
	a.folder = id
	a.selectFolder()
}

// toggleThreaded switches the list between conversations and messages.
func (a *app) toggleThreaded() { a.setView(!a.mailSet.Conversations) }

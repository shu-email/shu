package main

import (
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// The bar of the window's lists and reading pane: capsules of glass that
// hold their controls, as the toolbars of macOS 26 and later group theirs,
// floating over what scrolls under them.

// headHeight is the least height of the top of each column, the window
// controls' and the bars' room.
const headHeight float32 = 56

// readingInset is the room the floating bar takes over the top of the
// reading pane: what is read starts below it, and scrolls under it.
const readingInset = headHeight

// The bar's buttons are 32 DIPs square, round, in capsules of 40: a
// radius of half of those, as the glass draws a radius of its own oddly.
const (
	barButton = 16
	barHeight = 40
	barRadius = barHeight / 2
)

// barInset is the room a capsule leaves around its buttons, and barPad
// the room a button keeps around its icon.
const barInset = (barHeight - 2*barButton) / 2

const barPad = 8

// compactReading is the reading pane's width below which the search field
// leaves the actions their room and shows only while in use.
const compactReading float32 = 600

// readingCompact reports whether the reading pane is too narrow to show
// every group of actions beside the search.
func (a *app) readingCompact() bool {
	return a.readingWidth > 0 && a.readingWidth < compactReading
}

// readingSearchShown reports whether the bar shows the search field
// itself, rather than the magnifier that opens it.
func (a *app) readingSearchShown() bool {
	return !a.readingCompact() || a.searchOpen || strings.TrimSpace(a.query) != ""
}

// barPalette is the glass of a capsule: its tint, its hairline and its
// shadow. Its values are the one place to tune them.
type barPalette struct {
	tint, border, shadow ui.Color
}

// barPaletteOf returns the palette of the theme's appearance.
func barPaletteOf(t *ui.Theme) barPalette {
	// The border and the shadow keep a capsule's edge whole over the
	// pane's surface, where only the glass's shadowed sides would show.
	if t.Dark {
		return barPalette{
			tint:   ui.RGBA(255, 255, 255, 0.04),
			border: ui.RGBA(255, 255, 255, 0.09),
			shadow: ui.RGBA(0, 0, 0, 0.3),
		}
	}
	return barPalette{
		tint:   ui.RGBA(0, 0, 0, 0.02),
		border: ui.RGBA(0, 0, 0, 0.07),
		shadow: ui.RGBA(0, 0, 0, 0.05),
	}
}

// barTheme is the theme inside a bar: the window's, with the lighter faces
// its controls take over the glass, as the toolbars of macOS do. A
// Themed around them makes the toolbar draw those as it paints, so that a
// hover repaints the bar and not the view.
func barTheme(t *ui.Theme) *ui.Theme {
	bar := *t
	if t.Dark {
		bar.SurfaceHover = ui.RGBA(255, 255, 255, 0.08)
		bar.SurfacePressed = ui.RGBA(255, 255, 255, 0.12)
		return &bar
	}
	bar.SurfaceHover = ui.RGBA(0, 0, 0, 0.03)
	bar.SurfacePressed = ui.RGBA(0, 0, 0, 0.065)
	return &bar
}

// capsule fills e with the bar's glass: round, with a hairline along its
// edge and a shadow that lifts it off what is under it.
func capsule(c *ui.Context, e ui.Element) ui.Element {
	p := barPaletteOf(c.Theme())
	return e.Radius(barRadius).Material(glass.Glass{Tint: p.tint}).
		Border(1, p.border).Shadow(0, 1, 2, 0, p.shadow)
}

// glassGroup is a capsule of glass holding a group of buttons. It is a
// toolbar: its buttons show no face until hovered, Left and Right move
// among them, and those that do not fit go into its overflow menu. label
// names the group; a group of one needs none, its button's label says it.
func (a *app) glassGroup(c *ui.Context, label string, fn func()) ui.Element {
	// Round as the buttons, and as tall as they are: a fixed height would
	// clip them where the theme's padding grows.
	g := capsule(c, ui.Toolbar(c, fn).Padding(barInset, 3).Gap(1).Shrink(1).MinWidth(0))
	if label != "" {
		g.Label(label)
	}
	return g
}

// barCapsule holds one control, in a capsule its own size: its pressed or
// hovered face fills the capsule, as a lone button's glass does.
func (a *app) barCapsule(c *ui.Context, fn func() ui.Element) ui.Element {
	return capsule(c, ui.Toolbar(c, func() {
		fn().Padding(barInset+barPad, barInset+barPad).Radius(barRadius)
	}).Padding(0).Gap(0).Shrink(0))
}

// barMenuButton is a bar button that opens menu: in a narrow pane a
// group of actions folds into it, rather than out of the bar.
func (a *app) barMenuButton(c *ui.Context, svg *ui.SVG, label, tip string, menu func(*ui.Menu)) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(tip).Padding(barPad, barPad).Radius(barButton).
		PopoverMenuButton(menu).Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// readingBar floats over the top of the reading pane: the groups of
// actions fn builds, then the search field. It takes no pointer between
// them, which reaches what is read under it. Where the pane is too narrow
// for the field beside the actions, a button stands for it, and the field
// shows, the actions giving it room, while it is in use: from that button
// or Cmd+K until a message is chosen with the field empty, or Escape.
func (a *app) readingBar(c *ui.Context, label string, fn func()) {
	t := c.Theme()
	compact, field := a.readingCompact(), a.readingSearchShown()
	// The bar ends clear of the window controls, which sit at the right
	// of this, the rightmost pane, on Linux and Windows.
	ui.Themed(c, barTheme(t)).Background(ui.Transparent).Absolute().Top(8).Left(12).
		Right(max(c.TitleBar().Right, 12)).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).PassThrough().Label(label).Children(func() {
			// The actions keep their room, but for a search in use in a
			// narrow pane: then their groups give way, into their menus.
			actions := ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0)
			if compact && field {
				actions.Shrink(1).MinWidth(0)
			}
			actions.Children(fn)
			ui.Spacer(c)
			// Closed, the field is only its magnifier, which opens it:
			// the same field, so that a click on it gives it the keys at
			// once.
			// A narrow bar keeps the field short enough for the replies
			// beside it and the filter at its end.
			width, placeholder := float32(240), a.searchPlaceholder()
			if compact {
				width = 200
			}
			if !field {
				width, placeholder = 30, ""
			}
			capsule(c, ui.Row(c.Key("search")).MinHeight(barHeight).Shrink(1).MinWidth(0).
				AlignItems(ui.Center).Gap(2).Padding(0, 3)).Children(func() {
				ui.SearchField(c, &a.query).Width(width).Shrink(1).MinWidth(min(width, 90)).
					Background(ui.Transparent).Border(0, ui.Transparent).
					Transition(ui.ElementTransition{Animation: &paneSpring, Width: true}).
					Placeholder(placeholder).
					Label(locales.SearchMailLabel()).Tooltip(locales.SearchMailLabel()).Bind(&a.searchField).
					OnClick(a.openSearch).
					OnChange(func() { a.search() })
				// Cmd+K gives the field the keys once it opened.
				if a.searchFocus {
					a.searchFocus = false
					a.searchField.Focus(c)
				}
				if a.searchOpen && strings.TrimSpace(a.query) == "" {
					a.searchField.OnShortcut(c, 0, ui.KeyEscape, func() {
						a.searchOpen = false
						a.list.Handle.Focus(c)
					})
				}
				if field {
					// In a toolbar of its own, the filter button is flat
					// on the glass, as the actions are.
					ui.Toolbar(c, func() { a.filterButton(c, t) }).Padding(0).Shrink(0)
				}
			})
		})
	})
}

// openSearch shows the search field, where it stood behind a button, and
// gives it the keys.
func (a *app) openSearch() {
	a.searchOpen, a.searchFocus, a.focusList = true, true, false
}

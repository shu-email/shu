package main

import "github.com/egoist/mygo/ui"

// The window shows notes above and below the list — what a change did and
// can be taken back, a failure, the sends still waiting — and a search's
// bar above it. They are frames of the same kind, so they are built the
// same way: as tall as what they hold, padded, on a tint, and opening and
// folding away as they appear.

// bannerNote is the frame a note or the search bar shares: what it holds
// is set by the note, and whether it is a row of items or several lines.
func bannerNote(e ui.Element, key string, bg ui.Color) ui.Element {
	return e.Key(key).Shrink(0).Padding(8, 14).Background(bg).Transition(bannerMotion)
}

// bannerRow frames a note that is one row of items.
func bannerRow(c *ui.Context, key string, bg ui.Color) ui.Element {
	return bannerNote(ui.Row(c), key, bg).Gap(8).AlignItems(ui.Center)
}

// bannerColumn frames a note of several lines, whose rows set their own
// gaps.
func bannerColumn(c *ui.Context, key string, bg ui.Color) ui.Element {
	return bannerNote(ui.Column(c), key, bg).Gap(6)
}

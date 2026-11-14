package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// The motion of the mail window. Moves stay short so that the keys, J and
// K above all, never wait on them. While the desktop asks for less motion,
// MyGo shows every move at once and keeps only the fades.

// paneSpring moves the panes as the sidebar opens or closes: the sidebar
// and the panes beside it share it, so that they move in step.
var paneSpring = ui.Animation{Spring: ui.SpringOptions{Duration: 320 * time.Millisecond}}

// sidebarMotion folds the sidebar to nothing as it closes and opens it from
// nothing, opaque, as a drawer.
var sidebarMotion = ui.ElementTransition{
	Animation: &paneSpring,
	Width:     true,
	Enter:     &ui.Motion{Collapse: true, Opacity: 1},
	Exit:      &ui.Motion{Collapse: true, Opacity: 1},
}

// paneMotion slides a pane beside the sidebar to its new place and width.
var paneMotion = ui.ElementTransition{Animation: &paneSpring, Position: true, Width: true}

// readingMotion fades in what the reading pane shows as another message
// or conversation is chosen. The one before goes at once, so two never
// show over each other. Colors alone are set so that the pane's own
// resizing is not animated.
var readingMotion = ui.ElementTransition{
	Duration: 160 * time.Millisecond,
	Colors:   true,
	Enter:    &ui.Motion{Y: 6},
}

// bannerMotion opens a banner above the list from nothing and folds it
// away, and animates its height as its text changes. The list below moves
// with listMotion, of the same length, in step with it.
var bannerMotion = ui.ElementTransition{
	Duration: 200 * time.Millisecond,
	Height:   true,
	Enter:    &ui.Motion{Collapse: true},
	Exit:     &ui.Motion{Collapse: true},
}

var listMotion = ui.ElementTransition{Duration: 200 * time.Millisecond, Position: true, Height: true}

// swapIcon swaps an icon that says a state, as the filled star, by
// shrinking the old one away as the new one grows in.
var swapIcon = ui.ContentTransition{}

// badgeMotion pops a count's badge in as the count first shows, and out as
// it goes.
var badgeMotion = ui.ElementTransition{
	Duration: 180 * time.Millisecond,
	Colors:   true,
	Enter:    &ui.Motion{Scale: 0.6},
	Exit:     &ui.Motion{Scale: 0.6},
}

// countBadge is ui.Badge for a count that changes as mail comes and is
// read: its digits roll, up as it grows and down as it shrinks. key names
// the count, so that each keeps which way it last went.
func (a *app) countBadge(c *ui.Context, key, label string, n int) ui.Element {
	t := c.Theme()
	down := a.countWent(key, n)
	return ui.Box(c.Key("count/"+key)).Padding(0, t.Space(1.5)).Radius(999).Shrink(0).
		Transition(badgeMotion).Children(func() {
		ui.Text(c, label).FontSize(t.FontSize - 2).SingleLine().
			ContentTransition(ui.ContentTransition{Numeric: true, Down: down})
	})
}

// sidebarCount is a count as plain muted digits, as a sidebar shows its
// folders' unread mail, rolling as countBadge's do.
func (a *app) sidebarCount(c *ui.Context, key string, n int) ui.Element {
	t := c.Theme()
	down := a.countWent(key, n)
	return ui.Text(c.Key("count/"+key), fmt.Sprint(n)).FontSize(t.FontSize - 1).TextColor(t.TextMuted).
		SingleLine().Shrink(0).Transition(badgeMotion).
		ContentTransition(ui.ContentTransition{Numeric: true, Down: down})
}

// countWent notes n as the count of key, and reports whether the count
// went down to it.
func (a *app) countWent(key string, n int) bool {
	if a.counts == nil {
		a.counts = map[string]countSeen{}
	}
	last, seen := a.counts[key]
	if !seen || last.n != n {
		a.counts[key] = countSeen{n: n, down: seen && n < last.n}
	}
	return a.counts[key].down
}

// noteCount records a count that shows no badge, so that the next badge
// rolls the way the count really went.
func (a *app) noteCount(key string, n int) {
	a.countWent(key, n)
}

// countSeen is the last value of a count a badge showed, and whether it
// went down to it.
type countSeen struct {
	n    int
	down bool
}

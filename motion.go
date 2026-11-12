package main

import (
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

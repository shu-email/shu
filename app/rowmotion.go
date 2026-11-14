package app

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
)

// rowMotion moves the list's rows as rows leave and come. ui.List places
// its rows itself and animates none, so the window does it: the rows after
// one that left start where they were and slide up into its room, and a
// row that came fades in. What changed the rows (an archive, an Undo, new
// mail, a sync) need not say: the list's build compares its rows with
// those it built before, in the same list.
type rowMotion struct {
	query listQuery
	keys  []string
	// heights is the height of each row as it last showed.
	heights map[string]float32
	// shift is how far below its place each row that moved starts, and
	// coming the rows that came; both as the rows changed, at changed.
	shift   map[string]float32
	coming  map[string]bool
	changed time.Time
}

// rowMotionMost is the most rows that leave or come at once with motion:
// more is another list, as a sync bringing a page, and shows at once.
const rowMotionMost = 8

// rowMotionWindow is how long after the rows changed the builds of that
// change take its motion: those of the frame it showed in. A row built
// later, scrolled into view, is where it is.
const rowMotionWindow = 50 * time.Millisecond

// rowSpring slides the rows into their places.
var rowSpring = ui.Animation{Spring: ui.SpringOptions{Duration: 280 * time.Millisecond}}

// rowEnter fades a row that came in.
var rowEnter = ui.ElementTransition{Duration: 220 * time.Millisecond, Colors: true, Enter: &ui.Motion{}}

// update compares the rows the list shows now, keys, with those it showed
// before, and notes how the rows that stay moved.
func (m *rowMotion) update(q listQuery, keys []string, now time.Time) {
	prev, same := m.keys, q == m.query && len(m.keys) > 0
	m.query, m.keys = q, slices.Clone(keys)
	if m.heights == nil {
		m.heights = map[string]float32{}
	}
	if !same {
		// Another list shows: none of its rows was here before.
		m.heights = map[string]float32{}
		m.shift, m.coming = nil, nil
		return
	}
	if slices.Equal(prev, keys) {
		return
	}
	stays := make(map[string]bool, len(keys))
	for _, k := range keys {
		stays[k] = true
	}
	was := make(map[string]bool, len(prev))
	for _, k := range prev {
		was[k] = true
	}
	// Rows after the last of those shown before are a page read, not
	// mail that came.
	last := -1
	for i, k := range keys {
		if was[k] {
			last = i
		}
	}
	var coming []string
	for i, k := range keys {
		if !was[k] && i < last {
			coming = append(coming, k)
		}
	}
	shift, gone, above := map[string]float32{}, 0, float32(0)
	for _, k := range prev {
		if !stays[k] {
			gone++
			above += m.heights[k]
			continue
		}
		if above > 0 {
			shift[k] = above
		}
	}
	// Forget the heights of rows no longer in the list, before the change
	// is dropped for its size, or they would stay for good.
	for k := range m.heights {
		if !stays[k] {
			delete(m.heights, k)
		}
	}
	m.shift, m.coming, m.changed = nil, nil, now
	if gone > rowMotionMost || len(coming) > rowMotionMost {
		return
	}
	m.shift = shift
	m.coming = map[string]bool{}
	for _, k := range coming {
		m.coming[k] = true
	}
}

// row gives the content of the row of key, box, its motion: the slide
// from where it was, or the fade of a row that came. A chosen row stays
// in its place, where the list draws its highlight; the rows after it
// still slide.
func (m *rowMotion) row(box ui.Element, key string, chosen bool, now time.Time) {
	fresh := now.Sub(m.changed) <= rowMotionWindow
	if off, ok := m.shift[key]; ok && fresh {
		delete(m.shift, key)
		if !chosen {
			box.Jump("slide", off)
		}
	}
	if y := box.AnimateTo("slide", 0, rowSpring); y != 0 {
		box.Translate(0, y)
	}
	if m.coming[key] && fresh {
		box.Transition(rowEnter)
	}
	if h := box.LayoutBounds().H; h > 0 {
		m.heights[key] = h
	}
}

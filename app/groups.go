package app

import (
	"sort"
	"time"

	"shu/locales"
	"shu/mail"
)

// The list draws its mail as cards under a caption a group of them shares.
// The groups are the day's, then yesterday's, then the week's, then the
// rest: a list is read from the top, so what arrived together is captioned
// together. Ordering the cards by what they are about is the model's work,
// and lands with the read that ranks them.

// listGroup is the run of cards a caption names.
type listGroup int

const (
	groupToday listGroup = iota
	groupYesterday
	groupThisWeek
	groupEarlier
)

// groupOf is the group a message of date belongs to at now, in the window's
// own clock.
func groupOf(date, now time.Time) listGroup {
	if date.IsZero() {
		return groupEarlier
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	day := date.In(now.Location())
	switch {
	case !day.Before(today):
		return groupToday
	case !day.Before(today.AddDate(0, 0, -1)):
		return groupYesterday
	case !day.Before(today.AddDate(0, 0, -7)):
		return groupThisWeek
	}
	return groupEarlier
}

// groupCaption is what a group is called in the user's words.
func groupCaption(g listGroup) string {
	switch g {
	case groupToday:
		return locales.GroupToday()
	case groupYesterday:
		return locales.GroupYesterday()
	case groupThisWeek:
		return locales.GroupThisWeek()
	}
	return locales.GroupEarlier()
}

// rowGroup is the group of row i, for the card and the caption it draws:
// the model's topic when the list is grouped, the day's otherwise.
func (a *app) rowGroup(i int, now time.Time) (int, string) {
	if g, name, ok := a.topicGroup(i); ok {
		return g, name
	}
	// The stream is ranked by what asks of the user, so its captions are
	// those two groups rather than the days: a day's caption would repeat
	// as often as the ranking crosses it.
	if a.streaming() && i >= 0 && i < len(a.all) {
		if intentOf(a.all[i]) == intentNeeds {
			return int(intentNeeds), locales.StreamNeedsGroup()
		}
		return int(intentNews), locales.StreamNewsGroup()
	}
	if i < 0 || i >= len(a.all) {
		return int(groupEarlier), groupCaption(groupEarlier)
	}
	g := groupOf(a.all[i].Date, now)
	return int(g), groupCaption(g)
}

// startsGroup reports that row i opens a new group, so that it draws the
// caption the rows after it share.
func (a *app) startsGroup(i int, now time.Time) bool {
	if len(a.all) == 0 || i < 0 || i >= len(a.all) {
		return false
	}
	if i == 0 {
		return true
	}
	was, _ := a.rowGroup(i-1, now)
	nowGroup, _ := a.rowGroup(i, now)
	return nowGroup != was
}

// endsGroup reports that row i closes its group, so that only its last
// card rounds its bottom corners.
func (a *app) endsGroup(i int, now time.Time) bool {
	n := len(a.all)
	if i < 0 || i >= n {
		return false
	}
	if i == n-1 {
		return true
	}
	nowGroup, _ := a.rowGroup(i, now)
	next, _ := a.rowGroup(i+1, now)
	return next != nowGroup
}

// listInset is the inset the cards and their captions share from the
// list's own edge: the gutter of a card plus its padding.
const listInset = 24

// pinnedCaption is the caption the list keeps at its top: that of the
// group the first row on screen belongs to, once that group's own caption
// has scrolled away with its first card.
func (a *app) pinnedCaption(now time.Time) string {
	first, last := a.list.Visible()
	if last < 0 || first <= 0 || first >= len(a.all) {
		return ""
	}
	if a.startsGroup(first, now) {
		return ""
	}
	return a.rowGroupName(first, now)
}

// rowGroupName is the caption row i is drawn under.
func (a *app) rowGroupName(i int, now time.Time) string {
	_, name := a.rowGroup(i, now)
	return name
}

// rankRows orders the stream by what asks of the user: the mail that needs
// an answer first, the news after it, and each of them newest first. It
// runs on the rows read, after a read, never during a build, and keeps the
// choice by key so the message that was open stays open. What the store
// reads is untouched: paging still takes its cursor from the page it read,
// so a card that moves below the rows shown does not move the next page.
func (a *app) rankRows() {
	n := len(a.all)
	if n < 2 {
		return
	}
	key := a.selectedKey()
	needs := make([]bool, n)
	for i := 0; i < n; i++ {
		needs[i] = intentOf(a.all[i]) == intentNeeds
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		if needs[order[x]] != needs[order[y]] {
			return needs[order[x]]
		}
		return false
	})
	all := make([]mail.Summary, n)
	convs := make([]mail.Conversation, len(a.convs))
	for to, from := range order {
		all[to] = a.all[from]
		if len(a.convs) == n {
			convs[to] = a.convs[from]
		}
	}
	a.all = all
	if len(a.convs) == n {
		a.convs = convs
	}
	a.restoreSelection(key)
}

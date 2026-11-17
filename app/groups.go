package app

import (
	"time"

	"shu/locales"
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

// rowGroupName is the caption row i is drawn under.
func (a *app) rowGroupName(i int, now time.Time) string {
	_, name := a.rowGroup(i, now)
	return name
}

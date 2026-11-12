package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// filterForm is the filter control beside the search field: whether it is
// open, and the values its fields edit. The text fields and the days are
// taken from the search as it opens, the check boxes from the search as
// each frame builds them: the search field's text is what is searched,
// and the control only writes it.
type filterForm struct {
	open                        bool
	from, to, subject           string
	since, before               time.Time
	sinceOn, beforeOn           bool
	unread, starred, attachment bool
}

// searchPlaceholder says what a search typed now would cover.
func (a *app) searchPlaceholder() string {
	switch a.searchScope() {
	case scopeAll:
		return locales.SearchAllAccounts()
	case scopeAccount:
		if len(a.accounts) > 1 {
			return locales.SearchFolder(a.accountLabel(a.active))
		}
		return locales.SearchMailLabel()
	}
	return locales.SearchFolder(a.folderLabel(a.folder))
}

// filterCount is how many filters the search field's text holds.
func filterCount(text string) int {
	n := 0
	for _, p := range splitQuery(text) {
		if p.filter() {
			n++
		}
	}
	return n
}

// filterButton is the filter control: a button beside the search field,
// showing how many filters are on, that opens the filters.
func (a *app) filterButton(c *ui.Context, t *ui.Theme) {
	n := filterCount(a.query)
	now := c.Now()
	label := locales.SearchFilters()
	b := ui.Button(c, "").Label(label).Tooltip(label).Padding(6, 8).Expanded(a.filters.open).
		OnClick(func() { a.toggleFilters(now) })
	if n > 0 {
		b.Background(t.Accent.Alpha(0.14))
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, filterIcon).FontSize(16)
			if n > 0 {
				ui.Text(c, fmt.Sprint(n)).FontSize(11).Bold().TextColor(t.Accent)
			}
		})
	})
	ui.PopoverBase(c, b, &a.filters.open, func(panel ui.Element) {
		panel.Margin(6, 0, 0, 0).Radius(10).Background(t.Background).Border(1, t.Border).
			Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18)).Label(locales.SearchFiltersTitle())
		a.filterPanel(c, t, now)
	})
}

// toggleFilters opens the filters, their fields showing what the search
// asks, or closes them.
func (a *app) toggleFilters(now time.Time) {
	f := &a.filters
	if f.open {
		f.open = false
		return
	}
	q := a.parsedQuery()
	y, m, d := now.In(searchZone).Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, searchZone)
	f.from, f.to, f.subject = strings.Join(q.From, " "), strings.Join(q.To, " "), strings.Join(q.Subject, " ")
	f.since, f.before = today, today
	if !q.Since.IsZero() {
		f.since = q.Since
	}
	if !q.Before.IsZero() {
		f.before = q.Before
	}
	f.open = true
}

// writeFilter writes a filter into the search field's text, in place of
// those drop reports, and searches again.
func (a *app) writeFilter(drop func(searchQuery) bool, add string) {
	a.query = withFilter(a.query, drop, add)
	a.search()
}

// filterPanel is what the filter control opens: a field for each filter,
// each written into the search as an operator.
func (a *app) filterPanel(c *ui.Context, t *ui.Theme, now time.Time) {
	f := &a.filters
	q := a.parsedQuery()
	f.sinceOn, f.beforeOn = !q.Since.IsZero(), !q.Before.IsZero()
	if f.sinceOn {
		f.since = q.Since
	}
	if f.beforeOn {
		f.before = q.Before
	}
	f.unread, f.starred, f.attachment = q.Unread, q.Starred, q.Attachment
	ui.Column(c).Width(360).Padding(12, 14).Gap(10).Children(func() {
		ui.Text(c, locales.SearchFiltersTitle()).FontSize(13).Bold()
		a.textFilter(c, locales.FilterFrom(), &f.from, "from", func(s searchQuery) bool { return len(s.From) > 0 })
		a.textFilter(c, locales.FilterTo(), &f.to, "to", func(s searchQuery) bool { return len(s.To) > 0 })
		a.textFilter(c, locales.FilterSubject(), &f.subject, "subject", func(s searchQuery) bool { return len(s.Subject) > 0 })
		a.dayFilter(c, locales.FilterAfter(), locales.FilterAfterDate(), &f.sinceOn, &f.since, "after",
			func(s searchQuery) bool { return !s.Since.IsZero() })
		a.dayFilter(c, locales.FilterBefore(), locales.FilterBeforeDate(), &f.beforeOn, &f.before, "before",
			func(s searchQuery) bool { return !s.Before.IsZero() })
		ui.Row(c).Wrap().Gap(14).Padding(2, 0).Children(func() {
			a.flagFilter(c, locales.FilterUnread(), &f.unread, "is:unread", func(s searchQuery) bool { return s.Unread })
			a.flagFilter(c, locales.FilterStarred(), &f.starred, "is:starred", func(s searchQuery) bool { return s.Starred })
			a.flagFilter(c, locales.FilterAttachment(), &f.attachment, "has:attachment", func(s searchQuery) bool { return s.Attachment })
		})
		a.folderFilter(c, q)
		a.accountFilter(c, q)
		ui.Divider(c)
		ui.Text(c, locales.FilterSyntaxHint()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Children(func() {
			ui.Spacer(c)
			ui.Button(c, locales.ClearFilters()).Disabled(!hasFilters(q)).
				OnClick(func() { a.writeFilter(func(searchQuery) bool { return true }, "") })
		})
	})
}

// filterCaptionWidth is the width of the names before the filters' fields,
// check boxes included, so that the fields line up.
const filterCaptionWidth = 112

// filterCaption is the name before a filter's field, which clicking
// focuses the field.
func filterCaption(c *ui.Context, name string) ui.Element {
	return ui.Text(c, name).FontSize(12).Width(filterCaptionWidth).Shrink(0)
}

// textFilter is a filter of words in a part of a message: one operator,
// written as the field changes.
func (a *app) textFilter(c *ui.Context, name string, value *string, op string, has func(searchQuery) bool) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		caption := filterCaption(c, name)
		ui.TextInput(c, value).Grow(1).MinWidth(0).LabelledBy(caption).
			OnChange(func() { a.writeFilter(has, operator(op, *value)) })
	})
}

// dayFilter is one end of the date range: a check box that turns it on
// and off, and the day. A day chosen turns it on.
func (a *app) dayFilter(c *ui.Context, name, dayName string, on *bool, day *time.Time, op string, has func(searchQuery) bool) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Checkbox(c, on, name).Width(filterCaptionWidth).Shrink(0).OnChange(func() {
			add := ""
			if *on {
				add = dayOperator(op, *day)
			}
			a.writeFilter(has, add)
		})
		date := ui.DateInput(c, day).Label(dayName).Grow(1).OnChange(func() { a.writeFilter(has, dayOperator(op, *day)) })
		if !*on {
			// The day of a filter that is off searches nothing yet.
			date.Opacity(0.55)
		}
	})
}

// flagFilter is a filter of a flag or of attachments.
func (a *app) flagFilter(c *ui.Context, name string, on *bool, op string, has func(searchQuery) bool) {
	ui.Checkbox(c, on, name).OnChange(func() {
		add := ""
		if *on {
			add = op
		}
		a.writeFilter(has, add)
	})
}

// folderChoice is a folder the filter control offers: what in: takes,
// and the folder's name in the window.
type folderChoice struct{ value, label string }

// folderChoices are the folders the filter control offers: those of a
// special use any account has, by the name in: knows them by in every
// account, then the other folders of the account on screen.
func (a *app) folderChoices() []folderChoice {
	var out []folderChoice
	for _, s := range []struct {
		use, value string
		label      func() string
	}{
		{mail.SpecialInbox, "inbox", locales.Inbox}, {mail.SpecialSent, "sent", locales.Sent},
		{mail.SpecialDrafts, "drafts", locales.Drafts}, {mail.SpecialArchive, "archive", locales.Archive},
		{mail.SpecialJunk, "junk", locales.Junk}, {mail.SpecialTrash, "trash", locales.Trash},
	} {
		for _, acc := range a.accounts {
			if a.specialFolder(acc.ID, s.use) != "" {
				out = append(out, folderChoice{s.value, s.label()})
				break
			}
		}
	}
	for _, f := range a.foldersFor(a.active) {
		if f.SpecialUse == "" {
			out = append(out, folderChoice{f.Name, f.Name})
		}
	}
	return out
}

// inLabel is the name of the folders an in: means.
func (a *app) inLabel(in string) string {
	for _, f := range a.folderChoices() {
		if strings.EqualFold(f.value, in) {
			return f.label
		}
	}
	for _, f := range a.foldersFor(a.active) {
		if f.NamedBy(in) {
			return a.folderLabel(f.Name)
		}
	}
	return in
}

// folderFilter is the filter of a folder: in:, which searches every
// folder of the scope that the name means.
func (a *app) folderFilter(c *ui.Context, q searchQuery) {
	has := func(s searchQuery) bool { return s.In != "" }
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		caption := filterCaption(c, locales.FilterFolder())
		label := locales.AnyFolder()
		if q.In != "" {
			label = a.inLabel(q.In)
		}
		ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
			if m.Item(locales.AnyFolder()).Radio(q.In == "").Chosen() {
				a.writeFilter(has, "")
			}
			m.Separator()
			for _, f := range a.folderChoices() {
				if m.Item(f.label).Radio(strings.EqualFold(q.In, f.value)).Chosen() {
					a.writeFilter(has, operator("in", f.value))
				}
			}
		}).LabelledBy(caption)
	})
}

// accountValue is what account: the filter control writes for an account:
// its name, or its address where another account has the same name.
func (a *app) accountValue(acc mail.Account) string {
	for _, other := range a.accounts {
		if other.ID != acc.ID && strings.EqualFold(other.Label(), acc.Label()) {
			return acc.Email
		}
	}
	return acc.Label()
}

// accountChoice is an account's item of the filter control: its name, and
// its address after it where another account has the same name.
func (a *app) accountChoice(acc mail.Account) string {
	if value := a.accountValue(acc); value != acc.Label() {
		return acc.Label() + " · " + value
	}
	return acc.Label()
}

// accountFilter is the filter of an account, with two or more: account:,
// which keeps the accounts whose name or address has the value.
func (a *app) accountFilter(c *ui.Context, q searchQuery) {
	if len(a.accounts) < 2 {
		return
	}
	has := func(s searchQuery) bool { return len(s.Accounts) > 0 }
	chosen := func(acc mail.Account) bool {
		return len(q.Accounts) == 1 && strings.EqualFold(q.Accounts[0], a.accountValue(acc))
	}
	label := locales.AnyAccount()
	if len(q.Accounts) > 0 {
		label = strings.Join(q.Accounts, " ")
		for _, acc := range a.accounts {
			if chosen(acc) {
				label = a.accountChoice(acc)
			}
		}
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		caption := filterCaption(c, locales.FilterAccount())
		ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
			if m.Item(locales.AnyAccount()).Radio(len(q.Accounts) == 0).Chosen() {
				a.writeFilter(has, "")
			}
			m.Separator()
			for _, acc := range a.accounts {
				if m.Item(a.accountChoice(acc)).Radio(chosen(acc)).Chosen() {
					a.writeFilter(has, operator("account", a.accountValue(acc)))
				}
			}
		}).LabelledBy(caption)
	})
}

// dayText writes a day of a filter as the list writes dates.
func dayText(c *ui.Context, day time.Time) string {
	return i18n.FormatDate(c.Locale(), day, i18n.DateOptions{Skeleton: "yMMMd"})
}

// describeFilter says what one filter asks, as its chip and the empty
// list name it.
func (a *app) describeFilter(c *ui.Context, s searchQuery) string {
	switch {
	case len(s.Accounts) > 0:
		value := strings.Join(s.Accounts, " ")
		if len(a.accountsNamed(value)) == 0 {
			return locales.FilterNoAccount(value)
		}
		return locales.FilterAccountValue(value)
	case len(s.From) > 0:
		return locales.FilterFromValue(strings.Join(s.From, " "))
	case len(s.To) > 0:
		return locales.FilterToValue(strings.Join(s.To, " "))
	case len(s.Subject) > 0:
		return locales.FilterSubjectValue(strings.Join(s.Subject, " "))
	case len(s.Body) > 0:
		return locales.FilterBodyValue(strings.Join(s.Body, " "))
	case s.Unread:
		return locales.FilterUnread()
	case s.Starred:
		return locales.FilterStarred()
	case s.Attachment:
		return locales.FilterAttachment()
	case !s.Since.IsZero():
		return locales.FilterAfterDay(dayText(c, s.Since))
	case !s.Before.IsZero():
		return locales.FilterBeforeDay(dayText(c, s.Before))
	case s.In != "":
		return locales.FilterInFolder(a.inLabel(s.In))
	}
	return ""
}

// filterNames names the filters of the search on screen.
func (a *app) filterNames(c *ui.Context) []string {
	var out []string
	for _, p := range splitQuery(a.query) {
		if p.filter() {
			out = append(out, a.describeFilter(c, p.search))
		}
	}
	return out
}

// scopeLabel names a scope on the scope control. The list's own is not
// called by the list's name, which the sidebar shows beside it and the
// line under the title repeats: two controls of one name are one too
// many for a screen reader.
func (a *app) scopeLabel(s searchScope) string {
	switch {
	case s == scopeAccount:
		return locales.AllFolders()
	case s == scopeAll:
		return locales.AllAccounts()
	case a.isUnified():
		return locales.ScopeInboxes()
	}
	return locales.ThisFolder()
}

// accountNames names accounts, by ID, as a list in words.
func (a *app) accountNames(ids []string) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = a.accountLabel(id)
	}
	return strings.Join(names, locales.ListSeparator())
}

// scopeWhere says in words what the search on screen covers.
func (a *app) scopeWhere() string {
	ids, narrowed := a.queryAccounts(a.parsedQuery())
	switch s := a.searchScope(); {
	case narrowed && len(ids) == 0:
		return locales.WhereNoAccount()
	case s == scopeAll && narrowed && len(ids) < len(a.accounts):
		return locales.WhereAccount(a.accountNames(ids))
	case a.isUnified() && narrowed && len(ids) < len(a.accounts):
		return locales.WhereInboxesOf(a.accountNames(ids))
	}
	switch a.searchScope() {
	case scopeAll:
		return locales.WhereAllAccounts()
	case scopeAccount:
		if len(a.accounts) > 1 {
			return locales.WhereAccount(a.accountLabel(a.active))
		}
		return locales.WhereAllFolders()
	}
	if len(a.accounts) > 1 && !a.isUnified() {
		return locales.WhereFolderOf(a.folderLabel(a.folder), a.accountLabel(a.active))
	}
	return a.folderLabel(a.folder)
}

// wider is the scope one wider than the search's, and whether there is
// one that covers more: none when its account: parts keep no account,
// and not every account when they keep only the one on screen.
func (a *app) wider() (searchScope, bool) {
	ids, narrowed := a.queryAccounts(a.parsedQuery())
	if narrowed && len(ids) == 0 {
		return scopeHere, false
	}
	switch s := a.searchScope(); {
	case s == scopeHere && !a.isUnified():
		return scopeAccount, true
	case s != scopeAll && len(a.accounts) > 1 && !(s == scopeAccount && narrowed && len(ids) == 1):
		return scopeAll, true
	}
	return scopeHere, false
}

// searchBar is shown while a search is: the scope it covers, which the
// control changes, and its filters, each of which can be taken off.
func (a *app) searchBar(c *ui.Context, t *ui.Theme) {
	choices := a.scopeChoices()
	current := a.searchScope()
	labels := make([]string, len(choices))
	for i, s := range choices {
		labels[i] = a.scopeLabel(s)
		if s == current {
			a.scopeSeg = i
		}
	}
	ui.Column(c.Key("search-bar")).Shrink(0).Padding(8, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Transition(bannerMotion).Children(func() {
		ui.Segmented(c, &a.scopeSeg, labels...).FillEqually().Label(locales.SearchScope()).
			OnChange(func() {
				if a.scopeSeg >= 0 && a.scopeSeg < len(choices) {
					a.chooseScope(choices[a.scopeSeg])
				}
			})
		a.filterChips(c, t)
	})
}

// filterChips shows the search's filters, each with a button that takes
// it out of the search field's text.
func (a *app) filterChips(c *ui.Context, t *ui.Theme) {
	parts := splitQuery(a.query)
	if filterCount(a.query) == 0 {
		return
	}
	text := a.query
	ui.Row(c).Wrap().Gap(6).Label(locales.ActiveFilters()).Children(func() {
		for i, p := range parts {
			if !p.filter() {
				continue
			}
			name := a.describeFilter(c, p.search)
			ui.Row(c.Key(i)).Gap(2).AlignItems(ui.Center).Padding(1, 2, 1, 9).Radius(999).
				Background(t.Surface).Border(1, t.Border).Children(func() {
				ui.Text(c, name).FontSize(11).SingleLine().MaxWidth(240)
				ui.Button(c, "").Label(locales.RemoveFilter(name)).Tooltip(locales.RemoveFilter(name)).Padding(3, 5).
					OnClick(func() {
						if a.query == text {
							a.query = withoutPart(text, i)
							a.search()
						}
					}).
					Children(func() { ui.Icon(c, closeIcon).FontSize(11) })
			})
		}
	})
}

// noResults is what an empty search says beside its text: what it
// covered and with which filters, that it read this device's mail, and
// the way to search wider.
func (a *app) noResults(c *ui.Context, t *ui.Theme) {
	q := a.parsedQuery()
	if ids, narrowed := a.queryAccounts(q); narrowed && len(ids) == 0 {
		// No account to search: the values to blame, or that the
		// account: parts keep no account together.
		blamed := false
		for _, v := range q.Accounts {
			if len(a.accountsNamed(v)) == 0 {
				ui.Text(c, locales.NoAccountNamed(v)).FontSize(12).TextColor(t.Warning).TextAlign(ui.Center).MaxLines(2)
				blamed = true
			}
		}
		if !blamed {
			ui.Text(c, locales.NoAccountMatchesAll()).FontSize(12).TextColor(t.Warning).TextAlign(ui.Center).MaxLines(2)
		}
	} else {
		ui.Text(c, locales.SearchedWhere(a.scopeWhere())).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
	}
	if names := a.filterNames(c); len(names) > 0 {
		ui.Text(c, locales.WithFilters(strings.Join(names, locales.ListSeparator()))).
			FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxLines(3)
	}
	ui.Text(c, locales.SearchedHere()).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center)
	if s, ok := a.wider(); ok {
		label := locales.SearchAllFolders()
		if s == scopeAll {
			label = locales.SearchAllAccounts()
		}
		ui.PrimaryButton(c, label).OnClick(func() { a.chooseScope(s) })
	}
}

// noResultsText is the first line of an empty search: the words it
// looked for, a phrase without its quotes inside the line's own, or that
// its filters found nothing.
func noResultsText(query string) string {
	words := parseQuery(query).Text
	if len(words) == 0 {
		return locales.NoFilterResults()
	}
	return locales.SearchNoResults(strings.Join(words, " "))
}

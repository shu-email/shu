package main

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// searchScope is how much of the cache a search covers, as the scope
// control above the results chooses it.
type searchScope int

const (
	// scopeHere is the list's own: its folder, Starred, Drafts, or the
	// inboxes of the unified inbox.
	scopeHere searchScope = iota
	// scopeAccount is every folder of the account on screen.
	scopeAccount
	// scopeAll is every folder of every account.
	scopeAll
)

// searchZone is the time zone typed days are read in: the system's, which
// the list writes its dates in.
var searchZone = time.Local

// searchQuery is what a search's text asks: what the engine reads of it,
// and the values of its account: parts, which the window applies to the
// scope itself. The engine never sees them: they are no text to find.
type searchQuery struct {
	mail.Search
	// Accounts are the values of the account: parts. Each keeps the
	// accounts whose name or address contains it, in any case.
	Accounts []string
}

// IsZero reports whether the search asks nothing.
func (s searchQuery) IsZero() bool { return s.Search.IsZero() && len(s.Accounts) == 0 }

// parseQuery reads a search's text: its account: parts taken out, the
// rest as the engine reads it.
func parseQuery(text string) searchQuery {
	return queryOf(text, splitQuery(text))
}

// queryOf reads a search's text with the parts it is already split into.
func queryOf(text string, parts []queryPart) searchQuery {
	rest := parts[:0:0]
	var q searchQuery
	for _, p := range parts {
		if p.account {
			q.Accounts = append(q.Accounts, p.search.Accounts...)
			continue
		}
		rest = append(rest, p)
	}
	if len(rest) == len(parts) {
		q.Search = mail.ParseSearch(text, searchZone)
	} else {
		q.Search = mail.ParseSearch(joinQuery(rest), searchZone)
	}
	return q
}

// clone is the query with lists of its own, so that a caller cannot
// change the one a read kept.
func (q searchQuery) clone() searchQuery {
	q.Text = slices.Clone(q.Text)
	q.From = slices.Clone(q.From)
	q.To = slices.Clone(q.To)
	q.Subject = slices.Clone(q.Subject)
	q.Body = slices.Clone(q.Body)
	q.Labels = slices.Clone(q.Labels)
	q.Accounts = slices.Clone(q.Accounts)
	return q
}

// parseMemo keeps the last read of a search's text. A build asks what
// the text means from the list, its title and its toolbar, and the answer
// is the same until the text changes; the zone is part of the key, as
// typed days are read in it.
//
// It belongs to the window: a background job reads the text of a saved
// search with parseQuery and writes no view state.
type parseMemo struct {
	text  string
	zone  *time.Location
	query searchQuery
	ok    bool
}

// parse is what a search's text asks, read once per text.
func (a *app) parse(text string) searchQuery {
	m := &a.queryMemo
	if !m.ok || m.text != text || m.zone != searchZone {
		*m = parseMemo{text: text, zone: searchZone, query: parseQuery(text), ok: true}
	}
	return m.query.clone()
}

// parsedQuery is what the search field asks.
func (a *app) parsedQuery() searchQuery { return a.parse(a.query) }

// accountsNamed are the accounts, by ID, whose name or address contains
// value in any case, as from: finds a sender.
func (a *app) accountsNamed(value string) []string {
	value = strings.ToLower(value)
	var out []string
	for _, acc := range a.accounts {
		if strings.Contains(strings.ToLower(acc.Name), value) || strings.Contains(strings.ToLower(acc.Email), value) {
			out = append(out, acc.ID)
		}
	}
	return out
}

// namesAccount reports whether an account: value keeps the account.
func (a *app) namesAccount(value, id string) bool { return slices.Contains(a.accountsNamed(value), id) }

// queryAccounts are the accounts, in the window's order, that every
// account: part of a search keeps, and whether it has any: with none,
// every account.
func (a *app) queryAccounts(q searchQuery) (ids []string, narrowed bool) {
	if len(q.Accounts) == 0 {
		return nil, false
	}
	for _, acc := range a.accounts {
		keep := true
		for _, v := range q.Accounts {
			keep = keep && a.namesAccount(v, acc.ID)
		}
		if keep {
			ids = append(ids, acc.ID)
		}
	}
	return ids, true
}

// leavesOutShown reports whether a search's account: parts leave out the
// account on screen, which its folder or its folders cannot then answer.
func (a *app) leavesOutShown(q searchQuery) bool {
	ids, narrowed := a.queryAccounts(q)
	return narrowed && !slices.Contains(ids, a.active)
}

// defaultScopeFor is where a list's searches start. A folder's cover its
// whole account, where what is looked for so often is: in Sent, in
// Archive. The drafts of this device are in no folder of the server, the
// unified inbox is already a list of every account, and a label's list
// of every folder of its own.
func (a *app) defaultScopeFor(folder string) searchScope {
	if folder == draftsFolder || folder == remindersFolder || folder == followUpFolder ||
		folder == allInboxes || labelOf(folder) != 0 {
		return scopeHere
	}
	return scopeAccount
}

// searchScope is the scope the search on screen covers (scopeFor), read
// as the list it shows: a smart mailbox's is the list it was saved from,
// not its entry in the sidebar.
func (a *app) searchScope() searchScope {
	return a.scopeFor(a.smartSaveFolder(), a.scope, a.scopeSet, a.parsedQuery())
}

// scopeFor is the scope a search of a list covers: the one chosen, widened
// where the list's own cannot apply what is typed. A folder, or the
// inboxes, and an in: that names another folder would find nothing
// together, as would the account on screen and an account: that leaves it
// out; the drafts' search knows their text alone. A smart mailbox reads
// its list by the same rules, though it is not that list.
func (a *app) scopeFor(folder string, scope searchScope, chosen bool, q searchQuery) searchScope {
	// The reminders' and follow-ups' lists are their own scope: no other
	// widens them.
	if folder == remindersFolder || folder == followUpFolder {
		return scopeHere
	}
	s := a.defaultScopeFor(folder)
	if chosen {
		s = scope
	}
	if s == scopeAll && len(a.accounts) < 2 {
		s = scopeAccount
	}
	if s == scopeAccount && folder == allInboxes {
		s = scopeHere
	}
	if s == scopeAll || q.IsZero() {
		return s
	}
	if folder != allInboxes && len(a.accounts) > 1 && a.leavesOutShown(q) {
		return scopeAll
	}
	if s != scopeHere {
		return s
	}
	switch {
	case folder == draftsFolder && hasFilters(q):
		return scopeAccount
	case q.In == "":
	case folder == allInboxes:
		return scopeAll
	case a.isServerFolder(folder):
		return scopeAccount
	}
	return s
}

// scopeChoices are the scopes the control offers on this list.
func (a *app) scopeChoices() []searchScope {
	folder := a.smartSaveFolder()
	out := []searchScope{scopeHere}
	if folder == remindersFolder || folder == followUpFolder {
		return out
	}
	if folder != allInboxes {
		out = append(out, scopeAccount)
	}
	if len(a.accounts) > 1 {
		out = append(out, scopeAll)
	}
	return out
}

// chooseScope searches again with another scope, the text kept. A scope
// of the account on screen drops what the text asks that it cannot
// apply, as an in: of another folder in the list's own or an account:
// that leaves the account out, so that the field says what is searched.
func (a *app) chooseScope(s searchScope) {
	folder := a.smartSaveFolder()
	q := a.query
	if s == scopeHere {
		switch {
		case folder == draftsFolder:
			q = withFilter(q, func(searchQuery) bool { return true }, "")
		case folder == allInboxes || a.isServerFolder(folder):
			q = withFilter(q, func(s searchQuery) bool { return s.In != "" }, "")
		}
	}
	if s != scopeAll && folder != allInboxes {
		q = withFilter(q, func(s searchQuery) bool {
			return len(s.Accounts) > 0 && !a.namesAccount(s.Accounts[0], a.active)
		}, "")
	}
	a.query = q
	a.scope, a.scopeSet = s, true
	// The results of another scope are another list, read from its top:
	// where the last one was scrolled must not read on in this one.
	a.list = ui.ListState{}
	a.search()
}

// spansAccounts reports whether the list's rows may be of several
// accounts, which each row then names.
func (a *app) spansAccounts() bool {
	if len(a.accounts) < 2 {
		return false
	}
	q := a.listQuery()
	if q.folder == allInboxes {
		return true
	}
	if smartOf(q.folder) != 0 {
		return q.smartFolder == allInboxes || q.search != "" && q.scope == scopeAll
	}
	return q.search != "" && q.scope == scopeAll
}

// spansFolders reports whether the list's rows may be of several folders
// of an account: a search of every folder, or of every account, a label's
// list, and a smart mailbox saved from one of these.
func (a *app) spansFolders() bool {
	q := a.listQuery()
	if smartOf(q.folder) != 0 {
		if q.scope != scopeHere {
			return true
		}
		switch {
		case q.smartFolder == "starred", q.smartFolder == draftsFolder:
			// As the starred list and the drafts' own, which one folder
			// stands for.
			return false
		case a.isServerFolder(q.smartFolder):
			return false
		}
		return true
	}
	return q.search != "" && q.scope != scopeHere || labelOf(q.folder) != 0 ||
		q.folder == remindersFolder || q.folder == followUpFolder
}

// hasFilters reports whether a search asks more than its free text.
func hasFilters(s searchQuery) bool {
	s.Text = nil
	return !s.IsZero()
}

// queryPart is a piece of the search field's text as mail.ParseSearch
// splits it: a word, a "quoted phrase", or an operator and its value.
// raw is the piece as typed, so that the text can be written again
// around it; search is what it asks on its own. account marks an
// account: part, with its value or still without one.
type queryPart struct {
	raw     string
	search  searchQuery
	account bool
}

// filter reports whether the part is an operator that narrows the
// search, as opposed to free text or an operator still without its value.
func (p queryPart) filter() bool { return len(p.search.Text) == 0 && !p.search.IsZero() }

// queryOperators are the operators a search's text has: those
// mail.ParseSearch reads, and account:, which the window reads. A quote
// right after one of them opens its value, and after any other word a
// phrase.
var queryOperators = map[string]bool{
	"from": true, "to": true, "subject": true, "body": true, "in": true,
	"has": true, "is": true, "after": true, "before": true, "label": true, "account": true,
}

// isQuoteRune is a quote that opens or closes a phrase in a search.
func isQuoteRune(r rune) bool { return r == '"' || r == '“' || r == '”' }

// splitQuery splits a search's text where mail.ParseSearch does. A quote
// left open is closed, so that what is written after the part stays out
// of it.
func splitQuery(text string) []queryPart {
	runes := []rune(text)
	quoted := func(i int) int {
		end := i + 1
		for end < len(runes) && !isQuoteRune(runes[end]) {
			end++
		}
		if end < len(runes) {
			end++
		}
		return end
	}
	var out []queryPart
	closed := func(raw string) string {
		quotes := 0
		for _, r := range raw {
			if isQuoteRune(r) {
				quotes++
			}
		}
		if quotes%2 == 1 {
			raw += `"`
		}
		return raw
	}
	add := func(raw string) {
		raw = closed(raw)
		out = append(out, queryPart{raw: raw, search: searchQuery{Search: mail.ParseSearch(raw, searchZone)}})
	}
	for i := 0; i < len(runes); {
		switch r := runes[i]; {
		case unicode.IsSpace(r):
			i++
			continue
		case isQuoteRune(r):
			end := quoted(i)
			add(string(runes[i:end]))
			i = end
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) && !isQuoteRune(runes[i]) {
			i++
		}
		word := string(runes[start:i])
		if colon := strings.IndexAny(word, ":："); colon >= 0 && queryOperators[strings.ToLower(word[:colon])] {
			_, size := utf8.DecodeRuneInString(word[colon:])
			value := word[colon+size:]
			if value == "" && i < len(runes) && isQuoteRune(runes[i]) {
				end := quoted(i)
				value = strings.TrimFunc(string(runes[i:end]), isQuoteRune)
				i = end
			}
			if strings.EqualFold(word[:colon], "account") {
				p := queryPart{raw: closed(string(runes[start:i])), account: true}
				if value = strings.TrimSpace(value); value != "" {
					p.search.Accounts = []string{value}
				}
				out = append(out, p)
				continue
			}
		}
		add(string(runes[start:i]))
	}
	return out
}

// joinQuery writes parts back as the text of a search.
func joinQuery(parts []queryPart) string {
	raws := make([]string, len(parts))
	for i, p := range parts {
		raws[i] = p.raw
	}
	return strings.Join(raws, " ")
}

// withFilter is a search's text without the filters drop reports, and
// with add after the rest unless it is "": what a choice in the filter
// control writes. Free text stays as it is.
func withFilter(text string, drop func(searchQuery) bool, add string) string {
	var kept []queryPart
	for _, p := range splitQuery(text) {
		if p.filter() && drop(p.search) {
			continue
		}
		kept = append(kept, p)
	}
	if add != "" {
		kept = append(kept, queryPart{raw: add})
	}
	return joinQuery(kept)
}

// withoutPart is a search's text without its part i.
func withoutPart(text string, i int) string {
	parts := splitQuery(text)
	if i < 0 || i >= len(parts) {
		return text
	}
	return joinQuery(append(parts[:i:i], parts[i+1:]...))
}

// operator writes an operator with a value, quoted when it has spaces. A
// search has no way to escape a quote, so a value loses its quotes; an
// empty value writes nothing.
func operator(op, value string) string {
	value = strings.TrimSpace(strings.Join(strings.FieldsFunc(value, isQuoteRune), ""))
	if value == "" {
		return ""
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		value = `"` + value + `"`
	}
	return op + ":" + value
}

// dayOperator writes after: or before: with a day, as the engine reads it
// back in searchZone.
func dayOperator(op string, day time.Time) string {
	return op + ":" + day.In(searchZone).Format("2006-01-02")
}

// fieldNames names the parts of a message a search found its terms in, in
// the order a header lists them.
func fieldNames(f mail.Field) []string {
	var out []string
	for _, part := range []struct {
		field mail.Field
		name  func() string
	}{
		{mail.FieldFrom, locales.FieldFrom},
		{mail.FieldTo, locales.FieldTo},
		{mail.FieldSubject, locales.FieldSubject},
		{mail.FieldBody, locales.FieldBody},
		{mail.FieldLabel, locales.FieldLabel},
	} {
		if f.Has(part.field) {
			out = append(out, part.name())
		}
	}
	return out
}

// matchedLine says where a message's search terms were found, or "" for a
// search of flags, dates or folders alone, which the filters on screen
// already say.
func matchedLine(f mail.Field) string {
	names := fieldNames(f)
	if len(names) == 0 {
		return ""
	}
	return locales.MatchedFields(strings.Join(names, locales.ListSeparator()))
}

// convMatchedLine says which messages of a conversation a search found,
// and where in them.
func convMatchedLine(c mail.Conversation) string {
	var fields mail.Field
	for _, m := range c.Found {
		fields |= m.Fields
	}
	names := fieldNames(fields)
	if len(names) == 0 {
		return ""
	}
	return locales.MatchedInMessages(len(c.Found), strings.Join(names, locales.ListSeparator()))
}

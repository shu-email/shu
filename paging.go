package main

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

const (
	// listPage is how many rows a read of the list adds, and listAhead
	// how near the end of the rows read the user scrolls before the next
	// page is read: it is there before they reach the end.
	listPage  = 100
	listAhead = 20
	// olderTimeout bounds a fetch of older mail, which may bring the
	// settings' window of messages from a slow server.
	olderTimeout = 2 * time.Minute
	// endKey is the key of the row after the list's last.
	endKey = "end"
)

// listQuery is what the rows of the list are a read of: an account's
// folder, its starred mail, a label's mail or the unified inbox, searched
// or not, in a scope, by conversation or by message.
type listQuery struct {
	account, folder, search string
	// scope is the search's, scopeHere when there is none.
	scope searchScope
	// narrowed is set when the search's account: parts keep only some
	// accounts: accounts, by ID and one per line, which may be none.
	narrowed bool
	accounts string
	threaded bool
}

func (a *app) listQuery() listQuery {
	q := listQuery{account: a.active, folder: a.folder, search: strings.TrimSpace(a.query), threaded: a.threaded()}
	if q.search != "" {
		q.scope = a.searchScope()
		ids, narrowed := a.queryAccounts(a.parsedQuery())
		q.narrowed, q.accounts = narrowed, strings.Join(ids, "\n")
	}
	return q
}

// isFolder reports whether the list is one folder of the server, whose
// older mail can be fetched.
func (q listQuery) isFolder() bool {
	return q.folder != "" && q.folder != "starred" && q.folder != draftsFolder && q.folder != remindersFolder &&
		q.folder != followUpFolder && q.folder != allInboxes && labelOf(q.folder) == 0 && q.scope == scopeHere
}

// drafts reports whether the list is of the drafts, which their own read
// lists rather than a page of the cache.
func (q listQuery) drafts() bool { return q.folder == draftsFolder && q.scope == scopeHere }

// mailScope is the part of the cache the list reads: that of its scope,
// in the accounts the search's account: parts keep.
func (q listQuery) mailScope() mail.Scope {
	var sc mail.Scope
	switch {
	case q.scope == scopeAll:
	case q.scope == scopeAccount:
		sc = mail.AccountScope(q.account, "")
	case q.folder == allInboxes:
		sc = mail.AllInboxes()
	case q.folder == remindersFolder:
		sc = mail.RemindersScope(q.account)
	case q.folder == followUpFolder:
		// The store's clock decides what waits for a reply now.
		sc = mail.FollowUpScope(q.account, time.Time{})
	case labelOf(q.folder) != 0:
		sc = mail.LabelScope(q.account, labelOf(q.folder))
	default:
		sc = mail.AccountScope(q.account, q.folder)
	}
	if q.narrowed {
		var keep []string
		for _, id := range strings.Split(q.accounts, "\n") {
			if id != "" && (len(sc.Accounts) == 0 || slices.Contains(sc.Accounts, id)) {
				keep = append(keep, id)
			}
		}
		sc.Accounts = keep
	}
	return sc
}

// none reports whether the search's account: parts keep none of the
// scope's accounts: a scope of no account, which the engine would read
// as every account's.
func (q listQuery) none() bool { return q.narrowed && len(q.mailScope().Accounts) == 0 }

// read is the engine's query of a page of the list. The typed text is
// read as the engine reads a search, operators and all, but for the
// account: parts the scope applies.
func (q listQuery) read(after mail.Cursor, limit int) mail.ListQuery {
	lq := mail.ListQuery{Scope: q.mailScope(), After: after, Limit: limit}
	if q.search != "" {
		lq.Search = parseQuery(q.search).Search
	}
	return lq
}

// pager is how far the list has read the cache: the rows on screen are the
// first pages of query, newest first.
type pager struct {
	query listQuery
	// next is where the next page starts and more whether the cache has
	// rows after those read; reading is a page being read to add, and
	// failed why the last one could not be.
	next    mail.Cursor
	more    bool
	reading bool
	failed  string
	// reads counts the reads of the list from the top, only the latest of
	// which shows, and spin is set while one the user waits for runs.
	// rows counts the changes of the rows read: a page read while they
	// changed does not continue them.
	reads uint64
	spin  bool
	rows  uint64
}

// olderMail is what the cache holds of a folder against what the server
// has, and the fetch of the older mail it lacks.
type olderMail struct {
	cache    mail.FolderCache
	known    bool
	fetching bool
	err      string
}

// olderOf is the state of an account's folder, kept while the window runs
// so that a fetch going on shows when the folder shows again.
func (a *app) olderOf(account, folder string) *olderMail {
	key := folderKey(account, folder)
	o := a.older[key]
	if o == nil {
		o = &olderMail{}
		a.older[key] = o
	}
	return o
}

// olderAt is olderOf for views, which read it and keep nothing.
func (a *app) olderAt(account, folder string) olderMail {
	if o := a.older[folderKey(account, folder)]; o != nil {
		return *o
	}
	return olderMail{}
}

func folderKey(account, folder string) string { return url.PathEscape(account) + "/" + folder }

// rowID names a row: an account's message, or an account's conversation.
// It is comparable and costs no allocation, as keys do.
type rowID struct {
	account, folder string
	uid             mail.UID
	thread          mail.ThreadID
}

// rowMark is where a row of the list was.
type rowMark struct {
	id   rowID
	date time.Time
}

// listRows is a read of the list: its messages and, in conversation view,
// its conversations, row for row.
type listRows struct {
	msgs  []mail.Summary
	convs []mail.Conversation
	next  mail.Cursor
	more  bool
}

func (r *listRows) id(i int) rowID {
	if r.convs != nil {
		return rowID{account: r.convs[i].Account, thread: r.convs[i].Thread}
	}
	m := r.msgs[i]
	return rowID{account: m.Account, folder: m.Folder, uid: m.UID}
}

func (r *listRows) add(more listRows) {
	r.msgs = append(r.msgs, more.msgs...)
	if more.convs != nil {
		r.convs = append(r.convs, more.convs...)
	}
	r.next, r.more = more.next, more.more
}

// index is where a row is among those read, or -1.
func (r *listRows) index(mark rowMark) int {
	for i := range r.msgs {
		if r.id(i) == mark.id && r.msgs[i].Date.Equal(mark.date) {
			return i
		}
	}
	return -1
}

// reaches reports whether the rows go as far as a row did: they hold it
// where it was, or one older than it. A conversation that came up with a
// new message is not where it was.
func (r *listRows) reaches(mark rowMark) bool {
	n := len(r.msgs)
	if n == 0 {
		return false
	}
	if r.msgs[n-1].Date.Before(mark.date) {
		return true
	}
	for i := n - 1; i >= 0; i-- {
		if r.id(i) == mark.id && r.msgs[i].Date.Equal(mark.date) {
			return true
		}
	}
	return false
}

// mark is where row i of the list is.
func (a *app) mark(i int) rowMark {
	if c := a.convAt(i); c != nil {
		return rowMark{id: rowID{account: c.Account, thread: c.Thread}, date: a.all[i].Date}
	}
	m := a.all[i]
	return rowMark{id: rowID{account: m.Account, folder: m.Folder, uid: m.UID}, date: m.Date}
}

// readRows reads a page of the list. It runs in a job.
func (a *app) readRows(ctx context.Context, q listQuery, after mail.Cursor, limit int) (listRows, error) {
	if q.none() {
		if q.threaded {
			return listRows{convs: []mail.Conversation{}}, nil
		}
		return listRows{}, nil
	}
	if q.threaded {
		page, err := a.svc.ConversationPage(ctx, q.read(after, limit))
		if page.Conversations == nil {
			page.Conversations = []mail.Conversation{}
		}
		return listRows{msgs: latestOf(page.Conversations), convs: page.Conversations, next: page.Next, more: page.More}, err
	}
	page, err := a.svc.MessagePage(ctx, q.read(after, limit))
	return listRows{msgs: page.Messages, next: page.Next, more: page.More}, err
}

// readTop reads the list from the top: want rows, and on until the row
// that was last, when there is one, so that a list read again keeps every
// row it showed while mail that came pushes them down. It reads on by as
// many rows as came above the row that was first. It runs in a job.
func (a *app) readTop(ctx context.Context, q listQuery, want int, first, last *rowMark) (listRows, error) {
	out, err := a.readRows(ctx, q, "", want)
	for n := 0; err == nil && last != nil && out.more && !out.reaches(*last); n = listPage {
		if n == 0 {
			n = listPage
			if i := out.index(*first); i > 0 {
				n = i
			}
		}
		var more listRows
		more, err = a.readRows(ctx, q, out.next, n)
		out.add(more)
	}
	return out, err
}

// setRows shows a read of the list from the top.
func (a *app) setRows(q listQuery, got listRows) {
	a.all, a.convs = got.msgs, got.convs
	p := &a.pager
	p.query, p.next, p.more = q, got.next, got.more
	p.reading, p.failed = false, ""
	p.rows++
}

// clearRows empties the list for another folder, account or view: a page
// still being read for the rows that were is dropped.
func (a *app) clearRows() {
	a.all, a.convs, a.conv = nil, nil, nil
	p := &a.pager
	p.query, p.next, p.more = listQuery{}, "", false
	p.reading, p.failed = false, ""
	p.rows++
}

// loadMore reads the next page of the list and adds it at the end, which
// the user is scrolling to. The rows read, where the list is scrolled and
// the rows chosen stay as they are. A page that failed is read again only
// when the user asks.
func (a *app) loadMore() {
	p := &a.pager
	if !p.more || p.reading || p.failed != "" || p.query != a.listQuery() {
		return
	}
	q, after, rows := p.query, p.next, p.rows
	p.reading = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		got, err := a.readRows(ctx, q, after, listPage)
		a.post(func() {
			// The list was read again meanwhile, from the top, or is
			// another's: this page continues neither.
			if p.rows != rows || p.query != q {
				return
			}
			p.reading = false
			if err != nil {
				p.failed = locales.LoadMoreFailed(err.Error())
				return
			}
			a.addRows(got)
		})
	})
}

// retryMore reads again the page that could not be read.
func (a *app) retryMore() {
	a.pager.failed = ""
	a.loadMore()
}

// addRows adds a page after the rows read. A conversation whose newest
// message left the folder is now further down, and may come again: it
// stays where it was until the list is read again from the top.
func (a *app) addRows(got listRows) {
	had := make(map[rowID]bool, len(got.msgs))
	for i := range got.msgs {
		had[got.id(i)] = true
	}
	shown := listRows{msgs: a.all, convs: a.convs}
	dup := map[rowID]bool{}
	for i := range shown.msgs {
		if id := shown.id(i); had[id] {
			dup[id] = true
		}
	}
	for i := range got.msgs {
		if dup[got.id(i)] {
			continue
		}
		a.all = append(a.all, got.msgs[i])
		if got.convs != nil {
			a.convs = append(a.convs, got.convs[i])
		}
	}
	p := &a.pager
	p.next, p.more = got.next, got.more
	p.rows++
}

// fetchOlder fetches from the server the older mail of the folder on
// show that the cache lacks, as the end of its list offers. Only the user
// asks for it: scrolling never reaches the network.
func (a *app) fetchOlder() {
	q := a.listQuery()
	if !q.isFolder() {
		return
	}
	o := a.olderOf(q.account, q.folder)
	if o.fetching {
		return
	}
	o.fetching, o.err = true, ""
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), olderTimeout)
		defer cancel()
		result, err := a.svc.FetchOlder(ctx, q.account, q.folder, 0)
		a.post(func() {
			o.fetching = false
			switch {
			case a.signInRequired(q.account, err):
				o.err = locales.OlderNeedsSignIn()
				return
			case err != nil:
				o.err = olderError(err)
				return
			}
			o.cache.Complete = !result.More
			if a.active != q.account || a.folder != q.folder {
				// The folder's list is read when it shows again.
				return
			}
			if result.Reset {
				a.restartList()
				return
			}
			// What came is read with the list, which goes on to it from
			// where the user is; a message a sync had left out shows in
			// its place.
			a.readList(a.gen, false, false)
		})
	})
}

// restartList reads the folder's list from its top again, as after the
// server renumbered its messages: a UID chosen or read before may be
// another message's now.
func (a *app) restartList() {
	a.gen++
	a.selected = -1
	a.setUndo(nil)
	a.list = ui.ListState{}
	a.sel.Clear()
	a.bodies = map[string]mail.FetchResult{}
	a.clearAI()
	a.clearRows()
	a.loadMessages(a.gen)
}

// listEnd is what the row after the list's last says.
type listEnd int

const (
	endNone listEnd = iota
	// endLoading: the next page is being read; endLoadFailed: it could
	// not be.
	endLoading
	endLoadFailed
	// endFetching: older mail is being fetched from the server;
	// endFetchFailed: it could not be.
	endFetching
	endFetchFailed
	// endOnServer: the cache has no more of the folder, the server has
	// older mail; endComplete: the cache has all of it.
	endOnServer
	endComplete
	// endNoMore: no more rows of starred mail or of a search.
	endNoMore
)

// endState is what comes after the list's last row, or why nothing does.
func (a *app) endState() listEnd {
	p := &a.pager
	q := a.listQuery()
	switch {
	case q.drafts() || len(a.all) == 0:
		return endNone
	case p.failed != "":
		return endLoadFailed
	case p.more:
		return endLoading
	case !q.isFolder():
		return endNoMore
	}
	return a.olderState(q)
}

// olderState is where the older mail of a folder stands, once the list
// has read all the cache holds of it.
func (a *app) olderState(q listQuery) listEnd {
	o := a.olderAt(q.account, q.folder)
	switch {
	case o.fetching:
		return endFetching
	case o.err != "":
		return endFetchFailed
	case o.known && o.cache.Complete:
		return endComplete
	}
	return endOnServer
}

// emptyOlder is what an empty list offers of its folder's older mail:
// endNone unless the server is known to have mail the cache lacks, as
// when every message cached was archived.
func (a *app) emptyOlder() listEnd {
	q := a.listQuery()
	if a.loading || !q.isFolder() {
		return endNone
	}
	o := a.olderAt(q.account, q.folder)
	switch state := a.olderState(q); {
	case state == endFetching || state == endFetchFailed:
		return state
	case state == endOnServer && o.known && o.cache.Total > o.cache.Cached:
		return state
	}
	return endNone
}

// olderControls draws, in an empty list, the fetch of the folder's older
// mail: its progress, its failure, or the button that starts it.
func (a *app) olderControls(c *ui.Context, t *ui.Theme, state listEnd) {
	switch state {
	case endFetching:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.FetchingOlder()).FontSize(12).TextColor(t.TextMuted)
		})
	case endFetchFailed:
		q := a.listQuery()
		ui.Text(c, a.olderAt(q.account, q.folder).err).FontSize(12).TextColor(t.Warning).TextAlign(ui.Center).MaxLines(3)
		a.retryOlderButton(c, q.account)
	case endOnServer:
		ui.Button(c, locales.LoadOlder()).OnClick(a.fetchOlder)
	}
}

// endText is what the row after the last says, which assistive
// technology reads as its name.
func (a *app) endText(state listEnd) string {
	q := a.listQuery()
	switch state {
	case endLoading:
		return locales.LoadingMore()
	case endLoadFailed:
		return a.pager.failed
	case endFetching:
		return locales.FetchingOlder()
	case endFetchFailed:
		return a.olderAt(q.account, q.folder).err
	case endComplete:
		if q.search != "" {
			return locales.NoMoreResults()
		}
		return locales.AllOnDevice()
	case endOnServer:
		if q.search != "" {
			return locales.NoMoreResultsHere()
		}
		return locales.NoMoreHere()
	case endNoMore:
		switch {
		case q.search != "":
			// A search reads the cache alone, whatever its scope.
			return locales.NoMoreResultsHere()
		case q.folder == allInboxes, labelOf(q.folder) != 0:
			return locales.NoMoreHere()
		}
		return locales.NoMoreMail()
	}
	return ""
}

// endHint is the line under the end of a list that reads several folders
// from the cache: older mail comes from the server in a folder's own
// list, one folder at a time.
func (a *app) endHint(state listEnd) string {
	q := a.listQuery()
	if state == endNoMore && (q.folder == allInboxes || labelOf(q.folder) != 0 || q.search != "" && q.scope != scopeHere) {
		return locales.OlderInFolderHint()
	}
	return ""
}

// endRow draws the row after the list's last: the next page on its way,
// the way to older mail, or that there is no more.
func (a *app) endRow(c *ui.Context, t *ui.Theme, state listEnd) {
	text := a.endText(state)
	ui.Column(c).Padding(14, 14, 18, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).MaxWidth(340).Children(func() {
			color, beside := t.TextMuted, true
			switch state {
			case endLoading, endFetching:
				ui.Spinner(c).Size(14, 14)
			case endLoadFailed, endFetchFailed:
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				color = t.Text
			case endComplete:
				ui.Icon(c, checkIcon).FontSize(14).TextColor(t.TextMuted)
			default:
				beside = false
			}
			line := ui.Text(c, text).FontSize(12).TextColor(color).MaxLines(3).Shrink(1)
			if !beside {
				line.TextAlign(ui.Center)
			}
		})
		if hint := a.endHint(state); hint != "" {
			ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(320).MaxLines(3)
		}
		switch state {
		case endLoadFailed:
			ui.Button(c, locales.Retry()).Label(locales.RetryLoadMore()).OnClick(a.retryMore)
		case endFetchFailed:
			a.retryOlderButton(c, a.listQuery().account)
		case endOnServer:
			ui.Button(c, locales.LoadOlder()).OnClick(a.fetchOlder)
		}
	})
}

// retryOlderButton fetches older mail again after a failure. An account
// that has to sign in again gets none: a fetch could only fail the same
// way, and the banner above the list offers the sign-in.
func (a *app) retryOlderButton(c *ui.Context, account string) {
	if a.needSignIn[account] {
		return
	}
	ui.Button(c, locales.Retry()).Label(locales.RetryFetchOlder()).OnClick(a.fetchOlder)
}

// cacheLine is the list header's line on a folder that is not searched:
// how much of it is on this device, and how much of it is unread.
func (a *app) cacheLine(f mail.Folder) string {
	o := a.olderAt(a.active, f.Name)
	if !o.known {
		total := f.Total
		if total == 0 {
			total = len(a.all)
		}
		return locales.MailboxSummary(total, f.Unread)
	}
	if o.cache.Complete || o.cache.Cached >= o.cache.Total {
		return locales.MailboxSummary(max(o.cache.Total, o.cache.Cached), f.Unread)
	}
	return locales.OnDeviceSummary(o.cache.Cached, o.cache.Total, f.Unread)
}

// olderError says why older mail could not be fetched. A server that took
// too long is said in the user's words; other errors keep their own.
func olderError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return locales.FetchOlderFailed(locales.ServerTooSlow())
	}
	return locales.FetchOlderFailed(err.Error())
}

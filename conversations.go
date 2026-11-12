package main

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// threadKey is the key of a conversation's row in the list, and of its
// summary. A conversation is one account's, and two accounts' rows may
// share a number.
func threadKey(account string, id mail.ThreadID) string {
	return "thread/" + url.PathEscape(account) + "/" + strconv.FormatInt(int64(id), 10)
}

// convRef names a conversation among those of every account.
type convRef struct {
	account string
	thread  mail.ThreadID
}

func refOf(c mail.Conversation) convRef { return convRef{c.Account, c.Thread} }

// threaded reports whether the list shows conversations. The Drafts entry
// lists its drafts one by one in either view: a draft of this device is in
// no conversation of the cache.
func (a *app) threaded() bool { return a.mailSet.Conversations && !a.draftsList() }

// draftsList reports whether the list is the Drafts entry's drafts, and
// not a search of it that reaches further.
func (a *app) draftsList() bool {
	return a.folder == draftsFolder && (strings.TrimSpace(a.query) == "" || a.searchScope() == scopeHere)
}

// rowKey is the identity of row i of the list: its conversation's in
// conversation view, its message's otherwise.
func (a *app) rowKey(i int) string {
	if i < 0 || i >= len(a.all) {
		return ""
	}
	if c := a.convAt(i); c != nil {
		return threadKey(c.Account, c.Thread)
	}
	return summaryKey(a.all[i])
}

// convAt is the conversation of row i, or nil when the list shows
// messages.
func (a *app) convAt(i int) *mail.Conversation {
	if !a.threaded() || i < 0 || i >= len(a.convs) {
		return nil
	}
	return &a.convs[i]
}

// rowOf is the row of a conversation, or -1.
func (a *app) rowOf(ref convRef) int {
	for i := range a.convs {
		if refOf(a.convs[i]) == ref {
			return i
		}
	}
	return -1
}

// latestOf is the message each row of conversations shows: its newest.
func latestOf(convs []mail.Conversation) []mail.Summary {
	out := make([]mail.Summary, len(convs))
	for i, c := range convs {
		out[i] = c.Latest
	}
	return out
}

func hasThread(convs []mail.Conversation, ref convRef) bool {
	for _, c := range convs {
		if refOf(c) == ref {
			return true
		}
	}
	return false
}

// threadOf reads which conversation a cached message is in now. It runs
// in a job.
func (a *app) threadOf(ctx context.Context, m mail.Summary) mail.ThreadID {
	result, _, err := a.svc.CachedMessage(ctx, m.Account, m.Folder, m.UID)
	if err != nil {
		return 0
	}
	return result.Message.Thread
}

// findChosen chooses again the row of the message that was chosen when
// its row's key is gone from the list: its conversation merged into
// another, found, or the list changed between conversations and messages.
func (a *app) findChosen(prev mail.Summary, found mail.ThreadID) {
	if prev.UID == 0 {
		return
	}
	for i := range a.all {
		hit := summaryKey(a.all[i]) == summaryKey(prev)
		if c := a.convAt(i); c != nil {
			hit = c.Account == prev.Account &&
				((found != 0 && c.Thread == found) || (prev.Thread != 0 && c.Thread == prev.Thread))
		}
		if hit {
			a.selected = i
			return
		}
	}
}

func refsOf(list []mail.Summary) []mail.MessageRef {
	out := make([]mail.MessageRef, 0, len(list))
	for _, m := range list {
		out = append(out, mail.MessageRef{Folder: m.Folder, UID: m.UID})
	}
	return out
}

// byAccount splits messages by their account: an action reaches each
// account on its own.
func byAccount(list []mail.Summary) (accounts []string, of map[string][]mail.Summary) {
	of = map[string][]mail.Summary{}
	for _, m := range list {
		if _, ok := of[m.Account]; !ok {
			accounts = append(accounts, m.Account)
		}
		of[m.Account] = append(of[m.Account], m)
	}
	return accounts, of
}

// refKey is the key of an account's message a reference names.
func refKey(account string, r mail.MessageRef) string { return messageKey(account, r.Folder, r.UID) }

// convView is the conversation open in the reading pane.
type convView struct {
	account string
	thread  mail.ThreadID
	// scope is the list's folder it was opened from, and query the search
	// whose matches it expanded.
	scope string
	query string
	// msgs are the messages shown, oldest first: each once, and those in
	// Drafts, Junk and Trash only where the list shows them.
	msgs []mail.Summary
	// members are its messages in the scope: what its row stands for, and
	// what the toolbar acts on.
	members []mail.Summary
	// found are, by key, the messages shown that the search found, with
	// the parts its terms were found in.
	found map[string]mail.Field
	// open and details hold, by message key, the messages expanded and
	// those showing their header fields. html is the message shown as
	// HTML: the window has one web view.
	open    map[string]bool
	details map[string]bool
	html    string
	// err is why the conversation could not be read, bodyErr why the body
	// of a message could not, by key, and fetching the bodies on the way.
	err      string
	bodyErr  map[string]string
	fetching map[string]bool
}

func newConvView(ref convRef, scope string) *convView {
	return &convView{
		account: ref.account, thread: ref.thread, scope: scope,
		open: map[string]bool{}, details: map[string]bool{},
		bodyErr: map[string]string{}, fetching: map[string]bool{},
	}
}

// message is the shown message with a key.
func (cv *convView) message(key string) (mail.Summary, bool) {
	for _, m := range cv.msgs {
		if summaryKey(m) == key {
			return m, true
		}
	}
	return mail.Summary{}, false
}

// update changes the shown message and its member with a key.
func (cv *convView) update(key string, change func(*mail.Summary)) {
	for _, list := range [][]mail.Summary{cv.msgs, cv.members} {
		for i := range list {
			if summaryKey(list[i]) == key {
				change(&list[i])
			}
		}
	}
}

// drop takes a message out of the conversation on show.
func (cv *convView) drop(key string) {
	keep := func(list []mail.Summary) []mail.Summary {
		out := list[:0:0]
		for _, m := range list {
			if summaryKey(m) != key {
				out = append(out, m)
			}
		}
		return out
	}
	cv.msgs, cv.members = keep(cv.msgs), keep(cv.members)
	delete(cv.open, key)
	delete(cv.details, key)
	if cv.html == key {
		cv.html = ""
	}
}

// ref names the conversation the pane shows.
func (cv *convView) ref() convRef { return convRef{cv.account, cv.thread} }

// openRow is the row of the conversation on show, or -1.
func (a *app) openRow() int {
	if a.conv == nil || !a.threaded() {
		return -1
	}
	return a.rowOf(a.conv.ref())
}

// loadConversation shows the conversation of the chosen row: opened as it
// first looks when it is another than the one on show, and else read
// again as the cache has it now, which a sync or an action may change.
func (a *app) loadConversation(g uint64) {
	row := a.convAt(a.selected)
	if row == nil {
		a.conv = nil
		return
	}
	ref, folder, scope := refOf(*row), a.folder, a.listScope()
	query := strings.TrimSpace(a.query)
	found := append([]mail.Match(nil), row.Found...)
	key := threadKey(ref.account, ref.thread)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		all, err := a.svc.CachedConversation(ctx, ref.account, "", ref.thread)
		var members []mail.Summary
		if err == nil {
			members, err = a.svc.ScopedConversation(ctx, ref.account, scope, ref.thread)
		}
		a.post(func() {
			if a.stale(g) || a.folder != folder || a.selectedKey() != key {
				return
			}
			if err != nil {
				cv := newConvView(ref, folder)
				cv.err = locales.ConversationFailed(err.Error())
				a.conv = cv
				return
			}
			if len(all) == 0 || len(members) == 0 {
				// It merged into another conversation since the list was
				// read, or left the list: reading it again shows where.
				a.readList(g, false, false)
				return
			}
			a.showConversation(g, ref, folder, query, all, members, found)
		})
	})
}

// shownMessages are the messages of a conversation the reading pane
// shows, oldest first: each once, the copy the row stands for first, and
// those in Drafts, Junk and Trash only when the list is that folder, the
// row stands for them or a search found them. alias names, for each copy
// left out, the one shown in its place.
func (a *app) shownMessages(account string, all, members []mail.Summary, matches []mail.MessageRef) (shown []mail.Summary, alias map[string]string) {
	member := map[string]bool{}
	for _, m := range members {
		member[summaryKey(m)] = true
	}
	found := map[string]bool{}
	for _, r := range matches {
		found[refKey(account, r)] = true
	}
	hidden := map[string]bool{}
	for _, special := range []string{mail.SpecialDrafts, mail.SpecialJunk, mail.SpecialTrash} {
		if name := a.specialFolder(account, special); name != "" && name != a.folder {
			hidden[name] = true
		}
	}
	alias = map[string]string{}
	byID := map[string]int{}
	for _, m := range all {
		key := summaryKey(m)
		if hidden[m.Folder] && !member[key] && !found[key] {
			continue
		}
		id := messageIDKey(m.MessageID)
		i, seen := byID[id]
		if id == "" || !seen {
			if id != "" {
				byID[id] = len(shown)
			}
			shown = append(shown, m)
			continue
		}
		// Another copy of a message shown, as Gmail's labels and mail to
		// oneself make: the one the row stands for is the one shown.
		if member[key] && !member[summaryKey(shown[i])] {
			alias[summaryKey(shown[i])] = key
			shown[i] = m
		} else {
			alias[key] = summaryKey(shown[i])
		}
	}
	return shown, alias
}

// messageIDKey is a Message-ID as copies of one message share it.
func messageIDKey(id string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(id), "<"), ">"))
}

// showConversation puts a conversation read from the cache in the reading
// pane. Opened anew, its newest message, the unread ones and those a
// search found are expanded, and the unread among them marked read. Read
// again, it keeps what the user expanded and collapsed, and a message
// that came meanwhile stays collapsed: nothing is marked read that the
// user did not open. A new search expands what it found in it.
func (a *app) showConversation(g uint64, ref convRef, scope, query string, all, members []mail.Summary, matches []mail.Match) {
	refs := make([]mail.MessageRef, len(matches))
	for i, m := range matches {
		refs[i] = m.MessageRef
	}
	msgs, alias := a.shownMessages(ref.account, all, members, refs)
	cv := a.conv
	again := cv != nil && cv.err == "" && cv.scope == scope && cv.account == ref.account &&
		(cv.thread == ref.thread || sharesMessage(cv.msgs, msgs))
	found := map[string]mail.Field{}
	for _, r := range matches {
		k := refKey(ref.account, r.MessageRef)
		if shown, ok := alias[k]; ok {
			k = shown
		}
		found[k] |= r.Fields
	}
	var expand []string
	for i, m := range msgs {
		k := summaryKey(m)
		_, hit := found[k]
		switch {
		case !again && (i == len(msgs)-1 || !m.Seen || hit):
			expand = append(expand, k)
		case again && query != cv.query && hit:
			expand = append(expand, k)
		}
	}
	if !again {
		cv = newConvView(ref, scope)
	}
	cv.query, cv.found = query, found
	if k := a.convExpand; k != "" {
		a.convExpand = ""
		if shown, ok := alias[k]; ok {
			k = shown
		}
		expand = append(expand, k)
	}
	cv.thread, cv.msgs, cv.members = ref.thread, msgs, members
	present := map[string]bool{}
	for _, m := range msgs {
		present[summaryKey(m)] = true
	}
	for k := range cv.open {
		if !present[k] {
			delete(cv.open, k)
		}
	}
	a.conv = cv
	a.expand(g, expand)
}

func sharesMessage(a, b []mail.Summary) bool {
	keys := map[string]bool{}
	for _, m := range a {
		keys[summaryKey(m)] = true
	}
	for _, m := range b {
		if keys[summaryKey(m)] {
			return true
		}
	}
	return false
}

// toggleMessage expands a message of the conversation on show, or
// collapses it. It is a click on its header, or Enter or Space there.
func (a *app) toggleMessage(key string) {
	cv := a.conv
	if cv == nil {
		return
	}
	if cv.open[key] {
		delete(cv.open, key)
		if cv.html == key {
			cv.html = ""
		}
		return
	}
	a.expand(a.gen, []string{key})
}

// expand expands messages of the conversation on show, loads the bodies
// the open ones lack and marks the unread among those expanded read.
func (a *app) expand(g uint64, keys []string) {
	cv := a.conv
	if cv == nil {
		return
	}
	var unread []mail.Summary
	done := map[string]bool{}
	for _, k := range keys {
		m, ok := cv.message(k)
		if !ok || done[k] || cv.open[k] && m.Seen {
			continue
		}
		done[k] = true
		cv.open[k] = true
		if !m.Seen {
			unread = append(unread, m)
		}
	}
	var load []mail.Summary
	for _, m := range cv.msgs {
		k := summaryKey(m)
		if _, have := a.bodies[k]; cv.open[k] && !have && !cv.fetching[k] && cv.bodyErr[k] == "" {
			load = append(load, m)
		}
	}
	a.seenInThread(unread, true)
	a.loadBodies(g, cv, load, unread)
}

// loadBodies reads bodies of the conversation on show, from the cache or
// else the server, and then marks the messages of unread read on the
// server. One job reads them in turn, the newest first: a long
// conversation opened does not open a connection for each message at
// once.
func (a *app) loadBodies(g uint64, cv *convView, load, unread []mail.Summary) {
	if len(load) == 0 && len(unread) == 0 {
		return
	}
	marked := map[string]bool{}
	for _, m := range unread {
		marked[summaryKey(m)] = true
	}
	for _, m := range load {
		cv.fetching[summaryKey(m)] = true
	}
	accountID := cv.account
	refs := refsOf(unread)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		for i := len(load) - 1; i >= 0; i-- {
			m := load[i]
			result, cached, err := a.svc.CachedMessage(ctx, m.Account, m.Folder, m.UID)
			if err == nil && !cached {
				result, err = a.svc.FetchMessage(ctx, m.Account, m.Folder, m.UID, false)
			}
			key := summaryKey(m)
			a.post(func() {
				delete(cv.fetching, key)
				if a.stale(g) {
					return
				}
				if err != nil {
					cv.bodyErr[key] = a.bodyError(m.Account, err)
					return
				}
				if marked[key] {
					result.Message.Seen = true
				}
				a.bodies[key] = result
				if a.conv == cv && cv.html == key {
					a.syncHTMLView()
				}
			})
		}
		if len(refs) > 0 {
			// As when a message is opened in the list of messages, the
			// window already shows them read.
			_ = a.svc.SetFlags(ctx, accountID, refs, mail.FlagSeen, true)
		}
	})
}

// retryBody loads again a body that could not be loaded.
func (a *app) retryBody(key string) {
	cv := a.conv
	if cv == nil {
		return
	}
	delete(cv.bodyErr, key)
	delete(a.bodies, key)
	if m, ok := cv.message(key); ok {
		a.loadBodies(a.gen, cv, []mail.Summary{m}, nil)
	}
}

// seenInThread shows messages of the conversation on show as read or
// unread, with its row and the folders' unread counts in step.
func (a *app) seenInThread(list []mail.Summary, seen bool) {
	cv := a.conv
	if cv == nil || len(list) == 0 {
		return
	}
	for _, m := range list {
		if m.Seen == seen {
			continue
		}
		cv.update(summaryKey(m), func(s *mail.Summary) { s.Seen = seen })
		a.addUnread(m.Account, m.Folder, unreadDelta(seen))
		a.labelUnread(m, unreadDelta(seen))
	}
	a.syncRow()
	a.updateBadge()
}

// unreadDelta is what marking a message read, or unread, does to its
// folder's unread count.
func unreadDelta(seen bool) int {
	if seen {
		return -1
	}
	return 1
}

// addUnread changes an account's folder's unread count by n, as the
// window shows it until the server's comes.
func (a *app) addUnread(account, folder string, n int) {
	folders := a.foldersFor(account)
	for i := range folders {
		if folders[i].Name == folder {
			folders[i].Unread = max(0, folders[i].Unread+n)
		}
	}
}

// syncRow makes the row of the conversation on show say what its messages
// now do: how many there are, whether any is unread, starred or has
// attachments, and the labels they have.
func (a *app) syncRow() {
	i := a.openRow()
	if i < 0 || len(a.conv.members) == 0 {
		return
	}
	members := a.conv.members
	c := &a.convs[i]
	c.Count = len(members)
	c.Unread, c.Flagged, c.HasAttachments, c.Labels = false, false, false, nil
	for _, m := range members {
		c.Unread = c.Unread || !m.Seen
		c.Flagged = c.Flagged || m.Flagged
		c.HasAttachments = c.HasAttachments || m.HasAttachments
		for _, l := range m.Labels {
			if !slices.Contains(c.Labels, l.ID) {
				c.Labels = append(c.Labels, l.ID)
			}
		}
	}
	slices.SortStableFunc(c.Labels, func(x, y mail.LabelID) int { return a.labelPlace(c.Account, x) - a.labelPlace(c.Account, y) })
	c.Latest = members[len(members)-1]
	a.all[i] = c.Latest
}

// threadTargets are the conversations the toolbar acts on: the rows
// chosen with Cmd or Shift, or the one chosen.
func (a *app) threadTargets() []mail.Conversation {
	if c := a.convAt(a.selected); c != nil && a.sel.Len() == 1 && a.sel.Has(a.rowKey(a.selected)) {
		// The usual case, which a long list need not be walked for.
		return []mail.Conversation{*c}
	}
	if a.sel.Len() > 0 {
		var out []mail.Conversation
		for i := range a.convs {
			if a.sel.Has(threadKey(a.convs[i].Account, a.convs[i].Thread)) {
				out = append(out, a.convs[i])
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if c := a.convAt(a.selected); c != nil {
		return []mail.Conversation{*c}
	}
	return nil
}

// shownMembers is what the pane shows the row of the conversation on show
// to stand for, when it is one of convs. It is taken before an action
// changes what the pane shows.
func (a *app) shownMembers(convs []mail.Conversation) map[convRef][]mail.Summary {
	cv := a.conv
	if cv == nil || !threadSet(convs)[cv.ref()] {
		return nil
	}
	return map[convRef][]mail.Summary{cv.ref(): append([]mail.Summary(nil), cv.members...)}
}

// scopeMessages is what the rows of conversations stood for when they
// were acted on: the messages the pane showed of the one on show, and for
// the others their messages in the list's scope as the cache has them,
// without any newer than the newest the row showed. Mail that came after
// the list was drawn is not acted on. It runs in a job.
func (a *app) scopeMessages(ctx context.Context, scope mail.Scope, convs []mail.Conversation, shown map[convRef][]mail.Summary) ([]mail.Summary, error) {
	var out []mail.Summary
	for _, c := range convs {
		if list := shown[refOf(c)]; len(list) > 0 {
			out = append(out, list...)
			continue
		}
		list, err := a.svc.ScopedConversation(ctx, c.Account, scope, c.Thread)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			if !m.Date.After(c.Latest.Date) {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func threadSet(convs []mail.Conversation) map[convRef]bool {
	out := make(map[convRef]bool, len(convs))
	for _, c := range convs {
		out[refOf(c)] = true
	}
	return out
}

// toggleThreadStar stars the chosen conversations, or unstars them when
// all are starred. A star goes on the newest message a row stands for,
// and comes off every one of them that has it.
func (a *app) toggleThreadStar() {
	targets := a.threadTargets()
	if len(targets) == 0 {
		return
	}
	on := false
	for _, c := range targets {
		on = on || !c.Flagged
	}
	shown := a.shownMembers(targets)
	chosen := threadSet(targets)
	for i := range a.convs {
		if chosen[refOf(a.convs[i])] {
			a.convs[i].Flagged = on
			a.convs[i].Latest.Flagged = on
			a.all[i].Flagged = on
		}
	}
	if cv := a.conv; cv != nil && chosen[cv.ref()] && len(cv.members) > 0 {
		latest := summaryKey(cv.members[len(cv.members)-1])
		for _, m := range cv.members {
			if k := summaryKey(m); k == latest || !on {
				cv.update(k, func(s *mail.Summary) { s.Flagged = on })
			}
		}
	}
	g, scope, folder := a.gen, a.listScope(), a.folder
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			msgs []mail.Summary
			err  error
		)
		if on {
			for _, c := range targets {
				msgs = append(msgs, c.Latest)
			}
		} else {
			var members []mail.Summary
			members, err = a.scopeMessages(ctx, scope, targets, shown)
			for _, m := range members {
				if m.Flagged {
					msgs = append(msgs, m)
				}
			}
		}
		if err == nil {
			err = a.setFlags(ctx, msgs, mail.FlagFlagged, on)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			switch {
			case err != nil:
				// The cache changed only where the server did: read
				// again, it shows what is so.
				a.err = failureText(err)
				a.readMessages(g, false)
			case folder == "starred" && !on:
				a.loadMessages(g)
			}
		})
	})
}

// setFlags changes a flag of messages, with a call for each account they
// are in: an action reaches one account at a time. It runs in a job.
func (a *app) setFlags(ctx context.Context, msgs []mail.Summary, flag mail.Flag, on bool) error {
	accounts, of := byAccount(msgs)
	for _, id := range accounts {
		if err := a.svc.SetFlags(ctx, id, refsOf(of[id]), flag, on); err != nil {
			return err
		}
	}
	return nil
}

// toggleThreadRead marks the chosen conversations read, every message the
// rows stand for, or unread when none of them is unread.
func (a *app) toggleThreadRead() {
	targets := a.threadTargets()
	if len(targets) == 0 {
		return
	}
	seen := false
	for _, c := range targets {
		seen = seen || c.Unread
	}
	shown := a.shownMembers(targets)
	chosen := threadSet(targets)
	for i := range a.convs {
		if chosen[refOf(a.convs[i])] {
			a.convs[i].Unread = !seen
			a.convs[i].Latest.Seen = seen
			a.all[i].Seen = seen
		}
	}
	if cv := a.conv; cv != nil && chosen[cv.ref()] {
		for _, m := range cv.members {
			cv.update(summaryKey(m), func(s *mail.Summary) { s.Seen = seen })
		}
	}
	g, scope := a.gen, a.listScope()
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		members, err := a.scopeMessages(ctx, scope, targets, shown)
		var changed []mail.Summary
		for _, m := range members {
			if m.Seen != seen {
				changed = append(changed, m)
			}
		}
		if err == nil {
			err = a.setFlags(ctx, changed, mail.FlagSeen, seen)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = failureText(err)
				a.readMessages(g, false)
				return
			}
			for _, m := range changed {
				a.addUnread(m.Account, m.Folder, unreadDelta(seen))
				a.labelUnread(m, unreadDelta(seen))
			}
			a.updateBadge()
		})
	})
}

// moveThreads archives, junks or deletes the chosen conversations: the
// messages their rows stand for, and no others. Their rows go at once and
// come back if the move fails.
func (a *app) moveThreads(special string) {
	// Each conversation goes to the folder of its own account, and stays
	// where that account has none or where the list is that folder.
	dests := map[string]string{}
	var targets []mail.Conversation
	for _, c := range a.threadTargets() {
		if a.moves(a.threadPlace(c.Account), special) {
			dests[c.Account] = a.specialFolder(c.Account, special)
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		return
	}
	// A search of every folder finds conversations with their messages in
	// Sent and Drafts as well. The user's own mail stays where it is, and
	// a draft in its folder, which the drafts' sync keeps.
	stay := map[string]bool{}
	if a.spansFolders() {
		for _, c := range targets {
			for _, special := range []string{mail.SpecialSent, mail.SpecialDrafts} {
				if name := a.specialFolder(c.Account, special); name != "" {
					stay[folderKey(c.Account, name)] = true
				}
			}
		}
	}
	shown := a.shownMembers(targets)
	chosen := threadSet(targets)
	keptAll, keptConvs := a.all[:0:0], a.convs[:0:0]
	for i, c := range a.convs {
		if !chosen[refOf(c)] {
			keptAll, keptConvs = append(keptAll, a.all[i]), append(keptConvs, c)
		}
	}
	a.all, a.convs = keptAll, keptConvs
	a.sel.Clear()
	a.setUndo(nil)
	if a.selected >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	if a.conv != nil && chosen[a.conv.ref()] {
		a.conv = nil
	}
	g, scope := a.gen, a.listScope()
	label := threadMovedLabel(special, len(targets))
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		members, err := a.scopeMessages(ctx, scope, targets, shown)
		members = slices.DeleteFunc(members, func(m mail.Summary) bool { return stay[folderKey(m.Account, m.Folder)] })
		moved := map[string][]mail.MoveResult{}
		if err == nil {
			accounts, of := byAccount(members)
			for _, id := range accounts {
				var done []mail.MoveResult
				done, err = a.svc.MoveMessages(ctx, id, refsOf(of[id]), dests[id])
				if len(done) > 0 {
					moved[id] = done
				}
				if err != nil {
					break
				}
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.movedOut(members, moved)
			if err != nil {
				a.err = failureText(err)
			}
			// What moved before a failure can be taken back as well.
			if canUndo(moved) {
				a.setUndo(&undoMove{label: label, moved: moved})
			}
			a.loadMessages(g)
		})
	})
}

// movedOut lowers the unread counts of the folders messages left. moved
// is what each account's move did.
func (a *app) movedOut(msgs []mail.Summary, moved map[string][]mail.MoveResult) {
	unread := map[string]bool{}
	for _, m := range msgs {
		if !m.Seen {
			unread[summaryKey(m)] = true
		}
	}
	for account, list := range moved {
		for _, r := range list {
			if unread[messageKey(account, r.From, r.UID)] {
				a.addUnread(account, r.From, -1)
			}
		}
	}
	a.updateBadge()
}

// canUndo reports whether the server said where any of the messages went.
func canUndo(moved map[string][]mail.MoveResult) bool {
	for _, list := range moved {
		for _, r := range list {
			if r.DestUID != 0 {
				return true
			}
		}
	}
	return false
}

func threadMovedLabel(special string, n int) string {
	switch special {
	case mail.SpecialTrash:
		return locales.ConversationsDeleted(n)
	case mail.SpecialJunk:
		return locales.ConversationsJunked(n)
	}
	return locales.ConversationsArchived(n)
}

// undoThreadMove takes back a move of several messages, then syncs the
// folders they went back to, so that their rows return, and reads the
// folders of each account it reached with their counts.
func (a *app) undoThreadMove(u *undoMove) {
	g, shown := a.gen, a.active
	accounts := slices.Sorted(maps.Keys(u.moved))
	if !slices.Contains(accounts, shown) {
		accounts = append(accounts, shown)
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err, syncErr error
		for _, account := range slices.Sorted(maps.Keys(u.moved)) {
			moved := u.moved[account]
			if e := a.svc.MoveBackMessages(ctx, account, moved); e != nil && err == nil {
				err = e
			}
			// Those that went back are synced even when others could not.
			var folders []string
			for _, r := range moved {
				if !slices.Contains(folders, r.From) {
					folders = append(folders, r.From)
				}
			}
			for _, f := range folders {
				if e := a.svc.SyncFolder(ctx, account, f, 0); e != nil && syncErr == nil {
					syncErr = e
				}
			}
		}
		lists := map[string][]mail.Folder{}
		for _, id := range accounts {
			if list, err := a.svc.CachedFolders(ctx, id); err == nil {
				lists[id] = list
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			switch {
			case err != nil:
				a.err = failureText(err)
			case syncErr != nil:
				a.err = locales.SyncFailed(syncErr.Error())
			}
			for _, id := range accounts {
				if list, ok := lists[id]; ok && (id != shown || a.active == shown) {
					a.setAccountFolders(id, list)
				}
			}
			a.updateBadge()
			a.loadMessages(g)
		})
	})
}

// flagMessage changes a flag of one message of the conversation on show,
// as its own menu does.
func (a *app) flagMessage(m mail.Summary, flag mail.Flag, on bool) {
	cv := a.conv
	if cv == nil {
		return
	}
	key := summaryKey(m)
	if flag == mail.FlagSeen {
		a.seenInThread([]mail.Summary{m}, on)
	} else {
		cv.update(key, func(s *mail.Summary) { s.Flagged = on })
		a.syncRow()
	}
	g, folder := a.gen, a.folder
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SetFlag(ctx, m.Account, m.Folder, m.UID, flag, on)
		a.post(func() {
			if a.stale(g) {
				return
			}
			switch {
			case err != nil:
				a.err = failureText(err)
				a.readMessages(g, false)
			case folder == "starred" && flag == mail.FlagFlagged && !on:
				a.readMessages(g, false)
			}
		})
	})
}

// moveMessage archives or deletes one message of the conversation on
// show. The conversation stays open while its row has other messages;
// with none left, the row goes as a conversation's does.
func (a *app) moveMessage(m mail.Summary, special string) {
	cv := a.conv
	dest := a.specialFolder(m.Account, special)
	if cv == nil || dest == "" || m.Folder == dest {
		return
	}
	row := a.openRow()
	cv.drop(summaryKey(m))
	a.setUndo(nil)
	if !m.Seen {
		a.addUnread(m.Account, m.Folder, -1)
		a.updateBadge()
	}
	if row >= 0 && len(cv.members) == 0 {
		a.all = append(a.all[:row:row], a.all[row+1:]...)
		a.convs = append(a.convs[:row:row], a.convs[row+1:]...)
		a.sel.Clear()
		if a.selected >= len(a.all) {
			a.selected = len(a.all) - 1
		}
		a.conv = nil
	} else {
		a.syncRow()
	}
	g := a.gen
	label := locales.Archived()
	if special == mail.SpecialTrash {
		label = locales.Deleted()
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		done, err := a.svc.MoveMessages(ctx, m.Account, []mail.MessageRef{{Folder: m.Folder, UID: m.UID}}, dest)
		moved := map[string][]mail.MoveResult{m.Account: done}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = failureText(err)
			} else if canUndo(moved) {
				a.setUndo(&undoMove{label: label, moved: moved})
			}
			a.loadMessages(g)
		})
	})
}

// newestShown is the message the toolbar's replies answer: the newest of
// the conversation on show that is in none of Drafts, Junk and Trash,
// unless the list is that folder, with its body when it is loaded.
func (a *app) newestShown() (*mail.Summary, *mail.Message) {
	cv := a.conv
	if cv == nil || a.openRow() != a.selected || len(cv.msgs) == 0 {
		return nil, nil
	}
	aside := map[string]bool{}
	for _, special := range []string{mail.SpecialDrafts, mail.SpecialJunk, mail.SpecialTrash} {
		if name := a.specialFolder(cv.account, special); name != "" && name != a.folder {
			aside[name] = true
		}
	}
	pick := len(cv.msgs) - 1
	for i := len(cv.msgs) - 1; i >= 0; i-- {
		if !aside[cv.msgs[i].Folder] {
			pick = i
			break
		}
	}
	m := cv.msgs[pick]
	if b, ok := a.bodies[summaryKey(m)]; ok {
		return &m, &b.Message
	}
	return &m, nil
}

// setView switches the list between conversations and messages, keeps the
// choice, and stores it. The row chosen after the switch shows the same
// mail as the one before.
func (a *app) setView(conversations bool) {
	a.convToggle = conversations
	if a.mailSet.Conversations == conversations {
		return
	}
	prev := a.chosenMessage()
	a.mailSet.Conversations = conversations
	a.saveMailSettings()
	if a.draftsList() {
		return
	}
	a.gen++
	a.setUndo(nil)
	a.clearTriage()
	a.sel.Clear()
	a.list = ui.ListState{}
	a.conv = nil
	if conversations && prev.UID != 0 {
		a.convExpand = summaryKey(prev)
	}
	a.readList(a.gen, true, true)
}

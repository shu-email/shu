package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"shu/mail"
)

// fakeService is the UI tests' mail engine. It answers at once and
// records what the window asked it to do.
type fakeService struct {
	accounts []mail.Account
	active   string
	folders  map[string][]mail.Folder
	messages map[string][]mail.Summary
	bodies   map[string]mail.Message
	pending  []mail.PendingSend

	testErr    error
	flagErr    error
	moveErr    error
	sendErr    error
	sendWarn   error
	retryErr   error
	password   map[string]string
	sent       []mail.Outgoing
	moved      []string
	moveBacks  []string
	flagCalls  []string
	attachment mail.Attachment

	// Drafts. The fake keeps them as the engine does: saving is local,
	// and SyncDrafts is the step that reaches the server, or fails to.
	drafts map[string]mail.Draft
	// draftBase and draftTick give each save a later time than the last,
	// without a clock.
	draftBase time.Time
	draftTick int
	draftUID  mail.UID
	// draftSyncErr is the server being unreachable, draftSaveErr and
	// discardErr the cache failing, adoptErr a draft that cannot be taken
	// over.
	draftSyncErr error
	draftSaveErr error
	discardErr   error
	adoptErr     error
	// onDraftSync runs as a sync begins: what another mail client did to
	// the server's drafts meanwhile. replaced marks the drafts it took
	// over, as the engine remembers them.
	onDraftSync func()
	replaced    map[string]bool
	draftSaves  int
	draftSyncs  int
	discarded   []string
	// draftFullSyncs counts, of the syncs, those that take the drafts
	// with large attachments too.
	draftFullSyncs int

	// Attachments. attachErr and removeErr are the engine refusing;
	// onAttach runs as a file is about to be copied in, and onSend while
	// a message is on its way, where a test reports progress, looks at
	// the window, or has the send fail.
	attachErr   error
	removeErr   error
	onAttach    func(path string)
	onSend      func(ctx context.Context, out mail.Outgoing) error
	attachCalls []string
	sweeps      int

	// Watching. watching is each watched account's report, which a test
	// calls as the engine's watcher would; watchLog is every call that
	// starts, stops or wakes a watcher, in order.
	watching    map[string]func(mail.WatchEvent)
	watchLog    []string
	settings    mail.Settings
	settingsErr error
	saved       []mail.Settings

	// Conversations. A message without a Thread is a conversation of its
	// own, under an ID given on first sight, as the engine puts every
	// message in one. bulkFlags records each SetFlags, movedRefs each
	// message MoveMessages moved, and convErr fails CachedConversation.
	ownThreads map[string]mail.ThreadID
	nextThread mail.ThreadID
	bulkFlags  []bulkFlag
	movedRefs  []string
	convErr    error

	// Pages. cursors are the rows the cursors handed out end at;
	// pageReads counts the page reads, onPage runs as each begins, and
	// pageErr fails them. syncCalls records each SyncFolder with its
	// limit.
	cursors   map[mail.Cursor]mail.Summary
	pageReads int
	onPage    func(q mail.ListQuery)
	pageErr   error
	syncCalls []string
	// Older mail. older holds, by listKey, the messages of a folder that
	// the server has and the cache lacks, newest first; FetchOlder moves
	// them into the cache. olderCalls records each FetchOlder, onOlder
	// runs as one begins, olderErr fails it and olderReset has it report
	// that the folder was synced anew. cacheErr fails FolderCache.
	older      map[string][]mail.Summary
	olderCalls []string
	onOlder    func()
	olderErr   error
	olderReset bool
	cacheErr   error
	// acts records every call that reads a body, changes a flag, moves a
	// message or sends, each with the account it reached:
	// "a2 SetFlag INBOX/1 \Flagged true".
	acts []string
}

// act records a call with its account.
func (f *fakeService) act(accountID, format string, args ...any) {
	f.acts = append(f.acts, accountID+" "+fmt.Sprintf(format, args...))
}

// actsOf are the calls that reached an account, without its name.
func (f *fakeService) actsOf(accountID string) []string {
	var out []string
	for _, a := range f.acts {
		if rest, ok := strings.CutPrefix(a, accountID+" "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// bulkFlag is one SetFlags call.
type bulkFlag struct {
	flag mail.Flag
	on   bool
	refs []string
}

func newFakeService() *fakeService {
	return &fakeService{
		folders:   map[string][]mail.Folder{},
		messages:  map[string][]mail.Summary{},
		bodies:    map[string]mail.Message{},
		password:  map[string]string{},
		drafts:    map[string]mail.Draft{},
		replaced:  map[string]bool{},
		draftBase: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		draftUID:  500,
		watching:  map[string]func(mail.WatchEvent){},
		settings:  mail.DefaultSettings(),

		ownThreads: map[string]mail.ThreadID{},
		cursors:    map[mail.Cursor]mail.Summary{},
		older:      map[string][]mail.Summary{},
	}
}

func listKey(accountID, folder string) string { return accountID + "/" + folder }

func bodyKey(accountID, folder string, uid mail.UID) string {
	return accountID + "/" + folder + "/" + itoa(uid)
}

// addAccount stores an account with its folders and headers.
func (f *fakeService) addAccount(a mail.Account, password string, folders []mail.Folder, msgs []mail.Summary) {
	f.accounts = append(f.accounts, a)
	f.password[a.ID] = password
	f.folders[a.ID] = append([]mail.Folder(nil), folders...)
	if f.active == "" {
		f.active = a.ID
	}
	for _, m := range msgs {
		folder := m.Folder
		if folder == "" {
			folder = "INBOX"
		}
		m.Folder, m.Account = folder, a.ID
		key := listKey(a.ID, folder)
		f.messages[key] = append(f.messages[key], m)
	}
	for key := range f.messages {
		sortSummaries(f.messages[key])
	}
}

func sortSummaries(list []mail.Summary) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })
}

func (f *fakeService) Accounts(ctx context.Context) ([]mail.Account, string, error) {
	return append([]mail.Account(nil), f.accounts...), f.active, nil
}

func (f *fakeService) AddAccount(ctx context.Context, a mail.Account, password string) error {
	f.accounts = append(f.accounts, a)
	f.password[a.ID] = password
	if f.active == "" {
		f.active = a.ID
	}
	if f.folders[a.ID] == nil {
		f.folders[a.ID] = []mail.Folder{
			{Name: "INBOX", SpecialUse: mail.SpecialInbox},
			{Name: "Sent", SpecialUse: mail.SpecialSent},
		}
	}
	return nil
}

func (f *fakeService) UpdateAccount(ctx context.Context, a mail.Account, password string) error {
	for i := range f.accounts {
		if f.accounts[i].ID == a.ID {
			f.accounts[i] = a
			if password != "" {
				f.password[a.ID] = password
			}
			return nil
		}
	}
	return mail.ErrNotFound
}

func (f *fakeService) RemoveAccount(ctx context.Context, id string) error {
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts = append(f.accounts[:i], f.accounts[i+1:]...)
			break
		}
	}
	if f.active == id {
		f.active = ""
		if len(f.accounts) > 0 {
			f.active = f.accounts[0].ID
		}
	}
	return nil
}

func (f *fakeService) SetActive(ctx context.Context, id string) error {
	f.active = id
	return nil
}

func (f *fakeService) TestConnection(ctx context.Context, a mail.Account, password string) error {
	return f.testErr
}

func (f *fakeService) CachedFolders(ctx context.Context, accountID string) ([]mail.Folder, error) {
	return append([]mail.Folder(nil), f.folders[accountID]...), nil
}

// MessagePage reads the messages of a scope a search finds, newest first,
// a page after a cursor, as the engine does.
func (f *fakeService) MessagePage(ctx context.Context, q mail.ListQuery) (mail.MessagePage, error) {
	if err := f.beginPage(q); err != nil {
		return mail.MessagePage{}, err
	}
	accounts := f.scopeAccounts(q.Scope)
	var all []mail.Summary
	if len(accounts) == 1 && q.Scope.Folder != "" && q.Search.IsZero() {
		// A folder of an account, read as it is kept: a large folder is
		// not copied for each page.
		all = f.messages[listKey(accounts[0], q.Scope.Folder)]
	} else {
		for _, accountID := range accounts {
			var list []mail.Summary
			if q.Scope.Folder != "" {
				list = f.messages[listKey(accountID, q.Scope.Folder)]
			} else {
				list = f.everyMessage(accountID)
			}
			for _, m := range list {
				m.Account = accountID
				if fields, ok := f.found(accountID, m, q.Search); ok && f.inScope(q.Scope, m) {
					m.Matched = fields
					all = append(all, m)
				}
			}
		}
	}
	byAge := func(i, j int) bool { return newer(all[i], all[j]) }
	if !sort.SliceIsSorted(all, byAge) {
		sort.SliceStable(all, byAge)
	}
	rows, next, more := page(f, all, q, func(m mail.Summary) mail.Summary { return m })
	for i := range rows {
		if rows[i].Account == "" {
			rows[i].Account = accounts[0]
		}
	}
	return mail.MessagePage{Messages: rows, Next: next, More: more}, nil
}

// beginPage counts a page read and runs what the test asked for.
func (f *fakeService) beginPage(q mail.ListQuery) error {
	f.pageReads++
	if f.onPage != nil {
		f.onPage(q)
	}
	if err := q.Scope.Check(); err != nil {
		return err
	}
	return f.pageErr
}

// page takes the page of rows after the query's cursor: those older, in
// the order of newer, than the row it ended at.
func page[T any](f *fakeService, all []T, q mail.ListQuery, latest func(T) mail.Summary) ([]T, mail.Cursor, bool) {
	start := 0
	if q.After != "" {
		end, ok := f.cursors[q.After]
		if !ok {
			return nil, q.After, false
		}
		start = sort.Search(len(all), func(i int) bool { return newer(end, latest(all[i])) })
	}
	limit := q.Limit
	if limit <= 0 {
		limit = mail.DefaultPageSize
	}
	rows := all[start:min(start+limit, len(all))]
	if len(rows) == 0 {
		return []T{}, q.After, false
	}
	last := latest(rows[len(rows)-1])
	next := mail.Cursor(fmt.Sprintf("%d/%d/%s/%s", last.Date.UnixNano(), last.UID, last.Account, last.Folder))
	f.cursors[next] = last
	return append([]T(nil), rows...), next, start+len(rows) < len(all)
}

// scopeAccounts are the accounts a scope reads.
func (f *fakeService) scopeAccounts(sc mail.Scope) []string {
	if len(sc.Accounts) > 0 {
		return sc.Accounts
	}
	var out []string
	for _, a := range f.accounts {
		out = append(out, a.ID)
	}
	return out
}

// inScope reports whether a message of an account is in a scope's
// folders.
func (f *fakeService) inScope(sc mail.Scope, m mail.Summary) bool {
	switch {
	case sc.Starred:
		return m.Flagged
	case sc.Folder != "":
		return m.Folder == sc.Folder
	case sc.Special != "":
		for _, folder := range f.folders[m.Account] {
			if folder.Name == m.Folder {
				return folder.SpecialUse == sc.Special
			}
		}
		return false
	}
	return true
}

// found reports whether a message matches every part of a search, as the
// engine does, and the parts of it its terms were found in.
func (f *fakeService) found(accountID string, m mail.Summary, s mail.Search) (mail.Field, bool) {
	body := m.Preview
	if b, ok := f.bodies[bodyKey(accountID, m.Folder, m.UID)]; ok {
		body += " " + b.BodyText
	}
	var to []string
	for _, a := range append(append([]mail.Address(nil), m.To...), m.Cc...) {
		to = append(to, a.Name, a.Email)
	}
	text := map[mail.Field]string{
		mail.FieldSubject: m.Subject, mail.FieldFrom: m.From.Name + " " + m.From.Email,
		mail.FieldTo: strings.Join(to, " "), mail.FieldBody: body,
	}
	var fields mail.Field
	look := func(terms []string, in mail.Field) bool {
		for _, term := range terms {
			term = strings.ToLower(strings.TrimSpace(term))
			hit := false
			for part, s := range text {
				if in.Has(part) && strings.Contains(strings.ToLower(s), term) {
					fields |= part
					hit = true
				}
			}
			if !hit {
				return false
			}
		}
		return true
	}
	ok := look(s.Text, mail.FieldAny) && look(s.From, mail.FieldFrom) && look(s.To, mail.FieldTo) &&
		look(s.Subject, mail.FieldSubject) && look(s.Body, mail.FieldBody) &&
		(s.Since.IsZero() || !m.Date.Before(s.Since)) && (s.Before.IsZero() || m.Date.Before(s.Before)) &&
		(!s.Unread || !m.Seen) && (!s.Starred || m.Flagged) && (!s.Attachment || m.HasAttachments)
	if ok && strings.TrimSpace(s.In) != "" {
		ok = false
		for _, folder := range f.folders[accountID] {
			if folder.Name == m.Folder && folder.NamedBy(s.In) {
				ok = true
			}
		}
	}
	return fields, ok
}

func (f *fakeService) matches(accountID string, m mail.Summary, q string) bool {
	if q == "" {
		return true
	}
	text := strings.ToLower(m.Subject + " " + m.From.Name + " " + m.From.Email + " " + m.Preview)
	if b, ok := f.bodies[bodyKey(accountID, m.Folder, m.UID)]; ok {
		text += " " + strings.ToLower(b.BodyText)
	}
	return strings.Contains(text, q)
}

func (f *fakeService) CachedMessage(ctx context.Context, accountID, folder string, uid mail.UID) (mail.FetchResult, bool, error) {
	s, found := f.find(accountID, folder, uid)
	if found {
		s.Thread = f.threadOf(accountID, s)
	}
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		b.Thread = s.Thread
		return mail.FetchResult{Message: b}, true, nil
	}
	if found {
		return mail.FetchResult{Message: mail.Message{Summary: s}}, false, nil
	}
	return mail.FetchResult{}, false, nil
}

func (f *fakeService) find(accountID, folder string, uid mail.UID) (mail.Summary, bool) {
	for _, m := range f.messages[listKey(accountID, folder)] {
		if m.UID == uid {
			m.Account = accountID
			return m, true
		}
	}
	return mail.Summary{}, false
}

func (f *fakeService) SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error) {
	return append([]mail.Folder(nil), f.folders[accountID]...), nil
}

func (f *fakeService) SyncFolder(ctx context.Context, accountID, folder string, limit int) error {
	f.syncCalls = append(f.syncCalls, fmt.Sprintf("%s/%s/%d", accountID, folder, limit))
	return nil
}

// FolderCache counts a folder's messages in the cache against those of
// the server: those cached and those older still to fetch.
func (f *fakeService) FolderCache(ctx context.Context, accountID, folder string) (mail.FolderCache, error) {
	if f.cacheErr != nil {
		return mail.FolderCache{}, f.cacheErr
	}
	key := listKey(accountID, folder)
	cached, left := len(f.messages[key]), len(f.older[key])
	total := cached + left
	for _, fl := range f.folders[accountID] {
		if fl.Name == folder {
			total = max(total, fl.Total)
		}
	}
	return mail.FolderCache{Cached: cached, Total: total, Complete: left == 0}, nil
}

// FetchOlder moves the newest n of a folder's messages still on the
// server into the cache, the settings' window for n of zero.
func (f *fakeService) FetchOlder(ctx context.Context, accountID, folder string, n int) (mail.OlderResult, error) {
	f.olderCalls = append(f.olderCalls, fmt.Sprintf("%s/%s/%d", accountID, folder, n))
	if f.onOlder != nil {
		f.onOlder()
	}
	if f.olderErr != nil {
		return mail.OlderResult{}, f.olderErr
	}
	if n <= 0 {
		n = f.settings.SyncWindow
	}
	key := listKey(accountID, folder)
	take := min(n, len(f.older[key]))
	for _, m := range f.older[key][:take] {
		m.Folder, m.Account = folder, accountID
		f.messages[key] = append(f.messages[key], m)
	}
	f.older[key] = f.older[key][take:]
	sortSummaries(f.messages[key])
	return mail.OlderResult{Fetched: take, More: len(f.older[key]) > 0, Reset: f.olderReset}, nil
}

func (f *fakeService) FetchMessage(ctx context.Context, accountID, folder string, uid mail.UID, markSeen bool) (mail.FetchResult, error) {
	f.act(accountID, "FetchMessage %s/%d %v", folder, uid, markSeen)
	s, ok := f.find(accountID, folder, uid)
	if !ok {
		return mail.FetchResult{}, mail.ErrNotFound
	}
	s.Thread = f.threadOf(accountID, s)
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		b.Thread = s.Thread
		return mail.FetchResult{Message: b}, nil
	}
	msg := mail.Message{Summary: s, BodyText: "Body of " + s.Subject, BodyCached: true}
	msg.BodyHTML = "<p>Body of <b>" + s.Subject + "</b></p><script>alert(1)</script>"
	if markSeen {
		msg.Seen = true
	}
	f.bodies[bodyKey(accountID, folder, uid)] = msg
	f.markSeen(accountID, folder, uid, msg.Seen)
	return mail.FetchResult{Message: msg}, nil
}

func (f *fakeService) markSeen(accountID, folder string, uid mail.UID, seen bool) {
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID == uid {
			list[i].Seen = seen
			f.messages[key] = list
		}
	}
	f.recount(accountID, folder)
}

// recount keeps the fake folder's unread count in step with its rows.
func (f *fakeService) recount(accountID, folder string) {
	unread := 0
	for _, m := range f.messages[listKey(accountID, folder)] {
		if !m.Seen {
			unread++
		}
	}
	for i := range f.folders[accountID] {
		if f.folders[accountID][i].Name == folder {
			f.folders[accountID][i].Unread = unread
			f.folders[accountID][i].Total = len(f.messages[listKey(accountID, folder)])
		}
	}
}

func (f *fakeService) SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error {
	f.flagCalls = append(f.flagCalls, string(flag))
	f.act(accountID, "SetFlag %s/%d %s %v", folder, uid, flag, on)
	if f.flagErr != nil {
		return f.flagErr
	}
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		if flag == mail.FlagFlagged {
			list[i].Flagged = on
		}
		if flag == mail.FlagSeen {
			list[i].Seen = on
		}
		f.messages[key] = list
	}
	if flag == mail.FlagSeen {
		f.recount(accountID, folder)
	}
	return nil
}

// hasFolder refuses a move to a folder the account does not have, as a
// server does: one account's folder named for another's mail.
func (f *fakeService) hasFolder(accountID, name string) error {
	for _, folder := range f.folders[accountID] {
		if folder.Name == name {
			return nil
		}
	}
	return fmt.Errorf("fake: %s has no folder %q", accountID, name)
}

func (f *fakeService) Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) (mail.MoveResult, error) {
	f.moved = append(f.moved, folder+"->"+dest)
	f.act(accountID, "Move %s/%d->%s", folder, uid, dest)
	if f.moveErr != nil {
		return mail.MoveResult{}, f.moveErr
	}
	if err := f.hasFolder(accountID, dest); err != nil {
		return mail.MoveResult{}, err
	}
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		m := list[i]
		f.messages[key] = append(list[:i], list[i+1:]...)
		m.Folder = dest
		m.UID = 1000 + uid
		destKey := listKey(accountID, dest)
		f.messages[destKey] = append(f.messages[destKey], m)
		f.recount(accountID, folder)
		f.recount(accountID, dest)
		return mail.MoveResult{Folder: dest, DestUID: m.UID}, nil
	}
	return mail.MoveResult{Folder: dest}, nil
}

func (f *fakeService) MoveBack(ctx context.Context, accountID, from, to string, uid mail.UID) error {
	f.moveBacks = append(f.moveBacks, from+"->"+to)
	f.act(accountID, "MoveBack %s/%d->%s", from, uid, to)
	key := listKey(accountID, from)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		m := list[i]
		f.messages[key] = append(list[:i], list[i+1:]...)
		m.Folder = to
		if m.UID > 1000 {
			m.UID -= 1000
		}
		srcKey := listKey(accountID, to)
		f.messages[srcKey] = append(f.messages[srcKey], m)
		f.recount(accountID, from)
		f.recount(accountID, to)
		return nil
	}
	return nil
}

func (f *fakeService) Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error) {
	f.act(accountID, "Send %s", out.Subject)
	if f.sendErr != nil {
		return mail.SendResult{}, f.sendErr
	}
	if f.onSend != nil {
		if err := f.onSend(ctx, out); err != nil {
			return mail.SendResult{}, err
		}
	}
	f.sent = append(f.sent, out)
	// The message is out: its draft goes, here and on the server.
	f.removeDraft(out.DraftID)
	return mail.SendResult{Copied: f.sendWarn == nil, Warning: f.sendWarn, Outbox: mail.PendingSend{ID: "out-1"}}, nil
}

func (f *fakeService) RetryPending(ctx context.Context, id string) (mail.SendResult, error) {
	if f.retryErr != nil {
		return mail.SendResult{}, f.retryErr
	}
	f.pending = nil
	return mail.SendResult{Copied: true}, nil
}

func (f *fakeService) PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error) {
	return append([]mail.PendingSend(nil), f.pending...), nil
}

func (f *fakeService) Attachment(ctx context.Context, accountID, folder string, uid mail.UID, part string) (mail.Attachment, []byte, error) {
	if f.attachment.Part == "" {
		return mail.Attachment{}, nil, mail.ErrNotFound
	}
	return f.attachment, []byte("data"), nil
}

// serverDrafts is the name of the account's Drafts folder, or "".
func (f *fakeService) serverDrafts(accountID string) string {
	for _, folder := range f.folders[accountID] {
		if folder.SpecialUse == mail.SpecialDrafts {
			return folder.Name
		}
	}
	return ""
}

// removeDraft deletes a draft and the server copy it is linked to.
func (f *fakeService) removeDraft(id string) {
	d, ok := f.drafts[id]
	if !ok {
		return
	}
	delete(f.drafts, id)
	if d.ServerUID == 0 {
		return
	}
	key := listKey(d.AccountID, d.ServerFolder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID == d.ServerUID {
			f.messages[key] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// draftRecipients reads a recipient field as far as it parses.
func draftRecipients(text string) []mail.Address {
	var out []mail.Address
	for _, part := range strings.Split(text, ",") {
		if list, err := mail.ParseAddressList([]string{part}); err == nil {
			out = append(out, list...)
		}
	}
	return out
}

func (f *fakeService) DraftList(ctx context.Context, accountID, query string, limit int) ([]mail.Summary, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var from mail.Address
	for _, a := range f.accounts {
		if a.ID == accountID {
			from = mail.Address{Name: a.Name, Email: a.Email}
		}
	}
	var out []mail.Summary
	uploaded := map[mail.UID]bool{}
	for _, d := range f.drafts {
		if d.AccountID != accountID {
			continue
		}
		uploaded[d.ServerUID] = true
		if q != "" && !strings.Contains(strings.ToLower(d.Subject+" "+d.To+" "+d.Body), q) {
			continue
		}
		out = append(out, mail.Summary{
			Account: accountID, DraftID: d.ID, MessageID: d.MessageID, From: from,
			To: draftRecipients(d.To), Cc: draftRecipients(d.Cc),
			Subject: strings.TrimSpace(d.Subject), Preview: strings.Join(strings.Fields(d.Body), " "),
			Date: d.UpdatedAt, Seen: true, HasAttachments: len(d.Attachments) > 0,
		})
	}
	sortSummaries(out)
	if folder := f.serverDrafts(accountID); folder != "" {
		var server []mail.Summary
		for _, m := range f.messages[listKey(accountID, folder)] {
			if !uploaded[m.UID] && f.matches(accountID, m, q) {
				m.Account = accountID
				server = append(server, m)
			}
		}
		sortSummaries(server)
		out = append(out, server...)
	}
	return out, nil
}

func (f *fakeService) Draft(ctx context.Context, id string) (mail.Draft, bool, error) {
	d, ok := f.drafts[id]
	return d, ok, nil
}

func (f *fakeService) SaveDraft(ctx context.Context, d mail.Draft) (mail.Draft, error) {
	f.draftSaves++
	if f.draftSaveErr != nil {
		return mail.Draft{}, f.draftSaveErr
	}
	if old, ok := f.drafts[d.ID]; ok {
		if old.To == d.To && old.Cc == d.Cc && old.Bcc == d.Bcc && old.Subject == d.Subject &&
			old.Body == d.Body && old.InReplyTo == d.InReplyTo {
			return old, nil
		}
		// The content changes; where the copy is, and its revision on
		// the server, do not.
		old.To, old.Cc, old.Bcc, old.Subject, old.Body = d.To, d.Cc, d.Bcc, d.Subject, d.Body
		old.InReplyTo, old.References = d.InReplyTo, d.References
		old.Revision++
		d = old
	} else {
		d.Revision, d.SyncedRevision = 1, 0
		d.ServerFolder, d.ServerUID, d.ServerUIDValidity = "", 0, 0
		// A save never attaches anything.
		d.Attachments = nil
		d.MessageID = d.ID + "@example.com"
		// A draft another client took over comes back as one of its own,
		// another message than the one it was.
		if d.Conflict = f.replaced[d.ID]; d.Conflict {
			d.MessageID = d.ID + ".kept@example.com"
		}
		delete(f.replaced, d.ID)
	}
	f.draftTick++
	d.UpdatedAt = f.draftBase.Add(time.Duration(f.draftTick) * time.Minute)
	f.drafts[d.ID] = d
	return d, nil
}

func (f *fakeService) SyncDrafts(ctx context.Context, accountID string) (mail.DraftSyncResult, error) {
	f.draftFullSyncs++
	return f.syncDrafts(accountID, true)
}

// SyncDraftEdits leaves the drafts with large attachments where they
// are, as the engine's does.
func (f *fakeService) SyncDraftEdits(ctx context.Context, accountID string) (mail.DraftSyncResult, error) {
	return f.syncDrafts(accountID, false)
}

func (f *fakeService) syncDrafts(accountID string, heavy bool) (mail.DraftSyncResult, error) {
	f.draftSyncs++
	if f.draftSyncErr != nil {
		return mail.DraftSyncResult{}, f.draftSyncErr
	}
	if f.onDraftSync != nil {
		f.onDraftSync()
	}
	folder := f.serverDrafts(accountID)
	if folder == "" {
		return mail.DraftSyncResult{}, nil
	}
	result := mail.DraftSyncResult{Folder: folder}
	for id, d := range f.drafts {
		if d.AccountID != accountID || !d.Dirty() {
			continue
		}
		if !heavy && d.AttachmentBytes() > mail.DraftAutoUploadLimit {
			result.Deferred++
			continue
		}
		f.draftUID++
		d.SyncedRevision = d.Revision
		d.ServerFolder, d.ServerUID, d.ServerUIDValidity = folder, f.draftUID, 1
		f.drafts[id] = d
		result.Uploaded++
	}
	return result, nil
}

// fakeContentType is a file's type by its name, enough for the tests'
// files.
func fakeContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".txt":
		return "text/plain"
	case ".zip":
		return "application/zip"
	}
	return "application/octet-stream"
}

// AttachFile reads the file as the engine does, without keeping its
// bytes: the fake's drafts name their files and no more.
func (f *fakeService) AttachFile(ctx context.Context, d mail.Draft, path string) (mail.Draft, mail.Attachment, error) {
	f.attachCalls = append(f.attachCalls, path)
	if f.onAttach != nil {
		f.onAttach(path)
	}
	if f.attachErr != nil {
		return mail.Draft{}, mail.Attachment{}, f.attachErr
	}
	info, err := os.Stat(path)
	if err != nil {
		return mail.Draft{}, mail.Attachment{}, err
	}
	if info.IsDir() {
		return mail.Draft{}, mail.Attachment{}, mail.ErrAttachDirectory
	}
	file, err := os.Open(path)
	if err != nil {
		return mail.Draft{}, mail.Attachment{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return mail.Draft{}, mail.Attachment{}, err
	}
	att := mail.Attachment{
		Filename: filepath.Base(path), ContentType: fakeContentType(path), Size: size,
		ContentHash: hex.EncodeToString(hash.Sum(nil)),
	}
	cur, ok := f.drafts[d.ID]
	if !ok {
		// As the engine does: the draft is made as the window has it.
		if cur, err = f.SaveDraft(ctx, d); err != nil {
			return mail.Draft{}, mail.Attachment{}, err
		}
	}
	for _, have := range cur.Attachments {
		if have.ContentHash == att.ContentHash && have.Filename == att.Filename {
			return cur, have, nil
		}
	}
	if cur.AttachmentBytes()+att.Size > mail.MaxAttachmentBytes {
		return mail.Draft{}, mail.Attachment{}, mail.ErrAttachTooLarge
	}
	cur.Attachments = append(append([]mail.Attachment(nil), cur.Attachments...), att)
	return f.touchDraft(cur), att, nil
}

func (f *fakeService) RemoveAttachment(ctx context.Context, id string, att mail.Attachment) (mail.Draft, error) {
	if f.removeErr != nil {
		return mail.Draft{}, f.removeErr
	}
	cur, ok := f.drafts[id]
	if !ok {
		return mail.Draft{}, mail.ErrNotFound
	}
	var kept []mail.Attachment
	for _, have := range cur.Attachments {
		if have.ContentHash != att.ContentHash || have.Filename != att.Filename {
			kept = append(kept, have)
		}
	}
	if len(kept) == len(cur.Attachments) {
		return cur, nil
	}
	cur.Attachments = kept
	return f.touchDraft(cur), nil
}

// touchDraft stores a draft whose attachments changed: an edit like any
// other.
func (f *fakeService) touchDraft(d mail.Draft) mail.Draft {
	d.Revision++
	f.draftTick++
	d.UpdatedAt = f.draftBase.Add(time.Duration(f.draftTick) * time.Minute)
	f.drafts[d.ID] = d
	return d
}

func (f *fakeService) SweepDraftFiles(ctx context.Context) error {
	f.sweeps++
	return nil
}

func (f *fakeService) DiscardDraft(ctx context.Context, id string) error {
	f.discarded = append(f.discarded, id)
	if f.discardErr != nil {
		return f.discardErr
	}
	f.removeDraft(id)
	return nil
}

func (f *fakeService) AdoptDraft(ctx context.Context, accountID, folder string, uid mail.UID) (mail.Draft, error) {
	if f.adoptErr != nil {
		return mail.Draft{}, f.adoptErr
	}
	for _, d := range f.drafts {
		if d.AccountID == accountID && d.ServerFolder == folder && d.ServerUID == uid {
			return d, nil
		}
	}
	m, ok := f.find(accountID, folder, uid)
	if !ok {
		return mail.Draft{}, mail.ErrNotFound
	}
	body := "Body of " + m.Subject
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		body = b.BodyText
	}
	f.draftTick++
	d := mail.Draft{
		ID: "adopted-" + itoa(uid), AccountID: accountID,
		To: joinAddresses(m.To), Cc: joinAddresses(m.Cc), Subject: m.Subject, Body: body,
		MessageID: m.MessageID, UpdatedAt: f.draftBase.Add(time.Duration(f.draftTick) * time.Minute),
		Revision: 1, SyncedRevision: 1, ServerFolder: folder, ServerUID: uid, ServerUIDValidity: 1,
	}
	f.drafts[d.ID] = d
	return d, nil
}

func (f *fakeService) DismissDraftConflict(ctx context.Context, id string) error {
	if d, ok := f.drafts[id]; ok {
		d.Conflict = false
		f.drafts[id] = d
	}
	return nil
}

func joinAddresses(list []mail.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

func itoa(uid mail.UID) string {
	if uid == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for uid > 0 {
		i--
		b[i] = byte('0' + uid%10)
		uid /= 10
	}
	return string(b[i:])
}

var _ mailService = (*fakeService)(nil)

var errFake = errors.New("fake error")

func (f *fakeService) Watch(accountID string, report func(mail.WatchEvent)) {
	f.watchLog = append(f.watchLog, "watch "+accountID)
	f.watching[accountID] = report
}

func (f *fakeService) Unwatch(accountID string) {
	f.watchLog = append(f.watchLog, "unwatch "+accountID)
	delete(f.watching, accountID)
}

func (f *fakeService) Nudge() { f.watchLog = append(f.watchLog, "nudge") }

func (f *fakeService) SetOnline(online bool) {
	if online {
		f.watchLog = append(f.watchLog, "online")
	} else {
		f.watchLog = append(f.watchLog, "offline")
	}
}

func (f *fakeService) Settings(ctx context.Context) (mail.Settings, error) {
	set := f.settings
	set.Muted = map[string]bool{}
	for id, muted := range f.settings.Muted {
		set.Muted[id] = muted
	}
	return set, nil
}

func (f *fakeService) SaveSettings(ctx context.Context, s mail.Settings) error {
	if f.settingsErr != nil {
		return f.settingsErr
	}
	f.settings = s
	f.saved = append(f.saved, s)
	return nil
}

// threadOf is a message's conversation.
func (f *fakeService) threadOf(accountID string, m mail.Summary) mail.ThreadID {
	if m.Thread != 0 {
		return m.Thread
	}
	key := bodyKey(accountID, m.Folder, m.UID)
	if id, ok := f.ownThreads[key]; ok {
		return id
	}
	f.nextThread++
	f.ownThreads[key] = 100000 + f.nextThread
	return f.ownThreads[key]
}

// everyMessage is the account's mail in every folder, each message with
// its conversation.
func (f *fakeService) everyMessage(accountID string) []mail.Summary {
	var out []mail.Summary
	for _, folder := range f.folders[accountID] {
		for _, m := range f.messages[listKey(accountID, folder.Name)] {
			m.Account, m.Thread = accountID, f.threadOf(accountID, m)
			out = append(out, m)
		}
	}
	return out
}

// inFakeScope reports whether a message is in a list's scope: a folder,
// the starred messages, or "" for all.
func inFakeScope(scope string, m mail.Summary) bool {
	switch scope {
	case "":
		return true
	case "starred":
		return m.Flagged
	}
	return m.Folder == scope
}

// newer orders messages as the cache does: by date, then by UID, and the
// folder and the account for an order with no ties.
func newer(a, b mail.Summary) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.After(b.Date)
	}
	if a.UID != b.UID {
		return a.UID > b.UID
	}
	if a.Folder != b.Folder {
		return a.Folder > b.Folder
	}
	return a.Account > b.Account
}

// ConversationPage reads the conversations with a message in a scope, the
// one whose newest such message is newest first, a page after a cursor:
// a conversation a new message moves above the cursor is in no later
// page, as in the engine.
func (f *fakeService) ConversationPage(ctx context.Context, q mail.ListQuery) (mail.ConversationPage, error) {
	if err := f.beginPage(q); err != nil {
		return mail.ConversationPage{}, err
	}
	var all []mail.Conversation
	for _, accountID := range f.scopeAccounts(q.Scope) {
		all = append(all, f.conversations(accountID, q)...)
	}
	sort.SliceStable(all, func(i, j int) bool { return newer(all[i].Latest, all[j].Latest) })
	rows, next, more := page(f, all, q, func(c mail.Conversation) mail.Summary { return c.Latest })
	return mail.ConversationPage{Conversations: rows, Next: next, More: more}, nil
}

// conversations are an account's rows of a scope a search finds.
func (f *fakeService) conversations(accountID string, q mail.ListQuery) []mail.Conversation {
	every := f.everyMessage(accountID)
	byThread := map[mail.ThreadID]*mail.Conversation{}
	var order []mail.ThreadID
	for _, m := range every {
		if !f.inScope(q.Scope, m) {
			continue
		}
		c := byThread[m.Thread]
		if c == nil {
			c = &mail.Conversation{Thread: m.Thread, Account: accountID, Latest: m}
			byThread[m.Thread] = c
			order = append(order, m.Thread)
		}
		c.Count++
		c.Unread = c.Unread || !m.Seen
		c.Flagged = c.Flagged || m.Flagged
		c.HasAttachments = c.HasAttachments || m.HasAttachments
		if newer(m, c.Latest) {
			c.Latest = m
		}
	}
	search := !q.Search.IsZero()
	if search {
		// A conversation is found by any of its messages, in any folder.
		sort.SliceStable(every, func(i, j int) bool { return newer(every[i], every[j]) })
		for _, m := range every {
			c := byThread[m.Thread]
			if c == nil {
				continue
			}
			if fields, ok := f.found(accountID, m, q.Search); ok {
				ref := mail.MessageRef{Folder: m.Folder, UID: m.UID}
				c.Matches = append(c.Matches, ref)
				c.Found = append(c.Found, mail.Match{MessageRef: ref, Fields: fields})
				if ref.Folder == c.Latest.Folder && ref.UID == c.Latest.UID {
					c.Latest.Matched = fields
				}
			}
		}
	}
	var out []mail.Conversation
	for _, id := range order {
		if c := byThread[id]; !search || len(c.Matches) > 0 {
			out = append(out, *c)
		}
	}
	return out
}

// ScopedConversation is the messages a conversation's row of a scope
// stands for, oldest first.
func (f *fakeService) ScopedConversation(ctx context.Context, accountID string, scope mail.Scope, thread mail.ThreadID) ([]mail.Summary, error) {
	if f.convErr != nil {
		return nil, f.convErr
	}
	if err := scope.Check(); err != nil {
		return nil, err
	}
	if len(scope.Accounts) > 0 && !slices.Contains(scope.Accounts, accountID) {
		return nil, nil
	}
	var out []mail.Summary
	for _, m := range f.everyMessage(accountID) {
		if m.Thread == thread && f.inScope(scope, m) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return newer(out[j], out[i]) })
	return out, nil
}

func (f *fakeService) CachedConversation(ctx context.Context, accountID, folder string, thread mail.ThreadID) ([]mail.Summary, error) {
	if f.convErr != nil {
		return nil, f.convErr
	}
	var out []mail.Summary
	for _, m := range f.everyMessage(accountID) {
		if m.Thread == thread && inFakeScope(folder, m) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return newer(out[j], out[i]) })
	return out, nil
}

func (f *fakeService) SetFlags(ctx context.Context, accountID string, msgs []mail.MessageRef, flag mail.Flag, on bool) error {
	call := bulkFlag{flag: flag, on: on}
	for _, r := range msgs {
		call.refs = append(call.refs, r.Folder+"/"+itoa(r.UID))
	}
	f.bulkFlags = append(f.bulkFlags, call)
	f.act(accountID, "SetFlags %s %s %v", strings.Join(call.refs, ","), flag, on)
	if f.flagErr != nil {
		return f.flagErr
	}
	for _, r := range msgs {
		key := listKey(accountID, r.Folder)
		for i := range f.messages[key] {
			m := &f.messages[key][i]
			if m.UID != r.UID {
				continue
			}
			switch flag {
			case mail.FlagFlagged:
				m.Flagged = on
			case mail.FlagSeen:
				m.Seen = on
			}
		}
		f.recount(accountID, r.Folder)
	}
	return nil
}

// moveOne moves a message to another folder under another UID, in its
// conversation still, and returns that UID.
func (f *fakeService) moveOne(accountID, from string, uid mail.UID, to string, newUID mail.UID) bool {
	key := listKey(accountID, from)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		m := list[i]
		m.Thread = f.threadOf(accountID, m)
		f.messages[key] = append(list[:i:i], list[i+1:]...)
		m.Folder, m.UID = to, newUID
		destKey := listKey(accountID, to)
		f.messages[destKey] = append(f.messages[destKey], m)
		f.recount(accountID, from)
		f.recount(accountID, to)
		return true
	}
	return false
}

func (f *fakeService) MoveMessages(ctx context.Context, accountID string, msgs []mail.MessageRef, dest string) ([]mail.MoveResult, error) {
	for _, r := range msgs {
		f.movedRefs = append(f.movedRefs, r.Folder+"/"+itoa(r.UID)+"->"+dest)
		f.act(accountID, "MoveMessages %s/%d->%s", r.Folder, r.UID, dest)
	}
	if f.moveErr != nil {
		return nil, f.moveErr
	}
	if err := f.hasFolder(accountID, dest); err != nil {
		return nil, err
	}
	var out []mail.MoveResult
	for _, r := range msgs {
		if r.Folder == dest {
			continue
		}
		if f.moveOne(accountID, r.Folder, r.UID, dest, 1000+r.UID) {
			out = append(out, mail.MoveResult{Folder: dest, DestUID: 1000 + r.UID, From: r.Folder, UID: r.UID})
		}
	}
	return out, nil
}

func (f *fakeService) MoveBackMessages(ctx context.Context, accountID string, moved []mail.MoveResult) error {
	lost := 0
	for _, r := range moved {
		f.moveBacks = append(f.moveBacks, r.Folder+"->"+r.From)
		f.act(accountID, "MoveBackMessages %s/%d->%s", r.Folder, r.DestUID, r.From)
		if r.DestUID == 0 || !f.moveOne(accountID, r.Folder, r.DestUID, r.From, r.UID) {
			lost++
		}
	}
	if lost > 0 {
		return errFake
	}
	return nil
}

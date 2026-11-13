package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
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

	testErr         error
	flagErr         error
	moveErr         error
	sendErr         error
	sendWarn        error
	retryErr        error
	password        map[string]string
	sent            []mail.Outgoing
	moved           []string
	moveBacks       []string
	flagCalls       []string
	attachment      mail.Attachment
	attachmentBytes []byte
	// folderCalls records the folder actions taken, in order, and
	// folderErr makes them fail.
	folderCalls []string
	folderErr   error
	// networkSet is the network settings the window saved, networkStored
	// whether a proxy password is kept, and networkErr makes the calls
	// fail.
	networkSet    mail.NetworkSettings
	networkStored bool
	networkErr    error
	// remote is the senders whose images are allowed, and remoteErr makes
	// the policy calls fail.
	remote    map[string]bool
	remoteErr error
	// imported, exported and restored record the transfer actions, and
	// transferErr makes them fail.
	imported    []string
	exported    []string
	restored    []string
	transferErr error

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
	prepares    int

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

	// Reminders, one per conversation, and the calls that set and took
	// them off; reminderErr fails them.
	reminders   []mail.Reminder
	reminderSeq int64
	reminderErr error
	reminderSet []string

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

	// Signing in with a provider. oauth is the providers the build has a
	// client for. A sign-in opens the page and then runs onSignIn, the
	// user in the browser, where a test looks at the window, cancels, or
	// ends the sign-in with what it returns. signInAs is the address the
	// browser signs in as, "" for the account's own or newAddress for a
	// new one, whose inbox holds newInbox. needSignIn is the accounts
	// that have to sign in again: whatever of theirs reaches a server
	// fails as the engine's does. signIns, signOuts and signInChecks
	// record the calls, and prompts what each sign-in was given.
	oauth        map[string]bool
	onSignIn     func(ctx context.Context) error
	signInAs     string
	newAddress   string
	newInbox     []mail.Summary
	needSignIn   map[string]bool
	signOutErr   error
	signIns      []string
	signOuts     []string
	signInChecks []string
	prompts      []mail.OAuthPrompt

	// How messages are written. compose is what ComposeSettings reads,
	// composeSaved each save, and composeErr a store that refuses one.
	// signatures are the accounts' saved signatures, and identities the
	// addresses added to each account, its own being the account's;
	// signatureErr and identityErr fail their saves as a store would.
	compose      mail.ComposeSettings
	composeSaved []mail.ComposeSettings
	composeErr   error
	signatures   map[string]mail.Signature
	signatureErr error
	identities   map[string][]mail.Identity
	identityErr  error
	nextIdentity int

	// Sending with a delay. scheduled is every message Schedule wrote, in
	// order, with what became of it. onScheduled runs while SendScheduled
	// waits for one to be due, where a test looks at the window and takes
	// the message back or not; clock is what DueAt counts from, the time
	// of the window's tester. scheduleErr fails Schedule, unsendErr
	// Unsend, and delivered and deliverErr are what DeliverScheduled
	// returns, deliverCalls how often it was asked.
	scheduled    []*fakeScheduled
	onScheduled  func(ctx context.Context, id string)
	clock        func() time.Time
	scheduleErr  error
	unsendErr    error
	delivered    []mail.ScheduledResult
	deliverErr   error
	deliverCalls int

	// Forwards. forwards records each AttachFromMessage; forwardFail
	// fails the attachment of that name, and forwardErr all of them.
	forwards    []string
	forwardFail map[string]error
	forwardErr  error

	// The address book, kept as the engine keeps it: a contact of each
	// address by its key, changed by mail.ContactState's rules, found by
	// its terms and ranked as the store ranks it. A message sent counts
	// its recipients, as the engine's outbox does. suggested records the
	// text of each Suggest and onSuggest runs as one begins;
	// recipientChecks records the field of each CheckRecipients;
	// contactErr fails the book's reads and edits as a store would.
	book            map[string]*mail.ContactState
	suggested       []string
	onSuggest       func(typed string)
	recipientChecks []string
	contactErr      error

	// Labels, kept as the engine keeps them. labels are each account's,
	// in order. A message's summary holds the labels it has as the server
	// keeps them, and localLabels those kept on this device, by the
	// account and the mail.LabelKey the copies of a message share: a
	// label goes on as a keyword unless the folder's PermanentFlags are
	// known and keep none of it. setLabelErr fails SetLabel as a server
	// that refuses, the cache put back; deleteLabelErr fails DeleteLabel,
	// which with mail.ErrLabelPartlyRemoved has taken the label off the
	// messages of the first folder that had it.
	labels         map[string][]mail.Label
	nextLabel      mail.LabelID
	localLabels    map[string]map[mail.LabelID]bool
	setLabelErr    error
	deleteLabelErr error

	// Rules, kept, checked and matched as the engine keeps, checks and
	// matches them (fakerules_test.go): in their order, with IDs never
	// given again, and their log, oldest first. rawHeaders are the header
	// lines the cache keeps of a message, by bodyKey, which a header
	// condition reads. ruleReport is the window's OnRuleActivity.
	// ruleErr fails the rules' reads and writes as a store would,
	// runWaits leaves the actions of a run pending as a server out of
	// reach does, and undoErr fails the undos. ruleCalls records every
	// call that writes rules or changes mail through them.
	rules      []mail.Rule
	nextRule   mail.RuleID
	ruleLog    []mail.RuleActivity
	nextEntry  mail.ActivityID
	rawHeaders map[string]string
	ruleReport func(accountID string)
	ruleErr    error
	runWaits   error
	undoErr    error
	ruleCalls  []string
}

// fakeScheduled is a message Schedule wrote: its outbox entry, whose
// State follows what became of it, the delay it was given, and the draft
// it was written as, as the store held it when the message was
// scheduled.
type fakeScheduled struct {
	mail.PendingSend
	delay time.Duration
	draft mail.Draft
}

// noDelay is the engine's compose settings with no send delay: the tests
// written before the delay send at once, and those about it set one.
func noDelay() mail.ComposeSettings {
	set := mail.DefaultComposeSettings()
	set.SendDelay = 0
	return set
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

		oauth:      map[string]bool{},
		newAddress: "ann@gmail.com",
		needSignIn: map[string]bool{},

		compose:     noDelay(),
		signatures:  map[string]mail.Signature{},
		identities:  map[string][]mail.Identity{},
		forwardFail: map[string]error{},

		book: map[string]*mail.ContactState{},

		labels:      map[string][]mail.Label{},
		localLabels: map[string]map[mail.LabelID]bool{},

		rawHeaders: map[string]string{},
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
			// As the engine: of an account that signs in with OAuth only
			// the name changes, and it takes no password. Here the window
			// asking for more is an error.
			if stored := f.accounts[i]; stored.OAuth != "" {
				renamed := stored
				renamed.Name = a.Name
				if password != "" || a != renamed {
					return fmt.Errorf("fake: %+v asks more of an OAuth account than a new name", a)
				}
			}
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
				m = f.withLabels(accountID, m)
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
		rows[i] = f.withLabels(rows[i].Account, rows[i])
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
	case sc.Reminded:
		return f.reminded(m.Account, m.Thread) && f.inLabelScope(m.Account, m.Folder)
	case sc.Starred:
		return m.Flagged
	case sc.Label != 0:
		return m.HasLabel(sc.Label) && f.inLabelScope(m.Account, m.Folder)
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
	// Each label: part keeps the messages with a label of the account so
	// named, in any case; what such a search finds is found by its labels.
	for _, name := range s.Labels {
		key := mail.LabelNameKey(strings.TrimSpace(name))
		ok = ok && slices.ContainsFunc(f.labels[accountID], func(l mail.Label) bool {
			return mail.LabelNameKey(l.Name) == key && m.HasLabel(l.ID)
		})
	}
	if ok && len(s.Labels) > 0 {
		fields |= mail.FieldLabel
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
		b.Thread, b.Labels, b.LabelsLocal = s.Thread, s.Labels, s.LabelsLocal
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
			return f.withLabels(accountID, m), true
		}
	}
	return mail.Summary{}, false
}

func (f *fakeService) SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error) {
	if err := f.signedOut(accountID); err != nil {
		return nil, err
	}
	return append([]mail.Folder(nil), f.folders[accountID]...), nil
}

func (f *fakeService) SyncFolder(ctx context.Context, accountID, folder string, limit int) error {
	f.syncCalls = append(f.syncCalls, fmt.Sprintf("%s/%s/%d", accountID, folder, limit))
	return f.signedOut(accountID)
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
	if err := f.signedOut(accountID); err != nil {
		return mail.OlderResult{}, err
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
	if err := f.signedOut(accountID); err != nil {
		return mail.FetchResult{}, err
	}
	s, ok := f.find(accountID, folder, uid)
	if !ok {
		return mail.FetchResult{}, mail.ErrNotFound
	}
	s.Thread = f.threadOf(accountID, s)
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		b.Thread, b.Labels, b.LabelsLocal = s.Thread, s.Labels, s.LabelsLocal
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
	if err := f.signedOut(accountID); err != nil {
		return err
	}
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
	if err := f.signedOut(accountID); err != nil {
		return mail.MoveResult{}, err
	}
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
	if err := f.signedOut(accountID); err != nil {
		return mail.SendResult{}, err
	}
	if f.sendErr != nil {
		return mail.SendResult{}, f.sendErr
	}
	if f.onSend != nil {
		if err := f.onSend(ctx, out); err != nil {
			return mail.SendResult{}, err
		}
	}
	f.sent = append(f.sent, out)
	f.countSent(out)
	// The message is out: its draft goes, here and on the server.
	f.removeDraft(out.DraftID)
	return mail.SendResult{Copied: f.sendWarn == nil, Warning: f.sendWarn, Outbox: mail.PendingSend{ID: "out-1"}}, nil
}

func (f *fakeService) RetryPending(ctx context.Context, id string) (mail.SendResult, error) {
	for _, p := range f.pending {
		if err := f.signedOut(p.AccountID); p.ID == id && err != nil {
			return mail.SendResult{}, err
		}
	}
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
	if err := f.signedOut(accountID); err != nil {
		return mail.Attachment{}, nil, err
	}
	if f.attachment.Part == "" {
		return mail.Attachment{}, nil, mail.ErrNotFound
	}
	if f.attachmentBytes != nil {
		return f.attachment, f.attachmentBytes, nil
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
			old.Body == d.Body && old.InReplyTo == d.InReplyTo && old.Identity == d.Identity {
			return old, nil
		}
		// The content changes; where the copy is, and its revision on
		// the server, do not.
		old.To, old.Cc, old.Bcc, old.Subject, old.Body = d.To, d.Cc, d.Bcc, d.Subject, d.Body
		old.InReplyTo, old.References, old.Identity = d.InReplyTo, d.References, d.Identity
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
	if err := f.signedOut(accountID); err != nil {
		return mail.DraftSyncResult{}, err
	}
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

func (f *fakeService) PrepareCache(ctx context.Context) error {
	f.prepares++
	return nil
}

func (f *fakeService) TempFile(ext string, data []byte) (string, error) {
	file, err := os.CreateTemp("", "shu-*"+filepath.Ext(ext))
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
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
			out = append(out, f.withLabels(accountID, m))
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
		for _, l := range m.Labels {
			if !slices.Contains(c.Labels, l.ID) {
				c.Labels = append(c.Labels, l.ID)
			}
		}
		if newer(m, c.Latest) {
			c.Latest = m
		}
	}
	// A row's labels are in the order of its account's.
	for _, c := range byThread {
		slices.SortFunc(c.Labels, func(x, y mail.LabelID) int { return f.labelPlace(accountID, x) - f.labelPlace(accountID, y) })
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
	if err := f.signedOut(accountID); err != nil {
		return err
	}
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
	if err := f.signedOut(accountID); err != nil {
		return nil, err
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

// signedOut is what an operation of an account that has to sign in again
// fails with before it reaches a server: the engine's error after the
// provider refused to renew the account's sign-in.
func (f *fakeService) signedOut(accountID string) error {
	if !f.needSignIn[accountID] {
		return nil
	}
	return fmt.Errorf("%w (%w)", mail.ErrSignInRequired,
		&mail.OAuthError{Code: "invalid_grant", Description: "Token has been expired or revoked."})
}

func (f *fakeService) OAuthConfigured(provider string) bool { return f.oauth[provider] }

// signInPage is the provider's page a fake sign-in opens.
func signInPage(provider string) string {
	return "https://accounts.example/" + provider + "/authorize?client_id=shu&state=fake-state"
}

// browse is a sign-in's time in the browser: the page opens, and the
// user does what onSignIn says, which decides how it ends, even after a
// cancel, as a sign-in done just as it was cancelled; or signs in at once.
func (f *fakeService) browse(ctx context.Context, provider string, prompt mail.OAuthPrompt) error {
	if !f.oauth[provider] {
		return mail.ErrOAuthNotConfigured
	}
	f.prompts = append(f.prompts, prompt)
	prompt.Open(signInPage(provider))
	if f.onSignIn != nil {
		return f.onSignIn(ctx)
	}
	return ctx.Err()
}

func (f *fakeService) AddOAuthAccount(ctx context.Context, provider, name string, prompt mail.OAuthPrompt) (mail.Account, error) {
	f.signIns = append(f.signIns, "add "+provider)
	if err := f.browse(ctx, provider, prompt); err != nil {
		return mail.Account{}, err
	}
	address := f.newAddress
	if f.signInAs != "" {
		address = f.signInAs
	}
	preset := map[string]string{mail.OAuthGoogle: "gmail", mail.OAuthMicrosoft: "outlook"}[provider]
	servers, _ := mail.ProviderByID(preset)
	a := mail.Account{
		ID: fmt.Sprintf("%s-%d", provider, len(f.accounts)+1), Name: strings.TrimSpace(name),
		Email: address, Username: address, IMAP: servers.IMAP, SMTP: servers.SMTP, OAuth: provider,
	}
	if a.Name == "" {
		a.Name = address
	}
	f.addAccount(a, "", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox, Total: len(f.newInbox)},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
	}, f.newInbox)
	return a, nil
}

func (f *fakeService) SignInAgain(ctx context.Context, accountID string, prompt mail.OAuthPrompt) error {
	f.signIns = append(f.signIns, "again "+accountID)
	var acc mail.Account
	for _, a := range f.accounts {
		if a.ID == accountID {
			acc = a
		}
	}
	switch {
	case acc.ID == "":
		return mail.ErrNotFound
	case acc.OAuth == "":
		return errors.New("fake: the account signs in with a password")
	}
	if err := f.browse(ctx, acc.OAuth, prompt); err != nil {
		return err
	}
	if f.signInAs != "" && !strings.EqualFold(f.signInAs, acc.Email) {
		return fmt.Errorf("%w: signed in as %s, for the account %s", mail.ErrAddressMismatch, f.signInAs, acc.Email)
	}
	delete(f.needSignIn, accountID)
	return nil
}

func (f *fakeService) SignOut(ctx context.Context, accountID string) error {
	f.signOuts = append(f.signOuts, accountID)
	if f.signOutErr != nil {
		return f.signOutErr
	}
	f.needSignIn[accountID] = true
	return nil
}

func (f *fakeService) NeedsSignIn(ctx context.Context, accountID string) (bool, error) {
	f.signInChecks = append(f.signInChecks, accountID)
	return f.needSignIn[accountID], nil
}

func (f *fakeService) ComposeSettings(ctx context.Context) (mail.ComposeSettings, error) {
	return f.compose, nil
}

func (f *fakeService) SaveComposeSettings(ctx context.Context, set mail.ComposeSettings) error {
	if f.composeErr != nil {
		return f.composeErr
	}
	f.compose = set
	f.composeSaved = append(f.composeSaved, set)
	return nil
}

func (f *fakeService) Signature(ctx context.Context, accountID string) (mail.Signature, error) {
	if sig, ok := f.signatures[accountID]; ok {
		return sig, nil
	}
	return mail.DefaultSignature(), nil
}

func (f *fakeService) SaveSignature(ctx context.Context, accountID string, sig mail.Signature) error {
	if f.signatureErr != nil {
		return f.signatureErr
	}
	f.signatures[accountID] = sig
	return nil
}

// fakeAccount is an account of the fake's, by its ID.
func (f *fakeService) fakeAccount(id string) (mail.Account, bool) {
	for _, a := range f.accounts {
		if a.ID == id {
			return a, true
		}
	}
	return mail.Account{}, false
}

func (f *fakeService) Identities(ctx context.Context, accountID string) ([]mail.Identity, error) {
	a, ok := f.fakeAccount(accountID)
	if !ok {
		return nil, mail.ErrNotFound
	}
	own := mail.Identity{AccountID: a.ID, Name: a.Name, Email: a.Email}
	return append([]mail.Identity{own}, f.identities[accountID]...), nil
}

// checkIdentity cleans an identity and refuses it as the engine does: an
// address that is not one alone, or one the account sends from already.
func (f *fakeService) checkIdentity(id *mail.Identity) error {
	a, ok := f.fakeAccount(id.AccountID)
	if !ok {
		return mail.ErrNotFound
	}
	id.Name, id.Email = strings.TrimSpace(id.Name), strings.TrimSpace(id.Email)
	if parsed, err := netmail.ParseAddress(id.Email); err != nil || parsed.Address != id.Email {
		return mail.ErrIdentityAddress
	}
	if strings.EqualFold(id.Email, a.Email) {
		return mail.ErrIdentityExists
	}
	for _, have := range f.identities[a.ID] {
		if have.ID != id.ID && strings.EqualFold(have.Email, id.Email) {
			return mail.ErrIdentityExists
		}
	}
	return f.identityErr
}

func (f *fakeService) AddIdentity(ctx context.Context, accountID, name, email string) (mail.Identity, error) {
	f.nextIdentity++
	id := mail.Identity{ID: fmt.Sprintf("identity-%d", f.nextIdentity), AccountID: accountID, Name: name, Email: email}
	if err := f.checkIdentity(&id); err != nil {
		return mail.Identity{}, err
	}
	f.identities[accountID] = append(f.identities[accountID], id)
	return id, nil
}

func (f *fakeService) UpdateIdentity(ctx context.Context, id mail.Identity) (mail.Identity, error) {
	list := f.identities[id.AccountID]
	i := slices.IndexFunc(list, func(have mail.Identity) bool { return id.ID != "" && have.ID == id.ID })
	if i < 0 {
		return mail.Identity{}, mail.ErrNotFound
	}
	if err := f.checkIdentity(&id); err != nil {
		return mail.Identity{}, err
	}
	list[i] = id
	return id, nil
}

// DeleteIdentity removes an identity, and as the engine does has the
// drafts written from it go from the account's own address.
func (f *fakeService) DeleteIdentity(ctx context.Context, accountID, id string) error {
	list := f.identities[accountID]
	i := slices.IndexFunc(list, func(have mail.Identity) bool { return id != "" && have.ID == id })
	if i < 0 {
		return mail.ErrNotFound
	}
	if f.identityErr != nil {
		return f.identityErr
	}
	f.identities[accountID] = slices.Delete(slices.Clone(list), i, i+1)
	for key, d := range f.drafts {
		if d.AccountID == accountID && d.Identity == id {
			d.Identity = ""
			f.drafts[key] = d
		}
	}
	return nil
}

// now is the time a message is scheduled at.
func (f *fakeService) now() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

// scheduledSend is a message Schedule wrote, by its outbox ID.
func (f *fakeService) scheduledSend(id string) *fakeScheduled {
	for _, s := range f.scheduled {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Schedule writes a message to send once its delay is over, as the
// engine does: from the identity chosen, and refused as Send refuses it.
func (f *fakeService) Schedule(ctx context.Context, accountID string, out mail.Outgoing, delay time.Duration) (mail.PendingSend, error) {
	f.act(accountID, "Schedule %s %v", out.Subject, delay)
	if err := f.signedOut(accountID); err != nil {
		return mail.PendingSend{}, err
	}
	if f.scheduleErr != nil {
		return mail.PendingSend{}, f.scheduleErr
	}
	if len(out.To) == 0 {
		return mail.PendingSend{}, errors.New("fake: the message has no recipients")
	}
	identities, err := f.Identities(ctx, accountID)
	if err != nil {
		return mail.PendingSend{}, err
	}
	out.From = identities[0].Address()
	for _, id := range identities {
		if id.ID == out.Identity {
			out.From = id.Address()
		}
	}
	now := f.now()
	s := &fakeScheduled{PendingSend: mail.PendingSend{
		ID: fmt.Sprintf("outbox-%d", len(f.scheduled)+1), AccountID: accountID, Envelope: out,
		State: mail.StateScheduled, CreatedAt: now, DueAt: now.Add(delay),
	}, delay: delay}
	if d, ok := f.drafts[out.DraftID]; ok {
		s.DraftID, s.draft = d.ID, d
	}
	f.scheduled = append(f.scheduled, s)
	return s.PendingSend, nil
}

// SendScheduled waits for a scheduled message as the engine does: the
// test's onScheduled is the wait. One taken back meanwhile is not sent,
// nor one whose wait ended with ctx; the others leave, or fail and stay
// among the pending sends, as Send's do.
func (f *fakeService) SendScheduled(ctx context.Context, id string) (mail.SendResult, error) {
	s := f.scheduledSend(id)
	if s == nil {
		return mail.SendResult{}, mail.ErrNotFound
	}
	f.act(s.AccountID, "SendScheduled %s", s.Envelope.Subject)
	if s.State == mail.StateCancelled {
		return mail.SendResult{}, mail.ErrUnsent
	}
	if s.State != mail.StateScheduled {
		return mail.SendResult{}, mail.ErrAlreadyLeft
	}
	if f.onScheduled != nil {
		f.onScheduled(ctx, id)
	}
	if s.State == mail.StateCancelled {
		return mail.SendResult{}, mail.ErrUnsent
	}
	if err := ctx.Err(); err != nil {
		return mail.SendResult{}, err
	}
	err := f.signedOut(s.AccountID)
	if err == nil {
		err = f.sendErr
	}
	if err != nil {
		s.State, s.LastError = mail.StateFailed, err.Error()
		f.pending = append(f.pending, s.PendingSend)
		return mail.SendResult{}, err
	}
	s.State, s.SentAt = mail.StateSent, f.now()
	f.sent = append(f.sent, s.Envelope)
	f.countSent(s.Envelope)
	f.removeDraft(s.DraftID)
	return mail.SendResult{Copied: f.sendWarn == nil, Warning: f.sendWarn, Outbox: s.PendingSend}, nil
}

// Unsend takes a scheduled message back and returns its draft, or one
// made from the message when it has none, as the engine does.
func (f *fakeService) Unsend(ctx context.Context, id string) (mail.Draft, error) {
	s := f.scheduledSend(id)
	if s == nil {
		return mail.Draft{}, mail.ErrNotFound
	}
	f.act(s.AccountID, "Unsend %s", s.Envelope.Subject)
	if f.unsendErr != nil {
		return mail.Draft{}, f.unsendErr
	}
	switch s.State {
	case mail.StateScheduled:
		s.State = mail.StateCancelled
	case mail.StateCancelled:
		return mail.Draft{}, mail.ErrUnsent
	default:
		return mail.Draft{}, mail.ErrAlreadyLeft
	}
	if d, ok := f.drafts[s.DraftID]; ok {
		return d, nil
	}
	out := s.Envelope
	d := mail.Draft{
		ID: "unsent-" + s.ID, AccountID: s.AccountID,
		To: joinAddresses(out.To), Cc: joinAddresses(out.Cc), Bcc: joinAddresses(out.Bcc),
		Subject: out.Subject, Body: out.Text, InReplyTo: out.InReplyTo, References: out.References,
		Identity: out.Identity, MessageID: out.MessageID, Attachments: out.Attachments, Revision: 1,
	}
	f.drafts[d.ID] = d
	return d, nil
}

func (f *fakeService) DeliverScheduled(ctx context.Context) ([]mail.ScheduledResult, error) {
	f.deliverCalls++
	return f.delivered, f.deliverErr
}

// AttachFromMessage copies the attachments the fake's body of a message
// lists into a draft, which it makes as d says if it is not stored, and
// reports those forwardFail names as not copied, as the engine does.
func (f *fakeService) AttachFromMessage(ctx context.Context, d mail.Draft, accountID, folder string, uid mail.UID) (mail.Draft, []mail.AttachFailure, error) {
	f.forwards = append(f.forwards, fmt.Sprintf("%s/%s/%d", accountID, folder, uid))
	if err := f.signedOut(accountID); err != nil {
		return mail.Draft{}, nil, err
	}
	if f.forwardErr != nil {
		return mail.Draft{}, nil, f.forwardErr
	}
	m, ok := f.bodies[bodyKey(accountID, folder, uid)]
	if !ok || d.AccountID != accountID {
		return mail.Draft{}, nil, mail.ErrNotFound
	}
	cur, ok := f.drafts[d.ID]
	if !ok {
		var err error
		if cur, err = f.SaveDraft(ctx, d); err != nil {
			return mail.Draft{}, nil, err
		}
	}
	var failures []mail.AttachFailure
	list := slices.Clone(cur.Attachments)
	for _, att := range m.Attachments {
		if err := f.forwardFail[att.Filename]; err != nil {
			failures = append(failures, mail.AttachFailure{Attachment: att, Err: err})
			continue
		}
		list = append(list, mail.Attachment{
			Filename: att.Filename, ContentType: att.ContentType, Size: att.Size,
			ContentHash: "copy-of-" + folder + "/" + itoa(uid) + "/" + att.Part,
		})
	}
	if len(list) == len(cur.Attachments) {
		return cur, failures, nil
	}
	cur.Attachments = list
	return f.touchDraft(cur), failures, nil
}

// wrote has the user write to someone at a time, as a message sent does
// in the engine's address book.
func (f *fakeService) wrote(name, email string, at time.Time) {
	f.countContact(mail.ContactEvent{Email: email, Name: name, Sent: true, At: at})
}

// heard has someone write to the user at a time, as a message cached
// does.
func (f *fakeService) heard(name, email string, at time.Time) {
	f.countContact(mail.ContactEvent{Email: email, Name: name, At: at})
}

func (f *fakeService) countContact(e mail.ContactEvent) {
	key := mail.ContactKey(e.Email)
	c := f.book[key]
	if c == nil {
		c = &mail.ContactState{}
	}
	if c.Apply(e) {
		f.book[key] = c
	}
}

// countSent counts the recipients of a message sent, as the engine's
// outbox does once it records it as sent.
func (f *fakeService) countSent(out mail.Outgoing) {
	for _, e := range mail.SentContacts(out, f.now(), f.ownAddress) {
		f.countContact(e)
	}
}

// ownAddress reports whether an address is the user's, as the engine
// reads it: an account's or an identity's, whatever its case.
func (f *fakeService) ownAddress(email string) bool {
	key := mail.ContactKey(email)
	for _, a := range f.accounts {
		if mail.ContactKey(a.Email) == key {
			return true
		}
	}
	for _, ids := range f.identities {
		for _, id := range ids {
			if mail.ContactKey(id.Email) == key {
				return true
			}
		}
	}
	return false
}

// contactRanksBefore orders the address book as the store does: the
// favorites, then those the user wrote to or added, by what the user
// wrote them, then those that only wrote to the user, by what they
// wrote; the key breaks what is left of a tie.
func contactRanksBefore(ka string, a *mail.ContactState, kb string, b *mail.ContactState) bool {
	wrote := func(c *mail.ContactState) bool { return c.Sent > 0 || c.Added }
	score := func(c *mail.ContactState) float64 {
		if wrote(c) {
			return c.SentScore
		}
		return c.ReceivedScore
	}
	switch {
	case a.Favorite != b.Favorite:
		return a.Favorite
	case wrote(a) != wrote(b):
		return wrote(a)
	case score(a) != score(b):
		return score(a) > score(b)
	case a.ReceivedScore != b.ReceivedScore:
		return a.ReceivedScore > b.ReceivedScore
	}
	return ka < kb
}

// readBook reads the address book as the store does (mail.ContactQuery):
// the live contacts that are not the user's and not left out, each word
// beginning a term of theirs, best first.
func (f *fakeService) readBook(q mail.ContactQuery) ([]mail.Contact, error) {
	if f.contactErr != nil {
		return nil, f.contactErr
	}
	excluded := map[string]bool{}
	for _, email := range q.Exclude {
		excluded[mail.ContactKey(email)] = true
	}
	var keys []string
	for key, c := range f.book {
		if c.Deleted || f.ownAddress(c.Email) || excluded[key] {
			continue
		}
		terms := mail.ContactTerms(c.Name, c.Email)
		found := true
		for _, w := range q.Words {
			found = found && slices.ContainsFunc(terms, func(t string) bool { return strings.HasPrefix(t, w) })
		}
		if found {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return contactRanksBefore(keys[i], f.book[keys[i]], keys[j], f.book[keys[j]]) })
	keys = keys[min(max(q.Offset, 0), len(keys)):]
	if q.Limit > 0 && len(keys) > q.Limit {
		keys = keys[:q.Limit]
	}
	out := []mail.Contact{}
	for _, key := range keys {
		out = append(out, f.book[key].Contact())
	}
	return out, nil
}

func (f *fakeService) Suggest(ctx context.Context, typed string, exclude []string, limit int) ([]mail.Contact, error) {
	f.suggested = append(f.suggested, typed)
	if f.onSuggest != nil {
		f.onSuggest(typed)
	}
	words := mail.ContactWords(typed)
	if len(words) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = mail.DefaultSuggestions
	}
	return f.readBook(mail.ContactQuery{Words: words, Exclude: exclude, Limit: limit})
}

func (f *fakeService) Contacts(ctx context.Context, search string, offset, limit int) (mail.ContactPage, error) {
	if limit <= 0 {
		limit = mail.DefaultPageSize
	}
	list, err := f.readBook(mail.ContactQuery{Words: mail.ContactWords(search), Offset: max(offset, 0), Limit: limit + 1})
	if err != nil {
		return mail.ContactPage{}, err
	}
	page := mail.ContactPage{Contacts: list, More: len(list) > limit}
	if page.More {
		page.Contacts = list[:limit]
	}
	return page, nil
}

// cleanContactName is a name as the engine keeps it, its spaces
// collapsed.
func cleanContactName(name string) string { return strings.Join(strings.Fields(name), " ") }

// AddContact refuses what the engine refuses: what is not one address
// alone, one of the user's own, and one the book has.
func (f *fakeService) AddContact(ctx context.Context, name, email string) (mail.Contact, error) {
	email = strings.TrimSpace(email)
	if a, err := netmail.ParseAddress(email); err != nil || a.Name != "" || a.Address != email {
		return mail.Contact{}, mail.ErrContactAddress
	}
	if f.contactErr != nil {
		return mail.Contact{}, f.contactErr
	}
	if f.ownAddress(email) {
		return mail.Contact{}, mail.ErrContactOwn
	}
	key := mail.ContactKey(email)
	c := f.book[key]
	if c != nil && !c.Deleted {
		return mail.Contact{}, mail.ErrContactExists
	}
	if c == nil {
		c = &mail.ContactState{}
		f.book[key] = c
	}
	c.Add(cleanContactName(name), email)
	return c.Contact(), nil
}

// editContact changes a contact of the book: one there and not deleted.
func (f *fakeService) editContact(email string, edit func(*mail.ContactState)) (mail.Contact, error) {
	if f.contactErr != nil {
		return mail.Contact{}, f.contactErr
	}
	c := f.book[mail.ContactKey(email)]
	if c == nil || c.Deleted {
		return mail.Contact{}, mail.ErrNotFound
	}
	edit(c)
	return c.Contact(), nil
}

func (f *fakeService) RenameContact(ctx context.Context, email, name string) (mail.Contact, error) {
	return f.editContact(email, func(c *mail.ContactState) { c.Rename(cleanContactName(name)) })
}

func (f *fakeService) FavoriteContact(ctx context.Context, email string, on bool) (mail.Contact, error) {
	return f.editContact(email, func(c *mail.ContactState) { c.Favorite = on })
}

func (f *fakeService) DeleteContact(ctx context.Context, email string) error {
	_, err := f.editContact(email, func(c *mail.ContactState) { c.Forget(f.now()) })
	return err
}

// CheckRecipients reads a recipient field as the engine does: whole when
// net/mail reads it so, else entry by entry, a bare name or word asked of
// the address book.
func (f *fakeService) CheckRecipients(ctx context.Context, field string) (mail.RecipientCheck, error) {
	f.recipientChecks = append(f.recipientChecks, field)
	if strings.TrimSpace(field) == "" {
		return mail.RecipientCheck{}, nil
	}
	if list, err := mail.ParseAddressList([]string{field}); err == nil {
		return mail.RecipientCheck{Addresses: list}, nil
	}
	var check mail.RecipientCheck
	for _, e := range mail.RecipientEntries(field) {
		if e.Valid {
			check.Addresses = append(check.Addresses, e.Addresses...)
			continue
		}
		p := mail.RecipientProblem{RecipientEntry: e, Kind: mail.RecipientInvalid}
		if words := mail.ContactWords(e.Text); len(words) > 0 && !strings.ContainsAny(e.Text, `@<>":;`) {
			matches, err := f.readBook(mail.ContactQuery{Words: words, Limit: mail.DefaultSuggestions})
			if err != nil {
				return mail.RecipientCheck{}, err
			}
			p.Matches = matches
			switch len(matches) {
			case 0:
				p.Kind = mail.RecipientUnknown
			case 1:
				p.Kind = mail.RecipientMatch
			default:
				p.Kind = mail.RecipientAmbiguous
			}
		}
		check.Problems = append(check.Problems, p)
	}
	if len(check.Problems) == 0 {
		check.Problems = []mail.RecipientProblem{{
			RecipientEntry: mail.RecipientEntry{Text: strings.TrimSpace(field), End: len(field)},
			Kind:           mail.RecipientInvalid,
		}}
	}
	return check, nil
}

// fakeFolder is an account's folder, by its name.
func (f *fakeService) fakeFolder(accountID, name string) (mail.Folder, bool) {
	for _, folder := range f.folders[accountID] {
		if folder.Name == name {
			return folder, true
		}
	}
	return mail.Folder{}, false
}

// inLabelScope reports whether a label's list shows the messages of an
// account's folder: those of every folder but Junk, Trash and Drafts.
func (f *fakeService) inLabelScope(accountID, folder string) bool {
	fl, ok := f.fakeFolder(accountID, folder)
	return ok && fl.InLabelScope()
}

// labelPlace is where a label is among its account's, or -1.
func (f *fakeService) labelPlace(accountID string, id mail.LabelID) int {
	return slices.IndexFunc(f.labels[accountID], func(l mail.Label) bool { return l.ID == id })
}

// localLabelKey is what the labels kept on this device know an account's
// message by, whichever copy of it.
func localLabelKey(accountID string, m mail.Summary) string {
	return accountID + "\x00" + mail.LabelKey(m)
}

// withLabels gives a message of an account the labels it has, as the
// engine's reads do: those of its summary, as the server keeps them, and
// those kept on this device for any copy of it, in the order of the
// account's labels; and whether its folder keeps no new keyword.
func (f *fakeService) withLabels(accountID string, m mail.Summary) mail.Summary {
	here := f.localLabels[localLabelKey(accountID, m)]
	var out []mail.MessageLabel
	for _, l := range f.labels[accountID] {
		switch {
		case slices.ContainsFunc(m.Labels, func(ml mail.MessageLabel) bool { return ml.ID == l.ID && !ml.Local }):
			out = append(out, mail.MessageLabel{ID: l.ID})
		case here[l.ID]:
			out = append(out, mail.MessageLabel{ID: l.ID, Local: true})
		}
	}
	m.Labels = out
	folder, _ := f.fakeFolder(accountID, m.Folder)
	m.LabelsLocal = folder.KeywordsKnown() && !folder.KeepsNewKeywords()
	return m
}

// addLabel gives an account a label, as the user made it.
func (f *fakeService) addLabel(accountID, name string, color mail.LabelColor) mail.Label {
	l, err := f.CreateLabel(context.Background(), accountID, name, color)
	if err != nil {
		panic(err)
	}
	return l
}

// putLabel puts a label on messages of a folder as the server keeps it,
// as another mail client or a sync did.
func (f *fakeService) putLabel(accountID, folder string, id mail.LabelID, uids ...mail.UID) {
	list := f.messages[listKey(accountID, folder)]
	for i := range list {
		if slices.Contains(uids, list[i].UID) && !list[i].HasLabel(id) {
			list[i].Labels = append(slices.Clone(list[i].Labels), mail.MessageLabel{ID: id})
		}
	}
}

// keepNoKeywords has an account's folders keep no keyword, as Outlook's,
// once they were opened to change.
func (f *fakeService) keepNoKeywords(accountID string) {
	for i := range f.folders[accountID] {
		f.folders[accountID][i].PermanentFlags = []string{`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, `\Draft`}
	}
}

// labelNow is a label with how many unread messages of its list have it,
// and whether it is kept on this device only.
func (f *fakeService) labelNow(accountID string, l mail.Label) mail.Label {
	l.Local, l.Unread = mail.KeptLocally(f.folders[accountID], l.Keyword), 0
	for _, m := range f.everyMessage(accountID) {
		if !m.Seen && m.HasLabel(l.ID) && f.inLabelScope(accountID, m.Folder) {
			l.Unread++
		}
	}
	return l
}

func (f *fakeService) Labels(ctx context.Context, accountID string) ([]mail.Label, error) {
	out := make([]mail.Label, 0, len(f.labels[accountID]))
	for _, l := range f.labels[accountID] {
		out = append(out, f.labelNow(accountID, l))
	}
	return out, nil
}

// CreateLabel adds a label after an account's others, refusing what the
// engine refuses.
func (f *fakeService) CreateLabel(ctx context.Context, accountID, name string, color mail.LabelColor) (mail.Label, error) {
	name, err := mail.CleanLabelName(name)
	if err != nil {
		return mail.Label{}, err
	}
	if color != "" && !color.Valid() {
		return mail.Label{}, mail.ErrLabelColor
	}
	if _, ok := f.fakeAccount(accountID); !ok {
		return mail.Label{}, mail.ErrNotFound
	}
	list := f.labels[accountID]
	if slices.ContainsFunc(list, func(l mail.Label) bool { return mail.LabelNameKey(l.Name) == mail.LabelNameKey(name) }) {
		return mail.Label{}, mail.ErrLabelExists
	}
	if color == "" {
		color = mail.LabelColorAt(len(list))
	}
	keyword := mail.FreeKeyword(mail.LabelKeyword(name), func(lower string) bool {
		return slices.ContainsFunc(list, func(l mail.Label) bool { return strings.ToLower(l.Keyword) == lower })
	})
	f.nextLabel++
	l := mail.Label{ID: f.nextLabel, Account: accountID, Name: name, Color: color, Keyword: keyword}
	f.labels[accountID] = append(slices.Clone(list), l)
	return f.labelNow(accountID, l), nil
}

// UpdateLabel renames a label and changes its color, "" keeping it; its
// keyword stays.
func (f *fakeService) UpdateLabel(ctx context.Context, accountID string, id mail.LabelID, name string, color mail.LabelColor) (mail.Label, error) {
	name, err := mail.CleanLabelName(name)
	if err != nil {
		return mail.Label{}, err
	}
	if color != "" && !color.Valid() {
		return mail.Label{}, mail.ErrLabelColor
	}
	i := f.labelPlace(accountID, id)
	if i < 0 {
		return mail.Label{}, mail.ErrNotFound
	}
	list := slices.Clone(f.labels[accountID])
	if slices.ContainsFunc(list, func(l mail.Label) bool { return l.ID != id && mail.LabelNameKey(l.Name) == mail.LabelNameKey(name) }) {
		return mail.Label{}, mail.ErrLabelExists
	}
	list[i].Name = name
	if color != "" {
		list[i].Color = color
	}
	f.labels[accountID] = list
	return f.labelNow(accountID, list[i]), nil
}

// SetLabel puts a label on messages or takes it off, as the engine does:
// as a keyword where the folder keeps it or is not known yet, else on
// this device by the message's key; taking it off takes it off both.
// Only a change of keywords reaches the server.
func (f *fakeService) SetLabel(ctx context.Context, accountID string, msgs []mail.MessageRef, id mail.LabelID, on bool) error {
	refs := make([]string, 0, len(msgs))
	for _, r := range msgs {
		refs = append(refs, r.Folder+"/"+itoa(r.UID))
	}
	f.act(accountID, "SetLabel %s %d %v", strings.Join(refs, ","), id, on)
	i := f.labelPlace(accountID, id)
	if i < 0 {
		return mail.ErrNotFound
	}
	label := f.labels[accountID][i]
	kept := func(folder string) bool {
		fl, _ := f.fakeFolder(accountID, folder)
		return !fl.KeywordsKnown() || fl.KeepsKeyword(label.Keyword)
	}
	if slices.ContainsFunc(msgs, func(r mail.MessageRef) bool { return kept(r.Folder) }) {
		if err := f.signedOut(accountID); err != nil {
			return err
		}
		if f.setLabelErr != nil {
			return f.setLabelErr
		}
	}
	for _, r := range msgs {
		list := f.messages[listKey(accountID, r.Folder)]
		for j := range list {
			if list[j].UID != r.UID {
				continue
			}
			key := localLabelKey(accountID, list[j])
			switch {
			case on && !kept(r.Folder):
				if f.localLabels[key] == nil {
					f.localLabels[key] = map[mail.LabelID]bool{}
				}
				f.localLabels[key][id] = true
			case on:
				if !list[j].HasLabel(id) {
					list[j].Labels = append(slices.Clone(list[j].Labels), mail.MessageLabel{ID: id})
				}
			default:
				list[j].Labels = slices.DeleteFunc(slices.Clone(list[j].Labels), func(ml mail.MessageLabel) bool { return ml.ID == id })
				delete(f.localLabels[key], id)
			}
		}
	}
	return nil
}

// DeleteLabel takes a label off every message, as the server keeps it
// and on this device, then drops it; no message moves. It reaches the
// server unless every folder is known to keep none of its keyword.
func (f *fakeService) DeleteLabel(ctx context.Context, accountID string, id mail.LabelID) error {
	f.act(accountID, "DeleteLabel %d", id)
	i := f.labelPlace(accountID, id)
	if i < 0 {
		return mail.ErrNotFound
	}
	label := f.labels[accountID][i]
	if slices.ContainsFunc(f.folders[accountID], func(fl mail.Folder) bool { return !fl.KeywordsKnown() || fl.KeepsKeyword(label.Keyword) }) {
		if err := f.signedOut(accountID); err != nil {
			return err
		}
		if err := f.deleteLabelErr; err != nil {
			if errors.Is(err, mail.ErrLabelPartlyRemoved) {
				for _, folder := range f.folders[accountID] {
					if f.unlabelFolder(accountID, folder.Name, id) {
						break
					}
				}
			}
			return err
		}
	}
	for _, folder := range f.folders[accountID] {
		f.unlabelFolder(accountID, folder.Name, id)
	}
	for _, here := range f.localLabels {
		delete(here, id)
	}
	f.labels[accountID] = slices.Delete(slices.Clone(f.labels[accountID]), i, i+1)
	return nil
}

// unlabelFolder takes a label off the messages of a folder as the server
// keeps it, and reports whether any had it.
func (f *fakeService) unlabelFolder(accountID, folder string, id mail.LabelID) bool {
	list := f.messages[listKey(accountID, folder)]
	had := false
	for j := range list {
		if slices.ContainsFunc(list[j].Labels, func(ml mail.MessageLabel) bool { return ml.ID == id }) {
			list[j].Labels = slices.DeleteFunc(slices.Clone(list[j].Labels), func(ml mail.MessageLabel) bool { return ml.ID == id })
			had = true
		}
	}
	return had
}

// CreateFolder adds a folder to the fake account, as the engine does once
// the server made it.
func (f *fakeService) CreateFolder(ctx context.Context, accountID, name string) (mail.Folder, error) {
	if f.folderErr != nil {
		return mail.Folder{}, f.folderErr
	}
	for _, fl := range f.folders[accountID] {
		if fl.Name == name {
			return mail.Folder{}, mail.ErrFolderExists
		}
	}
	f.folderCalls = append(f.folderCalls, "create "+name)
	folder := mail.Folder{Name: name, Delimiter: "/", Subscribed: true}
	f.folders[accountID] = append(f.folders[accountID], folder)
	f.messages[listKey(accountID, name)] = nil
	return folder, nil
}

// RenameFolder renames a folder and the folders below it, keeping their
// mail, as the engine's store does.
func (f *fakeService) RenameFolder(ctx context.Context, accountID, from, to string) error {
	if f.folderErr != nil {
		return f.folderErr
	}
	for _, fl := range f.folders[accountID] {
		if fl.Name == to {
			return mail.ErrFolderExists
		}
	}
	f.folderCalls = append(f.folderCalls, "rename "+from+" "+to)
	found := false
	for i := range f.folders[accountID] {
		name := f.folders[accountID][i].Name
		if name == from || strings.HasPrefix(name, from+"/") {
			next := to + strings.TrimPrefix(name, from)
			if msgs, ok := f.messages[listKey(accountID, name)]; ok {
				delete(f.messages, listKey(accountID, name))
				f.messages[listKey(accountID, next)] = msgs
			}
			for j := range f.messages[listKey(accountID, next)] {
				f.messages[listKey(accountID, next)][j].Folder = next
			}
			f.folders[accountID][i].Name = next
			found = true
		}
	}
	if !found {
		return mail.ErrNotFound
	}
	return nil
}

func (f *fakeService) DeleteFolder(ctx context.Context, accountID, name string) error {
	if f.folderErr != nil {
		return f.folderErr
	}
	f.folderCalls = append(f.folderCalls, "delete "+name)
	kept := f.folders[accountID][:0]
	found := false
	for _, fl := range f.folders[accountID] {
		if fl.Name == name {
			found = true
			continue
		}
		kept = append(kept, fl)
	}
	if !found {
		return mail.ErrNotFound
	}
	f.folders[accountID] = kept
	delete(f.messages, listKey(accountID, name))
	return nil
}

func (f *fakeService) SetFolderSubscribed(ctx context.Context, accountID, name string, on bool) error {
	if f.folderErr != nil {
		return f.folderErr
	}
	f.folderCalls = append(f.folderCalls, fmt.Sprintf("subscribe %s %v", name, on))
	for i := range f.folders[accountID] {
		if f.folders[accountID][i].Name == name {
			f.folders[accountID][i].Subscribed = on
			return nil
		}
	}
	return mail.ErrNotFound
}

func (f *fakeService) NetworkSettings(ctx context.Context) (mail.NetworkSettings, bool, error) {
	return f.networkSet, f.networkStored, f.networkErr
}

func (f *fakeService) SaveNetworkSettings(ctx context.Context, set mail.NetworkSettings, password string, setPassword bool) error {
	if f.networkErr != nil {
		return f.networkErr
	}
	f.networkSet = set
	if setPassword {
		f.networkStored = password != ""
	}
	return nil
}

func (f *fakeService) TestNetwork(ctx context.Context, accountID string) error { return f.testErr }

func (f *fakeService) HTTPClient(ctx context.Context) (*http.Client, error) {
	if f.networkErr != nil {
		return nil, f.networkErr
	}
	return http.DefaultClient, nil
}

func (f *fakeService) RemoteImages(ctx context.Context) (map[string]bool, error) {
	return f.remote, f.remoteErr
}

func (f *fakeService) SetRemoteImages(ctx context.Context, from mail.Address, on bool) error {
	if f.remoteErr != nil {
		return f.remoteErr
	}
	if f.remote == nil {
		f.remote = map[string]bool{}
	}
	key := mail.RemoteImageKey(from)
	if on {
		f.remote[key] = true
	} else {
		delete(f.remote, key)
	}
	return nil
}

func (f *fakeService) ImportMbox(ctx context.Context, accountID, folder string, r io.Reader, progress func(mail.ImportResult)) (mail.ImportResult, error) {
	if f.transferErr != nil {
		return mail.ImportResult{}, f.transferErr
	}
	data, _ := io.ReadAll(r)
	count := strings.Count(string(data), "From ")
	f.imported = append(f.imported, fmt.Sprintf("%s/%s/%d", accountID, folder, count))
	if progress != nil {
		progress(mail.ImportResult{Imported: count})
	}
	return mail.ImportResult{Imported: count}, nil
}

func (f *fakeService) ImportEML(ctx context.Context, accountID, folder string, raw []byte) (mail.ImportResult, error) {
	if f.transferErr != nil {
		return mail.ImportResult{}, f.transferErr
	}
	f.imported = append(f.imported, accountID+"/"+folder+"/eml")
	return mail.ImportResult{Imported: 1}, nil
}

func (f *fakeService) Export(ctx context.Context, req mail.ExportRequest, w io.Writer, progress func(mail.ExportResult)) (mail.ExportResult, error) {
	if f.transferErr != nil {
		return mail.ExportResult{}, f.transferErr
	}
	if _, err := io.WriteString(w, "From a@example.com Mon Jan  2 15:04:05 2006\nFrom: Alice <alice@example.com>\nSubject: Exported\n\nHello\n"); err != nil {
		return mail.ExportResult{}, err
	}
	f.exported = append(f.exported, fmt.Sprintf("%s/%s/%s/%v", req.AccountID, req.Folder, req.Format, req.IncludeAttachments))
	if progress != nil {
		progress(mail.ExportResult{Messages: 1})
	}
	return mail.ExportResult{Messages: 1}, nil
}

func (f *fakeService) BackupConfig(ctx context.Context) ([]byte, error) {
	if f.transferErr != nil {
		return nil, f.transferErr
	}
	return []byte(`{"version":1,"accounts":[]}`), nil
}

func (f *fakeService) RestoreConfig(ctx context.Context, data []byte) (int, error) {
	if f.transferErr != nil {
		return 0, f.transferErr
	}
	f.restored = append(f.restored, string(data))
	return 1, nil
}

// reminded reports whether a conversation has a reminder.
func (f *fakeService) reminded(accountID string, thread mail.ThreadID) bool {
	for _, r := range f.reminders {
		if r.Account == accountID && r.Thread == thread {
			return true
		}
	}
	return false
}

func (f *fakeService) reminderOf(accountID string, thread mail.ThreadID) *mail.Reminder {
	for i := range f.reminders {
		if f.reminders[i].Account == accountID && f.reminders[i].Thread == thread {
			return &f.reminders[i]
		}
	}
	return nil
}

func (f *fakeService) Reminders(ctx context.Context, accountID string) ([]mail.Reminder, error) {
	if f.reminderErr != nil {
		return nil, f.reminderErr
	}
	out := []mail.Reminder{}
	for _, r := range f.reminders {
		if r.Account == accountID {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RemindAt.Before(out[j].RemindAt) })
	return out, nil
}

func (f *fakeService) SetReminder(ctx context.Context, accountID string, thread mail.ThreadID, at time.Time) (mail.Reminder, []mail.MoveResult, error) {
	if f.reminderErr != nil {
		return mail.Reminder{}, nil, f.reminderErr
	}
	f.reminderSet = append(f.reminderSet, fmt.Sprintf("%s/%d/%d", accountID, thread, at.Unix()))
	var refs []mail.MessageRef
	subject := ""
	for _, m := range f.messages[listKey(accountID, "INBOX")] {
		if f.threadOf(accountID, m) == thread {
			refs = append(refs, mail.MessageRef{Folder: m.Folder, UID: m.UID})
			subject = m.Subject
		}
	}
	moved, err := f.MoveMessages(ctx, accountID, refs, "Archive")
	if err != nil {
		return mail.Reminder{}, moved, err
	}
	r := f.reminderOf(accountID, thread)
	if r == nil {
		f.reminderSeq++
		f.reminders = append(f.reminders, mail.Reminder{ID: f.reminderSeq, Account: accountID, Thread: thread})
		r = &f.reminders[len(f.reminders)-1]
	}
	r.Subject, r.RemindAt, r.CreatedAt = subject, at, time.Now()
	return *r, moved, nil
}

func (f *fakeService) RemoveReminder(ctx context.Context, accountID string, thread mail.ThreadID) error {
	if f.reminderErr != nil {
		return f.reminderErr
	}
	f.reminders = slices.DeleteFunc(f.reminders, func(r mail.Reminder) bool {
		return r.Account == accountID && r.Thread == thread
	})
	return nil
}

// RestoreReminder saves a reminder as the undo of a new time has it.
func (f *fakeService) RestoreReminder(ctx context.Context, r mail.Reminder) (mail.Reminder, error) {
	if f.reminderErr != nil {
		return mail.Reminder{}, f.reminderErr
	}
	if got := f.reminderOf(r.Account, r.Thread); got != nil {
		got.Subject, got.RemindAt = r.Subject, r.RemindAt
		return *got, nil
	}
	f.reminderSeq++
	r.ID = f.reminderSeq
	f.reminders = append(f.reminders, r)
	return r, nil
}

// DeliverReminders brings back those due now, as the engine's clock would.
func (f *fakeService) DeliverReminders(ctx context.Context) ([]mail.Reminder, error) {
	if f.reminderErr != nil {
		return nil, f.reminderErr
	}
	now := time.Now()
	var delivered []mail.Reminder
	for _, r := range append([]mail.Reminder(nil), f.reminders...) {
		if r.RemindAt.After(now) {
			continue
		}
		var refs []mail.MessageRef
		for _, m := range f.messages[listKey(r.Account, "Archive")] {
			if f.threadOf(r.Account, m) == r.Thread {
				refs = append(refs, mail.MessageRef{Folder: m.Folder, UID: m.UID})
			}
		}
		if _, err := f.MoveMessages(ctx, r.Account, refs, "INBOX"); err != nil {
			return delivered, err
		}
		_ = f.RemoveReminder(ctx, r.Account, r.Thread)
		delivered = append(delivered, r)
	}
	return delivered, nil
}

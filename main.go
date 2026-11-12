package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// app is the state the windows show.
// undoMove remembers the last move so the list can offer to take it
// back. The destination UID comes from the server's MOVE/COPY response.
type undoMove struct {
	accountID string
	source    string
	dest      string
	destUID   mail.UID
	label     string
	// moved is a move of several messages, as a conversation's, by the
	// account they are in, which MoveBackMessages takes back; accountID,
	// source, dest and destUID are then not used.
	moved map[string][]mail.MoveResult
}

type app struct {
	svc mailService
	// ai is the AI layer, nil where there is none: the AI controls then
	// do not show.
	ai aiService

	win    *mygo.Window
	run    func(func())
	update func(func())
	closed bool

	started bool
	gen     uint64

	accounts []mail.Account
	active   string
	folders  []mail.Folder
	folder   string
	query    string

	all      []mail.Summary
	selected int
	// In conversation view the rows are convs, and all holds the newest
	// message of each, row for row. conv is the conversation open in the
	// reading pane, convExpand a message the next one shown expands, and
	// convToggle the list header's switch between the views.
	convs       []mail.Conversation
	conv        *convView
	convExpand  string
	convToggle  bool
	list        ui.ListState
	sel         ui.Selection[string]
	searchField ui.Handle
	focusList   bool
	// pager is how far the list has read the cache, and older, by account
	// and folder, how much of a folder the cache holds and the fetch of
	// the rest from the server.
	pager      pager
	older      map[string]*olderMail
	bodies     map[string]mail.FetchResult
	bodyLocked bool
	bodyErr    string
	loading    bool
	err        string

	pending          []mail.PendingSend
	pendingDismissed bool
	undo             *undoMove
	undoTimer        *time.Timer

	showAccounts  bool
	showSettings  bool
	accountOpen   bool
	sidebarOpen   bool
	mailboxesOpen bool
	details       bool
	accountForm   accountForm
	accountBusy   bool
	accountErr    string
	accountNote   string

	htmlOn   bool
	webview  *mygo.WebView
	htmlKey  string
	services ui.Services

	// AI state: the settings form, the summaries of this account's
	// messages by key, the triage of the list on screen, and the payload
	// being inspected.
	settingsTab int
	aiForm      aiSettings
	aiSaveMu    sync.Mutex
	aiSaveSeq   atomic.Uint64
	summaries   map[string]*aiJob
	triage      *triageState
	review      *aiReview

	// Receiving mail in the background. watches is whether the accounts
	// are watched at all; watched is each watched account as it was when
	// its watcher was asked for, and watch what that watcher last said.
	// watchOps are the calls that start and stop watchers, in order.
	watches   bool
	watched   map[string]mail.Account
	watch     map[string]mail.WatchStatus
	watchMu   sync.Mutex
	watchOps  []func()
	watchTurn sync.Mutex
	// inboxes is the inbox's unread count of each account, by its ID, and
	// foldersOf the account whose folders a.folders are: after a switch
	// they are the last account's until the cache is read.
	inboxes   map[string]int
	foldersOf string
	// announced is, by account and folder, the newest message already
	// dealt with as new mail. aim is what a clicked notification asked
	// to see; clicked is the ID of one clicked before the accounts were
	// read, which is when ready is set.
	announced map[string]noteMark
	aim       *noteAim
	ready     bool
	clicked   string
	// The settings of receiving mail, and the controls bound to them.
	mailSet       mail.Settings
	checkChoice   int
	windowChoice  int
	notifyOn      map[string]*bool
	mailSaveMu    sync.Mutex
	mailSaveSeq   atomic.Uint64
	mailSetFailed bool
	noteDenied    bool

	composes []*composeState
	// draftSyncs is the draft sync of each account, by its ID. draftErr
	// is why the draft with the key draftErrKey could not be opened.
	draftSyncs  map[string]*draftSync
	draftErr    string
	draftErrKey string

	// Hooks the window sets; tests leave them nil.
	newWindow func(mygo.WindowOptions) *mygo.Window
	saveFile  func(parent *mygo.Window, name string) (string, error)
	// pickFiles asks which files to attach. It blocks, so jobs call it.
	// While it is nil the attach button opens nothing; dropping files
	// still attaches them.
	pickFiles func(parent *mygo.Window) ([]string, error)
	setLocale func(string)
	setBadge  func(int)
	after     func(time.Duration, func()) *time.Timer
	// confirmDiscard asks whether to delete that many drafts for good.
	// It blocks, so jobs call it. While it is nil nothing is discarded.
	confirmDiscard func(parent *mygo.Window, count int) bool
	// notify shows a system notification. It may block, so jobs call it.
	// While it is nil nothing is notified; the counts still follow.
	notify func(note) error
	// focused reports whether the main window has the focus, and
	// showWindow brings it to the front.
	focused    func() bool
	showWindow func()
	lang       int
}

// newApp makes the state the tests and main use.
func newApp(svc mailService) *app {
	return &app{
		svc:           svc,
		run:           func(f func()) { go f() },
		selected:      -1,
		focusList:     true,
		sidebarOpen:   true,
		mailboxesOpen: true,
		bodies:        map[string]mail.FetchResult{},
		older:         map[string]*olderMail{},
		summaries:     map[string]*aiJob{},
		draftSyncs:    map[string]*draftSync{},
		watches:       true,
		watched:       map[string]mail.Account{},
		watch:         map[string]mail.WatchStatus{},
		inboxes:       map[string]int{},
		announced:     map[string]noteMark{},
		mailSet:       mail.DefaultSettings(),
		convToggle:    mail.DefaultSettings().Conversations,
		checkChoice:   1,
		windowChoice:  windowIndex(mail.DefaultSyncWindow),
		notifyOn:      map[string]*bool{},
	}
}

// newTestApp runs every job and update inline, so tests never sleep or
// race. It watches no account: a watcher reports from a goroutine of its
// own, which the tests that are about it ask for.
func newTestApp(svc mailService) *app {
	a := newApp(svc)
	a.run = func(f func()) { f() }
	a.watches = false
	return a
}

// ctx is the timeout of one network or cache job.
func (a *app) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// post publishes a result on the UI thread, or right away in tests.
func (a *app) post(f func()) {
	if a.closed {
		return
	}
	if a.update != nil {
		a.update(f)
		return
	}
	f()
}

func (a *app) stale(g uint64) bool { return a.closed || g != a.gen }

// attach connects the window, its update channel and its hooks.
func (a *app) attach(win *mygo.Window) {
	a.win = win
	a.update = win.Update
	a.newWindow = func(opts mygo.WindowOptions) *mygo.Window { return mygo.NewWindow(opts) }
	a.saveFile = func(parent *mygo.Window, name string) (string, error) {
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: parent, DefaultPath: name})
	}
	a.pickFiles = func(parent *mygo.Window) ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: parent, Title: locales.AttachFiles(), ButtonLabel: locales.Attach(), Multiple: true,
		})
	}
	a.setLocale = mygo.App.SetLocale
	a.setBadge = mygo.App.SetBadgeCount
	a.after = time.AfterFunc
	a.confirmDiscard = askDiscard
	if mygo.NotificationsSupported() {
		a.notify = func(n note) error {
			return mygo.NewNotification(mygo.NotificationOptions{
				ID: n.ID, Title: n.Title, Subtitle: n.Subtitle, Body: n.Body, Group: n.Group,
			}).Show()
		}
	}
	a.focused = win.IsFocused
	a.showWindow = func() {
		win.Show()
		win.Focus()
		mygo.App.Focus()
	}
	// Drafts written offline go up when the network is back, without
	// waiting for the next edit or sync. The watchers stop trying while
	// there is no network, and try at once when there is one again.
	mygo.Network.OnChanged(func(status mygo.NetworkStatus) {
		a.post(func() {
			a.setOnline(status.Online)
			if status.Online {
				a.networkBack()
			}
		})
	})
	a.run(func() {
		status := mygo.Network.Status()
		a.post(func() { a.setOnline(status.Online) })
	})
	// A connection that was idle while the computer slept is dead, and
	// mail that came while the app was in the back is wanted now.
	mygo.Power.OnResume(func() { a.post(a.nudge) })
	mygo.App.OnDidBecomeActive(func() { a.post(a.nudge) })
	win.OnClosed(func() { a.closed = true })
	mygo.App.OnLocaleChanged(func(string) { win.SetTitle(locales.AppName()) })
}

// start reads the accounts before the first sync.
func (a *app) start() {
	if a.started {
		return
	}
	a.started = true
	g := a.gen
	// The files of drafts that were sent or discarded without the time to
	// collect them, as when the app quit meanwhile, go now. It is best
	// effort: what stays is collected the next time.
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.SweepDraftFiles(ctx)
	})
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		accounts, active, err := a.svc.Accounts(ctx)
		// The settings come with the accounts: the first report of a
		// watcher, which may hold a night's mail, finds them read.
		set, setErr := a.svc.Settings(ctx)
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.accounts = accounts
			if err != nil {
				a.err = err.Error()
			}
			if setErr == nil {
				a.setMailSettings(set)
			}
			a.ready = true
			if len(accounts) == 0 {
				a.openAccountForm(nil)
				return
			}
			a.active = active
			if a.account(a.active) == nil {
				a.active = accounts[0].ID
			}
			a.accountsChanged()
			a.beginAccount()
			if id := a.clicked; id != "" {
				a.clicked = ""
				a.openNotification(id)
			}
		})
	})
}

func (a *app) account(id string) *mail.Account {
	for i := range a.accounts {
		if a.accounts[i].ID == id {
			return &a.accounts[i]
		}
	}
	return nil
}

func (a *app) activeAccount() *mail.Account { return a.account(a.active) }

// beginAccount loads one account's cache, then syncs it in the background.
func (a *app) beginAccount() {
	a.gen++
	a.folder = ""
	a.query = ""
	a.selected = -1
	a.setUndo(nil)
	a.list = ui.ListState{}
	a.sel.Clear()
	a.bodies = map[string]mail.FetchResult{}
	a.clearAI()
	a.clearRows()
	a.loadCache(a.gen)
}

// clearAI drops the AI results of the account on screen, cancelling the
// requests still running: they describe mail the window no longer shows.
func (a *app) clearAI() {
	for _, job := range a.summaries {
		if job.cancel != nil {
			job.cancel()
		}
	}
	a.summaries = map[string]*aiJob{}
	a.clearTriage()
}

// loadCache shows cached folders and messages at once.
func (a *app) loadCache(g uint64) {
	accountID := a.active
	a.loading = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, accountID)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err == nil {
				a.setFolders(folders)
				a.ensureFolder()
				a.aimFolder()
				a.updateBadge()
			}
			a.loadMessages(g)
			a.syncAccount(g)
		})
	})
}

// syncAccount refreshes the folder list and then the selected folder.
func (a *app) syncAccount(g uint64) {
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.SyncAccount(ctx, accountID)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.SyncFailed(err.Error())
			} else {
				a.err = ""
				if folders != nil {
					a.setFolders(folders)
				}
				a.ensureFolder()
				a.updateBadge()
			}
			a.loadMessages(g)
			a.syncFolder(g)
			a.syncSent(g)
			a.loadPending(g)
			// The drafts sync with the account, whatever folder shows:
			// what was written offline goes up, and what other mail
			// clients changed comes in. The Drafts folder asks for it
			// itself, just above.
			if a.folder != draftsFolder {
				a.syncDrafts(accountID)
			}
		})
	})
}

// syncFolder refreshes the selected folder and its unread counts. Synced
// for the first time, a folder keeps the settings' window of its newest
// mail.
func (a *app) syncFolder(g uint64) {
	accountID, folder := a.active, a.folder
	if folder == "" || folder == "starred" {
		return
	}
	if folder == draftsFolder {
		a.syncDrafts(accountID)
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SyncFolder(ctx, accountID, folder, 0)
		var folders []mail.Folder
		if err == nil {
			folders, _ = a.svc.CachedFolders(ctx, accountID)
		}
		a.post(func() {
			if a.stale(g) || a.folder != folder {
				return
			}
			if err != nil {
				a.err = locales.SyncFailed(err.Error())
			} else {
				a.err = ""
				if folders != nil {
					a.setFolders(folders)
				}
				a.updateBadge()
			}
			if a.aim != nil {
				// The read that follows is the last chance of the
				// message a notification asked for.
				a.aim.last = true
			}
			a.loadMessages(g)
		})
	})
}

// syncSent caches the Sent folder with the account in conversation view,
// so that the replies the user sent, from here or elsewhere, join their
// conversations. The folder on show is synced by syncFolder.
func (a *app) syncSent(g uint64) {
	sent := a.folderBySpecial(mail.SpecialSent)
	if !a.mailSet.Conversations || sent == "" || sent == a.folder {
		return
	}
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SyncFolder(ctx, accountID, sent, 0)
		a.post(func() {
			if err == nil && !a.stale(g) && a.threaded() {
				a.readList(g, false, false)
			}
		})
	})
}

// loadMessages reads the current folder and query from the cache.
func (a *app) loadMessages(g uint64) { a.readMessages(g, true) }

// readMessages reads the list, showing progress or, for a read the user
// did not ask for, none.
func (a *app) readMessages(g uint64, progress bool) { a.readList(g, progress, true) }

// readList reads the list from the top. A list read again reads as many
// rows as it shows, and on to the last of them, so that no row read
// leaves it while mail that came pushes them down. Unless neighbour is
// set, a chosen row that left the list leaves nothing chosen: new mail
// must not open, and so mark read, the message beside it.
func (a *app) readList(g uint64, progress, neighbour bool) {
	q := a.listQuery()
	accountID, folder, query, threaded := q.account, q.folder, q.search, q.threaded
	key, prev := a.selectedKey(), a.chosenMessage()
	var aimAt mail.Summary
	if aim := a.aim; threaded && aim != nil && aim.account == accountID && aim.folder == folder {
		aimAt = mail.Summary{Account: aim.account, Folder: aim.folder, UID: aim.uid}
	}
	if folder == "" {
		a.all, a.convs = nil, nil
		a.loading = false
		return
	}
	p := &a.pager
	if progress {
		a.loading, p.spin = true, true
	}
	want := listPage
	var first, last *rowMark
	again := p.query == q && len(a.all) > 0
	if again {
		want = max(want, len(a.all))
		f, l := a.mark(0), a.mark(len(a.all)-1)
		first, last = &f, &l
	}
	p.reads++
	seq, rows := p.reads, p.rows
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			got listRows
			err error
			// In conversation view, found is the conversation the chosen
			// message is in now when its own left the list, as one that
			// merged into another does, and aimed the conversation of the
			// message a notification asked to see.
			found, aimed mail.ThreadID
			cache        mail.FolderCache
			cacheErr     error
		)
		switch {
		case folder == draftsFolder:
			got.msgs, err = a.svc.DraftList(ctx, accountID, query, 50)
		default:
			got, err = a.readTop(ctx, q, want, first, last)
			if err == nil && threaded && prev.UID != 0 && !hasThread(got.convs, convRef{prev.Account, prev.Thread}) {
				found = a.threadOf(ctx, prev)
			}
			if err == nil && threaded && aimAt.UID != 0 {
				aimed = a.threadOf(ctx, aimAt)
			}
		}
		if q.isFolder() {
			cache, cacheErr = a.svc.FolderCache(ctx, accountID, folder)
		}
		a.post(func() {
			// Only the latest read of the list shows.
			if a.stale(g) || a.listQuery() != q || p.reads != seq {
				return
			}
			if again && p.rows != rows {
				// A page was added meanwhile, which this read does not
				// reach.
				a.readList(g, progress, neighbour)
				return
			}
			if p.spin {
				a.loading, p.spin = false, false
			}
			if !progress {
				if chosen := a.selectedKey(); chosen != key {
					// Nobody waited for this read, so the row chosen since
					// it began is the one to keep.
					key, prev, found = chosen, a.chosenMessage(), 0
				}
			}
			if err != nil {
				a.err = err.Error()
				return
			}
			if q.isFolder() && cacheErr == nil {
				o := a.olderOf(accountID, folder)
				o.cache, o.known = cache, true
			}
			was := a.selected
			a.setRows(q, got)
			a.pruneSelection()
			a.restoreSelection(key)
			if a.selected < 0 {
				a.findChosen(prev, found)
			}
			if i := a.aimRow(aimed); i >= 0 {
				a.chooseRow(i)
			}
			switch {
			case progress && a.selected < 0 && len(a.all) > 0 && query == "" && a.sel.Len() == 0 && a.aim == nil:
				// Opening a folder shows its first message. A read made
				// behind the user's back leaves an empty choice empty,
				// and so does one that waits for the message a
				// notification asked for.
				a.chooseRow(0)
			case !neighbour && a.selected < 0:
			case !progress && a.selected < 0 && key != "" && len(a.all) > 0 && a.sel.Len() == 0:
				// The chosen row left the list, as a draft sent or
				// replaced by another mail client does. The row that
				// took its place is chosen, as after archiving.
				a.chooseRow(min(was, len(a.all)-1))
			case a.selected >= 0 && a.sel.Len() == 0:
				a.sel.Add(a.rowKey(a.selected))
			}
			a.reloadBody()
		})
	})
}

// pruneSelection forgets rows the list no longer shows, so the count and
// actions match what the user sees.
func (a *app) pruneSelection() {
	if a.sel.Len() == 0 {
		return
	}
	present := make(map[string]bool, len(a.all))
	for i := range a.all {
		present[a.rowKey(i)] = true
	}
	for k := range a.sel.All() {
		if !present[k] {
			a.sel.Remove(k)
		}
	}
}

// selectedKey is the identity of the chosen row, or "".
func (a *app) selectedKey() string {
	if a.selected < 0 || a.selected >= len(a.all) {
		return ""
	}
	return a.rowKey(a.selected)
}

// chosenMessage is the message the chosen row shows, in conversation view
// its conversation's newest, or none.
func (a *app) chosenMessage() mail.Summary {
	if a.selected < 0 || a.selected >= len(a.all) {
		return mail.Summary{}
	}
	return a.all[a.selected]
}

// chooseRow makes row i the one chosen, and the only one, so the list
// shows it highlighted.
func (a *app) chooseRow(i int) {
	if i < 0 || i >= len(a.all) {
		return
	}
	a.selected = i
	a.details = false
	a.sel.Clear()
	a.sel.Add(a.rowKey(i))
}

// clearSelection drops the choice and the reading pane with it.
func (a *app) clearSelection() {
	a.sel.Clear()
	a.selected = -1
	a.details = false
	a.reloadBody()
}

// restoreSelection finds the chosen row again by its key.
func (a *app) restoreSelection(key string) {
	a.selected = -1
	if key == "" {
		return
	}
	for i := range a.all {
		if a.rowKey(i) == key {
			a.selected = i
			return
		}
	}
}

// summaryKey is the identity of a message: of its row, its body and its
// summary. Two accounts can both have a message 1 in their INBOX, so the
// account is part of it.
func summaryKey(m mail.Summary) string {
	if m.DraftID != "" {
		// A draft of this device has no folder or UID to tell it by.
		return "draft/" + m.DraftID
	}
	return messageKey(m.Account, m.Folder, m.UID)
}

// messageKey is summaryKey of an account's message. The account comes
// escaped, without a slash, as a folder's name may have one.
func messageKey(account, folder string, uid mail.UID) string {
	return url.PathEscape(account) + "/" + folder + "/" + strconv.FormatUint(uint64(uid), 10)
}

// reloadBody shows the body of the chosen row, if any, or in conversation
// view its conversation.
func (a *app) reloadBody() {
	if a.threaded() {
		a.loadConversation(a.gen)
		return
	}
	if a.selected < 0 || a.selected >= len(a.all) {
		a.bodyLocked = false
		a.bodyErr = ""
		return
	}
	a.loadBody(a.gen)
}

// loadBody reads a cached body, else fetches it, then marks the message
// read.
func (a *app) loadBody(g uint64) {
	idx := a.selected
	if idx < 0 || idx >= len(a.all) {
		return
	}
	m := a.all[idx]
	if m.DraftID != "" {
		a.loadDraftBody(g, m)
		return
	}
	key := summaryKey(m)
	accountID, folder, uid := m.Account, m.Folder, m.UID
	if result, ok := a.bodies[key]; ok {
		a.bodyLocked = false
		a.bodyErr = ""
		if !result.Message.Seen {
			result.Message.Seen = true
			a.bodies[key] = result
			a.applySeen(key)
			a.markSeen(accountID, folder, uid)
		}
		a.syncHTMLView()
		return
	}
	a.bodyLocked = true
	a.bodyErr = ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		result, cached, err := a.svc.CachedMessage(ctx, accountID, folder, uid)
		if err == nil && !cached {
			result, err = a.svc.FetchMessage(ctx, accountID, folder, uid, true)
		}
		a.post(func() {
			if a.stale(g) || a.selectedKey() != key {
				return
			}
			a.bodyLocked = false
			if err != nil {
				a.bodyErr = locales.BodyFailed(err.Error())
				return
			}
			if !result.Message.Seen {
				result.Message.Seen = true
				a.markSeen(accountID, folder, uid)
			}
			a.bodies[key] = result
			a.applySeen(key)
			a.syncHTMLView()
		})
	})
}

// markSeen asks the server to mark a message read; the UI already shows
// it.
func (a *app) markSeen(accountID, folder string, uid mail.UID) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.SetFlag(ctx, accountID, folder, uid, mail.FlagSeen, true)
	})
}

// applySeen marks the row read and lowers the folder badge in step.
func (a *app) applySeen(key string) {
	for i := range a.all {
		if summaryKey(a.all[i]) != key || a.all[i].Seen {
			continue
		}
		a.all[i].Seen = true
		a.addUnread(a.all[i].Account, a.all[i].Folder, -1)
	}
	a.updateBadge()
}

// updateBadge puts the unread count of every account's inbox on the
// Dock. The active account's is the one its folders show, which follows
// what the user reads before the server does.
func (a *app) updateBadge() {
	if a.active != "" && a.foldersOf == a.active {
		a.inboxes[a.active] = inboxUnread(a.folders)
	}
	if a.setBadge == nil {
		return
	}
	n := 0
	for _, acc := range a.accounts {
		n += a.inboxes[acc.ID]
	}
	a.setBadge(n)
}

// setUndo offers an undo, or clears it, and retires the offer after a
// while so a stale banner does not linger.
func (a *app) setUndo(u *undoMove) {
	if a.undoTimer != nil {
		a.undoTimer.Stop()
		a.undoTimer = nil
	}
	a.undo = u
	if u == nil || a.after == nil {
		return
	}
	a.undoTimer = a.after(10*time.Second, func() {
		a.post(func() {
			if a.undo == u {
				a.undo = nil
			}
		})
	})
}

// loadPending reads the outbox's leftovers for the delivery banner.
func (a *app) loadPending(g uint64) {
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		pending, err := a.svc.PendingSends(ctx, accountID)
		a.post(func() {
			if a.stale(g) || a.active != accountID {
				return
			}
			if err == nil {
				a.pending = pending
			}
		})
	})
}

// search re-reads the list with the search field's text. Other rows show
// from their top.
func (a *app) search() {
	a.sel.Clear()
	a.list.ScrollTo(0, ui.Start)
	a.loadMessages(a.gen)
}

// selectFolder switches the list to a folder and syncs it.
func (a *app) selectFolder() {
	a.gen++
	a.selected = -1
	a.setUndo(nil)
	a.list = ui.ListState{}
	a.sel.Clear()
	a.clearTriage()
	a.clearRows()
	a.query = ""
	a.loadMessages(a.gen)
	a.syncFolder(a.gen)
}

// switchAccount makes another account active.
func (a *app) switchAccount(id string) {
	if id == a.active {
		return
	}
	a.active = id
	a.beginAccount()
	accountID := id
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.SetActive(ctx, accountID)
	})
}

// refresh is the Sync button. It syncs the account on screen whatever
// state its watcher is in, and has every watcher check now.
func (a *app) refresh() {
	a.gen++
	a.syncAccount(a.gen)
	a.nudge()
}

// folderEntry is one row of the sidebar.
type folderEntry struct {
	ID    string
	Label string
	Icon  *ui.SVG
}

// folderEntries turns the cached folders into special-use first entries
// and the rest after them.
func (a *app) folderEntries() []folderEntry {
	entries := []folderEntry{{ID: "starred", Label: locales.Starred(), Icon: starIcon}}
	seen := map[string]bool{}
	add := func(special string, icon *ui.SVG, label string) {
		for _, f := range a.folders {
			if f.SpecialUse == special && !seen[f.Name] {
				seen[f.Name] = true
				entries = append(entries, folderEntry{ID: f.Name, Label: label, Icon: icon})
				return
			}
		}
	}
	add(mail.SpecialInbox, inboxIcon, locales.Inbox())
	add(mail.SpecialSent, sentIcon, locales.Sent())
	// Drafts always shows: it holds the drafts on this device, whether or
	// not the server has a Drafts folder. The server's folder is listed
	// inside it, so it gets no entry of its own.
	entries = append(entries, folderEntry{ID: draftsFolder, Label: locales.Drafts(), Icon: draftIcon})
	seen[a.folderBySpecial(mail.SpecialDrafts)] = true
	add(mail.SpecialArchive, archiveIcon, locales.Archive())
	add(mail.SpecialJunk, spamIcon, locales.Junk())
	add(mail.SpecialTrash, trashIcon, locales.Trash())
	for _, f := range a.folders {
		// A folder named as an entry above would be chosen with it.
		if seen[f.Name] || f.SpecialUse == mail.SpecialFlagged || f.Name == draftsFolder {
			continue
		}
		seen[f.Name] = true
		entries = append(entries, folderEntry{ID: f.Name, Label: f.Name, Icon: tagIcon})
	}
	return entries
}

func (a *app) ensureFolder() {
	if a.folder != "" && a.folderExists(a.folder) {
		return
	}
	a.folder = ""
	for _, f := range a.folders {
		if f.SpecialUse == mail.SpecialInbox {
			a.folder = f.Name
			return
		}
	}
	if len(a.folders) > 0 {
		a.folder = a.folders[0].Name
	}
}

func (a *app) folderExists(id string) bool {
	if id == "starred" || id == draftsFolder {
		return true
	}
	for _, f := range a.folders {
		if f.Name == id {
			return true
		}
	}
	return false
}

func (a *app) folderBySpecial(special string) string {
	for _, f := range a.folders {
		if f.SpecialUse == special {
			return f.Name
		}
	}
	return ""
}

// specialFolder is an account's folder of a special use, where an action
// on its mail moves it, or "": the window holds the folders of the
// account on show.
func (a *app) specialFolder(account, special string) string {
	if account != a.foldersOf {
		return ""
	}
	return a.folderBySpecial(special)
}

// listScope is the part of the cache the list shows.
func (a *app) listScope() mail.Scope { return mail.AccountScope(a.active, a.folder) }

func (a *app) folderLabel(id string) string {
	if id == "starred" {
		return locales.Starred()
	}
	if id == draftsFolder {
		return locales.Drafts()
	}
	for _, f := range a.folders {
		if f.Name != id {
			continue
		}
		switch f.SpecialUse {
		case mail.SpecialInbox:
			return locales.Inbox()
		case mail.SpecialSent:
			return locales.Sent()
		case mail.SpecialDrafts:
			return locales.Drafts()
		case mail.SpecialArchive:
			return locales.Archive()
		case mail.SpecialJunk:
			return locales.Junk()
		case mail.SpecialTrash:
			return locales.Trash()
		}
		return f.Name
	}
	return id
}

func (a *app) folderUnread(id string) int {
	for _, f := range a.folders {
		if f.Name == id {
			return f.Unread
		}
	}
	return 0
}

// messageTime writes a list timestamp the way mail clients do: a time
// today, "yesterday", a weekday this week, a date this year.
func (a *app) messageTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := c.Now()
	y, m, d := now.Date()
	ty, tm, td := t.Date()
	if y == ty && m == tm && d == td {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Time: i18n.ShortStyle})
	}
	yesterday := now.AddDate(0, 0, -1)
	yy, ym, yd := yesterday.Date()
	if ty == yy && tm == ym && td == yd {
		return i18n.FormatRelativeTime(c.Locale(), -1, i18n.RelativeDay, i18n.RelativeTimeOptions{Auto: true})
	}
	if now.Sub(t) < 7*24*time.Hour && now.Sub(t) > 0 {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "EEE"})
	}
	if y == ty {
		return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "MMMd"})
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Skeleton: "yMMMd"})
}

// longTime is the reading pane's full timestamp.
func (a *app) longTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return i18n.FormatDate(c.Locale(), t, i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
}

// view is the window's root: settings, account setup or the three panes.
func (a *app) view(c *ui.Context) {
	switch {
	case a.showSettings:
		a.settingsView(c)
	case a.showAccounts || len(a.accounts) == 0:
		a.accountsView(c)
	default:
		a.mailView(c)
	}
}

// mailView draws the three-pane window.
func (a *app) mailView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	c.OnShortcut(ui.Cmd, ui.KeyK, func() { a.searchField.Focus(c) })
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.openCompose(nil) })
	c.OnShortcut(ui.Cmd, ui.KeyR, func() { a.refresh() })
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.titleBar(c, t, bar, top)
		panes := ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if a.sidebarOpen {
				a.sidebar(c, t)
				ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			}
			a.messageList(c, t)
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			a.readingPane(c, t)
		})
		// Unmodified shortcuts live in the three panes' focus subtree, so
		// typing in the search field is never taken for a command.
		panes.OnShortcut(0, ui.KeyJ, func() { a.stepMessage(1) })
		panes.OnShortcut(0, ui.KeyK, func() { a.stepMessage(-1) })
		panes.OnShortcut(0, ui.KeyU, func() { a.toggleRead() })
		panes.OnShortcut(0, ui.KeyS, func() { a.toggleStar() })
		panes.OnShortcut(0, ui.KeyE, func() { a.moveSelected(mail.SpecialArchive) })
		panes.OnShortcut(0, ui.KeyR, func() { a.reply(false) })
		panes.OnShortcut(0, ui.KeyF, func() { a.forward() })
		panes.OnShortcut(0, ui.KeyDelete, func() { a.deleteSelected() })
	})
	a.accountModal(c)
	aiReviewModal(c, &a.review)
}

// titleBar draws the search field, sync and compose.
func (a *app) titleBar(c *ui.Context, t *ui.Theme, bar ui.TitleBar, height float32) {
	ui.Row(c).Height(height).Shrink(0).AlignItems(ui.Center).Gap(10).
		Padding(0, max(bar.Right, 0)+14, 0, max(bar.Left, 0)+14).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {
		label := locales.HideSidebar()
		if !a.sidebarOpen {
			label = locales.ShowSidebar()
		}
		a.iconButton(c, panelLeftIcon, label, label).
			OnClick(func() { a.sidebarOpen = !a.sidebarOpen })
		ui.Spacer(c)
		ui.SearchField(c, &a.query).Width(300).
			Placeholder(locales.SearchFolder(a.folderLabel(a.folder))).
			Label(locales.SearchMailLabel()).Bind(&a.searchField).
			OnClick(func() { a.focusList = false }).
			OnChange(func() { a.search() })
		if a.loading {
			ui.Spinner(c).Size(14, 14)
		}
		a.iconButton(c, refreshIcon, locales.Sync(), locales.SyncMail()).
			OnClick(func() { a.refresh() })
		a.iconButton(c, settingsIcon, locales.Settings(), locales.Settings()).
			OnClick(func() { a.openSettings(settingsTabGeneral) })
		ui.PrimaryButton(c, "").Children(func() {
			ui.Icon(c, composeIcon).FontSize(14)
			ui.Text(c, locales.Compose())
		}).Label(locales.Compose()).Tooltip(locales.NewMessage()).
			OnClick(func() { a.openCompose(nil) })
	})
}

// sidebar draws the account switcher and the folders.
func (a *app) sidebar(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(222).Shrink(0).Background(t.Surface).Children(func() {
		ui.Row(c).Padding(10, 12, 4, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			acc := a.activeAccount()
			name, email := "", ""
			if acc != nil {
				name, email = acc.Label(), acc.Email
			}
			if name == "" {
				name = locales.AccountsTitle()
			}
			ui.Avatar(c, name, nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, name).FontSize(12).Bold().SingleLine()
				ui.Text(c, email).FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			ui.PopoverMenuButton(c, "", func(m *ui.Menu) {
				for _, acc := range a.accounts {
					label := acc.Label()
					item := m.Item(label + " · " + acc.Email).ID(acc.ID).Radio(acc.ID == a.active)
					// The sidebar shows the active account's unread mail;
					// the other accounts' is said here.
					if n := a.inboxes[acc.ID]; n > 0 && acc.ID != a.active {
						item.Trailing(fmt.Sprint(n))
					}
					if item.Chosen() {
						a.aim = nil
						a.switchAccount(acc.ID)
					}
				}
				m.Separator()
				if m.Item(locales.AddAccount()).Chosen() {
					a.openAccountForm(nil)
				}
				if m.Item(locales.ManageAccounts()).Chosen() {
					if acc := a.activeAccount(); acc != nil {
						a.openAccountForm(acc)
					} else {
						a.openAccountForm(nil)
					}
				}
				if m.Item(locales.Settings()).Chosen() {
					a.openSettings(settingsTabGeneral)
				}
			}).Label(locales.AccountsMenu()).Tooltip(locales.AccountsMenu())
		})

		entries := a.folderEntries()
		changed := ui.Sidebar(c, &a.folder, func() {
			ui.SidebarSection(c, locales.Mailboxes(), &a.mailboxesOpen, func() {
				for _, e := range entries {
					item := ui.SidebarItem(c, e.ID, e.Icon, e.Label)
					if e.ID != "starred" && e.ID != draftsFolder {
						if n := a.folderUnread(e.ID); n > 0 {
							item.Children(func() {
								ui.Badge(c, fmt.Sprint(n)).Background(t.Accent).TextColor(t.AccentText)
							})
						}
					}
				}
			})
		}).Grow(1).Label(locales.Mailboxes())
		if changed.Changed() {
			a.aim = nil
			a.selectFolder()
		}
		a.sidebarFooter(c, t)
	})
}

// sidebarFooter shows the account's cached totals and whether a sync is
// running, as mail apps keep their quota at the bottom, and under them
// how receiving mail in the background is going.
func (a *app) sidebarFooter(c *ui.Context, t *ui.Theme) {
	unread, total := a.sidebarTotals()
	ui.Column(c).Shrink(0).Padding(10, 12, 10, 12).Gap(4).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.loading {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, locales.Syncing()).FontSize(11).TextColor(t.TextMuted)
			})
		} else {
			ui.Text(c, locales.SidebarSummary(unread, total)).FontSize(11).TextColor(t.TextMuted)
		}
		a.watchLine(c, t)
	})
}

// sidebarTotals counts the account's cached folders, without the ones
// whose mail is already dealt with.
func (a *app) sidebarTotals() (unread, total int) {
	for _, f := range a.folders {
		switch f.SpecialUse {
		case mail.SpecialJunk, mail.SpecialTrash, mail.SpecialDrafts:
			continue
		}
		unread += f.Unread
		total += f.Total
	}
	return unread, total
}

// iconButton is an icon-only button named for assistive technology.
func (a *app) iconButton(c *ui.Context, svg *ui.SVG, label, tip string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(tip).Padding(6, 8).
		Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolButton is a flat toolbar button showing only an icon. Its padding
// is the icon buttons', which lets the whole toolbar fit the reading pane
// at the window's minimum width.
func (a *app) toolButton(c *ui.Context, svg *ui.SVG, label string, on bool, fn func()) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(label).Disabled(!on).Padding(6, 8)
	if on && fn != nil {
		b = b.OnClick(fn)
	}
	return b.Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolDivider separates groups of toolbar buttons.
func (a *app) toolDivider(c *ui.Context) {
	ui.Box(c).Width(1).Height(18).Shrink(0).Margin(0, 4).Background(c.Theme().Border)
}

// keychain adapts mygo.Secrets to the engine's SecretStore.
type keychain struct{}

func (keychain) Set(key string, secret []byte) error { return mygo.Secrets.Set(key, secret) }
func (keychain) Get(key string) ([]byte, error) {
	data, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, mail.ErrNotFound
	}
	return data, err
}
func (keychain) Delete(key string) error { return mygo.Secrets.Delete(key) }

// noteClicks holds the notification clicks that come before the window
// can take them: the click that launched the app arrives as it starts.
type noteClicks struct {
	mu      sync.Mutex
	waiting []string
	open    func(id string)
}

// add takes a click, for the window if it is there and for later if not.
func (q *noteClicks) add(id string) {
	q.mu.Lock()
	open := q.open
	if open == nil {
		q.waiting = append(q.waiting, id)
	}
	q.mu.Unlock()
	if open != nil {
		open(id)
	}
}

// deliver hands the clicks so far to open, and every later one as it
// comes.
func (q *noteClicks) deliver(open func(id string)) {
	q.mu.Lock()
	waiting := q.waiting
	q.waiting, q.open = nil, open
	q.mu.Unlock()
	for _, id := range waiting {
		open(id)
	}
}

func main() {
	// Registered before Run, as the click on a notification that launched
	// the app is delivered while it starts.
	var clicks noteClicks
	mygo.App.OnNotificationClick(clicks.add)
	mygo.App.WhenReady(func() {
		dataDir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		cacheDir, err := mygo.App.Path(mygo.PathCache)
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sqlite.Load(); err != nil {
			log.Fatalf("sqlite: %v (run go tool mygo generate)", err)
		}
		db, err := store.Open(ctx, filepath.Join(dataDir, "shu.db"))
		if err != nil {
			log.Fatal(err)
		}
		svc := mail.NewService(db, keychain{}, cacheDir)
		// What a draft has attached is the user's data, not a cache that
		// the system may clear: it is kept beside the database.
		svc.SetDataDir(dataDir)
		a := newApp(svc)
		a.ai = ai.NewService(db, aiKeychain{})
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           locales.AppName(),
			Width:           1220,
			Height:          800,
			MinWidth:        1000,
			MinHeight:       640,
			StateKey:        "main",
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			BackgroundColor: "light-dark(#ffffff, #18181b)",
			Content:         ui.View(a.view),
		})
		a.attach(win)
		chosen := mygo.App.ChosenLocale()
		switch {
		case chosen == "en":
			a.lang = 1
		case chosen == "zh-Hans":
			a.lang = 2
		}
		mygo.App.OnQuit(func() {
			// The watchers end before the database they write to closes.
			svc.StopWatching()
			db.Close()
		})
		clicks.deliver(func(id string) {
			a.post(func() { a.openNotification(id) })
		})
		a.start()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

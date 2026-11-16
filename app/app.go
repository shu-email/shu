// Package app is Shu's window: its state, the views that draw it and
// the service values they read.
package app

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// app is the state the windows show.
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
	// queryMemo is the last read of query (search.go), shared by the parts
	// of a build that ask what the search text means.
	queryMemo parseMemo

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
	// searchOpen shows the search field where the reading pane is too
	// narrow for it beside the actions and offers it as a button
	// (readingBar), and searchFocus gives it the keys as it is next built;
	// readingWidth is the reading pane's last width.
	searchOpen, searchFocus bool
	// searchTimer holds the search the last keystroke in the search field
	// armed; a search asked for another way retires it.
	searchTimer  searchDebounce
	readingWidth float32
	focusList    bool
	// scope is the scope chosen for the list's searches, when scopeSet;
	// scopeSeg is the scope control's choice as it draws it. filters is
	// the filter control beside the search field.
	scope    searchScope
	scopeSet bool
	scopeSeg int
	filters  filterForm
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
	undo             *undoAction
	undoTimer        *time.Timer
	// reminders are the active account's, the soonest due first, and
	// remindForm the dialog of a time typed (reminders.go). reminderLoop
	// says the due check is waiting to run again.
	reminders    []mail.Reminder
	remindForm   reminderForm
	reminderLoop bool
	// followUps is how many of the active account's conversations wait for
	// a reply (followup.go).
	followUps int

	showAccounts  bool
	showSettings  bool
	accountOpen   bool
	sidebarOpen   bool
	mailboxesOpen bool
	// palette is the shell's one field and what it lists (palette.go),
	// which the preview window draws where the sidebar is.
	palette paletteState
	details bool
	// securityOpen is, by message key, whether its sender-authentication
	// details are shown; linkOpen and linkURL hold a link the HTML view
	// asked to open until the user confirms where it goes (security.go).
	securityOpen map[string]bool
	linkOpen     bool
	linkURL      string
	linkInfo     linkReport
	accountForm  accountForm
	accountBusy  bool
	accountErr   string
	accountNote  string

	// Signing in with Google or Microsoft (signin.go). signIn is the
	// sign-in under way, nil when none, signInErr how the last one failed,
	// and signInName the name an account added so is to go by. needSignIn
	// is the accounts that have to sign in again before they reach their
	// servers, and signOutOpen the question before an account signs out.
	signIn      *signInFlow
	signInErr   *signInFailure
	signInName  string
	needSignIn  map[string]bool
	signOutOpen bool

	// counts is the last value each count badge showed (countBadge), and
	// rows what the list's rows did as they last showed (rowMotion).
	counts map[string]countSeen
	rows   rowMotion

	htmlOn  bool
	webview *mygo.WebView
	htmlKey string
	// translationHTMLKey names the message of a conversation whose
	// translated HTML holds the window's one web view.
	translationHTMLKey string
	services           ui.Services

	// AI state: the settings form, the summaries and the translations of
	// this account's messages by key, the triage of the list on screen,
	// and the payload being inspected.
	settingsTab  int
	aiForm       aiSettings
	aiSaveMu     sync.Mutex
	aiSaveSeq    atomic.Uint64
	summaries    map[string]*aiJob
	translations map[string]*translation
	triage       *triageState
	review       *aiReview

	// Receiving mail in the background. watches is whether the accounts
	// are watched at all; watched is each watched account as it was when
	// its watcher was asked for, and watch what that watcher last said.
	// watchOps are the calls that start and stop watchers, in order.
	watches bool
	watched map[string]mail.Account
	watch   map[string]mail.WatchStatus
	queue   map[string]mail.QueueStatus
	// syncs is what each account's syncs last said (mail.SyncEvent): by
	// account ID and then by run, the line above the list while one
	// runs, and why one failed until that sync starts again. syncSeq
	// orders those runs. syncPostMu guards syncPosted, which remembers a
	// background check or an older-mail fetch whose failure the notice
	// shows, so that its end, which changes nothing, is not posted.
	syncs      map[string]map[string]syncStatus
	syncSeq    uint64
	syncPostMu sync.Mutex
	syncPosted map[string]bool
	watchMu    sync.Mutex
	watchOps   []func()
	watchTurn  sync.Mutex
	// inboxes is the inbox's unread count of each account, by its ID, and
	// foldersOf the account whose folders a.folders are: after a switch
	// they are the last account's until the cache is read. otherFolders
	// are the other accounts' folders, by ID: where an action on a row of
	// theirs moves it, and what their counts are.
	inboxes      map[string]int
	foldersOf    string
	otherFolders map[string][]mail.Folder
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

	// How messages are written (sending.go, sendfrom.go). composeSet is
	// the compose settings, as stored and as the settings change them,
	// composeChoice where their controls stand, and composeSetFailed that
	// the last save failed. signatures and aliases are each account's
	// signature and the addresses added to it, by account ID, as last
	// read or saved: what compose writes with.
	composeSet       mail.ComposeSettings
	composeChoice    composeChoices
	composeSaveMu    sync.Mutex
	composeSaveSeq   atomic.Uint64
	composeSetFailed bool
	signatures       map[string]mail.Signature
	aliases          map[string][]mail.Identity
	// waiting are the messages sent with a delay that have not left,
	// which the mail window counts down with Undo, and sendNote is news
	// of sending the window did not see happen, a warning or not.
	waiting      []*waitingSend
	sendNote     string
	sendNoteWarn bool
	// copied is the row whose value was just put on the clipboard, and
	// copiedTimer clears it: a copy made from the list says so where it
	// happened (stream.go).
	copied      string
	copiedTimer *time.Timer
	// The account editor's signature, saved as it changes, and the form
	// of an address the account sends from.
	sigForm    signatureForm
	sigSaveMu  sync.Mutex
	sigSaveSeq atomic.Uint64
	idForm     identityForm

	// book is the address book's view, open or not (contacts.go).
	book addressBook
	// labels are each account's labels and the views that change them
	// (labels.go), and smarts the account on screen's saved searches
	// (smart.go).
	labels labelState
	smarts smartState
	// folderNote is the news of an action on a folder, such as a folder
	// of the server that a saved attachment was put in (folders.go).
	folderNote string
	// networkForm is the Network tab's editable state (network.go).
	networkForm networkForm
	// providers is the AI providers panel (providers.go).
	providers providersPanel
	// transfer is the import, export or backup running or just done
	// (transfer.go).
	transfer transferState
	// shortcutsOpen shows the keyboard reference (shortcuts.go).
	shortcutsOpen bool
	// settingsWin is the settings window, when it is a window of its own;
	// nil in tests, where the settings show in the main view.
	settingsWin *mygo.Window
	// contactsWin and rulesWin are the address book's and the rules'
	// windows, nil in tests, where they show in the main view.
	contactsWin *mygo.Window
	rulesWin    *mygo.Window
	// inviteResponses, inviteBusy and inviteNote hold the user's RSVP
	// answers by event UID, the send in flight and its result
	// (calendar.go).
	inviteResponses map[string]string
	inviteBusy      bool
	inviteNote      string
	// Remote images and attachment previews (images.go): images is the
	// senders whose pictures are allowed, imagesShown the messages the
	// user chose to show them in, and preview the open preview.
	images       map[string]bool
	imagesShown  map[string]bool
	imagesLoaded bool
	preview      attachmentPreview
	previewView  *mygo.WebView
	// Sender icons (avatars.go): icons holds the domains whose picture the
	// window has, iconPending those it asked for and iconGone those it has
	// no picture for and asks no more in this run.
	icons       map[string]*ui.Bitmap
	iconPending map[string]bool
	iconGone    map[string]bool
	// rules is the rules' view, open or not (rules.go); the live checks
	// of the rule being written take turns under ruleCheckMu, and one a
	// later check overtook (ruleCheckSeq) does not run.
	rules        ruleState
	ruleCheckMu  sync.Mutex
	ruleCheckSeq atomic.Uint64
	// Leaving a mailing list and blocking a sender (block.go): lists is
	// what this device recorded of leaving each sender's list, by account
	// and sender, and unsubForm the question before one is left. blocks
	// is the Privacy tab's list of blocked senders, and blockForm the
	// question before one is blocked.
	lists     map[string]mail.UnsubscribeRecord
	unsubForm unsubForm
	blocks    blockState
	// cache is the settings' cache check (integrity.go): the run in flight
	// and what the last one found.
	cache cacheCheck

	// Hooks the window sets; tests leave them nil.
	newWindow func(mygo.WindowOptions) *mygo.Window
	saveFile  func(parent *mygo.Window, name string) (string, error)
	// pickFiles asks which files to attach. It blocks, so jobs call it.
	// While it is nil the attach button opens nothing; dropping files
	// still attaches them.
	pickFiles func(parent *mygo.Window) ([]string, error)
	// pickFolder asks where to save several attachments. It blocks, so
	// jobs call it.
	pickFolder func(parent *mygo.Window) (string, error)
	setLocale  func(string)
	setBadge   func(int)
	after      func(time.Duration, func()) *time.Timer
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
	// openBrowser opens a page in the system browser, as a sign-in does.
	// It may wait for the system, so jobs call it. While it is nil
	// nothing opens.
	openBrowser func(url string) error
	lang        int
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
		translations:  map[string]*translation{},
		counts:        map[string]countSeen{},
		draftSyncs:    map[string]*draftSync{},
		watches:       true,
		watched:       map[string]mail.Account{},
		watch:         map[string]mail.WatchStatus{},
		queue:         map[string]mail.QueueStatus{},
		syncs:         map[string]map[string]syncStatus{},
		syncPosted:    map[string]bool{},
		needSignIn:    map[string]bool{},
		inboxes:       map[string]int{},
		otherFolders:  map[string][]mail.Folder{},
		announced:     map[string]noteMark{},
		mailSet:       mail.DefaultSettings(),
		convToggle:    mail.DefaultSettings().Conversations,
		checkChoice:   1,
		windowChoice:  windowIndex(mail.DefaultSyncWindow),
		notifyOn:      map[string]*bool{},
		composeSet:    mail.DefaultComposeSettings(),
		composeChoice: choicesOf(mail.DefaultComposeSettings()),
		signatures:    map[string]mail.Signature{},
		aliases:       map[string][]mail.Identity{},
		labels:        newLabelState(),
		smarts:        newSmartState(),
		images:        map[string]bool{},
		imagesShown:   map[string]bool{},
		icons:         map[string]*ui.Bitmap{},
		iconPending:   map[string]bool{},
		iconGone:      map[string]bool{},
		securityOpen:  map[string]bool{},
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
	run := func() {
		f()
		// The settings, the address book and the rules are windows of
		// their own: a result that arrives after their frame must redraw
		// them, not the mail window.
		for _, w := range []*mygo.Window{a.settingsWin, a.contactsWin, a.rulesWin} {
			if w != nil {
				w.Invalidate()
			}
		}
	}
	if a.update != nil {
		a.update(run)
		return
	}
	run()
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
	a.pickFolder = func(parent *mygo.Window) (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: parent, Title: locales.SaveAllAttachments(), ButtonLabel: locales.Save(),
			Directory: true, CreateDirectories: true,
		})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
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
	a.openBrowser = mygo.Shell.OpenExternal
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
	win.OnClosed(func() {
		// A sign-in waiting for the browser has nowhere left to end.
		a.cancelSignIn()
		a.searchTimer.stop()
		a.closed = true
	})
	mygo.App.OnLocaleChanged(func(string) { win.SetTitle(locales.AppName()) })
}

// start reads the accounts before the first sync.
func (a *app) start() {
	if a.started {
		return
	}
	a.started = true
	g := a.gen
	// What the rules did in the background shows in the list, the
	// folders' counts and the log.
	a.svc.OnRuleActivity(func(accountID string) {
		a.post(func() { a.ruleActivity(accountID) })
	})
	// Each sync says where it is, for the line above the list. A
	// background check or an older-mail fetch has its own line already;
	// only a failure of theirs takes this one.
	a.svc.OnSync(func(ev mail.SyncEvent) {
		if !a.syncWorthPosting(ev) {
			return
		}
		a.post(func() { a.syncEvent(ev) })
	})
	// The parsed cache is brought to this build's version, and what no row
	// names any more goes with it: older bodies and attachment files, and
	// the temporary files an earlier run left.
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.PrepareCache(ctx)
	})
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
		// watcher, which may hold a night's mail, finds them read, and
		// the first message written its compose settings.
		set, setErr := a.svc.Settings(ctx)
		compose, composeErr := a.svc.ComposeSettings(ctx)
		// What this device kept of leaving lists, for the sender cards.
		lists := map[string]mail.UnsubscribeRecord{}
		for _, acc := range accounts {
			records, e := a.svc.Unsubscribes(ctx, acc.ID)
			if e != nil {
				continue
			}
			for _, rec := range records {
				lists[unsubKey(rec.AccountID, rec.Email)] = rec
			}
		}
		// An account whose sign-in was revoked or signed out says so from
		// the start, not after a sync has failed.
		signedOut := map[string]bool{}
		for _, acc := range accounts {
			if acc.OAuth == "" {
				continue
			}
			if need, err := a.svc.NeedsSignIn(ctx, acc.ID); err == nil && need {
				signedOut[acc.ID] = true
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.accounts = accounts
			a.lists = lists
			a.needSignIn = signedOut
			if err != nil {
				a.err = err.Error()
			}
			if setErr == nil {
				a.setMailSettings(set)
			}
			if composeErr == nil {
				a.setComposeSettings(compose)
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
			// Reminders due while the app was closed come back now.
			a.deliverReminders()
			if id := a.clicked; id != "" {
				a.clicked = ""
				a.openNotification(id)
			}
		})
	})
	a.deliverWaiting()
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
	a.scopeSet = false
	a.selected = -1
	a.setUndo(nil)
	a.setLabelNote("")
	a.labels.picker = false
	a.list = ui.ListState{}
	a.sel.Clear()
	a.bodies = map[string]mail.FetchResult{}
	// Another account's smart mailboxes are not this one's, and their
	// entries must not be clickable while its list is read.
	a.smarts.list, a.smarts.loaded = nil, false
	a.smarts.form, a.smarts.del = smartForm{}, smartDeletion{}
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
	for _, job := range a.translations {
		if job.cancel != nil {
			job.cancel()
		}
	}
	a.translations = map[string]*translation{}
	a.translationHTMLKey = ""
	a.htmlKey = ""
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
			if !a.imagesLoaded {
				a.loadImagePolicy()
			}
			a.loadReminders(g)
			a.loadFollowUps(g)
			a.loadSmarts(g)
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
			switch {
			case a.signInRequired(accountID, err):
				// Its banner says so, and offers the sign-in.
				a.err = ""
			case err != nil:
				a.err = locales.SyncFailed(err.Error())
			default:
				a.err = ""
				if folders != nil {
					a.setFolders(folders)
				}
				a.ensureFolder()
				a.updateBadge()
			}
			a.loadMessages(g)
			a.loadReminders(g)
			a.loadFollowUps(g)
			a.loadSmarts(g)
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
	// Starred, a label's list, the reminders and the follow-ups read the
	// cache alone.
	if folder == "" || folder == "starred" || folder == remindersFolder || folder == followUpFolder ||
		labelOf(folder) != 0 || smartOf(folder) != 0 {
		return
	}
	if folder == allInboxes {
		a.syncInboxes(g)
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
			switch {
			case a.signInRequired(accountID, err):
				a.err = ""
			case err != nil:
				a.err = locales.SyncFailed(err.Error())
			default:
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
			a.loadReminders(g)
			a.loadFollowUps(g)
			a.loadSmarts(g)
		})
	})
}

// syncSent caches the Sent folder with the account, so that the replies
// the user sent, from here or elsewhere, join their conversations, and so
// that the follow-ups can tell the mail they sent from the answers. The
// folder on show is synced by syncFolder.
func (a *app) syncSent(g uint64) {
	sent := a.folderBySpecial(mail.SpecialSent)
	if sent == "" || sent == a.folder {
		return
	}
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SyncFolder(ctx, accountID, sent, 0)
		a.post(func() {
			if err != nil || a.stale(g) {
				return
			}
			a.loadFollowUps(g)
			if a.threaded() {
				a.loadReminders(g)
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
	if aim := a.aim; threaded && aim != nil && a.aimShown(aim) {
		aimAt = mail.Summary{Account: aim.account, Folder: aim.folder, UID: aim.uid}
	}
	// A smart mailbox whose own folder is gone, or which the account no
	// longer has, has no list to read: the search it saved would silently
	// cover every folder instead.
	if folder == "" || a.smartFolderGone() || smartOf(folder) != 0 && a.shownSmart() == nil {
		a.all, a.convs = nil, nil
		a.loading, a.pager.more = false, false
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
		case q.drafts():
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
			// The labels' counts follow what the list shows, and the rows
			// of each account show its labels.
			a.loadLabels(a.labelAccounts()...)
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
				a.bodyErr = a.bodyError(accountID, err)
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
		a.labelUnread(a.all[i], -1)
	}
	a.updateBadge()
}

// updateBadge puts the unread count of every account's inbox on the
// Dock. Each is the one the account's folders show, which follows what
// the user reads before the server does.
func (a *app) updateBadge() {
	for _, acc := range a.accounts {
		if folders := a.foldersFor(acc.ID); folders != nil {
			a.inboxes[acc.ID] = inboxUnread(folders)
		}
	}
	// A smart mailbox's badge is of the same cache, and follows the same
	// reads and moves.
	if a.smarts.loaded {
		a.countSmarts(a.gen)
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
// from their top. A search the user asked for takes the place of one the
// typing armed.
func (a *app) search() {
	a.searchTimer.stop()
	a.sel.Clear()
	a.list.ScrollTo(0, ui.Start)
	a.loadMessages(a.gen)
}

// searchDelay is how long a search field waits after a keystroke: a word
// typed a letter at a time asks the cache once.
const searchDelay = 150 * time.Millisecond

// searchDebounce is the search a pause in typing will run: the timer the
// last keystroke armed and the number of that search. A later keystroke,
// or a search asked for another way, stops the timer and outnumbers it,
// so the one that waited runs nothing.
type searchDebounce struct {
	timer *time.Timer
	seq   uint64
}

// stop retires the search armed before, if any: what it would have read
// is read by whatever asks for a list now.
func (d *searchDebounce) stop() {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.seq++
}

// armSearch runs do once the typing in a field pauses, for the text the
// field holds as do runs rather than for every word on the way there: a
// keystroke after this one replaces it. A test app arms no timers and
// runs it at once.
func (a *app) armSearch(d *searchDebounce, text func() string, do func()) {
	d.stop()
	if a.after == nil {
		do()
		return
	}
	seq, wanted := d.seq, text()
	d.timer = a.after(searchDelay, func() {
		a.post(func() {
			// The user may have asked for another search, or opened
			// another list, while this one waited.
			if d.seq != seq || text() != wanted {
				return
			}
			do()
		})
	})
}

// searchSoon searches the list once the typing pauses.
func (a *app) searchSoon() {
	a.armSearch(&a.searchTimer, func() string { return a.query }, a.search)
}

// view is the window's root: the settings, account setup, the address
// book or the rules, each of them the main view's only where it has no
// window of its own, or the three panes.
func (a *app) view(c *ui.Context) {
	useTheme(c)
	c.OnShortcut(ui.Cmd, ui.KeySlash, func() { a.shortcutsOpen = true })
	a.shortcutsDialog(c)
	switch {
	case a.showSettings && a.settingsWin == nil:
		a.settingsView(c)
	case a.showAccounts || len(a.accounts) == 0:
		a.accountsView(c)
	case a.book.open && a.contactsWin == nil:
		a.contactsView(c)
	case a.rules.open && a.rulesWin == nil:
		a.rulesView(c)
	default:
		if shellWindow() {
			a.shellView(c)
			return
		}
		a.mailView(c)
	}
}

// mailView draws the three-pane window.
func (a *app) mailView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	// There is no bar across the window: each column's top is its own,
	// as tall as the window controls need, and the window drags by it.
	head := max(bar.Bottom, headHeight)
	c.OnShortcut(ui.Cmd, ui.KeyK, a.openSearch)
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() { a.openSettings(settingsTabGeneral) })
	c.OnShortcut(ui.Cmd, ui.KeyN, func() { a.openCompose(nil) })
	c.OnShortcut(ui.Cmd, ui.KeyR, func() { a.refresh() })
	if s := a.signIn; s != nil && s.account != "" && a.onScreen(s.account) {
		// Escape stops the sign-in its banner waits for, as its Cancel
		// does.
		c.OnShortcut(0, ui.KeyEscape, a.cancelSignIn)
	}
	// Where the window shows its material (macOS, Windows 11), the sidebar
	// lets it through and the panes draw their own background over it.
	vibrant := c.Vibrancy()
	if vibrant {
		c.Root().Background(ui.Transparent)
	}
	// Keyed, so that the mail view is new as it first shows, after the
	// onboarding or the loading, and its sidebar shows at once there
	// rather than opening as it does when the user shows it.
	ui.Row(c.Key("mail-view")).Fill().AlignItems(ui.Stretch).Children(func() {
		// The sidebar runs the window's full height, under the window
		// controls, as a native sidebar does. The panes are keyed, so that
		// they keep their identity, and their transitions, as it comes and
		// goes before them.
		if a.sidebarOpen {
			side := ui.Row(c.Key("sidebar")).Shrink(0).AlignItems(ui.Stretch).ClipX().
				Transition(sidebarMotion)
			if !vibrant {
				side.Background(t.Surface)
			}
			side.Children(func() {
				ui.Column(c).Width(222).Shrink(0).Children(func() {
					a.sidebarHead(c, head)
					a.sidebar(c, t, vibrant)
				})
				ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			})
			a.paneShortcuts(side)
		}
		ui.Column(c.Key("content")).Grow(1).MinWidth(0).Background(t.Background).
			Transition(paneMotion).Children(func() {
			panes := ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				ui.Row(c.Key("list-pane")).Shrink(0).AlignItems(ui.Stretch).Children(func() {
					a.messageList(c, t, bar, head)
					ui.Box(c).Width(1).Shrink(0).Background(t.Border)
				})
				ui.Row(c.Key("reading-pane")).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
					a.readingPane(c, t, head)
				})
			})
			a.paneShortcuts(panes)
		})
	})
	a.accountModal(c)
	a.labelDialog(c)
	a.labelAlerts(c)
	a.smartDialog(c)
	a.smartAlerts(c)
	aiReviewModal(c, &a.review)
	a.linkDialog(c)
	a.unsubscribeDialog(c)
	a.blockSenderDialog(c)
	a.reminderDialog(c)
	a.attachmentPreviewDialog(c)
}

// paneShortcuts gives the sidebar and the panes the unmodified shortcuts.
// They live in those panes' focus subtrees, so typing in the search field
// is never taken for a command.
func (a *app) paneShortcuts(panes ui.Element) {
	panes.OnShortcut(0, ui.KeyJ, func() { a.stepMessage(1) })
	panes.OnShortcut(0, ui.KeyK, func() { a.stepMessage(-1) })
	panes.OnShortcut(0, ui.KeyU, func() { a.toggleRead() })
	panes.OnShortcut(0, ui.KeyS, func() { a.toggleStar() })
	panes.OnShortcut(0, ui.KeyE, func() { a.moveSelected(mail.SpecialArchive) })
	// R answers as the settings say, and Shift+R the other way.
	panes.OnShortcut(0, ui.KeyR, func() { a.reply(a.replyOrder()[0]) })
	panes.OnShortcut(ui.Shift, ui.KeyR, func() { a.reply(a.replyOrder()[1]) })
	panes.OnShortcut(0, ui.KeyF, func() { a.forward() })
	panes.OnShortcut(0, ui.KeyDelete, func() { a.deleteSelected() })
	panes.OnShortcut(0, ui.KeyL, a.openLabelPicker)
}

// iconButton is an icon-only button named for assistive technology.
func (a *app) iconButton(c *ui.Context, svg *ui.SVG, label, tip string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(tip).Padding(barPad, barPad).Radius(barButton).
		Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolButton is a flat toolbar button showing only an icon. Its padding
// is the icon buttons', which lets the whole toolbar fit the reading pane
// at the window's minimum width.
func (a *app) toolButton(c *ui.Context, svg *ui.SVG, label string, on bool, fn func()) ui.Element {
	return a.toolButtonTip(c, svg, label, label, on, fn)
}

// toolButtonTip is a toolbar button whose tooltip says more than its
// label.
func (a *app) toolButtonTip(c *ui.Context, svg *ui.SVG, label, tip string, on bool, fn func()) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip).Disabled(!on).Padding(barPad, barPad).Radius(barButton)
	if on && fn != nil {
		b = b.OnClick(fn)
	}
	return b.Children(func() { ui.Icon(c, svg).FontSize(16) })
}

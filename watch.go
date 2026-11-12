package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// noteBatchMax is how many new messages get a notification each when
// notifications say who wrote. More than that arrive as one count: a
// computer that slept through the night does not wake to a screenful.
const noteBatchMax = 3

// noteIDMax bounds the notification IDs that are looked at. The app's own
// are far shorter.
const noteIDMax = 1024

// note is a system notification to show.
type note struct {
	// ID says what a click opens. The system keeps it, and hands it back
	// also to a later run of the app.
	ID       string
	Title    string
	Subtitle string
	Body     string
	// Group stacks an account's notifications together.
	Group string
}

// noteTarget is what a notification opens: a folder of an account, and
// in it one message or, for a count of several, none.
type noteTarget struct {
	account  string
	folder   string
	validity uint32
	// uid is the message to open, or the newest of those counted.
	uid     mail.UID
	message bool
}

// id writes the target as a notification's ID.
func (t noteTarget) id() string {
	kind := "s"
	if t.message {
		kind = "m"
	}
	return fmt.Sprintf("mail/%s/%s/%s/%d/%d", kind, url.PathEscape(t.account), url.PathEscape(t.folder), t.validity, t.uid)
}

// parseNoteID reads a notification's ID back. The ID comes from outside
// the app: the system stored it, and anything able to post a click may
// send one. Only what id itself would have written is accepted.
func parseNoteID(id string) (noteTarget, bool) {
	if len(id) > noteIDMax {
		return noteTarget{}, false
	}
	parts := strings.Split(id, "/")
	if len(parts) != 6 || parts[0] != "mail" || (parts[1] != "m" && parts[1] != "s") {
		return noteTarget{}, false
	}
	account, err := url.PathUnescape(parts[2])
	if err != nil || account == "" {
		return noteTarget{}, false
	}
	folder, err := url.PathUnescape(parts[3])
	if err != nil || folder == "" {
		return noteTarget{}, false
	}
	validity, err := strconv.ParseUint(parts[4], 10, 32)
	if err != nil || validity == 0 {
		return noteTarget{}, false
	}
	uid, err := strconv.ParseUint(parts[5], 10, 32)
	if err != nil || uid == 0 {
		return noteTarget{}, false
	}
	t := noteTarget{
		account: account, folder: folder, validity: uint32(validity),
		uid: mail.UID(uid), message: parts[1] == "m",
	}
	// Another spelling of the same numbers or names is not one of ours.
	if t.id() != id {
		return noteTarget{}, false
	}
	return t, true
}

// noteMark is the newest message of a folder that the window has dealt
// with as new mail.
type noteMark struct {
	validity uint32
	uid      mail.UID
}

// noteAim is what a clicked notification asked to see, kept until the
// list shows it.
type noteAim struct {
	account string
	folder  string
	// key is the message's key in the list, or "" for the folder alone.
	key string
	// last is set once the folder has synced: a list that still lacks
	// the message will not get it.
	last bool
}

// accountsChanged follows a.accounts: the notification switches and the
// watchers match the accounts there are.
func (a *app) accountsChanged() {
	a.bindMailSettings()
	for _, acc := range a.accounts {
		if _, known := a.inboxes[acc.ID]; !known && acc.ID != a.active {
			a.loadUnread(acc.ID)
		}
	}
	for id := range a.inboxes {
		if a.account(id) == nil {
			delete(a.inboxes, id)
		}
	}
	a.watchAccounts()
	a.updateBadge()
}

// watchOp runs a call that starts or stops a watcher. Such a call waits
// for the watcher's goroutine, so it is a job; the jobs run one at a
// time in the order they were asked for, so that an account removed and
// added again, or edited twice, ends up watched as it was last asked.
func (a *app) watchOp(op func()) {
	a.watchMu.Lock()
	a.watchOps = append(a.watchOps, op)
	a.watchMu.Unlock()
	a.run(func() {
		a.watchTurn.Lock()
		defer a.watchTurn.Unlock()
		for {
			a.watchMu.Lock()
			if len(a.watchOps) == 0 {
				a.watchMu.Unlock()
				return
			}
			next := a.watchOps[0]
			a.watchOps = a.watchOps[1:]
			a.watchMu.Unlock()
			next()
		}
	})
}

// watchAccounts starts a watcher for each account that has none or was
// edited since, and stops those of accounts that are gone.
func (a *app) watchAccounts() {
	if !a.watches {
		return
	}
	present := map[string]bool{}
	for _, acc := range a.accounts {
		present[acc.ID] = true
		if was, ok := a.watched[acc.ID]; ok && was == acc {
			continue
		}
		a.watched[acc.ID] = acc
		a.watch[acc.ID] = mail.WatchStatus{State: mail.WatchConnecting}
		id := acc.ID
		a.watchOp(func() {
			a.svc.Watch(id, func(ev mail.WatchEvent) {
				a.post(func() { a.watchEvent(ev) })
			})
		})
	}
	for id := range a.watched {
		if present[id] {
			continue
		}
		delete(a.watched, id)
		delete(a.watch, id)
		a.watchOp(func() { a.svc.Unwatch(id) })
	}
}

// rewatch watches an account anew. Saving an account calls it: its
// password may have changed, which the account itself does not show.
func (a *app) rewatch(id string) {
	delete(a.watched, id)
}

// loadUnread reads the cached inbox count of an account that is not on
// screen, for the Dock badge and the account menu. The account's watcher
// brings the server's count soon after.
func (a *app) loadUnread(id string) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, id)
		a.post(func() {
			if err != nil || a.account(id) == nil || id == a.active {
				return
			}
			if _, known := a.inboxes[id]; known {
				return
			}
			a.inboxes[id] = inboxUnread(folders)
			a.updateBadge()
		})
	})
}

// inboxUnread is the unread count of the inbox among folders.
func inboxUnread(folders []mail.Folder) int {
	for _, f := range folders {
		if f.IsInbox() {
			return f.Unread
		}
	}
	return 0
}

// setFolders shows the active account's folders.
func (a *app) setFolders(folders []mail.Folder) {
	a.folders = folders
	a.foldersOf = a.active
}

// nudge has the watchers check now. The app coming to the front, the
// computer waking and the Sync button call it.
func (a *app) nudge() {
	if a.watches {
		a.svc.Nudge()
	}
}

// setOnline tells the watchers whether there is a network: without one
// they wait for it instead of trying, and with it back they try at once.
func (a *app) setOnline(online bool) {
	if a.watches {
		a.svc.SetOnline(online)
	}
}

// watchEvent takes a watcher's report: its state, and after a check the
// account's folders and what arrived.
func (a *app) watchEvent(ev mail.WatchEvent) {
	if _, watched := a.watched[ev.AccountID]; !watched || a.account(ev.AccountID) == nil {
		// The account was removed while the report was on its way.
		return
	}
	a.watch[ev.AccountID] = ev.Status
	if ev.Folders == nil {
		return
	}
	if ev.AccountID != a.active {
		a.inboxes[ev.AccountID] = inboxUnread(ev.Folders)
		a.updateBadge()
		a.announce(ev)
		return
	}
	if a.foldersOf != a.active {
		// The account was just switched to and its cache is still being
		// read: that read shows the folders, and the list after it.
		a.inboxes[ev.AccountID] = inboxUnread(ev.Folders)
		a.updateBadge()
		a.announce(ev)
		return
	}
	shown := a.folder
	a.setFolders(ev.Folders)
	a.ensureFolder()
	a.updateBadge()
	// Whether to notify depends on what the window shows now, before
	// the list changes under it.
	a.announce(ev)
	switch {
	case a.folder != shown:
		// The folder on show is gone from the server.
		a.selectFolder()
	case a.folder == ev.Folder:
		a.readList(a.gen, false, false)
	}
}

// announce notifies of the new mail in a report, once.
func (a *app) announce(ev mail.WatchEvent) {
	if len(ev.New) == 0 {
		return
	}
	acc := a.account(ev.AccountID)
	target := noteTarget{account: ev.AccountID, folder: ev.Folder}
	for _, f := range ev.Folders {
		if f.Name == ev.Folder {
			target.validity = f.UIDValidity
		}
	}
	// The engine reports a message as new once. A report that arrives
	// twice all the same is told by what it holds.
	key := ev.AccountID + "/" + ev.Folder
	mark := a.announced[key]
	var fresh []mail.Summary
	for _, m := range ev.New {
		if mark.validity != target.validity || m.UID > mark.uid {
			fresh = append(fresh, m)
			target.uid = max(target.uid, m.UID)
		}
	}
	if len(fresh) == 0 || acc == nil {
		return
	}
	a.announced[key] = noteMark{validity: target.validity, uid: target.uid}
	if !a.mailSet.Notify || a.mailSet.Muted[ev.AccountID] || a.looksAt(ev.AccountID, ev.Folder) {
		return
	}
	a.showNotes(a.notesFor(*acc, target, fresh))
}

// looksAt reports whether the user is looking at a folder's list: the
// window has the focus and shows that folder, unfiltered. Mail arriving
// there appears before their eyes, and a notification would repeat it.
func (a *app) looksAt(accountID, folder string) bool {
	if a.focused == nil || !a.focused() {
		return false
	}
	return !a.showSettings && !a.showAccounts && a.active == accountID &&
		a.folder == folder && strings.TrimSpace(a.query) == ""
}

// notesFor writes the notifications of an account's new messages, newest
// first as the engine reports them. Unless the user asked for more, a
// notification holds nothing of the mail itself: the screen it shows on
// may be shared, locked or mirrored.
func (a *app) notesFor(acc mail.Account, target noteTarget, fresh []mail.Summary) []note {
	if a.mailSet.NotifyDetails && len(fresh) <= noteBatchMax {
		notes := make([]note, 0, len(fresh))
		// The oldest goes first, so that the newest ends on top.
		for i := len(fresh) - 1; i >= 0; i-- {
			m := fresh[i]
			one := target
			one.uid, one.message = m.UID, true
			sender := noteText(m.From.Name)
			if sender == "" {
				sender = noteText(m.From.Email)
			}
			subject := noteText(m.Subject)
			if subject == "" {
				subject = locales.NoSubject()
			}
			notes = append(notes, note{
				ID: one.id(), Title: sender, Subtitle: acc.Label(), Body: subject, Group: acc.ID,
			})
		}
		return notes
	}
	// A count. One message is still the one to open; several open the
	// folder they are in.
	target.message = len(fresh) == 1
	return []note{{
		ID: target.id(), Title: acc.Label(), Body: locales.NewMailCount(len(fresh)), Group: acc.ID,
	}}
}

// noteText is a header's text on one line: what a sender wrote does not
// get to lay out the notification.
func noteText(s string) string { return strings.Join(strings.Fields(s), " ") }

// showNotes hands notifications to the system. Showing one may wait, as
// for the user's answer to whether the app may notify at all, so it is a
// job.
func (a *app) showNotes(notes []note) {
	if a.notify == nil || len(notes) == 0 {
		return
	}
	a.run(func() {
		var err error
		for _, n := range notes {
			if err = a.notify(n); err != nil {
				break
			}
		}
		// The settings say when the system refuses; other failures of
		// one notification are not worth a word.
		denied := errors.Is(err, mygo.ErrNotificationsDenied)
		a.post(func() { a.noteDenied = denied })
	})
}

// openNotification shows what a clicked notification is about: the
// account, the folder and, for one message, the message.
func (a *app) openNotification(id string) {
	if !a.ready {
		// The click that launched the app comes before the accounts
		// are read; it waits for them.
		a.clicked = id
		return
	}
	target, ok := parseNoteID(id)
	if !ok || a.account(target.account) == nil {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, target.account)
		a.post(func() {
			if err != nil || a.account(target.account) == nil {
				return
			}
			for _, f := range folders {
				if f.Name != target.folder {
					continue
				}
				// Under another UIDVALIDITY the UID is another message's,
				// or nobody's: the folder is all there is to show.
				target.message = target.message && f.UIDValidity == target.validity
				a.showTarget(target)
				return
			}
		})
	})
}

// showTarget brings the window forward on a folder, and on a message of
// it once the list has it.
func (a *app) showTarget(target noteTarget) {
	if a.showWindow != nil {
		a.showWindow()
	}
	a.showSettings, a.showAccounts = false, false
	a.closeAccountForm()
	a.aim = &noteAim{account: target.account, folder: target.folder}
	if target.message {
		a.aim.key = summaryKey(mail.Summary{Folder: target.folder, UID: target.uid})
	}
	switch {
	case target.account != a.active:
		a.switchAccount(target.account)
	case a.folder != target.folder:
		a.folder = target.folder
		a.selectFolder()
	default:
		// The folder is on screen. It is read again without a search,
		// and synced: the message may be newer than the cache.
		a.query = ""
		a.loadMessages(a.gen)
		a.syncFolder(a.gen)
	}
}

// aimFolder opens the folder a notification asked for, once the folders
// of its account are there.
func (a *app) aimFolder() {
	if aim := a.aim; aim != nil && aim.account == a.active && a.folderExists(aim.folder) {
		a.folder = aim.folder
	}
}

// aimRow is the row of the message a notification asked for, or -1. The
// wish ends when it is met, and when the synced folder cannot meet it.
func (a *app) aimRow() int {
	aim := a.aim
	if aim == nil || aim.account != a.active || aim.folder != a.folder {
		return -1
	}
	if aim.key != "" {
		for i, m := range a.all {
			if summaryKey(m) == aim.key {
				a.aim = nil
				return i
			}
		}
	}
	if aim.key == "" || aim.last {
		a.aim = nil
	}
	return -1
}

// watchLine says in a few words how receiving mail is going for the
// account on screen, under the sidebar's totals. Trouble is said here
// and nowhere else: the cached mail stays usable, so nothing covers it.
func (a *app) watchLine(c *ui.Context, t *ui.Theme) {
	status, ok := a.watch[a.active]
	if !ok {
		return
	}
	var (
		icon *ui.SVG
		text string
	)
	switch status.State {
	case mail.WatchUpToDate:
		text = locales.WatchUpToDate()
		// The engine's clock need not be in the window's time zone.
		if when := a.messageTime(c, status.CheckedAt.In(c.Now().Location())); when != "" {
			text = locales.WatchCheckedAt(when)
		}
	case mail.WatchRetrying:
		icon = alertIcon
		switch status.Failure {
		case mail.WatchFailNetwork:
			text = locales.WatchRetryNetwork()
		case mail.WatchFailAuth:
			text = locales.WatchRetryAuth()
		default:
			text = locales.WatchRetryOther()
		}
	case mail.WatchOffline:
		icon = offlineIcon
		text = locales.WatchOffline()
	default:
		text = locales.WatchConnecting()
	}
	ui.Row(c).Gap(6).Children(func() {
		if icon != nil {
			ui.Row(c).Width(12).Shrink(0).Padding(2, 0, 0, 0).Children(func() {
				ui.Icon(c, icon).FontSize(12).TextColor(t.Warning)
			})
		}
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0)
	})
}

// setMailSettings shows the stored settings of receiving mail.
func (a *app) setMailSettings(set mail.Settings) {
	if set.Muted == nil {
		set.Muted = map[string]bool{}
	}
	a.mailSet = set
	a.checkChoice = 0
	for i, minutes := range mail.CheckIntervals {
		if minutes == set.CheckMinutes {
			a.checkChoice = i
		}
	}
	a.notifyOn = map[string]*bool{}
	a.bindMailSettings()
}

// bindMailSettings gives each account the switch of its notifications:
// controls bind to variables rather than to map entries.
func (a *app) bindMailSettings() {
	for _, acc := range a.accounts {
		if a.notifyOn[acc.ID] == nil {
			on := !a.mailSet.Muted[acc.ID]
			a.notifyOn[acc.ID] = &on
		}
	}
}

// saveMailSettings writes the settings as the controls show them. Every
// control calls it, so what the settings show is what is saved.
func (a *app) saveMailSettings() {
	set := a.mailSet
	set.CheckMinutes = mail.CheckIntervals[min(max(a.checkChoice, 0), len(mail.CheckIntervals)-1)]
	set.Muted = map[string]bool{}
	for _, acc := range a.accounts {
		if on := a.notifyOn[acc.ID]; on != nil && !*on {
			set.Muted[acc.ID] = true
		}
	}
	a.mailSet = set
	// The saves take turns, and one that a later save has overtaken does
	// not write, so the last choice made is the one stored.
	seq := a.mailSaveSeq.Add(1)
	a.run(func() {
		a.mailSaveMu.Lock()
		defer a.mailSaveMu.Unlock()
		if a.mailSaveSeq.Load() != seq {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SaveSettings(ctx, set)
		a.post(func() { a.mailSetFailed = err != nil })
	})
}

// mailSettingsView draws the settings of receiving mail: how often it is
// checked, and what is notified.
func (a *app) mailSettingsView(c *ui.Context, t *ui.Theme) {
	a.section(c, locales.CheckMail(), func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, locales.CheckEvery()).FontSize(11).Bold().TextColor(t.TextMuted)
			labels := make([]string, 0, len(mail.CheckIntervals))
			for _, minutes := range mail.CheckIntervals {
				labels = append(labels, locales.CheckMinutes(minutes))
			}
			choice := ui.Segmented(c, &a.checkChoice, labels...).Label(locales.CheckEvery()).Width(320)
			if choice.Changed() {
				a.saveMailSettings()
			}
		})
		ui.Text(c, locales.CheckEveryDetail()).FontSize(12).TextColor(t.TextMuted)
	})
	a.section(c, locales.Notifications(), func() {
		ui.Column(c).Gap(12).Padding(2, 0, 0, 0).Children(func() {
			a.switchRow(c, t, locales.NotifyNewMail(), locales.NotifyNewMailDetail(), &a.mailSet.Notify, false)
			a.switchRow(c, t, locales.NotifyDetails(), locales.NotifyDetailsDetail(), &a.mailSet.NotifyDetails, !a.mailSet.Notify)
			// One account's switch would only repeat the one above.
			if len(a.accounts) > 1 {
				ui.Text(c, locales.NotifyAccounts()).FontSize(11).Bold().TextColor(t.TextMuted).Margin(4, 0, 0, 0)
				for _, acc := range a.accounts {
					on := a.notifyOn[acc.ID]
					if on == nil {
						continue
					}
					ui.Column(c.Key(acc.ID)).Children(func() {
						a.switchRow(c, t, acc.Label(), acc.Email, on, !a.mailSet.Notify)
					})
				}
			}
			if a.noteDenied {
				a.settingsWarning(c, t, locales.NotifyDenied())
			}
			if a.mailSetFailed {
				a.settingsWarning(c, t, locales.SettingsSaveFailed())
			}
		})
	})
}

// switchRow is a switch with its name and a line about it. The name
// labels the switch and toggles it when clicked.
func (a *app) switchRow(c *ui.Context, t *ui.Theme, title, detail string, on *bool, disabled bool) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		var caption ui.Element
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			caption = ui.Text(c, title).FontSize(13).Bold()
			if detail != "" && detail != title {
				ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted)
			}
		})
		ui.Switch(c, on).LabelledBy(caption).Disabled(disabled).OnChange(a.saveMailSettings)
	})
}

// settingsWarning is a line of the settings that says something is off.
func (a *app) settingsWarning(c *ui.Context, t *ui.Theme, text string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(8).
		Background(t.Warning.Alpha(0.12)).Children(func() {
		ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0)
	})
}

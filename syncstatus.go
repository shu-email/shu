package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// How a sync is going, above the list (issue: sync progress). The engine
// reports each step (mail.SyncEvent); the window keeps the last word of
// each of an account's syncs and draws the account on show. Nothing here
// polls, and one sync finishing does not take another's line.

// syncStatus is what one sync last said, and when, so that the account
// shows its newest running sync, or, with none running, its newest
// failure.
type syncStatus struct {
	event mail.SyncEvent
	seq   uint64
}

// syncKey names a sync of an account: what kind, and of which folder. A
// watcher checks one folder and reports before it knows which; its run is
// the account's alone.
func syncKey(ev mail.SyncEvent) string {
	if ev.Kind == mail.SyncKindWatch {
		return "watch"
	}
	return strconv.Itoa(int(ev.Kind)) + "/" + ev.Folder
}

// syncWorthPosting tells whether an event may change the notice. A
// watcher's check and FetchOlder already have their lines (the first
// above the list, the second in the list's footer), so only a failure of
// theirs takes the notice, and its end is posted only when it clears one:
// otherwise every check at the interval would rebuild the window though
// nothing on it changed.
func (a *app) syncWorthPosting(ev mail.SyncEvent) bool {
	if ev.Kind != mail.SyncKindWatch && ev.Kind != mail.SyncKindOlder {
		return true
	}
	key := ev.AccountID + "/" + syncKey(ev)
	a.syncPostMu.Lock()
	defer a.syncPostMu.Unlock()
	if ev.Failure != mail.SyncFailNone {
		a.syncPosted[key] = true
		return true
	}
	if ev.Phase != mail.SyncPhaseFinished && !ev.Cancelled {
		return false
	}
	if !a.syncPosted[key] {
		return false
	}
	delete(a.syncPosted, key)
	return true
}

// syncEvent takes a sync's report: a run that ends, well or stopped, takes
// its own line away; one that fails keeps it, with the step it reached and
// why, until that sync starts again. Another run of the account keeps its
// own line meanwhile, so a watcher's check is not the folder sync's.
func (a *app) syncEvent(ev mail.SyncEvent) {
	if ev.AccountID == "" {
		return
	}
	key := syncKey(ev)
	if ev.Phase == mail.SyncPhaseFinished || ev.Cancelled {
		if runs := a.syncs[ev.AccountID]; runs != nil {
			delete(runs, key)
			if len(runs) == 0 {
				delete(a.syncs, ev.AccountID)
			}
		}
		return
	}
	a.syncSeq++
	if a.syncs[ev.AccountID] == nil {
		a.syncs[ev.AccountID] = map[string]syncStatus{}
	}
	a.syncs[ev.AccountID][key] = syncStatus{event: ev, seq: a.syncSeq}
}

// syncNotice draws how the account on show is syncing: a spinner and what
// it is reading while it runs, or, when it stopped, the step that failed
// and why.
func (a *app) syncNotice(c *ui.Context, t *ui.Theme) {
	var running, failedStatus syncStatus
	haveRunning, haveFailed := false, false
	for _, s := range a.syncs[a.active] {
		if s.event.Failure == mail.SyncFailNone {
			if !haveRunning || s.seq > running.seq {
				running, haveRunning = s, true
			}
		} else if !haveFailed || s.seq > failedStatus.seq {
			failedStatus, haveFailed = s, true
		}
	}
	ev, ok := running.event, haveRunning
	if !ok {
		ev, ok = failedStatus.event, haveFailed
	}
	if !ok {
		return
	}
	failed := ev.Failure != mail.SyncFailNone
	bannerRow(c, "sync/notice", syncNoticeBackground(t, failed)).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		if failed {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, syncFailureText(ev)).FontSize(12).Grow(1).MinWidth(0).MaxLines(2).
				Role(ui.RoleStatus)
			return
		}
		ui.Spinner(c).Size(14, 14)
		ui.Text(c, syncProgressText(ev)).FontSize(12).Grow(1).MinWidth(0).SingleLine().
			Role(ui.RoleStatus)
	})
}

func syncNoticeBackground(t *ui.Theme, failed bool) ui.Color {
	if failed {
		return t.Warning.Alpha(0.10)
	}
	return t.Accent.Alpha(0.06)
}

// syncSubject names what a sync reads: a folder, drafts, or mail when the
// account's inbox is not known yet.
func syncSubject(ev mail.SyncEvent) string {
	switch {
	case ev.Kind == mail.SyncKindDrafts:
		return locales.Drafts()
	case ev.Folder != "":
		return ev.Folder
	default:
		return locales.SyncSubjectMail()
	}
}

// syncProgressText says what a running sync is reading, with how far it
// has come when the engine knows how much there is.
func syncProgressText(ev mail.SyncEvent) string {
	subject := syncSubject(ev)
	if ev.Total > 0 {
		return locales.SyncProgress(subject, ev.Done, ev.Total)
	}
	return locales.SyncStarted(subject)
}

// syncFailureText says what failed, in which step, and why, in words of
// the window's own: never the server's error text.
func syncFailureText(ev mail.SyncEvent) string {
	return locales.SyncFailedAt(syncSubject(ev), syncPhaseText(ev.Phase), syncReasonText(ev.Failure))
}

func syncPhaseText(phase mail.SyncPhase) string {
	switch phase {
	case mail.SyncPhaseReading:
		return locales.SyncPhaseReading()
	case mail.SyncPhaseWriting:
		return locales.SyncPhaseWriting()
	default:
		return locales.SyncPhaseConnecting()
	}
}

func syncReasonText(failure mail.SyncFailure) string {
	switch failure {
	case mail.SyncFailNetwork:
		return locales.SyncReasonNetwork()
	case mail.SyncFailAuth:
		return locales.SyncReasonAuth()
	case mail.SyncFailSignIn:
		return locales.SyncReasonSignIn()
	default:
		return locales.SyncReasonOther()
	}
}

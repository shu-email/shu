package main

import (
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// How a sync is going, above the list (issue: sync progress). The engine
// reports each step (mail.SyncEvent); the window keeps the last one for
// each account and draws the account on show. Nothing here polls.

// syncEvent takes a sync's report: a run that ends, well or stopped, takes
// its line away; one that fails keeps it, with the step it reached and
// why, until another sync starts.
func (a *app) syncEvent(ev mail.SyncEvent) {
	if ev.AccountID == "" {
		return
	}
	switch {
	case ev.Phase == mail.SyncPhaseFinished, ev.Cancelled:
		delete(a.syncs, ev.AccountID)
	default:
		a.syncs[ev.AccountID] = ev
	}
}

// syncNotice draws how the account on show is syncing: a spinner and what
// it is reading while it runs, or, when it stopped, the step that failed
// and why.
func (a *app) syncNotice(c *ui.Context, t *ui.Theme) {
	ev, ok := a.syncs[a.active]
	if !ok {
		return
	}
	failed := ev.Failure != mail.SyncFailNone
	ui.Row(c.Key("sync/notice")).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(syncNoticeBackground(t, failed)).BorderWidth(0, 0, 1, 0).
		BorderColor(t.Border).Transition(bannerMotion).Children(func() {
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

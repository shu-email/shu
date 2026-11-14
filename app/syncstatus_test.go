package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// A sync of the account on show says what it is reading, and how far it
// has come when the engine knows; the end of it takes the line away.
func TestSyncNoticeShowsProgress(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	if !tt.HasText("Syncing INBOX…") {
		t.Fatalf("the line does not say what is syncing: %q", tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseReading, Done: 3, Total: 10})
	tt.Frame()
	if !tt.HasText("Syncing INBOX… 3 of 10") {
		t.Errorf("the progress is not shown: %q", tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseFinished})
	tt.Frame()
	if tt.HasText("Syncing INBOX… 3 of 10") {
		t.Error("the line stayed after the sync ended")
	}
}

// A sync that fails says the step it reached and why, until another runs;
// one the app stopped says nothing.
func TestSyncNoticeSaysWhyASyncFailed(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseReading, Failure: mail.SyncFailNetwork})
	tt.Frame()
	if want := "Could not sync INBOX while fetching mail: the server could not be reached"; !tt.HasText(want) {
		t.Errorf("the failure is not said as %q: %q", want, tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseReading, Cancelled: true})
	tt.Frame()
	if tt.HasText("Could not sync INBOX") {
		t.Error("a cancelled sync was reported as a failure")
	}
}

// A sync of another folder of the account does not take the line, and one
// ending there does not take another's: the syncs of one account may run
// beside one another.
func TestSyncNoticeKeepsEachSyncsLine(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "Sent", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	if !tt.HasText("Syncing Sent…") {
		t.Fatalf("the newest sync is not the line: %q", tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "Sent", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseFinished})
	tt.Frame()
	if !tt.HasText("Syncing INBOX…") {
		t.Errorf("the sync still running lost its line: %q", tt.Texts())
	}
}

// A background check already has its own line, so it neither flashes the
// notice nor changes it; a check that fails says why there.
func TestSyncNoticeLeavesBackgroundChecksAlone(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	watch := func(phase mail.SyncPhase, failure mail.SyncFailure, cancelled bool) {
		fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindWatch,
			Phase: phase, Failure: failure, Cancelled: cancelled})
	}
	watch(mail.SyncPhaseConnecting, mail.SyncFailNone, false)
	tt.Frame()
	watch(mail.SyncPhaseReading, mail.SyncFailNone, false)
	tt.Frame()
	if tt.HasText("Syncing INBOX…") {
		t.Fatalf("a background check took the notice: %q", tt.Texts())
	}
	watch(mail.SyncPhaseReading, mail.SyncFailNetwork, false)
	tt.Frame()
	if want := "Could not sync INBOX while fetching mail: the server could not be reached"; !tt.HasText(want) {
		t.Errorf("a failed check is not said as %q: %q", want, tt.Texts())
	}
	watch(mail.SyncPhaseFinished, mail.SyncFailNone, false)
	tt.Frame()
	if tt.HasText("Could not sync INBOX") {
		t.Error("a check that worked later left its failure")
	}
}

// The end of another folder's sync does not take a failure that no newer
// sync of that folder has replaced.
func TestSyncNoticeKeepsAFailureUntilItsSyncRuns(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseReading, Failure: mail.SyncFailNetwork})
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "Sent", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "Sent", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseFinished})
	tt.Frame()
	if !tt.HasText("Could not sync INBOX") {
		t.Errorf("another folder's sync took the failure: %q", tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a1", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	if tt.HasText("Could not sync INBOX") {
		t.Error("a new sync of the folder left the old failure")
	}
}

// Drafts are the account's, not a folder's, and another account's sync is
// not the line of the account on show.
func TestSyncNoticeNamesDrafts(t *testing.T) {
	a, fs := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	fs.saySync(mail.SyncEvent{AccountID: "a1", Kind: mail.SyncKindDrafts,
		Phase: mail.SyncPhaseWriting})
	tt.Frame()
	if !tt.HasText("Syncing Drafts…") {
		t.Fatalf("drafts are not named: %q", tt.Texts())
	}
	fs.saySync(mail.SyncEvent{AccountID: "a2", Folder: "INBOX", Kind: mail.SyncKindFolder,
		Phase: mail.SyncPhaseConnecting})
	tt.Frame()
	if !tt.HasText("Syncing Drafts…") {
		t.Errorf("another account's sync took the line: %q", tt.Texts())
	}
}

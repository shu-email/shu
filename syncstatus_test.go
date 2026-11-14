package main

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

package app

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// A sound cache says so, and the check runs once per press.
func TestCacheCheckReportsASoundCache(t *testing.T) {
	a, fs := sampleMessageApp(t)
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 1500)
	if !tt.HasText("Cache check") || !tt.HasText("Check Cache") {
		t.Fatalf("the action is missing: %q", tt.Texts())
	}
	if err := tt.Click("Check Cache"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if fs.integrityCalls != 1 {
		t.Errorf("the check ran %d times", fs.integrityCalls)
	}
	if !tt.HasText("No problems found.") {
		t.Errorf("a sound cache does not say so: %q", tt.Texts())
	}
}

// Every kind of finding is named with how many, and a few of their IDs.
func TestCacheCheckListsFindings(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.integrity = mail.IntegrityReport{Findings: []mail.IntegrityFinding{
		{Kind: mail.IntegritySchema, Count: 1, IDs: []string{"3", "24"}},
		{Kind: mail.IntegrityThreads, Count: 2, IDs: []string{"7", "9"}},
		{Kind: mail.IntegrityAttachmentMissing, Count: 1, IDs: []string{"ab12"}},
		{Kind: mail.IntegritySearchIndex, Count: 1},
	}}
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 1500)
	if err := tt.Click("Check Cache"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{
		"Schema version 3, this build expects 24",
		"Conversation counts out of step: 2 (7, 9)",
		"Attachment files missing: 1 (ab12)",
		"Search index out of step: 1",
	} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
}

// A check that cannot run says why, and keeps no findings of an earlier
// run.
func TestCacheCheckFailure(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.integrity = mail.IntegrityReport{Findings: []mail.IntegrityFinding{
		{Kind: mail.IntegrityThreads, Count: 1, IDs: []string{"7"}},
	}}
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 1500)
	if err := tt.Click("Check Cache"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Conversation counts out of step") {
		t.Fatalf("the first check did not show: %q", tt.Texts())
	}
	fs.integrity = mail.IntegrityReport{}
	fs.integrityErr = errors.New("store: the database gave no schema version")
	if err := tt.Click("Check Cache"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("The check could not run: store: the database gave no schema version") {
		t.Errorf("the failure is not shown: %q", tt.Texts())
	}
	if tt.HasText("Conversation counts out of step") {
		t.Error("the earlier findings stayed after a failed run")
	}
}

// What a run proved before it failed is shown beside the reason.
func TestCacheCheckKeepsPartialFindings(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.integrity = mail.IntegrityReport{Findings: []mail.IntegrityFinding{
		{Kind: mail.IntegrityThreads, Count: 1, IDs: []string{"7"}},
	}}
	fs.integrityErr = errors.New("mail: the draft files are unreadable")
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 1500)
	if err := tt.Click("Check Cache"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{
		"The check could not run: mail: the draft files are unreadable",
		"Conversation counts out of step: 1 (7)",
	} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
}

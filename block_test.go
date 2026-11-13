package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// leaveHeaders is a newsletter whose list offers a one-click request and
// an address, as a message's headers carry them.
const leaveHeaders = "List-Unsubscribe: <https://shop.example/unsub?id=1>, <mailto:leave@shop.example>\r\n" +
	"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"

// Leaving a list shows the exact destination first, and nothing leaves
// the device unless the user confirms it there.
func TestUnsubscribeNeedsConfirmation(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, leaveHeaders, nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Unsubscribe…"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Leave this mailing list?", "https://shop.example/unsub?id=1", "one-click request"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	// Cancelling sends nothing.
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fs.unsubCalls) != 0 {
		t.Fatalf("a cancelled confirmation sent %v", fs.unsubCalls)
	}

	if err := tt.Click("Unsubscribe…"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Unsubscribe"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := "a1 maya@example.com one-click https://shop.example/unsub?id=1"
	if !slices.Equal(fs.unsubCalls, []string{want}) {
		t.Errorf("sent %v, want %q", fs.unsubCalls, want)
	}
	if !tt.HasText("You unsubscribed from this list on") {
		t.Errorf("the record does not show: %q", tt.Texts())
	}
}

// A list that names a page opens it: the confirmation says so, and the
// app sends nothing else.
func TestUnsubscribePageConfirmsTheURL(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "List-Unsubscribe: <https://shop.example/unsubscribe>\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Unsubscribe…"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Open Page", "https://shop.example/unsubscribe", "opens the list's page in your browser"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Open Page"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := "a1 maya@example.com page https://shop.example/unsubscribe"
	if !slices.Equal(fs.unsubCalls, []string{want}) {
		t.Errorf("recorded %v, want %q", fs.unsubCalls, want)
	}
}

// A message whose list offers nothing is not offered an unsubscribe, and
// one that offers a list an unsubscribe email.
func TestUnsubscribeOfferFollowsTheHeaders(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "Delivered-To: me@example.com\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("Unsubscribe…") {
		t.Fatalf("a message without a list offers an unsubscribe: %q", tt.Texts())
	}

	a, fs = sampleMessageApp(t)
	loadFirst(t, fs, a, "List-Unsubscribe: <mailto:leave@shop.example?subject=unsubscribe>\r\n", nil)
	tt = ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Unsubscribe…"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("leave@shop.example") || !tt.HasText("unsubscribe email from this account") {
		t.Fatalf("the email confirmation is missing: %q", tt.Texts())
	}
	if err := tt.Click("Unsubscribe"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fs.unsubCalls) != 1 || !strings.Contains(fs.unsubCalls[0], "email leave@shop.example") {
		t.Errorf("sent %v", fs.unsubCalls)
	}
}

// Blocking a sender asks first, files its new mail where the user chose,
// and the banner takes the block back.
func TestBlockSenderThenUndo(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "Authentication-Results: mx.example; dmarc=pass header.from=example.com\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Block Sender…"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Block this sender?", "maya@example.com",
		"Block the whole domain @example.com", "Move new mail to Junk", "Move new mail to Trash",
	} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Block"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Equal(fs.blockCalls, []string{"a1 maya@example.com Junk"}) {
		t.Errorf("blocked %v", fs.blockCalls)
	}
	if got := fs.blocks["a1"]; len(got) != 1 || got[0].Value != "maya@example.com" || got[0].Folder != "Junk" {
		t.Errorf("blocks = %+v", got)
	}
	if !tt.HasText("Sender blocked") {
		t.Fatalf("no undo banner: %q", tt.Texts())
	}
	if err := tt.Click("Undo"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fs.blocks["a1"]) != 0 {
		t.Errorf("the undo left %+v", fs.blocks["a1"])
	}
}

// The whole domain can be blocked, and its mail filed to the trash.
func TestBlockWholeDomain(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Block Sender…"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Block the whole domain @example.com"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Move new mail to Trash"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Block"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Equal(fs.blockCalls, []string{"a1 example.com Trash"}) {
		t.Errorf("blocked %v", fs.blockCalls)
	}
	if got := fs.blocks["a1"]; len(got) != 1 || !got[0].Domain || got[0].Value != "example.com" {
		t.Errorf("blocks = %+v", got)
	}
}

// The settings' Privacy tab lists the blocked senders and unblocks them.
func TestPrivacyTabListsBlockedSenders(t *testing.T) {
	a, fs := sampleMessageApp(t)
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	fs.blocks["a1"] = []mail.BlockedSender{
		{AccountID: "a1", Value: "deals@shop.example", Folder: "Junk", At: at},
		{AccountID: "a1", Value: "spam.example", Domain: true, Folder: "Trash", At: at},
	}
	a.openSettings(settingsTabPrivacy)
	tt := ui.NewTester(a.view, 1220, 800)
	for _, want := range []string{"Blocked senders", "deals@shop.example", "@spam.example", "New mail goes to Junk"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if strings.Count(strings.Join(tt.Texts(), "\n"), "Unblock") != 2 {
		t.Fatalf("want an Unblock for each block: %q", tt.Texts())
	}
	if err := tt.Click("Unblock"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fs.blocks["a1"]) != 1 || fs.blocks["a1"][0].Value != "spam.example" {
		t.Fatalf("after unblocking: %+v", fs.blocks["a1"])
	}
	if tt.HasText("deals@shop.example") {
		t.Error("the unblocked sender still shows")
	}
}

func TestPrivacyTabWithoutBlocks(t *testing.T) {
	a, _ := sampleMessageApp(t)
	a.openSettings(settingsTabPrivacy)
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText("No blocked senders.") {
		t.Fatalf("empty state missing: %q", tt.Texts())
	}
	if !tt.HasText("Blocked senders") {
		t.Fatal("the tab has no title")
	}
}

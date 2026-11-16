package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// pngMark is the icon a fake domain answers with, 32 pixels of one colour.
func pngMark(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0x11, G: 0x6f, B: 0xef, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A frame asks each sender's domain once, and draws the icon it gets; the
// addresses of a mail service, whose icon is the provider's, ask nothing.
func TestSenderAvatarsAskEachDomainOnce(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.icons = map[string][]byte{"github.com": pngMark(t), "example.com": pngMark(t)}
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	if got := countDomains(fs.iconRequests); got["example.com"] != 1 || len(got) != 1 {
		t.Fatalf("asked %+v, want the sender shown and the account's domain once", got)
	}
	if a.icons["example.com"] == nil {
		t.Error("no icon kept for the sender shown")
	}

	// The next sender shown is asked for as it appears, and the frames
	// after ask nothing: a domain is asked once.
	a.selected = 1
	tt.Frame()
	if got := countDomains(fs.iconRequests); got["github.com"] != 1 {
		t.Fatalf("asked %+v, want the next sender's domain once", got)
	}
	if a.icons["github.com"] == nil {
		t.Error("no icon kept for the sender shown next")
	}
	asked := len(fs.iconRequests)
	tt.Frame()
	tt.Frame()
	if n := len(fs.iconRequests); n != asked {
		t.Errorf("%d more requests after the icons arrived", n-asked)
	}
}

// countDomains counts the asks by domain.
func countDomains(asked []string) map[string]int {
	out := map[string]int{}
	for _, domain := range asked {
		out[domain]++
	}
	return out
}

// The initials of a sender at a public mailbox stay: its domain's icon is
// the provider's, and says nothing of who wrote.
func TestSenderAvatarsSkipMailServices(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.addAccount(testAccount("a2", "Gmail", "ann@gmail.com"), "secret",
		[]mail.Folder{{Name: "INBOX", SpecialUse: "\\Inbox", Subscribed: true}},
		[]mail.Summary{{UID: 9, Folder: "INBOX", Subject: "Hello", Preview: "Body",
			From: mail.Address{Name: "Ann", Email: "ann@gmail.com"}}})
	a.switchAccount("a2")
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	for _, domain := range fs.iconRequests {
		if domain == "gmail.com" {
			t.Fatalf("asked for a mail service's icon: %v", fs.iconRequests)
		}
	}
	if a.icons["gmail.com"] != nil {
		t.Error("the provider's icon is kept for a sender there")
	}
}

// Without the setting the window asks nothing, and what it had goes when
// the user turns the setting off.
func TestSenderAvatarsOffAskNothing(t *testing.T) {
	a, fs := sampleMessageApp(t)
	a.mailSet.SenderIcons = false
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	if len(fs.iconRequests) > 0 {
		t.Errorf("asked %v though the setting is off", fs.iconRequests)
	}

	// The setting is the settings' own switch, and turning it off forgets
	// the icons the window holds.
	a.mailSet.SenderIcons = true
	a.icons["github.com"] = ui.NewBitmap(image.NewNRGBA(image.Rect(0, 0, 32, 32)))
	a.openSettings(settingsTabGeneral)
	tt = ui.NewTester(a.view, 1220, 900)
	if !tt.HasText(locales.SenderIcons()) || !tt.HasText(locales.ShowSenderIconsDetail()) {
		t.Fatalf("the settings lack the switch: %q", tt.Texts())
	}
	click(t, tt, locales.ShowSenderIcons())
	if fs.settings.SenderIcons {
		t.Error("the setting was not saved off")
	}
	if len(a.icons) != 0 {
		t.Errorf("icons kept after turning the setting off: %v", a.icons)
	}
}

// A domain that failed is not asked again in this run: the initials show,
// and the next run tries once more.
func TestSenderAvatarsDoNotRepeatAFailure(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.iconErr = errTestIcon
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	first := len(fs.iconRequests)
	tt.Frame()
	if n := len(fs.iconRequests); n != first {
		t.Errorf("%d more requests after a failed lookup", n-first)
	}
	if !a.iconGone["example.com"] {
		t.Error("a failed domain is asked again in the same run")
	}
}

// errTestIcon is a lookup that could not reach its sources.
var errTestIcon = &testIconError{}

type testIconError struct{}

func (*testIconError) Error() string { return "the network is down" }

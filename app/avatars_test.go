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
	// The rows' senders and the account's own domain, each once.
	if got := countDomains(fs.iconRequests); got["example.com"] != 1 || got["github.com"] != 1 || len(got) != 2 {
		t.Fatalf("asked %+v, want each domain of the window once", got)
	}
	for _, domain := range []string{"github.com", "example.com"} {
		if a.icons[domain] == nil {
			t.Errorf("no icon kept for %s", domain)
		}
	}

	// The frames after ask nothing: a domain is asked once.
	asked := len(fs.iconRequests)
	a.selected = 1
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

// A row names the recipient where it names no sender: the icon is the one
// of the address the row's first line says.
func TestRowAvatarNamesTheAddressTheRowDoes(t *testing.T) {
	a, _ := sampleMessageApp(t)
	m := mail.Summary{
		From: mail.Address{Name: "GitHub", Email: "notifications@github.com"},
		To:   []mail.Address{{Name: "Bob", Email: "bob@stripe.com"}},
	}
	if got := a.rowAvatar(m); got.Email != "notifications@github.com" {
		t.Errorf("a mail row's icon is %q, want the sender's", got.Email)
	}
	a.folder = followUpFolder
	if got := a.rowAvatar(m); got.Email != "bob@stripe.com" {
		t.Errorf("a follow-up row's icon is %q, want the recipient's", got.Email)
	}
	a.folder = draftsFolder
	if got := a.rowAvatar(m); got.Email != "bob@stripe.com" {
		t.Errorf("a draft row's icon is %q, want the recipient's", got.Email)
	}
	// A draft with no recipient yet has no address, and the initials show.
	a.folder = draftsFolder
	if got := a.rowAvatar(mail.Summary{From: m.From}); got.Email != "" {
		t.Errorf("a draft without a recipient names %q, want none", got.Email)
	}
}

// A row's picture costs the list no density: a row with an icon is as tall
// as one without.
func TestSenderAvatarsKeepTheRowDensity(t *testing.T) {
	a, fs := sampleMessageApp(t)
	fs.icons = map[string][]byte{"github.com": pngMark(t)}
	const row = "GitHub [shu-email/shu] Pull request #128"
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Frame()
	with, ok := tt.Find(row)
	if !ok {
		t.Fatalf("no row %q: %q", row, tt.Texts())
	}
	a.mailSet.SenderIcons = false
	tt.Frame()
	without, ok := tt.Find(row)
	if !ok {
		t.Fatalf("the row went with its icon: %q", tt.Texts())
	}
	// The height is the platform's own, from the font's metrics: what
	// holds is that the picture adds none of it.
	if with.H != without.H {
		t.Errorf("a row with an icon is %g high, without one %g", with.H, without.H)
	}
}

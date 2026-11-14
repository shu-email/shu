package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// loadFirst gives the first message of a sample window a body with the
// given raw headers and attachments, as a fetch would.
func loadFirst(t *testing.T, fs *fakeService, a *app, raw string, attachments []mail.Attachment) mail.Summary {
	t.Helper()
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Body", BodyCached: true,
		RawHeaders: raw, Attachments: attachments,
	}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	if _, ok := a.bodies[summaryKey(m)]; !ok {
		t.Fatal("body did not load")
	}
	return m
}

func TestSecurityNoticeStates(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "passed",
			raw:  "Authentication-Results: mx.example; spf=pass; dkim=pass; dmarc=pass header.from=example.com\r\n",
			want: "Sender verified",
		},
		{
			name: "failed",
			raw:  "Authentication-Results: mx.example; spf=fail; dkim=none; dmarc=fail header.from=example.com\r\n",
			want: "Sender check failed",
		},
		{
			name: "absent is never shown as safe",
			raw:  "Delivered-To: me@example.com\r\n",
			want: "Sender not verified",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, fs := sampleMessageApp(t)
			loadFirst(t, fs, a, c.raw, nil)
			tt := ui.NewTester(a.view, 1220, 800)
			if !tt.HasText(c.want) {
				t.Fatalf("missing %q in %q", c.want, tt.Texts())
			}
			for _, harmless := range []string{"Sender verified", "Sender check failed", "Sender not verified"} {
				if harmless != c.want && tt.HasText(harmless) {
					t.Errorf("shows %q as well as %q", harmless, c.want)
				}
			}
		})
	}
}

// The card reads what the cache kept of the headers, not the headers
// themselves: a message read after a restart shows its verification and
// its list, though its body came without a header line.
func TestSecurityNoticeReadsThePersistedState(t *testing.T) {
	a, fs := sampleMessageApp(t)
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Body", BodyCached: true,
		Security: &mail.Security{
			Trust: mail.Trust{
				Verdict:  mail.AuthPassed,
				Checks:   []mail.AuthCheck{{Method: "dmarc", Result: mail.ResultPass}},
				Evidence: []string{"Authentication-Results: mx.example; dmarc=pass header.from=example.com"},
			},
			ListUnsubscribe: "<https://lists.example.org/unsubscribe?id=7>",
		},
	}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText("Sender verified") {
		t.Fatalf("the persisted verdict is not shown: %q", tt.Texts())
	}
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Mailing list. To unsubscribe: <https://lists.example.org/unsubscribe?id=7>",
		"Authentication-Results: mx.example; dmarc=pass header.from=example.com",
	} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
}

func TestSecurityDetailsShowTheChecks(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "Authentication-Results: mx.example; spf=pass; dkim=fail header.d=example.com; dmarc=fail header.from=example.com\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Checks: SPF pass · DKIM fail · DMARC fail", "The receiving server reported that a sender check failed"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Hide reason"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("Checks: SPF pass · DKIM fail · DMARC fail") {
		t.Error("details stay after Hide reason")
	}
}

func TestExternalSenderAndReplyToWarnings(t *testing.T) {
	a, fs := sampleMessageApp(t)
	m := a.all[0]
	m.From = mail.Address{Name: "Billing", Email: "billing@outside.example"}
	m.ReplyTo = []mail.Address{{Email: "collect@even-more-outside.example"}}
	a.all[0] = m
	loadFirst(t, fs, a, "Authentication-Results: mx.example; dmarc=pass header.from=outside.example\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	for _, want := range []string{"External sender", "Replies go elsewhere"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Why?"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("billing@outside.example is outside your organization's domains") {
		t.Errorf("external explanation missing: %q", tt.Texts())
	}
	if !tt.HasText("The message is from outside.example, but replies would go to collect@even-more-outside.example") {
		t.Errorf("reply explanation missing: %q", tt.Texts())
	}
}

func TestAttachmentWarnings(t *testing.T) {
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "Authentication-Results: mx.example; dmarc=pass\r\n", []mail.Attachment{
		{Part: "2", Filename: "invoice.pdf.exe", ContentType: "application/pdf", Size: 10},
		{Part: "3", Filename: "photo.png", ContentType: "image/png", Size: 10},
	})
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText("Name hides a program") {
		t.Errorf("warning missing: %q", tt.Texts())
	}
	if tt.HasText("Can run a program") || tt.HasText("Content does not match the name") {
		t.Errorf("wrong warning: %q", tt.Texts())
	}
}

// A link in HTML mail never opens by itself: the window shows its real
// destination first, and only the user's click leaves.
func TestLinkNeedsConfirmation(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.askOpenLink("https://evil.example/login?next=bank")
	tt.Frame()
	if !tt.HasText("Open this link?") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	if !tt.HasText("https://evil.example/login?next=bank") {
		t.Errorf("destination missing: %q", tt.Texts())
	}
	if err := tt.Click("Open link"); err != nil {
		t.Fatal(err)
	}
	if a.linkOpen || a.linkURL != "" {
		t.Errorf("link still pending: open=%v url=%q", a.linkOpen, a.linkURL)
	}
}

// The dialog names the real domain before the full address, and explains
// a deceptive one in plain words, in both languages.
func TestLinkDialogExplainsDeception(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.askOpenLink("https://bank.com@evil.example/login")
	tt.Frame()
	var message string
	for _, text := range tt.Texts() {
		if strings.Contains(text, "bank.com@evil.example") {
			message = text
			break
		}
	}
	if message == "" {
		t.Fatalf("no destination shown: %q", tt.Texts())
	}
	domain, address := strings.Index(message, "evil.example"), strings.Index(message, "https://bank.com@evil.example/login")
	if domain < 0 || address < 0 || domain > address {
		t.Errorf("the real domain does not come first: %q", message)
	}
	if !strings.Contains(message, "before @ is not the site") {
		t.Errorf("no plain-language reason: %q", message)
	}

	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	tt.SetLocale("zh-Hans")
	tt.Frame()
	found := false
	for _, text := range tt.Texts() {
		if strings.Contains(text, "值得注意的迹象") && strings.Contains(text, "@ 前的") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the Chinese dialog does not explain it: %q", tt.Texts())
	}
}

// A benign link names its domain and says nothing more.
func TestLinkDialogBenign(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.askOpenLink("https://example.com/about")
	tt.Frame()
	if !tt.HasText("example.com") || !tt.HasText("https://example.com/about") {
		t.Fatalf("the destination is not shown: %q", tt.Texts())
	}
	if tt.HasText("Signs worth checking") {
		t.Errorf("a benign link warns: %q", tt.Texts())
	}
}

func TestLinkCanBeCancelled(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.askOpenLink("https://example.com")
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if a.linkOpen || a.linkURL != "" {
		t.Errorf("link still pending: open=%v url=%q", a.linkOpen, a.linkURL)
	}
}

// The security strings exist in both languages.
func TestSecurityStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, fs := sampleMessageApp(t)
	loadFirst(t, fs, a, "Authentication-Results: mx.example; dmarc=fail\r\n", nil)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	if !tt.HasText("发件人验证失败") {
		t.Errorf("missing Chinese verdict: %q", tt.Texts())
	}
	if tt.HasText("Sender check failed") {
		t.Errorf("English string in the Chinese view: %q", tt.Texts())
	}
}

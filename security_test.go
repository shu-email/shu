package main

import (
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

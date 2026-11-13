package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// promptTimer runs a scheduled action at once, as the user's typing
// pausing does.
func promptTimer(_ time.Duration, fn func()) *time.Timer {
	fn()
	return nil
}

// exampleDiscovery is what an autoconfig lookup finds for example.com.
func exampleDiscovery() mail.Discovery {
	return mail.Discovery{
		Domain: "example.com",
		IMAP:   mail.Server{Host: "imap.example.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP:   mail.Server{Host: "smtp.example.com", Port: 587, TLS: mail.TLSStartTLS},
		Source: mail.DiscoveryAutoconfig,
	}
}

// Entering an address fills the form with the servers its domain's
// lookup finds, and says where they came from; the manual form stays.
func TestAccountFormDiscoversServers(t *testing.T) {
	fs := newFakeService()
	fs.discovered["example.com"] = exampleDiscovery()
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 1200)
	a.openAccountForm(nil)
	a.after = promptTimer
	a.accountForm.Email = "ann@example.com"
	a.emailChanged()
	tt.Frame()
	f := a.accountForm
	if f.Host != "imap.example.com" || f.Port != "993" || f.TLS != "TLS" ||
		f.SMTPHost != "smtp.example.com" || f.SMTPPort != "587" || f.SMTPTLS != "STARTTLS" {
		t.Fatalf("the form = %+v", f)
	}
	if !tt.HasText(locales.DiscoverFound(locales.DiscoverSourceAutoconfig())) {
		t.Errorf("no word of the finding: %q", tt.Texts())
	}
	if len(fs.discoveredCalls) != 1 || fs.discoveredCalls[0] != "ann@example.com" {
		t.Errorf("lookups %v", fs.discoveredCalls)
	}
	// The server fields are still the user's to change.
	if !tt.HasText(locales.ImapSection()) || !tt.HasText(locales.SmtpSection()) {
		t.Errorf("the manual form went: %q", tt.Texts())
	}
}

// A known provider's servers fill the form without a lookup.
func TestAccountFormDiscoversPreset(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.openAccountForm(nil)
	a.accountForm.Email = "ann@fastmail.com"
	a.discoverSettings()
	tt.Frame()
	f := a.accountForm
	if f.Host != "imap.fastmail.com" || f.Port != "993" || f.TLS != "TLS" ||
		f.SMTPHost != "smtp.fastmail.com" || f.SMTPPort != "465" || f.SMTPTLS != "TLS" {
		t.Errorf("the form = %+v", f)
	}
	if !tt.HasText(locales.DiscoverFound(locales.DiscoverSourcePreset())) {
		t.Errorf("no word of the preset: %q", tt.Texts())
	}
	if f.Provider != "Fastmail" {
		t.Errorf("the provider on show is %q", f.Provider)
	}
	if len(fs.discoveredCalls) != 1 {
		t.Errorf("lookups %v", fs.discoveredCalls)
	}
}

// Servers the user entered after a lookup are not replaced by another,
// and the form says so.
func TestAccountFormKeepsManualServers(t *testing.T) {
	fs := newFakeService()
	fs.discovered["example.com"] = exampleDiscovery()
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 1200)
	a.openAccountForm(nil)
	a.after = promptTimer
	a.accountForm.Email = "ann@example.com"
	a.emailChanged()
	a.accountForm.SMTPHost = "send.ann.example"
	a.accountForm.SMTPPort = "465"
	a.serversChanged()
	a.discoverSettings()
	tt.Frame()
	if got := a.accountForm.SMTPHost; got != "send.ann.example" {
		t.Errorf("the user's server became %q", got)
	}
	if a.accountForm.Host != "imap.example.com" {
		t.Errorf("the found IMAP server was not kept: %q", a.accountForm.Host)
	}
	if !tt.HasText(locales.DiscoverManualKept()) {
		t.Errorf("no word of the kept entry: %q", tt.Texts())
	}
}

// A lookup that finds nothing says which sources it tried and why, and
// leaves the manual form there.
func TestAccountFormDiscoveryFails(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	fs.discoverErr = &mail.DiscoveryError{
		Domain: "unknown.example",
		Steps: []mail.DiscoveryStep{
			{Source: mail.DiscoveryAutoconfig, Err: "no configuration"},
			{Source: mail.DiscoverySRV, Err: "no record"},
		},
	}
	a.openAccountForm(nil)
	a.after = promptTimer
	a.accountForm.Email = "ann@unknown.example"
	a.emailChanged()
	tt.Frame()
	want := locales.DiscoverFailed(locales.DiscoverSourceAutoconfig() + ": no configuration; " +
		locales.DiscoverSourceSRV() + ": no record")
	if !tt.HasText(want) {
		t.Errorf("the refusal %q is not on screen: %q", want, tt.Texts())
	}
	if !tt.HasText(locales.ImapSection()) || !tt.HasText(locales.SmtpSection()) {
		t.Errorf("the manual form went: %q", tt.Texts())
	}
	// The Gmail servers the form opened with did not stay under another
	// domain's address.
	if f := a.accountForm; f.Host != "" || f.SMTPHost != "" {
		t.Errorf("another domain's servers stayed: %q and %q", f.Host, f.SMTPHost)
	}
}

// The servers the user entered are kept for the domain when the account
// is saved; those a lookup found are not written again.
func TestAccountFormRemembersManualServers(t *testing.T) {
	a, fs := sampleApp(t)
	a.openAccountForm(nil)
	f := &a.accountForm
	f.Name, f.Email, f.Password = "Ann", "ann@example.com", "secret"
	f.Host, f.Port, f.TLS = "imap.example.com", "993", "TLS"
	f.SMTPHost, f.SMTPPort, f.SMTPTLS = "smtp.example.com", "465", "TLS"
	a.serversChanged()
	a.saveAccount()
	if len(fs.remembered) != 1 {
		t.Fatalf("remembered %+v", fs.remembered)
	}
	if r := fs.remembered[0]; r.Domain != "example.com" || r.Source != mail.DiscoveryManual ||
		r.IMAP.Host != "imap.example.com" || r.SMTP.Host != "smtp.example.com" {
		t.Errorf("kept %+v", r)
	}
}

// A form whose servers a lookup filled keeps nothing more: the lookup
// already did.
func TestAccountFormDoesNotRemberFoundServers(t *testing.T) {
	a, fs := sampleApp(t)
	fs.discovered["example.com"] = exampleDiscovery()
	a.openAccountForm(nil)
	f := &a.accountForm
	f.Email = "ann@example.com"
	a.discoverSettings()
	f.Name, f.Password = "Ann", "secret"
	a.saveAccount()
	if len(fs.remembered) != 0 {
		t.Errorf("remembered %+v", fs.remembered)
	}
}

// The discovery's words read in Simplified Chinese.
func TestDiscoveryStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	if got := locales.DiscoverLooking("example.com"); got != "正在查找 example.com 的服务器…" {
		t.Errorf("looking = %q", got)
	}
	if got := locales.DiscoverSourceSRV(); got != "DNS 记录" {
		t.Errorf("source = %q", got)
	}
	if got := locales.DiscoverFound("autoconfig"); !strings.Contains(got, "已从autoconfig找到服务器") {
		t.Errorf("found = %q", got)
	}
	if got := locales.DiscoverManualKept(); !strings.Contains(got, "保留了") {
		t.Errorf("kept = %q", got)
	}
}

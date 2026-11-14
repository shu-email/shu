package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

func networkApp(t *testing.T) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	a, fs := sampleMessageApp(t)
	a.openSettings(settingsTabNetwork)
	return a, fs, ui.NewTester(a.view, 1220, 800)
}

// The Network tab shows the settings, saves a manual proxy with its
// password, and reports it.
func TestNetworkSettingsTab(t *testing.T) {
	a, fs, tt := networkApp(t)
	for _, text := range []string{locales.NetworkTitle(), locales.ProxySystem(), locales.ProxyDirect(), locales.ProxyManual()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if !a.networkForm.loaded || a.networkForm.proxyMode() != mail.ProxySystem {
		t.Fatalf("form = %+v", a.networkForm)
	}
	click(t, tt, locales.ProxyManual())
	focusField(t, tt, locales.ProxyAddress(), "socks5://proxy.example:1080")
	focusField(t, tt, locales.ProxyUsername(), "ann")
	focusField(t, tt, locales.ProxyPassword(), "secret")
	focusField(t, tt, locales.ProxyBypass(), "localhost, .internal.example")
	click(t, tt, locales.Save())
	if fs.networkSet.Mode != mail.ProxyManual || fs.networkSet.ProxyURL != "socks5://proxy.example:1080" ||
		fs.networkSet.ProxyUsername != "ann" || fs.networkSet.Bypass != "localhost, .internal.example" {
		t.Errorf("saved = %+v", fs.networkSet)
	}
	if !fs.networkStored {
		t.Error("the proxy password was not stored")
	}
	if a.networkForm.note != locales.NetworkSaved() {
		t.Errorf("note = %q, error %q", a.networkForm.note, a.networkForm.err)
	}
	// The password field is cleared and the form knows the keychain
	// keeps it.
	if a.networkForm.password != "" || !a.networkForm.passwordStored {
		t.Errorf("password = %q, stored = %v", a.networkForm.password, a.networkForm.passwordStored)
	}
}

func TestNetworkSaveFailureStaysVisible(t *testing.T) {
	a, fs, tt := networkApp(t)
	fs.networkErr = errors.New("the keychain refused the password")
	click(t, tt, locales.ProxyManual())
	click(t, tt, locales.Save())
	if !strings.Contains(a.networkForm.err, "the keychain refused the password") {
		t.Fatalf("error = %q", a.networkForm.err)
	}
	if !tt.HasText(a.networkForm.err) {
		t.Errorf("the tab does not show why: %q", tt.Texts())
	}
}

func TestNetworkTestUsesTheAccount(t *testing.T) {
	a, fs, tt := networkApp(t)
	click(t, tt, locales.NetworkTest())
	if a.networkForm.note != locales.NetworkTestOk() {
		t.Fatalf("note = %q, error %q", a.networkForm.note, a.networkForm.err)
	}
	fs.testErr = errors.New("proxy refused the tunnel")
	click(t, tt, locales.NetworkTest())
	if !strings.Contains(a.networkForm.err, "proxy refused the tunnel") {
		t.Errorf("error = %q", a.networkForm.err)
	}
}

func TestNetworkWithoutAnAccount(t *testing.T) {
	a, _ := sampleMessageApp(t)
	a.accounts = nil
	a.openSettings(settingsTabNetwork)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.NetworkTest())
	if a.networkForm.err != locales.NetworkNoAccount() {
		t.Errorf("error = %q", a.networkForm.err)
	}
}

func TestNetworkStringsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, _ := sampleMessageApp(t)
	a.openSettings(settingsTabNetwork)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	for _, text := range []string{"网络", "跟随系统", "使用代理", "测试连接"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
}

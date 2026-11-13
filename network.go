package main

import (
	"crypto/x509"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The network settings: the proxy every connection goes through, and a
// certificate authority to trust besides the system's. Saving writes the
// store and the keychain; a change applies to the next connection, and
// the AI layer's client is rebuilt at once.

// networkForm is the Network tab's editable state.
type networkForm struct {
	loaded bool
	set    mail.NetworkSettings
	// modeChoice is the proxy mode as the radio group shows it.
	modeChoice int
	password   string
	// passwordStored says a password is in the keychain; leaving the
	// field empty keeps it.
	passwordStored bool
	caName         string
	busy           bool
	note           string
	err            string
}

var networkModes = []mail.ProxyMode{mail.ProxySystem, mail.ProxyDirect, mail.ProxyManual}

// loadNetworkSettings reads the settings into the tab.
func (a *app) loadNetworkSettings() {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		set, stored, err := a.svc.NetworkSettings(ctx)
		a.post(func() {
			f := &a.networkForm
			f.set, f.passwordStored, f.err = set, stored, ""
			f.modeChoice = modeIndex(set.Mode)
			f.loaded = true
			if err != nil {
				f.err = failureText(err)
			}
		})
	})
}

func modeIndex(mode mail.ProxyMode) int {
	for i, m := range networkModes {
		if m == mode {
			return i
		}
	}
	return 0
}

// proxyMode returns the mode the radio group chose.
func (f *networkForm) proxyMode() mail.ProxyMode {
	if f.modeChoice < 0 || f.modeChoice >= len(networkModes) {
		return mail.ProxySystem
	}
	return networkModes[f.modeChoice]
}

// saveNetworkSettings writes the tab. The password leaves the form for
// the keychain; an empty one keeps what is stored.
func (a *app) saveNetworkSettings() {
	f := &a.networkForm
	if f.busy {
		return
	}
	f.busy, f.note, f.err = true, "", ""
	set := f.set
	set.Mode = f.proxyMode()
	password, setPassword := f.password, f.password != ""
	f.password = ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SaveNetworkSettings(ctx, set, password, setPassword)
		var clientErr error
		if err == nil && a.ai != nil {
			c, cerr := a.svc.HTTPClient(ctx)
			clientErr = cerr
			if cerr == nil {
				a.ai.SetHTTPClient(c)
			}
		}
		a.post(func() {
			f.busy = false
			switch {
			case err != nil:
				f.err = locales.NetworkSaveFailed(failureText(err))
			case clientErr != nil:
				f.err = locales.NetworkSaveFailed(failureText(clientErr))
			default:
				f.note = locales.NetworkSaved()
				f.passwordStored = f.passwordStored || setPassword
				a.loadNetworkSettings()
			}
		})
	})
}

// testNetwork checks the same path real mail takes, with the account on
// screen.
func (a *app) testNetwork() {
	account := a.active
	if account == "" || a.account(account) == nil {
		a.networkForm.note, a.networkForm.err = "", locales.NetworkNoAccount()
		return
	}
	f := &a.networkForm
	f.busy, f.note, f.err = true, "", ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.TestNetwork(ctx, account)
		a.post(func() {
			f.busy = false
			if err != nil {
				f.err = locales.NetworkTestFailed(failureText(err))
			} else {
				f.note = locales.NetworkTestOk()
			}
		})
	})
}

// importCA asks for a PEM file and keeps it as the trusted authority.
func (a *app) importCA() {
	if a.pickFiles == nil {
		return
	}
	paths, err := a.pickFiles(a.win)
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		a.networkForm.err = locales.CaInvalid()
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		a.networkForm.err = locales.CaInvalid()
		return
	}
	f := &a.networkForm
	f.set.CA = string(data)
	f.caName = fileBase(paths[0])
	f.err = ""
	f.note = locales.CaImported(f.caName)
}

// networkSettingsView draws the Network tab.
func (a *app) networkSettingsView(c *ui.Context, t *ui.Theme) {
	f := &a.networkForm
	if !f.loaded {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.Loading()).FontSize(13).TextColor(t.TextMuted)
		})
		return
	}
	ui.Text(c, locales.NetworkTitle()).FontSize(15).Bold()
	ui.Text(c, locales.NetworkIntro()).FontSize(12).TextColor(t.TextMuted).MaxLines(3)

	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, locales.ProxyMode()).FontSize(12).Bold().TextColor(t.TextMuted)
		ui.RadioGroup(c, func() {
			for i, label := range []string{locales.ProxySystem(), locales.ProxyDirect(), locales.ProxyManual()} {
				ui.RadioBase(c, &f.modeChoice, i).Label(label).Children(func() { ui.Text(c, label) })
			}
		}).Row().Gap(16)
	})
	if f.proxyMode() == mail.ProxyManual {
		ui.Column(c).Gap(12).Children(func() {
			networkField(c, t, locales.ProxyAddress(), &f.set.ProxyURL, locales.ProxyAddressHint(), false)
			networkField(c, t, locales.ProxyUsername(), &f.set.ProxyUsername, "", false)
			networkField(c, t, locales.ProxyPassword(), &f.password, passwordHint(f.passwordStored), true)
			networkField(c, t, locales.ProxyBypass(), &f.set.Bypass, locales.ProxyBypassHint(), false)
		})
	}

	ui.Divider(c)
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, locales.CertificateAuthority()).FontSize(12).Bold().TextColor(t.TextMuted)
		ui.Text(c, locales.CertificateAuthorityHint()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c, locales.ImportCA()).OnClick(a.importCA)
			if strings.TrimSpace(f.set.CA) != "" {
				name := f.caName
				if name == "" {
					name = locales.CertificateAuthority()
				}
				ui.Text(c, name).FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0).SingleLine()
				ui.Button(c, locales.RemoveCA()).OnClick(func() {
					f.set.CA, f.caName = "", ""
				})
			}
		})
	})

	if f.err != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
		})
	}
	if f.note != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
			ui.Text(c, f.note).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
		})
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if f.busy {
			ui.Spinner(c).Size(14, 14)
		}
		ui.Button(c, locales.NetworkTest()).Disabled(f.busy).OnClick(a.testNetwork)
		ui.Spacer(c)
		ui.PrimaryButton(c, locales.Save()).Disabled(f.busy).OnClick(a.saveNetworkSettings)
	})
}

// networkField is one labelled text field of the tab.
func networkField(c *ui.Context, t *ui.Theme, label string, value *string, hint string, password bool) {
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, label).FontSize(11).Bold().TextColor(t.TextMuted)
		input := ui.TextInput(c, value).LabelledBy(caption).Placeholder(hint).FillWidth()
		if password {
			input.Password()
		}
	})
}

func passwordHint(stored bool) string {
	if stored {
		return locales.ProxyPasswordStored()
	}
	return ""
}

func fileBase(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

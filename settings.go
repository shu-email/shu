package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
)

// The tabs of the settings view.
const (
	settingsTabGeneral = iota
	settingsTabNetwork
	settingsTabAI
)

// aiSettings is the AI tab's editable state. Changes apply at once, as
// the switches of a settings pane do; the typed key is the exception, and
// leaves this struct as soon as the keychain has it.
type aiSettings struct {
	loaded   bool
	cfg      ai.Config
	provider string
	// features and models mirror the per-capability maps of cfg, as
	// controls bind to fields rather than to map entries.
	features [3]bool
	models   [3]string

	key       string
	keyStored bool
	busy      bool
	note      string
	err       string

	// Local command-line providers: the explicit path, the check in
	// flight and its result, without a model request.
	cliPath   string
	cliBusy   bool
	cliStatus ai.CLIStatus
	cliModels []string
}

// settingsWindowOptions is the settings window: its own, as a system
// settings window is, so that mail stays readable behind it.
func (a *app) settingsWindowOptions() mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:           locales.Settings(),
		Width:           900,
		Height:          720,
		MinWidth:        760,
		MinHeight:       560,
		TitleBarStyle:   mygo.TitleBarHiddenInset,
		BackgroundColor: "light-dark(#f6f6f7, #18181b)",
		Content:         ui.View(a.settingsView),
	}
}

// openSettings shows the settings window on a tab. One window serves
// every opening: a second call brings the open one forward.
func (a *app) openSettings(tab int) {
	a.settingsTab = tab
	a.loadAISettings()
	if tab == settingsTabNetwork {
		a.loadNetworkSettings()
	}
	if a.showSettings && a.settingsWin != nil {
		a.settingsWin.Show()
		a.settingsWin.Focus()
		a.settingsWin.Invalidate()
		return
	}
	a.showSettings = true
	if a.newWindow == nil {
		// Tests run the view without a window: the settings show in the
		// main view.
		return
	}
	win := a.newWindow(a.settingsWindowOptions())
	if win == nil {
		return
	}
	a.settingsWin = win
	win.OnClosed(func() {
		if a.settingsWin == win {
			a.settingsWin = nil
		}
		a.showSettings = false
	})
}

// closeSettings closes the settings window, or leaves the inline view.
func (a *app) closeSettings() {
	a.showSettings = false
	if win := a.settingsWin; win != nil {
		a.settingsWin = nil
		win.Close()
	}
}

// aiProviderName is a provider's name in the settings.
func aiProviderName(p ai.Provider) string {
	switch p.ID {
	case ai.ProviderLocal:
		return locales.AiProviderLocal()
	case ai.ProviderCustom:
		return locales.AiProviderCustom()
	}
	return p.Name
}

func aiProviderNames() []string {
	out := make([]string, 0, len(ai.Providers))
	for _, p := range ai.Providers {
		out = append(out, aiProviderName(p))
	}
	return out
}

// setConfig fills the form from saved settings.
func (f *aiSettings) setConfig(cfg ai.Config) {
	f.cfg = cfg.Clone()
	if p, ok := ai.ProviderByID(cfg.Provider); ok {
		f.provider = aiProviderName(p)
	}
	for i, feature := range ai.Features {
		f.features[i] = cfg.FeatureOn(feature)
		f.models[i] = cfg.Models[feature]
	}
	f.cliPath = cfg.CLIPath(cfg.Provider)
	f.cliStatus = ai.CLIStatus{}
}

// config reads the form back into settings to save.
func (f *aiSettings) config() ai.Config {
	for i, feature := range ai.Features {
		f.cfg.SetFeature(feature, f.features[i])
		f.cfg.SetModelFor(feature, f.models[i])
	}
	return f.cfg.Clone()
}

// loadAISettings reads the AI settings and whether a key is stored.
func (a *app) loadAISettings() {
	if a.ai == nil {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		cfg, err := a.ai.Config(ctx)
		stored := false
		if err == nil {
			stored, err = a.ai.KeyStored(cfg.Provider)
		}
		a.post(func() {
			f := &a.aiForm
			f.setConfig(cfg)
			f.keyStored = stored
			f.loaded = true
			f.err = ""
			if err != nil {
				f.err, _ = aiErrorText(err)
			}
			if ai.CLIProvider(f.cfg.Provider) {
				a.loadCLIStatus()
			}
		})
	})
}

// saveAI writes the form's settings. Every control of the tab calls it,
// so what the tab shows is what is saved.
func (a *app) saveAI() {
	f := &a.aiForm
	cfg := f.config()
	// A text field saves on every keystroke, each in a job of its own.
	// The jobs take turns, and one that a later save has overtaken does
	// not write, so the last thing typed is the last thing saved.
	seq := a.aiSaveSeq.Add(1)
	a.run(func() {
		a.aiSaveMu.Lock()
		defer a.aiSaveMu.Unlock()
		if a.aiSaveSeq.Load() != seq {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		if err := a.ai.SaveConfig(ctx, cfg); err != nil {
			a.post(func() { f.err, _ = aiErrorText(err) })
		}
	})
}

// chooseAIProvider switches provider, which changes the endpoint, the
// suggested model and which key applies.
func (a *app) chooseAIProvider() {
	f := &a.aiForm
	for _, p := range ai.Providers {
		if aiProviderName(p) != f.provider {
			continue
		}
		if p.ID == f.cfg.Provider {
			return
		}
		f.cfg.UseProvider(p.ID)
		if p.EditableURL {
			f.cfg.BaseURL = p.BaseURL
		}
		f.models = [3]string{}
		f.key, f.note, f.err = "", "", ""
		f.keyStored = false
		f.cliPath = f.cfg.CLIPath(p.ID)
		f.cliStatus = ai.CLIStatus{}
		f.cliModels = nil
		a.saveAI()
		if ai.CLIProvider(p.ID) {
			a.loadCLIStatus()
		}
		id := p.ID
		a.run(func() {
			stored, err := a.ai.KeyStored(id)
			a.post(func() {
				if f.cfg.Provider != id {
					return
				}
				f.keyStored = stored
				if err != nil {
					f.err, _ = aiErrorText(err)
				}
			})
		})
		return
	}
}

// saveAIKey moves the typed key into the keychain and forgets it.
func (a *app) saveAIKey() {
	f := &a.aiForm
	key, id := strings.TrimSpace(f.key), f.cfg.Provider
	if key == "" {
		return
	}
	f.key = ""
	a.run(func() {
		err := a.ai.SetKey(id, key)
		a.post(func() {
			if f.cfg.Provider != id {
				return
			}
			if err != nil {
				f.note = ""
				f.err, _ = aiErrorText(err)
				return
			}
			f.keyStored = true
			f.err, f.note = "", locales.AiKeySaved()
		})
	})
}

// deleteAIKey removes the provider's key from the keychain.
func (a *app) deleteAIKey() {
	f := &a.aiForm
	id := f.cfg.Provider
	a.run(func() {
		err := a.ai.DeleteKey(id)
		a.post(func() {
			if f.cfg.Provider != id {
				return
			}
			if err != nil {
				f.note = ""
				f.err, _ = aiErrorText(err)
				return
			}
			f.keyStored = false
			f.err, f.note = "", locales.AiKeyDeleted()
		})
	})
}

// testAI checks the provider settings with a request that carries no
// mail. A key typed but not yet saved is the one tested.
func (a *app) testAI() {
	f := &a.aiForm
	if f.busy {
		return
	}
	cfg, key := f.config(), strings.TrimSpace(f.key)
	f.busy, f.note, f.err = true, "", ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.ai.Test(ctx, cfg, key)
		a.post(func() {
			f.busy = false
			if err != nil {
				f.err, _ = aiErrorText(err)
				return
			}
			f.note = locales.AiTestOK(strings.TrimSpace(cfg.Model))
		})
	})
}

// settingsView draws the settings window: a title, an icon tab bar as a
// system settings window has, and the chosen tab below it.
func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	head := max(bar.Bottom, 34)
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Column(c).Height(head).Shrink(0).Center().DragWindow().Children(func() {
			ui.Text(c, locales.Settings()).FontSize(13).Bold()
		})
		ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Padding(2, 20, 10, 20).
			DragWindow().Children(func() {
			ui.Spacer(c)
			a.settingsTabButton(c, t, settingsIcon, locales.General(), a.settingsTab == settingsTabGeneral, func() {
				a.settingsTab = settingsTabGeneral
			})
			a.settingsTabButton(c, t, globeIcon, locales.NetworkTab(), a.settingsTab == settingsTabNetwork, func() {
				a.settingsTab = settingsTabNetwork
				a.loadNetworkSettings()
			})
			if a.ai != nil {
				a.settingsTabButton(c, t, sparklesIcon, locales.Ai(), a.settingsTab == settingsTabAI, func() {
					a.settingsTab = settingsTabAI
					a.loadAISettings()
				})
			}
			ui.Spacer(c)
		})
		ui.Divider(c)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(22, 36, 32, 36).Gap(16).MaxWidth(760).Children(func() {
				switch {
				case a.settingsTab == settingsTabAI && a.ai != nil:
					a.aiSettingsView(c, t)
				case a.settingsTab == settingsTabNetwork:
					a.networkSettingsView(c, t)
				default:
					ui.Text(c, locales.Language()).FontSize(12).Bold().TextColor(t.TextMuted)
					a.languageChoice(c).Width(320)
					a.mailSettingsView(c, t)
					a.composeSettingsView(c, t)
					a.transferSettingsView(c, t)
					ui.Divider(c)
					ui.Row(c).Gap(10).Children(func() {
						ui.Button(c, locales.ManageAccounts()).OnClick(func() {
							a.closeSettings()
							a.openAccountForm(a.activeAccount())
						})
					})
				}
				if a.settingsWin == nil {
					// Without a window, as in tests, Done is the way back.
					ui.Row(c).Children(func() {
						ui.Spacer(c)
						ui.Button(c, locales.Done()).OnClick(a.closeSettings)
					})
				}
			})
		})
	})
}

// settingsTabButton is one icon and label of the settings window's tab
// bar, filled with the accent while its tab shows.
func (a *app) settingsTabButton(c *ui.Context, t *ui.Theme, icon *ui.SVG, label string, chosen bool, fn func()) {
	color := t.TextMuted
	background := ui.Transparent
	if chosen {
		color = t.Accent
		background = t.Accent.Alpha(0.12)
	}
	ui.ButtonBase(c).Label(label).OnClick(fn).Width(104).Padding(7, 8).Radius(9).
		Background(background).Children(func() {
		ui.Column(c).Gap(5).Center().Children(func() {
			ui.Icon(c, icon).FontSize(18).TextColor(color)
			ui.Text(c, label).FontSize(11).TextColor(color)
		})
	})
}

// aiSettingsView draws the AI tab: the master switch, the provider and
// its key, and a switch for each capability.
func (a *app) aiSettingsView(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	if !f.loaded {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.Loading()).FontSize(13).TextColor(t.TextMuted)
		})
		return
	}
	provider, _ := ai.ProviderByID(f.cfg.Provider)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, sparklesIcon).FontSize(20).TextColor(t.Accent)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, locales.AiTitle()).FontSize(15).Bold()
			ui.Text(c, locales.AiIntro()).FontSize(12).TextColor(t.TextMuted)
		})
		ui.Switch(c, &f.cfg.Enabled).Label(locales.AiEnable()).OnChange(a.saveAI)
	})

	a.section(c, locales.Provider(), func() {
		ui.Select(c, &f.provider, aiProviderNames()).FillWidth().Label(locales.Provider()).
			OnChange(a.chooseAIProvider)
		if ai.CLIProvider(f.cfg.Provider) {
			a.aiLocalToolSection(c, t)
			return
		}
		if provider.EditableURL {
			a.field(c, locales.AiBaseURL(), func() ui.Element {
				return ui.TextInput(c, &f.cfg.BaseURL).Label(locales.AiBaseURL()).
					Placeholder("http://localhost:11434/v1").OnChange(a.saveAI)
			})
		}
		if ai.NeedsHTTPOptIn(f.cfg) {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 10).Radius(8).
				Background(t.Warning.Alpha(0.12)).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				var caption ui.Element
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, locales.AiHTTPWarning()).FontSize(12)
					caption = ui.Text(c, locales.AiAllowHTTP()).FontSize(12).Bold()
				})
				// The caption names the switch and toggles it when clicked.
				ui.Switch(c, &f.cfg.AllowHTTP).LabelledBy(caption).OnChange(a.saveAI)
			})
		}
		a.field(c, locales.AiModel(), func() ui.Element {
			return ui.TextInput(c, &f.cfg.Model).Label(locales.AiModel()).
				Placeholder(provider.Model).OnChange(a.saveAI)
		})
		ui.Column(c).Gap(4).Children(func() {
			// The caption names the input, so a click on it focuses it.
			caption := ui.Text(c, locales.AiKey()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				placeholder := locales.AiKeyPlaceholder()
				if f.keyStored {
					placeholder = locales.AiKeyReplace()
				}
				ui.TextInput(c, &f.key).Password().Grow(1).LabelledBy(caption).
					Placeholder(placeholder).OnSubmit(a.saveAIKey)
				ui.Button(c, locales.AiKeySave()).Disabled(strings.TrimSpace(f.key) == "").
					OnClick(a.saveAIKey)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				switch {
				case f.keyStored:
					ui.Icon(c, checkIcon).FontSize(13).TextColor(t.Success)
					ui.Text(c, locales.AiKeyStored()).FontSize(11).TextColor(t.TextMuted).Grow(1)
					ui.Button(c, locales.AiKeyDelete()).OnClick(a.deleteAIKey)
				case provider.NeedsKey:
					ui.Text(c, locales.AiKeyMissing()).FontSize(11).TextColor(t.TextMuted)
				default:
					ui.Text(c, locales.AiKeyOptional()).FontSize(11).TextColor(t.TextMuted)
				}
			})
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := locales.AiTest()
			if f.busy {
				label = locales.AiTesting()
			}
			ui.Button(c, label).Label(locales.AiTest()).Disabled(f.busy).OnClick(a.testAI)
			switch {
			case f.busy:
				ui.Spinner(c).Size(14, 14)
			case f.err != "":
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, f.err).FontSize(12).Grow(1).MaxLines(2)
			case f.note != "":
				ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
				ui.Text(c, f.note).FontSize(12).TextColor(t.TextMuted).Grow(1)
			}
		})
	})

	a.section(c, locales.AiCapabilities(), func() {
		details := [3][2]string{
			{locales.AiSummaries(), locales.AiSummariesDetail(ai.BodyLimit, ai.ConversationLimit)},
			{locales.AiTriage(), locales.AiTriageDetail(ai.TriageLimit)},
			{locales.AiDrafts(), locales.AiDraftsDetail()},
		}
		for i := range ai.Features {
			title, detail := details[i][0], details[i][1]
			ui.Column(c.Key(string(ai.Features[i]))).Gap(6).Children(func() {
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					var caption ui.Element
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						caption = ui.Text(c, title).FontSize(13).Bold()
						ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted)
					})
					ui.Switch(c, &f.features[i]).LabelledBy(caption).OnChange(a.saveAI)
				})
				if f.features[i] {
					ui.TextInput(c, &f.models[i]).Width(320).Label(title + " · " + locales.AiFeatureModel()).
						Placeholder(locales.AiFeatureModel()).OnChange(a.saveAI)
				}
			})
		}
		ui.Text(c, locales.AiNeverSent()).FontSize(11).TextColor(t.TextMuted)
	})
}

// loadCLIStatus checks the selected local tool with its own version and
// sign-in commands: no prompt, no model request.
func (a *app) loadCLIStatus() {
	f := &a.aiForm
	id := f.cfg.Provider
	path := f.cfg.CLIPath(id)
	f.cliBusy, f.cliStatus = true, ai.CLIStatus{}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		status := a.ai.CLIStatus(ctx, id, path)
		var models []string
		if status.Installed && status.SignedIn {
			models, _ = a.ai.CLIModels(ctx, id, path)
		}
		a.post(func() {
			if f.cfg.Provider != id {
				return
			}
			f.cliBusy = false
			f.cliStatus = status
			f.cliModels = models
		})
	})
}

// aiLocalToolSection shows the selected tool's status, its command path
// and how it is kept from acting.
func (a *app) aiLocalToolSection(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	p, _ := ai.ProviderByID(f.cfg.Provider)
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AiModel()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Autocomplete(c, &f.cfg.Model, f.cliModels).LabelledBy(caption).
			Placeholder(p.Model).FillWidth().OnChange(a.saveAI)
	})
	ui.Text(c, locales.AiLocalToolIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(4).Role(ui.RoleStatus)
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AiLocalToolPath()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.TextInput(c, &f.cliPath).LabelledBy(caption).Placeholder(locales.AiLocalToolPathHint()).
			FillWidth().OnChange(func() {
			f.cfg.SetCLIPath(p.ID, f.cliPath)
			a.saveAI()
		})
	})
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		if f.cliBusy {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.AiLocalToolChecking()).FontSize(12).TextColor(t.TextMuted).Grow(1)
		} else {
			status, icon, color := cliStatusText(f.cliStatus, p, t)
			ui.Icon(c, icon).FontSize(14).TextColor(color)
			ui.Text(c, status).FontSize(12).TextColor(t.Text).Grow(1).MinWidth(0).MaxLines(2)
			ui.Button(c, locales.AiLocalToolCheck()).Disabled(f.cliBusy).OnClick(a.loadCLIStatus)
		}
	})
	if f.cliStatus.Path != "" {
		ui.Text(c, f.cliStatus.Path).FontSize(10).TextColor(t.TextMuted).Selectable().SingleLine()
	}
	ui.Text(c, locales.AiLocalToolIsolation()).FontSize(11).TextColor(t.TextMuted).MaxLines(4)
}

// cliStatusText is the one line a tool's check shows.
func cliStatusText(status ai.CLIStatus, p ai.Provider, t *ui.Theme) (string, *ui.SVG, ui.Color) {
	if !status.Installed {
		if status.Err != "" {
			return status.Err, alertIcon, t.Warning
		}
		return locales.AiLocalToolNotInstalled(), alertIcon, t.Warning
	}
	if !status.SignedIn {
		return locales.AiLocalToolSignIn(), alertIcon, t.Warning
	}
	text := locales.AiLocalToolReady()
	if status.Version != "" {
		text = locales.AiLocalToolVersion(status.Version)
	}
	return text, checkIcon, t.Success
}

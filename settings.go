package main

import (
	"strings"

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
}

// openSettings shows the settings view on a tab.
func (a *app) openSettings(tab int) {
	a.showSettings = true
	a.settingsTab = tab
	a.loadAISettings()
	if tab == settingsTabNetwork {
		a.loadNetworkSettings()
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
		a.saveAI()
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

// settingsView is the settings window: the language, receiving mail,
// writing and sending, and accounts, and the AI settings on a tab of
// their own.
func (a *app) settingsView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			ui.Text(c, locales.Settings()).FontSize(17).Bold()
			ui.Spacer(c)
			ui.Button(c, locales.Done()).OnClick(func() { a.showSettings = false })
		})
		ui.Row(c).Shrink(0).Padding(10, 32, 0, 32).Children(func() {
			labels := []string{locales.General(), locales.NetworkTab()}
			if a.ai != nil {
				labels = append(labels, locales.Ai())
			}
			ui.Tabs(c, &a.settingsTab, labels...).Label(locales.Settings()).
				OnChange(func() {
					if a.settingsTab == settingsTabAI {
						a.loadAISettings()
					}
					if a.settingsTab == settingsTabNetwork {
						a.loadNetworkSettings()
					}
				})
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(20, 32, 28, 32).Gap(14).MaxWidth(680).Children(func() {
				if a.settingsTab == settingsTabAI && a.ai != nil {
					a.aiSettingsView(c, t)
					return
				}
				if a.settingsTab == settingsTabNetwork {
					a.networkSettingsView(c, t)
					return
				}
				ui.Text(c, locales.Language()).FontSize(12).Bold().TextColor(t.TextMuted)
				a.languageChoice(c).Width(320)
				a.mailSettingsView(c, t)
				a.composeSettingsView(c, t)
				a.transferSettingsView(c, t)
				ui.Divider(c)
				ui.Row(c).Gap(10).Children(func() {
					ui.Button(c, locales.ManageAccounts()).OnClick(func() {
						a.showSettings = false
						a.openAccountForm(a.activeAccount())
					})
				})
			})
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

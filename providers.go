package main

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
)

// The providers panel: a list of API connections and installed tools on
// the left, and the chosen provider's Overview, Models and Advanced on
// the right, in the shape of a system settings panel.

type providersPanel struct {
	open     bool
	tab      int
	selected ai.ProviderID
	// statuses is what each installed tool reported, and keys whether a
	// stored API key exists, by provider.
	statuses map[ai.ProviderID]ai.CLIStatus
	keys     map[ai.ProviderID]bool
	loaded   bool
}

// openProviders shows the panel on the active provider.
func (a *app) openProviders() {
	p := &a.providers
	p.open = true
	p.tab = 0
	p.selected = a.aiForm.cfg.Provider
	p.loaded = false
	a.loadProviderStates()
}

// loadProviderStates checks every installed tool and API key without any
// model request.
func (a *app) loadProviderStates() {
	p := &a.providers
	cfg := a.aiForm.cfg.Clone()
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		statuses := map[ai.ProviderID]ai.CLIStatus{}
		keys := map[ai.ProviderID]bool{}
		for _, preset := range ai.Providers {
			if ai.CLIProvider(preset.ID) {
				statuses[preset.ID] = a.ai.CLIStatus(ctx, preset.ID, cfg.CLIPath(preset.ID))
				continue
			}
			if preset.NeedsKey {
				stored, _ := a.ai.KeyStored(preset.ID)
				keys[preset.ID] = stored
			}
		}
		a.post(func() {
			p.statuses, p.keys, p.loaded = statuses, keys, true
		})
	})
}

// providersSummary is the AI tab's single row into the panel.
func (a *app) providersSummary(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	p, _ := ai.ProviderByID(f.cfg.Provider)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, aiProviderName(p)).FontSize(13).Bold()
			ui.Text(c, a.providerStatusLine(f.cfg.Provider)).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
		ui.Button(c, locales.AiManageProviders()).OnClick(a.openProviders)
	})
}

// providerStatusLine says a provider's state in one line.
func (a *app) providerStatusLine(id ai.ProviderID) string {
	preset, ok := ai.ProviderByID(id)
	if !ok {
		return ""
	}
	if ai.CLIProvider(id) {
		status := a.providers.statuses[id]
		if !a.providers.loaded {
			return locales.AiLocalToolChecking()
		}
		switch {
		case !status.Installed:
			if status.Err != "" {
				return status.Err
			}
			return locales.AiLocalToolNotInstalled()
		case !status.SignedIn:
			return locales.AiLocalToolSignIn()
		case status.Version != "":
			return locales.AiLocalToolVersion(status.Version)
		}
		return locales.AiLocalToolReady()
	}
	if preset.NeedsKey && !a.providers.keys[id] {
		return locales.AiKeyMissing()
	}
	if preset.ID == ai.ProviderLocal {
		return locales.AiProviderApi()
	}
	return preset.BaseURL
}

// selectProvider makes a provider the active one and loads its state.
func (a *app) selectProvider(id ai.ProviderID) {
	a.providers.selected = id
	preset, ok := ai.ProviderByID(id)
	if !ok {
		return
	}
	a.aiForm.provider = aiProviderName(preset)
	a.chooseAIProvider()
	a.loadProviderStates()
}

// providersDialog draws the panel.
func (a *app) providersDialog(c *ui.Context) {
	p := &a.providers
	if !p.open {
		return
	}
	t := c.Theme()
	ui.Modal(c, &p.open, func() {
		ui.Column(c).Width(880).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.AiProvidersTitle()).FontSize(17).Bold().Grow(1)
				ui.Button(c, locales.Done()).OnClick(func() { p.open = false })
			})
			ui.Text(c, locales.AiProvidersIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			ui.Divider(c)
			ui.Row(c).Height(540).Gap(0).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).Width(290).Shrink(0).Children(func() {
					ui.Scroll(c).Grow(1).Children(func() {
						ui.Column(c).Padding(2, 2).Gap(2).Children(func() {
							a.providerGroup(c, t, locales.AiProviderApi(), false)
							a.providerGroup(c, t, locales.AiLocalTool(), true)
						})
					})
				})
				ui.Box(c).Width(1).Shrink(0).Background(t.Border)
				ui.Column(c).Grow(1).MinWidth(0).Padding(14, 18).Gap(10).Children(func() {
					ui.Tabs(c, &p.tab, locales.AiProviderOverview(), locales.AiProviderModels(), locales.AiProviderAdvanced()).Label(locales.AiProvidersTitle())
					switch p.tab {
					case 1:
						a.providerModels(c, t)
					case 2:
						a.providerAdvanced(c, t)
					default:
						a.providerOverview(c, t)
					}
				})
			})
			if !p.loaded {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(12, 12)
					ui.Text(c, locales.AiLocalToolChecking()).FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
}

// providerGroup lists the providers of one kind.
func (a *app) providerGroup(c *ui.Context, t *ui.Theme, title string, cli bool) {
	p := &a.providers
	ui.Text(c, title).FontSize(11).Bold().TextColor(t.TextMuted).Padding(8, 8)
	for _, preset := range ai.Providers {
		if ai.CLIProvider(preset.ID) != cli {
			continue
		}
		preset := preset
		selected := p.selected == preset.ID
		bg := ui.Transparent
		if selected {
			bg = t.Accent.Alpha(0.12)
		}
		ui.ButtonBase(c).Label(aiProviderName(preset)).Width(280).Padding(7, 8).Radius(8).
			Background(bg).OnClick(func() { a.selectProvider(preset.ID) }).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					name := aiProviderName(preset)
					ui.Text(c, name).FontSize(12).Bold().SingleLine()
					ui.Text(c, a.providerStatusLine(preset.ID)).FontSize(10).TextColor(t.TextMuted).SingleLine()
				})
				if preset.ID == a.aiForm.cfg.Provider {
					ui.Icon(c, checkIcon).FontSize(12).TextColor(t.Accent)
				}
			})
		})
	}
}

// providerOverview is the provider's status, command or endpoint, and the
// action that makes it the active one.
func (a *app) providerOverview(c *ui.Context, t *ui.Theme) {
	p := &a.providers
	preset, _ := ai.ProviderByID(p.selected)
	ui.Text(c, aiProviderName(preset)).FontSize(15).Bold()
	if p.selected == a.aiForm.cfg.Provider {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(12).TextColor(t.Accent)
			ui.Text(c, locales.AiProviderInUse()).FontSize(11).TextColor(t.Accent)
		})
	} else {
		ui.Button(c, locales.AiProviderUse()).OnClick(func() { a.selectProvider(p.selected) })
	}
	ui.Text(c, a.providerStatusLine(p.selected)).FontSize(12).TextColor(t.Text).MaxLines(3)
	if ai.CLIProvider(p.selected) {
		status := p.statuses[p.selected]
		if status.Path != "" {
			ui.Text(c, status.Path).FontSize(11).TextColor(t.TextMuted).Selectable().SingleLine()
		}
		ui.Text(c, locales.AiLocalToolIsolation()).FontSize(11).TextColor(t.TextMuted).MaxLines(5)
		ui.Text(c, locales.AiLocalToolIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(4)
		return
	}
	ui.Text(c, preset.BaseURL).FontSize(11).TextColor(t.TextMuted).Selectable().SingleLine()
	switch {
	case a.providers.keys[p.selected]:
		ui.Text(c, locales.AiKeyStored()).FontSize(11).TextColor(t.TextMuted)
	case preset.NeedsKey:
		ui.Text(c, locales.AiKeyMissing()).FontSize(11).TextColor(t.TextMuted)
	default:
		ui.Text(c, locales.AiKeyOptional()).FontSize(11).TextColor(t.TextMuted)
	}
}

// providerModels edits the chosen model and, for an installed tool, which
// of its reported models appear in the pickers.
func (a *app) providerModels(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	p := &a.providers
	if p.selected != f.cfg.Provider {
		ui.Text(c, locales.AiProviderUse()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	preset, _ := ai.ProviderByID(p.selected)
	suggestions := a.visibleCLIModels()
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AiModel()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Autocomplete(c, &f.cfg.Model, suggestions).LabelledBy(caption).
			Placeholder(preset.Model).FillWidth().OnChange(a.saveAI)
	})
	if !ai.CLIProvider(p.selected) {
		ui.Text(c, locales.AiProviderModelHint()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	if len(f.cliModels) == 0 {
		ui.Text(c, locales.AiProviderNoModels()).FontSize(11).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, locales.AiProviderModelDefault()).FontSize(11).TextColor(t.TextMuted)
	for _, model := range f.cliModels {
		model := model
		shown := !f.cfg.ModelHidden(p.selected, model)
		isDefault := model == f.cfg.Model
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, model).FontSize(12).Bold()
				label := locales.AiProviderModelHidden()
				if shown {
					label = locales.AiProviderModelShown()
				}
				ui.Text(c, label).FontSize(10).TextColor(t.TextMuted)
			})
			value := shown
			ui.Switch(c, &value).Disabled(isDefault).Label(model).OnChange(func() {
				f.cfg.SetModelHidden(p.selected, model, !value)
				a.saveAI()
			})
		})
	}
}

// visibleCLIModels are the reported models the user has not hidden, with
// the chosen default always present.
func (a *app) visibleCLIModels() []string {
	f := &a.aiForm
	var out []string
	for _, model := range f.cliModels {
		if !f.cfg.ModelHidden(f.cfg.Provider, model) || model == f.cfg.Model {
			out = append(out, model)
		}
	}
	return out
}

// providerAdvanced holds the connection details: command path and values
// for a tool, endpoint and key for an API connection.
func (a *app) providerAdvanced(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	p := &a.providers
	if p.selected != f.cfg.Provider {
		ui.Text(c, locales.AiProviderUse()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	preset, _ := ai.ProviderByID(p.selected)
	if ai.CLIProvider(p.selected) {
		a.aiCLIEnvSection(c, t)
		return
	}
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AiBaseURL()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.TextInput(c, &f.cfg.BaseURL).LabelledBy(caption).Placeholder(preset.BaseURL).
			FillWidth().Disabled(!preset.EditableURL).OnChange(a.saveAI)
	})
	a.aiAPIKeyFields(c, t)
	if ai.NeedsHTTPOptIn(f.cfg) {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 10).Radius(8).
			Background(t.Warning.Alpha(0.12)).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			var caption ui.Element
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, locales.AiHTTPWarning()).FontSize(12)
				caption = ui.Text(c, locales.AiAllowHTTP()).FontSize(12).Bold()
			})
			ui.Switch(c, &f.cfg.AllowHTTP).LabelledBy(caption).OnChange(a.saveAI)
		})
	}
	a.aiTestRow(c, t)
}

// aiAPIKeyFields edits the provider's key, which lives in the keychain.
func (a *app) aiAPIKeyFields(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	provider, _ := ai.ProviderByID(f.cfg.Provider)
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AiKey()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			placeholder := locales.AiKeyPlaceholder()
			if f.keyStored {
				placeholder = locales.AiKeyReplace()
			}
			ui.TextInput(c, &f.key).Password().Grow(1).LabelledBy(caption).
				Placeholder(placeholder).OnSubmit(a.saveAIKey)
			ui.Button(c, locales.AiKeySave()).Disabled(strings.TrimSpace(f.key) == "").OnClick(a.saveAIKey)
		})
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
}

// aiTestRow checks the chosen provider; a local tool is checked without a
// model request.
func (a *app) aiTestRow(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
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
}

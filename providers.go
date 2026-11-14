package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
)

// The providers panel: the user's API connections and the installed
// tools on the left, and the chosen provider's Overview, Models and
// Advanced on the right. An API connection is created, edited and
// removed here; its models are fetched from its endpoint.

type providersPanel struct {
	open     bool
	tab      int
	selected ai.ProviderID // an installed tool
	conn     string        // an API connection
	editing  bool
	form     connectionForm
	statuses map[ai.ProviderID]ai.CLIStatus
	keys     map[ai.ProviderID]bool
	loaded   bool
	// gen numbers the loads of the two: one that ends after a later load
	// began says nothing of the panel as it is now.
	gen uint64
	// revealAccount shows the address a tool reports; it is masked until
	// the user asks, so a screenshot of the panel gives no account away.
	revealAccount bool
	// askingRemove holds the open confirmation, and removing the one in
	// flight: a second click must not start another.
	askingRemove bool
	removing     bool
	note         string
}

// connectionForm is the add/edit overlay.
type connectionForm struct {
	id       string
	name     string
	preset   int
	baseURL  string
	key      string
	models   []string
	selected map[string]bool
	newModel string
	fetching bool
	err      string
	isNew    bool
}

// apiPresets are the presets an API connection may use.
func apiPresets() []ai.Provider {
	var out []ai.Provider
	for _, p := range ai.Providers {
		if !ai.CLIProvider(p.ID) {
			out = append(out, p)
		}
	}
	return out
}

func (a *app) openProviders() {
	p := &a.providers
	p.open = true
	p.tab = 0
	p.editing = false
	p.conn = a.aiForm.cfg.Active
	p.selected = ""
	if p.conn == "" && ai.CLIProvider(a.aiForm.cfg.Provider) {
		p.selected = a.aiForm.cfg.Provider
	}
	p.loaded = false
	a.loadProviderStates()
}

// loadProviderStates checks installed tools and whether each connection's
// key is stored. No model request is made.
func (a *app) loadProviderStates() {
	p := &a.providers
	p.gen++
	g := p.gen
	cfg := a.aiForm.cfg.Clone()
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		statuses := map[ai.ProviderID]ai.CLIStatus{}
		keys := map[ai.ProviderID]bool{}
		for _, preset := range ai.Providers {
			if ai.CLIProvider(preset.ID) {
				statuses[preset.ID] = a.ai.CLIStatus(ctx, preset.ID, cfg.CLIPath(preset.ID))
			}
		}
		for _, conn := range cfg.Connections {
			stored, _ := a.ai.KeyStored(ai.ConnectionKeyID(conn))
			keys[ai.ConnectionKeyID(conn)] = stored
		}
		a.post(func() {
			if p.gen != g {
				return
			}
			p.statuses, p.keys, p.loaded = statuses, keys, true
		})
	})
}

// providersSummary is the AI tab's single row into the panel.
func (a *app) providersSummary(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	name, status := locales.AiProviderNoConnection(), ""
	if conn, ok := f.cfg.ActiveConnection(); ok {
		name, status = conn.Name, a.connectionStatusLine(conn)
	} else if ai.CLIProvider(f.cfg.Provider) {
		preset, _ := ai.ProviderByID(f.cfg.Provider)
		name, status = aiProviderName(preset), a.providerStatusLine(f.cfg.Provider)
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontSize(13).Bold()
			if status != "" {
				ui.Text(c, status).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}
		})
		ui.Button(c, locales.AiManageProviders()).OnClick(a.openProviders)
	})
}

func (a *app) connectionStatusLine(conn ai.AIConnection) string {
	if !conn.Enabled {
		return locales.AiProviderDisabled()
	}
	return locales.AiProviderModelsCount(len(conn.Models))
}

// providerStatusLine says an installed tool's state.
func (a *app) providerStatusLine(id ai.ProviderID) string {
	preset, ok := ai.ProviderByID(id)
	if !ok {
		return ""
	}
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
	case status.Outdated:
		return locales.AiLocalToolOutdated()
	case !status.SignedIn:
		return locales.AiLocalToolSignIn()
	case status.Version != "":
		return locales.AiLocalToolVersion(status.Version)
	}
	_ = preset
	return locales.AiLocalToolReady()
}

// selectProvider makes an installed tool the active one.
func (a *app) selectProvider(id ai.ProviderID) {
	a.providers.selected, a.providers.conn = id, ""
	preset, ok := ai.ProviderByID(id)
	if !ok {
		return
	}
	a.aiForm.cfg.Active = ""
	a.aiForm.provider = aiProviderName(preset)
	a.chooseAIProvider()
}

// selectConnection makes an API connection the active one.
func (a *app) selectConnection(conn ai.AIConnection) {
	f := &a.aiForm
	p := &a.providers
	p.conn, p.selected, p.editing = conn.ID, "", false
	f.cfg.Active = conn.ID
	f.cfg.UseProvider(conn.Preset)
	f.cfg.BaseURL = conn.BaseURL
	if conn.Model != "" {
		f.cfg.Model = conn.Model
	} else if len(conn.Models) > 0 {
		f.cfg.Model = conn.Models[0]
	}
	f.provider = aiProviderName(mustPreset(conn.Preset))
	a.saveAI()
	f.keyStored, _ = a.ai.KeyStored(ai.ConnectionKeyID(conn))
	a.loadCLIStatus()
	a.loadProviderStates()
}

func mustPreset(id ai.ProviderID) ai.Provider {
	p, _ := ai.ProviderByID(id)
	return p
}

// startAddConnection opens the overlay for a new connection.
func (a *app) startAddConnection() {
	presets := apiPresets()
	p := &a.providers
	p.editing, p.conn, p.selected = true, "", ""
	p.form = connectionForm{
		id: fmt.Sprintf("conn-%d", time.Now().UnixNano()), preset: 0,
		baseURL: presets[0].BaseURL, selected: map[string]bool{}, isNew: true,
	}
}

func (a *app) startEditConnection(conn ai.AIConnection) {
	p := &a.providers
	presets := apiPresets()
	idx := 0
	for i, preset := range presets {
		if preset.ID == conn.Preset {
			idx = i
		}
	}
	sel := map[string]bool{}
	for _, m := range conn.Models {
		sel[m] = true
	}
	p.editing = true
	p.form = connectionForm{id: conn.ID, name: conn.Name, preset: idx, baseURL: conn.BaseURL,
		models: append([]string(nil), conn.Models...), selected: sel, isNew: false}
}

// removeConnection deletes the selected connection and its key.
func (a *app) removeConnection() {
	p := &a.providers
	conn, ok := a.aiForm.cfg.Connection(p.conn)
	if !ok {
		return
	}
	f := &a.aiForm
	cfg := f.cfg
	cfg.RemoveConnection(conn.ID)
	if cfg.Active == conn.ID {
		cfg.Active = ""
	}
	_ = a.ai.DeleteKey(ai.ConnectionKeyID(conn))
	if len(cfg.Connections) > 0 {
		next := cfg.Connections[0]
		cfg.Active = next.ID
		cfg.UseProvider(next.Preset)
		cfg.BaseURL = next.BaseURL
		cfg.Model = next.Model
		p.conn = next.ID
	} else {
		p.conn = ""
	}
	f.cfg = cfg
	a.saveAI()
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
		ui.Column(c).Width(700).Gap(10).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.AiProvidersTitle()).FontSize(17).Bold().Grow(1)
				ui.Button(c, locales.Done()).OnClick(func() { p.open = false })
			})
			ui.Text(c, locales.AiProvidersIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			ui.Divider(c)
			ui.Row(c).Height(440).Gap(0).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).Width(220).Shrink(0).Children(func() {
					ui.Scroll(c).Grow(1).Children(func() {
						ui.Column(c).Padding(2, 2).Gap(2).Children(func() { a.providerList(c, t) })
					})
					ui.Divider(c)
					ui.Row(c).Shrink(0).Gap(2).Padding(4, 6).Children(func() {
						ui.Button(c, "+").Label(locales.AiProviderAdd()).Tooltip(locales.AiProviderAdd()).
							Disabled(p.editing).OnClick(a.startAddConnection)
						ui.Button(c, "−").Label(locales.AiProviderDelete()).Tooltip(locales.AiProviderDelete()).
							Disabled(p.editing || p.conn == "").OnClick(a.removeConnection)
					})
				})
				ui.Box(c).Width(1).Shrink(0).Background(t.Border)
				ui.Column(c).Grow(1).MinWidth(0).Padding(14, 18).Gap(10).Children(func() {
					switch {
					case p.editing:
						a.connectionEditor(c, t)
					case p.conn != "":
						a.connectionDetail(c, t)
					case p.selected != "":
						a.toolDetail(c, t)
					default:
						a.providerOverview(c, t, c.Services())
					}
				})
			})
			if !p.loaded && !p.editing {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(12, 12)
					ui.Text(c, locales.AiLocalToolChecking()).FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
	a.removeProviderAlert(c)
}

// providerList lists the connections and then the installed tools.
func (a *app) providerList(c *ui.Context, t *ui.Theme) {
	p := &a.providers
	f := &a.aiForm
	ui.Text(c, locales.AiProviderApi()).FontSize(11).Bold().TextColor(t.TextMuted).Padding(8, 8)
	if len(f.cfg.Connections) == 0 {
		ui.Text(c, locales.AiProviderNoConnection()).FontSize(11).TextColor(t.TextMuted).Padding(6, 8)
	}
	for _, conn := range f.cfg.Connections {
		conn := conn
		selected := p.conn == conn.ID && !p.editing
		bg := ui.Transparent
		if selected {
			bg = t.Accent.Alpha(0.12)
		}
		ui.ButtonBase(c).Label(conn.Name).Width(206).Padding(7, 9).Radius(8).
			Background(bg).OnClick(func() { a.selectConnection(conn) }).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).AlignItems(ui.Start).Children(func() {
					ui.Text(c, conn.Name).FontSize(12).Bold().SingleLine()
					ui.Text(c, a.connectionStatusLine(conn)).FontSize(10).TextColor(t.TextMuted).SingleLine()
				})
				if f.cfg.Active == conn.ID {
					ui.Icon(c, checkIcon).FontSize(12).TextColor(t.Accent)
				}
			})
		})
	}
	ui.Text(c, locales.AiLocalTool()).FontSize(11).Bold().TextColor(t.TextMuted).Padding(8, 8)
	for _, preset := range ai.Providers {
		if !ai.CLIProvider(preset.ID) {
			continue
		}
		preset := preset
		selected := p.selected == preset.ID && p.conn == "" && !p.editing
		bg := ui.Transparent
		if selected {
			bg = t.Accent.Alpha(0.12)
		}
		ui.ButtonBase(c).Label(aiProviderName(preset)).Width(206).Padding(7, 9).Radius(8).
			Background(bg).OnClick(func() { a.selectProvider(preset.ID) }).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).AlignItems(ui.Start).Children(func() {
					ui.Text(c, aiProviderName(preset)).FontSize(12).Bold().SingleLine()
					ui.Text(c, a.providerStatusLine(preset.ID)).FontSize(10).TextColor(t.TextMuted).SingleLine()
				})
				if f.cfg.Provider == preset.ID && f.cfg.Active == "" {
					ui.Icon(c, checkIcon).FontSize(12).TextColor(t.Accent)
				}
			})
		})
	}
}

// connectionDetail shows the chosen connection's Overview and Models.
func (a *app) connectionDetail(c *ui.Context, t *ui.Theme) {
	p := &a.providers
	f := &a.aiForm
	conn, ok := f.cfg.Connection(p.conn)
	if !ok {
		return
	}
	preset := mustPreset(conn.Preset)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, conn.Name).FontSize(15).Bold().SingleLine()
			ui.Text(c, aiProviderName(preset)+" · "+locales.AiProviderModelsCount(len(conn.Models))).
				FontSize(11).TextColor(t.TextMuted)
		})
		ui.Button(c, locales.AiProviderEdit()).OnClick(func() { a.startEditConnection(conn) })
		enabled := conn.Enabled
		ui.Switch(c, &enabled).Label(locales.AiProviderEnabled()).OnChange(func() {
			conn.Enabled = enabled
			f.cfg.SetConnection(conn)
			a.saveAI()
		})
	})
	if f.cfg.Active == conn.ID {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(12).TextColor(t.Accent)
			ui.Text(c, locales.AiProviderInUse()).FontSize(11).TextColor(t.Accent)
		})
	} else {
		ui.Button(c, locales.AiProviderUse()).OnClick(func() { a.selectConnection(conn) })
	}
	ui.Divider(c)
	ui.Text(c, locales.AiProviderBaseURL()).FontSize(12).Bold().TextColor(t.TextMuted)
	ui.Text(c, conn.BaseURL).FontSize(12).Selectable().SingleLine()
	if p.keys[ai.ConnectionKeyID(conn)] {
		ui.Text(c, locales.AiKeyStored()).FontSize(11).TextColor(t.TextMuted)
	} else {
		ui.Text(c, locales.AiKeyMissing()).FontSize(11).TextColor(t.Warning)
	}
	a.aiAPIKeyFields(c, t)
	if ai.NeedsHTTPOptIn(f.cfg) {
		ui.Switch(c, &f.cfg.AllowHTTP).Label(locales.AiAllowHTTP()).OnChange(a.saveAI)
	}
	a.aiTestRow(c, t)
	ui.Divider(c)
	ui.Text(c, locales.AiProviderModelsCount(len(conn.Models))).FontSize(12).Bold().TextColor(t.TextMuted)
	if len(conn.Models) == 0 {
		ui.Text(c, locales.AiProviderNoModelsYet()).FontSize(11).TextColor(t.TextMuted)
	}
	for _, model := range conn.Models {
		ui.Text(c, model).FontSize(12).SingleLine()
	}
}

// connectionEditor is the add/edit overlay: name, preset, endpoint, key,
// and the models fetched from the endpoint.
func (a *app) connectionEditor(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	p := &a.providers
	form := &p.form
	presets := apiPresets()
	title := locales.AiProviderAdd()
	if !form.isNew {
		title = locales.AiProviderEdit()
	}
	ui.Text(c, title).FontSize(15).Bold()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(10).PaddingEnd(8).Children(func() {
			ui.TextInput(c, &form.name).Label(locales.AiProviderName()).
				Placeholder(locales.AiProviderNameHint()).FillWidth()
			names := make([]string, 0, len(presets))
			for _, preset := range presets {
				names = append(names, aiProviderName(preset))
			}
			choice := ""
			if form.preset >= 0 && form.preset < len(names) {
				choice = names[form.preset]
			}
			ui.Select(c, &choice, names).Label(locales.Provider()).FillWidth().OnChange(func() {
				for i, name := range names {
					if name == choice {
						form.preset = i
					}
				}
				if form.baseURL == "" || form.baseURL == presets[0].BaseURL {
					form.baseURL = presets[form.preset].BaseURL
				}
			})
			ui.TextInput(c, &form.baseURL).Label(locales.AiProviderBaseURL()).
				Placeholder(presets[form.preset].BaseURL).FillWidth()
			ui.TextInput(c, &form.key).Password().Label(locales.AiProviderAPIKey()).
				Placeholder(locales.AiProviderAPIKeyHint()).FillWidth()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := locales.AiProviderFetchModels()
				if form.fetching {
					label = locales.AiProviderFetching()
				}
				ui.Button(c, label).Disabled(form.fetching).OnClick(a.fetchConnectionModels)
				if form.err != "" {
					ui.Icon(c, alertIcon).FontSize(13).TextColor(t.Warning)
					ui.Text(c, form.err).FontSize(11).TextColor(t.Warning).Grow(1).MaxLines(2)
				}
			})
			ui.Divider(c)
			ui.Text(c, locales.AiProviderModelsHint()).FontSize(11).TextColor(t.TextMuted)
			if len(form.models) == 0 {
				ui.Text(c, locales.AiProviderNoModelsYet()).FontSize(11).TextColor(t.TextMuted)
			}
			for _, model := range form.models {
				model := model
				on := form.selected[model]
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, model).FontSize(12).Grow(1).MinWidth(0).SingleLine()
					value := on
					ui.Switch(c, &value).Label(model).OnChange(func() { form.selected[model] = value })
				})
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &form.newModel).Label(locales.AiProviderModelAdd()).
					Placeholder("model-id").Grow(1)
				ui.Button(c, locales.AiProviderModelAdd()).Disabled(strings.TrimSpace(form.newModel) == "").
					OnClick(func() {
						model := strings.TrimSpace(form.newModel)
						if model != "" && !containsString(form.models, model) {
							form.models = append(form.models, model)
							form.selected[model] = true
							form.newModel = ""
						}
					})
			})
		})
	})
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Spacer(c)
		ui.Button(c, locales.AiProviderCancel()).OnClick(func() { p.editing = false })
		ui.PrimaryButton(c, locales.AiProviderSave()).Disabled(form.fetching).OnClick(func() {
			a.saveConnection()
		})
	})
	_ = f
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// fetchConnectionModels asks the endpoint for the models it offers.
func (a *app) fetchConnectionModels() {
	form := &a.providers.form
	if form.fetching {
		return
	}
	presets := apiPresets()
	preset := presets[form.preset]
	key := form.key
	form.fetching, form.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		if key == "" {
			if conn, ok := a.aiForm.cfg.Connection(form.id); ok {
				if data, err := a.ai.Secret(ai.ConnectionKeyID(conn)); err == nil {
					key = data
				}
			}
		}
		models, err := a.ai.ListModels(ctx, preset.ID, form.baseURL, key)
		a.post(func() {
			form.fetching = false
			if err != nil {
				form.err, _ = aiErrorText(err)
				return
			}
			form.models = models
			form.selected = map[string]bool{}
			for _, m := range models {
				form.selected[m] = true
			}
		})
	})
}

// saveConnection validates the form and keeps the connection, its models
// and its key.
func (a *app) saveConnection() {
	p := &a.providers
	form := &p.form
	presets := apiPresets()
	preset := presets[form.preset]
	name := strings.TrimSpace(form.name)
	if name == "" {
		name = preset.Name
	}
	base := strings.TrimSpace(form.baseURL)
	if base == "" {
		base = preset.BaseURL
	}
	var models []string
	for _, m := range form.models {
		if form.selected[m] {
			models = append(models, m)
		}
	}
	conn := ai.AIConnection{ID: form.id, Name: name, Preset: preset.ID, BaseURL: base,
		Model: "", Models: models, Enabled: true}
	if len(models) > 0 {
		conn.Model = models[0]
	}
	f := &a.aiForm
	cfg := f.cfg
	cfg.SetConnection(conn)
	cfg.Active = conn.ID
	cfg.UseProvider(preset.ID)
	cfg.BaseURL = base
	if conn.Model != "" {
		cfg.Model = conn.Model
	} else {
		cfg.Model = preset.Model
	}
	f.cfg = cfg
	f.provider = aiProviderName(preset)
	f.keyStored, _ = a.ai.KeyStored(ai.ConnectionKeyID(conn))
	if form.key != "" {
		ctx, cancel := a.ctx()
		_ = a.ai.SetKey(ctx, ai.ConnectionKeyID(conn), form.key)
		cancel()
	}
	a.saveAI()
	p.conn, p.editing, p.selected = conn.ID, false, ""
	a.loadProviderStates()
}

// toolDetail is the installed tool's Overview/Models/Advanced, as before.
func (a *app) toolDetail(c *ui.Context, t *ui.Theme) {
	p := &a.providers
	ui.Tabs(c, &p.tab, locales.AiProviderOverview(), locales.AiProviderModels(), locales.AiProviderAdvanced()).Label(locales.AiProvidersTitle())
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(10).PaddingEnd(8).Children(func() {
			switch p.tab {
			case 1:
				a.providerModels(c, t)
			case 2:
				a.providerAdvanced(c, t)
			default:
				a.providerOverview(c, t, c.Services())
			}
		})
	})
}
func (a *app) providerOverview(c *ui.Context, t *ui.Theme, services ui.Services) {
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
	a.providerOffSwitch(c, t)
	ui.Text(c, a.providerStatusLine(p.selected)).FontSize(12).TextColor(t.Text).MaxLines(3)
	if ai.CLIProvider(p.selected) {
		status := p.statuses[p.selected]
		if status.Path != "" {
			ui.Text(c, status.Path).FontSize(11).TextColor(t.TextMuted).Selectable().SingleLine()
		}
		a.cliAccountRow(c, t, status)
		a.cliNextSteps(c, t, status, services)
		ui.Text(c, locales.AiLocalToolIsolation()).FontSize(11).TextColor(t.TextMuted).MaxLines(5)
		ui.Text(c, locales.AiLocalToolIntro()).FontSize(11).TextColor(t.TextMuted).MaxLines(4)
		ui.Text(c, locales.AiLocalToolNotOfficial()).FontSize(11).TextColor(t.TextMuted).MaxLines(5)
		a.removeProviderRow(c, t)
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
	a.removeProviderRow(c, t)
}

// cliAccountRow says which account a tool is signed in as, masked until
// the user reveals it: the address is the tool's, not Shu's, and the
// panel should not put it on screen by itself.
func (a *app) cliAccountRow(c *ui.Context, t *ui.Theme, status ai.CLIStatus) {
	p := &a.providers
	if status.Account == "" {
		return
	}
	account := status.Account
	if !p.revealAccount {
		account = maskAddress(account)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, mailIcon).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, locales.AiLocalToolAccount(account)).FontSize(11).TextColor(t.TextMuted).
			Grow(1).MinWidth(0).SingleLine()
		label := locales.AiLocalToolAccountShow()
		if p.revealAccount {
			label = locales.AiLocalToolAccountHide()
		}
		ui.Button(c, label).Label(label).OnClick(func() { p.revealAccount = !p.revealAccount })
	})
}

// maskAddress keeps the first letter and the domain, so the user
// recognises the account while a screenshot does not name it.
func maskAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return "•••"
	}
	local := address[:at]
	head := string([]rune(local)[:1])
	return head + "•••" + address[at:]
}

// cliNextSteps is what the user can do next while a tool is not ready,
// with the reason the status line gives above it.
func (a *app) cliNextSteps(c *ui.Context, t *ui.Theme, status ai.CLIStatus, services ui.Services) {
	p := &a.providers
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if !status.Installed {
			if url := ai.CLIInstallURL(p.selected); url != "" {
				ui.Button(c, locales.AiLocalToolInstall()).OnClick(func() { services.OpenURL(url) })
			}
		}
		if status.Installed && status.Outdated {
			// An update is the one fix for an outdated tool, and it comes
			// before sign-in: its status command may work while the flags
			// Shu launches it with do not.
			if url := ai.CLIInstallURL(p.selected); url != "" {
				ui.Button(c, locales.AiLocalToolUpdate()).OnClick(func() { services.OpenURL(url) })
			}
		}
		if status.Installed && !status.Outdated && !status.SignedIn {
			if command := ai.CLISignInCommand(p.selected); command != "" {
				ui.Button(c, locales.AiLocalToolSignInCommand()).OnClick(func() { a.copySignIn(services, command) })
			}
		}
		if p.loaded {
			label := locales.AiLocalToolCheck()
			ui.Button(c, label).Disabled(p.removing).OnClick(a.loadProviderStates)
		}
	})
}

// copySignIn puts a tool's sign-in command on the clipboard, which the
// user runs in a terminal: Shu never runs it, since it needs a browser
// and would make the tool's credentials its own.
func (a *app) copySignIn(services ui.Services, command string) {
	services.WriteClipboard(command)
	a.providers.note = locales.AiLocalToolCopied(command)
}

// removeProviderRow is the destructive action, set apart from the
// others: it asks first, and names what will be gone.
func (a *app) removeProviderRow(c *ui.Context, t *ui.Theme) {
	p := &a.providers
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 0).Children(func() {
		ui.Text(c, locales.AiProviderRemoveHint()).FontSize(11).TextColor(t.TextMuted).Grow(1).MaxLines(2)
		ui.Button(c, locales.AiProviderRemove()).Disabled(p.removing).OnClick(func() { p.askingRemove = true })
	})
	if p.note != "" {
		ui.Text(c, p.note).FontSize(11).TextColor(t.Success).MaxLines(2).Role(ui.RoleStatus)
	}
}

// removeProviderAlert is the confirmation. It blocks until answered, so
// it is drawn beside the panel and never from a job: the panel stays
// readable behind it.
func (a *app) removeProviderAlert(c *ui.Context) {
	p := &a.providers
	if !p.askingRemove {
		return
	}
	preset, ok := ai.ProviderByID(p.selected)
	if !ok {
		p.askingRemove = false
		return
	}
	message := locales.AiProviderRemoveKey()
	if ai.CLIProvider(p.selected) {
		message = locales.AiProviderRemoveTool(len(a.aiForm.cfg.CLIEnvNames(p.selected)))
	}
	if ui.AlertDialog(c, &p.askingRemove, locales.AiProviderRemoveTitle(aiProviderName(preset)), message,
		locales.Cancel(), locales.AiProviderRemoveButton()) == 1 {
		a.removeProvider(p.selected)
	}
}

// removeProvider forgets a provider's setup and key in the background,
// then reloads what the panel and the settings show.
func (a *app) removeProvider(id ai.ProviderID) {
	p := &a.providers
	if p.removing {
		return
	}
	p.removing, p.note = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.ai.RemoveProvider(ctx, id)
		cfg, cfgErr := a.ai.Config(ctx)
		a.post(func() {
			p.removing = false
			if err != nil {
				p.note, _ = aiErrorText(err)
				return
			}
			f := &a.aiForm
			f.key, f.note, f.err = "", "", ""
			f.keyStored = false
			if cfgErr != nil {
				f.err, _ = aiErrorText(cfgErr)
				return
			}
			f.setConfig(cfg)
			// The removed provider keeps no state of its own, so a later
			// look at it starts empty rather than showing what it had.
			delete(p.statuses, id)
			delete(p.keys, id)
			if preset, ok := ai.ProviderByID(id); ok {
				p.note = locales.AiProviderRemoved(aiProviderName(preset))
			}
			if ai.CLIProvider(f.cfg.Provider) {
				a.loadCLIStatus()
			}
			a.loadProviderStates()
		})
	})
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
	choices := a.visibleCLIModels()
	if f.cfg.Model != "" && !containsString(choices, f.cfg.Model) {
		choices = append([]string{f.cfg.Model}, choices...)
	}
	if len(choices) > 0 {
		ui.Column(c).Gap(4).Children(func() {
			caption := ui.Text(c, locales.AiModel()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Select(c, &f.cfg.Model, choices).LabelledBy(caption).FillWidth().OnChange(a.saveAI)
		})
	} else {
		ui.Column(c).Gap(4).Children(func() {
			caption := ui.Text(c, locales.AiModel()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Autocomplete(c, &f.cfg.Model, choices).LabelledBy(caption).
				Placeholder(preset.Model).FillWidth().OnChange(a.saveAI)
		})
	}
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

// providerOffSwitch turns the chosen provider on or off. It is a switch
// of its own, apart from making a provider the active one: an off
// provider keeps its setup and its key, and only leaves the pickers.
func (a *app) providerOffSwitch(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	id := a.providers.selected
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		var caption ui.Element
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			caption = ui.Text(c, locales.AiProviderEnabled()).FontSize(12)
			ui.Text(c, locales.AiProviderOffHint()).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
		on := !f.cfg.ProviderOff(id)
		ui.Switch(c, &on).LabelledBy(caption).Label(locales.AiProviderEnabled()).OnChange(func() {
			f.cfg.SetProviderOff(id, !on)
			a.saveAI()
		})
	})
}

// providerEffort picks the reasoning effort a local tool runs with.
func (a *app) providerEffort(c *ui.Context, t *ui.Theme) {
	f := &a.aiForm
	id := a.providers.selected
	options := append([]string{locales.AiProviderEffortDefault()}, ai.Efforts...)
	choice := locales.AiProviderEffortDefault()
	current := f.cfg.Effort(id)
	for _, effort := range ai.Efforts {
		if current == effort {
			choice = effort
		}
	}
	caption := ui.Text(c, locales.AiProviderEffort()).FontSize(11).Bold().TextColor(t.TextMuted)
	ui.Select(c, &choice, options).LabelledBy(caption).Width(200).OnChange(func() {
		effort := choice
		if effort == locales.AiProviderEffortDefault() {
			effort = ""
		}
		f.cfg.SetEffort(id, effort)
		a.saveAI()
	})
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
		a.providerEffort(c, t)
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
		case f.keyStored && f.cfg.KeyEndpointMismatch(f.cfg.Provider):
			ui.Icon(c, alertIcon).FontSize(13).TextColor(t.Warning)
			ui.Text(c, locales.AiProviderKeyMoved()).FontSize(11).TextColor(t.Warning).Grow(1).MaxLines(2)
			ui.Button(c, locales.AiKeyDelete()).OnClick(a.deleteAIKey)
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

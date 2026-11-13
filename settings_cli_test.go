//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
)

// The AI tab shows an installed, signed-in local tool with its version
// and command path, without a model request or an API key.
func TestAISettingsLocalToolStatus(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '2.1.323 (Claude Code)'; exit 0; fi\n" +
		"if [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\": true}'; exit 0; fi\n" +
		"echo '{\"type\":\"control_response\",\"response\":{\"request_id\":\"shu-models\",\"response\":{\"models\":[{\"value\":\"sonnet\"},{\"value\":\"opus\"}]}}}'\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, script)
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.openSettings(settingsTabAI)
	tt := ui.NewTester(a.view, 1220, 900)
	a.openProviders()
	tt.Frame()
	for _, text := range []string{locales.AiLocalToolIntro(),
		locales.AiLocalToolVersion("2.1.323"), script, locales.AiLocalToolIsolation()} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if tt.HasText(locales.AiKey()) {
		t.Errorf("a local tool asks for an API key: %q", tt.Texts())
	}
	if len(a.aiForm.cliModels) != 2 || a.aiForm.cliModels[1] != "opus" {
		t.Errorf("models = %q", a.aiForm.cliModels)
	}
	click(t, tt, locales.AiProviderModels())
	if !tt.HasText(locales.AiModel()) {
		t.Errorf("the model field is missing: %q", tt.Texts())
	}
	// A tool's own value is added by name and kept out of the visible
	// text, then listed as a variable.
	a.aiForm.envName, a.aiForm.envValue = "MY_TOKEN", "env-secret"
	a.addCLIEnv()
	if len(a.aiForm.cliEnv) != 1 || a.aiForm.cliEnv[0].Name != "MY_TOKEN" {
		t.Errorf("env = %+v", a.aiForm.cliEnv)
	}
	if tt.HasText("env-secret") {
		t.Errorf("the value is visible: %q", tt.Texts())
	}
}

// A set command path is saved with the settings, and a broken one is
// reported rather than replaced by another copy.
func TestAISettingsLocalToolPath(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, "/does/not/exist")
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.openSettings(settingsTabAI)
	tt := ui.NewTester(a.view, 1220, 900)
	a.openProviders()
	tt.Frame()
	if a.aiForm.cliStatus.Err != "the command path does not run" {
		t.Errorf("status = %+v", a.aiForm.cliStatus)
	}
	if !tt.HasText("the command path does not run") {
		t.Errorf("the bad path is not reported: %q", tt.Texts())
	}
}

// Hidden models leave the model suggestions, but the chosen default is
// always offered even if it was hidden earlier.
func TestProviderModelHiding(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	a.aiForm.cfg = ai.DefaultConfig()
	a.aiForm.cfg.UseProvider(ai.ProviderClaudeCLI)
	a.aiForm.cfg.Model = "sonnet"
	a.aiForm.cliModels = []string{"sonnet", "opus", "haiku"}
	a.aiForm.cfg.SetModelHidden(ai.ProviderClaudeCLI, "opus", true)
	if got := a.visibleCLIModels(); len(got) != 2 || got[1] != "haiku" {
		t.Errorf("visible = %q", got)
	}
	a.aiForm.cfg.SetModelHidden(ai.ProviderClaudeCLI, "sonnet", true)
	if got := a.visibleCLIModels(); len(got) != 2 || got[0] != "sonnet" {
		t.Errorf("the default is hidden: %q", got)
	}
}

// The providers panel switches a provider off, and the choice survives a
// reload. The provider stays chosen: only its models leave the pickers.
func TestProviderOffSwitch(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.Model = "sonnet"
	cfg.SetCLIPath(ai.ProviderClaudeCLI, "/nonexistent/claude")
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.aiForm.setConfig(cfg)
	a.aiForm.cliModels = []string{"sonnet", "opus"}
	a.openSettings(settingsTabAI)
	a.openProviders()
	a.providers.selected = ai.ProviderClaudeCLI
	tt := ui.NewTester(a.view, 1220, 900)
	tt.Frame()
	if !tt.HasText(locales.AiProviderOffHint()) {
		t.Fatalf("no off switch in %q", tt.Texts())
	}
	click(t, tt, locales.AiProviderEnabled())
	if !a.aiForm.cfg.ProviderOff(ai.ProviderClaudeCLI) {
		t.Fatal("the provider is still on")
	}
	saved, err := a.ai.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !saved.ProviderOff(ai.ProviderClaudeCLI) {
		t.Error("the choice was not saved")
	}
	if saved.Provider != ai.ProviderClaudeCLI {
		t.Errorf("provider = %q, want it still chosen", saved.Provider)
	}
	tt.Frame()
	click(t, tt, locales.AiProviderEnabled())
	if a.aiForm.cfg.ProviderOff(ai.ProviderClaudeCLI) {
		t.Error("the provider did not switch back on")
	}
}

// The account a tool reports stays masked until the user asks for it: a
// screenshot of the panel should not name anyone.
func TestProviderAccountMasked(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '2.1.323 (Claude Code)'; exit 0; fi\n" +
		"if [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\": true, \"email\": \"maya@example.com\"}'; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, script)
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.openSettings(settingsTabAI)
	a.openProviders()
	a.providers.selected = ai.ProviderClaudeCLI
	tt := ui.NewTester(a.view, 1220, 900)
	tt.Frame()
	if !tt.HasText(locales.AiLocalToolAccount("m•••@example.com")) {
		t.Fatalf("the masked account is missing: %q", tt.Texts())
	}
	if tt.HasText("maya@example.com") {
		t.Errorf("the address is shown before the user asked: %q", tt.Texts())
	}
	click(t, tt, locales.AiLocalToolAccountShow())
	if !tt.HasText(locales.AiLocalToolAccount("maya@example.com")) {
		t.Errorf("the address did not appear: %q", tt.Texts())
	}
	click(t, tt, locales.AiLocalToolAccountHide())
	if tt.HasText("maya@example.com") {
		t.Errorf("the address stayed after hiding: %q", tt.Texts())
	}
}

// A tool that is not ready offers the next step it needs: the install
// page when it is missing, the sign-in command when it is signed out.
func TestProviderNextSteps(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	// Installed, but the sign-in check fails: a signed-out tool.
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '2.1.323'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, script)
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.openSettings(settingsTabAI)
	a.openProviders()
	a.providers.selected = ai.ProviderClaudeCLI
	tt := ui.NewTester(a.view, 1220, 900)
	tt.Frame()
	if !tt.HasText(locales.AiLocalToolSignInCommand()) {
		t.Fatalf("no sign-in step for a signed-out tool: %q", tt.Texts())
	}
	if tt.HasText(locales.AiLocalToolInstall()) {
		t.Errorf("an installed tool offers its install page: %q", tt.Texts())
	}
	click(t, tt, locales.AiLocalToolSignInCommand())
	if a.providers.note != locales.AiLocalToolCopied(ai.CLISignInCommand(ai.ProviderClaudeCLI)) {
		t.Errorf("note = %q", a.providers.note)
	}

	// A path that does not run means the tool is missing: the install
	// page is the next step, and the sign-in one is not offered.
	a.aiForm.cfg.SetCLIPath(ai.ProviderClaudeCLI, filepath.Join(dir, "missing"))
	if err := a.ai.SaveConfig(context.Background(), a.aiForm.cfg); err != nil {
		t.Fatal(err)
	}
	a.loadProviderStates()
	tt.Frame()
	if !tt.HasText(locales.AiLocalToolInstall()) {
		t.Fatalf("no install step for a missing tool: %q", tt.Texts())
	}
	if tt.HasText(locales.AiLocalToolSignInCommand()) {
		t.Errorf("a missing tool offers to sign in: %q", tt.Texts())
	}
	if !tt.HasText(locales.AiLocalToolCheck()) {
		t.Errorf("no way to check again: %q", tt.Texts())
	}
}

// Removing a provider is confirmed first, and the confirmation names
// what goes: the tool's keychain values and its setup.
func TestProviderRemove(t *testing.T) {
	a, _, _, secrets := aiMessageApp(t, true)
	ctx := context.Background()
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, "/nonexistent/claude")
	cfg.SetModelHidden(ai.ProviderClaudeCLI, "opus", true)
	if err := a.ai.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.ai.SaveCLIEnv(ctx, ai.ProviderClaudeCLI, []ai.CLIEnvVar{{Name: "MY_TOKEN", Value: "secret"}}); err != nil {
		t.Fatal(err)
	}
	a.aiForm.setConfig(cfg)
	a.openSettings(settingsTabAI)
	a.openProviders()
	a.providers.selected = ai.ProviderClaudeCLI
	tt := ui.NewTester(a.view, 1220, 900)
	tt.Frame()
	if !tt.HasText(locales.AiProviderRemove()) {
		t.Fatalf("no remove action in %q", tt.Texts())
	}
	click(t, tt, locales.AiProviderRemove())
	tt.Frame()
	if !tt.HasText(locales.AiProviderRemoveTitle("Claude Code")) {
		t.Fatalf("no confirmation in %q", tt.Texts())
	}
	if !tt.HasText(locales.AiProviderRemoveTool(1)) {
		t.Errorf("the confirmation does not name the values: %q", tt.Texts())
	}
	// Cancel leaves everything as it was.
	click(t, tt, locales.Cancel())
	if a.aiForm.cfg.Provider != ai.ProviderClaudeCLI {
		t.Errorf("provider = %q after cancel", a.aiForm.cfg.Provider)
	}
	click(t, tt, locales.AiProviderRemove())
	tt.Frame()
	click(t, tt, locales.AiProviderRemoveButton())
	saved, err := a.ai.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Provider == ai.ProviderClaudeCLI {
		t.Error("the removed provider is still chosen")
	}
	if saved.CLIPath(ai.ProviderClaudeCLI) != "" || len(saved.CLIEnvNames(ai.ProviderClaudeCLI)) != 0 {
		t.Errorf("its setup is still there: %+v", saved)
	}
	if saved.ModelHidden(ai.ProviderClaudeCLI, "opus") {
		t.Error("its hidden models are still there")
	}
	if _, ok := secrets["ai:cli:claude-code:env:MY_TOKEN"]; ok {
		t.Error("the tool's value is still in the keychain")
	}
	if !tt.HasText(locales.AiProviderRemoved("Claude Code")) {
		t.Errorf("no note after removal: %q", tt.Texts())
	}
}

// A tool older than the flags Shu starts it with says to update, and
// offers the page, rather than reporting a version as if it worked.
func TestProviderOutdated(t *testing.T) {
	a, _, _, _ := aiMessageApp(t, true)
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '0.4.0'; exit 0; fi\n" +
		"if [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\": true}'; exit 0; fi\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ai.ProviderClaudeCLI)
	cfg.SetCLIPath(ai.ProviderClaudeCLI, script)
	if err := a.ai.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a.openSettings(settingsTabAI)
	a.openProviders()
	a.providers.selected = ai.ProviderClaudeCLI
	tt := ui.NewTester(a.view, 1220, 900)
	tt.Frame()
	if !tt.HasText(locales.AiLocalToolOutdated()) {
		t.Fatalf("no update state for an old tool: %q", tt.Texts())
	}
	if !tt.HasText(locales.AiLocalToolUpdate()) {
		t.Errorf("no update page for an old tool: %q", tt.Texts())
	}
	if tt.HasText(locales.AiLocalToolSignInCommand()) {
		t.Errorf("an outdated tool offers to sign in: %q", tt.Texts())
	}
	if !tt.HasText(locales.AiLocalToolNotOfficial()) {
		t.Errorf("the panel does not say it is not an official integration: %q", tt.Texts())
	}
}

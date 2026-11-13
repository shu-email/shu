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
	for _, text := range []string{locales.AiLocalToolIntro(), locales.AiLocalToolPath(),
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
	if a.aiForm.cliStatus.Err != "the command path does not run" {
		t.Errorf("status = %+v", a.aiForm.cliStatus)
	}
	if !tt.HasText("the command path does not run") {
		t.Errorf("the bad path is not reported: %q", tt.Texts())
	}
}

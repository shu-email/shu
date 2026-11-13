//go:build !windows

package ai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI writes a shell script that records what it was run with and
// answers as a local tool does.
func fakeCLI(t *testing.T, body string) (script, log, stdin string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	stdin = filepath.Join(dir, "stdin")
	script = filepath.Join(dir, "claude")
	content := "#!/bin/sh\n" +
		"{\n echo \"args:$@\"\n echo \"pwd:$PWD\"\n echo \"secret:${OPENAI_API_KEY:-}\"\n} > " + log + "\n" +
		body
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log, stdin
}

func readLog(t *testing.T, log, stdinPath string) (args, pwd, secret, stdin string) {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "args:"):
			args = strings.TrimPrefix(line, "args:")
		case strings.HasPrefix(line, "pwd:"):
			pwd = strings.TrimPrefix(line, "pwd:")
		case strings.HasPrefix(line, "secret:"):
			secret = strings.TrimPrefix(line, "secret:")
		}
	}
	if data, err := os.ReadFile(stdinPath); err == nil {
		stdin = string(data)
	}
	return
}

func cliConfig(script string) Config {
	cfg := Config{Provider: ProviderClaudeCLI, Model: "sonnet"}
	cfg.SetCLIPath(ProviderClaudeCLI, script)
	return cfg
}

func TestCLIStatusWithoutAModelRequest(t *testing.T) {
	script, _, _ := fakeCLI(t, `if [ "$1" = "--version" ]; then echo "2.1.323 (Claude Code)"; exit 0; fi
if [ "$1" = "auth" ]; then echo '{"loggedIn": true}'; exit 0; fi
exit 1
`)
	svc := NewService(nil, nil)
	status := svc.CLIStatus(context.Background(), ProviderClaudeCLI, script)
	if !status.Installed || !status.SignedIn || status.Version != "2.1.323" || status.Path != script {
		t.Fatalf("status = %+v", status)
	}
	// A signed-out tool is installed and says so, without failing.
	script, _, _ = fakeCLI(t, `if [ "$1" = "--version" ]; then echo "2.1.323"; exit 0; fi
if [ "$1" = "auth" ]; then echo '{"loggedIn": false}'; exit 0; fi
exit 1
`)
	status = svc.CLIStatus(context.Background(), ProviderClaudeCLI, script)
	if !status.Installed || status.SignedIn {
		t.Errorf("signed out status = %+v", status)
	}
	if status := svc.CLIStatus(context.Background(), ProviderClaudeCLI, "/does/not/exist"); status.Installed {
		t.Errorf("missing path = %+v", status)
	}
}

// The prompt is on standard input, the working directory is private and
// removed, and the tool's environment carries no API key.
func TestRunCLIIsolated(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	log := filepath.Join(dir, "log")
	stdinPath := filepath.Join(dir, "stdin")
	body := "#!/bin/sh\n" +
		"{ echo \"args:$@\"; echo \"pwd:$PWD\"; echo \"secret:${OPENAI_API_KEY:-}\"; } > " + log + "\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 2.1.323; exit 0; fi\n" +
		"if [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\": true}'; exit 0; fi\n" +
		"cat > " + stdinPath + "\n" +
		"printf '\\033[31mHello from CLI\\033[0m\\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "top-secret")
	svc := NewService(nil, nil)
	out, err := svc.runCLI(context.Background(), cliConfig(script), "sonnet", request{
		system: "SYSTEM-INSTRUCTION", user: "MAIL-TEXT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello from CLI" {
		t.Errorf("output = %q", out)
	}
	args, pwd, secret, stdin := readLog(t, log, stdinPath)
	if strings.Contains(args, "SYSTEM-INSTRUCTION") || strings.Contains(args, "MAIL-TEXT") {
		t.Errorf("the prompt is in argv: %q", args)
	}
	for _, want := range []string{"--tools", "--disallowedTools", "--no-session-persistence", "--strict-mcp-config"} {
		if !strings.Contains(args, want) {
			t.Errorf("args miss %q: %q", want, args)
		}
	}
	if !strings.Contains(stdin, "SYSTEM-INSTRUCTION") || !strings.Contains(stdin, "MAIL-TEXT") {
		t.Errorf("stdin = %q", stdin)
	}
	if secret != "" {
		t.Errorf("an API key reached the tool: %q", secret)
	}
	if !strings.Contains(pwd, "shu-cli-") {
		t.Errorf("working directory = %q", pwd)
	}
	if _, err := os.Stat(pwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the working directory stayed: %v", err)
	}
}

func TestRunCLICancelEndsTheProcess(t *testing.T) {
	script, _, _ := fakeCLI(t, `sleep 30
`)
	svc := NewService(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	start := time.Now()
	_, err := svc.runCLI(ctx, cliConfig(script), "sonnet", request{system: "s", user: "u"})
	if err == nil {
		t.Fatal("a cancelled tool returned no error")
	}
	var aiErr *Error
	if !errors.As(err, &aiErr) || aiErr.Kind != KindCancelled {
		t.Fatalf("error = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancel took %v", time.Since(start))
	}
}

func TestRunProcessOutputCap(t *testing.T) {
	_, err := runProcess(context.Background(), "/bin/sh", []string{"-c", "yes"}, "", nil, 1024)
	var aiErr *Error
	if !errors.As(err, &aiErr) || aiErr.Kind != KindCLIOutput {
		t.Fatalf("error = %v", err)
	}
}

// A summary runs through an installed, signed-in tool with no API key.
func TestSummaryThroughACLI(t *testing.T) {
	script, _, _ := fakeCLI(t, `if [ "$1" = "--version" ]; then echo "2.1.323"; exit 0; fi
if [ "$1" = "auth" ]; then echo '{"loggedIn": true}'; exit 0; fi
cat > /dev/null
echo "- CLI summary line"
`)
	svc, provider, _, _ := newTestService(t)
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.UseProvider(ProviderClaudeCLI)
	cfg.SetCLIPath(ProviderClaudeCLI, script)
	if err := svc.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := svc.Check(context.Background(), FeatureSummary); err != nil {
		t.Fatalf("check: %v", err)
	}
	result, err := svc.Summarize(context.Background(), testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != ProviderClaudeCLI || result.Model != "sonnet" ||
		!strings.Contains(result.Text, "CLI summary") {
		t.Errorf("result = %+v", result)
	}
	if provider.count() != 0 {
		t.Errorf("an HTTP request was made: %d", provider.count())
	}
}

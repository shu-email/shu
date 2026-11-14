//go:build windows

package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTool writes a batch file that records what it was run with and
// answers as a local tool does. The Unix tests use a shell script; this
// is the same idea in the form Windows has.
func fakeTool(t *testing.T, body string) (script, log, stdin string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	stdin = filepath.Join(dir, "stdin")
	script = filepath.Join(dir, "claude.cmd")
	content := "@echo off\r\n" +
		"(\r\n echo args:%*\r\n echo pwd:%CD%\r\n echo secret:%OPENAI_API_KEY%\r\n" +
		") > \"" + log + "\"\r\n" +
		body
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return script, log, stdin
}

func readToolLog(t *testing.T, log string) (args, pwd, secret string) {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\r\n") {
		switch {
		case strings.HasPrefix(line, "args:"):
			args = strings.TrimPrefix(line, "args:")
		case strings.HasPrefix(line, "pwd:"):
			pwd = strings.TrimPrefix(line, "pwd:")
		case strings.HasPrefix(line, "secret:"):
			secret = strings.TrimPrefix(line, "secret:")
		}
	}
	return
}

// An explicit path is what runs, and a batch file is executable on
// Windows, where no permission bit says so.
func TestCLIFindWindows(t *testing.T) {
	script, _, _ := fakeTool(t, "exit /b 0\r\n")
	if path, ok := cliFind("claude", script); !ok || path != script {
		t.Errorf("cliFind explicit = %q, %v", path, ok)
	}
	if _, ok := cliFind("claude", filepath.Join(t.TempDir(), "missing.cmd")); ok {
		t.Error("a path that does not exist was accepted")
	}
	// A bare name is found through PATH, by the extensions Windows uses.
	t.Setenv("PATH", filepath.Dir(script))
	if path, ok := cliFind("claude", ""); !ok || path != script {
		t.Errorf("cliFind through PATH = %q, %v", path, ok)
	}
}

// A launch on Windows runs the tool in a private directory, keeps the
// environment to what the tool needs, and takes the prompt on standard
// input.
func TestRunProcessWindows(t *testing.T) {
	script, log, _ := fakeTool(t, "more > \""+filepath.Join(t.TempDir(), "stdin")+"\"\r\nexit /b 0\r\n")
	out, err := runProcess(context.Background(), script, []string{"exec", "-"},
		"hello from shu", map[string]string{"PATH": os.Getenv("PATH"), "OPENAI_API_KEY": ""}, 1<<20)
	if err != nil {
		t.Fatalf("run: %v (%q)", err, out)
	}
	args, pwd, secret := readToolLog(t, log)
	if args != "exec -" {
		t.Errorf("args = %q", args)
	}
	if secret != "" {
		t.Errorf("a variable the launch did not set reached the tool: %q", secret)
	}
	if pwd == "" || pwd == "." || filepath.Base(pwd) == "" {
		t.Errorf("no private working directory: %q", pwd)
	}
	if !strings.Contains(strings.ToLower(pwd), "shu-cli-") {
		t.Errorf("the working directory is not a run's own: %q", pwd)
	}
}

// A tool that does not end is killed as a whole when the request is
// cancelled, and the call returns the cancelled kind, not a hang.
func TestRunProcessCancelWindows(t *testing.T) {
	script, _, _ := fakeTool(t, "ping -n 30 127.0.0.1 > nul\r\nexit /b 0\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runProcess(ctx, script, nil, "", nil, 1<<20)
	// runProcess hands back the context's own error, which the service
	// classifies with cliError as it does any launch's; the kind is what
	// the window words for a tool that ran out of time or was called off.
	if kind := KindOf(cliError(ctx, err)); kind != KindCancelled && kind != KindTimeout {
		t.Fatalf("err = %v, kind %v", err, kind)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("cancel took %s", took)
	}
}

// pi's installer puts its command in the user's profile on Windows too, and
// adds that folder to PATH only for a shell: an app started from Explorer
// finds nothing there.
func TestFindPiInstallWindows(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".pi", "agent", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(bin, "pi.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\nexit /b 0\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", "")
	path, ok := cliFind("pi", "")
	if !ok || path != script {
		t.Errorf("pi found at %q, %v", path, ok)
	}
}

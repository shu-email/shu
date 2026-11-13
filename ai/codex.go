package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Codex's app-server speaks JSON-RPC over standard input and output. Shu
// starts it only to read the model list and the account state: one
// initialize handshake, then model/list. No thread, turn or prompt is
// created, so no mail and no model request is involved.

// CodexModels asks the installed Codex app-server which models it offers.
func (s *Service) CodexModels(ctx context.Context, explicit string) ([]string, error) {
	path, ok := cliFind("codex", explicit)
	if !ok {
		if strings.TrimSpace(explicit) != "" {
			return nil, &Error{Kind: KindCLIBadPath}
		}
		return nil, &Error{Kind: KindCLINotInstalled}
	}
	dir, err := os.MkdirTemp("", "shu-codex-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.Command(path, "app-server", "-c", "check_for_update_on_startup=false")
	cmd.Dir = dir
	cmd.Env = pairs(cliEnvironment(nil))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, cliError(ctx, err)
	}
	watchdog := time.AfterFunc(20*time.Second, func() { killCommand(cmd) })
	defer func() {
		watchdog.Stop()
		killCommand(cmd)
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	write := func(line string) error {
		_, err := io.WriteString(stdin, line+"\n")
		return err
	}
	if err := write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"shu","title":"Shu","version":"0"},"capabilities":{"experimentalApi":false}}}`); err != nil {
		return nil, cliError(ctx, err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	readID := func(id int) (map[string]any, bool, error) {
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			var msg map[string]any
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			if n, ok := msg["id"].(float64); !ok || int(n) != id {
				continue
			}
			if msg["error"] != nil {
				return nil, false, &Error{Kind: KindCLIFailed}
			}
			result, _ := msg["result"].(map[string]any)
			return result, true, nil
		}
		return nil, false, errors.New("the app-server closed")
	}
	if _, ok, err := readID(1); err != nil || !ok {
		if err == nil {
			err = &Error{Kind: KindCLIFailed}
		}
		return nil, cliError(ctx, err)
	}
	if err := write(`{"jsonrpc":"2.0","method":"initialized"}`); err != nil {
		return nil, cliError(ctx, err)
	}
	if err := write(`{"jsonrpc":"2.0","id":2,"method":"model/list","params":{"includeHidden":false,"limit":100}}`); err != nil {
		return nil, cliError(ctx, err)
	}
	result, ok, err := readID(2)
	if err != nil || !ok {
		if err == nil {
			err = &Error{Kind: KindCLIFailed}
		}
		return nil, cliError(ctx, err)
	}
	data, _ := result["data"].([]any)
	seen := map[string]bool{}
	var models []string
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["model"].(string)
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, &Error{Kind: KindBadResponse}
	}
	return models, nil
}

// pairs turns an environment map into the form exec.Cmd wants.
func pairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, fmt.Sprintf("%s=%s", name, value))
	}
	return out
}

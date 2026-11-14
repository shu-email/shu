package ai

import (
	"context"
	"strings"
	"time"
)

// pi is the pi coding agent. It is signed in to model providers of its
// own, and its own catalog says which of their models the account can
// reach: `pi --list-models` reads the credentials it already has and
// prints a table, making no model request. An account with no provider
// signed in prints a notice instead of a table, which is how a signed-out
// pi is told apart without reading anything of its own.

// PiModels asks the installed pi which models its account offers.
func (s *Service) PiModels(ctx context.Context, explicit string) ([]string, error) {
	path, ok := cliFind("pi", explicit)
	if !ok {
		if strings.TrimSpace(explicit) != "" {
			return nil, &Error{Kind: KindCLIBadPath}
		}
		return nil, &Error{Kind: KindCLINotInstalled}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := runProcess(ctx, path, []string{"--list-models"}, "", cliEnvironment(nil), 1<<20)
	if err != nil {
		return nil, cliError(ctx, err)
	}
	return piCatalog(out), nil
}

// piCatalog reads the table --list-models prints: a header, then a row per
// model with its provider first and its ID second. The provider is kept on
// the value, as `provider/id`, so that a model two providers share names
// the one the user picked.
func piCatalog(output string) []string {
	var models []string
	seen := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(stripANSI(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "provider" {
			inTable = true
			continue
		}
		if !inTable || len(fields) < 2 {
			continue
		}
		id := fields[0] + "/" + fields[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	return models
}

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ListModels asks an OpenAI-compatible or Anthropic endpoint which models
// it offers, with the connection's key. It sends no mail and stores
// nothing.
func (s *Service) ListModels(ctx context.Context, preset ProviderID, baseURL, key string) ([]string, error) {
	p, ok := ProviderByID(preset)
	if !ok || p.CLI != "" {
		return nil, &Error{Kind: KindBadConfig}
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = strings.TrimRight(p.BaseURL, "/")
	}
	if base == "" {
		return nil, &Error{Kind: KindBadConfig}
	}
	target := base + "/models"
	headers := map[string]string{}
	if p.Messages {
		if strings.HasSuffix(base, "/v1") {
			target = base + "/models"
		} else {
			target = base + "/v1/models"
		}
		headers["anthropic-version"] = "2023-06-01"
		headers["x-api-key"] = key
	} else if key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, &Error{Kind: KindBadConfig}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &Error{Kind: KindBadConfig}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	s.clientMu.RLock()
	client := s.client
	s.clientMu.RUnlock()
	resp, err := client.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(resp.StatusCode, data)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, &Error{Kind: KindBadResponse}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range parsed.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
	}
	if len(out) == 0 {
		return nil, &Error{Kind: KindBadResponse}
	}
	return out, nil
}

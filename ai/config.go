// Package ai is Shu's AI layer: the provider configuration, the excerpt
// that decides what part of a message may leave the device, the single
// egress point every model request passes through, and the summary,
// triage and draft features built on it.
//
// It is bring-your-own-key and local-first: keys live only in the OS
// keychain, nothing is sent unless the user asked for it, and the output
// is text for the user to review. It never imports the UI or the mail
// engine, and it never changes the mailbox.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ProviderID names a model provider.
type ProviderID string

// The providers the settings offer.
const (
	ProviderOpenAI    ProviderID = "openai"
	ProviderAnthropic ProviderID = "anthropic"
	ProviderDeepSeek  ProviderID = "deepseek"
	// ProviderLocal is an OpenAI-compatible server on this machine, such
	// as Ollama or LM Studio. It needs no key and no internet.
	ProviderLocal ProviderID = "local"
	// ProviderCustom is any OpenAI-compatible server the user names.
	ProviderCustom ProviderID = "custom"
	// ProviderClaudeCLI and ProviderCodexCLI are the user's installed,
	// already signed-in command-line tools.
	ProviderClaudeCLI ProviderID = "claude-code"
	ProviderCodexCLI  ProviderID = "codex"
)

// Provider is a preset of a provider's endpoint.
type Provider struct {
	ID ProviderID
	// Name is the brand name. Local and Custom have none: the UI names
	// them in the user's language.
	Name string
	// BaseURL is the endpoint's root, and Model a suggestion the user can
	// replace.
	BaseURL string
	Model   string
	// NeedsKey reports whether a request without a key is pointless.
	NeedsKey bool
	// EditableURL lets the user point the provider elsewhere.
	EditableURL bool
	// Messages selects Anthropic's Messages API over the OpenAI-compatible
	// chat completions every other provider speaks.
	Messages bool
	// CLI is the command of a local tool provider, "" for an HTTP one.
	CLI string
}

// Providers are the presets, in the order the settings list them.
var Providers = []Provider{
	{ID: ProviderOpenAI, Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", NeedsKey: true},
	{ID: ProviderAnthropic, Name: "Anthropic", BaseURL: "https://api.anthropic.com", Model: "claude-3-5-haiku-latest", NeedsKey: true, Messages: true},
	{ID: ProviderDeepSeek, Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", NeedsKey: true},
	{ID: ProviderLocal, BaseURL: "http://localhost:11434/v1", Model: "llama3.2", EditableURL: true},
	{ID: ProviderCustom, EditableURL: true},
	{ID: ProviderClaudeCLI, Name: "Claude Code", Model: "sonnet", CLI: "claude"},
	{ID: ProviderCodexCLI, Name: "Codex", Model: "gpt-5-codex", CLI: "codex"},
}

// ProviderByID returns a preset.
func ProviderByID(id ProviderID) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Feature is one AI capability, with a switch of its own.
type Feature string

// The capabilities of the first AI release.
const (
	FeatureSummary Feature = "summary"
	FeatureTriage  Feature = "triage"
	FeatureDraft   Feature = "draft"
)

// Features lists the capabilities in the order the settings show them.
var Features = []Feature{FeatureSummary, FeatureTriage, FeatureDraft}

// Config is the user's AI settings. It holds no secret: keys are in the
// keychain, under KeyName.
type Config struct {
	// Enabled is the master switch. It is off until the user turns it on.
	Enabled  bool       `json:"enabled"`
	Provider ProviderID `json:"provider"`
	// BaseURL overrides the provider's endpoint where that is editable.
	BaseURL string `json:"baseURL,omitempty"`
	// Model is always written, so a model the user cleared stays cleared
	// instead of turning back into the default.
	Model string `json:"model"`
	// AllowHTTP is the explicit opt-in to send mail text to a remote
	// server over plain HTTP. Loopback servers never need it.
	AllowHTTP bool `json:"allowHTTP,omitempty"`
	// Off holds the capabilities the user switched off. A capability is on
	// unless it is listed, so the master switch alone turns AI on.
	Off map[Feature]bool `json:"off,omitempty"`
	// Models are optional per-capability models; empty uses Model.
	Models map[Feature]string `json:"models,omitempty"`
	// Reviewed records the capabilities whose payload the user has
	// inspected and approved once.
	Reviewed map[Feature]bool `json:"reviewed,omitempty"`
	// CLIPaths are the explicit executable paths of local command-line
	// providers, by provider. Empty finds the command the way a Terminal
	// would.
	CLIPaths map[ProviderID]string `json:"cliPaths,omitempty"`
	// Connections are the API connections the user added.
	Connections []AIConnection `json:"connections,omitempty"`
	// Active is the connection in use, "" when none.
	Active string `json:"active,omitempty"`
	// CLIEnv holds the names of the environment values a local tool gets,
	// by provider; their values are only in the keychain.
	CLIEnv map[ProviderID][]string `json:"cliEnv,omitempty"`
	// HiddenModels are the models a provider reports that the user keeps
	// out of the model pickers, by provider. The chosen default is always
	// shown.
	HiddenModels map[ProviderID][]string `json:"hiddenModels,omitempty"`
}

// AIConnection is one API connection the user added.
type AIConnection struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Preset  ProviderID `json:"preset"`
	BaseURL string     `json:"baseURL,omitempty"`
	Model   string     `json:"model,omitempty"`
	Models  []string   `json:"models,omitempty"`
	Enabled bool       `json:"enabled"`
}

// Connection returns the connection of an ID.
func (c Config) Connection(id string) (AIConnection, bool) {
	for _, conn := range c.Connections {
		if conn.ID == id {
			return conn, true
		}
	}
	return AIConnection{}, false
}

// ActiveConnection returns the connection in use.
func (c Config) ActiveConnection() (AIConnection, bool) {
	if c.Active == "" {
		return AIConnection{}, false
	}
	return c.Connection(c.Active)
}

// SetConnection replaces or appends a connection.
func (c *Config) SetConnection(conn AIConnection) {
	for i := range c.Connections {
		if c.Connections[i].ID == conn.ID {
			c.Connections[i] = conn
			return
		}
	}
	c.Connections = append(c.Connections, conn)
}

// RemoveConnection drops a connection.
func (c *Config) RemoveConnection(id string) {
	out := c.Connections[:0]
	for _, conn := range c.Connections {
		if conn.ID != id {
			out = append(out, conn)
		}
	}
	c.Connections = out
	if c.Active == id {
		c.Active = ""
	}
}

// ConnectionKeyID is the keychain key of a connection.
func ConnectionKeyID(conn AIConnection) ProviderID { return ProviderID("conn:" + conn.ID) }

// ModelHidden reports whether a model is kept out of the pickers.
func (c Config) ModelHidden(id ProviderID, model string) bool {
	for _, m := range c.HiddenModels[id] {
		if m == model {
			return true
		}
	}
	return false
}

// SetModelHidden hides or shows a model a provider reports.
func (c *Config) SetModelHidden(id ProviderID, model string, hidden bool) {
	if c.HiddenModels == nil {
		c.HiddenModels = map[ProviderID][]string{}
	}
	list := c.HiddenModels[id]
	out := list[:0]
	for _, m := range list {
		if m != model {
			out = append(out, m)
		}
	}
	if hidden {
		out = append(out, model)
	}
	if len(out) == 0 {
		delete(c.HiddenModels, id)
		return
	}
	c.HiddenModels[id] = out
}

// DefaultConfig is what a new install starts with: AI off.
func DefaultConfig() Config {
	p := Providers[0]
	return Config{Provider: p.ID, Model: p.Model}
}

// Clone copies the configuration with its maps, so a background job can
// read one copy while the settings edit another.
func (c Config) Clone() Config {
	out := c
	out.Off = cloneMap(c.Off)
	out.Models = cloneMap(c.Models)
	out.Reviewed = cloneMap(c.Reviewed)
	out.CLIPaths = cloneProviderMap(c.CLIPaths)
	out.CLIEnv = cloneEnvMap(c.CLIEnv)
	out.HiddenModels = cloneEnvMap(c.HiddenModels)
	out.Connections = append([]AIConnection(nil), c.Connections...)
	return out
}

func cloneEnvMap(m map[ProviderID][]string) map[ProviderID][]string {
	if m == nil {
		return nil
	}
	out := make(map[ProviderID][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneProviderMap(m map[ProviderID]string) map[ProviderID]string {
	if m == nil {
		return nil
	}
	out := make(map[ProviderID]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneMap[V any](m map[Feature]V) map[Feature]V {
	if m == nil {
		return nil
	}
	out := make(map[Feature]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// FeatureOn reports whether a capability's own switch is on.
func (c Config) FeatureOn(f Feature) bool { return !c.Off[f] }

// SetFeature turns a capability's switch on or off.
func (c *Config) SetFeature(f Feature, on bool) {
	if on {
		delete(c.Off, f)
		return
	}
	if c.Off == nil {
		c.Off = map[Feature]bool{}
	}
	c.Off[f] = true
}

// ModelFor is the model a capability uses.
func (c Config) ModelFor(f Feature) string {
	if m := strings.TrimSpace(c.Models[f]); m != "" {
		return m
	}
	return strings.TrimSpace(c.Model)
}

// SetModelFor sets or clears a capability's own model.
func (c *Config) SetModelFor(f Feature, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		delete(c.Models, f)
		return
	}
	if c.Models == nil {
		c.Models = map[Feature]string{}
	}
	c.Models[f] = model
}

// HasReviewed reports whether the user approved a capability's payload.
func (c Config) HasReviewed(f Feature) bool { return c.Reviewed[f] }

// MarkReviewed records that approval.
func (c *Config) MarkReviewed(f Feature) {
	if c.Reviewed == nil {
		c.Reviewed = map[Feature]bool{}
	}
	c.Reviewed[f] = true
}

// UseProvider switches provider and takes its endpoint and suggested
// model, since those of the previous provider do not apply.
func (c *Config) UseProvider(id ProviderID) {
	p, ok := ProviderByID(id)
	if !ok {
		return
	}
	c.Provider = id
	c.BaseURL = ""
	c.Model = p.Model
	c.AllowHTTP = false
	c.Models = nil
}

// Endpoint is the base URL requests go to.
func (c Config) Endpoint() string {
	p, _ := ProviderByID(c.Provider)
	if p.EditableURL && strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	}
	return strings.TrimRight(p.BaseURL, "/")
}

// KeyName is the keychain key of a provider's API key.
func KeyName(id ProviderID) string { return "ai:" + string(id) + ":key" }

// configKey is where the settings table keeps the configuration.
const configKey = "ai.config"

// Store is what the AI layer keeps in the app's database: its settings
// and the cache of summaries. Neither holds a secret.
type Store interface {
	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	AICached(ctx context.Context, key string) (CacheEntry, bool, error)
	SaveAICache(ctx context.Context, e CacheEntry) error
}

// CacheEntry is one cached model answer. Key is a hash of everything that
// produced it, so a changed message, prompt or model never reuses it.
type CacheEntry struct {
	Key string
	// Scope ties the entry to what it was made from, the mail account, so
	// removing that removes the entry.
	Scope     string
	Feature   Feature
	Provider  ProviderID
	Model     string
	Text      string
	CreatedAt time.Time
}

// ErrSecretNotFound is what a Secrets returns for a key it does not hold.
var ErrSecretNotFound = errors.New("ai: no such secret")

// Secrets is the OS keychain. Production passes mygo.Secrets, tests an
// in-memory map.
type Secrets interface {
	Set(key string, secret []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// Config reads the settings, or the defaults of a new install.
func (s *Service) Config(ctx context.Context) (Config, error) {
	raw, ok, err := s.store.Setting(ctx, configKey)
	if err != nil {
		return DefaultConfig(), err
	}
	if !ok || raw == "" {
		return DefaultConfig(), nil
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		// Settings that do not parse are settings the user cannot have
		// meant: start from the defaults, with AI off.
		return DefaultConfig(), nil
	}
	if _, ok := ProviderByID(cfg.Provider); !ok {
		return DefaultConfig(), nil
	}
	return cfg, nil
}

// SaveConfig writes the settings. When they point at another server than
// the saved ones did, the user's approvals of what is sent are dropped:
// they were given for the old destination.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	if _, ok := ProviderByID(cfg.Provider); !ok {
		return &Error{Kind: KindBadConfig}
	}
	cfg = cfg.Clone()
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if old, err := s.Config(ctx); err == nil && (old.Provider != cfg.Provider || old.Endpoint() != cfg.Endpoint()) {
		cfg.Reviewed = nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, configKey, string(raw))
}

// KeyStored reports whether a provider's key is in the keychain. The key
// itself is never returned to a caller outside this package.
func (s *Service) KeyStored(id ProviderID) (bool, error) {
	_, err := s.secrets.Get(KeyName(id))
	if errors.Is(err, ErrSecretNotFound) {
		return false, nil
	}
	if err != nil {
		return false, &Error{Kind: KindKeychain}
	}
	return true, nil
}

// SetKey stores or replaces a provider's key in the keychain.
func (s *Service) SetKey(id ProviderID, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return &Error{Kind: KindNoKey}
	}
	if _, ok := ProviderByID(id); !ok && !strings.HasPrefix(string(id), "conn:") {
		return &Error{Kind: KindBadConfig}
	}
	if err := s.secrets.Set(KeyName(id), []byte(key)); err != nil {
		return &Error{Kind: KindKeychain}
	}
	return nil
}

// DeleteKey removes a provider's key from the keychain.
func (s *Service) DeleteKey(id ProviderID) error {
	if err := s.secrets.Delete(KeyName(id)); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return &Error{Kind: KindKeychain}
	}
	return nil
}

// key reads a provider's key for a request. It stays inside the package.
func (s *Service) key(id ProviderID) (string, error) {
	data, err := s.secrets.Get(KeyName(id))
	if errors.Is(err, ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", &Error{Kind: KindKeychain}
	}
	return strings.TrimSpace(string(data)), nil
}

// Secret reads a stored secret by its key name, for settings that need to
// reuse a key they do not show.
func (s *Service) Secret(id ProviderID) (string, error) {
	data, err := s.secrets.Get(KeyName(id))
	if err != nil {
		return "", &Error{Kind: KindKeychain}
	}
	return string(data), nil
}

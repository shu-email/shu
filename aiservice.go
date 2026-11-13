package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/egoist/mygo"

	"shu/ai"
)

// aiService is what the windows need from the AI layer. Production passes
// *ai.Service; tests pass a fake that answers at once. It has no method
// that returns a key: the windows can only learn whether one is stored.
type aiService interface {
	SetHTTPClient(c *http.Client)
	// CLIStatus checks a local tool with its version and sign-in commands
	// only, without a model request.
	CLIStatus(ctx context.Context, id ai.ProviderID, path string) ai.CLIStatus
	// CLIModels asks an installed tool which models it reports, without a
	// model request; nil leaves the field free text.
	CLIModels(ctx context.Context, id ai.ProviderID, path string) ([]string, error)
	// CodexModels asks the Codex app-server which models it offers, with
	// no thread and no model request.
	CodexModels(ctx context.Context, path string) ([]string, error)
	// CLIEnv reads and writes the values kept for one local tool; values
	// live only in the keychain.
	CLIEnv(id ai.ProviderID) ([]ai.CLIEnvVar, error)
	// Secret returns a stored secret, for a connection's models fetch.
	Secret(id ai.ProviderID) (string, error)
	// ListModels asks an API endpoint which models it offers, with no
	// mail and no model request.
	ListModels(ctx context.Context, preset ai.ProviderID, baseURL, key string) ([]string, error)
	SaveCLIEnv(ctx context.Context, id ai.ProviderID, list []ai.CLIEnvVar) error
	Config(ctx context.Context) (ai.Config, error)
	SaveConfig(ctx context.Context, cfg ai.Config) error
	KeyStored(id ai.ProviderID) (bool, error)
	SetKey(ctx context.Context, id ai.ProviderID, key string) error
	DeleteKey(id ai.ProviderID) error
	// RemoveProvider forgets a provider's key, a tool's values and its
	// settings, after the user confirmed it.
	RemoveProvider(ctx context.Context, id ai.ProviderID) error
	Test(ctx context.Context, cfg ai.Config, typedKey string) error

	// Check reports why a capability cannot run, without any network call.
	Check(ctx context.Context, f ai.Feature) error
	MarkReviewed(ctx context.Context, f ai.Feature) error

	SummaryPayload(ctx context.Context, m ai.Message) (ai.Payload, error)
	Summarize(ctx context.Context, m ai.Message) (ai.Result, error)
	ConversationPayload(ctx context.Context, c ai.Conversation) (ai.Payload, error)
	SummarizeConversation(ctx context.Context, c ai.Conversation) (ai.Result, error)
	TriagePayload(ctx context.Context, items []ai.TriageItem, language string) (ai.Payload, error)
	Triage(ctx context.Context, items []ai.TriageItem, language string) (ai.TriageResult, error)
	DraftPayload(ctx context.Context, m ai.Message, guidance string) (ai.Payload, error)
	Draft(ctx context.Context, m ai.Message, guidance string) (ai.Result, error)
}

// aiKeychain adapts mygo.Secrets to the AI layer's Secrets.
type aiKeychain struct{}

func (aiKeychain) Set(key string, secret []byte) error { return mygo.Secrets.Set(key, secret) }
func (aiKeychain) Get(key string) ([]byte, error) {
	data, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, ai.ErrSecretNotFound
	}
	return data, err
}
func (aiKeychain) Delete(key string) error { return mygo.Secrets.Delete(key) }

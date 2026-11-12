package store

import (
	"context"

	"shu/ai"
)

// Setting reads one app setting from the kv table. Settings are small
// values the app needs before any network call; secrets never go here.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	rows, err := s.query(ctx, `SELECT value FROM kv WHERE key = ?`, key)
	if err != nil {
		return "", false, err
	}
	if len(rows.Values) == 0 {
		return "", false, nil
	}
	return text(rows.Values[0][0]), true, nil
}

// SetSetting writes one app setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.exec(ctx, `INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// AICached reads a cached AI answer by the hash of its request.
func (s *Store) AICached(ctx context.Context, key string) (ai.CacheEntry, bool, error) {
	rows, err := s.query(ctx, `SELECT key, COALESCE(account_id, ''), feature, provider, model, result, created_at
		FROM ai_cache WHERE key = ?`, key)
	if err != nil {
		return ai.CacheEntry{}, false, err
	}
	if len(rows.Values) == 0 {
		return ai.CacheEntry{}, false, nil
	}
	r := rows.Values[0]
	return ai.CacheEntry{
		Key:       text(r[0]),
		Scope:     text(r[1]),
		Feature:   ai.Feature(text(r[2])),
		Provider:  ai.ProviderID(text(r[3])),
		Model:     text(r[4]),
		Text:      text(r[5]),
		CreatedAt: unix(r[6]),
	}, true, nil
}

// SaveAICache stores an AI answer. An entry whose scope is not an account
// is kept without one rather than refused.
func (s *Store) SaveAICache(ctx context.Context, e ai.CacheEntry) error {
	var account any
	if e.Scope != "" {
		if _, ok, err := s.Account(ctx, e.Scope); err != nil {
			return err
		} else if ok {
			account = e.Scope
		}
	}
	_, err := s.exec(ctx, `INSERT INTO ai_cache (key, account_id, feature, provider, model, result, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			account_id=excluded.account_id, feature=excluded.feature, provider=excluded.provider,
			model=excluded.model, result=excluded.result, created_at=excluded.created_at`,
		e.Key, account, string(e.Feature), string(e.Provider), e.Model, e.Text, e.CreatedAt.Unix())
	return err
}

package store

import (
	"context"
	"fmt"
)

// Keys of the settings table.
const (
	settingLanguage = "language"
	settingProvider = "provider"
)

// Settings are the user's app settings (SPEC §4.6). The zero value is the
// default for every field. Secrets never go here (SPEC §8).
type Settings struct {
	// Language is the interface language code ("fr", "en"), or "" to
	// follow the browser. Not validated here: see i18n.Parse.
	Language string
	// Provider is the sending mode: "" = test (default), "graph" =
	// Microsoft 365 (mailer.ProviderGraph). Not validated here.
	Provider string
}

// Settings reads the settings. Missing keys keep their zero value;
// unknown keys are ignored.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()

	var st Settings
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, fmt.Errorf("scan setting: %w", err)
		}
		switch k {
		case settingLanguage:
			st.Language = v
		case settingProvider:
			st.Provider = v
		}
	}
	if err := rows.Err(); err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	return st, nil
}

// SaveSettings writes every field of st in one transaction (upsert per
// key, updated_at set).
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	defer tx.Rollback()

	now := s.timestamp()
	for _, kv := range [][2]string{{settingLanguage, st.Language}, {settingProvider, st.Provider}} {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			kv[0], kv[1], now)
		if err != nil {
			return fmt.Errorf("save setting %s: %w", kv[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

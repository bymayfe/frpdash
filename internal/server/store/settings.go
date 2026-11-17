package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SettingsRepo persists the global settings layer as JSON values by key.
type SettingsRepo struct{ base }

// Get decodes key into dst, or returns ErrNotFound.
func (r *SettingsRepo) Get(ctx context.Context, key string, dst any) error {
	var raw string
	err := r.q.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("decode setting %q: %w", key, err)
	}
	return nil
}

// Set stores v as JSON under key, replacing any previous value.
func (r *SettingsRepo) Set(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %q: %w", key, err)
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		key, string(raw), r.unixNow())
	return err
}

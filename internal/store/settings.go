package store

import (
	"context"
	"fmt"
	"time"
)

// GetSettings returns every explicitly-set global flag value, keyed by flag
// name, in its stored scalar form ("5", "true").
func (db *DB) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("store: list settings: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: scan setting: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate settings: %w", err)
	}
	return out, nil
}

// SetSetting stores a global flag value.
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("store: set setting %s: %w", key, err)
	}
	return nil
}

// UnsetSetting removes a global flag value. ErrNotFound when it was not set.
func (db *DB) UnsetSetting(ctx context.Context, key string) error {
	res, err := db.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("store: unset setting %s: %w", key, err)
	}
	return requireAffected(res, "setting")
}

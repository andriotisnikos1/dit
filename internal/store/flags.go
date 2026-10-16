package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Pending-counter columns, managed only through IncrementPendingCount and
// ResetPendingCount. The whitelist keeps column names out of SQL built from
// a string.
const (
	PendingNotifyCount = "pending_notify_count"
	PendingSemverCount = "pending_semver_count"
)

var pendingColumns = map[string]bool{
	PendingNotifyCount: true,
	PendingSemverCount: true,
}

// SetWatchFlag stores one per-watch flag override. Values were validated by
// the flags package before they got here.
func (db *DB) SetWatchFlag(ctx context.Context, watchID, name string, value any) error {
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx,
			`SELECT flags_json FROM watches WHERE id = ?`, watchID).Scan(&raw); err != nil {
			return translateErr(err)
		}
		flags := map[string]any{}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &flags); err != nil {
				return fmt.Errorf("store: decode watch flags: %w", err)
			}
		}
		flags[name] = value
		encoded, err := json.Marshal(flags)
		if err != nil {
			return fmt.Errorf("store: encode watch flags: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE watches SET flags_json = ?, updated_at = ? WHERE id = ?`,
			string(encoded), formatTime(time.Now()), watchID)
		if err != nil {
			return translateErr(err)
		}
		return requireAffected(res, "watch")
	})
	return err
}

// UnsetWatchFlag removes one per-watch flag override; the flag then falls
// back to the global value or the default. ErrNotFound when the watch is
// unknown or the flag was not set locally.
func (db *DB) UnsetWatchFlag(ctx context.Context, watchID, name string) error {
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx,
			`SELECT flags_json FROM watches WHERE id = ?`, watchID).Scan(&raw); err != nil {
			return translateErr(err)
		}
		if raw == "" {
			return ErrNotFound
		}
		flags := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &flags); err != nil {
			return fmt.Errorf("store: decode watch flags: %w", err)
		}
		if _, ok := flags[name]; !ok {
			return ErrNotFound
		}
		delete(flags, name)
		encoded, err := json.Marshal(flags)
		if err != nil {
			return fmt.Errorf("store: encode watch flags: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE watches SET flags_json = ?, updated_at = ? WHERE id = ?`,
			string(encoded), formatTime(time.Now()), watchID)
		if err != nil {
			return translateErr(err)
		}
		return requireAffected(res, "watch")
	})
	return err
}

// IncrementPendingCount adds delta to a pending counter and returns the new
// value.
func (db *DB) IncrementPendingCount(ctx context.Context, watchID, column string, delta int) (int, error) {
	if !pendingColumns[column] {
		return 0, fmt.Errorf("store: not a pending counter column: %s", column)
	}
	var n int
	err := db.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT `+column+` FROM watches WHERE id = ?`, watchID).Scan(&n); err != nil {
			return translateErr(err)
		}
		n += delta
		res, err := tx.ExecContext(ctx,
			`UPDATE watches SET `+column+` = ?, updated_at = ? WHERE id = ?`,
			n, formatTime(time.Now()), watchID)
		if err != nil {
			return translateErr(err)
		}
		return requireAffected(res, "watch")
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ResetPendingCount zeroes a pending counter.
func (db *DB) ResetPendingCount(ctx context.Context, watchID, column string) error {
	if !pendingColumns[column] {
		return fmt.Errorf("store: not a pending counter column: %s", column)
	}
	res, err := db.db.ExecContext(ctx,
		`UPDATE watches SET `+column+` = 0, updated_at = ? WHERE id = ?`,
		formatTime(time.Now()), watchID)
	if err != nil {
		return fmt.Errorf("store: reset %s: %w", column, err)
	}
	return requireAffected(res, "watch")
}

// SetLatestSemver records the highest version a semver watch has seen.
func (db *DB) SetLatestSemver(ctx context.Context, watchID, version, tag string) error {
	res, err := db.db.ExecContext(ctx,
		`UPDATE watches SET latest_semver = ?, latest_semver_tag = ?, updated_at = ? WHERE id = ?`,
		version, tag, formatTime(time.Now()), watchID)
	if err != nil {
		return fmt.Errorf("store: set latest semver: %w", err)
	}
	return requireAffected(res, "watch")
}

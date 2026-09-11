package store

import (
	"context"
	"fmt"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// Counts summarises the database for GET /api/v1/status.
type Counts struct {
	Watches    apitypes.WatchCounts
	Channels   int
	Registries int
	Events     int
}

// Stats returns the aggregate counts used by the status endpoint.
func (db *DB) Stats(ctx context.Context) (Counts, error) {
	var out Counts
	err := db.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN enabled = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN consecutive_failures > 0 THEN 1 ELSE 0 END), 0)
		FROM watches`).Scan(
		&out.Watches.Total, &out.Watches.Enabled, &out.Watches.Disabled, &out.Watches.Failing)
	if err != nil {
		return out, fmt.Errorf("store: watch counts: %w", err)
	}

	if out.Channels, err = db.count(ctx, "channels"); err != nil {
		return out, err
	}
	if out.Events, err = db.CountEvents(ctx); err != nil {
		return out, err
	}
	if out.Registries, err = db.CountRegistries(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func (db *DB) count(ctx context.Context, table string) (int, error) {
	var n int
	// table is a package-internal constant, never user input.
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count %s: %w", table, err)
	}
	return n, nil
}

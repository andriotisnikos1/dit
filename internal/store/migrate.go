package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// Migrate applies every embedded migration that has not been applied yet, in
// filename order, each inside its own transaction. Applied versions are
// tracked in schema_migrations.
func (db *DB) Migrate(ctx context.Context, fsys fs.FS) error {
	if fsys == nil {
		return fmt.Errorf("store: migrations filesystem is nil")
	}

	if _, err := db.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, err := db.AppliedMigrations(ctx)
	if err != nil {
		return err
	}

	names, err := migrationNames(fsys)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		// Silently applying nothing would leave a server running against an
		// empty schema, so fail loudly instead.
		return fmt.Errorf("store: no migrations found in the embedded filesystem")
	}

	for _, name := range names {
		version := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if _, ok := applied[version]; ok {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}

		err = db.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("store: apply migration %s: %w", name, err)
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				version, formatTime(time.Now()))
			if err != nil {
				return fmt.Errorf("store: record migration %s: %w", name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// AppliedMigrations returns the set of applied migration versions.
func (db *DB) AppliedMigrations(ctx context.Context) (map[string]struct{}, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: list applied migrations: %w", err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: scan migration version: %w", err)
		}
		out[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate migrations: %w", err)
	}
	return out, nil
}

// migrationNames lists *.sql files in fsys, sorted by name.
func migrationNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Package store owns the SQLite database: schema, migrations and every query.
//
// The database handle is deliberately limited to a single connection. SQLite
// allows only one writer at a time, and a single connection makes write
// ordering deterministic and eliminates SQLITE_BUSY entirely. Every statement
// here is short; long-running work belongs in the check engine, not in SQL.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/crypto"

	_ "modernc.org/sqlite" // pure-Go driver: CGO_ENABLED=0 builds work
)

// Sentinel errors returned by store methods.
var (
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate reports a uniqueness violation.
	ErrDuplicate = errors.New("already exists")
)

// timeLayout is fixed-width so that TEXT ordering equals chronological order.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Options configures Open.
type Options struct {
	// Path is the SQLite file. ":memory:" is supported for tests.
	Path string
	// Sealer encrypts channel secrets and registry credentials. Required.
	Sealer *crypto.Sealer
	// Migrations is the embedded migration filesystem. Optional: when nil,
	// Migrate must be called with an explicit filesystem.
	Migrations fs.FS
}

// DB is the store handle.
type DB struct {
	db     *sql.DB
	sealer *crypto.Sealer
	path   string
}

// Open opens (creating if needed) the SQLite database and applies migrations.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Sealer == nil {
		return nil, errors.New("store: sealer is required")
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = "dit.db"
	}

	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("store: create data dir %s: %w", dir, err)
			}
		}
	}

	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection: see the package comment.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	db := &DB{db: sqlDB, sealer: opts.Sealer, path: path}
	if opts.Migrations != nil {
		if err := db.Migrate(ctx, opts.Migrations); err != nil {
			sqlDB.Close()
			return nil, err
		}
	}
	return db, nil
}

// dsn builds the modernc.org/sqlite connection string with the pragmas the
// plan calls for: WAL journalling, enforced foreign keys and a busy timeout.
func dsn(path string) string {
	if path == ":memory:" {
		// A shared-cache in-memory database is per-name, so each Open gets a
		// unique name. Reusing one name would make every in-memory store in
		// the process alias the same database.
		name := "dit-mem-" + NewID("")
		return "file:" + name + "?mode=memory&cache=shared" +
			"&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + q.Encode()
}

// Path returns the database file this handle was opened on.
func (db *DB) Path() string { return db.path }

// Close closes the underlying handle.
func (db *DB) Close() error {
	if db == nil || db.db == nil {
		return nil
	}
	return db.db.Close()
}

// SQL exposes the raw handle for tests and for statements the typed API does
// not cover.
func (db *DB) SQL() *sql.DB { return db.db }

// Sealer exposes the sealer in use, so the server can report key provenance.
func (db *DB) Sealer() *crypto.Sealer { return db.sealer }

// withTx runs fn inside a transaction, rolling back on error.
func (db *DB) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// translateErr maps driver errors onto the store sentinels.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed") {
		if strings.Contains(msg, "unique") {
			return fmt.Errorf("%w: %v", ErrDuplicate, err)
		}
	}
	return err
}

// idEncoding produces lowercase, padding-free base32 — the same alphabet used
// for the prefixed IDs (w_…, c_…, e_…).
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns a short random identifier with the given prefix, e.g. "w_".
func NewID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand never fails on supported platforms; if it somehow does,
		// fall back to a timestamp rather than returning an empty ID.
		return prefix + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + idEncoding.EncodeToString(buf)
}

// ---------- time helpers ----------

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func formatTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

func timePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	return string(raw), nil
}

func unmarshalJSONMap(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return out, nil
}

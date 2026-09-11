package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// CredentialRecord is a stored registry credential. Secret holds the decrypted
// password or token; it is empty when the record was loaded metadata-only.
type CredentialRecord struct {
	Registry   string
	Username   string
	Secret     string
	Kind       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
	LastOKAt   *time.Time
}

// PutCredentials inserts or replaces the credentials for a registry, sealing
// the secret.
func (db *DB) PutCredentials(ctx context.Context, registry, username, secret, kind string) (*CredentialRecord, error) {
	registry = strings.TrimSpace(registry)
	if registry == "" {
		return nil, fmt.Errorf("store: registry host is required")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("store: credential secret is required")
	}
	if kind == "" {
		kind = apitypes.CredentialBasic
	}
	sealed, err := db.sealer.SealString(secret)
	if err != nil {
		return nil, fmt.Errorf("store: seal registry secret: %w", err)
	}

	now := time.Now().UTC()
	_, err = db.db.ExecContext(ctx, `
		INSERT INTO registry_creds (registry, username, secret_enc, kind, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (registry) DO UPDATE SET
			username = excluded.username,
			secret_enc = excluded.secret_enc,
			kind = excluded.kind,
			updated_at = excluded.updated_at`,
		registry, strings.TrimSpace(username), sealed, kind, formatTime(now), formatTime(now))
	if err != nil {
		return nil, translateErr(err)
	}
	return db.GetCredentials(ctx, registry)
}

// GetCredentials loads the credentials for a registry with the secret
// decrypted. Returns ErrNotFound when none are stored.
func (db *DB) GetCredentials(ctx context.Context, registry string) (*CredentialRecord, error) {
	var (
		c                    CredentialRecord
		secretEnc            string
		createdAt, updatedAt string
		lastUsed, lastOK     = newNullString(), newNullString()
	)
	err := db.db.QueryRowContext(ctx, `
		SELECT registry, username, secret_enc, kind, created_at, updated_at, last_used_at, last_ok_at
		FROM registry_creds WHERE registry = ?`, strings.TrimSpace(registry)).
		Scan(&c.Registry, &c.Username, &secretEnc, &c.Kind, &createdAt, &updatedAt, lastUsed.ptr(), lastOK.ptr())
	if err != nil {
		return nil, translateErr(err)
	}

	plain, err := db.sealer.OpenString(secretEnc)
	if err != nil {
		return nil, fmt.Errorf("store: open registry secret for %s: %w", c.Registry, err)
	}
	c.Secret = plain

	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	if c.LastUsedAt, err = lastUsed.value(); err != nil {
		return nil, err
	}
	if c.LastOKAt, err = lastOK.value(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCredentials returns every stored credential, metadata-only: Secret is
// left empty so the values can never leak into a response.
func (db *DB) ListCredentials(ctx context.Context) ([]*CredentialRecord, error) {
	rows, err := db.db.QueryContext(ctx, `
		SELECT registry, username, kind, created_at, updated_at, last_used_at, last_ok_at
		FROM registry_creds ORDER BY registry`)
	if err != nil {
		return nil, fmt.Errorf("store: list registry creds: %w", err)
	}
	defer rows.Close()

	out := []*CredentialRecord{}
	for rows.Next() {
		var (
			c                    CredentialRecord
			createdAt, updatedAt string
			lastUsed, lastOK     = newNullString(), newNullString()
		)
		if err := rows.Scan(&c.Registry, &c.Username, &c.Kind, &createdAt, &updatedAt,
			lastUsed.ptr(), lastOK.ptr()); err != nil {
			return nil, translateErr(err)
		}
		if c.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		if c.LastUsedAt, err = lastUsed.value(); err != nil {
			return nil, err
		}
		if c.LastOKAt, err = lastOK.value(); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate registry creds: %w", err)
	}
	return out, nil
}

// DeleteCredentials removes stored credentials for a registry.
func (db *DB) DeleteCredentials(ctx context.Context, registry string) error {
	res, err := db.db.ExecContext(ctx, `DELETE FROM registry_creds WHERE registry = ?`,
		strings.TrimSpace(registry))
	if err != nil {
		return fmt.Errorf("store: delete registry creds: %w", err)
	}
	return requireAffected(res, "registry credentials")
}

// TouchCredentials records that stored credentials were used, and whether the
// registry accepted them.
func (db *DB) TouchCredentials(ctx context.Context, registry string, ok bool) error {
	now := formatTime(time.Now())
	var lastOK any
	if ok {
		lastOK = now
	}
	res, err := db.db.ExecContext(ctx, `
		UPDATE registry_creds SET last_used_at = ?, last_ok_at = COALESCE(?, last_ok_at)
		WHERE registry = ?`, now, lastOK, strings.TrimSpace(registry))
	if err != nil {
		return fmt.Errorf("store: touch registry creds: %w", err)
	}
	return requireAffected(res, "registry credentials")
}

// RegistryHosts returns every registry host referenced by a watch or holding
// credentials, with the watch count and credential metadata attached.
func (db *DB) RegistryHosts(ctx context.Context) ([]*apitypes.Registry, error) {
	rows, err := db.db.QueryContext(ctx, `
		SELECT h.host,
		       COALESCE(rc.username, '')      AS username,
		       COALESCE(rc.kind, '')          AS kind,
		       rc.registry IS NOT NULL        AS has_creds,
		       COALESCE(rc.last_used_at, '')  AS last_used_at,
		       COALESCE(rc.last_ok_at, '')    AS last_ok_at,
		       COALESCE(rc.created_at, '')    AS created_at,
		       COALESCE(rc.updated_at, '')    AS updated_at,
		       COALESCE(w.n, 0)               AS watches
		FROM (
			SELECT registry AS host FROM watches
			UNION
			SELECT registry AS host FROM registry_creds
		) h
		LEFT JOIN registry_creds rc ON rc.registry = h.host
		LEFT JOIN (
			SELECT registry, COUNT(*) AS n FROM watches GROUP BY registry
		) w ON w.registry = h.host
		ORDER BY h.host`)
	if err != nil {
		return nil, fmt.Errorf("store: list registries: %w", err)
	}
	defer rows.Close()

	out := []*apitypes.Registry{}
	for rows.Next() {
		var (
			r                              apitypes.Registry
			hasCreds                       int
			lastUsed, lastOK, created, upd string
		)
		if err := rows.Scan(&r.Host, &r.Username, &r.Kind, &hasCreds,
			&lastUsed, &lastOK, &created, &upd, &r.Watches); err != nil {
			return nil, fmt.Errorf("store: scan registry: %w", err)
		}
		r.HasCredentials = hasCreds != 0
		var err error
		if r.LastUsedAt, err = parseOptional(lastUsed); err != nil {
			return nil, err
		}
		if r.LastOKAt, err = parseOptional(lastOK); err != nil {
			return nil, err
		}
		if r.CreatedAt, err = parseOptional(created); err != nil {
			return nil, err
		}
		if r.UpdatedAt, err = parseOptional(upd); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate registries: %w", err)
	}
	return out, nil
}

// CountRegistries returns how many distinct registry hosts are known.
func (db *DB) CountRegistries(ctx context.Context) (int, error) {
	var n int
	err := db.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT registry FROM watches UNION SELECT registry FROM registry_creds
		)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count registries: %w", err)
	}
	return n, nil
}

// ---------- small helpers ----------

// nullString is a tiny helper that keeps Scan destinations tidy.
type nullString struct {
	ns  sql.NullString
	set bool
}

func newNullString() *nullString { return &nullString{} }

func (n *nullString) ptr() any {
	n.set = true
	return &n.ns
}

func (n *nullString) value() (*time.Time, error) {
	if !n.set {
		return nil, nil
	}
	return timePtr(n.ns)
}

func parseOptional(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, err := parseTime(raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

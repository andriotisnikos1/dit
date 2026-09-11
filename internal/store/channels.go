package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// SecretConfigKeys are the channel config keys whose values are sealed at rest
// and never returned by the API.
var SecretConfigKeys = []string{apitypes.ConfigPassword, apitypes.ConfigToken}

// ChannelRecord is the stored form of a channel, with secrets decrypted.
// Callers must redact before exposing it over the API.
type ChannelRecord struct {
	ID        string
	Name      string
	Type      apitypes.ChannelType
	Config    map[string]string
	Enabled   bool
	IsDefault bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasSecret reports whether the channel carries a sealed secret.
func (c *ChannelRecord) HasSecret() bool {
	for _, key := range SecretConfigKeys {
		if c.Config[key] != "" {
			return true
		}
	}
	return false
}

// Redacted returns a copy safe to hand to an API caller: secrets are replaced
// by empty strings.
func (c *ChannelRecord) Redacted() *ChannelRecord {
	clone := *c
	clone.Config = make(map[string]string, len(c.Config))
	for k, v := range c.Config {
		clone.Config[k] = v
	}
	for _, key := range SecretConfigKeys {
		if _, ok := clone.Config[key]; ok {
			clone.Config[key] = ""
		}
	}
	return &clone
}

// CreateChannelInput is the input to CreateChannel.
type CreateChannelInput struct {
	Name      string
	Type      apitypes.ChannelType
	Config    map[string]string
	Enabled   bool
	IsDefault bool
}

// UpdateChannelInput carries the mutable fields of a channel.
type UpdateChannelInput struct {
	Name      *string
	Config    *map[string]string
	Enabled   *bool
	IsDefault *bool
}

// ChannelFilter narrows ListChannels.
type ChannelFilter struct {
	Type    apitypes.ChannelType
	Enabled *bool
	Limit   int
	Offset  int
}

const channelColumns = `id, name, type, config_json, enabled, is_default, created_at, updated_at`

// CreateChannel inserts a channel, sealing any secret config values.
func (db *DB) CreateChannel(ctx context.Context, in CreateChannelInput) (*ChannelRecord, error) {
	if !in.Type.Valid() {
		return nil, fmt.Errorf("store: invalid channel type %q", in.Type)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("store: channel name is required")
	}
	encoded, err := db.encodeChannelConfig(in.Config)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := NewID("c_")

	_, err = db.db.ExecContext(ctx, `
		INSERT INTO channels (id, name, type, config_json, enabled, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, string(in.Type), encoded, boolInt(in.Enabled), boolInt(in.IsDefault),
		formatTime(now), formatTime(now))
	if err != nil {
		return nil, translateErr(err)
	}
	return db.GetChannel(ctx, id)
}

// GetChannel loads one channel with secrets decrypted.
func (db *DB) GetChannel(ctx context.Context, id string) (*ChannelRecord, error) {
	row := db.db.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	return db.scanChannel(row)
}

// FindChannelByName loads a channel by its unique name.
func (db *DB) FindChannelByName(ctx context.Context, name string) (*ChannelRecord, error) {
	row := db.db.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE name = ?`,
		strings.TrimSpace(name))
	return db.scanChannel(row)
}

// ListChannels returns channels matching filter plus the pre-pagination total.
func (db *DB) ListChannels(ctx context.Context, f ChannelFilter) ([]*ChannelRecord, int, error) {
	clauses := []string{}
	args := []any{}
	if f.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, string(f.Type))
	}
	if f.Enabled != nil {
		clauses = append(clauses, "enabled = ?")
		args = append(args, boolInt(*f.Enabled))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count channels: %w", err)
	}

	args = append(args, apitypes.ClampLimit(f.Limit), max(f.Offset, 0))
	rows, err := db.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels`+where+` ORDER BY name LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list channels: %w", err)
	}
	defer rows.Close()

	out := []*ChannelRecord{}
	for rows.Next() {
		c, err := db.scanChannel(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate channels: %w", err)
	}
	return out, total, nil
}

// DefaultChannels returns the enabled channels flagged as defaults. These are
// what a watch with no explicit subscriptions notifies.
func (db *DB) DefaultChannels(ctx context.Context) ([]*ChannelRecord, error) {
	rows, err := db.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE is_default = 1 AND enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list default channels: %w", err)
	}
	defer rows.Close()

	out := []*ChannelRecord{}
	for rows.Next() {
		c, err := db.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate default channels: %w", err)
	}
	return out, nil
}

// ChannelsForWatch resolves the channels a watch notifies: its explicit
// subscriptions when it has any, otherwise the default set. Disabled channels
// are excluded.
func (db *DB) ChannelsForWatch(ctx context.Context, watchID string) ([]*ChannelRecord, error) {
	ids, err := db.WatchChannelIDs(ctx, watchID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return db.DefaultChannels(ctx)
	}
	out := make([]*ChannelRecord, 0, len(ids))
	for _, id := range ids {
		c, err := db.GetChannel(ctx, id)
		if err != nil {
			if IsNotFound(err) {
				continue // subscription outlived its channel; ignore
			}
			return nil, err
		}
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

// UpdateChannel applies a partial update. Secrets present in a replacement
// config overwrite the stored ones; secrets omitted are preserved.
func (db *DB) UpdateChannel(ctx context.Context, id string, in UpdateChannelInput) (*ChannelRecord, error) {
	current, err := db.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}

	merged := current.Config
	if in.Config != nil {
		merged = make(map[string]string, len(current.Config)+len(*in.Config))
		for k, v := range current.Config {
			merged[k] = v
		}
		for k, v := range *in.Config {
			// An empty secret means "keep what is stored": the API only ever
			// sees redacted values, so echoing them back must not wipe them.
			if isSecretKey(k) && strings.TrimSpace(v) == "" {
				continue
			}
			merged[k] = v
		}
	}

	encoded, err := db.encodeChannelConfig(merged)
	if err != nil {
		return nil, err
	}

	sets := []string{"config_json = ?"}
	args := []any{encoded}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, fmt.Errorf("store: channel name cannot be empty")
		}
		sets = append(sets, "name = ?")
		args = append(args, name)
	}
	if in.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolInt(*in.Enabled))
	}
	if in.IsDefault != nil {
		sets = append(sets, "is_default = ?")
		args = append(args, boolInt(*in.IsDefault))
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(time.Now()), id)

	res, err := db.db.ExecContext(ctx,
		`UPDATE channels SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	if err := requireAffected(res, "channel"); err != nil {
		return nil, err
	}
	return db.GetChannel(ctx, id)
}

// DeleteChannel removes a channel; watch_channels and notifications cascade.
func (db *DB) DeleteChannel(ctx context.Context, id string) error {
	res, err := db.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete channel: %w", err)
	}
	return requireAffected(res, "channel")
}

// ---------- config encoding ----------

// encodeChannelConfig JSON-encodes a channel config, sealing secret values.
// Sealed values are stored with a "sealed:" prefix so that plaintext and
// ciphertext can never be confused.
func (db *DB) encodeChannelConfig(cfg map[string]string) (string, error) {
	encoded := map[string]string{}
	for k, v := range cfg {
		if isSecretKey(k) {
			if v == "" {
				continue
			}
			sealed, err := db.sealer.SealString(v)
			if err != nil {
				return "", fmt.Errorf("store: seal channel config %s: %w", k, err)
			}
			encoded[k] = sealedPrefix + sealed
			continue
		}
		encoded[k] = v
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		return "", fmt.Errorf("store: encode channel config: %w", err)
	}
	return string(raw), nil
}

// decodeChannelConfig JSON-decodes a channel config, opening sealed values.
func (db *DB) decodeChannelConfig(raw string) (map[string]string, error) {
	cfg := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("store: decode channel config: %w", err)
	}
	for k, v := range cfg {
		if !strings.HasPrefix(v, sealedPrefix) {
			continue
		}
		plain, err := db.sealer.OpenString(strings.TrimPrefix(v, sealedPrefix))
		if err != nil {
			return nil, fmt.Errorf("store: open channel config %s: %w", k, err)
		}
		cfg[k] = plain
	}
	return cfg, nil
}

// sealedPrefix marks an encrypted config value.
const sealedPrefix = "sealed:"

func isSecretKey(key string) bool {
	for _, k := range SecretConfigKeys {
		if k == key {
			return true
		}
	}
	return false
}

func (db *DB) scanChannel(s scanner) (*ChannelRecord, error) {
	var (
		c                    ChannelRecord
		ctype, configJSON    string
		enabled, isDefault   int
		createdAt, updatedAt string
	)
	if err := s.Scan(&c.ID, &c.Name, &ctype, &configJSON, &enabled, &isDefault,
		&createdAt, &updatedAt); err != nil {
		return nil, translateErr(err)
	}
	c.Type = apitypes.ChannelType(ctype)
	c.Enabled = enabled != 0
	c.IsDefault = isDefault != 0

	cfg, err := db.decodeChannelConfig(configJSON)
	if err != nil {
		return nil, err
	}
	c.Config = cfg

	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

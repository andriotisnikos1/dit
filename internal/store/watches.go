package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// WatchRecord is the stored form of a watch, with its explicit channel
// subscriptions resolved into IDs.
type WatchRecord struct {
	ID                  string
	Image               string
	Registry            string
	Repository          string
	Kind                apitypes.WatchKind
	Ref                 string
	Enabled             bool
	NotifyOnFailure     bool
	LastDigest          string
	LastCheckedAt       *time.Time
	LastOKAt            *time.Time
	BaselineAt          *time.Time
	LastError           string
	ConsecutiveFailures int
	NextAttemptAt       *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time

	// ChannelIDs are the explicit subscriptions. Empty means "use defaults".
	ChannelIDs []string
}

// HasBaseline reports whether the watch has completed at least one successful
// check, which is what makes the next check a diff rather than a baseline.
//
// This is tracked explicitly rather than inferred from the data a check
// produced: a pattern watch created before any matching tag exists legitimately
// produces an empty tag set, and a tag watch whose early checks all failed has
// no digest yet. In both cases the first *successful* check is the baseline and
// must stay silent.
func (w *WatchRecord) HasBaseline() bool { return w.BaselineAt != nil }

// CreateWatchInput is the input to CreateWatch.
type CreateWatchInput struct {
	Image           string
	Registry        string
	Repository      string
	Kind            apitypes.WatchKind
	Ref             string
	Enabled         bool
	NotifyOnFailure bool
	Channels        []string
	// NextAttemptAt defaults to now, so a new watch is checked on the next tick.
	NextAttemptAt *time.Time
}

// UpdateWatchInput carries the mutable fields of a watch. Nil means "leave
// unchanged".
type UpdateWatchInput struct {
	Enabled         *bool
	NotifyOnFailure *bool
	Channels        *[]string
}

// WatchFilter narrows ListWatches.
type WatchFilter struct {
	Enabled  *bool
	Registry string
	Query    string
	Limit    int
	Offset   int
}

const watchColumns = `id, image, registry, repository, kind, ref, enabled,
	notify_on_failure, last_digest, last_checked_at, last_ok_at, baseline_at,
	last_error, consecutive_failures, next_attempt_at, created_at, updated_at`

// CreateWatch inserts a watch and its channel subscriptions atomically. It
// returns ErrDuplicate when an identical (registry, repository, kind, ref)
// watch already exists.
func (db *DB) CreateWatch(ctx context.Context, in CreateWatchInput) (*WatchRecord, error) {
	if in.Kind != apitypes.WatchKindTag && in.Kind != apitypes.WatchKindPattern {
		return nil, fmt.Errorf("store: invalid watch kind %q", in.Kind)
	}
	now := time.Now().UTC()
	next := in.NextAttemptAt
	if next == nil {
		next = &now
	}
	id := NewID("w_")

	err := db.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO watches (id, image, registry, repository, kind, ref, enabled,
				notify_on_failure, last_digest, last_error, consecutive_failures,
				next_attempt_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', 0, ?, ?, ?)`,
			id, in.Image, in.Registry, in.Repository, string(in.Kind), in.Ref,
			boolInt(in.Enabled), boolInt(in.NotifyOnFailure), formatTime(*next),
			formatTime(now), formatTime(now))
		if err != nil {
			return translateErr(err)
		}
		return replaceWatchChannels(ctx, tx, id, in.Channels)
	})
	if err != nil {
		return nil, err
	}
	return db.GetWatch(ctx, id)
}

// GetWatch loads one watch by ID.
func (db *DB) GetWatch(ctx context.Context, id string) (*WatchRecord, error) {
	row := db.db.QueryRowContext(ctx, `SELECT `+watchColumns+` FROM watches WHERE id = ?`, id)
	w, err := scanWatch(row)
	if err != nil {
		return nil, err
	}
	if w.ChannelIDs, err = db.WatchChannelIDs(ctx, w.ID); err != nil {
		return nil, err
	}
	return w, nil
}

// FindWatch loads a watch by its natural key.
func (db *DB) FindWatch(ctx context.Context, registry, repository string, kind apitypes.WatchKind, ref string) (*WatchRecord, error) {
	row := db.db.QueryRowContext(ctx, `SELECT `+watchColumns+`
		FROM watches WHERE registry = ? AND repository = ? AND kind = ? AND ref = ?`,
		registry, repository, string(kind), ref)
	w, err := scanWatch(row)
	if err != nil {
		return nil, err
	}
	if w.ChannelIDs, err = db.WatchChannelIDs(ctx, w.ID); err != nil {
		return nil, err
	}
	return w, nil
}

// ListWatches returns watches matching filter plus the total row count before
// pagination.
func (db *DB) ListWatches(ctx context.Context, f WatchFilter) ([]*WatchRecord, int, error) {
	where, args := watchWhere(f)

	var total int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM watches`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count watches: %w", err)
	}

	query := `SELECT ` + watchColumns + ` FROM watches` + where +
		` ORDER BY created_at, id LIMIT ? OFFSET ?`
	args = append(args, apitypes.ClampLimit(f.Limit), max(f.Offset, 0))

	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list watches: %w", err)
	}
	defer rows.Close()

	out := []*WatchRecord{}
	byID := map[string]*WatchRecord{}
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, w)
		byID[w.ID] = w
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate watches: %w", err)
	}

	if err := db.attachChannelIDs(ctx, byID); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// DueWatches returns enabled watches whose next_attempt_at has passed, oldest
// first.
func (db *DB) DueWatches(ctx context.Context, now time.Time, limit int) ([]*WatchRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := db.db.QueryContext(ctx, `SELECT `+watchColumns+`
		FROM watches
		WHERE enabled = 1 AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY next_attempt_at IS NULL DESC, next_attempt_at, id
		LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("store: select due watches: %w", err)
	}
	defer rows.Close()

	out := []*WatchRecord{}
	byID := map[string]*WatchRecord{}
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
		byID[w.ID] = w
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate due watches: %w", err)
	}
	if err := db.attachChannelIDs(ctx, byID); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateWatch applies a partial update and returns the stored watch.
func (db *DB) UpdateWatch(ctx context.Context, id string, in UpdateWatchInput) (*WatchRecord, error) {
	sets := []string{}
	args := []any{}
	if in.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolInt(*in.Enabled))
	}
	if in.NotifyOnFailure != nil {
		sets = append(sets, "notify_on_failure = ?")
		args = append(args, boolInt(*in.NotifyOnFailure))
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(time.Now()))
	args = append(args, id)

	err := db.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE watches SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return translateErr(err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: update watch: %w", err)
		}
		if affected == 0 {
			return ErrNotFound
		}
		if in.Channels != nil {
			return replaceWatchChannels(ctx, tx, id, *in.Channels)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db.GetWatch(ctx, id)
}

// DeleteWatch removes a watch; watch_tags, watch_channels and events cascade.
func (db *DB) DeleteWatch(ctx context.Context, id string) error {
	res, err := db.db.ExecContext(ctx, `DELETE FROM watches WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete watch: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete watch: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkWatchSuccess records a healthy check result and schedules the next one.
//
// baseline_at is set on the first success only, so it is the durable record of
// "this watch has a baseline". COALESCE keeps it stable across later successes.
func (db *DB) MarkWatchSuccess(ctx context.Context, id, digest string, next time.Time) error {
	now := time.Now().UTC()
	res, err := db.db.ExecContext(ctx, `
		UPDATE watches
		SET last_digest = ?, last_checked_at = ?, last_ok_at = ?,
		    baseline_at = COALESCE(baseline_at, ?),
		    last_error = '', consecutive_failures = 0, next_attempt_at = ?, updated_at = ?
		WHERE id = ?`,
		digest, formatTime(now), formatTime(now), formatTime(now),
		formatTime(next), formatTime(now), id)
	if err != nil {
		return fmt.Errorf("store: mark watch success: %w", err)
	}
	return requireAffected(res, "watch")
}

// MarkWatchFailure records a failed check and schedules the backoff retry.
func (db *DB) MarkWatchFailure(ctx context.Context, id, errMsg string, next time.Time) error {
	now := time.Now().UTC()
	res, err := db.db.ExecContext(ctx, `
		UPDATE watches
		SET last_checked_at = ?, last_error = ?, consecutive_failures = consecutive_failures + 1,
		    next_attempt_at = ?, updated_at = ?
		WHERE id = ?`,
		formatTime(now), errMsg, formatTime(next), formatTime(now), id)
	if err != nil {
		return fmt.Errorf("store: mark watch failure: %w", err)
	}
	return requireAffected(res, "watch")
}

// ---------- watch_tags ----------

// KnownTags returns the baseline tag → digest map for a pattern watch.
func (db *DB) KnownTags(ctx context.Context, watchID string) (map[string]string, error) {
	rows, err := db.db.QueryContext(ctx,
		`SELECT tag, digest FROM watch_tags WHERE watch_id = ?`, watchID)
	if err != nil {
		return nil, fmt.Errorf("store: list known tags: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var tag, digest string
		if err := rows.Scan(&tag, &digest); err != nil {
			return nil, fmt.Errorf("store: scan known tag: %w", err)
		}
		out[tag] = digest
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate known tags: %w", err)
	}
	return out, nil
}

// CountWatchTags returns how many baseline tags a watch holds.
func (db *DB) CountWatchTags(ctx context.Context, watchID string) (int, error) {
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watch_tags WHERE watch_id = ?`, watchID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count watch tags: %w", err)
	}
	return n, nil
}

// UpsertWatchTags records baseline rows, updating the digest of tags already
// known while preserving their first_seen_at.
func (db *DB) UpsertWatchTags(ctx context.Context, watchID string, tags []apitypes.TagDigest) error {
	if len(tags) == 0 {
		return nil
	}
	now := formatTime(time.Now())
	return db.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO watch_tags (watch_id, tag, digest, first_seen_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (watch_id, tag) DO UPDATE SET digest = excluded.digest`)
		if err != nil {
			return fmt.Errorf("store: prepare upsert tag: %w", err)
		}
		defer stmt.Close()
		for _, t := range tags {
			if _, err := stmt.ExecContext(ctx, watchID, t.Tag, t.Digest, now); err != nil {
				return fmt.Errorf("store: upsert tag %s: %w", t.Tag, translateErr(err))
			}
		}
		return nil
	})
}

// PruneWatchTags removes baseline rows for tags no longer present upstream.
// Tags that disappear are pruned silently, without notifying.
func (db *DB) PruneWatchTags(ctx context.Context, watchID string, keep []string) (int, error) {
	known, err := db.KnownTags(ctx, watchID)
	if err != nil {
		return 0, err
	}
	if len(known) == 0 {
		return 0, nil
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, t := range keep {
		keepSet[t] = struct{}{}
	}
	stale := make([]string, 0, len(known))
	for tag := range known {
		if _, ok := keepSet[tag]; !ok {
			stale = append(stale, tag)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}
	pruned := 0
	err = db.withTx(ctx, func(tx *sql.Tx) error {
		for _, tag := range stale {
			res, err := tx.ExecContext(ctx,
				`DELETE FROM watch_tags WHERE watch_id = ? AND tag = ?`, watchID, tag)
			if err != nil {
				return fmt.Errorf("store: prune tag %s: %w", tag, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("store: prune tag %s: %w", tag, err)
			}
			pruned += int(n)
		}
		return nil
	})
	return pruned, err
}

// ---------- watch_channels ----------

// WatchChannelIDs returns the explicit channel subscriptions of a watch.
func (db *DB) WatchChannelIDs(ctx context.Context, watchID string) ([]string, error) {
	rows, err := db.db.QueryContext(ctx, `
		SELECT wc.channel_id
		FROM watch_channels wc
		JOIN channels c ON c.id = wc.channel_id
		WHERE wc.watch_id = ?
		ORDER BY c.name`, watchID)
	if err != nil {
		return nil, fmt.Errorf("store: list watch channels: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan watch channel: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate watch channels: %w", err)
	}
	return out, nil
}

// SetWatchChannels replaces a watch's explicit subscriptions.
func (db *DB) SetWatchChannels(ctx context.Context, watchID string, channelIDs []string) error {
	return db.withTx(ctx, func(tx *sql.Tx) error {
		return replaceWatchChannels(ctx, tx, watchID, channelIDs)
	})
}

func replaceWatchChannels(ctx context.Context, tx *sql.Tx, watchID string, channelIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM watch_channels WHERE watch_id = ?`, watchID); err != nil {
		return fmt.Errorf("store: clear watch channels: %w", err)
	}
	for _, cid := range channelIDs {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO watch_channels (watch_id, channel_id) VALUES (?, ?)`, watchID, cid); err != nil {
			return fmt.Errorf("store: subscribe channel %s: %w", cid, translateErr(err))
		}
	}
	return nil
}

// attachChannelIDs fills in ChannelIDs for a batch of watches in one query.
func (db *DB) attachChannelIDs(ctx context.Context, byID map[string]*WatchRecord) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := db.db.QueryContext(ctx, `
		SELECT wc.watch_id, wc.channel_id
		FROM watch_channels wc
		JOIN channels c ON c.id = wc.channel_id
		ORDER BY c.name`)
	if err != nil {
		return fmt.Errorf("store: list watch channels: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var watchID, channelID string
		if err := rows.Scan(&watchID, &channelID); err != nil {
			return fmt.Errorf("store: scan watch channel: %w", err)
		}
		if w, ok := byID[watchID]; ok {
			w.ChannelIDs = append(w.ChannelIDs, channelID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: iterate watch channels: %w", err)
	}
	return nil
}

// ---------- scanning helpers ----------

func watchWhere(f WatchFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	if f.Enabled != nil {
		clauses = append(clauses, "enabled = ?")
		args = append(args, boolInt(*f.Enabled))
	}
	if r := strings.TrimSpace(f.Registry); r != "" {
		clauses = append(clauses, "registry = ?")
		args = append(args, r)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		clauses = append(clauses, "(image LIKE ? OR repository LIKE ? OR ref LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanWatch(s scanner) (*WatchRecord, error) {
	var (
		w                               WatchRecord
		kind                            string
		enabled, notifyOnFailure        int
		lastChecked, lastOK, baselineAt sql.NullString
		nextTry                         sql.NullString
		createdAt, updatedAt            string
	)
	err := s.Scan(&w.ID, &w.Image, &w.Registry, &w.Repository, &kind, &w.Ref,
		&enabled, &notifyOnFailure, &w.LastDigest, &lastChecked, &lastOK, &baselineAt,
		&w.LastError, &w.ConsecutiveFailures, &nextTry, &createdAt, &updatedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	w.Kind = apitypes.WatchKind(kind)
	w.Enabled = enabled != 0
	w.NotifyOnFailure = notifyOnFailure != 0
	if w.LastCheckedAt, err = timePtr(lastChecked); err != nil {
		return nil, err
	}
	if w.LastOKAt, err = timePtr(lastOK); err != nil {
		return nil, err
	}
	if w.BaselineAt, err = timePtr(baselineAt); err != nil {
		return nil, err
	}
	if w.NextAttemptAt, err = timePtr(nextTry); err != nil {
		return nil, err
	}
	if w.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if w.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

func requireAffected(res sql.Result, what string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s rows affected: %w", what, err)
	}
	if affected == 0 {
		return fmt.Errorf("store: %s: %w", what, ErrNotFound)
	}
	return nil
}

// IsNotFound reports whether err is the store's not-found sentinel.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsDuplicate reports whether err is the store's duplicate sentinel.
func IsDuplicate(err error) bool { return errors.Is(err, ErrDuplicate) }

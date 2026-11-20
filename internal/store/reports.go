package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// ReportBacklogLimit caps how many unreported events per watch and family are
// protected from history pruning. A watch that changes hundreds of times a day
// must not grow the database without bound; older unreported events beyond this
// window are pruned like any other history.
const ReportBacklogLimit = 1000

// reportMarkColumn maps a report kind onto the events column holding its
// "already reported" marker. A fixed map keeps kind values out of SQL.
func reportMarkColumn(kind apitypes.ReportKind) (string, error) {
	switch kind {
	case apitypes.ReportDaily:
		return "daily_reported_at", nil
	case apitypes.ReportWeekly:
		return "weekly_reported_at", nil
	default:
		return "", fmt.Errorf("store: unknown report kind %q", kind)
	}
}

// ReportState is the stored last-run summary of one report kind.
type ReportState struct {
	Kind        apitypes.ReportKind
	LastRunAt   *time.Time
	LastSentAt  *time.Time
	LastStatus  string
	LastError   string
	LastEvents  int
	LastWatches int
	UpdatedAt   time.Time
}

// GetReportState loads one report's state, or ErrNotFound when it never ran.
func (db *DB) GetReportState(ctx context.Context, kind apitypes.ReportKind) (*ReportState, error) {
	row := db.db.QueryRowContext(ctx, `
		SELECT kind, last_run_at, last_sent_at, last_status, last_error,
		       last_events, last_watches, updated_at
		FROM report_state WHERE kind = ?`, string(kind))

	var (
		st                ReportState
		rawKind           string
		lastRun, lastSent sql.NullString
		updatedAt         string
	)
	if err := row.Scan(&rawKind, &lastRun, &lastSent, &st.LastStatus, &st.LastError,
		&st.LastEvents, &st.LastWatches, &updatedAt); err != nil {
		return nil, translateErr(err)
	}
	st.Kind = apitypes.ReportKind(rawKind)

	var err error
	if st.LastRunAt, err = timePtr(lastRun); err != nil {
		return nil, err
	}
	if st.LastSentAt, err = timePtr(lastSent); err != nil {
		return nil, err
	}
	if st.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &st, nil
}

// SaveReportState upserts one report's last-run summary.
func (db *DB) SaveReportState(ctx context.Context, st *ReportState) error {
	if _, err := reportMarkColumn(st.Kind); err != nil {
		return err
	}
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	_, err := db.db.ExecContext(ctx, `
		INSERT INTO report_state (kind, last_run_at, last_sent_at, last_status,
		                          last_error, last_events, last_watches, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind) DO UPDATE SET
			last_run_at = excluded.last_run_at,
			last_sent_at = excluded.last_sent_at,
			last_status = excluded.last_status,
			last_error = excluded.last_error,
			last_events = excluded.last_events,
			last_watches = excluded.last_watches,
			updated_at = excluded.updated_at`,
		string(st.Kind), nullableTime(st.LastRunAt), nullableTime(st.LastSentAt),
		st.LastStatus, st.LastError, st.LastEvents, st.LastWatches, formatTime(st.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: save report state %s: %w", st.Kind, err)
	}
	return nil
}

// UnreportedChangeEvents returns up to limit update events of a watch that the
// given report kind has not delivered yet, oldest first so a report reads
// chronologically.
func (db *DB) UnreportedChangeEvents(ctx context.Context, watchID string, kind apitypes.ReportKind, limit int) ([]*EventRecord, error) {
	column, err := reportMarkColumn(kind)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	types := apitypes.ChangeEventTypes()
	args := []any{watchID}
	for _, t := range types {
		args = append(args, string(t))
	}
	args = append(args, limit)

	rows, err := db.db.QueryContext(ctx, `
		SELECT e.id, e.watch_id, e.type, e.tag, e.old_digest, e.new_digest,
		       e.detail_json, e.created_at, COALESCE(w.image, '')
		FROM events e
		LEFT JOIN watches w ON w.id = e.watch_id
		WHERE e.watch_id = ? AND e.type IN (`+placeholders(len(types))+`)
		  AND e.`+column+` IS NULL
		ORDER BY e.created_at, e.id
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: unreported events: %w", err)
	}
	defer rows.Close()

	out := []*EventRecord{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate unreported events: %w", err)
	}
	return out, nil
}

// CountUnreportedChangeEvents counts a watch's update events not yet delivered
// by the given report kind.
func (db *DB) CountUnreportedChangeEvents(ctx context.Context, watchID string, kind apitypes.ReportKind) (int, error) {
	column, err := reportMarkColumn(kind)
	if err != nil {
		return 0, err
	}
	types := apitypes.ChangeEventTypes()
	args := []any{watchID}
	for _, t := range types {
		args = append(args, string(t))
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events
		WHERE watch_id = ? AND type IN (`+placeholders(len(types))+`)
		  AND `+column+` IS NULL`, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count unreported events: %w", err)
	}
	return n, nil
}

// MarkEventsReported records that the given events were delivered by a report
// kind. Unknown IDs are ignored; an empty list is a no-op.
func (db *DB) MarkEventsReported(ctx context.Context, ids []string, kind apitypes.ReportKind, at time.Time) error {
	column, err := reportMarkColumn(kind)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Chunk the IN list: a large report can cover thousands of events, and a
	// single statement would run into the driver's parameter limit.
	const chunk = 500
	return db.withTx(ctx, func(tx *sql.Tx) error {
		for start := 0; start < len(ids); start += chunk {
			end := min(start+chunk, len(ids))
			args := []any{formatTime(at)}
			for _, id := range ids[start:end] {
				args = append(args, id)
			}
			query := `UPDATE events SET ` + column + ` = ? WHERE id IN (` +
				placeholders(end-start) + `)`
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("store: mark events reported: %w", err)
			}
		}
		return nil
	})
}

// AllEnabledWatches returns every enabled watch with its channel IDs. It backs
// report collection, which must see all watches rather than one page.
func (db *DB) AllEnabledWatches(ctx context.Context) ([]*WatchRecord, error) {
	enabled := true
	return db.listWatchesUnpaged(ctx, WatchFilter{Enabled: &enabled})
}

// listWatchesUnpaged is ListWatches without pagination, for internal scans.
func (db *DB) listWatchesUnpaged(ctx context.Context, f WatchFilter) ([]*WatchRecord, error) {
	where, args := watchWhere(f)
	rows, err := db.db.QueryContext(ctx,
		`SELECT `+watchColumns+` FROM watches`+where+` ORDER BY image, ref, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list watches: %w", err)
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
		return nil, fmt.Errorf("store: iterate watches: %w", err)
	}
	if err := db.attachChannelIDs(ctx, byID); err != nil {
		return nil, err
	}
	return out, nil
}

// nullableTime renders an optional timestamp for a nullable TEXT column.
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// EventRecord is the stored form of an event.
type EventRecord struct {
	ID        string
	WatchID   string
	Type      apitypes.EventType
	Tag       string
	OldDigest string
	NewDigest string
	Detail    map[string]any
	CreatedAt time.Time

	// Image is denormalised from the owning watch for convenient rendering.
	Image string
}

// CreateEventInput is the input to CreateEvent.
type CreateEventInput struct {
	WatchID   string
	Type      apitypes.EventType
	Tag       string
	OldDigest string
	NewDigest string
	Detail    map[string]any
}

// EventFilter narrows ListEvents.
type EventFilter struct {
	WatchID string
	Type    apitypes.EventType
	Limit   int
	Offset  int
}

// NotificationRecord is a row of the delivery log.
type NotificationRecord struct {
	ID          string
	EventID     string
	ChannelID   string
	ChannelName string
	ChannelType string
	Status      apitypes.NotificationStatus
	Attempts    int
	LastError   string
	SentAt      *time.Time
	CreatedAt   time.Time
}

// NotificationFilter narrows ListNotifications.
type NotificationFilter struct {
	EventID string
	Status  apitypes.NotificationStatus
	Limit   int
	Offset  int
}

// CreateEvent records an event and returns it.
func (db *DB) CreateEvent(ctx context.Context, in CreateEventInput) (*EventRecord, error) {
	if in.Type == "" {
		return nil, fmt.Errorf("store: event type is required")
	}
	detail, err := marshalJSON(in.Detail)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := NewID("e_")

	_, err = db.db.ExecContext(ctx, `
		INSERT INTO events (id, watch_id, type, tag, old_digest, new_digest, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.WatchID, string(in.Type), in.Tag, in.OldDigest, in.NewDigest, detail, formatTime(now))
	if err != nil {
		return nil, translateErr(err)
	}
	return db.GetEvent(ctx, id)
}

// GetEvent loads one event.
func (db *DB) GetEvent(ctx context.Context, id string) (*EventRecord, error) {
	row := db.db.QueryRowContext(ctx, `
		SELECT e.id, e.watch_id, e.type, e.tag, e.old_digest, e.new_digest,
		       e.detail_json, e.created_at, COALESCE(w.image, '')
		FROM events e
		LEFT JOIN watches w ON w.id = e.watch_id
		WHERE e.id = ?`, id)
	return scanEvent(row)
}

// ListEvents returns events matching filter plus the pre-pagination total.
func (db *DB) ListEvents(ctx context.Context, f EventFilter) ([]*EventRecord, int, error) {
	clauses := []string{}
	args := []any{}
	if f.WatchID != "" {
		clauses = append(clauses, "e.watch_id = ?")
		args = append(args, f.WatchID)
	}
	if f.Type != "" {
		clauses = append(clauses, "e.type = ?")
		args = append(args, string(f.Type))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count events: %w", err)
	}

	args = append(args, apitypes.ClampLimit(f.Limit), max(f.Offset, 0))
	rows, err := db.db.QueryContext(ctx, `
		SELECT e.id, e.watch_id, e.type, e.tag, e.old_digest, e.new_digest,
		       e.detail_json, e.created_at, COALESCE(w.image, '')
		FROM events e
		LEFT JOIN watches w ON w.id = e.watch_id`+where+`
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	out := []*EventRecord{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate events: %w", err)
	}
	return out, total, nil
}

// LastEventOfType returns the most recent event of a type for a watch, or
// ErrNotFound. The check engine uses it to avoid duplicate failure events.
func (db *DB) LastEventOfType(ctx context.Context, watchID string, t apitypes.EventType) (*EventRecord, error) {
	row := db.db.QueryRowContext(ctx, `
		SELECT e.id, e.watch_id, e.type, e.tag, e.old_digest, e.new_digest,
		       e.detail_json, e.created_at, COALESCE(w.image, '')
		FROM events e
		LEFT JOIN watches w ON w.id = e.watch_id
		WHERE e.watch_id = ? AND e.type = ?
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT 1`, watchID, string(t))
	return scanEvent(row)
}

// CountEvents returns the total number of recorded events.
func (db *DB) CountEvents(ctx context.Context) (int, error) {
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}

// ---------- notifications ----------

// CreateNotification records a pending delivery attempt.
func (db *DB) CreateNotification(ctx context.Context, eventID, channelID string) (*NotificationRecord, error) {
	now := time.Now().UTC()
	id := NewID("n_")
	_, err := db.db.ExecContext(ctx, `
		INSERT INTO notifications (id, event_id, channel_id, status, attempts, last_error, created_at)
		VALUES (?, ?, ?, ?, 0, '', ?)`,
		id, eventID, channelID, string(apitypes.NotificationPending), formatTime(now))
	if err != nil {
		return nil, translateErr(err)
	}
	return db.GetNotification(ctx, id)
}

// GetNotification loads one delivery record.
func (db *DB) GetNotification(ctx context.Context, id string) (*NotificationRecord, error) {
	row := db.db.QueryRowContext(ctx, notificationSelect+` WHERE n.id = ?`, id)
	return scanNotification(row)
}

// ListNotifications returns delivery records matching filter plus the total.
func (db *DB) ListNotifications(ctx context.Context, f NotificationFilter) ([]*NotificationRecord, int, error) {
	clauses := []string{}
	args := []any{}
	if f.EventID != "" {
		clauses = append(clauses, "n.event_id = ?")
		args = append(args, f.EventID)
	}
	if f.Status != "" {
		clauses = append(clauses, "n.status = ?")
		args = append(args, string(f.Status))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications n`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count notifications: %w", err)
	}

	args = append(args, apitypes.ClampLimit(f.Limit), max(f.Offset, 0))
	rows, err := db.db.QueryContext(ctx, notificationSelect+where+`
		ORDER BY n.created_at DESC, n.id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list notifications: %w", err)
	}
	defer rows.Close()

	out := []*NotificationRecord{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate notifications: %w", err)
	}
	return out, total, nil
}

// NotificationsForEvent returns every delivery record attached to an event.
func (db *DB) NotificationsForEvent(ctx context.Context, eventID string) ([]*apitypes.Notification, error) {
	rows, err := db.db.QueryContext(ctx, notificationSelect+`
		WHERE n.event_id = ? ORDER BY n.created_at, n.id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("store: list event notifications: %w", err)
	}
	defer rows.Close()

	out := []*apitypes.Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n.ToAPIType())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate event notifications: %w", err)
	}
	return out, nil
}

// UpdateNotification records the outcome of a delivery attempt.
func (db *DB) UpdateNotification(ctx context.Context, id string, status apitypes.NotificationStatus, attempts int, lastErr string) error {
	var sentAt any
	if status == apitypes.NotificationSent {
		sentAt = formatTime(time.Now())
	}
	res, err := db.db.ExecContext(ctx, `
		UPDATE notifications SET status = ?, attempts = ?, last_error = ?, sent_at = ?
		WHERE id = ?`, string(status), attempts, lastErr, sentAt, id)
	if err != nil {
		return fmt.Errorf("store: update notification: %w", err)
	}
	return requireAffected(res, "notification")
}

// ToAPIType converts a stored notification to its wire form.
func (n *NotificationRecord) ToAPIType() *apitypes.Notification {
	return &apitypes.Notification{
		ID:          n.ID,
		EventID:     n.EventID,
		ChannelID:   n.ChannelID,
		ChannelName: n.ChannelName,
		ChannelType: n.ChannelType,
		Status:      n.Status,
		Attempts:    n.Attempts,
		LastError:   n.LastError,
		SentAt:      n.SentAt,
		CreatedAt:   n.CreatedAt,
	}
}

const notificationSelect = `
	SELECT n.id, n.event_id, n.channel_id, COALESCE(c.name, ''), COALESCE(c.type, ''),
	       n.status, n.attempts, n.last_error, n.sent_at, n.created_at
	FROM notifications n
	LEFT JOIN channels c ON c.id = n.channel_id`

func scanEvent(s scanner) (*EventRecord, error) {
	var (
		e          EventRecord
		etype      string
		detailJSON string
		createdAt  string
	)
	if err := s.Scan(&e.ID, &e.WatchID, &etype, &e.Tag, &e.OldDigest, &e.NewDigest,
		&detailJSON, &createdAt, &e.Image); err != nil {
		return nil, translateErr(err)
	}
	e.Type = apitypes.EventType(etype)
	detail, err := unmarshalJSONMap(detailJSON)
	if err != nil {
		return nil, err
	}
	e.Detail = detail
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func scanNotification(s scanner) (*NotificationRecord, error) {
	var (
		n         NotificationRecord
		status    string
		sentAt    sql.NullString
		createdAt string
	)
	if err := s.Scan(&n.ID, &n.EventID, &n.ChannelID, &n.ChannelName, &n.ChannelType,
		&status, &n.Attempts, &n.LastError, &sentAt, &createdAt); err != nil {
		return nil, translateErr(err)
	}
	n.Status = apitypes.NotificationStatus(status)
	var err error
	if n.SentAt, err = timePtr(sentAt); err != nil {
		return nil, err
	}
	if n.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &n, nil
}

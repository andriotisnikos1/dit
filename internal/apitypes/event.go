package apitypes

import "time"

// EventType classifies an entry in the event feed.
type EventType string

const (
	// EventDigestChanged fires when a pinned tag resolves to a new digest.
	EventDigestChanged EventType = "digest_changed"
	// EventNewTags fires when a pattern watch sees tags it has never seen.
	EventNewTags EventType = "new_tags"
	// EventCheckFailed fires on the first failure of a run of failures.
	EventCheckFailed EventType = "check_failed"
	// EventCheckRecovered fires when a failing watch returns to health.
	EventCheckRecovered EventType = "check_recovered"
)

// EventTypes lists every known event type, for CLI validation and help text.
func EventTypes() []EventType {
	return []EventType{EventDigestChanged, EventNewTags, EventCheckFailed, EventCheckRecovered}
}

// Valid reports whether t is a known event type.
func (t EventType) Valid() bool {
	for _, known := range EventTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// Event is a recorded check outcome.
type Event struct {
	ID        string         `json:"id"                   yaml:"id"`
	WatchID   string         `json:"watch_id"             yaml:"watch_id"`
	Type      EventType      `json:"type"                 yaml:"type"`
	Tag       string         `json:"tag,omitempty"        yaml:"tag,omitempty"`
	OldDigest string         `json:"old_digest,omitempty" yaml:"old_digest,omitempty"`
	NewDigest string         `json:"new_digest,omitempty" yaml:"new_digest,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"     yaml:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"           yaml:"created_at"`

	// Denormalised view fields, filled in by the API layer.
	Image         string         `json:"image,omitempty"         yaml:"image,omitempty"`
	Notifications []Notification `json:"notifications,omitempty" yaml:"notifications,omitempty"`
}

// NotificationStatus tracks delivery of one event to one channel.
type NotificationStatus string

const (
	NotificationPending NotificationStatus = "pending"
	NotificationSent    NotificationStatus = "sent"
	NotificationFailed  NotificationStatus = "failed"
)

// Notification is a row of the delivery log.
type Notification struct {
	ID          string             `json:"id"                     yaml:"id"`
	EventID     string             `json:"event_id"               yaml:"event_id"`
	ChannelID   string             `json:"channel_id"             yaml:"channel_id"`
	ChannelName string             `json:"channel_name,omitempty" yaml:"channel_name,omitempty"`
	ChannelType string             `json:"channel_type,omitempty" yaml:"channel_type,omitempty"`
	Status      NotificationStatus `json:"status"                 yaml:"status"`
	Attempts    int                `json:"attempts"               yaml:"attempts"`
	LastError   string             `json:"last_error,omitempty"   yaml:"last_error,omitempty"`
	SentAt      *time.Time         `json:"sent_at,omitempty"      yaml:"sent_at,omitempty"`
	CreatedAt   time.Time          `json:"created_at"             yaml:"created_at"`
}

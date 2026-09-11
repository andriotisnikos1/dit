package apitypes

import "time"

// WatchKind distinguishes a pinned-tag watch from a glob pattern watch.
type WatchKind string

const (
	// WatchKindTag tracks one tag and reports digest drift.
	WatchKindTag WatchKind = "tag"
	// WatchKindPattern tracks every tag matching a glob and reports new ones.
	WatchKindPattern WatchKind = "pattern"
)

// Watch is the wire representation of a configured watch.
type Watch struct {
	ID                  string     `json:"id"                    yaml:"id"`
	Image               string     `json:"image"                 yaml:"image"`
	Registry            string     `json:"registry"              yaml:"registry"`
	Repository          string     `json:"repository"            yaml:"repository"`
	Kind                WatchKind  `json:"kind"                  yaml:"kind"`
	Ref                 string     `json:"ref"                   yaml:"ref"`
	Enabled             bool       `json:"enabled"               yaml:"enabled"`
	NotifyOnFailure     bool       `json:"notify_on_failure"     yaml:"notify_on_failure"`
	Channels            []string   `json:"channels"              yaml:"channels"`
	LastDigest          string     `json:"last_digest,omitempty" yaml:"last_digest,omitempty"`
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	LastOKAt            *time.Time `json:"last_ok_at,omitempty"  yaml:"last_ok_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"  yaml:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"  yaml:"consecutive_failures"`
	NextAttemptAt       *time.Time `json:"next_attempt_at,omitempty" yaml:"next_attempt_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"            yaml:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"            yaml:"updated_at"`
}

// CreateWatchRequest is the body of POST /api/v1/watches. Exactly one of Tags
// or Patterns must be non-empty; Tags may hold several tags, in which case one
// watch is created per tag.
type CreateWatchRequest struct {
	Image           string   `json:"image"                     yaml:"image"`
	Tags            []string `json:"tags,omitempty"            yaml:"tags,omitempty"`
	Patterns        []string `json:"patterns,omitempty"        yaml:"patterns,omitempty"`
	Channels        []string `json:"channels,omitempty"        yaml:"channels,omitempty"`
	Disabled        bool     `json:"disabled,omitempty"        yaml:"disabled,omitempty"`
	NotifyOnFailure *bool    `json:"notify_on_failure,omitempty" yaml:"notify_on_failure,omitempty"`
}

// CreateWatchResponse reports what was created and what already existed.
type CreateWatchResponse struct {
	Image    string   `json:"image"    yaml:"image"`
	Registry string   `json:"registry" yaml:"registry"`
	Watches  []Watch  `json:"watches"  yaml:"watches"`
	Created  []string `json:"created"  yaml:"created"`
	Existing []string `json:"existing" yaml:"existing"`
}

// UpdateWatchRequest is the body of PATCH /api/v1/watches/{id}. Absent fields
// are left untouched.
type UpdateWatchRequest struct {
	Enabled         *bool     `json:"enabled,omitempty"           yaml:"enabled,omitempty"`
	NotifyOnFailure *bool     `json:"notify_on_failure,omitempty" yaml:"notify_on_failure,omitempty"`
	Channels        *[]string `json:"channels,omitempty"          yaml:"channels,omitempty"`
}

// TagDigest pairs a tag name with the digest it resolved to.
type TagDigest struct {
	Tag    string `json:"tag"              yaml:"tag"`
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`
}

// CheckResult is returned by POST /api/v1/watches/{id}/check.
type CheckResult struct {
	WatchID   string      `json:"watch_id"             yaml:"watch_id"`
	Changed   bool        `json:"changed"              yaml:"changed"`
	Baseline  bool        `json:"baseline"             yaml:"baseline"`
	Type      EventType   `json:"type,omitempty"       yaml:"type,omitempty"`
	Tag       string      `json:"tag,omitempty"        yaml:"tag,omitempty"`
	OldDigest string      `json:"old_digest,omitempty" yaml:"old_digest,omitempty"`
	NewDigest string      `json:"new_digest,omitempty" yaml:"new_digest,omitempty"`
	NewTags   []TagDigest `json:"new_tags,omitempty"   yaml:"new_tags,omitempty"`
	Pruned    []string    `json:"pruned,omitempty"     yaml:"pruned,omitempty"`
	Matched   int         `json:"matched"              yaml:"matched"`
	EventID   string      `json:"event_id,omitempty"   yaml:"event_id,omitempty"`
	Notified  int         `json:"notified"             yaml:"notified"`
	Error     string      `json:"error,omitempty"      yaml:"error,omitempty"`
}

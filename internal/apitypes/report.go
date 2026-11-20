package apitypes

import "time"

// ReportKind identifies one scheduled report.
type ReportKind string

const (
	// ReportDaily summarises the update events since the previous daily run.
	ReportDaily ReportKind = "daily"
	// ReportWeekly summarises the update events since the previous weekly run.
	ReportWeekly ReportKind = "weekly"
)

// ReportKinds lists every supported report kind, in display order.
func ReportKinds() []ReportKind { return []ReportKind{ReportDaily, ReportWeekly} }

// Valid reports whether k is a known report kind.
func (k ReportKind) Valid() bool {
	for _, known := range ReportKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// ReportChannelResult is the delivery outcome of one channel in a report run.
type ReportChannelResult struct {
	ChannelID   string `json:"channel_id"             yaml:"channel_id"`
	ChannelName string `json:"channel_name"           yaml:"channel_name"`
	ChannelType string `json:"channel_type"           yaml:"channel_type"`
	// Status is "sent", "failed" or "preview" (dry run only).
	Status   string `json:"status"               yaml:"status"`
	Attempts int    `json:"attempts"             yaml:"attempts"`
	Events   int    `json:"events"               yaml:"events"`
	Error    string `json:"error,omitempty"      yaml:"error,omitempty"`
}

// ReportResult is returned by a report preview or send.
type ReportResult struct {
	Kind    ReportKind `json:"kind"              yaml:"kind"`
	DryRun  bool       `json:"dry_run"           yaml:"dry_run"`
	Title   string     `json:"title,omitempty"   yaml:"title,omitempty"`
	Body    string     `json:"body,omitempty"    yaml:"body,omitempty"`
	Events  int        `json:"events"            yaml:"events"`
	Watches int        `json:"watches"           yaml:"watches"`
	// Marked is how many events were recorded as reported by this run.
	Marked int `json:"marked"            yaml:"marked"`
	// Sent and Failed count channels, not events.
	Sent     int                   `json:"sent"               yaml:"sent"`
	Failed   int                   `json:"failed"             yaml:"failed"`
	Channels []ReportChannelResult `json:"channels,omitempty" yaml:"channels,omitempty"`
}

// ReportInfo describes one report kind's schedule and last run.
type ReportInfo struct {
	Kind      ReportKind `json:"kind"                yaml:"kind"`
	Enabled   bool       `json:"enabled"             yaml:"enabled"`
	Schedule  string     `json:"schedule,omitempty"  yaml:"schedule,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty" yaml:"next_run_at,omitempty"`

	LastRunAt  *time.Time `json:"last_run_at,omitempty"  yaml:"last_run_at,omitempty"`
	LastSentAt *time.Time `json:"last_sent_at,omitempty" yaml:"last_sent_at,omitempty"`
	// LastStatus is "sent", "empty", "failed" or "no_channels".
	LastStatus  string `json:"last_status,omitempty"  yaml:"last_status,omitempty"`
	LastError   string `json:"last_error,omitempty"   yaml:"last_error,omitempty"`
	LastEvents  int    `json:"last_events"            yaml:"last_events"`
	LastWatches int    `json:"last_watches"           yaml:"last_watches"`
}

// ReportStatus is the response of GET /api/v1/reports.
type ReportStatus struct {
	Reports []ReportInfo `json:"reports" yaml:"reports"`
	// Timezone is the zone report times are expressed in.
	Timezone string `json:"timezone" yaml:"timezone"`
}

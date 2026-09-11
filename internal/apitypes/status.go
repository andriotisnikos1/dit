package apitypes

import "time"

// Health is returned by the unauthenticated GET /healthz probe.
type Health struct {
	Status  string `json:"status"  yaml:"status"`
	Version string `json:"version" yaml:"version"`
}

// WatchCounts summarises the watch table.
type WatchCounts struct {
	Total    int `json:"total"    yaml:"total"`
	Enabled  int `json:"enabled"  yaml:"enabled"`
	Disabled int `json:"disabled" yaml:"disabled"`
	Failing  int `json:"failing"  yaml:"failing"`
}

// Status is returned by GET /api/v1/status.
type Status struct {
	Version          string      `json:"version"           yaml:"version"`
	UptimeSeconds    int64       `json:"uptime_seconds"    yaml:"uptime_seconds"`
	Watches          WatchCounts `json:"watches"           yaml:"watches"`
	Channels         int         `json:"channels"          yaml:"channels"`
	Registries       int         `json:"registries"        yaml:"registries"`
	Events           int         `json:"events"            yaml:"events"`
	CheckInterval    string      `json:"check_interval"    yaml:"check_interval"`
	CheckConcurrency int         `json:"check_concurrency" yaml:"check_concurrency"`
	Ticks            uint64      `json:"ticks"             yaml:"ticks"`
	LastTick         *time.Time  `json:"last_tick,omitempty" yaml:"last_tick,omitempty"`
	NextTick         *time.Time  `json:"next_tick,omitempty" yaml:"next_tick,omitempty"`
}

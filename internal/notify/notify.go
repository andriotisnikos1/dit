// Package notify delivers notification messages over the supported channels.
//
// A Channel is a thin transport: it knows how to put one Message on the wire
// and nothing else. Delivery policy (retries, records in the notifications
// table) lives in the check engine, so a transport failure can never corrupt a
// check result.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Message is the transport-independent notification payload.
type Message struct {
	Title    string
	Body     string
	Priority string
	URL      string
	Tags     []string
}

// Channel is one notification transport.
type Channel interface {
	// Type is the channel type: "email" or "ntfy".
	Type() string
	// Send delivers the message, returning a descriptive error on failure.
	Send(ctx context.Context, msg Message) error
}

// Errors returned by channel construction.
var (
	// ErrMissingConfig reports a channel config that lacks a required key.
	ErrMissingConfig = errors.New("notify: missing required config")
)

// ErrConfig names the missing key.
func ErrConfig(channelType, key string) error {
	return fmt.Errorf("%w: %s channel requires %q", ErrMissingConfig, channelType, key)
}

// NormalisePriority maps a human priority onto the ntfy scale. Unknown values
// fall back to "default".
func NormalisePriority(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "min", "minimal", "low":
		return "low"
	case "default", "normal", "":
		return "default"
	case "high":
		return "high"
	case "max", "urgent", "critical":
		return "urgent"
	default:
		return "default"
	}
}

// SplitList splits a comma-separated config value.
func SplitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// JoinList renders a slice as the comma-separated form used in configs.
func JoinList(items []string) string { return strings.Join(items, ",") }

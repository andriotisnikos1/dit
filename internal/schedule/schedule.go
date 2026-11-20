// Package schedule holds the wall-clock types shared by the server
// configuration and the report scheduler.
//
// It is deliberately dependency-free: internal/config uses it to validate what
// the operator configured, and internal/check uses the same types to decide
// when a report runs. Keeping it separate stops the CLI binary (which imports
// config) from linking the registry and notification stacks.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// Clock is a wall-clock time of day in a report schedule.
type Clock struct {
	Hour   int
	Minute int
}

// ParseClock parses "HH:MM" (24-hour). Leading zeros are optional.
func ParseClock(raw string) (Clock, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return Clock{}, fmt.Errorf("invalid time %q: expected HH:MM", raw)
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return Clock{}, fmt.Errorf("invalid time %q: hour is not a number", raw)
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return Clock{}, fmt.Errorf("invalid time %q: minute is not a number", raw)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Clock{}, fmt.Errorf("invalid time %q: must be between 00:00 and 23:59", raw)
	}
	return Clock{Hour: hour, Minute: minute}, nil
}

// String renders the clock as HH:MM.
func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// on builds the clock time on date's calendar day in loc.
func (c Clock) on(date time.Time, loc *time.Location) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), c.Hour, c.Minute, 0, 0, loc)
}

// Next returns the first occurrence strictly after `after`.
func (c Clock) Next(after time.Time) time.Time {
	loc := after.Location()
	candidate := c.on(after, loc)
	if !candidate.After(after) {
		candidate = c.on(after.AddDate(0, 0, 1), loc)
	}
	return candidate
}

// NextWeekday returns the first occurrence strictly after `after` falling on
// the given weekday.
func (c Clock) NextWeekday(after time.Time, day time.Weekday) time.Time {
	loc := after.Location()
	delta := (int(day) - int(after.Weekday()) + 7) % 7
	candidate := c.on(after.AddDate(0, 0, delta), loc)
	if !candidate.After(after) {
		candidate = c.on(after.AddDate(0, 0, delta+7), loc)
	}
	return candidate
}

// ReportSchedule says when the server's reports run. A nil Daily/Weekly
// disables that kind.
type ReportSchedule struct {
	Daily     *Clock
	Weekly    *Clock
	WeeklyDay time.Weekday
	Location  *time.Location
}

// Loc returns the schedule's timezone, defaulting to UTC.
func (s ReportSchedule) Loc() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

// Enabled reports whether a kind is scheduled.
func (s ReportSchedule) Enabled(kind apitypes.ReportKind) bool {
	switch kind {
	case apitypes.ReportDaily:
		return s.Daily != nil
	case apitypes.ReportWeekly:
		return s.Weekly != nil
	default:
		return false
	}
}

// NextFor returns when a kind next runs, if it is enabled.
func (s ReportSchedule) NextFor(kind apitypes.ReportKind, after time.Time) (time.Time, bool) {
	local := after.In(s.Loc())
	switch {
	case kind == apitypes.ReportDaily && s.Daily != nil:
		return s.Daily.Next(local), true
	case kind == apitypes.ReportWeekly && s.Weekly != nil:
		return s.Weekly.NextWeekday(local, s.WeeklyDay), true
	default:
		return time.Time{}, false
	}
}

// Next returns the earliest upcoming run across both kinds.
func (s ReportSchedule) Next(after time.Time) (time.Time, apitypes.ReportKind, bool) {
	var (
		best     time.Time
		bestKind apitypes.ReportKind
		found    bool
	)
	for _, kind := range apitypes.ReportKinds() {
		next, ok := s.NextFor(kind, after)
		if !ok {
			continue
		}
		if !found || next.Before(best) {
			best, bestKind, found = next, kind, true
		}
	}
	return best, bestKind, found
}

// Describe renders a human-readable schedule for a kind.
func (s ReportSchedule) Describe(kind apitypes.ReportKind) string {
	zone := s.Loc().String()
	switch {
	case kind == apitypes.ReportDaily && s.Daily != nil:
		return fmt.Sprintf("daily at %s (%s)", s.Daily, zone)
	case kind == apitypes.ReportWeekly && s.Weekly != nil:
		return fmt.Sprintf("%ss at %s (%s)", s.WeeklyDay, s.Weekly, zone)
	default:
		return ""
	}
}

package check

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/flags"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/schedule"
	"github.com/andriotisnikos1/dit/internal/store"
)

// Report tuning. A report is a summary, not a log dump: a watch that changed
// hundreds of times contributes a bounded number of lines, and events beyond
// the fetch cap roll into the next report.
const (
	// reportMaxEventsPerWatch bounds how many events one watch contributes to
	// a single report (and therefore how many it marks as reported).
	reportMaxEventsPerWatch = 500
	// reportMaxLinesPerWatch bounds how many of those events are rendered.
	reportMaxLinesPerWatch = 40
	// reportMaxErrorLen truncates a stored failure message in the body.
	reportMaxErrorLen = 240
)

// Clock and ReportSchedule live in internal/schedule so the server
// configuration can validate them without importing the engine. The aliases
// keep the engine's own tests and call sites talking about check.Clock.
type Clock = schedule.Clock

// ReportSchedule is the report timetable shared with internal/config.
type ReportSchedule = schedule.ReportSchedule

// ParseClock parses "HH:MM" (24-hour). Leading zeros are optional.
func ParseClock(raw string) (Clock, error) { return schedule.ParseClock(raw) }

// reportWatch is one watch's contribution to a report.
type reportWatch struct {
	watch    *store.WatchRecord
	events   []*store.EventRecord
	channels []*store.ChannelRecord
}

// reportEnabled reports whether a kind is enabled for the watch's resolved
// settings.
func reportEnabled(kind apitypes.ReportKind, settings flags.Settings) bool {
	switch kind {
	case apitypes.ReportDaily:
		return settings.ReportDaily
	case apitypes.ReportWeekly:
		return settings.ReportWeekly
	default:
		return false
	}
}

// defaultPeriod is how far back a first-ever report looks.
func defaultPeriod(kind apitypes.ReportKind) time.Duration {
	if kind == apitypes.ReportWeekly {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// RunReporter drives the report schedule until ctx is cancelled. Reports are
// sent at the configured wall-clock times; a report with nothing to say sends
// nothing.
func (e *Engine) RunReporter(ctx context.Context) error {
	if !e.schedule.Enabled(apitypes.ReportDaily) && !e.schedule.Enabled(apitypes.ReportWeekly) {
		e.log.Info("report scheduler disabled: set DIT_REPORT_DAILY_AT and/or DIT_REPORT_WEEKLY_AT to enable it")
		return nil
	}
	e.log.Info("report scheduler started",
		"daily", e.schedule.Describe(apitypes.ReportDaily),
		"weekly", e.schedule.Describe(apitypes.ReportWeekly),
		"timezone", e.schedule.Loc().String())

	for {
		next, kind, ok := e.schedule.Next(e.now())
		if !ok {
			<-ctx.Done()
			return ctx.Err()
		}
		delay := next.Sub(e.now())
		if delay < 0 {
			delay = 0
		}
		e.log.Debug("next report scheduled", "kind", kind, "at", next.Format(time.RFC3339))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			e.log.Info("report scheduler stopped")
			return ctx.Err()
		case <-timer.C:
		}

		if _, err := e.SendReport(ctx, kind, false); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.log.Error("send scheduled report", "kind", kind, "error", err)
		}
	}
}

// SendReport collects the unreported events of every eligible watch and sends
// one aggregated message per notification channel. With dryRun it renders the
// report and contacts nothing, changing no state.
//
// Events are marked as reported only for the events that reached at least one
// channel: a run whose deliveries all failed leaves them pending for the next
// run.
func (e *Engine) SendReport(ctx context.Context, kind apitypes.ReportKind, dryRun bool) (*apitypes.ReportResult, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("check: unknown report kind %q (valid: daily, weekly)", kind)
	}

	e.reportMu.Lock()
	defer e.reportMu.Unlock()

	now := e.now().UTC()
	loc := e.schedule.Loc()

	// Period is informational: it comes from the previous run, so the body
	// says "since the last report" rather than a fixed rolling window.
	periodStart := now.Add(-defaultPeriod(kind))
	var previous *store.ReportState
	if state, err := e.store.GetReportState(ctx, kind); err == nil {
		previous = state
		if state.LastRunAt != nil && !state.LastRunAt.After(now) {
			periodStart = *state.LastRunAt
		}
	} else if !store.IsNotFound(err) {
		return nil, err
	}

	global, err := e.store.GetSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load global settings: %w", err)
	}
	globalAny := make(map[string]any, len(global))
	for k, v := range global {
		globalAny[k] = v
	}

	watches, err := e.store.AllEnabledWatches(ctx)
	if err != nil {
		return nil, err
	}

	selected := make([]*reportWatch, 0, len(watches))
	totalEvents := 0
	for _, watch := range watches {
		settings, err := e.settingsForGlobals(watch, globalAny)
		if err != nil {
			e.log.Warn("resolve flags for report", "watch", watch.ID, "error", err)
			continue
		}
		if !reportEnabled(kind, settings) {
			continue
		}
		events, err := e.store.UnreportedChangeEvents(ctx, watch.ID, kind, reportMaxEventsPerWatch)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		channels, err := e.store.ChannelsForWatch(ctx, watch.ID)
		if err != nil {
			return nil, err
		}
		selected = append(selected, &reportWatch{watch: watch, events: events, channels: channels})
		totalEvents += len(events)
	}

	result := &apitypes.ReportResult{
		Kind:    kind,
		DryRun:  dryRun,
		Events:  totalEvents,
		Watches: len(selected),
	}

	// Nothing changed in the period: say nothing. Recording the run keeps
	// `dit report status` honest about the scheduler still ticking.
	if len(selected) == 0 {
		title, body := renderReport(kind, periodStart, now, nil, loc)
		result.Title, result.Body = title, body
		if !dryRun {
			if err := e.saveReportState(ctx, kind, previous, 0, 0, now, "empty", ""); err != nil {
				return nil, err
			}
		}
		return result, nil
	}

	// Group by channel: one message per channel, carrying every eligible
	// watch that notifies it. A watch without explicit subscriptions lands in
	// the server's default channels through ChannelsForWatch.
	type group struct {
		channel *store.ChannelRecord
		items   []*reportWatch
	}
	byChannel := map[string]*group{}
	for _, item := range selected {
		for _, ch := range item.channels {
			g, ok := byChannel[ch.ID]
			if !ok {
				g = &group{channel: ch}
				byChannel[ch.ID] = g
			}
			g.items = append(g.items, item)
		}
	}
	groups := make([]*group, 0, len(byChannel))
	for _, g := range byChannel {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].channel.Name != groups[j].channel.Name {
			return groups[i].channel.Name < groups[j].channel.Name
		}
		return groups[i].channel.ID < groups[j].channel.ID
	})

	if dryRun {
		title, body := renderReport(kind, periodStart, now, selected, loc)
		result.Title, result.Body = title, body
		for _, g := range groups {
			events := 0
			for _, item := range g.items {
				events += len(item.events)
			}
			result.Channels = append(result.Channels, apitypes.ReportChannelResult{
				ChannelID:   g.channel.ID,
				ChannelName: g.channel.Name,
				ChannelType: string(g.channel.Type),
				Status:      "preview",
				Events:      events,
			})
		}
		return result, nil
	}

	if len(groups) == 0 {
		// Eligible events but no channel to tell: leave them pending.
		e.log.Warn("report has events but no notification channels",
			"kind", kind, "events", totalEvents, "watches", len(selected))
		result.Title, result.Body = renderReport(kind, periodStart, now, selected, loc)
		if err := e.saveReportState(ctx, kind, previous, len(selected), totalEvents, now,
			"no_channels", "no enabled notification channels"); err != nil {
			return nil, err
		}
		return result, nil
	}

	delivered := make([]string, 0, totalEvents)
	firstError := ""
	for _, g := range groups {
		title, body := renderReport(kind, periodStart, now, g.items, loc)
		events := 0
		for _, item := range g.items {
			events += len(item.events)
		}
		channelResult := apitypes.ReportChannelResult{
			ChannelID:   g.channel.ID,
			ChannelName: g.channel.Name,
			ChannelType: string(g.channel.Type),
			Events:      events,
		}

		transport, err := e.notifier.Build(g.channel.ID, string(g.channel.Type), g.channel.Config)
		if err != nil {
			channelResult.Status = "failed"
			channelResult.Error = "build channel: " + err.Error()
			result.Failed++
			if firstError == "" {
				firstError = channelResult.Error
			}
			e.log.Error("build report channel", "channel", g.channel.Name, "type", g.channel.Type, "error", err)
			result.Channels = append(result.Channels, channelResult)
			continue
		}

		attempts, sendErr := e.sendWithRetries(ctx, transport, notify.Message{
			Title:    title,
			Body:     body,
			Priority: "default",
			Tags:     []string{"calendar"},
		})
		channelResult.Attempts = attempts
		if sendErr != nil {
			channelResult.Status = "failed"
			channelResult.Error = sendErr.Error()
			result.Failed++
			if firstError == "" {
				firstError = channelResult.Error
			}
			e.log.Warn("report delivery failed", "kind", kind,
				"channel", g.channel.Name, "attempts", attempts, "error", sendErr)
		} else {
			channelResult.Status = "sent"
			result.Sent++
			for _, item := range g.items {
				for _, event := range item.events {
					delivered = append(delivered, event.ID)
				}
			}
			e.log.Info("report sent", "kind", kind, "channel", g.channel.Name, "events", events)
		}
		result.Channels = append(result.Channels, channelResult)
	}

	if len(delivered) > 0 {
		if err := e.store.MarkEventsReported(ctx, delivered, kind, now); err != nil {
			return nil, err
		}
		result.Marked = len(delivered)
	}

	status := "failed"
	switch {
	case result.Sent > 0:
		status = "sent"
	case result.Failed == 0:
		status = "empty"
	}
	if err := e.saveReportState(ctx, kind, previous, len(selected), totalEvents, now, status, firstError); err != nil {
		return nil, err
	}
	return result, nil
}

// saveReportState persists a run summary, preserving the previous last-sent
// time when this run delivered nothing.
func (e *Engine) saveReportState(ctx context.Context, kind apitypes.ReportKind, previous *store.ReportState, watches, events int, now time.Time, status, lastError string) error {
	state := &store.ReportState{
		Kind:        kind,
		LastRunAt:   &now,
		LastStatus:  status,
		LastError:   lastError,
		LastEvents:  events,
		LastWatches: watches,
		UpdatedAt:   now,
	}
	if previous != nil {
		state.LastSentAt = previous.LastSentAt
	}
	if status == "sent" {
		state.LastSentAt = &now
	}
	return e.store.SaveReportState(ctx, state)
}

// ReportStatus describes the schedule and last run of each report kind.
func (e *Engine) ReportStatus(ctx context.Context) (*apitypes.ReportStatus, error) {
	now := e.now()
	out := &apitypes.ReportStatus{Timezone: e.schedule.Loc().String()}
	for _, kind := range apitypes.ReportKinds() {
		info := apitypes.ReportInfo{Kind: kind, Enabled: e.schedule.Enabled(kind)}
		if info.Enabled {
			info.Schedule = e.schedule.Describe(kind)
			if next, ok := e.schedule.NextFor(kind, now); ok {
				info.NextRunAt = &next
			}
		}
		state, err := e.store.GetReportState(ctx, kind)
		switch {
		case err == nil:
			info.LastRunAt = state.LastRunAt
			info.LastSentAt = state.LastSentAt
			info.LastStatus = state.LastStatus
			info.LastError = state.LastError
			info.LastEvents = state.LastEvents
			info.LastWatches = state.LastWatches
		case store.IsNotFound(err):
			// Never ran; leave the fields empty.
		default:
			return nil, err
		}
		out.Reports = append(out.Reports, info)
	}
	return out, nil
}

// renderReport builds the title and body of one message. items may be nil,
// which renders the "no changes" report used by a preview and by a run that
// found nothing.
func renderReport(kind apitypes.ReportKind, periodStart, periodEnd time.Time, items []*reportWatch, loc *time.Location) (string, string) {
	events := 0
	for _, item := range items {
		events += len(item.events)
	}

	var title string
	switch {
	case events == 1 && len(items) == 1:
		title = fmt.Sprintf("dit %s report: 1 change in %s", kind, items[0].watch.Image)
	case events == 1:
		title = fmt.Sprintf("dit %s report: 1 change across %d images", kind, len(items))
	case events == 0:
		title = fmt.Sprintf("dit %s report: no changes", kind)
	default:
		title = fmt.Sprintf("dit %s report: %d changes across %d %s",
			kind, events, len(items), plural(len(items), "image", "images"))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "dit %s report\n", kind)
	fmt.Fprintf(&b, "Period: %s → %s\n", reportTime(periodStart, loc), reportTime(periodEnd, loc))
	if events == 0 {
		b.WriteString("\nNo image updates in this period.\n")
		return title, strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("\n")

	for _, item := range items {
		fmt.Fprintf(&b, "%s  (%s)\n", item.watch.Image, item.watch.ID)
		shown := item.events
		if len(shown) > reportMaxLinesPerWatch {
			shown = shown[:reportMaxLinesPerWatch]
		}
		for _, event := range shown {
			fmt.Fprintf(&b, "  %s  %s\n", reportTime(event.CreatedAt, loc), changeLine(event))
		}
		if rest := len(item.events) - len(shown); rest > 0 {
			fmt.Fprintf(&b, "  ... and %d more %s\n", rest, plural(rest, "change", "changes"))
		}
		if item.watch.ConsecutiveFailures > 0 {
			fmt.Fprintf(&b, "  ! currently failing (%d consecutive %s)",
				item.watch.ConsecutiveFailures, plural(item.watch.ConsecutiveFailures, "check", "checks"))
			if errText := truncate(item.watch.LastError, reportMaxErrorLen); errText != "" {
				fmt.Fprintf(&b, ": %s", errText)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return title, strings.TrimRight(b.String(), "\n")
}

// changeLine renders one event as a single report line.
func changeLine(event *store.EventRecord) string {
	switch event.Type {
	case apitypes.EventDigestChanged:
		oldDigest, newDigest := event.OldDigest, event.NewDigest
		if oldDigest == "" {
			return "digest changed: " + shortDigest(newDigest)
		}
		return "digest changed: " + shortDigest(oldDigest) + " → " + shortDigest(newDigest)
	case apitypes.EventNewTags:
		tags := newTagsOf(event)
		if len(tags) == 0 {
			return "new tags"
		}
		names := make([]string, 0, len(tags))
		for i, tag := range tags {
			if i == 8 {
				names = append(names, fmt.Sprintf("+%d more", len(tags)-8))
				break
			}
			names = append(names, tag.Tag)
		}
		return "new tags: " + strings.Join(names, ", ")
	case apitypes.EventSemverNewTag:
		previous := detailString(event, "previous")
		version := detailString(event, "version")
		if previous != "" && version != "" {
			return fmt.Sprintf("new version: %s → %s (tag %s)", previous, version, event.Tag)
		}
		return fmt.Sprintf("new version: %s (tag %s)", version, event.Tag)
	default:
		return string(event.Type)
	}
}

// reportTime formats a timestamp in the report's timezone.
func reportTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// plural picks the singular or plural form for a count.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// truncate shortens a string to n runes, adding an ellipsis when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

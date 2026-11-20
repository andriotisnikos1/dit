package check

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// setFlag stores one per-watch flag override, failing the test on error.
func setFlag(t *testing.T, db *store.DB, watchID, name string, value any) {
	t.Helper()
	if err := db.SetWatchFlag(context.Background(), watchID, name, value); err != nil {
		t.Fatalf("SetWatchFlag(%s, %s): %v", watchID, name, err)
	}
}

// testEngineWithSchedule builds an engine over an existing store with a report
// schedule and the same frozen clock as testEngine.
func testEngineWithSchedule(t *testing.T, db *store.DB, reg registry.Client, builder notify.Builder, schedule ReportSchedule) *Engine {
	t.Helper()
	engine, err := New(Options{
		Store:          db,
		Registry:       reg,
		Notifier:       builder,
		Interval:       time.Hour,
		Concurrency:    2,
		Timeout:        5 * time.Second,
		NotifyAttempts: 2,
		NotifyBackoff:  time.Millisecond,
		Now:            fixedNow,
		Rand:           rand.New(rand.NewSource(1)),
		Schedule:       schedule,
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	return engine
}

// baselineTagWatch seeds a tag watch with a first, silent digest.
func baselineTagWatch(t *testing.T, engine *Engine, fake *registry.Fake, watch *store.WatchRecord, digest string) {
	t.Helper()
	fake.SetDigest(watch.Registry, watch.Repository, watch.Ref, digest)
	if _, err := engine.CheckWatch(context.Background(), watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
}

// changeTagDigest moves a baseline watch onto a new digest.
func changeTagDigest(t *testing.T, engine *Engine, fake *registry.Fake, watch *store.WatchRecord, digest string) *apitypes.CheckResult {
	t.Helper()
	fake.SetDigest(watch.Registry, watch.Repository, watch.Ref, digest)
	result, err := engine.CheckWatch(context.Background(), watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("change check: %v", err)
	}
	return result
}

func TestNotifyIndividualFalseRecordsWithoutDelivering(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))

	setFlag(t, db, watch.ID, "notify_individual", false)

	result := changeTagDigest(t, engine, fake, watch, testutil.Digest(2))
	if result.EventID == "" {
		t.Fatal("EventID is empty: a muted change must still be recorded for reports and history")
	}
	if result.Notified != 0 {
		t.Errorf("Notified = %d, want 0 with notify_individual=false", result.Notified)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications, want 0", rec.Count())
	}

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.PendingNotifyCount != 0 {
		t.Errorf("PendingNotifyCount = %d after a muted change, want 0: muting must not accumulate a stale batch",
			stored.PendingNotifyCount)
	}
	if stored.LastDigest != testutil.Digest(2) {
		t.Errorf("LastDigest = %q, want the new digest: muting affects delivery, not detection", stored.LastDigest)
	}

	if _, err := db.GetEvent(ctx, result.EventID); err != nil {
		t.Errorf("GetEvent: %v; the muted event must exist", err)
	}
}

func TestMutingFlushesPendingThresholdBatch(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))

	setFlag(t, db, watch.ID, "notify_threshold", 5)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(2))
	if rec.Count() != 0 {
		t.Fatalf("sent %d notifications below the threshold, want 0", rec.Count())
	}

	setFlag(t, db, watch.ID, "notify_individual", false)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(3))

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.PendingNotifyCount != 0 {
		t.Fatalf("PendingNotifyCount = %d after muting, want 0 (the pending batch must be flushed, not fired later)",
			stored.PendingNotifyCount)
	}

	// Unmuting must start clean: one more change is below the threshold of 5.
	setFlag(t, db, watch.ID, "notify_individual", true)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(4))
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications after unmuting, want 0: the muted changes must not resurface as a batch",
			rec.Count())
	}
	stored, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.PendingNotifyCount != 1 {
		t.Errorf("PendingNotifyCount = %d after unmuting, want 1", stored.PendingNotifyCount)
	}
}

func TestMutedWatchStillNotifiesFailures(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))
	setFlag(t, db, watch.ID, "notify_individual", false)

	fake.SetDigestErr(testRegistry, testRepository, "1.27", errors.New("registry exploded"))
	result, err := engine.CheckWatch(context.Background(), watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if result.Error == "" {
		t.Fatal("Error is empty, want the registry failure")
	}
	if result.Notified != 1 {
		t.Errorf("Notified = %d, want 1: notify_individual must not silence failure alerts", result.Notified)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1", rec.Count())
	}
}

func TestDailyReportAggregatesWatchesPerChannel(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	first := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	second := testutil.Watch(t, db, testRegistry, "library/redis", "7", nil)
	baselineTagWatch(t, engine, fake, first, testutil.Digest(1))
	baselineTagWatch(t, engine, fake, second, testutil.Digest(3))

	for _, watch := range []*store.WatchRecord{first, second} {
		setFlag(t, db, watch.ID, "notify_individual", false)
		setFlag(t, db, watch.ID, "report_daily", true)
	}
	changeTagDigest(t, engine, fake, first, testutil.Digest(2))
	changeTagDigest(t, engine, fake, second, testutil.Digest(4))

	if rec.Count() != 0 {
		t.Fatalf("sent %d individual notifications, want 0", rec.Count())
	}

	result, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	if result.Events != 2 || result.Watches != 2 {
		t.Errorf("report = %d events / %d watches, want 2/2", result.Events, result.Watches)
	}
	if result.Sent != 1 || result.Failed != 0 {
		t.Errorf("sent=%d failed=%d, want 1/0 (one aggregated message on the default channel)",
			result.Sent, result.Failed)
	}
	if result.Marked != 2 {
		t.Errorf("Marked = %d, want 2", result.Marked)
	}
	if rec.Count() != 1 {
		t.Fatalf("channel received %d messages, want 1 aggregated report", rec.Count())
	}

	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "2 changes") || !strings.Contains(msg.Title, "2 images") {
		t.Errorf("title = %q, want a 2-changes/2-images summary", msg.Title)
	}
	for _, image := range []string{first.Image, second.Image} {
		if !strings.Contains(msg.Body, image) {
			t.Errorf("body does not mention %s:\n%s", image, msg.Body)
		}
	}
	if !strings.Contains(msg.Body, "digest changed") {
		t.Errorf("body has no change detail:\n%s", msg.Body)
	}

	// Every event was delivered once; the second run has nothing to say.
	again, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("second SendReport: %v", err)
	}
	if again.Events != 0 || again.Marked != 0 {
		t.Errorf("second report = %d events / %d marked, want 0/0", again.Events, again.Marked)
	}
	if rec.Count() != 1 {
		t.Errorf("an empty report sent a message; channel has %d messages", rec.Count())
	}
}

func TestReportExcludesWatchWithoutFlag(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	included := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	excluded := testutil.Watch(t, db, testRegistry, "library/redis", "7", nil)
	baselineTagWatch(t, engine, fake, included, testutil.Digest(1))
	baselineTagWatch(t, engine, fake, excluded, testutil.Digest(3))

	setFlag(t, db, included.ID, "notify_individual", false)
	setFlag(t, db, included.ID, "report_daily", true)
	setFlag(t, db, excluded.ID, "notify_individual", false)

	changeTagDigest(t, engine, fake, included, testutil.Digest(2))
	changeTagDigest(t, engine, fake, excluded, testutil.Digest(4))

	result, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	if result.Watches != 1 || result.Events != 1 {
		t.Errorf("report = %d watches / %d events, want 1/1", result.Watches, result.Events)
	}
	msg := rec.Messages()[0]
	if strings.Contains(msg.Body, excluded.Image) {
		t.Errorf("report mentions %s although report_daily is off there:\n%s", excluded.Image, msg.Body)
	}
	// The excluded watch's event is still waiting for its own report.
	if n, err := db.CountUnreportedChangeEvents(ctx, excluded.ID, apitypes.ReportDaily); err != nil || n != 1 {
		t.Errorf("excluded unreported = (%d, %v), want (1, nil)", n, err)
	}
}

func TestReportDryRunSendsAndMarksNothing(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))
	setFlag(t, db, watch.ID, "notify_individual", false)
	setFlag(t, db, watch.ID, "report_daily", true)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(2))

	result, err := engine.SendReport(ctx, apitypes.ReportDaily, true)
	if err != nil {
		t.Fatalf("SendReport(dry run): %v", err)
	}
	if !result.DryRun {
		t.Error("DryRun = false on a preview")
	}
	if result.Title == "" || result.Body == "" {
		t.Error("preview returned an empty title or body")
	}
	if result.Marked != 0 {
		t.Errorf("Marked = %d on a dry run, want 0", result.Marked)
	}
	if rec.Count() != 0 {
		t.Errorf("dry run delivered %d messages, want 0", rec.Count())
	}
	if n, _ := db.CountUnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily); n != 1 {
		t.Errorf("unreported after a dry run = %d, want 1", n)
	}
	if _, err := db.GetReportState(ctx, apitypes.ReportDaily); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("dry run wrote report state: %v", err)
	}
}

func TestDailyAndWeeklyReportMarkersAreIndependent(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))
	setFlag(t, db, watch.ID, "notify_individual", false)
	setFlag(t, db, watch.ID, "report_daily", true)
	setFlag(t, db, watch.ID, "report_weekly", true)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(2))

	daily, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("daily SendReport: %v", err)
	}
	if daily.Events != 1 || daily.Marked != 1 {
		t.Fatalf("daily = %d events / %d marked, want 1/1", daily.Events, daily.Marked)
	}

	weekly, err := engine.SendReport(ctx, apitypes.ReportWeekly, false)
	if err != nil {
		t.Fatalf("weekly SendReport: %v", err)
	}
	if weekly.Events != 1 || weekly.Marked != 1 {
		t.Errorf("weekly = %d events / %d marked, want 1/1: a daily report must not consume the weekly roll-up",
			weekly.Events, weekly.Marked)
	}
	if rec.Count() != 2 {
		t.Errorf("channel received %d messages, want 2 (daily + weekly)", rec.Count())
	}

	if _, err := engine.SendReport(ctx, apitypes.ReportWeekly, false); err != nil {
		t.Fatalf("second weekly: %v", err)
	}
	if rec.Count() != 2 {
		t.Errorf("an empty weekly report sent a message")
	}
}

func TestReportMarksOnlyDeliveredChannels(t *testing.T) {
	engine, db, fake, builder, _ := newEngine(t)
	ctx := context.Background()

	// Watches whose channel delivery fails keep their events for the next run.
	other, otherRec := testutil.Channel(t, db, builder, "broken", false)
	otherRec.FailFor = func(notify.Message) bool { return true }

	healthy := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	broken := testutil.Watch(t, db, testRegistry, "library/redis", "7", []string{other.ID})
	baselineTagWatch(t, engine, fake, healthy, testutil.Digest(1))
	baselineTagWatch(t, engine, fake, broken, testutil.Digest(3))

	for _, watch := range []*store.WatchRecord{healthy, broken} {
		setFlag(t, db, watch.ID, "notify_individual", false)
		setFlag(t, db, watch.ID, "report_daily", true)
	}
	changeTagDigest(t, engine, fake, healthy, testutil.Digest(2))
	changeTagDigest(t, engine, fake, broken, testutil.Digest(4))

	result, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	if result.Sent != 1 || result.Failed != 1 {
		t.Errorf("sent=%d failed=%d, want 1/1", result.Sent, result.Failed)
	}
	if result.Marked != 1 {
		t.Errorf("Marked = %d, want 1 (only the delivered watch)", result.Marked)
	}

	if n, _ := db.CountUnreportedChangeEvents(ctx, healthy.ID, apitypes.ReportDaily); n != 0 {
		t.Errorf("delivered watch still has %d unreported events, want 0", n)
	}
	if n, _ := db.CountUnreportedChangeEvents(ctx, broken.ID, apitypes.ReportDaily); n != 1 {
		t.Errorf("failed watch has %d unreported events, want 1 for the next run", n)
	}

	// Recovery: once the channel works, the next run delivers only the
	// pending event.
	otherRec.FailFor = nil
	otherRec.Reset()
	again, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("retry SendReport: %v", err)
	}
	if again.Events != 1 || again.Sent != 1 || again.Marked != 1 {
		t.Errorf("retry = %d events / %d sent / %d marked, want 1/1/1", again.Events, again.Sent, again.Marked)
	}
}

func TestReportIncludesCurrentlyFailingWatch(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))
	setFlag(t, db, watch.ID, "notify_individual", false)
	setFlag(t, db, watch.ID, "report_daily", true)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(2))

	if err := db.MarkWatchFailure(ctx, watch.ID, "unauthorized: bad token", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MarkWatchFailure: %v", err)
	}
	if _, err := engine.SendReport(ctx, apitypes.ReportDaily, false); err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Body, "currently failing") || !strings.Contains(msg.Body, "bad token") {
		t.Errorf("report body does not warn about the failing watch:\n%s", msg.Body)
	}
}

func TestReportWithNoChannelsKeepsEvents(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))

	// Detach the default channel by disabling it; the watch then resolves to
	// no channels at all.
	channels, _, err := db.ListChannels(ctx, store.ChannelFilter{})
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	disabled := false
	for _, ch := range channels {
		if _, err := db.UpdateChannel(ctx, ch.ID, store.UpdateChannelInput{Enabled: &disabled}); err != nil {
			t.Fatalf("UpdateChannel: %v", err)
		}
	}

	setFlag(t, db, watch.ID, "notify_individual", false)
	setFlag(t, db, watch.ID, "report_daily", true)
	changeTagDigest(t, engine, fake, watch, testutil.Digest(2))

	result, err := engine.SendReport(ctx, apitypes.ReportDaily, false)
	if err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	if result.Sent != 0 || result.Failed != 0 || result.Marked != 0 {
		t.Errorf("sent=%d failed=%d marked=%d, want 0/0/0 with no channels", result.Sent, result.Failed, result.Marked)
	}
	state, err := db.GetReportState(ctx, apitypes.ReportDaily)
	if err != nil {
		t.Fatalf("GetReportState: %v", err)
	}
	if state.LastStatus != "no_channels" {
		t.Errorf("status = %q, want no_channels", state.LastStatus)
	}
	if n, _ := db.CountUnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily); n != 1 {
		t.Errorf("unreported = %d, want 1: events must wait for a channel", n)
	}
}

func TestReportPruningKeepsPendingEvents(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	baselineTagWatch(t, engine, fake, watch, testutil.Digest(1))
	setFlag(t, db, watch.ID, "history", 1)
	setFlag(t, db, watch.ID, "notify_individual", false)
	setFlag(t, db, watch.ID, "report_daily", true)

	for seed := byte(2); seed <= 4; seed++ {
		changeTagDigest(t, engine, fake, watch, testutil.Digest(seed))
	}
	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 50})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 3 {
		t.Fatalf("history=1 pruned pending report events: %d remain, want 3 (events: %v)", total, reportEventIDs(events))
	}

	if _, err := engine.SendReport(ctx, apitypes.ReportDaily, false); err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	// Delivered events are ordinary history again.
	changeTagDigest(t, engine, fake, watch, testutil.Digest(5))
	events, total, err = db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 50})
	if err != nil {
		t.Fatalf("ListEvents after prune: %v", err)
	}
	if total != 1 {
		t.Errorf("after delivery and one more change, %d events remain, want 1 (events: %v)", total, reportEventIDs(events))
	}
}

func TestReportRejectsUnknownKind(t *testing.T) {
	engine, _, _, _, _ := newEngine(t)
	if _, err := engine.SendReport(context.Background(), "hourly", true); err == nil {
		t.Error("SendReport accepted an unknown kind")
	}
}

func TestReportStatusDescribesSchedule(t *testing.T) {
	engine, db, fake, builder, _ := newEngine(t)
	daily := Clock{Hour: 8, Minute: 0}
	scheduled := testEngineWithSchedule(t, db, fake, builder, ReportSchedule{
		Daily:    &daily,
		Location: time.UTC,
	})
	_ = engine

	status, err := scheduled.ReportStatus(context.Background())
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	if len(status.Reports) != 2 {
		t.Fatalf("status reports = %d, want 2", len(status.Reports))
	}
	if !status.Reports[0].Enabled || status.Reports[1].Enabled {
		t.Errorf("enabled flags = %v/%v, want daily enabled and weekly disabled",
			status.Reports[0].Enabled, status.Reports[1].Enabled)
	}
	wantNext := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) // fixedNow is 2026-09-11 12:00
	if status.Reports[0].NextRunAt == nil || !status.Reports[0].NextRunAt.Equal(wantNext) {
		t.Errorf("daily next run = %v, want %v", status.Reports[0].NextRunAt, wantNext)
	}
	if status.Reports[0].Schedule != "daily at 08:00 (UTC)" {
		t.Errorf("schedule = %q", status.Reports[0].Schedule)
	}

	if _, err := scheduled.SendReport(context.Background(), apitypes.ReportDaily, false); err != nil {
		t.Fatalf("SendReport: %v", err)
	}
	status, err = scheduled.ReportStatus(context.Background())
	if err != nil {
		t.Fatalf("ReportStatus after run: %v", err)
	}
	if status.Reports[0].LastStatus != "empty" {
		t.Errorf("last status = %q, want empty", status.Reports[0].LastStatus)
	}
	if status.Reports[0].LastRunAt == nil {
		t.Error("LastRunAt is nil after a run")
	}
}

func TestClockParsingAndScheduling(t *testing.T) {
	if clock, err := ParseClock("8:05"); err != nil || clock != (Clock{Hour: 8, Minute: 5}) {
		t.Errorf("ParseClock(8:05) = (%+v, %v), want 08:05", clock, err)
	}
	for _, bad := range []string{"", "24:00", "08:60", "noon", "08:00:00", "-1:00"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) accepted an invalid time", bad)
		}
	}

	eight := Clock{Hour: 8}
	noon := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) // Friday
	if next := eight.Next(noon); !next.Equal(time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("Next after noon = %v, want tomorrow 08:00", next)
	}
	morning := time.Date(2026, 9, 11, 6, 0, 0, 0, time.UTC)
	if next := eight.Next(morning); !next.Equal(time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("Next before 08:00 = %v, want today 08:00", next)
	}
	// A schedule at exactly the current minute must move to the next period,
	// not fire again in a tight loop.
	exact := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	if next := eight.Next(exact); !next.Equal(time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("Next at exactly 08:00 = %v, want tomorrow", next)
	}

	monday := eight.NextWeekday(noon, time.Monday)
	if !monday.Equal(time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("next Monday = %v, want 2026-09-14 08:00", monday)
	}
	sameDay := eight.NextWeekday(noon, time.Friday)
	if !sameDay.Equal(time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("next Friday from Friday noon = %v, want a week later", sameDay)
	}

	schedule := ReportSchedule{Daily: &eight, Location: time.UTC}
	next, kind, ok := schedule.Next(noon)
	if !ok || kind != apitypes.ReportDaily || !next.Equal(time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("schedule.Next = (%v, %v, %v)", next, kind, ok)
	}
	if _, _, ok := (ReportSchedule{}).Next(noon); ok {
		t.Error("a disabled schedule reported a next run")
	}
}

func TestRunReporterReturnsWhenDisabled(t *testing.T) {
	engine := testEngineWithSchedule(t, testutil.Store(t), registry.NewFake(), notify.NewFakeBuilder(), ReportSchedule{})
	done := make(chan error, 1)
	go func() { done <- engine.RunReporter(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunReporter(disabled) = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("RunReporter with no schedule did not return")
	}
}

func reportEventIDs(events []*store.EventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s@%s", e.Type, e.CreatedAt.Format(time.RFC3339Nano)))
	}
	return out
}

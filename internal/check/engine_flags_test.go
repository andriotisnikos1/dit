package check

import (
	"context"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

func TestNotifyThresholdBatchesAndPrunes(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	// Baseline: silent.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// Changes 1 and 2: recorded, not notified, still retained as pending.
	for i := 2; i <= 3; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
		if err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
		if result.Notified != 0 {
			t.Errorf("change %d notified %d channels, want 0 (below threshold)", i, result.Notified)
		}
		if rec.Count() != 0 {
			t.Fatalf("sent %d notifications below the threshold, want 0", rec.Count())
		}
	}

	// Change 3 crosses the threshold: exactly one batch notification.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(4))
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("firing check: %v", err)
	}
	if result.Notified != 1 {
		t.Errorf("Notified = %d, want 1", result.Notified)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications on the firing check, want exactly 1", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "3 changes") {
		t.Errorf("title = %q, want it to name the batch size", msg.Title)
	}
	// The batch body must summarize the earlier events so the operator sees
	// all three changes, not just the firing one.
	for _, want := range []string{"Earlier changes", testutil.Digest(2), testutil.Digest(3)} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("batch body missing %q:\n%s", want, msg.Body)
		}
	}

	// The counter reset: the next change does not notify.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(5))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("post-batch check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications after the batch, want still 1", rec.Count())
	}

	// History default of 1: the watch keeps exactly one change event.
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 {
		t.Errorf("kept %d events after pruning, want 1 (history default)", total)
	}
}

func TestHistoryFlagKeepsNEvents(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	for i := 2; i <= 4; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}

	// history=2 with threshold=1: every change notified, but only the two
	// newest change events are retained.
	if rec.Count() != 3 {
		t.Errorf("sent %d notifications, want 3 (threshold 1)", rec.Count())
	}
	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 2 {
		t.Fatalf("kept %d change events, want 2", total)
	}
	if len(events) > 0 && events[0].NewDigest != testutil.Digest(4) {
		t.Errorf("newest kept event = %q, want %q", events[0].NewDigest, testutil.Digest(4))
	}
}

func TestHistoryUnlimitedDisablesPruning(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "-1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	for i := 2; i <= 4; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 3 {
		t.Errorf("kept %d events with history=-1, want all 3", total)
	}
}

func TestLocalFlagOverridesGlobal(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	// Global 3, local 1: the watch's local override wins, so every change
	// still notifies.
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "notify_threshold", 1); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (local threshold 1 beats global 3)", rec.Count())
	}
}

func TestPatternNotifyThresholdCountsBatchesNotTags(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "1.27.*")
	if err := db.SetSetting(ctx, "notify_threshold", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "1.27.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// A check that discovers two new tags at once is ONE update event.
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1", "1.27.2")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("two-tag check: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("sent %d notifications, want 0 (1 of 2 updates)", rec.Count())
	}

	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1", "1.27.2", "1.27.3")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("firing check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (2 of 2 updates)", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "2 changes") {
		t.Errorf("title = %q, want a batch title", msg.Title)
	}
	if !strings.Contains(msg.Body, "1.27.2") {
		t.Errorf("batch body missing the earlier new tags:\n%s", msg.Body)
	}
}

func TestSemverPatternWatchReportsBumps(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	// Baseline: the highest existing version seeds silently.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	fake.SetDigest(testRegistry, testRepository, "v1.1.0", testutil.Digest(1))
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	if result.EventID != "" || rec.Count() != 0 {
		t.Fatalf("baseline seeded loudly: event=%q sent=%d", result.EventID, rec.Count())
	}

	// A version bump fires a semver_new_tag event.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0", "v1.2.0")
	fake.SetDigest(testRegistry, testRepository, "v1.2.0", testutil.Digest(2))
	result, err = engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("bump check: %v", err)
	}
	if result.EventID == "" {
		t.Fatal("no event recorded for a version bump")
	}
	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if event.Type != apitypes.EventSemverNewTag {
		t.Errorf("event type = %q, want semver_new_tag", event.Type)
	}
	if event.Tag != "v1.2.0" {
		t.Errorf("event tag = %q, want v1.2.0", event.Tag)
	}
	if event.NewDigest != testutil.Digest(2) {
		t.Errorf("event digest = %q, want %q", event.NewDigest, testutil.Digest(2))
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "new version") || !strings.Contains(msg.Title, "v1.2.0") {
		t.Errorf("title = %q, want a new-version title naming the tag", msg.Title)
	}
	if !strings.Contains(msg.Body, "1.1.0") {
		t.Errorf("body does not name the previous version:\n%s", msg.Body)
	}

	// Non-semver tags appear but produce no event (semver mode replaces
	// new-tags reporting).
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0", "v1.2.0", "nightly")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("non-semver check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("non-semver tag notified: %d messages, want still 1", rec.Count())
	}
}

func TestSemverStableSkipsPrereleases(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_stable", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0-rc.1")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("rc check: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("stable mode notified on a prerelease: %d messages", rec.Count())
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0-rc.1", "v1.1.0")
	fake.SetDigest(testRegistry, testRepository, "v1.1.0", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("stable check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("stable mode did not notify on the stable release: %d messages", rec.Count())
	}
}

func TestSemverNotifyThresholdBatches(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 2); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump 1: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("notified on bump 1 of 2: %d messages", rec.Count())
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump 2: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications on bump 2, want 1", rec.Count())
	}
	if !strings.Contains(rec.Messages()[0].Title, "2 changes") {
		t.Errorf("title = %q, want a batch title", rec.Messages()[0].Title)
	}
}

func TestSemverModeSuppressesNewTagEvents(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// v0.9.0 appears: new tag, but NOT a semver bump — must not produce an
	// event of any kind.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v0.9.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("lower-version check: %v", err)
	}
	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 0 {
		t.Errorf("recorded %d events for a non-bump new tag, want 0: %v", total, events)
	}

	// The tag baseline was still maintained, so toggling semver off does not
	// re-report v0.9.0.
	if err := db.UnsetWatchFlag(ctx, watch.ID, "semver"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("post-toggle check: %v", err)
	}
	events, total, err = db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 0 {
		t.Errorf("toggling semver off re-reported known tags: %v", events)
	}
}

func TestThresholdLoweredToOneFlushesPendingBatch(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	// Two changes accumulate below the threshold.
	for i := 2; i <= 3; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 0 {
		t.Fatalf("sent %d notifications below threshold, want 0", rec.Count())
	}

	// The operator drops the threshold to 1: the next change must notify the
	// whole accumulated batch rather than stranding the debt.
	if err := db.SetSetting(ctx, "notify_threshold", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(4))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("post-lowering check: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications after lowering, want 1 batch", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "3 changes") {
		t.Errorf("title = %q, want the 3-event batch summary", msg.Title)
	}
	if !strings.Contains(msg.Body, testutil.Digest(2)) {
		t.Errorf("batch body missing the earliest pending change:\n%s", msg.Body)
	}

	// The counter reset: the next change notifies alone, with a plain title.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(5))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("follow-up check: %v", err)
	}
	if rec.Count() != 2 {
		t.Fatalf("sent %d notifications total, want 2 (batch + single)", rec.Count())
	}
	last := rec.Messages()[1]
	if strings.Contains(last.Title, "changes") {
		t.Errorf("follow-up title = %q, want a plain single-change title", last.Title)
	}
}

func TestFailureEventsPrunedIndependentlyOfPendingChanges(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	// Baseline.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// A failing check records check_failed and notifies once.
	failDigest(t, fake)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failing check: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications on the failure, want 1", rec.Count())
	}

	// Recovery + a digest change in the same check: a check_recovered event
	// (notified immediately) plus a pending change event held for the batch.
	clearDigestFailure(t, fake)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("recovery check: %v", err)
	}
	if rec.Count() != 2 {
		t.Fatalf("sent %d notifications after recovery, want 2 (failure + recovery)", rec.Count())
	}

	// A second outage records another check_failed: failure-family pruning
	// keeps only the newest failure event, while the pending change event
	// survives (protected by the pending counter).
	failDigest(t, fake)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("second failing check: %v", err)
	}

	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 2 {
		t.Fatalf("kept %d events, want 2 (newest failure + pending change)", total)
	}
	types := map[apitypes.EventType]int{}
	for _, e := range events {
		types[e.Type]++
	}
	if types[apitypes.EventCheckFailed] != 1 || types[apitypes.EventDigestChanged] != 1 {
		t.Errorf("kept event types = %v, want one check_failed and one digest_changed", types)
	}
	if types[apitypes.EventCheckRecovered] != 0 {
		t.Errorf("the older check_recovered event survived failure-family pruning")
	}
}

// failDigest programs a digest resolution failure for the default watch's tag;
// clearDigestFailure undoes it. They mirror the inline Fake mechanism used by
// the existing failure tests in engine_test.go.
func failDigest(t *testing.T, fake *registry.Fake) {
	t.Helper()
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry,
			Repository: testRepository, Ref: "1.27"})
}

func clearDigestFailure(t *testing.T, fake *registry.Fake) {
	t.Helper()
	fake.DigestErr = map[string]error{}
}

func TestBatchNotificationRetryRendersIdentically(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	var result *apitypes.CheckResult
	for i := 2; i <= 4; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		var err error
		result, err = engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
		if err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1 batch", rec.Count())
	}
	if result == nil || result.EventID == "" {
		t.Fatal("no firing event recorded")
	}
	delivered := rec.Messages()[0]

	// Rebuild the message exactly the way a notification retry does: reload
	// the watch and the event from the store, then BuildMessage.
	reloadedWatch, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	reloadedEvent, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	again := BuildMessage(reloadedWatch, reloadedEvent)
	if again.Title != delivered.Title {
		t.Errorf("retry title %q != delivered %q", again.Title, delivered.Title)
	}
	if again.Body != delivered.Body {
		t.Errorf("retry body differs from the delivered body:\n--- delivered:\n%s\n--- retry:\n%s", delivered.Body, again.Body)
	}
}

func TestSemverToggleOffResetsStaleCounter(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 2); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	// One bump accumulates below the semver threshold.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump check: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("notified %d on a below-threshold bump, want 0", rec.Count())
	}

	// Toggling semver off: the next (plain) check clears the stale counter.
	if err := db.UnsetWatchFlag(ctx, watch.ID, "semver"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("plain check: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingSemverCount != 0 {
		t.Errorf("stale semver counter = %d after toggling off, want 0 (self-healed)", got.PendingSemverCount)
	}

	// Retention is no longer inflated by the stale debt.
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 {
		t.Errorf("kept %d events, want 1 (history default, debt cleared)", total)
	}
}

func TestGarbageLatestSemverReseedsSilently(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	// Simulate a corrupted stored version.
	if err := db.SetLatestSemver(ctx, watch.ID, "not-a-version", "not-a-version"); err != nil {
		t.Fatalf("SetLatestSemver: %v", err)
	}

	// The next check reseeds silently from the current highest.
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("reseed check: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("reseeding notified %d, want silent", rec.Count())
	}

	// Bump reporting works again afterwards.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump check: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications on the post-reseed bump, want 1", rec.Count())
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.2.0" {
		t.Errorf("latest semver = %q, want 1.2.0", got.LatestSemver)
	}
}

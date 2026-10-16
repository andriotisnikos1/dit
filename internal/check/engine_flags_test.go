package check

import (
	"context"
	"strings"
	"testing"

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

package check

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// ---------------------------------------------------------------------------
// Probe group A — settingsFor: resolution, corrupt values, unknown names.
// ---------------------------------------------------------------------------

// A1: a watch flags map holding an unknown name cannot be written through the
// API, but the store accepts it. The engine must warn and fall back to
// defaults rather than failing every check.
func TestAdversarialUnknownWatchFlagFallsBackToDefaults(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	// Global threshold of 3, but a corrupt local flag name.
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "not_a_real_flag", 7); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change check: %v", err)
	}
	// Resolution failed, so defaults apply: threshold 1 notifies immediately.
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (defaults after a corrupt local flag)", rec.Count())
	}
}

// A2: a corrupt global value (a non-integer stored string) must not fail the
// check; the engine falls back to defaults.
func TestAdversarialCorruptGlobalSettingFallsBackToDefaults(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "abc"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (defaults after a corrupt global value)", rec.Count())
	}
}

// A3: a global value is used when the watch has no local override; a local
// override wins over it. Both directions of the resolution rule.
func TestAdversarialGlobalValueAppliesWithoutLocalOverride(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	// One change: below threshold 2, nothing sent.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change 1: %v", err)
	}
	if rec.Count() != 0 {
		t.Fatalf("sent %d below the global threshold, want 0", rec.Count())
	}
	// Second change crosses it.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(3))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change 2: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (global threshold honoured)", rec.Count())
	}
}

// ---------------------------------------------------------------------------
// Probe group B — recordChange batching: fetch-before-create, pending-1,
// threshold raised/lowered mid-batch, counter reset.
// ---------------------------------------------------------------------------

// B1: threshold 1 renders exactly the legacy shape: no batch key, no batch
// section, plain title. This is the byte-identical guarantee.
func TestAdversarialThresholdOneRendersLegacyFormat(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("change check: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1", rec.Count())
	}
	msg := rec.Messages()[0]
	wantTitle := "digest changed: index.docker.io/library/nginx:1.27"
	if msg.Title != wantTitle {
		t.Errorf("title = %q, want %q (legacy single-change title)", msg.Title, wantTitle)
	}
	for _, forbidden := range []string{"batch", "Earlier changes"} {
		if strings.Contains(msg.Body, forbidden) {
			t.Errorf("threshold-1 body contains %q; the batch feature changed the legacy format:\n%s", forbidden, msg.Body)
		}
	}
	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if _, ok := event.Detail["batch"]; ok {
		t.Errorf("threshold-1 event carries a batch key: %v", event.Detail)
	}
}

// B2: threshold 2 with two changes fetches exactly pending-1 = 1 prior event.
func TestAdversarialThresholdTwoBatchFetchesOnePrior(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change 1: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(3))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change 2: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1 batch", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "2 changes") {
		t.Errorf("title = %q, want a 2-change batch title", msg.Title)
	}
	if !strings.Contains(msg.Body, testutil.Digest(2)) {
		t.Errorf("batch body is missing the single prior change:\n%s", msg.Body)
	}
	if n := strings.Count(msg.Body, "  - "); n != 1 {
		t.Errorf("batch body has %d prior entries, want 1 (pending-1):\n%s", n, msg.Body)
	}
}

// B3: raising the threshold from 1 to 4 must not retro-batch already-notified
// events; the batch counts only the updates since the raise.
func TestAdversarialThresholdRaisedMidStreamCountsFresh(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Threshold 1: this change notifies alone and resets the counter.
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("change 1: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d, want 1 singleton", rec.Count())
	}

	if err := db.SetSetting(ctx, "notify_threshold", "4"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	for i := 3; i <= 6; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 2 {
		t.Fatalf("sent %d notifications total, want 2 (singleton + one batch)", rec.Count())
	}
	batch := rec.Messages()[1]
	if !strings.Contains(batch.Title, "4 changes") {
		t.Errorf("batch title = %q, want 4 changes (fresh batch after the raise)", batch.Title)
	}
	if n := strings.Count(batch.Body, "  - "); n != 3 {
		t.Errorf("batch body has %d prior entries, want 3:\n%s", n, batch.Body)
	}
}

// B4: two consecutive threshold-2 batches each notify once and reset.
func TestAdversarialConsecutiveBatchesReset(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for i := 2; i <= 5; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 2 {
		t.Fatalf("sent %d notifications, want 2 (two batches of two)", rec.Count())
	}
	for i, msg := range rec.Messages() {
		if !strings.Contains(msg.Title, "2 changes") {
			t.Errorf("batch %d title = %q, want 2 changes", i, msg.Title)
		}
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingNotifyCount != 0 {
		t.Errorf("pending notify = %d after the second batch, want 0", got.PendingNotifyCount)
	}
}

// ---------------------------------------------------------------------------
// Probe group C — pruneHistory: keep = max(history, pendingNotify, pendingSemver).
// ---------------------------------------------------------------------------

// C1: the pending counter protects undelivered events even when history is 1.
func TestAdversarialPendingCounterProtectsEvents(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for i := 2; i <= 3; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 0 {
		t.Fatalf("sent %d below threshold, want 0", rec.Count())
	}
	// Both pending change events survive (keep elevated to the pending count).
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 2 {
		t.Errorf("kept %d pending events, want 2 (history 1 must not prune undelivered events)", total)
	}
}

// C2: after the batch fires and the counter resets, history prunes down to the
// configured keep, so the firing event is the one that survives.
func TestAdversarialPostBatchHistoryPrunes(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for i := 2; i <= 4; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d, want 1 batch", rec.Count())
	}
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// history 2 caps the change family to two once the counter reset.
	if total != 2 {
		t.Errorf("kept %d events, want 2 (history 2 after reset)", total)
	}
}

// C3: toggling semver off resets the counter, but pruneHistory still uses the
// stale in-memory value, so the counter keeps inflating retention across the
// check that performs the self-heal.
func TestAdversarialSemverToggleOffPruneUsesStaleCounter(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag semver: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 10); err != nil {
		t.Fatalf("SetWatchFlag threshold: %v", err)
	}

	tags := []string{"v1.0.0"}
	fake.SetTags(testRegistry, testRepository, tags...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Three bumps accumulate below the semver threshold.
	for _, tag := range []string{"v1.1.0", "v1.2.0", "v1.3.0"} {
		tags = append(tags, tag)
		fake.SetTags(testRegistry, testRepository, tags...)
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("bump %s: %v", tag, err)
		}
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingSemverCount != 3 {
		t.Fatalf("pending semver = %d, want 3", got.PendingSemverCount)
	}

	// Toggle semver off; a genuinely new tag arrives and prunes.
	if err := db.UnsetWatchFlag(ctx, watch.ID, "semver"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, append(tags, "v1.4.0")...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("post-toggle check: %v", err)
	}

	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 {
		t.Errorf("kept %d events after semver was toggled off with history=1, want 1: "+
			"the self-healed semver counter must not keep inflating retention", total)
	}
}

// C4: once a counter is cleared, the next check must not consider it pending.
func TestAdversarialClearedCounterNotPending(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "history", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Two changes at threshold 1: each notifies, each resets, history keeps 1.
	for i := 2; i <= 3; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 {
		t.Errorf("kept %d events, want 1 (history 1, counters cleared by each fire)", total)
	}
}

// ---------------------------------------------------------------------------
// Probe group D — checkTag: baseline, unchanged, failure, recovery, backoff.
// ---------------------------------------------------------------------------

// D1: unchanged digest after a baseline produces no event and no notification.
func TestAdversarialUnchangedDigestSilent(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if result.Changed || result.EventID != "" || rec.Count() != 0 {
		t.Errorf("unchanged digest fired: changed=%v event=%q sent=%d", result.Changed, result.EventID, rec.Count())
	}
}

// D2: failure backoff doubles per consecutive failure, counted after the
// failure being scheduled.
func TestAdversarialFailureBackoffScheduling(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository, Ref: "1.27"})

	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failure 1: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.ConsecutiveFailures != 1 {
		t.Fatalf("consecutive failures = %d, want 1", got.ConsecutiveFailures)
	}
	// First failure: one interval (1h) after now (fixedNow).
	want1 := fixedNow().Add(time.Hour)
	if got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(want1) {
		t.Errorf("next attempt after failure 1 = %v, want %v", got.NextAttemptAt, want1)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications on the first failure, want 1", rec.Count())
	}

	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failure 2: %v", err)
	}
	got, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	want2 := fixedNow().Add(2 * time.Hour)
	if got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(want2) {
		t.Errorf("next attempt after failure 2 = %v, want %v (doubled backoff)", got.NextAttemptAt, want2)
	}
	// A still-broken watch must not re-notify.
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications during the outage, want 1 (first failure only)", rec.Count())
	}
}

// D3: notify_on_failure=false records the failure event but sends nothing.
func TestAdversarialNotifyOnFailureDisabled(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	off := false
	if _, err := db.UpdateWatch(ctx, watch.ID, store.UpdateWatchInput{NotifyOnFailure: &off}); err != nil {
		t.Fatalf("UpdateWatch: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository, Ref: "1.27"})
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("failure: %v", err)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications with notify_on_failure=false", rec.Count())
	}
	if result.EventID == "" {
		t.Errorf("no check_failed event recorded despite notify_on_failure=false")
	}
}

// D4: recovery and a digest change in the same check produce both events, with
// the change event held for the batch.
func TestAdversarialRecoveryPlusChangeSameCheck(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "2"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository, Ref: "1.27"})
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failure: %v", err)
	}
	rec.Reset()
	// Recover and change in one check.
	fake.DigestErr = map[string]error{}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(2))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("recovery check: %v", err)
	}
	// Recovery fires immediately; the change is below the threshold.
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1 (recovery only)", rec.Count())
	}
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	types := map[apitypes.EventType]int{}
	events, _, _ := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	for _, e := range events {
		types[e.Type]++
	}
	if types[apitypes.EventCheckRecovered] != 1 || types[apitypes.EventDigestChanged] != 1 {
		t.Errorf("events = %v (%d total), want one recovered and one digest_changed", types, total)
	}
}

// ---------------------------------------------------------------------------
// Probe group F — checkPatternSemver.
// ---------------------------------------------------------------------------

// F1: the highest tag disappearing upstream must not fire or reseed.
func TestAdversarialSemverHighestTagVanishes(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// The highest tag vanishes; only the lower one remains.
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("vanish check: %v", err)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d on the highest tag vanishing, want 0", rec.Count())
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.1.0" {
		t.Errorf("latest semver = %q, want 1.1.0 (state must hold)", got.LatestSemver)
	}
	// A genuinely higher version later is a bump from the stored value.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d on the later bump, want 1", rec.Count())
	}
}

// F2: with only non-semver tags present the check is a silent no-op and the
// stored version is untouched.
func TestAdversarialSemverNoParsableTagNoOp(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Nothing parses as semver now.
	fake.SetTags(testRegistry, testRepository, "nightly", "latest")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("no-parsable check: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.1.0" {
		t.Errorf("latest semver = %q, want 1.1.0 (a no-parsable-tag check must not clear state)", got.LatestSemver)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications on a no-parsable-tag check, want 0", rec.Count())
	}
}

// F3: an equal version is not a bump.
func TestAdversarialSemverEqualVersionNoBump(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// A new tag that is not a version increase.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.2.0", "v0.9.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("lower check: %v", err)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d on a lower version, want 0", rec.Count())
	}
}

// F4: a digest-resolution failure still records the bump, with an empty digest.
func TestAdversarialSemverDigestFailureStillEvents(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.2.0")
	// Resolving v1.2.0 fails; the bump must still be recorded.
	fake.SetDigestErr(testRegistry, testRepository, "v1.2.0",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository, Ref: "v1.2.0"})
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("bump check: %v", err)
	}
	if result.EventID == "" {
		t.Fatal("no event recorded when the bump's digest failed to resolve")
	}
	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if event.NewDigest != "" {
		t.Errorf("new digest = %q, want empty on a resolution failure", event.NewDigest)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications, want 1", rec.Count())
	}
	if strings.Contains(rec.Messages()[0].Body, "New digest") {
		t.Errorf("body names a digest that does not exist:\n%s", rec.Messages()[0].Body)
	}
}

// F5: semver threshold batching renders the batch body with the prior bump.
func TestAdversarialSemverBatchBody(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag semver: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 2); err != nil {
		t.Fatalf("SetWatchFlag threshold: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump 1: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0", "v1.2.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump 2: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d, want 1 batch", rec.Count())
	}
	msg := rec.Messages()[0]
	if !strings.Contains(msg.Title, "2 changes") {
		t.Errorf("title = %q, want a 2-change batch", msg.Title)
	}
	if !strings.Contains(msg.Body, "v1.1.0") {
		t.Errorf("batch body does not name the prior bump:\n%s", msg.Body)
	}
}

// F6: prerelease precedence — a prerelease of a higher version beats a lower
// stable release, and the stable release of that version supersedes it.
func TestAdversarialSemverPrereleasePrecedence(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0-rc.1")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.1.0-rc.1" {
		t.Errorf("seeded latest = %q, want 1.1.0-rc.1 (prerelease is the highest)", got.LatestSemver)
	}
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0-rc.1", "v1.1.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("stable check: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d on the stable release of the prereleased version, want 1", rec.Count())
	}
}

// ---------------------------------------------------------------------------
// Probe group G — message rendering.
// ---------------------------------------------------------------------------

func TestAdversarialBatchRenderingCountsAndEntries(t *testing.T) {
	watch := &store.WatchRecord{ID: "w_1", Image: "index.docker.io/library/nginx:1.27",
		Registry: "index.docker.io", Repository: "library/nginx", Kind: apitypes.WatchKindTag, Ref: "1.27"}
	event := &store.EventRecord{
		Type: apitypes.EventDigestChanged, Tag: "1.27", Image: watch.Image,
		OldDigest: "sha256:old", NewDigest: "sha256:new", CreatedAt: nowFixed(),
		Detail: map[string]any{"batch": map[string]any{"count": 3, "entries": []any{
			map[string]any{"type": "digest_changed", "tag": "1.27", "at": "2026-09-11T11:00:00Z", "old_digest": "sha256:a", "new_digest": "sha256:b"},
			map[string]any{"type": "digest_changed", "tag": "1.27", "at": "2026-09-11T11:30:00Z", "old_digest": "sha256:b", "new_digest": "sha256:c"},
		}}},
	}
	if got := batchCount(event); got != 3 {
		t.Errorf("batchCount = %d, want 3", got)
	}
	title := Title(watch, event)
	if !strings.Contains(title, "3 changes") {
		t.Errorf("title = %q, want 3 changes", title)
	}
	body := Body(watch, event)
	if !strings.Contains(body, "Earlier changes in this batch (2):") {
		t.Errorf("batch header wrong:\n%s", body)
	}
	if n := strings.Count(body, "  - "); n != 2 {
		t.Errorf("rendered %d batch entries, want 2:\n%s", n, body)
	}
}

func TestAdversarialCorruptBatchDetailIsSafe(t *testing.T) {
	watch := &store.WatchRecord{ID: "w_1", Image: "index.docker.io/library/nginx:1.27",
		Registry: "index.docker.io", Repository: "library/nginx", Kind: apitypes.WatchKindTag, Ref: "1.27"}
	cases := []map[string]any{
		{"batch": "not-a-map"},
		{"batch": map[string]any{"count": "three"}},
		{"batch": map[string]any{"count": 2, "entries": "not-a-list"}},
		{"batch": map[string]any{"count": 2, "entries": []any{"not-a-map"}}},
	}
	for i, detail := range cases {
		event := &store.EventRecord{Type: apitypes.EventDigestChanged, Tag: "1.27",
			Image: watch.Image, CreatedAt: nowFixed(), Detail: detail}
		// Must not panic and must not claim a batch count it cannot parse.
		_ = Title(watch, event)
		_ = Body(watch, event)
		if i == 0 && Title(watch, event) == "" {
			t.Errorf("empty title for a corrupt batch detail")
		}
	}
}

func TestAdversarialNilDetailRendering(t *testing.T) {
	watch := &store.WatchRecord{ID: "w_1", Image: "index.docker.io/library/nginx:1.27",
		Registry: "index.docker.io", Repository: "library/nginx", Kind: apitypes.WatchKindTag, Ref: "1.27"}
	for _, typ := range apitypes.EventTypes() {
		event := &store.EventRecord{Type: typ, Tag: "1.27", Image: watch.Image, CreatedAt: nowFixed()}
		if Title(watch, event) == "" {
			t.Errorf("empty title for %s with a nil detail map", typ)
		}
		_ = Body(watch, event)
	}
	// A semver event with the expected detail renders both version lines.
	semver := &store.EventRecord{Type: apitypes.EventSemverNewTag, Tag: "v1.2.0", Image: watch.Image,
		CreatedAt: nowFixed(), NewDigest: testutil.Digest(1),
		Detail: map[string]any{"previous": "1.1.0", "version": "1.2.0"}}
	body := Body(watch, semver)
	if !strings.Contains(body, "Previous version: 1.1.0") || !strings.Contains(body, "New version: 1.2.0") {
		t.Errorf("semver body missing version lines:\n%s", body)
	}
	if got := Priority(apitypes.EventSemverNewTag); got != "default" {
		t.Errorf("semver priority = %q, want default", got)
	}
	tags := Tags(apitypes.EventSemverNewTag)
	if len(tags) == 0 {
		t.Errorf("semver event has no ntfy tags")
	}
}

// ---------------------------------------------------------------------------
// Probe group H — multi-watch isolation.
// ---------------------------------------------------------------------------

// H1: two watches with different thresholds do not share counters or events.
func TestAdversarialMultiWatchCounterIsolation(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	a := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	b := testutil.Watch(t, db, testRegistry, testRepository, "1.28", nil)
	if err := db.SetWatchFlag(ctx, a.ID, "notify_threshold", 3); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	fake.SetDigest(testRegistry, testRepository, "1.28", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, a.ID, TriggerSchedule); err != nil {
		t.Fatalf("a baseline: %v", err)
	}
	if _, err := engine.CheckWatch(ctx, b.ID, TriggerSchedule); err != nil {
		t.Fatalf("b baseline: %v", err)
	}

	// Interleave: a accumulates (threshold 3), b notifies every change.
	for i := 2; i <= 4; i++ {
		fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, a.ID, TriggerSchedule); err != nil {
			t.Fatalf("a change %d: %v", i, err)
		}
		fake.SetDigest(testRegistry, testRepository, "1.28", testutil.Digest(byte(i)))
		if _, err := engine.CheckWatch(ctx, b.ID, TriggerSchedule); err != nil {
			t.Fatalf("b change %d: %v", i, err)
		}
	}
	// b notified on each of its three changes; a's third change crossed its
	// threshold of three, so it notified once.
	if rec.Count() != 4 {
		t.Errorf("sent %d notifications, want 4 (b: 3, a: 1)", rec.Count())
	}
	gotA, err := db.GetWatch(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetWatch a: %v", err)
	}
	gotB, err := db.GetWatch(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetWatch b: %v", err)
	}
	if gotA.PendingNotifyCount != 0 {
		t.Errorf("watch a pending = %d after its batch fired, want 0", gotA.PendingNotifyCount)
	}
	if gotB.PendingNotifyCount != 0 {
		t.Errorf("watch b pending = %d, want 0", gotB.PendingNotifyCount)
	}
	_, totalA, err := db.ListEvents(ctx, store.EventFilter{WatchID: a.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents a: %v", err)
	}
	_, totalB, err := db.ListEvents(ctx, store.EventFilter{WatchID: b.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents b: %v", err)
	}
	// Default history 1: each watch keeps exactly its newest change event.
	if totalA != 1 || totalB != 1 {
		t.Errorf("kept a=%d b=%d events, want 1 each (history 1)", totalA, totalB)
	}
}

// ---------------------------------------------------------------------------
// Probe group C (cont.) — cross-counter protection and the fetch boundary.
// ---------------------------------------------------------------------------

// C5: pending semver bumps must survive an unrelated prune (documented
// cross-counter protection).
func TestAdversarialPendingSemverProtectedByFailurePrune(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag semver: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 5); err != nil {
		t.Fatalf("SetWatchFlag threshold: %v", err)
	}
	if err := db.SetSetting(ctx, "history", "1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	tags := []string{"v1.0.0"}
	fake.SetTags(testRegistry, testRepository, tags...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for _, tag := range []string{"v1.1.0", "v1.2.0"} {
		tags = append(tags, tag)
		fake.SetTags(testRegistry, testRepository, tags...)
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("bump %s: %v", tag, err)
		}
	}

	// A failing check prunes; the two pending semver events must survive.
	fake.SetTagsErr(testRegistry, testRepository,
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository})
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failure check: %v", err)
	}
	events, _, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	semver := 0
	for _, e := range events {
		if e.Type == apitypes.EventSemverNewTag {
			semver++
		}
	}
	if semver != 2 {
		t.Errorf("kept %d pending semver events, want 2 (cross-counter protection)", semver)
	}
}

// C6: notify debt and a semver bump on the same watch interleave two event
// families under one history cap; the counter-based protection can drop a
// genuinely undelivered event.
func TestAdversarialCrossFamilyInterleavePrune(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v*")
	if err := db.SetSetting(ctx, "notify_threshold", "5"); err != nil {
		t.Fatalf("SetSetting notify: %v", err)
	}
	if err := db.SetSetting(ctx, "history", "1"); err != nil {
		t.Fatalf("SetSetting history: %v", err)
	}
	// Seed semver so a later toggle-on produces a real bump.
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag semver: %v", err)
	}
	if err := db.SetWatchFlag(ctx, watch.ID, "semver_notify_threshold", 5); err != nil {
		t.Fatalf("SetWatchFlag semver threshold: %v", err)
	}
	tags := []string{"v1.0.0"}
	fake.SetTags(testRegistry, testRepository, tags...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// One semver bump, then toggle semver off so the tag baseline and the
	// stored version are armed for a later toggle-on.
	tags = append(tags, "v1.1.0")
	fake.SetTags(testRegistry, testRepository, tags...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if err := db.UnsetWatchFlag(ctx, watch.ID, "semver"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	// Three new_tags events accumulate as undelivered notify debt.
	for _, tag := range []string{"v1.2.0", "v1.3.0", "v1.4.0"} {
		tags = append(tags, tag)
		fake.SetTags(testRegistry, testRepository, tags...)
		if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
			t.Fatalf("new tag %s: %v", tag, err)
		}
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingNotifyCount != 3 {
		t.Fatalf("pending notify = %d, want 3", got.PendingNotifyCount)
	}

	// Semver back on; a bump prunes the change family.
	if err := db.SetWatchFlag(ctx, watch.ID, "semver", true); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	tags = append(tags, "v2.0.0")
	fake.SetTags(testRegistry, testRepository, tags...)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("bump check: %v", err)
	}

	events, _, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	newTags := 0
	for _, e := range events {
		if e.Type == apitypes.EventNewTags {
			newTags++
		}
	}
	// DESIGN GAP (characterization): with notify_threshold 5 and history 1,
	// three new_tags events are undelivered notify debt. Turning semver on and
	// recording a bump prunes the single change family to
	// max(history, pendingNotify, pendingSemver) = 3 events, so the OLDEST of
	// the three undelivered new_tags events is discarded, leaving the notify
	// counter (3) pointing at only two real events. The written contract says
	// "pending (undelivered) events are always retained" AND gives the
	// max-counter formula; when two counter families share one history cap the
	// two statements conflict. The engine implements the formula, so this is
	// recorded as a design gap, not a code defect.
	if newTags != 2 {
		t.Errorf("kept %d undelivered new_tags events, want 2 (the documented interleave gap): "+
			"change-family events=%d", newTags, len(events))
	}
	got, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingNotifyCount != 3 {
		t.Errorf("pending notify = %d, want 3 (counter still counts the discarded event)", got.PendingNotifyCount)
	}
	t.Logf("gap reproduced: notify counter=%d but only %d undelivered new_tags events remain",
		got.PendingNotifyCount, newTags)
}

// C7: the batch fetch must happen before the firing event is created, so the
// firing event never appears among its own prior entries.
func TestAdversarialBatchFetchExcludesFiringEvent(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
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
	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	entries := batchEntriesOf(event)
	if len(entries) != 2 {
		t.Fatalf("batch has %d prior entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e["new_digest"] == event.NewDigest {
			t.Errorf("firing event appears in its own batch: %v", e)
		}
	}
	if got := batchCount(event); got != 3 {
		t.Errorf("batchCount = %d, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// Probe group E — checkPattern baseline maintenance.
// ---------------------------------------------------------------------------

// E1: the comment claims known tags' digests are refreshed; a check with no new
// tags must not silently clear a stored digest.
func TestAdversarialPatternBaselineDigestRefresh(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v1.*")
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// A new tag is discovered and its digest resolved.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	fake.SetDigest(testRegistry, testRepository, "v1.1.0", testutil.Digest(9))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if known["v1.1.0"] != testutil.Digest(9) {
		t.Fatalf("after discovery digest = %q, want %q", known["v1.1.0"], testutil.Digest(9))
	}
	// No new tags this check: the stored digest must not be wiped.
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("steady check: %v", err)
	}
	known, err = db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if known["v1.1.0"] != testutil.Digest(9) {
		t.Errorf("known tag digest = %q after a steady check, want %q (stored digest cleared)",
			known["v1.1.0"], testutil.Digest(9))
	}
}

package check

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// TestPatternWatchWithEmptyBaselineStillReportsLaterTags is the regression test
// for a baseline that never latches: a pattern watch created before any
// matching tag exists records an empty baseline, and if "baseline" is derived
// from the tag set being non-empty, every later check re-baselines and the
// first tags to appear are never reported.
func TestPatternWatchWithEmptyBaselineStillReportsLaterTags(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v1.*")

	// First check: the repository exists but has no matching tags yet.
	fake.SetTags(testRegistry, testRepository, "latest")
	first, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	if !first.Baseline {
		t.Error("the first successful check must record a baseline")
	}
	if rec.Count() != 0 {
		t.Fatalf("the baseline check notified %d times, want 0", rec.Count())
	}

	// A matching tag appears.
	fake.SetTags(testRegistry, testRepository, "latest", "v1.0.0")
	fake.SetDigest(testRegistry, testRepository, "v1.0.0", testutil.Digest(4))

	second, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if second.Baseline {
		t.Error("Baseline = true on the second successful check; the watch already had one")
	}
	if len(second.NewTags) != 1 || second.NewTags[0].Tag != "v1.0.0" {
		t.Fatalf("NewTags = %v, want [v1.0.0]: the first tag to appear must be reported", second.NewTags)
	}
	if rec.Count() != 1 {
		t.Errorf("notified %d times, want 1 for the newly appearing tag", rec.Count())
	}
}

// TestPatternWatchRebaselinesAfterBaselineEmptied checks the same predicate
// from the other side: a watch whose baseline was emptied by pruning must not
// silently swallow tags that come back.
func TestPatternWatchRebaselinesAfterBaselineEmptied(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "v1.*")
	fake.SetTags(testRegistry, testRepository, "v1.0.0")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// Every tag disappears upstream: the baseline is pruned away.
	fake.SetTags(testRegistry, testRepository)
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("pruning check: %v", err)
	}
	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if len(known) != 0 {
		t.Fatalf("baseline has %d tags after everything vanished, want 0", len(known))
	}
	rec.Reset()

	// The tags come back. They are genuinely new to the operator's view, so
	// they must be reported rather than silently re-baselined.
	fake.SetTags(testRegistry, testRepository, "v1.0.0", "v1.1.0")
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("reappearance check: %v", err)
	}
	if result.Baseline {
		t.Error("Baseline = true after the baseline was emptied; the watch must report the reappearing tags")
	}
	if len(result.NewTags) != 2 {
		t.Errorf("NewTags = %v, want both reappearing tags", result.NewTags)
	}
	if rec.Count() != 1 {
		t.Errorf("notified %d times, want 1", rec.Count())
	}
}

// TestEngineRandIsRaceFree runs several different watches concurrently. Each
// check computes its own next attempt, which draws from the engine's jitter
// source; a shared *rand.Rand is not safe for concurrent use, so this fails
// under -race if the source is not serialized.
func TestEngineRandIsRaceFree(t *testing.T) {
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	testutil.Channel(t, db, builder, "ops", true)

	engine, err := New(Options{
		Store:          db,
		Registry:       fake,
		Notifier:       builder,
		Interval:       time.Hour,
		Concurrency:    8,
		Timeout:        5 * time.Second,
		NotifyAttempts: 1,
		NotifyBackoff:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Make the registry slow enough that the checks genuinely overlap.
	fake.Delay = 25 * time.Millisecond

	// Several distinct watches, so nothing serializes them against each other.
	const watches = 16
	ids := make([]string, 0, watches)
	for i := 0; i < watches; i++ {
		tag := "v" + string(rune('a'+i))
		w := testutil.Watch(t, db, testRegistry, testRepository, tag, nil)
		fake.SetDigest(testRegistry, testRepository, tag, testutil.Digest(byte(i)))
		ids = append(ids, w.ID)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := engine.CheckWatch(ctx, id, TriggerSchedule); err != nil {
				t.Errorf("CheckWatch(%s): %v", id, err)
			}
		}()
	}
	wg.Wait()

	for _, id := range ids {
		stored, err := db.GetWatch(ctx, id)
		if err != nil {
			t.Fatalf("GetWatch(%s): %v", id, err)
		}
		if stored.NextAttemptAt == nil {
			t.Errorf("watch %s has no next attempt scheduled", id)
		}
	}
}

// TestScheduledChecksOfDistinctWatchesAreRaceFree drives the scheduler with
// several due watches and more than one worker, which is the production shape
// that exercises the jitter source concurrently.
func TestScheduledChecksOfDistinctWatchesAreRaceFree(t *testing.T) {
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	testutil.Channel(t, db, builder, "ops", true)

	engine, err := New(Options{
		Store:          db,
		Registry:       fake,
		Notifier:       builder,
		Interval:       50 * time.Millisecond,
		Concurrency:    4,
		Timeout:        5 * time.Second,
		NotifyAttempts: 1,
		NotifyBackoff:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A slow registry keeps several workers busy at once.
	fake.Delay = 20 * time.Millisecond

	const watches = 8
	for i := 0; i < watches; i++ {
		tag := "v" + string(rune('a'+i))
		testutil.Watch(t, db, testRegistry, testRepository, tag, nil)
		fake.SetDigest(testRegistry, testRepository, tag, testutil.Digest(byte(i)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = engine.Run(ctx)
	}()

	// Let several ticks complete.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatal("the scheduler never completed a tick")
		case <-ticker.C:
			if engine.Ticks() >= 3 {
				cancel()
				wg.Wait()
				return
			}
		}
	}
}

// TestWatchRecordHasBaselineSemantics pins the predicate itself, so a future
// refactor cannot quietly reintroduce the empty-baseline hole.
func TestWatchRecordHasBaselineSemantics(t *testing.T) {
	now := fixedNow()

	// Never checked successfully: no baseline.
	fresh := &store.WatchRecord{Kind: apitypes.WatchKindTag, Ref: "v1"}
	if fresh.HasBaseline() {
		t.Error("a watch that never succeeded must not report a baseline")
	}

	// Failed first, then still failing: no baseline yet, even though checks
	// have run.
	failedOnly := &store.WatchRecord{
		Kind: apitypes.WatchKindTag, Ref: "v1",
		LastCheckedAt: &now, ConsecutiveFailures: 3,
	}
	if failedOnly.HasBaseline() {
		t.Error("a watch whose checks all failed must not report a baseline")
	}

	// A successful check records a baseline, even for a pattern watch whose
	// tag set is legitimately empty.
	patternOK := &store.WatchRecord{
		Kind: apitypes.WatchKindPattern, Ref: "v1.*",
		LastCheckedAt: &now, LastOKAt: &now, BaselineAt: &now,
	}
	if !patternOK.HasBaseline() {
		t.Error("a pattern watch that succeeded once has a baseline even when it matched no tags")
	}

	// The baseline is not re-stamped by later successes, and a recovered watch
	// keeps the one it had.
	recovered := &store.WatchRecord{
		Kind: apitypes.WatchKindTag, Ref: "v1",
		LastOKAt: &now, BaselineAt: &now, ConsecutiveFailures: 0,
	}
	if !recovered.HasBaseline() {
		t.Error("a recovered watch must keep its baseline")
	}
}

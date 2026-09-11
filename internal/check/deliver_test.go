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

func TestDeliverRecordsEveryAttempt(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	first, firstRec := testutil.Channel(t, db, builder, "first", true)
	second, secondRec := testutil.Channel(t, db, builder, "second", true)
	engine := testEngine(t, db, registry.NewFake(), builder)

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "1.27",
		OldDigest: "sha256:old", NewDigest: "sha256:new",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	sent := engine.deliver(context.Background(), watch, event)
	if sent != 2 {
		t.Errorf("deliver returned %d, want 2", sent)
	}
	if firstRec.Count() != 1 || secondRec.Count() != 1 {
		t.Errorf("each default channel must receive one message: %d and %d",
			firstRec.Count(), secondRec.Count())
	}

	records, total, err := db.ListNotifications(context.Background(), store.NotificationFilter{EventID: event.ID})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if total != 2 || len(records) != 2 {
		t.Fatalf("recorded %d notifications, want 2", total)
	}
	seen := map[string]bool{}
	for _, rec := range records {
		if rec.Status != apitypes.NotificationSent {
			t.Errorf("notification status = %q, want sent", rec.Status)
		}
		if rec.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", rec.Attempts)
		}
		if rec.SentAt == nil {
			t.Error("SentAt = nil on a sent notification")
		}
		seen[rec.ChannelID] = true
	}
	if !seen[first.ID] || !seen[second.ID] {
		t.Errorf("notification records cover %v, want both %s and %s", seen, first.ID, second.ID)
	}
}

func TestDeliverRetriesThenRecordsFailure(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	channel, rec := testutil.Channel(t, db, builder, "broken", true)
	rec.FailFor = func(notify.Message) bool { return true }
	engine := testEngine(t, db, registry.NewFake(), builder)

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "1.27",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	sent := engine.deliver(context.Background(), watch, event)
	if sent != 0 {
		t.Errorf("deliver returned %d for a failing channel, want 0", sent)
	}
	if rec.Count() != 0 {
		t.Errorf("the failing channel recorded %d successful messages, want 0", rec.Count())
	}

	records, _, err := db.ListNotifications(context.Background(), store.NotificationFilter{EventID: event.ID})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d notifications, want 1", len(records))
	}
	if records[0].ChannelID != channel.ID {
		t.Errorf("notification channel = %s, want %s", records[0].ChannelID, channel.ID)
	}
	if records[0].Status != apitypes.NotificationFailed {
		t.Errorf("status = %q, want failed", records[0].Status)
	}
	// NotifyAttempts is 2 in the test engine.
	if records[0].Attempts != 2 {
		t.Errorf("attempts = %d, want 2", records[0].Attempts)
	}
	if records[0].LastError == "" {
		t.Error("LastError is empty on a failed notification")
	}
	if records[0].SentAt != nil {
		t.Error("SentAt is set on a failed notification")
	}
}

func TestDeliverWithNoChannelsIsSilent(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	engine := testEngine(t, db, registry.NewFake(), builder)

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "1.27",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if sent := engine.deliver(context.Background(), watch, event); sent != 0 {
		t.Errorf("deliver returned %d with no channels, want 0", sent)
	}
	_, total, err := db.ListNotifications(context.Background(), store.NotificationFilter{EventID: event.ID})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if total != 0 {
		t.Errorf("recorded %d notifications with no channels, want 0", total)
	}
}

func TestDeliverRecordsChannelBuildFailure(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	channel, _ := testutil.Channel(t, db, builder, "unbuildable", true)
	builder.Fail(channel.ID, notify.ErrConfig("ntfy", "topic"))
	engine := testEngine(t, db, registry.NewFake(), builder)

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "1.27",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if sent := engine.deliver(context.Background(), watch, event); sent != 0 {
		t.Errorf("deliver returned %d for an unbuildable channel, want 0", sent)
	}
	records, _, err := db.ListNotifications(context.Background(), store.NotificationFilter{EventID: event.ID})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d notifications, want 1", len(records))
	}
	if records[0].Status != apitypes.NotificationFailed {
		t.Errorf("status = %q, want failed", records[0].Status)
	}
	if records[0].Attempts != 0 {
		t.Errorf("attempts = %d, want 0 when the channel could not be built", records[0].Attempts)
	}
}

func TestRetryNotification(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	channel, rec := testutil.Channel(t, db, builder, "flaky", true)
	engine := testEngine(t, db, registry.NewFake(), builder)

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "1.27",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	record, err := db.CreateNotification(context.Background(), event.ID, channel.ID)
	if err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}
	if err := db.UpdateNotification(context.Background(), record.ID,
		apitypes.NotificationFailed, 2, "smtp timeout"); err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}

	if err := engine.RetryNotification(context.Background(), record.ID); err != nil {
		t.Fatalf("RetryNotification: %v", err)
	}
	if rec.Count() != 1 {
		t.Errorf("the retry did not deliver a message (%d sent)", rec.Count())
	}
	updated, err := db.GetNotification(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetNotification: %v", err)
	}
	if updated.Status != apitypes.NotificationSent {
		t.Errorf("status = %q after a successful retry, want sent", updated.Status)
	}
	if updated.LastError != "" {
		t.Errorf("LastError = %q after a successful retry, want empty", updated.LastError)
	}

	if err := engine.RetryNotification(context.Background(), "n_missing"); err == nil {
		t.Error("RetryNotification on an unknown ID: expected an error")
	}
}

func TestTestChannel(t *testing.T) {
	db := testutil.Store(t)
	builder := notify.NewFakeBuilder()
	channel, rec := testutil.Channel(t, db, builder, "ops", false)
	engine := testEngine(t, db, registry.NewFake(), builder)

	if err := engine.TestChannel(context.Background(), channel); err != nil {
		t.Fatalf("TestChannel: %v", err)
	}
	if rec.Count() != 1 {
		t.Fatalf("TestChannel sent %d messages, want 1", rec.Count())
	}
	msg := rec.Messages()[0]
	if msg.Title == "" || msg.Body == "" {
		t.Error("the test message has an empty title or body")
	}

	// A channel that cannot be built reports the error rather than panicking.
	broken, _ := testutil.Channel(t, db, builder, "broken", false)
	builder.Fail(broken.ID, notify.ErrConfig("email", "smtp_host"))
	if err := engine.TestChannel(context.Background(), broken); err == nil {
		t.Error("TestChannel on an unbuildable channel: expected an error")
	}
}

// TestRunChecksDueWatches exercises the scheduler loop end to end: it starts
// Run, waits for a tick, and asserts the due watch was actually checked.
func TestRunChecksDueWatches(t *testing.T) {
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	_, rec := testutil.Channel(t, db, builder, "ops", true)

	// A short interval keeps the test fast; the engine jitters by ±10%.
	engine, err := New(Options{
		Store:          db,
		Registry:       fake,
		Notifier:       builder,
		Interval:       60 * time.Millisecond,
		Concurrency:    2,
		Timeout:        5 * time.Second,
		NotifyAttempts: 1,
		NotifyBackoff:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = engine.Run(ctx)
	}()

	// Wait until the baseline has been recorded, with a hard deadline so a
	// broken scheduler fails the test rather than hanging it.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	checked := false
	for !checked {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatal("the scheduler never checked the due watch")
		case <-ticker.C:
			stored, err := db.GetWatch(ctx, watch.ID)
			if err != nil {
				t.Fatalf("GetWatch: %v", err)
			}
			if stored.LastCheckedAt != nil {
				checked = true
			}
		}
	}

	cancel()
	wg.Wait()

	if engine.Ticks() == 0 {
		t.Error("Ticks() = 0 after a completed tick")
	}
	if engine.LastTick() == nil {
		t.Error("LastTick() = nil after a completed tick")
	}
	// The baseline check is silent by design.
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications on the baseline check, want 0", rec.Count())
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	db := testutil.Store(t)
	engine := testEngine(t, db, registry.NewFake(), notify.NewFakeBuilder())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned nil, want the context error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	if engine.NextTick() != nil {
		t.Error("NextTick() is still set after Run returned")
	}
}

func TestNewValidatesOptions(t *testing.T) {
	db := testutil.Store(t)

	if _, err := New(Options{}); err == nil {
		t.Error("New without a store: expected an error")
	}
	if _, err := New(Options{Store: db}); err == nil {
		t.Error("New without a registry client: expected an error")
	}
	// Defaults are filled in rather than rejected.
	engine, err := New(Options{Store: db, Registry: registry.NewFake()})
	if err != nil {
		t.Fatalf("New with defaults: %v", err)
	}
	if engine.Interval() != 15*time.Minute {
		t.Errorf("default interval = %s, want 15m", engine.Interval())
	}
	if engine.Concurrency() != 4 {
		t.Errorf("default concurrency = %d, want 4", engine.Concurrency())
	}
}

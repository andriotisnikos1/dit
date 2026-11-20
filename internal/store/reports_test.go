package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

func createEvent(t *testing.T, db *store.DB, watchID string, kind apitypes.EventType) *store.EventRecord {
	t.Helper()
	// Fixed-width timestamps have nanosecond resolution, but consecutive
	// inserts can still land in the same nanosecond on a coarse clock; a small
	// gap makes "newest N" deterministic (ids are random, so a tie would
	// break the order arbitrarily).
	time.Sleep(2 * time.Millisecond)
	event, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID:   watchID,
		Type:      kind,
		Tag:       "1.27",
		OldDigest: testutil.Digest(1),
		NewDigest: testutil.Digest(2),
		Detail:    map[string]any{"probe": true},
	})
	if err != nil {
		t.Fatalf("CreateEvent(%s): %v", kind, err)
	}
	return event
}

func TestUnreportedChangeEventsPerKind(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	digest := createEvent(t, db, watch.ID, apitypes.EventDigestChanged)
	tags := createEvent(t, db, watch.ID, apitypes.EventNewTags)
	// Failure events are never part of a report.
	createEvent(t, db, watch.ID, apitypes.EventCheckFailed)

	got, err := db.UnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily, 10)
	if err != nil {
		t.Fatalf("UnreportedChangeEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("daily unreported = %d events, want 2 change events", len(got))
	}
	// Oldest first: a report reads chronologically.
	if got[0].ID != digest.ID || got[1].ID != tags.ID {
		t.Errorf("order = %s,%s; want %s,%s (oldest first)",
			got[0].ID, got[1].ID, digest.ID, tags.ID)
	}
	for _, event := range got {
		if event.Image != watch.Image {
			t.Errorf("event image = %q, want %q", event.Image, watch.Image)
		}
	}

	if n, err := db.CountUnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily); err != nil || n != 2 {
		t.Errorf("CountUnreportedChangeEvents = (%d, %v), want (2, nil)", n, err)
	}

	if err := db.MarkEventsReported(ctx, []string{digest.ID}, apitypes.ReportDaily, time.Now()); err != nil {
		t.Fatalf("MarkEventsReported: %v", err)
	}
	got, err = db.UnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily, 10)
	if err != nil {
		t.Fatalf("UnreportedChangeEvents after mark: %v", err)
	}
	if len(got) != 1 || got[0].ID != tags.ID {
		t.Fatalf("after marking the digest event, daily unreported = %v; want only %s", reportIDsOf(got), tags.ID)
	}

	// Weekly markers are independent: nothing is reported weekly yet.
	weekly, err := db.UnreportedChangeEvents(ctx, watch.ID, apitypes.ReportWeekly, 10)
	if err != nil {
		t.Fatalf("weekly unreported: %v", err)
	}
	if len(weekly) != 2 {
		t.Errorf("weekly unreported = %d, want 2 (the daily mark must not leak)", len(weekly))
	}

	if _, err := db.UnreportedChangeEvents(ctx, watch.ID, "hourly", 10); err == nil {
		t.Error("an unknown report kind must be rejected, not silently matched")
	}
}

func TestUnreportedChangeEventsLimit(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	for i := 0; i < 3; i++ {
		createEvent(t, db, watch.ID, apitypes.EventDigestChanged)
	}
	got, err := db.UnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily, 2)
	if err != nil {
		t.Fatalf("UnreportedChangeEvents: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit = %d events, want 2", len(got))
	}
}

func TestPruneWatchEventsProtectsUnreported(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	first := createEvent(t, db, watch.ID, apitypes.EventDigestChanged)
	second := createEvent(t, db, watch.ID, apitypes.EventDigestChanged)
	third := createEvent(t, db, watch.ID, apitypes.EventDigestChanged)

	// history=1 would keep only the newest event, but a pending daily report
	// must keep all three.
	pruned, err := db.PruneWatchEvents(ctx, watch.ID, 1, store.ReportProtection{Daily: true})
	if err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned %d unreported events, want 0", pruned)
	}
	if n, _ := db.CountUnreportedChangeEvents(ctx, watch.ID, apitypes.ReportDaily); n != 3 {
		t.Fatalf("unreported after protected prune = %d, want 3", n)
	}

	// Once delivered they are ordinary history again.
	if err := db.MarkEventsReported(ctx, []string{first.ID, second.ID, third.ID}, apitypes.ReportDaily, time.Now()); err != nil {
		t.Fatalf("MarkEventsReported: %v", err)
	}
	pruned, err = db.PruneWatchEvents(ctx, watch.ID, 1, store.ReportProtection{Daily: true})
	if err != nil {
		t.Fatalf("PruneWatchEvents after marking: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned %d reported events, want 2", pruned)
	}
	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 || len(events) != 1 || events[0].ID != third.ID {
		t.Errorf("after prune kept %d events (%v), want only the newest %s", total, reportIDsOf(events), third.ID)
	}
}

func TestPruneWatchEventsWithoutProtectionTrimsUnreported(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	for i := 0; i < 3; i++ {
		createEvent(t, db, watch.ID, apitypes.EventDigestChanged)
	}
	pruned, err := db.PruneWatchEvents(ctx, watch.ID, 1, store.ReportProtection{})
	if err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned %d events, want 2 (no report protects them)", pruned)
	}
}

func TestReportStateRoundTrip(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()

	if _, err := db.GetReportState(ctx, apitypes.ReportDaily); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetReportState on a fresh store = %v, want ErrNotFound", err)
	}

	run := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	sent := run.Add(time.Second)
	state := &store.ReportState{
		Kind:        apitypes.ReportDaily,
		LastRunAt:   &run,
		LastSentAt:  &sent,
		LastStatus:  "sent",
		LastEvents:  12,
		LastWatches: 5,
	}
	if err := db.SaveReportState(ctx, state); err != nil {
		t.Fatalf("SaveReportState: %v", err)
	}
	got, err := db.GetReportState(ctx, apitypes.ReportDaily)
	if err != nil {
		t.Fatalf("GetReportState: %v", err)
	}
	if got.LastStatus != "sent" || got.LastEvents != 12 || got.LastWatches != 5 {
		t.Errorf("state = %+v, want status sent / 12 events / 5 watches", got)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(run) {
		t.Errorf("LastRunAt = %v, want %v", got.LastRunAt, run)
	}
	if got.LastSentAt == nil || !got.LastSentAt.Equal(sent) {
		t.Errorf("LastSentAt = %v, want %v", got.LastSentAt, sent)
	}

	// A later empty run must be able to clear the sent marker.
	state.LastRunAt = &sent
	state.LastSentAt = nil
	state.LastStatus = "empty"
	state.LastEvents = 0
	state.LastWatches = 0
	if err := db.SaveReportState(ctx, state); err != nil {
		t.Fatalf("SaveReportState (upsert): %v", err)
	}
	got, err = db.GetReportState(ctx, apitypes.ReportDaily)
	if err != nil {
		t.Fatalf("GetReportState after upsert: %v", err)
	}
	if got.LastStatus != "empty" || got.LastSentAt != nil {
		t.Errorf("upsert state = %+v, want status empty and no sent time", got)
	}

	if err := db.SaveReportState(ctx, &store.ReportState{Kind: "hourly"}); err == nil {
		t.Error("SaveReportState with an unknown kind must fail, not create an unreachable row")
	}
}

func TestAllEnabledWatches(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()

	enabled := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	disabled, err := db.CreateWatch(ctx, store.CreateWatchInput{
		Image:      testutil.DockerHub + "/library/redis:7",
		Registry:   testutil.DockerHub,
		Repository: "library/redis",
		Kind:       apitypes.WatchKindTag,
		Ref:        "7",
		Enabled:    false,
		Channels:   nil,
	})
	if err != nil {
		t.Fatalf("CreateWatch(disabled): %v", err)
	}

	watches, err := db.AllEnabledWatches(ctx)
	if err != nil {
		t.Fatalf("AllEnabledWatches: %v", err)
	}
	if len(watches) != 1 || watches[0].ID != enabled.ID {
		t.Errorf("AllEnabledWatches = %v, want only %s (not %s)", reportWatchIDs(watches), enabled.ID, disabled.ID)
	}
}

func reportIDsOf(events []*store.EventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func reportWatchIDs(watches []*store.WatchRecord) []string {
	out := make([]string, 0, len(watches))
	for _, w := range watches {
		out = append(out, w.ID)
	}
	return out
}

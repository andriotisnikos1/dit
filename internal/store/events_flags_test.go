package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// eventAt creates one event with a distinct timestamp. The 2ms sleep is not
// decorative: created_at has nanosecond precision but event IDs are random,
// so two events in the same nanosecond would tie-break on a random ID and
// make "newest N" nondeterministic.
func eventAt(t *testing.T, db *store.DB, watchID string, typ apitypes.EventType, newDigest string) {
	t.Helper()
	_, err := db.CreateEvent(context.Background(), store.CreateEventInput{
		WatchID: watchID, Type: typ, Tag: "1.27", NewDigest: newDigest,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
}

func TestRecentEvents(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "one")
	eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "two")
	eventAt(t, db, watch.ID, apitypes.EventCheckFailed, "three")

	events, err := db.RecentEvents(ctx, watch.ID, []apitypes.EventType{apitypes.EventDigestChanged}, 2)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	// Newest first.
	if events[0].NewDigest != "two" || events[1].NewDigest != "one" {
		t.Errorf("order = [%s, %s], want [two one]", events[0].NewDigest, events[1].NewDigest)
	}

	events, err = db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("change events = %d, want 2 (failure event excluded)", len(events))
	}
	if events, err = db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), 0); err != nil || len(events) != 0 {
		t.Errorf("limit 0 = (%d, %v), want (0, nil)", len(events), err)
	}
}

func TestPruneWatchEventsKeepsNewestPerFamily(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	for _, d := range []string{"c1", "c2", "c3", "c4"} {
		eventAt(t, db, watch.ID, apitypes.EventDigestChanged, d)
	}
	for _, d := range []string{"f1", "f2"} {
		eventAt(t, db, watch.ID, apitypes.EventCheckFailed, d)
	}

	pruned, err := db.PruneWatchEvents(ctx, watch.ID, 2)
	if err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned %d events, want 2 (c1, c2)", pruned)
	}

	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 4 {
		t.Fatalf("kept %d events, want 4 (2 change + 2 failure)", total)
	}
	digests := map[string]bool{}
	for _, e := range events {
		digests[e.NewDigest] = true
	}
	for _, want := range []string{"c3", "c4", "f1", "f2"} {
		if !digests[want] {
			t.Errorf("expected event %s to survive pruning; kept: %v", want, digests)
		}
	}

	// keep <= 0 wipes the watch's history entirely.
	if _, err := db.PruneWatchEvents(ctx, watch.ID, 0); err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	if _, total, err = db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100}); err != nil || total != 0 {
		t.Errorf("after wipe: (%d, %v), want 0 events", total, err)
	}
}

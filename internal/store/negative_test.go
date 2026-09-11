package store

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// TestPruneWatchTagsRemovesVanishedTagsFromTheReturnValue guards the count that
// the engine reports as "pruned". A prune that reports the wrong number would
// silently mislead the operator's logs.
func TestPruneWatchTagsRemovesVanishedTagsFromTheReturnValue(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v*", nil)
	seed := []apitypes.TagDigest{
		{Tag: "v1", Digest: "a"}, {Tag: "v2", Digest: "b"}, {Tag: "v3", Digest: "c"},
	}
	if err := db.UpsertWatchTags(ctx, watch.ID, seed); err != nil {
		t.Fatalf("UpsertWatchTags: %v", err)
	}

	pruned, err := db.PruneWatchTags(ctx, watch.ID, []string{"v1"})
	if err != nil {
		t.Fatalf("PruneWatchTags: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned = %d, want 2", pruned)
	}
	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if len(known) != 1 {
		t.Errorf("known tags = %v, want only v1", known)
	}

	// Pruning nothing is a no-op that reports zero.
	pruned, err = db.PruneWatchTags(ctx, watch.ID, []string{"v1"})
	if err != nil {
		t.Fatalf("PruneWatchTags: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d on a no-op, want 0", pruned)
	}

	// Pruning a watch with no baseline rows at all is also a clean no-op.
	empty := mustCreateWatch(t, db, "ghcr.io", "owner/other", "v*", nil)
	pruned, err = db.PruneWatchTags(ctx, empty.ID, []string{"v9"})
	if err != nil {
		t.Fatalf("PruneWatchTags on an empty baseline: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d on an empty baseline, want 0", pruned)
	}
}

// TestPruneWatchTagsDoesNotDisturbOtherWatches guards against a delete that
// forgets the watch_id predicate.
func TestPruneWatchTagsDoesNotDisturbOtherWatches(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	first := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v*", nil)
	second := mustCreateWatch(t, db, "ghcr.io", "owner/other", "v*", nil)
	if err := db.UpsertWatchTags(ctx, first.ID, []apitypes.TagDigest{{Tag: "v1"}}); err != nil {
		t.Fatalf("UpsertWatchTags: %v", err)
	}
	if err := db.UpsertWatchTags(ctx, second.ID, []apitypes.TagDigest{{Tag: "v1"}}); err != nil {
		t.Fatalf("UpsertWatchTags: %v", err)
	}

	// Prune everything from the first watch.
	if _, err := db.PruneWatchTags(ctx, first.ID, nil); err != nil {
		t.Fatalf("PruneWatchTags: %v", err)
	}
	known, err := db.KnownTags(ctx, second.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if len(known) != 1 {
		t.Errorf("pruning one watch removed tags from another: %v", known)
	}
}

// TestSetWatchChannelsIsReplaceNotAppend guards the subscription update: a
// second call must replace, not accumulate, or a watch would keep notifying
// channels the operator removed.
func TestSetWatchChannelsIsReplaceNotAppend(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	one := mustCreateChannel(t, db, "one")
	two := mustCreateChannel(t, db, "two")
	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)

	if err := db.SetWatchChannels(ctx, watch.ID, []string{one.ID, two.ID}); err != nil {
		t.Fatalf("SetWatchChannels: %v", err)
	}
	ids, err := db.WatchChannelIDs(ctx, watch.ID)
	if err != nil {
		t.Fatalf("WatchChannelIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("subscriptions = %v, want both channels", ids)
	}

	if err := db.SetWatchChannels(ctx, watch.ID, []string{two.ID}); err != nil {
		t.Fatalf("SetWatchChannels: %v", err)
	}
	ids, err = db.WatchChannelIDs(ctx, watch.ID)
	if err != nil {
		t.Fatalf("WatchChannelIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != two.ID {
		t.Errorf("subscriptions = %v, want only the second channel", ids)
	}

	// Clearing falls back to the defaults.
	if err := db.SetWatchChannels(ctx, watch.ID, nil); err != nil {
		t.Fatalf("SetWatchChannels: %v", err)
	}
	ids, err = db.WatchChannelIDs(ctx, watch.ID)
	if err != nil {
		t.Fatalf("WatchChannelIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("subscriptions = %v, want none", ids)
	}
}

// TestUpdateWatchDoesNotDropSubscriptions guards the partial update: changing
// an unrelated field must leave the channel subscriptions alone.
func TestUpdateWatchDoesNotDropSubscriptions(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	channel := mustCreateChannel(t, db, "ops")
	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", []string{channel.ID})

	notify := false
	updated, err := db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{NotifyOnFailure: &notify})
	if err != nil {
		t.Fatalf("UpdateWatch: %v", err)
	}
	if len(updated.ChannelIDs) != 1 || updated.ChannelIDs[0] != channel.ID {
		t.Errorf("ChannelIDs = %v after an unrelated patch, want [%s]", updated.ChannelIDs, channel.ID)
	}
}

// TestChannelsForWatchSkipsAMissingChannel guards a dangling subscription: a
// subscription whose channel was deleted must not fail the whole delivery.
func TestChannelsForWatchSkipsAMissingChannel(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	keep := mustCreateChannel(t, db, "keep")
	drop := mustCreateChannel(t, db, "drop")
	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", []string{keep.ID, drop.ID})

	// Deleting a channel cascades its subscriptions away, so simulate the
	// dangling case by removing the row behind the store's back.
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, drop.ID); err != nil {
		t.Fatalf("delete channel row: %v", err)
	}

	resolved, err := db.ChannelsForWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("ChannelsForWatch: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != keep.ID {
		t.Errorf("ChannelsForWatch = %v, want only the surviving channel", resolved)
	}
}

// TestCreateWatchRejectsAnInvalidKind guards the CHECK constraint path.
func TestCreateWatchRejectsAnInvalidKind(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.CreateWatch(ctx, CreateWatchInput{
		Image: "ghcr.io/owner/app", Registry: "ghcr.io", Repository: "owner/app",
		Kind: "bogus", Ref: "v1", Enabled: true,
	}); err == nil {
		t.Error("CreateWatch with an invalid kind: expected an error")
	}
}

// TestEventDetailRoundTripsThroughJSON guards the detail map the notification
// body is rendered from.
func TestEventDetailRoundTripsThroughJSON(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	event, err := db.CreateEvent(ctx, CreateEventInput{
		WatchID: watch.ID,
		Type:    apitypes.EventNewTags,
		Tag:     "v1.*",
		Detail: map[string]any{
			"pattern": "v1.*",
			"count":   2,
			"new_tags": []apitypes.TagDigest{
				{Tag: "v1.1.0", Digest: "sha256:aaa"},
				{Tag: "v1.2.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	loaded, err := db.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if loaded.Detail["pattern"] != "v1.*" {
		t.Errorf("pattern = %v, want v1.*", loaded.Detail["pattern"])
	}
	tags, ok := loaded.Detail["new_tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("new_tags = %#v, want two entries", loaded.Detail["new_tags"])
	}
	first, ok := tags[0].(map[string]any)
	if !ok {
		t.Fatalf("new_tags[0] = %#v, want an object", tags[0])
	}
	if first["tag"] != "v1.1.0" || first["digest"] != "sha256:aaa" {
		t.Errorf("new_tags[0] = %v, want the tag and digest preserved", first)
	}
}

// TestEventWithNoDetailIsEmpty guards the nil-detail path, which the message
// renderer and the JSON encoder both have to tolerate.
func TestEventWithNoDetailIsEmpty(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	event, err := db.CreateEvent(ctx, CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	loaded, err := db.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if len(loaded.Detail) != 0 {
		t.Errorf("Detail = %v, want empty", loaded.Detail)
	}
}

// TestCreateEventRejectsAnEmptyType guards against an untyped event row.
func TestCreateEventRejectsAnEmptyType(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	if _, err := db.CreateEvent(ctx, CreateEventInput{WatchID: watch.ID}); err == nil {
		t.Error("CreateEvent with no type: expected an error")
	}
}

// TestListEventsPaginates guards that the total describes the whole filtered
// set rather than the page, which the CLI relies on.
func TestListEventsPaginates(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	for i := 0; i < 5; i++ {
		if _, err := db.CreateEvent(ctx, CreateEventInput{
			WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "v1",
		}); err != nil {
			t.Fatalf("CreateEvent: %v", err)
		}
	}

	page, total, err := db.ListEvents(ctx, EventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("page size = %d, want 2", len(page))
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}

	// Newest first: the ordering the CLI presents.
	all, _, err := db.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("events are not in newest-first order at index %d", i)
		}
	}
}

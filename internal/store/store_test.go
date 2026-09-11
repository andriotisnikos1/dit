package store

import (
	"context"
	"errors"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	// Migrating again must be a no-op, not an error.
	if err := db.Migrate(ctx, migrationsFS()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	applied, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if _, ok := applied["0001_init"]; !ok {
		t.Errorf("0001_init not recorded as applied; got %v", applied)
	}
	if len(applied) != 1 {
		t.Errorf("applied migrations = %d, want 1", len(applied))
	}

	// The schema must be usable after the re-run.
	if _, err := db.Stats(ctx); err != nil {
		t.Fatalf("Stats after re-migrate: %v", err)
	}
}

func TestCreateWatchAndUniqueness(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch, err := db.CreateWatch(ctx, CreateWatchInput{
		Image:           "index.docker.io/library/nginx:1.27",
		Registry:        "index.docker.io",
		Repository:      "library/nginx",
		Kind:            apitypes.WatchKindTag,
		Ref:             "1.27",
		Enabled:         true,
		NotifyOnFailure: true,
	})
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}
	if watch.ID == "" || watch.ID[0:2] != "w_" {
		t.Errorf("watch ID = %q, want a w_ prefixed ID", watch.ID)
	}
	if !watch.Enabled {
		t.Error("new watch Enabled = false, want true")
	}
	if watch.NextAttemptAt == nil {
		t.Error("NextAttemptAt = nil, want the creation time so the next tick picks it up")
	}

	// The natural key is unique.
	_, err = db.CreateWatch(ctx, CreateWatchInput{
		Image:      "index.docker.io/library/nginx:1.27",
		Registry:   "index.docker.io",
		Repository: "library/nginx",
		Kind:       apitypes.WatchKindTag,
		Ref:        "1.27",
		Enabled:    true,
	})
	if !IsDuplicate(err) {
		t.Fatalf("duplicate CreateWatch: got %v, want ErrDuplicate", err)
	}

	// A different ref is a different watch.
	other, err := db.CreateWatch(ctx, CreateWatchInput{
		Image:      "index.docker.io/library/nginx:1.28",
		Registry:   "index.docker.io",
		Repository: "library/nginx",
		Kind:       apitypes.WatchKindTag,
		Ref:        "1.28",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateWatch for a second tag: %v", err)
	}
	if other.ID == watch.ID {
		t.Error("two different refs produced the same watch ID")
	}

	// The same ref on a different registry is also a different watch.
	if _, err := db.CreateWatch(ctx, CreateWatchInput{
		Image:      "ghcr.io/library/nginx:1.27",
		Registry:   "ghcr.io",
		Repository: "library/nginx",
		Kind:       apitypes.WatchKindTag,
		Ref:        "1.27",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("CreateWatch on another registry: %v", err)
	}
}

func TestFindWatch(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	created, err := db.CreateWatch(ctx, CreateWatchInput{
		Image: "ghcr.io/owner/app:v1", Registry: "ghcr.io", Repository: "owner/app",
		Kind: apitypes.WatchKindTag, Ref: "v1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}

	found, err := db.FindWatch(ctx, "ghcr.io", "owner/app", apitypes.WatchKindTag, "v1")
	if err != nil {
		t.Fatalf("FindWatch: %v", err)
	}
	if found.ID != created.ID {
		t.Errorf("FindWatch returned %s, want %s", found.ID, created.ID)
	}

	if _, err := db.FindWatch(ctx, "ghcr.io", "owner/app", apitypes.WatchKindPattern, "v1"); !IsNotFound(err) {
		t.Errorf("FindWatch with the wrong kind: got %v, want ErrNotFound", err)
	}
	if _, err := db.FindWatch(ctx, "ghcr.io", "owner/app", apitypes.WatchKindTag, "v2"); !IsNotFound(err) {
		t.Errorf("FindWatch for an unknown ref: got %v, want ErrNotFound", err)
	}
}

func TestUpdateWatchAndChannelSubscriptions(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	channel, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{apitypes.ConfigTopic: "t"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)

	// Subscribe.
	ids := []string{channel.ID}
	updated, err := db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{Channels: &ids})
	if err != nil {
		t.Fatalf("UpdateWatch(channels): %v", err)
	}
	if len(updated.ChannelIDs) != 1 || updated.ChannelIDs[0] != channel.ID {
		t.Errorf("ChannelIDs = %v, want [%s]", updated.ChannelIDs, channel.ID)
	}

	// Disable and change notify_on_failure in one patch.
	enabled, notify := false, false
	updated, err = db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{Enabled: &enabled, NotifyOnFailure: &notify})
	if err != nil {
		t.Fatalf("UpdateWatch(flags): %v", err)
	}
	if updated.Enabled {
		t.Error("Enabled = true, want false")
	}
	if updated.NotifyOnFailure {
		t.Error("NotifyOnFailure = true, want false")
	}
	// The subscriptions must survive an unrelated patch.
	if len(updated.ChannelIDs) != 1 {
		t.Errorf("ChannelIDs = %v after an unrelated patch, want 1 entry", updated.ChannelIDs)
	}

	// Clear the subscriptions.
	empty := []string{}
	updated, err = db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{Channels: &empty})
	if err != nil {
		t.Fatalf("UpdateWatch(clear channels): %v", err)
	}
	if len(updated.ChannelIDs) != 0 {
		t.Errorf("ChannelIDs = %v, want empty", updated.ChannelIDs)
	}

	if _, err := db.UpdateWatch(ctx, "w_missing", UpdateWatchInput{Enabled: &enabled}); !IsNotFound(err) {
		t.Errorf("UpdateWatch on a missing watch: got %v, want ErrNotFound", err)
	}
}

func TestDeleteWatchCascades(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	if err := db.UpsertWatchTags(ctx, watch.ID, []apitypes.TagDigest{{Tag: "v1", Digest: "sha256:x"}}); err != nil {
		t.Fatalf("UpsertWatchTags: %v", err)
	}
	event, err := db.CreateEvent(ctx, CreateEventInput{
		WatchID: watch.ID, Type: apitypes.EventDigestChanged, Tag: "v1",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	channel := mustCreateChannel(t, db, "ops")
	if _, err := db.CreateNotification(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}

	if err := db.DeleteWatch(ctx, watch.ID); err != nil {
		t.Fatalf("DeleteWatch: %v", err)
	}

	if _, err := db.GetWatch(ctx, watch.ID); !IsNotFound(err) {
		t.Errorf("GetWatch after delete: got %v, want ErrNotFound", err)
	}
	if tags, err := db.CountWatchTags(ctx, watch.ID); err != nil || tags != 0 {
		t.Errorf("watch_tags survived the cascade: %d rows, err %v", tags, err)
	}
	if _, err := db.GetEvent(ctx, event.ID); !IsNotFound(err) {
		t.Errorf("event survived the cascade: got %v, want ErrNotFound", err)
	}

	var notifications int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&notifications); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications != 0 {
		t.Errorf("notifications survived the cascade: %d rows", notifications)
	}

	if err := db.DeleteWatch(ctx, watch.ID); !IsNotFound(err) {
		t.Errorf("second DeleteWatch: got %v, want ErrNotFound", err)
	}
}

func TestListWatchesFiltersAndPagination(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	for i, ref := range []string{"v1", "v2", "v3"} {
		w := mustCreateWatch(t, db, "ghcr.io", "owner/app", ref, nil)
		if i == 2 {
			disabled := false
			if _, err := db.UpdateWatch(ctx, w.ID, UpdateWatchInput{Enabled: &disabled}); err != nil {
				t.Fatalf("disable watch: %v", err)
			}
		}
	}
	mustCreateWatch(t, db, "quay.io", "other/app", "latest", nil)

	all, total, err := db.ListWatches(ctx, WatchFilter{})
	if err != nil {
		t.Fatalf("ListWatches: %v", err)
	}
	if total != 4 || len(all) != 4 {
		t.Errorf("ListWatches returned %d rows (total %d), want 4", len(all), total)
	}

	enabled := true
	on, total, err := db.ListWatches(ctx, WatchFilter{Enabled: &enabled})
	if err != nil {
		t.Fatalf("ListWatches(enabled): %v", err)
	}
	if total != 3 || len(on) != 3 {
		t.Errorf("enabled watches = %d (total %d), want 3", len(on), total)
	}

	byRegistry, total, err := db.ListWatches(ctx, WatchFilter{Registry: "quay.io"})
	if err != nil {
		t.Fatalf("ListWatches(registry): %v", err)
	}
	if total != 1 || len(byRegistry) != 1 {
		t.Errorf("quay.io watches = %d (total %d), want 1", len(byRegistry), total)
	}

	searched, total, err := db.ListWatches(ctx, WatchFilter{Query: "other"})
	if err != nil {
		t.Fatalf("ListWatches(query): %v", err)
	}
	if total != 1 || len(searched) != 1 {
		t.Errorf("search for 'other' = %d (total %d), want 1", len(searched), total)
	}

	page, total, err := db.ListWatches(ctx, WatchFilter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListWatches(paginated): %v", err)
	}
	if len(page) != 2 {
		t.Errorf("paginated page size = %d, want 2", len(page))
	}
	// The total must describe the whole filtered set, not the page.
	if total != 4 {
		t.Errorf("paginated total = %d, want 4", total)
	}
}

func TestDueWatchesRespectsEnabledAndSchedule(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)

	// A new watch is due immediately.
	due, err := db.DueWatches(ctx, nowUTC(), 0)
	if err != nil {
		t.Fatalf("DueWatches: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("DueWatches returned %d watches, want 1", len(due))
	}

	// Push the next attempt into the future.
	future := nowUTC().Add(2 * timeHour)
	if err := db.MarkWatchSuccess(ctx, watch.ID, "sha256:abc", future); err != nil {
		t.Fatalf("MarkWatchSuccess: %v", err)
	}
	due, err = db.DueWatches(ctx, nowUTC(), 0)
	if err != nil {
		t.Fatalf("DueWatches: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("DueWatches returned %d watches after rescheduling, want 0", len(due))
	}

	// A disabled watch is never due, even when its schedule has passed.
	disabled := false
	if _, err := db.UpdateWatch(ctx, watch.ID, UpdateWatchInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	past := nowUTC().Add(-timeHour)
	if err := db.MarkWatchSuccess(ctx, watch.ID, "", past); err != nil {
		t.Fatalf("MarkWatchSuccess: %v", err)
	}
	due, err = db.DueWatches(ctx, nowUTC(), 0)
	if err != nil {
		t.Fatalf("DueWatches: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("DueWatches returned %d disabled watches, want 0", len(due))
	}
}

func TestMarkWatchSuccessResetsFailures(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v1", nil)
	if err := db.MarkWatchFailure(ctx, watch.ID, "boom", nowUTC()); err != nil {
		t.Fatalf("MarkWatchFailure: %v", err)
	}
	if err := db.MarkWatchFailure(ctx, watch.ID, "boom again", nowUTC()); err != nil {
		t.Fatalf("MarkWatchFailure: %v", err)
	}

	failed, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if failed.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", failed.ConsecutiveFailures)
	}
	if failed.LastError != "boom again" {
		t.Errorf("LastError = %q, want %q", failed.LastError, "boom again")
	}
	if failed.LastOKAt != nil {
		t.Error("LastOKAt is set after failures only")
	}

	if err := db.MarkWatchSuccess(ctx, watch.ID, "sha256:new", nowUTC()); err != nil {
		t.Fatalf("MarkWatchSuccess: %v", err)
	}
	recovered, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if recovered.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after success, want 0", recovered.ConsecutiveFailures)
	}
	if recovered.LastError != "" {
		t.Errorf("LastError = %q after success, want empty", recovered.LastError)
	}
	if recovered.LastOKAt == nil {
		t.Error("LastOKAt = nil after a success")
	}
	if recovered.LastDigest != "sha256:new" {
		t.Errorf("LastDigest = %q, want sha256:new", recovered.LastDigest)
	}
}

func TestWatchTagsUpsertAndPrune(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	watch := mustCreateWatch(t, db, "ghcr.io", "owner/app", "v*", nil)
	seed := []apitypes.TagDigest{
		{Tag: "v1", Digest: "sha256:one"},
		{Tag: "v2", Digest: "sha256:two"},
	}
	if err := db.UpsertWatchTags(ctx, watch.ID, seed); err != nil {
		t.Fatalf("UpsertWatchTags: %v", err)
	}
	// Re-upserting updates the digest without duplicating the row.
	if err := db.UpsertWatchTags(ctx, watch.ID, []apitypes.TagDigest{{Tag: "v1", Digest: "sha256:updated"}}); err != nil {
		t.Fatalf("UpsertWatchTags (update): %v", err)
	}

	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if len(known) != 2 {
		t.Fatalf("KnownTags has %d entries, want 2", len(known))
	}
	if known["v1"] != "sha256:updated" {
		t.Errorf("v1 digest = %q, want sha256:updated", known["v1"])
	}

	// Pruning removes tags that vanished upstream.
	pruned, err := db.PruneWatchTags(ctx, watch.ID, []string{"v1"})
	if err != nil {
		t.Fatalf("PruneWatchTags: %v", err)
	}
	if pruned != 1 {
		t.Errorf("PruneWatchTags removed %d rows, want 1", pruned)
	}
	known, err = db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if _, ok := known["v2"]; ok {
		t.Error("v2 was not pruned")
	}
	if _, ok := known["v1"]; !ok {
		t.Error("v1 was pruned but is still upstream")
	}
}

func TestStoreErrorsAreTyped(t *testing.T) {
	db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.GetWatch(ctx, "w_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetWatch: got %v, want ErrNotFound", err)
	}
	if err := db.DeleteWatch(ctx, "w_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteWatch: got %v, want ErrNotFound", err)
	}
	if _, err := db.GetChannel(ctx, "c_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetChannel: got %v, want ErrNotFound", err)
	}
	if _, err := db.GetCredentials(ctx, "nope.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetCredentials: got %v, want ErrNotFound", err)
	}
}

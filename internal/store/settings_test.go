package store_test

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

func TestSettingsRoundTrip(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, "history", "5"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, "notify_threshold", "3"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got["history"] != "5" || got["notify_threshold"] != "3" {
		t.Errorf("settings = %v, want history=5 notify_threshold=3", got)
	}

	// Re-setting a key must overwrite (the ON CONFLICT upsert branch).
	if err := db.SetSetting(ctx, "history", "7"); err != nil {
		t.Fatalf("SetSetting overwrite: %v", err)
	}
	got, err = db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got["history"] != "7" {
		t.Errorf("history = %v after overwrite, want 7", got["history"])
	}

	if err := db.UnsetSetting(ctx, "history"); err != nil {
		t.Fatalf("UnsetSetting: %v", err)
	}
	got, err = db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if _, ok := got["history"]; ok {
		t.Errorf("history still set after unset: %v", got)
	}
	if err := db.UnsetSetting(ctx, "history"); !store.IsNotFound(err) {
		t.Errorf("unsetting an unset setting = %v, want ErrNotFound", err)
	}
}

func TestWatchFlagOverrides(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	if err := db.SetWatchFlag(ctx, watch.ID, "history", 5); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if v, _ := got.Flags["history"].(float64); v != 5 {
		t.Errorf("history override = %v, want 5", got.Flags["history"])
	}

	// A second flag joins the same JSON object without losing the first.
	if err := db.SetWatchFlag(ctx, watch.ID, "notify_threshold", 2); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	got, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if len(got.Flags) != 2 {
		t.Errorf("flags = %v, want two overrides", got.Flags)
	}

	if err := db.UnsetWatchFlag(ctx, watch.ID, "history"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	got, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if _, ok := got.Flags["history"]; ok {
		t.Errorf("history still set after unset: %v", got.Flags)
	}
	if err := db.UnsetWatchFlag(ctx, watch.ID, "history"); !store.IsNotFound(err) {
		t.Errorf("unsetting an unset flag = %v, want ErrNotFound", err)
	}
	if err := db.SetWatchFlag(ctx, "w_missing", "history", 5); !store.IsNotFound(err) {
		t.Errorf("setting a flag on a missing watch = %v, want ErrNotFound", err)
	}
}

func TestPendingCountsAndSemverState(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	n, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingNotifyCount, 1)
	if err != nil || n != 1 {
		t.Fatalf("increment = (%d, %v), want (1, nil)", n, err)
	}
	n, err = db.IncrementPendingCount(ctx, watch.ID, store.PendingNotifyCount, 1)
	if err != nil || n != 2 {
		t.Fatalf("increment = (%d, %v), want (2, nil)", n, err)
	}
	if err := db.ResetPendingCount(ctx, watch.ID, store.PendingNotifyCount); err != nil {
		t.Fatalf("ResetPendingCount: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.PendingNotifyCount != 0 {
		t.Errorf("pending = %d after reset, want 0", got.PendingNotifyCount)
	}

	if _, err := db.IncrementPendingCount(ctx, watch.ID, "last_digest", 1); err == nil {
		t.Error("a non-counter column was accepted")
	}
	if _, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingSemverCount, 3); err != nil {
		t.Fatalf("increment semver: %v", err)
	}

	if err := db.SetLatestSemver(ctx, watch.ID, "1.2.3", "v1.2.3"); err != nil {
		t.Fatalf("SetLatestSemver: %v", err)
	}
	got, err = db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.2.3" || got.LatestSemverTag != "v1.2.3" || got.PendingSemverCount != 3 {
		t.Errorf("semver state = %+v, want 1.2.3 / v1.2.3 / pending 3", got)
	}
}

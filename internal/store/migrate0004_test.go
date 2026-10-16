package store_test

import (
	"context"
	"testing"

	"github.com/andriotisnikos1/dit/internal/testutil"
)

// TestMigration0004AddsFlagState verifies migration 0004 adds the flag columns
// with their defaults and leaves existing watches intact. 0004 is pure
// ADD COLUMN + CREATE TABLE — no CHECK constraint is touched, so unlike 0003
// there is no table rebuild and no cascade risk.
func TestMigration0004AddsFlagState(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	var (
		flagsJSON                     string
		pendingNotify, pendingSemver  int
		latestSemver, latestSemverTag string
	)
	err := db.SQL().QueryRowContext(ctx,
		`SELECT flags_json, pending_notify_count, pending_semver_count, latest_semver, latest_semver_tag
		 FROM watches WHERE id = ?`, watch.ID).
		Scan(&flagsJSON, &pendingNotify, &pendingSemver, &latestSemver, &latestSemverTag)
	if err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if flagsJSON != "" || pendingNotify != 0 || pendingSemver != 0 ||
		latestSemver != "" || latestSemverTag != "" {
		t.Errorf("new columns not defaulted: flags=%q pending=%d/%d semver=%q/%q",
			flagsJSON, pendingNotify, pendingSemver, latestSemver, latestSemverTag)
	}

	// The typed API must surface the new state.
	record, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if record.PendingNotifyCount != 0 || record.PendingSemverCount != 0 ||
		record.LatestSemver != "" || record.LatestSemverTag != "" {
		t.Errorf("WatchRecord not populated from the new columns: %+v", record)
	}
	if len(record.Flags) != 0 {
		t.Errorf("Flags = %v, want empty for a fresh watch", record.Flags)
	}
}

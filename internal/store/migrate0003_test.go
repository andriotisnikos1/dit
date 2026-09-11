package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// 0003 rebuilds the channels table to widen its type CHECK. Rebuilding a table
// that a cascading foreign key points at can silently delete the child rows, so
// this pins the behaviour: the migration text is executed against a populated
// database and the subscription rows must survive.
func TestMigration0003PreservesWatchChannelSubscriptions(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	watch, err := db.CreateWatch(ctx, CreateWatchInput{
		Image:           "index.docker.io/library/nginx:1.27-alpine",
		Registry:        "index.docker.io",
		Repository:      "library/nginx",
		Kind:            apitypes.WatchKindTag,
		Ref:             "1.27-alpine",
		Enabled:         true,
		NotifyOnFailure: true,
	})
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}
	channel, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "ops", Type: apitypes.ChannelNtfy,
		Config: map[string]string{"topic": "t"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := db.SetWatchChannels(ctx, watch.ID, []string{channel.ID}); err != nil {
		t.Fatalf("SetWatchChannels: %v", err)
	}

	before := countWatchChannels(t, db)
	if before == 0 {
		t.Fatal("setup failed: no watch_channels rows to protect")
	}

	// Capture the exact pre-migration column values to compare against
	// afterwards, rather than assuming what they were.
	var beforeEnabled, beforeDefault int
	if err := db.db.QueryRow(
		`SELECT enabled, is_default FROM channels WHERE id = ?`, channel.ID).
		Scan(&beforeEnabled, &beforeDefault); err != nil {
		t.Fatalf("read pre-migration flags: %v", err)
	}

	// Run the real migration body, in the transaction the runner would use.
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0003_channel_email_http.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if err := db.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(body))
		return err
	}); err != nil {
		t.Fatalf("apply 0003: %v", err)
	}

	if after := countWatchChannels(t, db); after != before {
		t.Errorf("DATA LOSS: watch_channels went from %d to %d rows across the rebuild",
			before, after)
	}

	// The subscription must still resolve to the same channel.
	var gotChannel string
	if err := db.db.QueryRow(
		`SELECT channel_id FROM watch_channels WHERE watch_id = ?`, watch.ID).Scan(&gotChannel); err != nil {
		t.Fatalf("subscription row gone: %v", err)
	}
	if gotChannel != channel.ID {
		t.Errorf("channel_id = %q, want %q", gotChannel, channel.ID)
	}

	// And the widened constraint must now accept the new type, while still
	// rejecting a genuinely unknown one.
	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "mail", Type: apitypes.ChannelEmailHTTP,
		Config: map[string]string{"provider": "resend"},
	}); err != nil {
		t.Errorf("email-http rejected after the rebuild: %v", err)
	}
	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "bogus", Type: apitypes.ChannelType("carrier-pigeon"),
		Config: map[string]string{},
	}); err == nil {
		t.Error("the CHECK constraint should still reject an unknown type")
	}

	// The original rows must be intact, not just the count.
	kept, err := db.GetChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetChannel after rebuild: %v", err)
	}
	if kept.Name != "ops" || kept.Config["topic"] != "t" {
		t.Errorf("channel rows were not preserved: %+v", kept)
	}
	var enabled, isDefault int
	if err := db.db.QueryRow(`SELECT enabled, is_default FROM channels WHERE id = ?`, channel.ID).
		Scan(&enabled, &isDefault); err != nil {
		t.Fatalf("scan flags: %v", err)
	}
	if enabled != beforeEnabled {
		t.Errorf("enabled = %d after the rebuild, want the pre-migration %d", enabled, beforeEnabled)
	}
	if isDefault != beforeDefault {
		t.Errorf("is_default = %d after the rebuild, want the pre-migration %d", isDefault, beforeDefault)
	}
}

func countWatchChannels(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM watch_channels`).Scan(&n); err != nil {
		t.Fatalf("count watch_channels: %v", err)
	}
	return n
}

// The migration must be safe on an empty database too, which is the case for
// every fresh install.
func TestMigration0003OnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t)

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0003_channel_email_http.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if err := db.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(body))
		return err
	}); err != nil {
		t.Fatalf("apply 0003 on an empty database: %v", err)
	}
	if _, err := db.CreateChannel(ctx, CreateChannelInput{
		Name: "mail", Type: apitypes.ChannelEmailHTTP,
		Config: map[string]string{"provider": "resend"},
	}); err != nil {
		t.Errorf("email-http rejected after the rebuild: %v", err)
	}
}

// A guard against the migration reintroducing the bare pragma, which the runner
// cannot honour inside its transaction and which would therefore be misleading.
func TestMigration0003DoesNotRelyOnForeignKeysPragma(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0003_channel_email_http.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(strings.ToUpper(line))
		if strings.HasPrefix(trimmed, "PRAGMA FOREIGN_KEYS") {
			t.Errorf("migration sets a foreign_keys pragma (%q), which is a no-op "+
				"inside the migration transaction", strings.TrimSpace(line))
		}
	}
}

package store

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/crypto"
)

// timeHour and nowUTC keep the tests terse without importing time everywhere.
const timeHour = time.Hour

func nowUTC() time.Time { return time.Now().UTC() }

// migrationsFS loads the migration files from the repository root. The store
// package cannot embed them itself (go:embed cannot reach outside its own
// directory), so tests read them from disk.
func migrationsFS() fs.FS {
	return os.DirFS(filepath.Join("..", "..", "migrations"))
}

// newTestStore opens a migrated store in a temporary directory.
func newTestStore(t *testing.T) *DB {
	t.Helper()
	sealer, err := crypto.NewSealer(make([]byte, crypto.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	db, err := Open(context.Background(), Options{
		Path:       filepath.Join(t.TempDir(), "dit.db"),
		Sealer:     sealer,
		Migrations: migrationsFS(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustCreateWatch(t *testing.T, db *DB, registryHost, repository, ref string, channels []string) *WatchRecord {
	t.Helper()
	watch, err := db.CreateWatch(context.Background(), CreateWatchInput{
		Image:           registryHost + "/" + repository + ":" + ref,
		Registry:        registryHost,
		Repository:      repository,
		Kind:            apitypes.WatchKindTag,
		Ref:             ref,
		Enabled:         true,
		NotifyOnFailure: true,
		Channels:        channels,
	})
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}
	return watch
}

func mustCreateChannel(t *testing.T, db *DB, name string) *ChannelRecord {
	t.Helper()
	channel, err := db.CreateChannel(context.Background(), CreateChannelInput{
		Name:    name,
		Type:    apitypes.ChannelNtfy,
		Config:  map[string]string{apitypes.ConfigTopic: "dit-test"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	return channel
}

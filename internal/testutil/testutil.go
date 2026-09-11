// Package testutil holds fixtures shared by the package test suites. It is a
// normal package so that several test packages can import it without
// duplicating setup code; only tests import it.
//
// It deliberately does not import internal/check: the check package's own
// tests build an engine directly, which keeps the dependency graph acyclic.
package testutil

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/andriotisnikos1/dit"
	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/crypto"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/store"
)

// Sealer returns a deterministic sealer for tests.
func Sealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{0x2b}, crypto.KeySize))
	if err != nil {
		t.Fatalf("build sealer: %v", err)
	}
	return sealer
}

// Store opens a migrated store backed by a temporary file. A file is used
// rather than :memory: so that WAL mode and foreign keys are exercised exactly
// as they are in production.
func Store(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{
		Path:       filepath.Join(t.TempDir(), "dit.db"),
		Sealer:     Sealer(t),
		Migrations: dit.Migrations,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Channel creates an ntfy channel and registers a recording transport for its
// ID, returning both.
func Channel(t *testing.T, db *store.DB, builder *notify.FakeBuilder, name string, isDefault bool) (*store.ChannelRecord, *notify.Recording) {
	t.Helper()
	channel, err := db.CreateChannel(context.Background(), store.CreateChannelInput{
		Name:      name,
		Type:      apitypes.ChannelNtfy,
		Config:    map[string]string{apitypes.ConfigTopic: "dit-test"},
		Enabled:   true,
		IsDefault: isDefault,
	})
	if err != nil {
		t.Fatalf("create channel %s: %v", name, err)
	}
	rec := builder.Set(channel.ID, string(apitypes.ChannelNtfy))
	return channel, rec
}

// Watch creates a tag watch.
func Watch(t *testing.T, db *store.DB, registryHost, repository, tag string, channels []string) *store.WatchRecord {
	t.Helper()
	watch, err := db.CreateWatch(context.Background(), store.CreateWatchInput{
		Image:           registryHost + "/" + repository + ":" + tag,
		Registry:        registryHost,
		Repository:      repository,
		Kind:            apitypes.WatchKindTag,
		Ref:             tag,
		Enabled:         true,
		NotifyOnFailure: true,
		Channels:        channels,
	})
	if err != nil {
		t.Fatalf("create watch: %v", err)
	}
	return watch
}

// PatternWatch creates a pattern watch.
func PatternWatch(t *testing.T, db *store.DB, registryHost, repository, pattern string) *store.WatchRecord {
	t.Helper()
	watch, err := db.CreateWatch(context.Background(), store.CreateWatchInput{
		Image:           registryHost + "/" + repository,
		Registry:        registryHost,
		Repository:      repository,
		Kind:            apitypes.WatchKindPattern,
		Ref:             pattern,
		Enabled:         true,
		NotifyOnFailure: true,
	})
	if err != nil {
		t.Fatalf("create pattern watch: %v", err)
	}
	return watch
}

// DockerHub is the normalised registry host for Docker Hub references.
const DockerHub = "index.docker.io"

// Digest builds a plausible sha256 digest string from a seed byte.
func Digest(seed byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 71)
	out = append(out, "sha256:"...)
	for i := 0; i < 64; i++ {
		out = append(out, hexDigits[(int(seed)+i)%16])
	}
	return string(out)
}

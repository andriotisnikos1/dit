package check

import (
	"math/rand"
	"testing"
	"time"

	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// fixedNow is the clock every engine test runs on, so scheduling assertions
// are deterministic.
func fixedNow() time.Time {
	return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
}

// nowFixed is the event timestamp used by message-rendering tests.
func nowFixed() time.Time { return fixedNow() }

// testEngine builds an engine wired to a fake registry and a recording
// notifier, with a frozen clock and a fixed jitter source.
func testEngine(t *testing.T, db *store.DB, reg registry.Client, builder notify.Builder) *Engine {
	t.Helper()
	engine, err := New(Options{
		Store:          db,
		Registry:       reg,
		Notifier:       builder,
		Interval:       time.Hour,
		Concurrency:    2,
		Timeout:        5 * time.Second,
		NotifyAttempts: 2,
		NotifyBackoff:  time.Millisecond,
		Now:            fixedNow,
		Rand:           rand.New(rand.NewSource(1)),
	})
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	return engine
}

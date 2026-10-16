package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/flags"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

// ---------------------------------------------------------------------------
// Adversarial probes for the flag persistence layer.
//
// Each test states a falsifiable claim about a boundary the code does not
// obviously cover: a corrupt stored value, a concurrent read-modify-write, an
// empty family in a per-family prune, a legacy row created before migration
// 0004. Passing tests are the evidence for "verified correct"; the failing ones
// were the bugs.
// ---------------------------------------------------------------------------

// rawFlagsJSON reads the column behind WatchRecord.Flags, so a test can see the
// stored representation rather than the decoded one.
func rawFlagsJSON(t *testing.T, db *store.DB, watchID string) string {
	t.Helper()
	var raw string
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT flags_json FROM watches WHERE id = ?`, watchID).Scan(&raw); err != nil {
		t.Fatalf("read flags_json: %v", err)
	}
	return raw
}

// claim: SetWatchFlag must not assume the stored flags_json decodes to a
// non-nil map. flags_json = "null" is a value json.Unmarshal accepts for a
// map[string]any target *and* leaves the target nil, so the read-modify-write
// panics on assignment. A panic inside an HTTP handler is a 500 (and a
// crash-loop risk under a panic-recovering middleware), not "flag not set".
func TestSetWatchFlagSurvivesNullFlagsJSON(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	// Corrupt the stored value the way only external tampering (or a future
	// writer bug) could: the JSON literal null.
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE watches SET flags_json = 'null' WHERE id = ?`, watch.ID); err != nil {
		t.Fatalf("seed corrupt flags_json: %v", err)
	}

	var panicked any
	func() {
		defer func() { panicked = recover() }()
		err := db.SetWatchFlag(ctx, watch.ID, "history", 5)
		if err != nil {
			t.Errorf("SetWatchFlag on a null flags_json = %v, want the override stored", err)
		}
	}()
	if panicked != nil {
		t.Fatalf("SetWatchFlag panicked on flags_json='null': %v", panicked)
	}

	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if v, isFloat := got.Flags["history"].(float64); !isFloat || v != 5 {
		t.Errorf("history override = %#v, want 5 after recovering from a null flags_json", got.Flags)
	}
}

// claim: the read-modify-write of flags_json is atomic, so concurrent writers
// setting distinct flags never lose one another's key. The store pins a single
// connection, so this is the real boundary, not a stub.
func TestSetWatchFlagConcurrentWritersDoNotLoseOverrides(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	names := []string{"history", "notify_threshold", "semver", "semver_stable", "semver_notify_threshold"}
	const rounds = 8

	var wg sync.WaitGroup
	errs := make([]error, 0, len(names)*rounds)
	var mu sync.Mutex
	for r := 0; r < rounds; r++ {
		for _, name := range names {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				if err := db.SetWatchFlag(ctx, watch.ID, name, true); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}(name)
		}
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d concurrent SetWatchFlag calls failed, first: %v", len(errs), errs[0])
	}

	raw := rawFlagsJSON(t, db, watch.ID)
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("stored flags_json %q is not an object: %v", raw, err)
	}
	if len(decoded) != len(names) {
		t.Errorf("stored overrides = %v (%d keys), want all %d names — a concurrent writer was lost",
			decoded, len(decoded), len(names))
	}
}

// claim: unsetting the last override leaves a decodable empty object (not ""),
// an absent flag reports ErrNotFound both for a fresh watch and on a repeat
// unset, and interleaved set/unset pairs of the same flag leave the key in the
// state the winning operation chose — never a written-but-absent or
// absent-but-present row.
func TestUnsetWatchFlagRepresentationAndPairs(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	if err := db.UnsetWatchFlag(ctx, watch.ID, "history"); !store.IsNotFound(err) {
		t.Errorf("unset on a fresh watch = %v, want ErrNotFound", err)
	}
	if err := db.UnsetWatchFlag(ctx, "w_missing", "history"); !store.IsNotFound(err) {
		t.Errorf("unset on a missing watch = %v, want ErrNotFound", err)
	}
	if raw := rawFlagsJSON(t, db, watch.ID); raw != "" {
		t.Errorf("a failed unset wrote flags_json=%q, want the untouched empty string", raw)
	}

	if err := db.SetWatchFlag(ctx, watch.ID, "notify_threshold", 3); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if err := db.UnsetWatchFlag(ctx, watch.ID, "notify_threshold"); err != nil {
		t.Fatalf("UnsetWatchFlag: %v", err)
	}
	if raw := rawFlagsJSON(t, db, watch.ID); raw != "{}" {
		t.Errorf("flags_json after the last unset = %q, want %q", raw, "{}")
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if len(got.Flags) != 0 {
		t.Errorf("Flags = %v after the last unset, want empty", got.Flags)
	}
	if err := db.UnsetWatchFlag(ctx, watch.ID, "notify_threshold"); !store.IsNotFound(err) {
		t.Errorf("second unset = %v, want ErrNotFound", err)
	}

	// Interleaved set/unset of two distinct flags: the only acceptable
	// inconsistencies are the documented ErrNotFound from losing the race.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := db.SetWatchFlag(ctx, watch.ID, "history", 2); err != nil {
				t.Errorf("concurrent SetWatchFlag: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.UnsetWatchFlag(ctx, watch.ID, "history"); err != nil && !store.IsNotFound(err) {
				t.Errorf("concurrent UnsetWatchFlag: %v", err)
			}
		}()
	}
	wg.Wait()

	raw := rawFlagsJSON(t, db, watch.ID)
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("flags_json %q is not decodable after concurrent pairs: %v", raw, err)
	}
	if v, ok := decoded["history"]; ok {
		if f, isFloat := v.(float64); !isFloat || f != 2 {
			t.Errorf("history = %#v, want 2 or absent", v)
		}
	}
}

// claim: the pending-counter increments are atomic across goroutines (the
// engine's per-watch mutex is in-process, but the store must not lose an
// increment even so), and the value that survives is the one GetWatch reads.
func TestPendingCountConcurrentIncrementsWithoutLoss(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	const goroutines, perGoroutine = 24, 3
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if _, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingNotifyCount, 1); err != nil {
					t.Errorf("IncrementPendingCount: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if want := goroutines * perGoroutine; got.PendingNotifyCount != want {
		t.Errorf("pending_notify_count = %d, want %d — %d increments were lost",
			got.PendingNotifyCount, want, want-got.PendingNotifyCount)
	}
	// The other counter must not have been touched.
	if got.PendingSemverCount != 0 {
		t.Errorf("pending_semver_count = %d, want 0 (increment hit the wrong column)", got.PendingSemverCount)
	}
}

// claim: the counter API rejects a delta that would make an undelivered-event
// count negative or leave it unchanged-as-a-write; and it rejects a missing
// watch and an unknown column. Records the observed behaviour either way.
func TestPendingCountDeltaBoundsAndErrors(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	if _, err := db.IncrementPendingCount(ctx, watch.ID, "history", 1); err == nil {
		t.Error("a whitelisted-but-unrelated column name was accepted as a counter")
	}
	if _, err := db.IncrementPendingCount(ctx, "w_missing", store.PendingNotifyCount, 1); !store.IsNotFound(err) {
		t.Errorf("increment on a missing watch = %v, want ErrNotFound", err)
	}
	if err := db.ResetPendingCount(ctx, "w_missing", store.PendingNotifyCount); !store.IsNotFound(err) {
		t.Errorf("reset on a missing watch = %v, want ErrNotFound", err)
	}
	if err := db.ResetPendingCount(ctx, watch.ID, "last_digest"); err == nil {
		t.Error("a non-counter column was accepted by ResetPendingCount")
	}
	if err := db.SetLatestSemver(ctx, "w_missing", "1.0.0", "v1.0.0"); !store.IsNotFound(err) {
		t.Errorf("SetLatestSemver on a missing watch = %v, want ErrNotFound", err)
	}

	// A failed increment must not have written anything.
	if _, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingNotifyCount, 0); err != nil {
		t.Fatalf("delta 0: %v", err)
	}
	n, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingSemverCount, -5)
	if err != nil {
		t.Fatalf("negative delta was rejected: %v", err)
	}
	if n < 0 {
		t.Logf("observed: a negative delta persists a negative undelivered-event count (%d)", n)
	}
}

// claim: the per-family prune applies the keep bound to each family
// independently — the NOT IN subquery must not delete a family with fewer rows
// than keep, an empty family must be a no-op, and an absent watch must not be
// an error.
func TestPruneWatchEventsBoundaries(t *testing.T) {
	ctx := context.Background()

	type scenario struct {
		name     string
		change   int
		failure  int
		keep     int
		wantLeft int
	}
	for _, sc := range []scenario{
		{"keep 1 of 4 changes", 4, 0, 1, 1},
		{"keep 2 of 4 changes", 4, 0, 2, 2},
		{"keep 4 of 4 changes", 4, 0, 4, 4},
		{"keep 5 of 4 changes (fewer rows than keep)", 4, 0, 5, 4},
		{"keep 2 of 3 changes + 1 failure", 3, 1, 2, 3}, // 2 change + 1 failure (its own family, under the bound)
		{"keep 2 of 1 change + 5 failures", 1, 5, 2, 3},
		{"empty failure family", 3, 0, 2, 2},
		{"empty change family", 0, 3, 2, 2},
	} {
		t.Run(sc.name, func(t *testing.T) {
			db := testutil.Store(t)
			watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
			for i := 0; i < sc.change; i++ {
				eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "c")
			}
			for i := 0; i < sc.failure; i++ {
				eventAt(t, db, watch.ID, apitypes.EventCheckFailed, "f")
			}
			if _, err := db.PruneWatchEvents(ctx, watch.ID, sc.keep); err != nil {
				t.Fatalf("PruneWatchEvents: %v", err)
			}
			_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100})
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if total != sc.wantLeft {
				t.Errorf("kept %d events, want %d (change=%d failure=%d keep=%d)",
					total, sc.wantLeft, sc.change, sc.failure, sc.keep)
			}
		})
	}

	// keep <= 0 wipes the whole history, and a watch with no events is a no-op.
	db := testutil.Store(t)
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	for i := 0; i < 3; i++ {
		eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "c")
	}
	eventAt(t, db, watch.ID, apitypes.EventCheckRecovered, "r")
	for _, keep := range []int{0, -1, -99} {
		pruned, err := db.PruneWatchEvents(ctx, watch.ID, keep)
		if err != nil {
			t.Fatalf("PruneWatchEvents(keep=%d): %v", keep, err)
		}
		if keep == 0 && pruned != 4 {
			t.Errorf("keep=0 pruned %d events, want the whole history (4)", pruned)
		}
	}
	if _, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 100}); err != nil || total != 0 {
		t.Errorf("after the wipe: (%d, %v), want 0 events", total, err)
	}
	if pruned, err := db.PruneWatchEvents(ctx, watch.ID, 5); err != nil || pruned != 0 {
		t.Errorf("pruning an empty history = (%d, %v), want (0, nil)", pruned, err)
	}
	if pruned, err := db.PruneWatchEvents(ctx, "w_missing", 2); err != nil || pruned != 0 {
		t.Errorf("pruning an unknown watch = (%d, %v), want (0, nil)", pruned, err)
	}
	// A watch with a very large history but a tiny keep: the newest survive,
	// and pruning is idempotent.
	big := testutil.Watch(t, db, testutil.DockerHub, "library/redis", "7", nil)
	for i := 0; i < 12; i++ {
		eventAt(t, db, big.ID, apitypes.EventNewTags, "batch")
	}
	if _, err := db.PruneWatchEvents(ctx, big.ID, 3); err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	if _, total, _ := db.ListEvents(ctx, store.EventFilter{WatchID: big.ID, Limit: 100}); total != 3 {
		t.Errorf("kept %d of 12 with keep=3", total)
	}
	if again, err := db.PruneWatchEvents(ctx, big.ID, 3); err != nil || again != 0 {
		t.Errorf("re-pruning = (%d, %v), want (0, nil) — pruning must be idempotent", again, err)
	}
}

// claim: RecentEvents returns exactly the newest n events of the requested
// types for that watch only, newest first, with the owning watch's image
// attached; n <= 0 and an empty type list short-circuit to nothing; an unknown
// type yields no rows rather than an error.
func TestRecentEventsBoundaries(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	other := testutil.Watch(t, db, testutil.DockerHub, "library/redis", "7", nil)

	for _, d := range []string{"d1", "d2", "d3"} {
		eventAt(t, db, watch.ID, apitypes.EventDigestChanged, d)
	}
	eventAt(t, db, other.ID, apitypes.EventDigestChanged, "other")

	got, err := db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), 2)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 2 || got[0].NewDigest != "d3" || got[1].NewDigest != "d2" {
		t.Errorf("RecentEvents(2) = %v, want [d3 d2]", digestsOf(got))
	}
	if got[0].Image != watch.Image {
		t.Errorf("Image = %q, want the owning watch's %q", got[0].Image, watch.Image)
	}

	got, err = db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), 99)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("RecentEvents(99) returned %d events, want 3 (only this watch's)", len(got))
	}

	for _, n := range []int{0, -1, -100} {
		got, err := db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), n)
		if err != nil || len(got) != 0 {
			t.Errorf("RecentEvents(n=%d) = (%v, %v), want (empty, nil)", n, len(got), err)
		}
	}
	for _, types := range [][]apitypes.EventType{nil, {}} {
		got, err := db.RecentEvents(ctx, watch.ID, types, 5)
		if err != nil || len(got) != 0 {
			t.Errorf("RecentEvents(types=%#v) = (%v, %v), want (empty, nil)", types, len(got), err)
		}
	}
	got, err = db.RecentEvents(ctx, watch.ID, []apitypes.EventType{apitypes.EventType("nonsense")}, 5)
	if err != nil {
		t.Errorf("RecentEvents with an unknown type errored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("RecentEvents with an unknown type returned %d events", len(got))
	}
	got, err = db.RecentEvents(ctx, watch.ID, apitypes.FailureEventTypes(), 5)
	if err != nil || len(got) != 0 {
		t.Errorf("failure family = (%d, %v), want none", len(got), err)
	}
}

func digestsOf(events []*store.EventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.NewDigest)
	}
	return out
}

// claim: migration 0004 backfills its NOT NULL columns on rows that already
// existed, leaves those rows otherwise intact, and rejects nothing — i.e. it is
// genuinely additive. Tested on a real database built from 0001–0003 only, so
// the row really predates 0004.
func TestMigration0004BackfillsLegacyRows(t *testing.T) {
	ctx := context.Background()
	legacy, err := store.Open(ctx, store.Options{
		Path:       filepath.Join(t.TempDir(), "legacy.db"),
		Sealer:     testutil.Sealer(t),
		Migrations: migrationsThrough(t, "0003_channel_email_http.sql"),
	})
	if err != nil {
		t.Fatalf("open a pre-0004 store: %v", err)
	}
	t.Cleanup(func() { _ = legacy.Close() })

	// Create the row before 0004 exists. Only the raw API can be used here:
	// WatchRecord's column list already references flags_json.
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	if _, err := legacy.SQL().ExecContext(ctx, `
		INSERT INTO watches (id, image, registry, repository, kind, ref, enabled,
			notify_on_failure, last_digest, last_error, consecutive_failures,
			next_attempt_at, created_at, updated_at)
		VALUES ('w_legacy', 'index.docker.io/library/nginx:1.27', 'index.docker.io',
			'library/nginx', 'tag', '1.27', 1, 1, 'sha256:abc', '', 0, ?, ?, ?)`,
		now, now, now); err != nil {
		t.Fatalf("insert pre-0004 watch: %v", err)
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0004_flags.sql"))
	if err != nil {
		t.Fatalf("read migration 0004: %v", err)
	}
	if _, err := legacy.SQL().ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("apply 0004 to a populated database: %v", err)
	}

	var (
		flagsJSON                     string
		pendingNotify, pendingSemver  int
		latestSemver, latestSemverTag string
		lastDigest                    string
	)
	if err := legacy.SQL().QueryRowContext(ctx, `
		SELECT flags_json, pending_notify_count, pending_semver_count,
		       latest_semver, latest_semver_tag, last_digest
		FROM watches WHERE id = 'w_legacy'`).
		Scan(&flagsJSON, &pendingNotify, &pendingSemver, &latestSemver, &latestSemverTag, &lastDigest); err != nil {
		t.Fatalf("read the migrated row: %v", err)
	}
	if flagsJSON != "" || pendingNotify != 0 || pendingSemver != 0 || latestSemver != "" || latestSemverTag != "" {
		t.Errorf("legacy row not backfilled: flags=%q pending=%d/%d semver=%q/%q",
			flagsJSON, pendingNotify, pendingSemver, latestSemver, latestSemverTag)
	}
	if lastDigest != "sha256:abc" {
		t.Errorf("legacy row lost data: last_digest = %q", lastDigest)
	}

	// The typed API must work on the migrated row, and the settings table it
	// created must be usable immediately.
	record, err := legacy.GetWatch(ctx, "w_legacy")
	if err != nil {
		t.Fatalf("GetWatch after 0004: %v", err)
	}
	if record.PendingNotifyCount != 0 || len(record.Flags) != 0 || record.LatestSemver != "" {
		t.Errorf("WatchRecord not populated from the backfilled columns: %+v", record)
	}
	if err := legacy.SetSetting(ctx, "history", "-1"); err != nil {
		t.Errorf("settings table unusable after 0004: %v", err)
	}
	// Rows created after the migration default the same way.
	if _, err := legacy.SQL().ExecContext(ctx, `
		INSERT INTO watches (id, image, registry, repository, kind, ref, enabled,
			notify_on_failure, last_digest, last_error, consecutive_failures,
			next_attempt_at, created_at, updated_at)
		VALUES ('w_new', 'index.docker.io/library/redis:7', 'index.docker.io',
			'library/redis', 'tag', '7', 1, 1, '', '', 0, ?, ?, ?)`,
		now, now, now); err != nil {
		t.Fatalf("insert post-0004 watch: %v", err)
	}
	if raw := rawFlagsJSON(t, legacy, "w_new"); raw != "" {
		t.Errorf("a row created after 0004 has flags_json=%q, want the '' default", raw)
	}
}

// migrationsThrough builds a migration filesystem containing every embedded
// migration up to and including last, so a test can create a database as it
// existed before a later migration.
func migrationsThrough(t *testing.T, last string) fstest.MapFS {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	fsys := fstest.MapFS{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "..", "migrations", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		fsys[e.Name()] = &fstest.MapFile{Data: body}
		if e.Name() == last {
			break
		}
	}
	if _, ok := fsys[last]; !ok {
		t.Fatalf("migration %s not found", last)
	}
	return fsys
}

// claim: the migration runner is the idempotency boundary — re-running Migrate
// on an up-to-date database applies nothing, errors nothing, and leaves the
// flag state of existing rows alone.
func TestMigrateIsIdempotentForFlagState(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	if err := db.SetWatchFlag(ctx, watch.ID, "history", 9); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	if err := db.SetSetting(ctx, "notify_threshold", "4"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	appliedBefore, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if _, ok := appliedBefore["0004_flags"]; !ok {
		t.Fatal("0004_flags is not recorded as applied — the test premise is wrong")
	}

	if err := db.Migrate(ctx, migrationsThrough(t, "0004_flags.sql")); err != nil {
		t.Fatalf("re-running Migrate: %v", err)
	}
	appliedAfter, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(appliedAfter) != len(appliedBefore) {
		t.Errorf("migration count changed from %d to %d across a re-run", len(appliedBefore), len(appliedAfter))
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if v, _ := got.Flags["history"].(float64); v != 9 {
		t.Errorf("history override = %#v after a migration re-run, want 9", got.Flags["history"])
	}
	settings, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings["notify_threshold"] != "4" {
		t.Errorf("settings = %v after a migration re-run, want notify_threshold=4", settings)
	}
}

// claim: the settings table stores the exact scalar strings the flags package
// formats, and those strings resolve back to the same typed values — including
// the two sentinel-looking values "-1" (keep all) and "true". An unknown key
// must be reported by Resolve rather than silently ignored.
func TestSettingsRoundTripThroughFlagResolution(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()

	history, _ := flags.Lookup("history")
	raw, err := flags.FormatValue(history, -1)
	if err != nil {
		t.Fatalf("FormatValue(history, -1): %v", err)
	}
	if raw != "-1" {
		t.Fatalf("FormatValue(history, -1) = %q, want \"-1\"", raw)
	}
	if err := db.SetSetting(ctx, "history", raw); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	semver, _ := flags.Lookup("semver")
	raw, err = flags.FormatValue(semver, true)
	if err != nil {
		t.Fatalf("FormatValue(semver, true): %v", err)
	}
	if err := db.SetSetting(ctx, "semver", raw); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	stored, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	asAny := map[string]any{}
	for k, v := range stored {
		asAny[k] = v
	}
	settings, err := flags.Resolve(nil, asAny)
	if err != nil {
		t.Fatalf("Resolve from stored settings: %v", err)
	}
	if settings.History != -1 || !settings.Semver {
		t.Errorf("resolved %+v, want history=-1 semver=true", settings)
	}

	// Empty settings resolve to the defaults, and the map is usable (non-nil).
	empty := testutil.Store(t)
	stored, err = empty.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings on a fresh store: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("fresh settings = %v, want empty", stored)
	}
	emptyAny := map[string]any{}
	for k, v := range stored {
		emptyAny[k] = v
	}
	if _, err := flags.Resolve(nil, emptyAny); err != nil {
		t.Errorf("Resolve over empty settings failed: %v", err)
	}

	// A stale/unknown key is surfaced by Resolve rather than ignored: the
	// documented contract, and the reason callers log and degrade.
	if err := db.SetSetting(ctx, "quota", "10"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	stored, err = db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	asAny = map[string]any{}
	for k, v := range stored {
		asAny[k] = v
	}
	if _, err := flags.Resolve(nil, asAny); err == nil {
		t.Error("Resolve accepted an unknown global key; corrupt state would be silently ignored")
	}
}

// claim: SetLatestSemver persists version and tag independently and never
// clears a value it was not handed; a reseed to "" must work.
func TestSetLatestSemverState(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.PatternWatch(t, db, testutil.DockerHub, "library/nginx", "1.27*")

	if err := db.SetLatestSemver(ctx, watch.ID, "1.2.3", "v1.2.3"); err != nil {
		t.Fatalf("SetLatestSemver: %v", err)
	}
	if err := db.SetLatestSemver(ctx, watch.ID, "1.3.0", ""); err != nil {
		t.Fatalf("SetLatestSemver: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if got.LatestSemver != "1.3.0" || got.LatestSemverTag != "" {
		t.Errorf("semver state = %q/%q, want 1.3.0/\"\"", got.LatestSemver, got.LatestSemverTag)
	}
	if err := db.SetLatestSemver(ctx, watch.ID, "", ""); err != nil {
		t.Fatalf("reseed to empty: %v", err)
	}
	if got, _ = db.GetWatch(ctx, watch.ID); got.LatestSemver != "" || got.LatestSemverTag != "" {
		t.Errorf("reseed left %q/%q", got.LatestSemver, got.LatestSemverTag)
	}

	// The counters and the semver state are separate columns: writing one must
	// not disturb the other.
	if _, err := db.IncrementPendingCount(ctx, watch.ID, store.PendingSemverCount, 2); err != nil {
		t.Fatalf("IncrementPendingCount: %v", err)
	}
	if err := db.SetLatestSemver(ctx, watch.ID, "2.0.0", "v2.0.0"); err != nil {
		t.Fatalf("SetLatestSemver: %v", err)
	}
	if got, _ = db.GetWatch(ctx, watch.ID); got.PendingSemverCount != 2 || got.LatestSemver != "2.0.0" {
		t.Errorf("state = %+v, want pending 2 with 2.0.0", got)
	}
}

// claim: migration 0004 does not rebuild any table and does not touch a CHECK
// constraint — child rows, the watch unique index and the kind CHECK all
// survive applying the real migration body to a populated pre-0004 database.
func TestMigration0004DoesNotRebuildOrRelaxConstraints(t *testing.T) {
	ctx := context.Background()
	legacy, err := store.Open(ctx, store.Options{
		Path:       filepath.Join(t.TempDir(), "legacy.db"),
		Sealer:     testutil.Sealer(t),
		Migrations: migrationsThrough(t, "0003_channel_email_http.sql"),
	})
	if err != nil {
		t.Fatalf("open a pre-0004 store: %v", err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	raw := legacy.SQL()

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("setup exec: %v", err)
		}
	}
	exec(`INSERT INTO watches (id, image, registry, repository, kind, ref, enabled,
			notify_on_failure, last_digest, last_error, consecutive_failures,
			next_attempt_at, created_at, updated_at)
		VALUES ('w1', 'index.docker.io/library/nginx:1.27', 'index.docker.io',
			'library/nginx', 'tag', '1.27', 1, 1, 'sha256:abc', '', 0, ?, ?, ?)`, now, now, now)
	exec(`INSERT INTO channels (id, name, type, config_json, enabled, is_default, created_at, updated_at)
		VALUES ('c1', 'ops', 'ntfy', '{}', 1, 1, ?, ?)`, now, now)
	exec(`INSERT INTO watch_channels (watch_id, channel_id) VALUES ('w1', 'c1')`)
	exec(`INSERT INTO watch_tags (watch_id, tag, digest, first_seen_at) VALUES ('w1', '1.27', 'sha256:abc', ?)`, now)
	exec(`INSERT INTO events (id, watch_id, type, tag, old_digest, new_digest, detail_json, created_at)
		VALUES ('e1', 'w1', 'digest_changed', '1.27', 'sha256:old', 'sha256:abc', '', ?)`, now)
	exec(`INSERT INTO notifications (id, event_id, channel_id, status, attempts, last_error, created_at)
		VALUES ('n1', 'e1', 'c1', 'sent', 1, '', ?)`, now)

	before := map[string]int{}
	for _, table := range []string{"watches", "watch_tags", "watch_channels", "channels", "events", "notifications"} {
		var n int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		before[table] = n
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0004_flags.sql"))
	if err != nil {
		t.Fatalf("read migration 0004: %v", err)
	}
	if _, err := raw.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("apply 0004: %v", err)
	}

	for _, table := range []string{"watches", "watch_tags", "watch_channels", "channels", "events", "notifications"} {
		var n int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != before[table] {
			t.Errorf("%s: %d rows before 0004, %d after — a table rebuild dropped data", table, before[table], n)
		}
	}

	// The unique index that enforces one watch per natural key must survive.
	var indexCount int
	if err := raw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_watches_unique'`).
		Scan(&indexCount); err != nil {
		t.Fatalf("inspect indexes: %v", err)
	}
	if indexCount != 1 {
		t.Error("idx_watches_unique did not survive migration 0004")
	}

	// The kind CHECK must still reject an unknown kind.
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO watches (id, image, registry, repository, kind, ref, enabled,
			notify_on_failure, last_digest, last_error, consecutive_failures,
			next_attempt_at, created_at, updated_at)
		VALUES ('w2', 'img', 'reg', 'repo', 'carrier-pigeon', 'r', 1, 1, '', '', 0, ?, ?, ?)`,
		now, now, now); err == nil {
		t.Error("the watches.kind CHECK no longer rejects an unknown kind after 0004")
	}

	// And every notification row still points at its event.
	var orphaned int
	if err := raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM notifications n
		LEFT JOIN events e ON e.id = n.event_id WHERE e.id IS NULL`).Scan(&orphaned); err != nil {
		t.Fatalf("check notifications: %v", err)
	}
	if orphaned != 0 {
		t.Errorf("%d notification rows lost their event", orphaned)
	}

	// The settings table shape the code depends on: key is the unique key,
	// value and updated_at are NOT NULL (the upsert in SetSetting relies on
	// the primary key's uniqueness).
	rows, err := raw.QueryContext(ctx, `PRAGMA table_info(settings)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(settings): %v", err)
	}
	defer rows.Close()
	shape := map[string]struct {
		notNull bool
		pk      int
	}{}
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan PRAGMA row: %v", err)
		}
		shape[name] = struct {
			notNull bool
			pk      int
		}{notNull == 1, pk}
	}
	if len(shape) != 3 {
		t.Fatalf("settings has %d columns, want 3: %v", len(shape), shape)
	}
	if c, ok := shape["key"]; !ok || c.pk != 1 {
		t.Errorf("settings.key = %+v, want the PRIMARY KEY", c)
	}
	for _, col := range []string{"value", "updated_at"} {
		if c, ok := shape[col]; !ok || !c.notNull {
			t.Errorf("settings.%s = %+v, want NOT NULL", col, c)
		}
	}
	// Uniqueness is enforced, and SetSetting upserts rather than duplicating.
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ('history', '1', ?)`, now); err != nil {
		t.Fatalf("first settings insert: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ('history', '2', ?)`, now); err == nil {
		t.Error("a duplicate settings key was accepted; the table has no unique key")
	}
	if err := legacy.SetSetting(ctx, "history", "3"); err != nil {
		t.Fatalf("SetSetting upsert: %v", err)
	}
	got, err := legacy.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got["history"] != "3" {
		t.Errorf("history = %q after the upsert, want 3", got["history"])
	}
}

// claim: the migration body itself is not re-runnable (ADD COLUMN is not
// idempotent), so re-application must fail loudly and leave the database
// untouched — the schema_migrations guard in Migrate is the boundary that keeps
// it from ever happening.
func TestMigration0004BodyReApplicationFailsCleanly(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	if err := db.SetWatchFlag(ctx, watch.ID, "history", 5); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0004_flags.sql"))
	if err != nil {
		t.Fatalf("read migration 0004: %v", err)
	}
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, execErr := tx.ExecContext(ctx, string(body))
	if execErr == nil {
		_ = tx.Rollback()
		t.Fatal("re-applying 0004 succeeded; ALTER TABLE ADD COLUMN should report a duplicate column")
	}
	if rbErr := tx.Rollback(); rbErr != nil {
		t.Fatalf("rollback: %v", rbErr)
	}
	t.Logf("observed: re-applying the 0004 body fails with %v and rolls back", execErr)

	// Nothing was lost, and the flag written before the attempt is intact.
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch after the failed re-apply: %v", err)
	}
	if v, _ := got.Flags["history"].(float64); v != 5 {
		t.Errorf("history = %#v after the failed re-apply, want 5", got.Flags["history"])
	}
	if err := db.SetSetting(ctx, "semver", "true"); err != nil {
		t.Errorf("settings table unusable after the failed re-apply: %v", err)
	}
	applied, err := db.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if _, ok := applied["0004_flags"]; !ok {
		t.Error("0004_flags is no longer recorded as applied")
	}
}

// claim: whatever scalar form the store is handed, GetWatch returns the JSON
// decoding of it and flags.Resolve turns that back into the same typed value —
// int, int64 and float64 all land as numbers and resolve as ints, bool stays a
// bool, and a stringified value (as a YAML-driven writer would send it) still
// resolves because ParseValue accepts strings.
func TestWatchFlagValueJSONRoundTripByType(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	writes := []struct {
		name string
		raw  any
	}{
		{"history", 5},
		{"notify_threshold", int64(3)},
		{"semver_notify_threshold", float64(4)},
		{"semver", true},
		{"semver_stable", "true"},
	}
	for _, w := range writes {
		if err := db.SetWatchFlag(ctx, watch.ID, w.name, w.raw); err != nil {
			t.Fatalf("SetWatchFlag(%s, %#v): %v", w.name, w.raw, err)
		}
	}

	raw := rawFlagsJSON(t, db, watch.ID)
	stored := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored flags_json %q: %v", raw, err)
	}
	if _, ok := stored["history"].(float64); !ok {
		t.Errorf("history stored as %T, want a JSON number", stored["history"])
	}
	if _, ok := stored["semver"].(bool); !ok {
		t.Errorf("semver stored as %T, want a JSON boolean", stored["semver"])
	}
	if _, ok := stored["semver_stable"].(string); !ok {
		t.Errorf("semver_stable (written as the string \"true\") stored as %T, want a JSON string", stored["semver_stable"])
	}

	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	settings, err := flags.Resolve(got.Flags, nil)
	if err != nil {
		t.Fatalf("Resolve over the stored overrides: %v", err)
	}
	want := flags.Settings{
		History:               5,
		NotifyThreshold:       3,
		Semver:                true,
		SemverStable:          true,
		SemverNotifyThreshold: 4,
	}
	if settings != want {
		t.Errorf("resolved %+v, want %+v (the store round trip changed a value)", settings, want)
	}
}

// claim: with identical created_at values the "newest" ordering falls back to
// id DESC, and ids are random, so "newest N" is arithmetically determined but
// arbitrary relative to insertion order. Records the boundary rather than
// asserting a policy the query does not implement.
func TestOrderingTieBreakForIdenticalTimestamps(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	// Inserted oldest-first: e_z is written BEFORE e_a, yet id DESC keeps e_z —
	// with identical timestamps the winner is decided by the id string, not by
	// which event was recorded first. Generated ids are random, so the survivor
	// of a same-nanosecond tie is arbitrary.
	for _, id := range []string{"e_z", "e_a"} {
		if _, err := db.SQL().ExecContext(ctx, `
			INSERT INTO events (id, watch_id, type, tag, old_digest, new_digest, detail_json, created_at)
			VALUES (?, ?, 'digest_changed', '1.27', '', ?, '', ?)`,
			id, watch.ID, id, now); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	got, err := db.RecentEvents(ctx, watch.ID, apitypes.ChangeEventTypes(), 1)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 1 || got[0].ID != "e_z" {
		t.Errorf("RecentEvents(1) = %v, want the id-descending winner e_z", idsOf(got))
	}
	if _, err := db.PruneWatchEvents(ctx, watch.ID, 1); err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}
	kept, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 || kept[0].ID != "e_z" {
		t.Errorf("prune kept %v, want e_z (id DESC within the same timestamp)", idsOf(kept))
	}
	t.Log("observed: identical timestamps tie-break on the random id, not on insertion order")
}

func idsOf(events []*store.EventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

// claim: corrupt stored flags_json is handled consistently across the read and
// write paths, and a write that cannot be encoded is rejected before anything
// is stored (no partial update).
func TestCorruptFlagsJSONAndUnencodableValues(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	if err := db.SetWatchFlag(ctx, watch.ID, "history", 5); err != nil {
		t.Fatalf("SetWatchFlag: %v", err)
	}
	before := rawFlagsJSON(t, db, watch.ID)

	// A value json cannot encode must be refused, and the stored overrides
	// must be exactly what they were.
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		if err := db.SetWatchFlag(ctx, watch.ID, "notify_threshold", math.NaN()); err == nil {
			t.Error("SetWatchFlag stored an unencodable value (NaN)")
		}
	}()
	if panicked != nil {
		t.Fatalf("SetWatchFlag panicked on an unencodable value: %v", panicked)
	}
	if after := rawFlagsJSON(t, db, watch.ID); after != before {
		t.Errorf("flags_json changed from %q to %q across a failed write", before, after)
	}

	// A JSON value of the wrong kind is corrupt state. Records the policy:
	// non-object values fail loudly on both paths, the null literal degrades.
	for _, bad := range []string{"[1,2]", `"history"`, "5", "{not json"} {
		if _, err := db.SQL().ExecContext(ctx,
			`UPDATE watches SET flags_json = ? WHERE id = ?`, bad, watch.ID); err != nil {
			t.Fatalf("seed %q: %v", bad, err)
		}
		if err := db.SetWatchFlag(ctx, watch.ID, "history", 5); err == nil {
			t.Errorf("flags_json=%s: SetWatchFlag accepted it", bad)
		}
		if err := db.UnsetWatchFlag(ctx, watch.ID, "history"); err == nil {
			t.Errorf("flags_json=%s: UnsetWatchFlag accepted it", bad)
		}
		if _, err := db.GetWatch(ctx, watch.ID); err == nil {
			t.Errorf("flags_json=%s: GetWatch returned it as valid state", bad)
		}
	}
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE watches SET flags_json = 'null' WHERE id = ?`, watch.ID); err != nil {
		t.Fatalf("seed null: %v", err)
	}
	got, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Errorf("flags_json=null: GetWatch failed with %v; the literal null decodes to \"no overrides\"", err)
	} else if len(got.Flags) != 0 {
		t.Errorf("flags_json=null: Flags = %v, want empty", got.Flags)
	}
	t.Log("observed: the null literal degrades to \"no overrides\" while other corrupt values fail loudly")
}

// claim: a channel notification attached to a pruned event disappears with it,
// and the surviving event's row is untouched — the cascade is by event, not by
// watch.
func TestPruneCascadeIsPerEvent(t *testing.T) {
	db := testutil.Store(t)
	ctx := context.Background()
	watch := testutil.Watch(t, db, testutil.DockerHub, "library/nginx", "1.27", nil)
	other := testutil.Watch(t, db, testutil.DockerHub, "library/redis", "7", nil)
	builder := notify.NewFakeBuilder()
	channel, _ := testutil.Channel(t, db, builder, "ops", true)

	eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "old")
	eventAt(t, db, watch.ID, apitypes.EventDigestChanged, "new")
	eventAt(t, db, other.ID, apitypes.EventDigestChanged, "other")

	// The other watch's event is created last, so it is the newest overall: a
	// prune that leaked across watches would delete it.
	events, _, err := db.ListEvents(ctx, store.EventFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range events {
		if _, err := db.CreateNotification(ctx, e.ID, channel.ID); err != nil {
			t.Fatalf("CreateNotification: %v", err)
		}
	}
	if _, err := db.PruneWatchEvents(ctx, watch.ID, 1); err != nil {
		t.Fatalf("PruneWatchEvents: %v", err)
	}

	remaining, total, err := db.ListEvents(ctx, store.EventFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 2 {
		t.Fatalf("kept %d events, want 2 (one per watch)", total)
	}
	for _, e := range remaining {
		if _, err := db.NotificationsForEvent(ctx, e.ID); err != nil {
			t.Fatalf("NotificationsForEvent: %v", err)
		}
	}
	_, notifTotal, err := db.ListNotifications(ctx, store.NotificationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if notifTotal != 2 {
		t.Errorf("kept %d notification rows, want 2 — the cascade removed a row whose event survived", notifTotal)
	}
}

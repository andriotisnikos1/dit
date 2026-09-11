package check

import (
	"context"
	"strings"
	"testing"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
	"github.com/andriotisnikos1/dit/internal/testutil"
)

const (
	testRegistry   = "index.docker.io"
	testRepository = "library/nginx"
)

// newEngine builds an engine over a fresh store with a fake registry and
// recording notifier, plus a default channel wired to a recorder.
func newEngine(t *testing.T) (*Engine, *store.DB, *registry.Fake, *notify.FakeBuilder, *notify.Recording) {
	t.Helper()
	db := testutil.Store(t)
	fake := registry.NewFake()
	builder := notify.NewFakeBuilder()
	_, rec := testutil.Channel(t, db, builder, "ops", true)
	engine := testEngine(t, db, fake, builder)
	return engine, db, fake, builder, rec
}

func TestCheckTagFirstCheckIsSilentBaseline(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	digest := testutil.Digest(1)
	fake.SetDigest(testRegistry, testRepository, "1.27", digest)

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if !result.Baseline {
		t.Error("Baseline = false on the first check")
	}
	if result.Changed {
		t.Error("Changed = true on the first check; a new watch must stay silent")
	}
	if result.EventID != "" {
		t.Errorf("EventID = %q, want empty: the first check records a baseline only", result.EventID)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications on the first check, want 0", rec.Count())
	}

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.LastDigest != digest {
		t.Errorf("LastDigest = %q, want %q", stored.LastDigest, digest)
	}
	if stored.LastCheckedAt == nil || stored.LastOKAt == nil {
		t.Error("LastCheckedAt/LastOKAt not set after a successful check")
	}
}

func TestCheckTagDigestDriftNotifies(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	newDigest := testutil.Digest(2)
	fake.SetDigest(testRegistry, testRepository, "1.27", newDigest)

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if !result.Changed {
		t.Fatal("Changed = false, want true after a digest change")
	}
	if result.Baseline {
		t.Error("Baseline = true on a second check")
	}
	if result.EventID == "" {
		t.Fatal("EventID is empty, want a recorded digest_changed event")
	}
	if result.Notified != 1 {
		t.Errorf("Notified = %d, want 1", result.Notified)
	}

	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if event.Type != apitypes.EventDigestChanged {
		t.Errorf("event type = %q, want %q", event.Type, apitypes.EventDigestChanged)
	}
	if event.OldDigest != testutil.Digest(1) || event.NewDigest != newDigest {
		t.Errorf("event digests = %q -> %q, want %q -> %q",
			event.OldDigest, event.NewDigest, testutil.Digest(1), newDigest)
	}

	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1", rec.Count())
	}
	msg := rec.Messages()[0]
	if msg.Title == "" || msg.Body == "" {
		t.Error("notification message has an empty title or body")
	}

	// The stored digest must be updated so the change is not reported twice.
	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.LastDigest != newDigest {
		t.Errorf("LastDigest = %q, want %q", stored.LastDigest, newDigest)
	}
}

func TestCheckTagNoChangeIsSilent(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if result.Changed {
		t.Error("Changed = true when the digest is unchanged")
	}
	if result.EventID != "" {
		t.Errorf("EventID = %q, want empty when nothing changed", result.EventID)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications with no change, want 0", rec.Count())
	}

	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 0 || len(events) != 0 {
		t.Errorf("recorded %d events with no change, want 0", total)
	}
}

func TestCheckPatternFirstCheckIsSilentBaseline(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "1.27.*")
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1", "1.26.9", "latest")

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if !result.Baseline {
		t.Error("Baseline = false on the first check")
	}
	if result.Matched != 2 {
		t.Errorf("Matched = %d, want 2", result.Matched)
	}
	if len(result.NewTags) != 0 {
		t.Errorf("NewTags = %v on the first check, want none reported", result.NewTags)
	}
	if result.EventID != "" {
		t.Errorf("EventID = %q, want empty on the baseline check", result.EventID)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications on the baseline check, want 0", rec.Count())
	}

	// The baseline must be recorded so the next check can diff against it.
	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if len(known) != 2 {
		t.Fatalf("recorded %d baseline tags, want 2", len(known))
	}
	if _, ok := known["latest"]; ok {
		t.Error("a tag outside the pattern was recorded in the baseline")
	}
}

func TestCheckPatternReportsNewTagsWithDigests(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "1.27.*")
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// A new tag appears upstream.
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1", "1.27.2")
	fake.SetDigest(testRegistry, testRepository, "1.27.2", testutil.Digest(7))

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if len(result.NewTags) != 1 {
		t.Fatalf("NewTags = %v, want exactly [1.27.2]", result.NewTags)
	}
	if result.NewTags[0].Tag != "1.27.2" {
		t.Errorf("new tag = %q, want 1.27.2", result.NewTags[0].Tag)
	}
	if result.NewTags[0].Digest != testutil.Digest(7) {
		t.Errorf("new tag digest = %q, want %q", result.NewTags[0].Digest, testutil.Digest(7))
	}
	if result.Notified != 1 {
		t.Errorf("Notified = %d, want 1", result.Notified)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d notifications, want 1", rec.Count())
	}
	// The body must name the new tag so the notification is actionable.
	if !containsSubstring(rec.Messages()[0].Body, "1.27.2") {
		t.Errorf("notification body does not mention the new tag:\n%s", rec.Messages()[0].Body)
	}

	event, err := db.GetEvent(ctx, result.EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if event.Type != apitypes.EventNewTags {
		t.Errorf("event type = %q, want %q", event.Type, apitypes.EventNewTags)
	}

	// The new tag must be in the baseline now, so it is not reported again.
	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if _, ok := known["1.27.2"]; !ok {
		t.Error("the new tag was not added to the baseline")
	}

	// A second identical check reports nothing.
	result, err = engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("third check: %v", err)
	}
	if len(result.NewTags) != 0 {
		t.Errorf("NewTags = %v on a repeat check, want none", result.NewTags)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d notifications after a repeat check, want 1", rec.Count())
	}
}

func TestCheckPatternPrunesVanishedTagsSilently(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.PatternWatch(t, db, testRegistry, testRepository, "1.27.*")
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1", "1.27.2")
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("baseline check: %v", err)
	}

	// A tag disappears upstream.
	fake.SetTags(testRegistry, testRepository, "1.27.0", "1.27.1")
	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if len(result.Pruned) != 1 || result.Pruned[0] != "1.27.2" {
		t.Errorf("Pruned = %v, want [1.27.2]", result.Pruned)
	}
	if result.EventID != "" {
		t.Errorf("EventID = %q, want empty: pruning must be silent", result.EventID)
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications for a pruned tag, want 0", rec.Count())
	}

	known, err := db.KnownTags(ctx, watch.ID)
	if err != nil {
		t.Fatalf("KnownTags: %v", err)
	}
	if _, ok := known["1.27.2"]; ok {
		t.Error("the vanished tag was not pruned from the baseline")
	}
}

func TestCheckFailureEmitsOneEventAndBacksOff(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry,
			Repository: testRepository, Ref: "1.27"})

	first, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	if first.Error == "" {
		t.Error("Error is empty on a failed check")
	}
	if first.EventID == "" {
		t.Error("EventID is empty: the first failure must emit check_failed")
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d failure notifications, want 1", rec.Count())
	}

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", stored.ConsecutiveFailures)
	}
	if stored.LastError == "" {
		t.Error("LastError is empty after a failure")
	}
	if stored.NextAttemptAt == nil {
		t.Fatal("NextAttemptAt = nil after a failure")
	}

	// Second failure: no new event, no new notification.
	second, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("second CheckWatch: %v", err)
	}
	if second.EventID != "" {
		t.Errorf("EventID = %q on the second failure, want empty (deduplicated)", second.EventID)
	}
	if rec.Count() != 1 {
		t.Errorf("sent %d failure notifications after two failures, want 1", rec.Count())
	}

	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Type: apitypes.EventCheckFailed})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Errorf("recorded %d check_failed events, want 1", total)
	}

	// The backoff must grow: the second failure schedules further out.
	after, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if !after.NextAttemptAt.After(*stored.NextAttemptAt) {
		t.Errorf("backoff did not grow: first %s, second %s", stored.NextAttemptAt, after.NextAttemptAt)
	}
}

func TestCheckRecoveryEmitsEventAndNotifies(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository})
	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("failing check: %v", err)
	}
	rec.Reset()

	// The registry recovers.
	fake.DigestErr = map[string]error{}
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(3))

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("recovery check: %v", err)
	}
	if result.Error != "" {
		t.Errorf("Error = %q after recovery, want empty", result.Error)
	}
	// No digest was ever recorded for this watch, so the first successful
	// check is a baseline: it must not report a change from nothing.
	if !result.Baseline {
		t.Error("Baseline = false, want true: the watch never recorded a digest")
	}
	if result.Changed {
		t.Error("Changed = true, want false: there was no previous digest to change from")
	}

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after recovery, want 0", stored.ConsecutiveFailures)
	}
	if stored.LastError != "" {
		t.Errorf("LastError = %q after recovery, want empty", stored.LastError)
	}

	events, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID, Type: apitypes.EventCheckRecovered})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Errorf("recorded %d check_recovered events, want 1", total)
	}
	if rec.Count() != 1 {
		t.Fatalf("sent %d recovery notifications, want 1", rec.Count())
	}
	if !containsSubstring(rec.Messages()[0].Title, "recovered") {
		t.Errorf("recovery notification title = %q, want it to mention recovery", rec.Messages()[0].Title)
	}
}

func TestCheckFailureNotificationSuppressed(t *testing.T) {
	engine, db, fake, _, rec := newEngine(t)
	ctx := context.Background()

	watch, err := db.CreateWatch(ctx, store.CreateWatchInput{
		Image:           testRegistry + "/" + testRepository + ":1.27",
		Registry:        testRegistry,
		Repository:      testRepository,
		Kind:            apitypes.WatchKindTag,
		Ref:             "1.27",
		Enabled:         true,
		NotifyOnFailure: false,
	})
	if err != nil {
		t.Fatalf("CreateWatch: %v", err)
	}
	fake.SetDigestErr(testRegistry, testRepository, "1.27",
		&registry.Error{Kind: registry.KindTransient, Registry: testRegistry, Repository: testRepository})

	result, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule)
	if err != nil {
		t.Fatalf("CheckWatch: %v", err)
	}
	// The event is still recorded, only delivery is suppressed.
	if result.EventID == "" {
		t.Error("EventID is empty: the failure event must still be recorded")
	}
	if rec.Count() != 0 {
		t.Errorf("sent %d notifications with notify_on_failure=false, want 0", rec.Count())
	}
}

func TestCheckWatchSerializesPerWatch(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))

	const goroutines = 8
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = engine.CheckWatch(ctx, watch.ID, TriggerManual)
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// Concurrent checks must not produce duplicate events: the first records
	// the baseline, the rest see no change.
	_, total, err := db.ListEvents(ctx, store.EventFilter{WatchID: watch.ID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 0 {
		t.Errorf("recorded %d events across concurrent checks of an unchanged image, want 0", total)
	}

	stored, err := db.GetWatch(ctx, watch.ID)
	if err != nil {
		t.Fatalf("GetWatch: %v", err)
	}
	if stored.LastDigest != testutil.Digest(1) {
		t.Errorf("LastDigest = %q, want %q", stored.LastDigest, testutil.Digest(1))
	}
}

func TestCheckWatchUnknownID(t *testing.T) {
	engine, _, _, _, _ := newEngine(t)
	if _, err := engine.CheckWatch(context.Background(), "w_missing", TriggerManual); err == nil {
		t.Error("CheckWatch on an unknown watch: expected an error")
	}
}

func TestCheckUsesStoredCredentials(t *testing.T) {
	engine, db, fake, _, _ := newEngine(t)
	ctx := context.Background()

	if _, err := db.PutCredentials(ctx, testRegistry, "user", "pass", apitypes.CredentialBasic); err != nil {
		t.Fatalf("PutCredentials: %v", err)
	}
	watch := testutil.Watch(t, db, testRegistry, testRepository, "1.27", nil)
	fake.SetDigest(testRegistry, testRepository, "1.27", testutil.Digest(1))

	if _, err := engine.CheckWatch(ctx, watch.ID, TriggerSchedule); err != nil {
		t.Fatalf("CheckWatch with stored credentials: %v", err)
	}
}

func TestBuildMessageContent(t *testing.T) {
	watch := &store.WatchRecord{
		ID:         "w_test",
		Image:      "ghcr.io/owner/app:v1",
		Registry:   "ghcr.io",
		Repository: "owner/app",
		Kind:       apitypes.WatchKindTag,
		Ref:        "v1",
	}
	event := &store.EventRecord{
		ID:        "e_test",
		WatchID:   "w_test",
		Type:      apitypes.EventDigestChanged,
		Tag:       "v1",
		OldDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NewDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt: nowFixed(),
	}

	msg := BuildMessage(watch, event)
	if msg.Title == "" {
		t.Error("Title is empty")
	}
	if !containsSubstring(msg.Title, "ghcr.io/owner/app:v1") {
		t.Errorf("Title = %q, want it to name the image", msg.Title)
	}
	// The tag must appear once, not twice: the image already carries it.
	if strings.Count(msg.Title, "v1") != 1 {
		t.Errorf("Title = %q, want the ref to appear exactly once", msg.Title)
	}
	if msg.URL == "" {
		t.Error("URL is empty for an event with a known repository")
	}
	if msg.Priority != "default" {
		t.Errorf("Priority = %q, want default for a digest change", msg.Priority)
	}
	if !containsSubstring(msg.Body, "v1") {
		t.Errorf("Body does not mention the ref:\n%s", msg.Body)
	}

	// Failures are high priority; recoveries are low.
	if got := Priority(apitypes.EventCheckFailed); got != "high" {
		t.Errorf("Priority(check_failed) = %q, want high", got)
	}
	if got := Priority(apitypes.EventCheckRecovered); got != "low" {
		t.Errorf("Priority(check_recovered) = %q, want low", got)
	}
}

func TestBuildMessageNewTagsBody(t *testing.T) {
	watch := &store.WatchRecord{
		ID: "w_test", Image: "ghcr.io/owner/app", Registry: "ghcr.io",
		Repository: "owner/app", Kind: apitypes.WatchKindPattern, Ref: "v1.*",
	}
	// The detail map has been through JSON, so numbers arrive as float64 and
	// nested objects as map[string]any.
	event := &store.EventRecord{
		ID: "e_test", WatchID: "w_test", Type: apitypes.EventNewTags, Tag: "v1.*",
		Detail: map[string]any{
			"pattern": "v1.*",
			"count":   float64(2),
			"new_tags": []any{
				map[string]any{"tag": "v1.2.0", "digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
				map[string]any{"tag": "v1.3.0", "digest": ""},
			},
		},
		CreatedAt: nowFixed(),
	}

	msg := BuildMessage(watch, event)
	for _, want := range []string{"v1.2.0", "v1.3.0"} {
		if !containsSubstring(msg.Body, want) {
			t.Errorf("Body does not mention %s:\n%s", want, msg.Body)
		}
	}
	if !containsSubstring(msg.Body, "sha256:cccccccccccc") {
		t.Errorf("Body does not carry the shortened digest:\n%s", msg.Body)
	}
}

func TestNewTagsOfToleratesOddDetail(t *testing.T) {
	cases := []*store.EventRecord{
		{Detail: nil},
		{Detail: map[string]any{}},
		{Detail: map[string]any{"new_tags": "not a list"}},
		{Detail: map[string]any{"new_tags": []any{"plain-string", 42}}},
	}
	for i, event := range cases {
		got := newTagsOf(event)
		if len(got) == 0 && i == 3 {
			t.Errorf("case %d: expected the plain string tag to survive, got %v", i, got)
		}
	}
}

func TestClassifyCause(t *testing.T) {
	cases := []struct {
		err  error
		want registry.ErrorKind
	}{
		{&registry.Error{Kind: registry.KindUnauthorized}, registry.KindUnauthorized},
		{&registry.Error{Kind: registry.KindNotFound}, registry.KindNotFound},
		{&registry.Error{Kind: registry.KindTransient}, registry.KindTransient},
		{context.DeadlineExceeded, registry.KindUnknown},
	}
	for _, tc := range cases {
		if got := classifyCause(tc.err); got != tc.want {
			t.Errorf("classifyCause(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestTitleDoesNotDuplicateTheRef covers the ntfy/email subject for a tag
// watch, whose image already ends with the tag.
func TestTitleDoesNotDuplicateTheRef(t *testing.T) {
	cases := []struct {
		name  string
		watch *store.WatchRecord
		event *store.EventRecord
		want  string
	}{
		{
			name: "tag watch image carries the ref",
			watch: &store.WatchRecord{
				Image: "ghcr.io/owner/app:v1", Registry: "ghcr.io",
				Repository: "owner/app", Kind: apitypes.WatchKindTag, Ref: "v1",
			},
			event: &store.EventRecord{Type: apitypes.EventDigestChanged, Tag: "v1"},
			want:  "digest changed: ghcr.io/owner/app:v1",
		},
		{
			name: "image without the ref gets it appended",
			watch: &store.WatchRecord{
				Image: "ghcr.io/owner/app", Registry: "ghcr.io",
				Repository: "owner/app", Kind: apitypes.WatchKindTag, Ref: "v2",
			},
			event: &store.EventRecord{Type: apitypes.EventDigestChanged, Tag: "v2"},
			want:  "digest changed: ghcr.io/owner/app:v2",
		},
		{
			name: "pattern event names the repository",
			watch: &store.WatchRecord{
				Image: "ghcr.io/owner/app:latest", Registry: "ghcr.io",
				Repository: "owner/app", Kind: apitypes.WatchKindPattern, Ref: "v1.*",
			},
			event: &store.EventRecord{Type: apitypes.EventNewTags, Tag: "v1.*"},
			want:  "new tags: ghcr.io/owner/app (v1.*)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.watch, tc.event); got != tc.want {
				t.Errorf("Title = %q, want %q", got, tc.want)
			}
		})
	}
}

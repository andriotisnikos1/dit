package check

import (
	"testing"
	"time"
)

func TestDiffTagBaselineIsSilent(t *testing.T) {
	outcome := DiffTag(true, "", "sha256:aaa")
	if !outcome.Baseline {
		t.Error("Baseline = false on the first check")
	}
	if outcome.Changed {
		t.Error("Changed = true on the first check; a new watch must stay silent")
	}
	if outcome.Digest != "sha256:aaa" {
		t.Errorf("Digest = %q, want the observed digest", outcome.Digest)
	}
}

func TestDiffTagDetectsDrift(t *testing.T) {
	outcome := DiffTag(false, "sha256:old", "sha256:new")
	if outcome.Baseline {
		t.Error("Baseline = true on a subsequent check")
	}
	if !outcome.Changed {
		t.Error("Changed = false, want true for a new digest")
	}
	if outcome.OldDigest != "sha256:old" || outcome.Digest != "sha256:new" {
		t.Errorf("outcome = %+v, want old/new digests populated", outcome)
	}
}

func TestDiffTagNoChange(t *testing.T) {
	outcome := DiffTag(false, "sha256:same", "sha256:same")
	if outcome.Changed {
		t.Error("Changed = true for an identical digest")
	}
}

func TestDiffPatternBaselineRecordsEverythingSilently(t *testing.T) {
	listed := []string{"v1.0.0", "v1.1.0", "latest"}
	outcome := DiffPattern("v1.*", listed, map[string]string{}, true)

	if !outcome.Baseline {
		t.Error("Baseline = false on the first check")
	}
	if len(outcome.NewTags) != 0 {
		t.Errorf("NewTags = %v on the first check, want none reported", outcome.NewTags)
	}
	want := []string{"v1.0.0", "v1.1.0"}
	if len(outcome.Matched) != len(want) {
		t.Fatalf("Matched = %v, want %v", outcome.Matched, want)
	}
	for i := range want {
		if outcome.Matched[i] != want[i] {
			t.Errorf("Matched[%d] = %q, want %q", i, outcome.Matched[i], want[i])
		}
	}
	if len(outcome.Pruned) != 0 {
		t.Errorf("Pruned = %v on the first check, want none", outcome.Pruned)
	}
}

func TestDiffPatternReportsNewTags(t *testing.T) {
	known := map[string]string{"v1.0.0": "sha256:one", "v1.1.0": "sha256:two"}
	listed := []string{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0", "latest"}

	outcome := DiffPattern("v1.*", listed, known, false)
	if outcome.Baseline {
		t.Error("Baseline = true on a subsequent check")
	}
	if len(outcome.NewTags) != 1 || outcome.NewTags[0] != "v1.2.0" {
		t.Errorf("NewTags = %v, want [v1.2.0]", outcome.NewTags)
	}
	if len(outcome.Matched) != 3 {
		t.Errorf("Matched = %v, want 3 entries", outcome.Matched)
	}
	if len(outcome.Pruned) != 0 {
		t.Errorf("Pruned = %v, want none", outcome.Pruned)
	}
}

func TestDiffPatternPrunesVanishedTags(t *testing.T) {
	known := map[string]string{"v1.0.0": "a", "v1.1.0": "b", "v1.9.0": "c"}
	listed := []string{"v1.0.0", "v1.1.0"}

	outcome := DiffPattern("v1.*", listed, known, false)
	if len(outcome.NewTags) != 0 {
		t.Errorf("NewTags = %v, want none", outcome.NewTags)
	}
	if len(outcome.Pruned) != 1 || outcome.Pruned[0] != "v1.9.0" {
		t.Errorf("Pruned = %v, want [v1.9.0]", outcome.Pruned)
	}
}

func TestDiffPatternSortsNewTags(t *testing.T) {
	known := map[string]string{}
	listed := []string{"v1.3.0", "v1.1.0", "v1.2.0"}

	outcome := DiffPattern("v1.*", listed, known, false)
	want := []string{"v1.1.0", "v1.2.0", "v1.3.0"}
	if len(outcome.NewTags) != len(want) {
		t.Fatalf("NewTags = %v, want %v", outcome.NewTags, want)
	}
	for i := range want {
		if outcome.NewTags[i] != want[i] {
			t.Errorf("NewTags[%d] = %q, want %q", i, outcome.NewTags[i], want[i])
		}
	}
}

func TestDiffPatternHandlesEmptyUpstream(t *testing.T) {
	known := map[string]string{"v1.0.0": "a"}
	outcome := DiffPattern("v1.*", nil, known, false)
	if len(outcome.Matched) != 0 {
		t.Errorf("Matched = %v, want empty", outcome.Matched)
	}
	if len(outcome.NewTags) != 0 {
		t.Errorf("NewTags = %v, want empty", outcome.NewTags)
	}
	if len(outcome.Pruned) != 1 {
		t.Errorf("Pruned = %v, want the vanished tag", outcome.Pruned)
	}
}

func TestBackoffProgression(t *testing.T) {
	interval := 15 * time.Minute
	max := 24 * time.Hour

	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 15 * time.Minute},
		{2, 30 * time.Minute},
		{3, time.Hour},
		{4, 2 * time.Hour},
		{5, 4 * time.Hour},
		{6, 8 * time.Hour},
		{7, 16 * time.Hour},
		{8, 24 * time.Hour},  // capped
		{20, 24 * time.Hour}, // still capped
	}
	for _, tc := range cases {
		got := Backoff(interval, tc.failures, max)
		if got != tc.want {
			t.Errorf("Backoff(15m, %d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func TestBackoffEdgeCases(t *testing.T) {
	// Zero or negative failure counts behave like the first failure.
	if got := Backoff(time.Minute, 0, time.Hour); got != time.Minute {
		t.Errorf("Backoff(1m, 0) = %s, want 1m", got)
	}
	// A non-positive interval falls back to one minute rather than zero.
	if got := Backoff(0, 1, time.Hour); got != time.Minute {
		t.Errorf("Backoff(0, 1) = %s, want 1m", got)
	}
	// A non-positive cap falls back to 24h.
	if got := Backoff(time.Hour, 100, 0); got != 24*time.Hour {
		t.Errorf("Backoff(1h, 100, 0) = %s, want 24h", got)
	}
}

func TestJitterStaysWithinBounds(t *testing.T) {
	base := 15 * time.Minute
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		got := Jitter(base, 0.10, frac)
		low := time.Duration(float64(base) * 0.90)
		high := time.Duration(float64(base) * 1.10)
		if got < low || got > high {
			t.Errorf("Jitter(15m, 10%%, %v) = %s, outside [%s, %s]", frac, got, low, high)
		}
	}
	// A zero fraction is the lower bound and a near-one fraction the upper.
	if got := Jitter(base, 0.10, 0); got != low(base) {
		t.Errorf("Jitter at frac=0 = %s, want %s", got, low(base))
	}
	// No jitter configured returns the input untouched.
	if got := Jitter(base, 0, 0.9); got != base {
		t.Errorf("Jitter with pct=0 = %s, want %s", got, base)
	}
}

func low(d time.Duration) time.Duration {
	return time.Duration(float64(d) * 0.90)
}

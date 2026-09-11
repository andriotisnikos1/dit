package check

import (
	"sort"
	"time"
)

// TagOutcome is the result of diffing a pinned tag's digest.
type TagOutcome struct {
	// Baseline is true when this was the watch's first check: the digest is
	// stored and nothing is reported.
	Baseline bool
	// Changed is true when the digest differs from the stored one.
	Changed bool
	// Digest is the digest observed now.
	Digest string
	// OldDigest is the digest stored before this check.
	OldDigest string
}

// DiffTag compares a freshly resolved digest against the stored one.
//
// The first check of a watch only records a baseline and stays silent: a watch
// must not fire on creation.
func DiffTag(baseline bool, lastDigest, newDigest string) TagOutcome {
	if baseline {
		return TagOutcome{Baseline: true, Digest: newDigest}
	}
	return TagOutcome{
		Changed:   newDigest != lastDigest,
		Digest:    newDigest,
		OldDigest: lastDigest,
	}
}

// PatternOutcome is the result of diffing a pattern watch's tag list.
type PatternOutcome struct {
	// Baseline is true when this was the watch's first check: every matching
	// tag is recorded and nothing is reported.
	Baseline bool
	// Matched are the tags matching the pattern, sorted.
	Matched []string
	// NewTags are matching tags with no baseline row, sorted.
	NewTags []string
	// Pruned are baseline tags that no longer match or no longer exist.
	Pruned []string
}

// DiffPattern diffs the upstream tag list against the stored baseline.
//
// New tags are reported; tags that disappear are pruned silently, because a
// deleted tag is not actionable and would otherwise spam the operator.
func DiffPattern(pattern string, listed []string, known map[string]string, baseline bool) PatternOutcome {
	matched := MatchTags(pattern, listed)
	sort.Strings(matched)

	out := PatternOutcome{Baseline: baseline, Matched: matched}
	if baseline {
		// Everything is new on the first check; nothing is reported.
		out.NewTags = nil
		return out
	}

	matchedSet := make(map[string]struct{}, len(matched))
	for _, tag := range matched {
		matchedSet[tag] = struct{}{}
		if _, ok := known[tag]; !ok {
			out.NewTags = append(out.NewTags, tag)
		}
	}

	out.Pruned = make([]string, 0)
	for tag := range known {
		if _, ok := matchedSet[tag]; !ok {
			out.Pruned = append(out.Pruned, tag)
		}
	}
	sort.Strings(out.Pruned)
	return out
}

// MatchTags filters tags with a glob pattern. Matching is case-sensitive,
// per path.Match semantics.
func MatchTags(pattern string, tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if globMatch(pattern, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// Backoff computes the delay before the next attempt after a run of failures.
//
// The delay doubles with each consecutive failure, starting from the poll
// interval and capped at max. The failure count is the count *after* the
// failure being scheduled, so the first failure waits one interval.
func Backoff(interval time.Duration, failures int, max time.Duration) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	if max <= 0 {
		max = 24 * time.Hour
	}
	if failures < 1 {
		failures = 1
	}

	delay := interval
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		return max
	}
	return delay
}

// MaxBackoff is the cap applied to failure backoff.
const MaxBackoff = 24 * time.Hour

// Jitter spreads a duration by ±pct to avoid a thundering herd. frac is a
// value in [0,1) — callers pass a random number.
func Jitter(d time.Duration, pct float64, frac float64) time.Duration {
	if pct <= 0 {
		return d
	}
	if frac < 0 {
		frac = 0
	}
	if frac >= 1 {
		frac = 0.999999
	}
	// Map frac onto [-pct, +pct].
	offset := (frac*2 - 1) * pct
	return time.Duration(float64(d) * (1 + offset))
}

// JitterFraction is the ±10% spread the scheduler applies to its interval.
const JitterFraction = 0.10

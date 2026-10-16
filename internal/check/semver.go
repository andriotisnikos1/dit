package check

import (
	"github.com/Masterminds/semver/v3"
)

// SemverCandidate is the highest semver tag found among a watch's matches.
type SemverCandidate struct {
	// Tag is the raw upstream tag, e.g. "v1.2.3".
	Tag string
	// Version is the canonical parsed version, e.g. "1.2.3".
	Version string
}

// HighestSemverTag returns the highest semver version among tags. Tags that
// do not parse as semver are ignored; stableOnly skips prerelease versions.
// ok is false when no tag qualifies. NewVersion (not StrictNewVersion) is
// deliberate: it accepts the "v" prefix and partial versions like "1.27",
// both of which are common in image tags.
func HighestSemverTag(tags []string, stableOnly bool) (SemverCandidate, bool) {
	var (
		best    *semver.Version
		bestTag string
	)
	for _, tag := range tags {
		v, err := semver.NewVersion(tag)
		if err != nil {
			continue
		}
		if stableOnly && v.Prerelease() != "" {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
			bestTag = tag
		}
	}
	if best == nil {
		return SemverCandidate{}, false
	}
	return SemverCandidate{Tag: bestTag, Version: best.String()}, true
}

// SemverBump reports whether candidate is a version increase over stored.
// An unparseable stored value (corruption) counts as "no bump"; the caller
// treats an empty stored value as "seed, don't fire".
func SemverBump(stored string, candidate SemverCandidate) bool {
	if stored == "" {
		return false
	}
	oldV, err := semver.NewVersion(stored)
	if err != nil {
		return false
	}
	newV, err := semver.NewVersion(candidate.Version)
	if err != nil {
		return false
	}
	return newV.GreaterThan(oldV)
}

package testutil

import (
	"math/rand"
	"time"
)

// newDeterministicRand returns a fixed-source rand so jitter never makes a test
// flaky.
func newDeterministicRand() *rand.Rand {
	return rand.New(rand.NewSource(1))
}

// FixedClock returns a clock that never advances, for deterministic
// scheduling assertions.
func FixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

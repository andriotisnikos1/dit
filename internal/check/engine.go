// Package check is the check engine: it schedules watch checks, diffs the
// results, records events and delivers notifications.
//
// Design notes:
//
//   - One scheduler goroutine selects due watches and fans them out over a
//     bounded worker pool. A watch that is slow can never block the tick.
//   - Checks are strictly serialized per watch, so a manual `dit watch check`
//     landing mid-tick cannot produce duplicate notifications.
//   - A delivery failure is recorded against the notification row and never
//     affects the stored check result.
package check

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// Options configures an Engine.
type Options struct {
	Store    *store.DB
	Registry registry.Client
	Notifier notify.Builder
	Logger   *slog.Logger

	// Interval is the global poll interval applied to every watch.
	Interval time.Duration
	// Concurrency bounds how many watches are checked at once.
	Concurrency int
	// Timeout bounds a single check.
	Timeout time.Duration
	// NotifyAttempts is how many times a delivery is attempted.
	NotifyAttempts int
	// NotifyBackoff is the delay between delivery attempts.
	NotifyBackoff time.Duration

	// Now is the clock, overridable in tests.
	Now func() time.Time
	// Rand is the jitter source, overridable in tests.
	Rand *rand.Rand
}

// Engine runs the check loop.
type Engine struct {
	store    *store.DB
	registry registry.Client
	notifier notify.Builder
	log      *slog.Logger

	interval       time.Duration
	concurrency    int
	timeout        time.Duration
	notifyAttempts int
	notifyBackoff  time.Duration

	now  func() time.Time
	rand *rand.Rand

	// Per-watch serialization.
	locksMu sync.Mutex
	locks   map[string]*sync.Mutex

	ticks    atomic.Uint64
	lastTick atomic.Pointer[time.Time]
	nextTick atomic.Pointer[time.Time]
}

// New builds an Engine.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, errors.New("check: store is required")
	}
	if opts.Registry == nil {
		return nil, errors.New("check: registry client is required")
	}
	if opts.Notifier == nil {
		opts.Notifier = notify.DefaultBuilder{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Minute
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.NotifyAttempts < 1 {
		opts.NotifyAttempts = 3
	}
	if opts.NotifyBackoff <= 0 {
		opts.NotifyBackoff = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return &Engine{
		store:          opts.Store,
		registry:       opts.Registry,
		notifier:       opts.Notifier,
		log:            opts.Logger,
		interval:       opts.Interval,
		concurrency:    opts.Concurrency,
		timeout:        opts.Timeout,
		notifyAttempts: opts.NotifyAttempts,
		notifyBackoff:  opts.NotifyBackoff,
		now:            opts.Now,
		rand:           opts.Rand,
		locks:          map[string]*sync.Mutex{},
	}, nil
}

// Interval returns the configured poll interval.
func (e *Engine) Interval() time.Duration { return e.interval }

// Concurrency returns the configured worker pool size.
func (e *Engine) Concurrency() int { return e.concurrency }

// Ticks returns how many scheduler ticks have completed.
func (e *Engine) Ticks() uint64 { return e.ticks.Load() }

// LastTick returns when the last tick started, if any.
func (e *Engine) LastTick() *time.Time { return e.lastTick.Load() }

// NextTick returns when the next tick is scheduled, if any.
func (e *Engine) NextTick() *time.Time { return e.nextTick.Load() }

// Run drives the scheduler until ctx is cancelled. It returns when the
// context is done.
func (e *Engine) Run(ctx context.Context) error {
	e.log.Info("check engine started",
		"interval", e.interval.String(),
		"concurrency", e.concurrency,
		"timeout", e.timeout.String())

	for {
		delay := Jitter(e.interval, JitterFraction, e.rand.Float64())
		next := e.now().Add(delay)
		e.nextTick.Store(&next)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			e.nextTick.Store(nil)
			e.log.Info("check engine stopped")
			return ctx.Err()
		case <-timer.C:
		}

		tickStart := e.now()
		e.lastTick.Store(&tickStart)
		e.tick(ctx)
		e.ticks.Add(1)
	}
}

// tick performs one scheduling pass: select due watches and check them.
func (e *Engine) tick(ctx context.Context) {
	due, err := e.store.DueWatches(ctx, e.now(), 0)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		e.log.Error("select due watches", "error", err)
		return
	}
	if len(due) == 0 {
		e.log.Debug("tick: no watches due")
		return
	}
	e.log.Debug("tick: checking watches", "count", len(due))

	sem := make(chan struct{}, e.concurrency)
	var wg sync.WaitGroup
	for _, w := range due {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if _, err := e.CheckWatch(ctx, w.ID, TriggerSchedule); err != nil && ctx.Err() == nil {
				e.log.Warn("check failed", "watch", w.ID, "image", w.Image, "error", err)
			}
		}()
	}
	wg.Wait()
}

// Trigger records why a check ran.
type Trigger string

const (
	// TriggerSchedule is a check run by the scheduler.
	TriggerSchedule Trigger = "schedule"
	// TriggerManual is a check forced through the API.
	TriggerManual Trigger = "manual"
)

// CheckWatch runs one check for a watch, serialized against any other check of
// the same watch, and returns the outcome.
//
// The returned error is non-nil only for failures that prevented the check from
// running at all (unknown watch, database error). Registry failures are
// recorded and reported through the result's Error field, because they are a
// normal outcome the operator wants to see.
func (e *Engine) CheckWatch(ctx context.Context, watchID string, trigger Trigger) (*apitypes.CheckResult, error) {
	unlock := e.lockWatch(watchID)
	defer unlock()

	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return nil, fmt.Errorf("load watch %s: %w", watchID, err)
	}

	// Bound the check itself; a hung registry must not hold the worker.
	checkCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	switch watch.Kind {
	case apitypes.WatchKindTag:
		return e.checkTag(checkCtx, watch, trigger)
	case apitypes.WatchKindPattern:
		return e.checkPattern(checkCtx, watch, trigger)
	default:
		return nil, fmt.Errorf("watch %s has unknown kind %q", watch.ID, watch.Kind)
	}
}

// checkTag resolves a pinned tag and reports digest drift.
func (e *Engine) checkTag(ctx context.Context, watch *store.WatchRecord, trigger Trigger) (*apitypes.CheckResult, error) {
	result := &apitypes.CheckResult{WatchID: watch.ID, Tag: watch.Ref}

	ref, err := registry.ParseTagRef(watch.Registry+"/"+watch.Repository, watch.Ref)
	if err != nil {
		return e.recordFailure(ctx, watch, result, fmt.Errorf("parse tag reference: %w", err))
	}
	auth, err := e.authenticator(ctx, watch.Registry)
	if err != nil {
		return e.recordFailure(ctx, watch, result, err)
	}

	digest, err := e.registry.ResolveDigest(ctx, ref, auth)
	if err != nil {
		return e.recordFailure(ctx, watch, result, err)
	}

	outcome := DiffTag(!watch.HasBaseline(), watch.LastDigest, digest.String())
	result.NewDigest = outcome.Digest
	result.Baseline = outcome.Baseline
	result.OldDigest = outcome.OldDigest
	result.Changed = outcome.Changed

	recovered := watch.ConsecutiveFailures > 0
	if err := e.store.MarkWatchSuccess(ctx, watch.ID, outcome.Digest, e.nextAttempt()); err != nil {
		return nil, err
	}
	if recovered {
		e.emitRecovered(ctx, watch)
	}

	if outcome.Baseline {
		e.log.Debug("baseline recorded", "watch", watch.ID, "image", watch.Image,
			"ref", watch.Ref, "digest", outcome.Digest)
		return result, nil
	}
	if !outcome.Changed {
		e.log.Debug("no change", "watch", watch.ID, "image", watch.Image, "digest", outcome.Digest)
		return result, nil
	}

	event, err := e.store.CreateEvent(ctx, store.CreateEventInput{
		WatchID:   watch.ID,
		Type:      apitypes.EventDigestChanged,
		Tag:       watch.Ref,
		OldDigest: outcome.OldDigest,
		NewDigest: outcome.Digest,
	})
	if err != nil {
		return nil, err
	}
	result.EventID = event.ID
	e.log.Info("digest changed", "watch", watch.ID, "image", watch.Image,
		"ref", watch.Ref, "old", outcome.OldDigest, "new", outcome.Digest)
	result.Notified = e.deliver(ctx, watch, event)
	return result, nil
}

// checkPattern lists tags, reports new matches and prunes vanished ones.
func (e *Engine) checkPattern(ctx context.Context, watch *store.WatchRecord, trigger Trigger) (*apitypes.CheckResult, error) {
	result := &apitypes.CheckResult{WatchID: watch.ID, Tag: watch.Ref}

	repo, err := registry.ParseRepositoryRef(watch.Registry, watch.Repository)
	if err != nil {
		return e.recordFailure(ctx, watch, result, fmt.Errorf("parse repository reference: %w", err))
	}
	auth, err := e.authenticator(ctx, watch.Registry)
	if err != nil {
		return e.recordFailure(ctx, watch, result, err)
	}

	listed, err := e.registry.ListTags(ctx, repo, auth)
	if err != nil {
		return e.recordFailure(ctx, watch, result, err)
	}

	known, err := e.store.KnownTags(ctx, watch.ID)
	if err != nil {
		return nil, err
	}

	// The baseline is the set of recorded tags, not the fact that a check ran:
	// a pattern watch whose first checks all failed has no baseline yet and
	// must record one instead of reporting every matching tag as new.
	outcome := DiffPattern(watch.Ref, listed, known, len(known) == 0)
	result.Matched = len(outcome.Matched)
	result.Baseline = outcome.Baseline

	// Baseline rows are written before the event so that a delivery failure
	// cannot cause the same tags to be reported again on the next tick.
	resolved := make([]apitypes.TagDigest, 0, len(outcome.Matched))
	if outcome.Baseline {
		for _, tag := range outcome.Matched {
			resolved = append(resolved, apitypes.TagDigest{Tag: tag})
		}
	} else {
		for _, tag := range outcome.NewTags {
			resolved = append(resolved, apitypes.TagDigest{
				Tag:    tag,
				Digest: e.resolveTagDigest(ctx, repo, tag, auth),
			})
		}
	}

	// Record baselines and refresh digests of known tags.
	all := make([]apitypes.TagDigest, 0, len(outcome.Matched))
	byTag := map[string]string{}
	for _, t := range resolved {
		byTag[t.Tag] = t.Digest
	}
	for _, tag := range outcome.Matched {
		all = append(all, apitypes.TagDigest{Tag: tag, Digest: byTag[tag]})
	}
	if err := e.store.UpsertWatchTags(ctx, watch.ID, all); err != nil {
		return nil, err
	}

	pruned, err := e.store.PruneWatchTags(ctx, watch.ID, outcome.Matched)
	if err != nil {
		return nil, err
	}
	if pruned > 0 {
		result.Pruned = outcome.Pruned
		e.log.Debug("pruned vanished tags", "watch", watch.ID, "count", pruned)
	}

	recovered := watch.ConsecutiveFailures > 0
	if err := e.store.MarkWatchSuccess(ctx, watch.ID, "", e.nextAttempt()); err != nil {
		return nil, err
	}
	if recovered {
		e.emitRecovered(ctx, watch)
	}

	if outcome.Baseline {
		e.log.Debug("pattern baseline recorded", "watch", watch.ID, "image", watch.Image,
			"pattern", watch.Ref, "tags", len(outcome.Matched))
		return result, nil
	}
	if len(outcome.NewTags) == 0 {
		e.log.Debug("no new tags", "watch", watch.ID, "image", watch.Image, "matched", len(outcome.Matched))
		return result, nil
	}

	detail := map[string]any{
		"pattern":  watch.Ref,
		"new_tags": resolved,
		"count":    len(resolved),
	}
	event, err := e.store.CreateEvent(ctx, store.CreateEventInput{
		WatchID: watch.ID,
		Type:    apitypes.EventNewTags,
		Tag:     watch.Ref,
		Detail:  detail,
	})
	if err != nil {
		return nil, err
	}
	result.EventID = event.ID
	result.NewTags = resolved
	e.log.Info("new tags", "watch", watch.ID, "image", watch.Image,
		"pattern", watch.Ref, "count", len(resolved))
	result.Notified = e.deliver(ctx, watch, event)
	return result, nil
}

// resolveTagDigest resolves a single tag's digest for a new_tags event. A
// failure here is not fatal: the tag is still reported, just without a digest.
func (e *Engine) resolveTagDigest(ctx context.Context, repo name.Repository, tag string, auth authn.Authenticator) string {
	ref, err := registry.ParseTagRef(repo.RegistryStr()+"/"+repo.RepositoryStr(), tag)
	if err != nil {
		return ""
	}
	digest, err := e.registry.ResolveDigest(ctx, ref, auth)
	if err != nil {
		e.log.Debug("resolve new tag digest", "tag", tag, "error", err)
		return ""
	}
	return digest.String()
}

// recordFailure stores a failed check and emits check_failed once per outage.
func (e *Engine) recordFailure(ctx context.Context, watch *store.WatchRecord, result *apitypes.CheckResult, cause error) (*apitypes.CheckResult, error) {
	msg := cause.Error()
	result.Error = msg

	// The failure count *after* this failure drives the backoff.
	failures := watch.ConsecutiveFailures + 1
	next := e.now().Add(Backoff(e.interval, failures, MaxBackoff))
	if err := e.store.MarkWatchFailure(ctx, watch.ID, msg, next); err != nil {
		return nil, err
	}

	kind := classifyCause(cause)
	e.log.Warn("check failed", "watch", watch.ID, "image", watch.Image,
		"failures", failures, "kind", kind, "error", msg)

	// Only the first failure of a run emits an event: a watch that stays
	// broken must not spam the operator every interval.
	if watch.ConsecutiveFailures == 0 {
		event, err := e.store.CreateEvent(ctx, store.CreateEventInput{
			WatchID: watch.ID,
			Type:    apitypes.EventCheckFailed,
			Tag:     watch.Ref,
			Detail:  map[string]any{"error": msg, "kind": string(kind)},
		})
		if err != nil {
			return nil, err
		}
		result.EventID = event.ID
		if watch.NotifyOnFailure {
			result.Notified = e.deliver(ctx, watch, event)
		} else {
			e.log.Debug("failure notification suppressed", "watch", watch.ID)
		}
	}
	return result, nil
}

// emitRecovered records the return to health after a run of failures.
func (e *Engine) emitRecovered(ctx context.Context, watch *store.WatchRecord) {
	event, err := e.store.CreateEvent(ctx, store.CreateEventInput{
		WatchID: watch.ID,
		Type:    apitypes.EventCheckRecovered,
		Tag:     watch.Ref,
		Detail:  map[string]any{"consecutive_failures": watch.ConsecutiveFailures},
	})
	if err != nil {
		e.log.Error("record recovery event", "watch", watch.ID, "error", err)
		return
	}
	e.log.Info("check recovered", "watch", watch.ID, "image", watch.Image,
		"failures", watch.ConsecutiveFailures)
	if watch.NotifyOnFailure {
		e.deliver(ctx, watch, event)
	}
}

// nextAttempt schedules the next check one (jittered) interval from now.
func (e *Engine) nextAttempt() time.Time {
	delay := Jitter(e.interval, JitterFraction, e.rand.Float64())
	return e.now().Add(delay)
}

// lockWatch returns a function that releases the per-watch lock.
func (e *Engine) lockWatch(watchID string) func() {
	e.locksMu.Lock()
	mu, ok := e.locks[watchID]
	if !ok {
		mu = &sync.Mutex{}
		e.locks[watchID] = mu
	}
	e.locksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

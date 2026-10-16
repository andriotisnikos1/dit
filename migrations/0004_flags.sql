-- 0004_flags.sql — behaviour flags: per-watch overrides, pending notification
-- counters, semver tracking state, and the global settings table.
--
-- Purely additive: no CHECK constraints are touched, so no table rebuild (see
-- 0003 for the rebuild this deliberately avoids).

-- Explicit per-watch flag overrides, as a JSON object of typed values, e.g.
-- {"history": 5}. Empty string = no overrides; the watch then uses the
-- global values or the flag defaults.
ALTER TABLE watches ADD COLUMN flags_json TEXT NOT NULL DEFAULT '';

-- Threshold-batching counters. pending_notify_count counts undelivered
-- update events (digest_changed / new_tags); pending_semver_count counts
-- undelivered semver bumps. Each resets to zero when its notification fires.
ALTER TABLE watches ADD COLUMN pending_notify_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE watches ADD COLUMN pending_semver_count INTEGER NOT NULL DEFAULT 0;

-- Semver tracking state for pattern watches. latest_semver is the canonical
-- parsed version string (e.g. "1.2.3"); latest_semver_tag is the raw upstream
-- tag (e.g. "v1.2.3").
ALTER TABLE watches ADD COLUMN latest_semver TEXT NOT NULL DEFAULT '';
ALTER TABLE watches ADD COLUMN latest_semver_tag TEXT NOT NULL DEFAULT '';

-- Global flag values. Only explicitly-set values live here; the effective
-- value of a flag is: watch override > global value > flag default.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
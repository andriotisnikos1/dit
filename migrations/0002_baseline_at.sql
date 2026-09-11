-- 0002_baseline_at.sql — record when a watch first completed a successful
-- check, instead of inferring it from the data a check happened to produce.
--
-- The previous inference was wrong for pattern watches: a watch created before
-- any matching tag existed recorded no tags, so it re-baselined on every
-- subsequent check and never reported the first tags to appear. An explicit
-- marker also keeps a tag watch that failed before its first success from
-- reporting a phantom digest change from an empty digest.

ALTER TABLE watches ADD COLUMN baseline_at TEXT;

-- Backfill: any watch that has ever succeeded already has a baseline.
UPDATE watches SET baseline_at = last_ok_at WHERE last_ok_at IS NOT NULL;

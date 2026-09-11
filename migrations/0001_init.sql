-- 0001_init.sql — initial dit schema.
--
-- Timestamps are TEXT in the fixed-width UTC layout
-- 2006-01-02T15:04:05.000000000Z07:00 so that lexicographic ordering matches
-- chronological ordering and ORDER BY works without parsing.

CREATE TABLE watches (
    id                   TEXT PRIMARY KEY,
    image                TEXT    NOT NULL,
    registry             TEXT    NOT NULL,
    repository           TEXT    NOT NULL,
    kind                 TEXT    NOT NULL CHECK (kind IN ('tag', 'pattern')),
    ref                  TEXT    NOT NULL,
    enabled              INTEGER NOT NULL DEFAULT 1,
    notify_on_failure    INTEGER NOT NULL DEFAULT 1,
    last_digest          TEXT    NOT NULL DEFAULT '',
    last_checked_at      TEXT,
    last_ok_at           TEXT,
    last_error           TEXT    NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    next_attempt_at      TEXT,
    created_at           TEXT    NOT NULL,
    updated_at           TEXT    NOT NULL
);

-- One watch per (registry, repository, kind, ref).
CREATE UNIQUE INDEX idx_watches_unique ON watches (registry, repository, kind, ref);
CREATE INDEX idx_watches_due ON watches (enabled, next_attempt_at);
CREATE INDEX idx_watches_registry ON watches (registry);

-- Baseline of known tags for pattern watches. A tag row appearing here is what
-- makes a tag "new"; rows are pruned silently when a tag disappears upstream.
CREATE TABLE watch_tags (
    watch_id      TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
    tag           TEXT NOT NULL,
    digest        TEXT NOT NULL DEFAULT '',
    first_seen_at TEXT NOT NULL,
    PRIMARY KEY (watch_id, tag)
);

CREATE TABLE channels (
    id          TEXT PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    type        TEXT    NOT NULL CHECK (type IN ('email', 'ntfy')),
    config_json TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

-- Explicit per-watch subscriptions. A watch with no rows here falls back to
-- the channels flagged is_default.
CREATE TABLE watch_channels (
    watch_id   TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    PRIMARY KEY (watch_id, channel_id)
);

CREATE TABLE registry_creds (
    registry     TEXT PRIMARY KEY,
    username     TEXT NOT NULL DEFAULT '',
    secret_enc   TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'basic',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    last_used_at TEXT,
    last_ok_at   TEXT
);

CREATE TABLE events (
    id          TEXT PRIMARY KEY,
    watch_id    TEXT NOT NULL REFERENCES watches (id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    tag         TEXT NOT NULL DEFAULT '',
    old_digest  TEXT NOT NULL DEFAULT '',
    new_digest  TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE INDEX idx_events_created ON events (created_at DESC);
CREATE INDEX idx_events_watch ON events (watch_id, created_at DESC);
CREATE INDEX idx_events_type ON events (type, created_at DESC);

CREATE TABLE notifications (
    id         TEXT PRIMARY KEY,
    event_id   TEXT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    status     TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    sent_at    TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_notifications_created ON notifications (created_at DESC);
CREATE INDEX idx_notifications_event ON notifications (event_id);

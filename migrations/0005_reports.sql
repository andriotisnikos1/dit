-- 0005_reports.sql — scheduled daily/weekly reports.
--
-- Update events carry one "reported" marker per report kind. A report marks
-- exactly the events it delivered, so an event appears in one daily and one
-- weekly report. Both columns are nullable: NULL means "not reported yet".
--
-- Purely additive: no CHECK constraints are touched, so no table rebuild (the
-- cascade-aware rebuild in 0003 is the pattern any future one must follow).

ALTER TABLE events ADD COLUMN daily_reported_at TEXT;
ALTER TABLE events ADD COLUMN weekly_reported_at TEXT;

-- Reporting reads only unreported change events, per watch, newest last.
CREATE INDEX idx_events_unreported_daily
    ON events (watch_id, created_at) WHERE daily_reported_at IS NULL;
CREATE INDEX idx_events_unreported_weekly
    ON events (watch_id, created_at) WHERE weekly_reported_at IS NULL;

-- One row per report kind: when it last ran, whether it was delivered, and
-- what it carried. Observability only — correctness comes from the event
-- markers above, so a lost state row can never cause duplicate or lost events.
CREATE TABLE report_state (
    kind         TEXT PRIMARY KEY CHECK (kind IN ('daily', 'weekly')),
    last_run_at  TEXT,
    last_sent_at TEXT,
    last_status  TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    last_events  INTEGER NOT NULL DEFAULT 0,
    last_watches INTEGER NOT NULL DEFAULT 0,
    updated_at   TEXT NOT NULL
);

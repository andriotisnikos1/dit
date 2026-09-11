-- 0003_channel_email_http.sql — allow the email-http channel type.
--
-- The channels table originally constrained type to ('email', 'ntfy'). SQLite
-- cannot alter a CHECK constraint in place, so the table is rebuilt: create the
-- replacement with the widened constraint, copy every row across, drop the
-- original and rename.
--
-- The email-http type sends through a transactional email provider's HTTPS API
-- instead of SMTP, for environments where outbound SMTP is blocked (Railway
-- disables it below its Pro plan, for example) while HTTPS egress works.
--
-- ORDER MATTERS. watch_channels has `channel_id REFERENCES channels (id) ON
-- DELETE CASCADE`, so dropping the old channels table with foreign keys enabled
-- deletes every subscription row that points at a channel — silently removing
-- which channels a watch notifies. The rebuild therefore empties watch_channels
-- first and repopulates it afterwards from a snapshot held in a temporary
-- table, which leaves the end state identical without depending on a pragma
-- that cannot be changed inside the transaction the runner opens.

CREATE TEMP TABLE watch_channels_backup AS SELECT watch_id, channel_id FROM watch_channels;

DELETE FROM watch_channels;

CREATE TABLE channels_new (
    id          TEXT PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    type        TEXT    NOT NULL CHECK (type IN ('email', 'email-http', 'ntfy')),
    config_json TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

INSERT INTO channels_new (id, name, type, config_json, enabled, is_default, created_at, updated_at)
SELECT id, name, type, config_json, enabled, is_default, created_at, updated_at
FROM channels;

DROP TABLE channels;

ALTER TABLE channels_new RENAME TO channels;

-- Restore the subscriptions. The DELETE above plus this INSERT is equivalent to
-- the rows never moving, and both tables exist in the same transaction.
INSERT INTO watch_channels (watch_id, channel_id)
SELECT b.watch_id, b.channel_id
FROM watch_channels_backup b
WHERE EXISTS (SELECT 1 FROM watches w WHERE w.id = b.watch_id)
  AND EXISTS (SELECT 1 FROM channels c WHERE c.id = b.channel_id);

DROP TABLE watch_channels_backup;

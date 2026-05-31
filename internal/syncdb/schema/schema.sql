-- Durable store. The events table (plus schema_meta) is the only place rows
-- actually live; everything derived from it — sessions / transcript_stats /
-- pr_metrics / session_concurrency_* and the INSTEAD OF INSERT triggers — lives
-- in views.sql, which is safe to drop & recreate without losing data. Keeping
-- the destructive DDL (DROP TABLE events) confined to this file lets the
-- view-repair path (schema.EnsureViews) heal a DB whose aggregate VIEWs went
-- missing without touching stored events (issue 0052).
--
-- schema.SQL = schema.sql + views.sql (composed in schema.go); the embedded
-- SHA256 hash in schema_hash.go is computed over that same composition by
-- ./genhash, so editing either file changes the hash and triggers a rebuild.
DROP TABLE IF EXISTS permission_events;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS schema_meta;

CREATE TABLE schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE events (
    local_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       TEXT NOT NULL UNIQUE,
    occurred_at    TEXT NOT NULL,
    received_at    TEXT NOT NULL DEFAULT '',
    session_id     TEXT NOT NULL,
    coding_agent   TEXT NOT NULL DEFAULT 'claude',
    event_name     TEXT NOT NULL,
    attributes     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_events_session ON events(session_id, coding_agent, event_name, occurred_at, local_sequence);
CREATE INDEX idx_events_name_time ON events(event_name, occurred_at);

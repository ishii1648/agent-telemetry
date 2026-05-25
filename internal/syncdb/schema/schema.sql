DROP TRIGGER IF EXISTS sessions_insert_events;
DROP TRIGGER IF EXISTS transcript_stats_insert_events;
DROP VIEW IF EXISTS weekly_session_metrics;
DROP VIEW IF EXISTS weekly_pr_metrics;
DROP VIEW IF EXISTS pr_merged_at_approx;
DROP VIEW IF EXISTS pr_metrics;
DROP VIEW IF EXISTS session_concurrency_weekly;
DROP VIEW IF EXISTS session_concurrency_daily;
DROP VIEW IF EXISTS session_intervals;
DROP VIEW IF EXISTS transcript_stats;
DROP VIEW IF EXISTS sessions;
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

CREATE VIEW sessions AS
WITH
latest_started AS (
    SELECT e.*
    FROM events e
    WHERE e.event_name = 'agent.session.started'
      AND e.local_sequence = (
        SELECT MAX(e2.local_sequence)
        FROM events e2
        WHERE e2.session_id = e.session_id
          AND e2.coding_agent = e.coding_agent
          AND e2.event_name = e.event_name
      )
),
latest_ended AS (
    SELECT e.*
    FROM events e
    WHERE e.event_name = 'agent.session.ended'
      AND e.local_sequence = (
        SELECT MAX(e2.local_sequence)
        FROM events e2
        WHERE e2.session_id = e.session_id
          AND e2.coding_agent = e.coding_agent
          AND e2.event_name = e.event_name
      )
),
latest_pr AS (
    SELECT e.*
    FROM events e
    WHERE e.event_name = 'agent.pr.observed'
      AND e.local_sequence = (
        SELECT MAX(e2.local_sequence)
        FROM events e2
        WHERE e2.session_id = e.session_id
          AND e2.coding_agent = e.coding_agent
          AND e2.event_name = e.event_name
      )
)
SELECT
    s.session_id,
    s.coding_agent,
    COALESCE(json_extract(s.attributes, '$.agent_version'), '') AS agent_version,
    COALESCE(json_extract(s.attributes, '$.user_id'), 'unknown') AS user_id,
    COALESCE(json_extract(s.attributes, '$.started_at'), s.occurred_at) AS timestamp,
    COALESCE(json_extract(s.attributes, '$.cwd'), '') AS cwd,
    COALESCE(json_extract(s.attributes, '$.repo'), '') AS repo,
    COALESCE(json_extract(s.attributes, '$.branch'), '') AS branch,
    COALESCE(json_extract(p.attributes, '$.pr_url'), '') AS pr_url,
    COALESCE(json_extract(p.attributes, '$.pr_title'), '') AS pr_title,
    COALESCE(json_extract(s.attributes, '$.transcript'), '') AS transcript,
    COALESCE(json_extract(s.attributes, '$.parent_session_id'), '') AS parent_session_id,
    COALESCE(json_extract(en.attributes, '$.ended_at'), '') AS ended_at,
    COALESCE(json_extract(en.attributes, '$.end_reason'), '') AS end_reason,
    CASE WHEN COALESCE(json_extract(s.attributes, '$.parent_session_id'), '') != '' THEN 1 ELSE 0 END AS is_subagent,
    COALESCE(json_extract(p.attributes, '$.backfill_checked'), 0) AS backfill_checked,
    COALESCE(json_extract(p.attributes, '$.is_merged'), 0) AS is_merged,
    CASE
      WHEN COALESCE(json_extract(s.attributes, '$.branch'), '') LIKE 'feat/%' THEN 'feat'
      WHEN COALESCE(json_extract(s.attributes, '$.branch'), '') LIKE 'fix/%' THEN 'fix'
      WHEN COALESCE(json_extract(s.attributes, '$.branch'), '') LIKE 'docs/%' THEN 'docs'
      WHEN COALESCE(json_extract(s.attributes, '$.branch'), '') LIKE 'chore/%' THEN 'chore'
      ELSE ''
    END AS task_type,
    COALESCE(json_extract(p.attributes, '$.review_comments'), 0) AS review_comments,
    COALESCE(json_extract(p.attributes, '$.changes_requested'), 0) AS changes_requested
FROM latest_started s
LEFT JOIN latest_ended en
    ON en.session_id = s.session_id AND en.coding_agent = s.coding_agent
LEFT JOIN latest_pr p
    ON p.session_id = s.session_id AND p.coding_agent = s.coding_agent;

CREATE VIEW transcript_stats AS
WITH latest_stats AS (
    SELECT e.*
    FROM events e
    WHERE e.event_name = 'agent.transcript.scanned'
      AND e.local_sequence = (
        SELECT MAX(e2.local_sequence)
        FROM events e2
        WHERE e2.session_id = e.session_id
          AND e2.coding_agent = e.coding_agent
          AND e2.event_name = e.event_name
      )
)
SELECT
    session_id,
    coding_agent,
    COALESCE(json_extract(attributes, '$.tool_use_total'), 0) AS tool_use_total,
    COALESCE(json_extract(attributes, '$.mid_session_msgs'), 0) AS mid_session_msgs,
    COALESCE(json_extract(attributes, '$.ask_user_question'), 0) AS ask_user_question,
    COALESCE(json_extract(attributes, '$.input_tokens'), 0) AS input_tokens,
    COALESCE(json_extract(attributes, '$.output_tokens'), 0) AS output_tokens,
    COALESCE(json_extract(attributes, '$.cache_write_tokens'), 0) AS cache_write_tokens,
    COALESCE(json_extract(attributes, '$.cache_read_tokens'), 0) AS cache_read_tokens,
    COALESCE(json_extract(attributes, '$.reasoning_tokens'), 0) AS reasoning_tokens,
    COALESCE(json_extract(attributes, '$.model'), '') AS model,
    COALESCE(json_extract(attributes, '$.is_ghost'), 0) AS is_ghost
FROM latest_stats;

CREATE TRIGGER sessions_insert_events
INSTEAD OF INSERT ON sessions
BEGIN
    INSERT INTO events (event_id, occurred_at, session_id, coding_agent, event_name, attributes)
    VALUES (
        lower(hex(randomblob(16))),
        COALESCE(NULLIF(NEW.timestamp, ''), datetime('now')),
        NEW.session_id,
        NEW.coding_agent,
        'agent.session.started',
        json_object(
            'agent_version', NEW.agent_version,
            'user_id', NEW.user_id,
            'cwd', NEW.cwd,
            'repo', NEW.repo,
            'branch', NEW.branch,
            'transcript', NEW.transcript,
            'parent_session_id', NEW.parent_session_id,
            'started_at', NEW.timestamp
        )
    );

    INSERT INTO events (event_id, occurred_at, session_id, coding_agent, event_name, attributes)
    SELECT
        lower(hex(randomblob(16))),
        COALESCE(NULLIF(NEW.ended_at, ''), datetime('now')),
        NEW.session_id,
        NEW.coding_agent,
        'agent.session.ended',
        json_object('ended_at', NEW.ended_at, 'end_reason', NEW.end_reason)
    WHERE NEW.ended_at != '' OR NEW.end_reason != '';

    INSERT INTO events (event_id, occurred_at, session_id, coding_agent, event_name, attributes)
    VALUES (
        lower(hex(randomblob(16))),
        datetime('now'),
        NEW.session_id,
        NEW.coding_agent,
        'agent.pr.observed',
        json_object(
            'pr_url', NEW.pr_url,
            'pr_title', NEW.pr_title,
            'pr_state', CASE WHEN NEW.is_merged = 1 THEN 'merged' ELSE '' END,
            'is_merged', NEW.is_merged,
            'review_comments', NEW.review_comments,
            'changes_requested', NEW.changes_requested,
            'pr_pinned', CASE WHEN NEW.pr_url != '' THEN 1 ELSE 0 END,
            'backfill_checked', NEW.backfill_checked
        )
    );
END;

CREATE TRIGGER transcript_stats_insert_events
INSTEAD OF INSERT ON transcript_stats
BEGIN
    INSERT INTO events (event_id, occurred_at, session_id, coding_agent, event_name, attributes)
    VALUES (
        lower(hex(randomblob(16))),
        datetime('now'),
        NEW.session_id,
        NEW.coding_agent,
        'agent.transcript.scanned',
        json_object(
            'tool_use_total', NEW.tool_use_total,
            'mid_session_msgs', NEW.mid_session_msgs,
            'ask_user_question', NEW.ask_user_question,
            'input_tokens', NEW.input_tokens,
            'output_tokens', NEW.output_tokens,
            'cache_write_tokens', NEW.cache_write_tokens,
            'cache_read_tokens', NEW.cache_read_tokens,
            'reasoning_tokens', NEW.reasoning_tokens,
            'model', NEW.model,
            'is_ghost', NEW.is_ghost
        )
    );
END;

CREATE VIEW session_intervals AS
SELECT
    s.session_id,
    s.coding_agent,
    s.timestamp AS started_at,
    s.ended_at,
    s.repo,
    s.branch,
    s.pr_url,
    s.task_type
FROM sessions s
LEFT JOIN transcript_stats ts
    ON s.session_id = ts.session_id AND s.coding_agent = ts.coding_agent
WHERE s.is_subagent = 0
  AND COALESCE(ts.is_ghost, 0) = 0
  AND s.repo NOT IN ('ishii1648/dotfiles')
  AND s.timestamp != ''
  AND s.ended_at != '';

CREATE VIEW session_concurrency_daily AS
SELECT
    date(anchor.started_at) AS day,
    anchor.coding_agent AS coding_agent,
    ROUND(AVG((
        SELECT COUNT(*)
        FROM session_intervals active
        WHERE active.coding_agent = anchor.coding_agent
          AND datetime(active.started_at) <= datetime(anchor.started_at)
          AND datetime(active.ended_at) > datetime(anchor.started_at)
    )), 2) AS avg_concurrent_sessions,
    MAX((
        SELECT COUNT(*)
        FROM session_intervals active
        WHERE active.coding_agent = anchor.coding_agent
          AND datetime(active.started_at) <= datetime(anchor.started_at)
          AND datetime(active.ended_at) > datetime(anchor.started_at)
    )) AS peak_concurrent_sessions
FROM session_intervals anchor
GROUP BY date(anchor.started_at), anchor.coding_agent;

CREATE VIEW session_concurrency_weekly AS
SELECT
    date(day, 'weekday 0', '-6 days') AS week_start,
    coding_agent,
    ROUND(AVG(avg_concurrent_sessions), 2) AS avg_concurrent_sessions,
    MAX(peak_concurrent_sessions) AS peak_concurrent_sessions
FROM session_concurrency_daily
GROUP BY date(day, 'weekday 0', '-6 days'), coding_agent;

CREATE VIEW pr_metrics AS
SELECT
    pm.*,
    (pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.cache_read_tokens + pm.reasoning_tokens) AS total_tokens,
    (pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.reasoning_tokens) AS fresh_tokens,
    CASE WHEN pm.session_count > 0
         THEN ROUND((pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.cache_read_tokens + pm.reasoning_tokens) * 1.0 / pm.session_count, 1)
         ELSE NULL END AS tokens_per_session,
    CASE WHEN pm.tool_use_total > 0
         THEN ROUND((pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.cache_read_tokens + pm.reasoning_tokens) * 1.0 / pm.tool_use_total, 1)
         ELSE NULL END AS tokens_per_tool_use,
    CASE WHEN (pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.cache_read_tokens + pm.reasoning_tokens) > 0
         THEN ROUND(1000000.0 / (pm.input_tokens + pm.output_tokens + pm.cache_write_tokens + pm.cache_read_tokens + pm.reasoning_tokens), 2)
         ELSE NULL END AS pr_per_million_tokens
FROM (
    SELECT
        s.pr_url,
        MAX(s.pr_title) AS pr_title,
        s.coding_agent,
        s.user_id,
        MAX(s.task_type) AS task_type,
        MAX(ts.model) AS model,
        COUNT(DISTINCT s.session_id) AS session_count,
        COALESCE(SUM(ts.tool_use_total), 0) AS tool_use_total,
        COALESCE(SUM(ts.mid_session_msgs), 0) AS mid_session_msgs,
        COALESCE(SUM(ts.ask_user_question), 0) AS ask_user_question,
        COALESCE(SUM(ts.input_tokens), 0) AS input_tokens,
        COALESCE(SUM(ts.output_tokens), 0) AS output_tokens,
        COALESCE(SUM(ts.cache_write_tokens), 0) AS cache_write_tokens,
        COALESCE(SUM(ts.cache_read_tokens), 0) AS cache_read_tokens,
        COALESCE(SUM(ts.reasoning_tokens), 0) AS reasoning_tokens,
        MAX(s.review_comments) AS review_comments,
        MAX(s.changes_requested) AS changes_requested
    FROM sessions s
    LEFT JOIN transcript_stats ts
        ON s.session_id = ts.session_id AND s.coding_agent = ts.coding_agent
    WHERE s.pr_url != ''
      AND s.is_subagent = 0
      AND s.is_merged = 1
      AND COALESCE(ts.is_ghost, 0) = 0
      AND s.repo NOT IN ('ishii1648/dotfiles')
    GROUP BY s.pr_url, s.coding_agent, s.user_id
) pm;

CREATE VIEW pr_merged_at_approx AS
SELECT
    pr_url,
    coding_agent,
    user_id,
    MAX(COALESCE(NULLIF(ended_at, ''), timestamp)) AS merged_at_approx
FROM sessions
WHERE pr_url != ''
  AND is_merged = 1
  AND is_subagent = 0
  AND repo NOT IN ('ishii1648/dotfiles')
GROUP BY pr_url, coding_agent, user_id;

CREATE VIEW weekly_pr_metrics AS
SELECT
    date(p.merged_at_approx, 'weekday 0', '-6 days') AS week_start,
    p.coding_agent,
    COUNT(DISTINCT p.pr_url) AS merged_pr_count,
    ROUND(AVG(pm.session_count), 2) AS avg_sessions_per_pr,
    SUM(CASE WHEN pm.changes_requested > 0 THEN 1 ELSE 0 END) AS prs_with_changes_requested,
    ROUND(SUM(CASE WHEN pm.changes_requested > 0 THEN 1.0 ELSE 0 END) / COUNT(DISTINCT p.pr_url), 3) AS changes_requested_rate
FROM pr_merged_at_approx p
JOIN pr_metrics pm
    ON pm.pr_url = p.pr_url AND pm.coding_agent = p.coding_agent AND pm.user_id = p.user_id
GROUP BY date(p.merged_at_approx, 'weekday 0', '-6 days'), p.coding_agent;

CREATE VIEW weekly_session_metrics AS
SELECT
    date(s.timestamp, 'weekday 0', '-6 days') AS week_start,
    s.coding_agent,
    COUNT(DISTINCT s.session_id) AS session_count,
    COALESCE(SUM(ts.input_tokens + ts.output_tokens + ts.cache_write_tokens + ts.cache_read_tokens + ts.reasoning_tokens), 0) AS total_tokens,
    CASE WHEN COUNT(DISTINCT s.session_id) > 0
         THEN ROUND(SUM(ts.input_tokens + ts.output_tokens + ts.cache_write_tokens + ts.cache_read_tokens + ts.reasoning_tokens) * 1.0 / COUNT(DISTINCT s.session_id), 1)
         ELSE 0 END AS tokens_per_session,
    CASE WHEN COUNT(DISTINCT s.session_id) > 0
         THEN ROUND(SUM(ts.ask_user_question) * 1.0 / COUNT(DISTINCT s.session_id), 2)
         ELSE 0 END AS ask_user_question_per_session
FROM sessions s
LEFT JOIN transcript_stats ts
    ON s.session_id = ts.session_id AND s.coding_agent = ts.coding_agent
WHERE s.is_subagent = 0
  AND COALESCE(ts.is_ghost, 0) = 0
  AND s.repo NOT IN ('ishii1648/dotfiles')
  AND s.timestamp != ''
GROUP BY date(s.timestamp, 'weekday 0', '-6 days'), s.coding_agent;

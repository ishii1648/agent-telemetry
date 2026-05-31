package serverclient

import (
	"database/sql"
	"fmt"
)

// This file implements the session-grain weekly gauge (OTLP Metrics)
// representation (issue 0053, Tier 3). It rides the same `signals = ["metrics"]`
// opt-in and the same metrics_cursors as the pr_metrics gauge (metrics.go): a
// metrics target sends BOTH families in one /v1/metrics flush. The mechanics are
// identical (client evaluates a local VIEW, sends pre-aggregated values as a
// gauge stamped with the send time, the backend resolves them with `last`).
//
// What this adds is the session grain that pr_metrics cannot express: pr_metrics
// is is_merged=1 only and keyed by pr_url, so its session_count is "PR-attributed
// sessions", not the count of top-level sessions. The weekly_session_metrics VIEW
// counts every top-level (non-subagent, non-ghost, non-noise-repo) session.
//
// Why WEEKLY pre-aggregation (the issue's weekday-0 bucket problem): the SQLite
// VIEW buckets by date(timestamp,'weekday 0','-6 days') — a Monday-start week in
// Asia/Tokyo. A PromQL [1w] window is epoch-aligned (UTC Thursday) and would
// drift from that calendar week. Rather than re-bucket on the backend (a Mimir
// recording rule would re-introduce the UTC boundary, and back-dating points to
// week_start risks out-of-order rejection on ingest), we do the bucketing in
// SQLite and carry week_start as a gauge LABEL. The dashboard then groups by the
// week_start label and never needs a [1w] window. See issue 0053 Tier 3.

// WeeklySessionRow is one row of the weekly_session_metrics VIEW: a single
// (week_start, coding_agent) bucket of top-level session activity. session_count
// is always >= 1 (a bucket exists only when at least one session falls in it), so
// the ratios are never NULL — unlike pr_metrics, every point is always present.
type WeeklySessionRow struct {
	WeekStart                 string
	CodingAgent               string
	SessionCount              int64
	TotalTokens               int64
	AskUserQuestion           int64
	TokensPerSession          float64
	AskUserQuestionPerSession float64
}

// weeklySessionDesc is the session-grain analogue of prMetricDesc: an int or
// float accessor over a WeeklySessionRow. It is a separate type because the row
// type differs; the gauge wire shape (numberDataPoint) is shared.
//
// Mirroring the pr_metrics philosophy (issue 0043), both the base measures
// (count / total tokens / ask_user_question sum) AND the convenience ratios are
// shipped: a backend without a formula gets the ratios directly, while a
// dashboard that aggregates across coding_agent recomputes the ratio from the
// base measures (sum(total_tokens)/sum(count)) to stay aggregation-safe. The
// ratios are never NULL (session_count >= 1 per bucket), so every point is sent.
type weeklySessionDesc struct {
	name   string
	intVal func(WeeklySessionRow) int64
	float  func(WeeklySessionRow) float64
}

var weeklySessionDescs = []weeklySessionDesc{
	{name: "agent_weekly_session_count", intVal: func(r WeeklySessionRow) int64 { return r.SessionCount }},
	{name: "agent_weekly_session_total_tokens", intVal: func(r WeeklySessionRow) int64 { return r.TotalTokens }},
	{name: "agent_weekly_session_ask_user_question_total", intVal: func(r WeeklySessionRow) int64 { return r.AskUserQuestion }},
	{name: "agent_weekly_session_tokens_per_session", float: func(r WeeklySessionRow) float64 { return r.TokensPerSession }},
	{name: "agent_weekly_session_ask_user_question_per_session", float: func(r WeeklySessionRow) float64 { return r.AskUserQuestionPerSession }},
}

// LoadWeeklySessionMetrics evaluates the local weekly_session_metrics VIEW for
// one coding agent. The VIEW already applies the top-level filters
// (is_subagent=0 / non-ghost / non-noise-repo / timestamp present) and the
// weekday-0 GROUP BY, so each returned row is one (week_start, coding_agent)
// gauge dimension set.
func LoadWeeklySessionMetrics(db *sql.DB, codingAgent string) ([]WeeklySessionRow, error) {
	rows, err := db.Query(`
SELECT week_start, coding_agent, session_count, total_tokens, ask_user_question,
       tokens_per_session, ask_user_question_per_session
FROM weekly_session_metrics
WHERE coding_agent = ?
ORDER BY week_start`, codingAgent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeeklySessionRow
	for rows.Next() {
		var r WeeklySessionRow
		if err := rows.Scan(
			&r.WeekStart, &r.CodingAgent, &r.SessionCount, &r.TotalTokens, &r.AskUserQuestion,
			&r.TokensPerSession, &r.AskUserQuestionPerSession,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// weeklySessionTags returns the gauge dimension set for a weekly session row.
// These are exactly the weekly_session_metrics GROUP BY columns; week_start
// carries the Asia/Tokyo Monday-start week so the backend groups on the label
// instead of a [1w] window. session_id is never a tag (cardinality is bounded by
// weeks × agents).
func weeklySessionTags(r WeeklySessionRow) []otlpAttribute {
	return []otlpAttribute{
		stringAttr("week_start", r.WeekStart),
		stringAttr("coding_agent", r.CodingAgent),
	}
}

// buildWeeklySessionPayload turns weekly session rows into one OTLP Metrics
// payload of gauges, all stamped with timeNano (the send time) like the
// pr_metrics path, so each flush is one fresh sample per (week, agent) and the
// backend's `last` aggregation picks the most recent — the current week's total
// grows across flushes while past weeks stay stable.
func buildWeeklySessionPayload(rows []WeeklySessionRow, clientVersion string, timeNano int64) otlpMetricsPayload {
	ts := fmt.Sprintf("%d", timeNano)
	metrics := make([]otlpMetric, 0, len(weeklySessionDescs))
	for _, d := range weeklySessionDescs {
		points := make([]numberDataPoint, 0, len(rows))
		for _, r := range rows {
			if d.float != nil {
				v := d.float(r)
				points = append(points, numberDataPoint{
					Attributes:   weeklySessionTags(r),
					TimeUnixNano: ts,
					AsDouble:     &v,
				})
				continue
			}
			points = append(points, numberDataPoint{
				Attributes:   weeklySessionTags(r),
				TimeUnixNano: ts,
				AsInt:        fmt.Sprintf("%d", d.intVal(r)),
			})
		}
		if len(points) == 0 {
			continue
		}
		metrics = append(metrics, otlpMetric{Name: d.name, Gauge: gaugeData{DataPoints: points}})
	}
	return otlpMetricsPayload{ResourceMetrics: []resourceMetric{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			stringAttr("service.name", "agent-telemetry"),
			stringAttr("service.version", clientVersion),
		}},
		ScopeMetrics: []scopeMetric{{
			Scope:   otlpScope{Name: "agent-telemetry/client"},
			Metrics: metrics,
		}},
	}}}
}

// splitWeeklySessionBatches chunks weekly rows so each payload stays under
// maxBytes, mirroring splitMetricBatches. Weekly rows are bounded by weeks ×
// agents (tiny), so this almost always yields a single batch; the split keeps it
// robust if a very long history ever exceeds the cap. An empty input yields no
// batches (the caller then sends nothing for this family).
func splitWeeklySessionBatches(rows []WeeklySessionRow, clientVersion string, timeNano int64, maxBytes int) ([]otlpMetricsPayload, error) {
	return splitGaugeBatches(rows, maxBytes, func(chunk []WeeklySessionRow) otlpMetricsPayload {
		return buildWeeklySessionPayload(chunk, clientVersion, timeNano)
	})
}

package serverclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// This file implements the pr_metrics gauge (OTLP Metrics) representation
// (issue 0043). A log-metric backend does not join across records, so the
// cross-event aggregation that the SQLite pr_metrics VIEW performs (session_id
// join + latest-wins + sum) cannot be reproduced from raw events on the backend
// side. Instead the client evaluates the local VIEW and sends each PR's
// pre-aggregated value as an OTLP Metrics gauge (last-value), keyed by the VIEW
// projection (pr_url / coding_agent / user_id / task_type / model).
//
// Gauge is NOT an idempotent upsert: each flush stamps the data points with the
// send time, so a re-computed value for the same PR becomes a NEW point in the
// series. The backend must take the `last` value per dimension set over a range
// (a naive SUM double-counts). session_id is intentionally never a tag, so the
// timeseries cardinality is bounded by the PR count, not the session count.
// See issues/closed/0040-design-pluggable-otlp-export-backends.md (section A).

// PRMetricRow is one row of the pr_metrics VIEW. The nullable float ratios are
// NULL when their denominator is zero (no sessions / no tool use / no tokens);
// such data points are omitted from the gauge rather than sent as zero.
type PRMetricRow struct {
	PRURL              string
	PRTitle            string
	CodingAgent        string
	UserID             string
	TaskType           string
	Model              string
	SessionCount       int64
	ToolUseTotal       int64
	MidSessionMsgs     int64
	AskUserQuestion    int64
	InputTokens        int64
	OutputTokens       int64
	CacheWriteTokens   int64
	CacheReadTokens    int64
	ReasoningTokens    int64
	ReviewComments     int64
	ChangesRequested   int64
	TotalTokens        int64
	FreshTokens        int64
	TokensPerSession   sql.NullFloat64
	TokensPerToolUse   sql.NullFloat64
	PRPerMillionTokens sql.NullFloat64
}

// prMetricDesc maps one pr_metrics column to its OTLP metric name. The names
// match the catalog in docs/metrics.md (agent_pr_*). Every metric is emitted as
// a gauge regardless of the metrics.md counter/gauge label: the client sends a
// cumulative per-PR snapshot and the backend resolves it with `last`, so the
// wire representation is always last-value (see the file header).
type prMetricDesc struct {
	name string
	// intVal returns the integer value for int metrics. float != nil marks a
	// float metric: it returns the value and whether it is present (NULL ratios
	// are skipped).
	intVal func(PRMetricRow) int64
	float  func(PRMetricRow) (float64, bool)
}

var prMetricDescs = []prMetricDesc{
	{name: "agent_pr_session_count", intVal: func(r PRMetricRow) int64 { return r.SessionCount }},
	{name: "agent_pr_tool_use_total", intVal: func(r PRMetricRow) int64 { return r.ToolUseTotal }},
	{name: "agent_pr_mid_session_msgs_total", intVal: func(r PRMetricRow) int64 { return r.MidSessionMsgs }},
	{name: "agent_pr_ask_user_question_total", intVal: func(r PRMetricRow) int64 { return r.AskUserQuestion }},
	{name: "agent_pr_input_tokens_total", intVal: func(r PRMetricRow) int64 { return r.InputTokens }},
	{name: "agent_pr_output_tokens_total", intVal: func(r PRMetricRow) int64 { return r.OutputTokens }},
	{name: "agent_pr_cache_write_tokens_total", intVal: func(r PRMetricRow) int64 { return r.CacheWriteTokens }},
	{name: "agent_pr_cache_read_tokens_total", intVal: func(r PRMetricRow) int64 { return r.CacheReadTokens }},
	{name: "agent_pr_reasoning_tokens_total", intVal: func(r PRMetricRow) int64 { return r.ReasoningTokens }},
	{name: "agent_pr_total_tokens", intVal: func(r PRMetricRow) int64 { return r.TotalTokens }},
	{name: "agent_pr_fresh_tokens", intVal: func(r PRMetricRow) int64 { return r.FreshTokens }},
	{name: "agent_pr_review_comments", intVal: func(r PRMetricRow) int64 { return r.ReviewComments }},
	{name: "agent_pr_changes_requested", intVal: func(r PRMetricRow) int64 { return r.ChangesRequested }},
	{name: "agent_pr_tokens_per_session", float: func(r PRMetricRow) (float64, bool) { return r.TokensPerSession.Float64, r.TokensPerSession.Valid }},
	{name: "agent_pr_tokens_per_tool_use", float: func(r PRMetricRow) (float64, bool) { return r.TokensPerToolUse.Float64, r.TokensPerToolUse.Valid }},
	{name: "agent_pr_per_million_tokens", float: func(r PRMetricRow) (float64, bool) { return r.PRPerMillionTokens.Float64, r.PRPerMillionTokens.Valid }},
}

// OTLP Metrics JSON payload shape (mirrors the otlpPayload structs in flush.go).
// Only the gauge metric kind is used; the per-data-point value is carried as
// either asInt (decimal string, OTLP/JSON convention) or asDouble.
type otlpMetricsPayload struct {
	ResourceMetrics []resourceMetric `json:"resourceMetrics"`
}

type resourceMetric struct {
	Resource     otlpResource  `json:"resource"`
	ScopeMetrics []scopeMetric `json:"scopeMetrics"`
}

type scopeMetric struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}

type otlpMetric struct {
	Name  string    `json:"name"`
	Gauge gaugeData `json:"gauge"`
}

type gaugeData struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
}

type numberDataPoint struct {
	Attributes   []otlpAttribute `json:"attributes"`
	TimeUnixNano string          `json:"timeUnixNano"`
	AsInt        string          `json:"asInt,omitempty"`
	AsDouble     *float64        `json:"asDouble,omitempty"`
}

type metricsPartialSuccess struct {
	RejectedDataPoints int    `json:"rejectedDataPoints"`
	ErrorMessage       string `json:"errorMessage"`
}

type metricsResponse struct {
	PartialSuccess metricsPartialSuccess `json:"partialSuccess"`
}

// LoadPRMetrics evaluates the local pr_metrics VIEW for one coding agent. The
// VIEW already applies the merged/non-subagent/non-ghost/repo filters and the
// GROUP BY (pr_url, coding_agent, user_id), so each returned row is one gauge
// dimension set.
func LoadPRMetrics(db *sql.DB, codingAgent string) ([]PRMetricRow, error) {
	rows, err := db.Query(`
SELECT pr_url, pr_title, coding_agent, user_id, task_type, model,
       session_count, tool_use_total, mid_session_msgs, ask_user_question,
       input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens,
       review_comments, changes_requested, total_tokens, fresh_tokens,
       tokens_per_session, tokens_per_tool_use, pr_per_million_tokens
FROM pr_metrics
WHERE coding_agent = ?
ORDER BY pr_url`, codingAgent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PRMetricRow
	for rows.Next() {
		var r PRMetricRow
		if err := rows.Scan(
			&r.PRURL, &r.PRTitle, &r.CodingAgent, &r.UserID, &r.TaskType, &r.Model,
			&r.SessionCount, &r.ToolUseTotal, &r.MidSessionMsgs, &r.AskUserQuestion,
			&r.InputTokens, &r.OutputTokens, &r.CacheWriteTokens, &r.CacheReadTokens, &r.ReasoningTokens,
			&r.ReviewComments, &r.ChangesRequested, &r.TotalTokens, &r.FreshTokens,
			&r.TokensPerSession, &r.TokensPerToolUse, &r.PRPerMillionTokens,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// maxLocalSequence returns the highest local_sequence for an agent, or 0 when
// there are no events. It drives the metrics cursor: a gauge flush is skipped
// while this has not advanced past the cursor, since no new event means no PR
// value can have changed.
func maxLocalSequence(db *sql.DB, codingAgent string) (int64, error) {
	var n sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(local_sequence) FROM events WHERE coding_agent = ?`, codingAgent).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// prMetricTags returns the gauge dimension set for a PR row. These are exactly
// the pr_metrics VIEW projection columns (issue 0043 keeps the projection
// minimal; repo / pr_state / branch are deferred to a follow-up VIEW
// expansion). session_id is deliberately absent to bound cardinality.
func prMetricTags(r PRMetricRow) []otlpAttribute {
	return []otlpAttribute{
		stringAttr("pr_url", r.PRURL),
		stringAttr("coding_agent", r.CodingAgent),
		stringAttr("user_id", r.UserID),
		stringAttr("task_type", r.TaskType),
		stringAttr("model", r.Model),
	}
}

// buildMetricsPayload turns PR rows into one OTLP Metrics payload of gauges. All
// data points share timeNano (the send time), so each flush is one fresh sample
// per PR and the backend's `last` aggregation picks the most recent.
func buildMetricsPayload(rows []PRMetricRow, clientVersion string, timeNano int64) otlpMetricsPayload {
	ts := fmt.Sprintf("%d", timeNano)
	metrics := make([]otlpMetric, 0, len(prMetricDescs))
	for _, d := range prMetricDescs {
		points := make([]numberDataPoint, 0, len(rows))
		for _, r := range rows {
			if d.float != nil {
				v, ok := d.float(r)
				if !ok {
					continue // NULL ratio: omit the point rather than send a fake zero
				}
				dv := v
				points = append(points, numberDataPoint{
					Attributes:   prMetricTags(r),
					TimeUnixNano: ts,
					AsDouble:     &dv,
				})
				continue
			}
			points = append(points, numberDataPoint{
				Attributes:   prMetricTags(r),
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

// splitMetricBatches chunks PR rows so each payload stays under maxBytes. Rows
// are split (not individual metrics) so every metric's data points for a chunk
// stay together; PR count is bounded so this rarely produces more than one
// batch. An empty input yields no batches.
func splitMetricBatches(rows []PRMetricRow, clientVersion string, timeNano int64, maxBytes int) ([]otlpMetricsPayload, error) {
	return splitGaugeBatches(rows, maxBytes, func(chunk []PRMetricRow) otlpMetricsPayload {
		return buildMetricsPayload(chunk, clientVersion, timeNano)
	})
}

// splitGaugeBatches is the row-type-generic byte splitter shared by every gauge
// family (pr_metrics, weekly session). It accumulates rows into a payload via
// build until the marshaled body would exceed maxBytes, then starts a new batch.
// Splitting on whole rows (not individual metrics) keeps every metric's points
// for a chunk together. An empty input yields no batches.
func splitGaugeBatches[T any](rows []T, maxBytes int, build func([]T) otlpMetricsPayload) ([]otlpMetricsPayload, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	var batches []otlpMetricsPayload
	var cur []T
	for _, r := range rows {
		next := append(append([]T{}, cur...), r)
		body, err := json.Marshal(build(next))
		if err != nil {
			return nil, err
		}
		if len(cur) > 0 && len(body) > maxBytes {
			batches = append(batches, build(cur))
			cur = []T{r}
			continue
		}
		cur = next
	}
	if len(cur) > 0 {
		batches = append(batches, build(cur))
	}
	return batches, nil
}

// metricsURL completes a target's base endpoint with the OTLP Metrics signal
// path, mirroring logsURL. The endpoint model is "base + signal path"
// (docs/spec.md): Datadog's intake is /v1/metrics and our own server matches.
func metricsURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/metrics"
	return u.String(), nil
}

// encodeMetricsBatch serializes a metrics payload in the target's wire encoding,
// returning the body and its Content-Type (mirrors encodeBatch for logs).
func encodeMetricsBatch(encoding string, p otlpMetricsPayload) (body []byte, contentType string, err error) {
	switch encoding {
	case "protobuf":
		body, err = marshalOTLPMetricsProtobuf(p)
		return body, "application/x-protobuf", err
	default: // "json"
		body, err = json.Marshal(p)
		return body, "application/json", err
	}
}

// postMetricsBatch encodes, optionally gzips, and POSTs one metrics batch to the
// target's /v1/metrics endpoint, returning the parsed OTLP partialSuccess and
// the wire size. Like the logs path, a permanent partial-success rejection is
// reported but does not hold the cursor; only a transport error does.
func postMetricsBatch(ctx context.Context, client *http.Client, t ExportTarget, endpoint string, p otlpMetricsPayload) (metricsResponse, int, error) {
	body, contentType, err := encodeMetricsBatch(t.Encoding, p)
	if err != nil {
		return metricsResponse{}, 0, err
	}
	respBody, wireSize, err := doPost(ctx, client, t, endpoint, body, contentType)
	if err != nil {
		return metricsResponse{}, wireSize, err
	}
	var r metricsResponse
	if len(respBody) > 0 && t.Encoding == defaultEncoding {
		if err := json.Unmarshal(respBody, &r); err != nil {
			return metricsResponse{}, wireSize, fmt.Errorf("decode response: %w", err)
		}
	}
	return r, wireSize, nil
}

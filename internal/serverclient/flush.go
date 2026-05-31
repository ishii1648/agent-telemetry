package serverclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/backfill"
	"github.com/ishii1648/agent-telemetry/internal/syncdb"
	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// MaxBatchBytes is the per-request hard cap from docs/spec.md. Kept as a var
// so tests can shrink it without producing massive fixtures.
var MaxBatchBytes = 50 * 1024 * 1024

// gzipThreshold is the payload size above which we compress. Below this,
// gzip overhead (~30 bytes minimum) is comparable to the savings on small
// JSON, so we send raw.
const gzipThreshold = 4 * 1024

// requestTimeout caps each HTTP attempt. Server work is a dumb INSERT OR
// IGNORE into SQLite, so anything beyond this points at network trouble.
const requestTimeout = 60 * time.Second

type FlushOptions struct {
	ClientVersion string
	SinceLast     bool
	Full          bool
	DryRun        bool
	AgentName     string
	DBPath        string
	ConfigPath    string
	HTTPClient    *http.Client
}

type FlushResult struct {
	PerAgent map[string]*FlushAgentResult
}

// FlushAgentResult collects the per-target outcomes for one coding agent.
// NoConfig is set when no export target is configured at all (the common
// opt-out case); Targets is then empty and Eligible reports how many events
// would have been sent so the CLI can show a useful "not configured" hint.
type FlushAgentResult struct {
	NoConfig bool
	DryRun   bool
	Eligible int
	Targets  map[string]*FlushTargetResult
}

// FlushTargetResult is the outcome of flushing to a single export target. Each
// target advances its own cursor independently, so Sent / StateUpdated are
// per-target (issue 0040 per-target cursor contract).
type FlushTargetResult struct {
	Endpoint      string
	Encoding      string
	Sent          int
	Skipped       int
	Batches       int
	PayloadBytes  int64
	Rejected      int
	RejectedError string
	StateUpdated  bool

	// Metrics* mirror the logs fields for the pr_metrics gauge representation
	// (issue 0043). A target that sends both representations populates both
	// halves. SendsMetrics reports whether this target opted into the gauge at
	// all (so Summarize knows to print a metrics line); MetricsUpToDate is set
	// when the gauge flush was skipped because no new event had arrived.
	SendsMetrics         bool
	MetricsUpToDate      bool
	MetricsSeries        int // PR rows (dimension sets) sent this flush
	MetricsBatches       int
	MetricsPayloadBytes  int64
	MetricsRejected      int
	MetricsRejectedError string
	MetricsStateUpdated  bool
}

type EventRow struct {
	LocalSequence int64
	EventID       string
	OccurredAt    string
	SessionID     string
	CodingAgent   string
	EventName     string
	Attributes    json.RawMessage
}

type otlpValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
	BoolValue   bool   `json:"boolValue,omitempty"`
}

type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

type otlpPayload struct {
	ResourceLogs []resourceLog `json:"resourceLogs"`
}

type resourceLog struct {
	Resource  otlpResource `json:"resource"`
	ScopeLogs []scopeLog   `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type scopeLog struct {
	Scope      otlpScope   `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type logRecord struct {
	TimeUnixNano         string          `json:"timeUnixNano"`
	ObservedTimeUnixNano string          `json:"observedTimeUnixNano,omitempty"`
	SeverityNumber       int             `json:"severityNumber"`
	EventName            string          `json:"eventName"`
	Attributes           []otlpAttribute `json:"attributes"`
}

type partialSuccess struct {
	RejectedLogRecords int    `json:"rejectedLogRecords"`
	ErrorMessage       string `json:"errorMessage"`
}

type flushResponse struct {
	PartialSuccess partialSuccess `json:"partialSuccess"`
}

func RunFlush(ctx context.Context, opts FlushOptions) (*FlushResult, error) {
	if opts.DBPath == "" {
		opts.DBPath = syncdb.DBPath()
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = ConfigPath()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: requestTimeout}
	}
	if !opts.Full && !opts.SinceLast {
		opts.SinceLast = true
	}

	targets, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	agents, err := agent.ResolveOrDetect(opts.AgentName)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}
	db, err := sql.Open("sqlite", opts.DBPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	// The metrics representation reads the pr_metrics VIEW, which sync-db owns
	// and which an old binary's DB (or an out-of-band DROP) may be missing.
	// flush does not rebuild from source, so guarantee the derived VIEWs here
	// — non-destructively, never touching events — before any target reads them
	// (issue 0052). Only when a metrics target is actually configured: a
	// logs-only flush has no VIEW dependency, and on a never-synced DB the logs
	// path fails on its own with the same missing-events signal.
	if anyMetricsTarget(targets) {
		if err := schema.EnsureViews(db); err != nil {
			return nil, fmt.Errorf("ensure pr_metrics view: %w", err)
		}
	}

	res := &FlushResult{PerAgent: make(map[string]*FlushAgentResult, len(agents))}
	var firstErr error
	for _, a := range agents {
		ar, err := runFlushForAgent(ctx, db, a, targets, opts)
		res.PerAgent[a.Name] = ar
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return res, firstErr
}

// anyMetricsTarget reports whether at least one configured target opted into the
// pr_metrics gauge representation. It gates the VIEW-ensure in RunFlush so a
// logs-only (or fully opted-out) flush never pays for — or fails on — the
// derived-VIEW guarantee.
func anyMetricsTarget(targets []ExportTarget) bool {
	for _, t := range targets {
		if t.Configured() && t.SendsMetrics() {
			return true
		}
	}
	return false
}

func runFlushForAgent(ctx context.Context, db *sql.DB, a *agent.Agent, targets []ExportTarget, opts FlushOptions) (*FlushAgentResult, error) {
	ar := &FlushAgentResult{DryRun: opts.DryRun, Targets: map[string]*FlushTargetResult{}}

	// Partition configured targets by representation. A target opts into each
	// independently via signals; one target can be in both lists (it then rides
	// two cursors). An unconfigured target (missing endpoint/token) is the
	// opt-out case and appears in neither.
	var logsTargets, metricsTargets []ExportTarget
	for _, t := range targets {
		if !t.Configured() {
			continue
		}
		if t.SendsLogs() {
			logsTargets = append(logsTargets, t)
		}
		if t.SendsMetrics() {
			metricsTargets = append(metricsTargets, t)
		}
	}

	total, err := countEvents(db, a.Name)
	if err != nil {
		return ar, fmt.Errorf("count events[%s]: %w", a.Name, err)
	}

	if len(logsTargets) == 0 && len(metricsTargets) == 0 {
		// No destination opted into any representation: report what would be
		// sent (from a zero cursor) so the CLI can print the "not configured"
		// hint, then return.
		ar.NoConfig = true
		ar.Eligible = total
		return ar, nil
	}

	state, err := backfill.LoadState(a.StatePath())
	if err != nil {
		return ar, fmt.Errorf("load state[%s]: %w", a.Name, err)
	}

	// One send time for the whole agent flush: every gauge point is stamped
	// with it so each flush is a single fresh sample per PR (see metrics.go).
	nowNano := time.Now().UnixNano()

	var firstErr error
	stateDirty := false
	noteErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for _, t := range logsTargets {
		dirty, err := flushLogsToTarget(ctx, db, a, t, opts, total, &state, ar)
		noteErr(err)
		stateDirty = stateDirty || dirty
	}
	for _, t := range metricsTargets {
		dirty, err := flushMetricsToTarget(ctx, db, a, t, opts, nowNano, &state, ar)
		noteErr(err)
		stateDirty = stateDirty || dirty
	}

	if stateDirty {
		if err := backfill.SaveState(a.StatePath(), state); err != nil {
			return ar, fmt.Errorf("save state[%s]: %w", a.Name, err)
		}
	}
	return ar, firstErr
}

// targetResult returns the shared per-target result, creating it on first use.
// Both representations write into the same struct so a target sending logs and
// metrics reports both halves under one id.
func (ar *FlushAgentResult) targetResult(t ExportTarget) *FlushTargetResult {
	if tr := ar.Targets[t.ID]; tr != nil {
		return tr
	}
	tr := &FlushTargetResult{Endpoint: t.Endpoint, Encoding: t.Encoding}
	ar.Targets[t.ID] = tr
	return tr
}

// flushLogsToTarget sends the raw-events (OTLP Logs) representation to one
// target and reports whether the shared state was mutated (cursor advanced).
func flushLogsToTarget(ctx context.Context, db *sql.DB, a *agent.Agent, t ExportTarget, opts FlushOptions, total int, state *backfill.State, ar *FlushAgentResult) (bool, error) {
	tr := ar.targetResult(t)

	after := cursorFor(*state, t.ID)
	if opts.Full {
		after = 0
	}
	events, err := LoadEvents(db, a.Name, after)
	if err != nil {
		return false, fmt.Errorf("load events[%s/%s]: %w", a.Name, t.ID, err)
	}
	tr.Sent = len(events)
	tr.Skipped = total - tr.Sent

	batches, err := splitEventBatches(events, opts.ClientVersion, MaxBatchBytes)
	if err != nil {
		return false, err
	}
	tr.Batches = len(batches)

	if opts.DryRun {
		// Report the size in the target's own encoding so a protobuf
		// destination shows its actual wire size, not the JSON estimate.
		for _, b := range batches {
			body, _, err := encodeBatch(t.Encoding, b)
			if err != nil {
				return false, err
			}
			tr.PayloadBytes += int64(len(body))
		}
		return false, nil
	}

	if err := sendBatches(ctx, opts.HTTPClient, t, batches, tr); err != nil {
		// Transport failure: the cursor stays put so the next flush resends
		// this target's range. Other targets are unaffected (independent
		// advance).
		return false, err
	}
	if len(events) > 0 {
		if state.FlushCursors == nil {
			state.FlushCursors = map[string]int64{}
		}
		state.FlushCursors[t.ID] = events[len(events)-1].LocalSequence
		tr.StateUpdated = true
		return true, nil
	}
	return false, nil
}

// flushMetricsToTarget evaluates the local pr_metrics VIEW and sends every PR's
// current value as an OTLP Metrics gauge (last-value) to one target. It rides
// the separate metrics cursor: the flush is skipped when no new event has
// arrived since the last gauge flush (no event ⇒ no PR value can have changed),
// avoiding redundant identical points. On a successful send the cursor advances
// to the current max local_sequence. See metrics.go for the gauge semantics.
func flushMetricsToTarget(ctx context.Context, db *sql.DB, a *agent.Agent, t ExportTarget, opts FlushOptions, nowNano int64, state *backfill.State, ar *FlushAgentResult) (bool, error) {
	tr := ar.targetResult(t)
	tr.SendsMetrics = true

	maxSeq, err := maxLocalSequence(db, a.Name)
	if err != nil {
		return false, fmt.Errorf("max sequence[%s/%s]: %w", a.Name, t.ID, err)
	}
	if !opts.Full && maxSeq <= metricsCursorFor(*state, t.ID) {
		tr.MetricsUpToDate = true
		return false, nil
	}

	rows, err := LoadPRMetrics(db, a.Name)
	if err != nil {
		return false, fmt.Errorf("load pr_metrics[%s/%s]: %w", a.Name, t.ID, err)
	}
	tr.MetricsSeries = len(rows)

	batches, err := splitMetricBatches(rows, opts.ClientVersion, nowNano, MaxBatchBytes)
	if err != nil {
		return false, err
	}
	tr.MetricsBatches = len(batches)

	if opts.DryRun {
		for _, b := range batches {
			body, _, err := encodeMetricsBatch(t.Encoding, b)
			if err != nil {
				return false, err
			}
			tr.MetricsPayloadBytes += int64(len(body))
		}
		return false, nil
	}

	endpoint, err := metricsURL(t.Endpoint)
	if err != nil {
		return false, err
	}
	for _, b := range batches {
		resp, wire, err := postMetricsBatch(ctx, opts.HTTPClient, t, endpoint, b)
		tr.MetricsPayloadBytes += int64(wire)
		if err != nil {
			// Transport failure: hold the metrics cursor so the next flush
			// re-sends the current gauge values.
			return false, err
		}
		tr.MetricsRejected += resp.PartialSuccess.RejectedDataPoints
		if resp.PartialSuccess.RejectedDataPoints > 0 && resp.PartialSuccess.ErrorMessage != "" {
			tr.MetricsRejectedError = resp.PartialSuccess.ErrorMessage
		}
	}
	// Advance even when rows is empty (no merged PRs yet): the cursor records
	// that we have processed up to maxSeq, so we don't re-scan an unchanged
	// VIEW every flush. A later merge appends a higher-sequence event.
	if state.MetricsCursors == nil {
		state.MetricsCursors = map[string]int64{}
	}
	state.MetricsCursors[t.ID] = maxSeq
	tr.MetricsStateUpdated = true
	return true, nil
}

// sendBatches posts every batch to one target. On the first transport failure
// it returns the error without touching subsequent batches (the caller then
// leaves the cursor un-advanced). Permanent OTLP partial-success rejections are
// accumulated and do NOT stop the cursor — resending malformed records would
// loop forever (see docs/spec.md ## プロトコル).
func sendBatches(ctx context.Context, client *http.Client, t ExportTarget, batches []otlpPayload, tr *FlushTargetResult) error {
	endpoint, err := logsURL(t.Endpoint)
	if err != nil {
		return err
	}
	for _, b := range batches {
		resp, sentBytes, err := postLogsBatch(ctx, client, t, endpoint, b)
		tr.PayloadBytes += int64(sentBytes)
		if err != nil {
			return err
		}
		tr.Rejected += resp.PartialSuccess.RejectedLogRecords
		if resp.PartialSuccess.RejectedLogRecords > 0 && resp.PartialSuccess.ErrorMessage != "" {
			tr.RejectedError = resp.PartialSuccess.ErrorMessage
		}
	}
	return nil
}

// cursorFor returns the last flushed local_sequence for a target. The legacy
// [server] target (id "server") falls back to the single last_flushed_sequence
// field written by pre-0042 binaries, so upgrading does not re-send the whole
// history. New targets with no cursor start at 0 (full backfill, idempotently
// deduped server-side).
func cursorFor(s backfill.State, targetID string) int64 {
	if v, ok := s.FlushCursors[targetID]; ok {
		return v
	}
	if targetID == legacyServerTargetID {
		return s.LastFlushedSequence
	}
	return 0
}

// metricsCursorFor returns the max local_sequence observed at the last gauge
// flush to a target. Unlike cursorFor there is no legacy seed: the gauge
// representation is new in 0043, so an absent entry means "never flushed" and
// starts at 0 (the first flush re-fills every PR's current value).
func metricsCursorFor(s backfill.State, targetID string) int64 {
	return s.MetricsCursors[targetID]
}

func countEvents(db *sql.DB, codingAgent string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE coding_agent = ?`, codingAgent).Scan(&n)
	return n, err
}

func LoadEvents(db *sql.DB, codingAgent string, after int64) ([]EventRow, error) {
	rows, err := db.Query(`
SELECT local_sequence, event_id, occurred_at, session_id, coding_agent, event_name, attributes
FROM events
WHERE coding_agent = ? AND local_sequence > ?
ORDER BY local_sequence`, codingAgent, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var attrs string
		if err := rows.Scan(&e.LocalSequence, &e.EventID, &e.OccurredAt, &e.SessionID, &e.CodingAgent, &e.EventName, &attrs); err != nil {
			return nil, err
		}
		if attrs == "" {
			attrs = "{}"
		}
		e.Attributes = json.RawMessage(attrs)
		out = append(out, e)
	}
	return out, rows.Err()
}

// logsURL completes a target's base endpoint with the OTLP Logs signal path.
// The endpoint model is fixed as "base + signal path" (docs/spec.md): a target
// configures the base URL (e.g. https://otlp.datadoghq.com) and the client
// appends /v1/logs. This matches Datadog's OTLP intake and our own server.
func logsURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/logs"
	return u.String(), nil
}

func splitEventBatches(events []EventRow, clientVersion string, maxBytes int) ([]otlpPayload, error) {
	if len(events) == 0 {
		return nil, nil
	}
	var batches []otlpPayload
	var cur []EventRow
	for _, e := range events {
		next := append(append([]EventRow{}, cur...), e)
		p, err := buildOTLPPayload(next, clientVersion)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(p)
		if len(cur) > 0 && len(body) > maxBytes {
			prev, err := buildOTLPPayload(cur, clientVersion)
			if err != nil {
				return nil, err
			}
			batches = append(batches, prev)
			cur = []EventRow{e}
			continue
		}
		cur = next
	}
	if len(cur) > 0 {
		p, err := buildOTLPPayload(cur, clientVersion)
		if err != nil {
			return nil, err
		}
		batches = append(batches, p)
	}
	return batches, nil
}

func buildOTLPPayload(events []EventRow, clientVersion string) (otlpPayload, error) {
	records := make([]logRecord, 0, len(events))
	for _, e := range events {
		attrs := []otlpAttribute{
			stringAttr("event_id", e.EventID),
			intAttr("local_sequence", e.LocalSequence),
			stringAttr("session_id", e.SessionID),
			stringAttr("coding_agent", e.CodingAgent),
		}
		var extra map[string]any
		if err := json.Unmarshal(e.Attributes, &extra); err != nil {
			return otlpPayload{}, fmt.Errorf("decode event attrs %s: %w", e.EventID, err)
		}
		keys := make([]string, 0, len(extra))
		for k := range extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			attrs = append(attrs, anyAttr(k, extra[k]))
		}
		records = append(records, logRecord{
			TimeUnixNano:         unixNanoString(e.OccurredAt),
			ObservedTimeUnixNano: unixNanoString(e.OccurredAt),
			SeverityNumber:       9,
			EventName:            e.EventName,
			Attributes:           attrs,
		})
	}
	return otlpPayload{ResourceLogs: []resourceLog{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			stringAttr("service.name", "agent-telemetry"),
			stringAttr("service.version", clientVersion),
		}},
		ScopeLogs: []scopeLog{{
			Scope:      otlpScope{Name: "agent-telemetry/client"},
			LogRecords: records,
		}},
	}}}, nil
}

func anyAttr(key string, v any) otlpAttribute {
	switch x := v.(type) {
	case bool:
		return boolAttr(key, x)
	case float64:
		return intAttr(key, int64(x))
	case string:
		return stringAttr(key, x)
	default:
		b, _ := json.Marshal(x)
		return stringAttr(key, string(b))
	}
}

func stringAttr(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{StringValue: value}}
}

func intAttr(key string, value int64) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{IntValue: fmt.Sprintf("%d", value)}}
}

func boolAttr(key string, value bool) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpValue{BoolValue: value}}
}

func unixNanoString(ts string) string {
	if ts == "" {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return fmt.Sprintf("%d", t.UnixNano())
		}
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// postLogsBatch serializes one batch in the target's encoding (JSON or
// protobuf), applies the target's auth header/scheme, optionally gzips, and
// POSTs it. It returns the parsed OTLP partialSuccess, the wire size, and a
// transport error (network failure or >=400) if the batch never landed.
func postLogsBatch(ctx context.Context, client *http.Client, t ExportTarget, endpoint string, p otlpPayload) (flushResponse, int, error) {
	body, contentType, err := encodeBatch(t.Encoding, p)
	if err != nil {
		return flushResponse{}, 0, err
	}
	respBody, wireSize, err := doPost(ctx, client, t, endpoint, body, contentType)
	if err != nil {
		return flushResponse{}, wireSize, err
	}
	var r flushResponse
	// A protobuf intake (Datadog) replies with a protobuf
	// ExportLogsServiceResponse, not JSON. We only need partialSuccess for
	// JSON destinations (our server); a decode failure on a non-JSON 2xx body
	// is treated as "fully accepted, no partial success".
	if len(respBody) > 0 && t.Encoding == defaultEncoding {
		if err := json.Unmarshal(respBody, &r); err != nil {
			return flushResponse{}, wireSize, fmt.Errorf("decode response: %w", err)
		}
	}
	return r, wireSize, nil
}

// doPost encodes auth/gzip and POSTs a pre-serialized body, returning the
// (size-limited) response body and the wire size. It is the shared transport
// for both the logs and metrics representations; only the response decoding
// (partialSuccess shape) differs per signal and stays in the callers. A non-2xx
// status is a transport failure (returned as an error) so the caller holds its
// cursor for a retry.
func doPost(ctx context.Context, client *http.Client, t ExportTarget, endpoint string, body []byte, contentType string) ([]byte, int, error) {
	var reqBody io.Reader = bytes.NewReader(body)
	contentEncoding := ""
	wireSize := len(body)
	if len(body) >= gzipThreshold {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(body); err != nil {
			return nil, 0, fmt.Errorf("gzip write: %w", err)
		}
		if err := gz.Close(); err != nil {
			return nil, 0, fmt.Errorf("gzip close: %w", err)
		}
		reqBody = &buf
		contentEncoding = "gzip"
		wireSize = buf.Len()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(t.AuthHeader, authValue(t.AuthScheme, t.Token))
	if contentEncoding != "" {
		req.Header.Set("Content-Encoding", contentEncoding)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, wireSize, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, wireSize, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, wireSize, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, wireSize, nil
}

// encodeBatch serializes a payload in the requested wire encoding and returns
// the body plus its Content-Type. JSON is the default (our server, OTel
// Collector); protobuf is required by Datadog's direct OTLP Logs intake.
func encodeBatch(encoding string, p otlpPayload) (body []byte, contentType string, err error) {
	switch encoding {
	case "protobuf":
		body, err = marshalOTLPLogsProtobuf(p)
		return body, "application/x-protobuf", err
	default: // "json"
		body, err = json.Marshal(p)
		return body, "application/json", err
	}
}

// authValue builds the credential header value. An empty scheme yields the raw
// token (e.g. dd-api-key: <token>); a scheme prefixes it (Authorization: Bearer
// <token>).
func authValue(scheme, token string) string {
	if scheme == "" {
		return token
	}
	return scheme + " " + token
}

func (r *FlushResult) Summarize(w io.Writer) {
	names := make([]string, 0, len(r.PerAgent))
	for name := range r.PerAgent {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ar := r.PerAgent[name]
		if ar.NoConfig {
			fmt.Fprintf(w, "flush[%s]: export target 設定なし — eligible=%d sent=0 (skipped network)\n", name, ar.Eligible)
			continue
		}
		ids := make([]string, 0, len(ar.Targets))
		for id := range ar.Targets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			tr := ar.Targets[id]
			if ar.DryRun {
				fmt.Fprintf(w, "flush[%s→%s] dry-run: sent=%d skipped=%d batches=%d payload=%d bytes encoding=%s\n",
					name, id, tr.Sent, tr.Skipped, tr.Batches, tr.PayloadBytes, tr.Encoding)
				if tr.SendsMetrics {
					fmt.Fprintf(w, "flush[%s→%s] dry-run (metrics): series=%d batches=%d payload=%d bytes encoding=%s\n",
						name, id, tr.MetricsSeries, tr.MetricsBatches, tr.MetricsPayloadBytes, tr.Encoding)
				}
				continue
			}
			fmt.Fprintf(w, "flush[%s→%s]: sent=%d skipped=%d batches=%d payload=%d bytes encoding=%s rejected=%d\n",
				name, id, tr.Sent, tr.Skipped, tr.Batches, tr.PayloadBytes, tr.Encoding, tr.Rejected)
			if tr.Rejected > 0 {
				// Rejected records are permanently dropped (cursor advanced past
				// them); warn so the operator can inspect the server's rejected.log.
				fmt.Fprintf(w, "  warning: %d records permanently rejected by %s (not retried): %s\n",
					tr.Rejected, id, tr.RejectedError)
			}
			if tr.SendsMetrics {
				if tr.MetricsUpToDate {
					fmt.Fprintf(w, "flush[%s→%s] (metrics): up-to-date — no new events since last gauge flush\n", name, id)
				} else {
					fmt.Fprintf(w, "flush[%s→%s] (metrics): series=%d batches=%d payload=%d bytes encoding=%s rejected=%d\n",
						name, id, tr.MetricsSeries, tr.MetricsBatches, tr.MetricsPayloadBytes, tr.Encoding, tr.MetricsRejected)
					if tr.MetricsRejected > 0 {
						fmt.Fprintf(w, "  warning: %d gauge points permanently rejected by %s (not retried): %s\n",
							tr.MetricsRejected, id, tr.MetricsRejectedError)
					}
				}
			}
		}
	}
}

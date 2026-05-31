package serverclient

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// metricsTestEnv stands up a temp HOME and a DB seeded with the events that a
// merged PR's lifecycle produces, plus a fake intake that records the path and
// decodes the OTLP Metrics payload so tests can assert the gauge points.
type metricsTestEnv struct {
	*flushTestEnv
	mu          sync.Mutex
	metricsBody []byte // last decoded /v1/metrics request body (JSON)
	metricsPath string
	gaugeCalls  int
}

func newMetricsTestEnv(t *testing.T) *metricsTestEnv {
	base := newFlushTestEnv(t)
	env := &metricsTestEnv{flushTestEnv: base}
	// Replace the handler so /v1/metrics is captured separately from /v1/logs.
	base.server.Config.Handler = http.HandlerFunc(env.handle)
	return env
}

func (e *metricsTestEnv) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/v1/metrics") {
		body, _ := readAllBody(r)
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				e.t.Fatalf("gunzip: %v", err)
			}
			body, _ = io.ReadAll(gz)
		}
		e.mu.Lock()
		e.metricsBody = body
		e.metricsPath = r.URL.Path
		e.gaugeCalls++
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(metricsResponse{})
		return
	}
	e.flushTestEnv.handle(w, r)
}

func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

// seedMergedPR writes the events for one merged PR: a started session, a
// transcript scan with token/tool counts, and a pr.observed marking it merged.
// These flow through the same VIEW the gauge reads.
func (e *metricsTestEnv) seedMergedPR(sessionID, prURL string, inputTokens, outputTokens, toolUse int) {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema.SQL); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('schema_hash', ?)", schema.Hash); err != nil {
		e.t.Fatal(err)
	}
	// Insert via the VIEW triggers so the event attributes match production.
	if _, err := db.Exec(`INSERT INTO sessions
		(session_id, coding_agent, agent_version, user_id, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, backfill_checked, is_merged, review_comments, changes_requested)
		VALUES (?, 'claude', 'v1', 'alice', '2026-05-10T10:00:00Z', '', 'u/r', 'feat/x', ?, 'My PR', '', '', '2026-05-10T11:00:00Z', 'clear', 1, 1, 0, 0)`,
		sessionID, prURL); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_stats
		(session_id, coding_agent, tool_use_total, mid_session_msgs, ask_user_question, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens, model, is_ghost)
		VALUES (?, 'claude', ?, 0, 0, ?, ?, 0, 0, 0, 'claude-sonnet-4-6', 0)`,
		sessionID, toolUse, inputTokens, outputTokens); err != nil {
		e.t.Fatal(err)
	}
}

func (e *metricsTestEnv) decodeMetrics() otlpMetricsPayload {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.metricsBody) == 0 {
		e.t.Fatal("no metrics request captured")
	}
	var p otlpMetricsPayload
	if err := json.Unmarshal(e.metricsBody, &p); err != nil {
		e.t.Fatalf("decode metrics body: %v", err)
	}
	return p
}

// gaugePoint finds the single data point for a metric name and returns its
// asInt value (as string) and its tags.
func gaugePoints(p otlpMetricsPayload, name string) []numberDataPoint {
	for _, rm := range p.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					return m.Gauge.DataPoints
				}
			}
		}
	}
	return nil
}

func tagOf(dp numberDataPoint, key string) string {
	for _, a := range dp.Attributes {
		if a.Key == key {
			return a.Value.StringValue
		}
	}
	return ""
}

// TestFlush_MetricsGauge verifies the end-to-end gauge path: a metrics target
// reads the pr_metrics VIEW, sends one gauge point per PR keyed by the VIEW
// projection tags, and advances the separate metrics cursor.
func TestFlush_MetricsGauge(t *testing.T) {
	env := newMetricsTestEnv(t)
	env.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"metrics\"]\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr := res.PerAgent["claude"].Targets["dd"]
	if tr == nil || !tr.SendsMetrics {
		t.Fatalf("dd target metrics: %+v", tr)
	}
	if tr.MetricsSeries != 1 {
		t.Errorf("MetricsSeries: got %d, want 1", tr.MetricsSeries)
	}
	if env.metricsPath != "/v1/metrics" {
		t.Errorf("path: got %q, want /v1/metrics", env.metricsPath)
	}

	p := env.decodeMetrics()
	// total_tokens = input + output (others 0) = 150.
	dps := gaugePoints(p, "agent_pr_total_tokens")
	if len(dps) != 1 {
		t.Fatalf("agent_pr_total_tokens points: got %d, want 1", len(dps))
	}
	if dps[0].AsInt != "150" {
		t.Errorf("agent_pr_total_tokens: got %q, want 150", dps[0].AsInt)
	}
	if got := tagOf(dps[0], "pr_url"); got != "https://gh/pr/1" {
		t.Errorf("pr_url tag: got %q", got)
	}
	if got := tagOf(dps[0], "user_id"); got != "alice" {
		t.Errorf("user_id tag: got %q", got)
	}
	if got := tagOf(dps[0], "model"); got != "claude-sonnet-4-6" {
		t.Errorf("model tag: got %q", got)
	}
	// session_id must never be a tag (cardinality bound).
	if got := tagOf(dps[0], "session_id"); got != "" {
		t.Errorf("session_id must not be a gauge tag, got %q", got)
	}

	// Ratio metric (float): tokens_per_session = 150 / 1 session = 150.0.
	rps := gaugePoints(p, "agent_pr_tokens_per_session")
	if len(rps) != 1 || rps[0].AsDouble == nil {
		t.Fatalf("agent_pr_tokens_per_session: %+v", rps)
	}
	if *rps[0].AsDouble != 150.0 {
		t.Errorf("tokens_per_session: got %v, want 150", *rps[0].AsDouble)
	}

	// Separate metrics cursor advanced; the logs cursor stays untouched.
	st := env.loadState()
	if st.MetricsCursors["dd"] == 0 {
		t.Error("metrics cursor should advance")
	}
	if _, ok := st.FlushCursors["dd"]; ok {
		t.Error("metrics-only target must not write a logs cursor")
	}
}

// TestFlush_MetricsUpToDateSkips verifies the cursor optimization: a second
// flush with no new events sends no gauge request and reports up-to-date.
func TestFlush_MetricsUpToDateSkips(t *testing.T) {
	env := newMetricsTestEnv(t)
	env.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"metrics\"]\n")

	if _, err := env.run(); err != nil {
		t.Fatalf("first flush: %v", err)
	}
	if env.gaugeCalls != 1 {
		t.Fatalf("first flush gauge calls: got %d, want 1", env.gaugeCalls)
	}

	res, err := env.run()
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if env.gaugeCalls != 1 {
		t.Errorf("second flush must not re-send: gauge calls got %d, want 1", env.gaugeCalls)
	}
	if !res.PerAgent["claude"].Targets["dd"].MetricsUpToDate {
		t.Error("second flush should report MetricsUpToDate")
	}
}

// TestFlush_LogsAndMetricsRideSeparateCursors verifies a single target sending
// both representations populates both halves of the result and both cursors.
func TestFlush_LogsAndMetricsRideSeparateCursors(t *testing.T) {
	env := newMetricsTestEnv(t)
	env.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	env.writeConfig("[[export]]\n" +
		"id = \"both\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"logs\", \"metrics\"]\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr := res.PerAgent["claude"].Targets["both"]
	if tr.Sent == 0 {
		t.Error("logs should have been sent")
	}
	if tr.MetricsSeries != 1 {
		t.Errorf("MetricsSeries: got %d, want 1", tr.MetricsSeries)
	}
	st := env.loadState()
	if st.FlushCursors["both"] == 0 {
		t.Error("logs cursor should advance")
	}
	if st.MetricsCursors["both"] == 0 {
		t.Error("metrics cursor should advance")
	}
}

// TestFlush_MetricsTransportErrorHoldsCursor verifies a non-2xx on /v1/metrics
// leaves the metrics cursor unadvanced for retry.
func TestFlush_MetricsTransportErrorHoldsCursor(t *testing.T) {
	env := newFlushTestEnv(t)
	// A backend that 503s on /v1/metrics.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)
	mEnv := &metricsTestEnv{flushTestEnv: env}
	mEnv.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + bad.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"metrics\"]\n")

	_, err := env.run()
	if err == nil {
		t.Fatal("expected transport error from 503 metrics intake")
	}
	if got := env.loadState().MetricsCursors["dd"]; got != 0 {
		t.Errorf("metrics cursor: got %d, want 0 (held on transport failure)", got)
	}
}

// TestFlush_MetricsProtobuf verifies a protobuf metrics target sends
// application/x-protobuf to /v1/metrics.
func TestFlush_MetricsProtobuf(t *testing.T) {
	var gotContentType, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	env := newFlushTestEnv(t)
	mEnv := &metricsTestEnv{flushTestEnv: env}
	mEnv.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + srv.URL + "\"\n" +
		"token = \"dd-secret\"\n" +
		"auth_header = \"dd-api-key\"\n" +
		"encoding = \"protobuf\"\n" +
		"signals = [\"metrics\"]\n")

	if _, err := env.run(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if gotPath != "/v1/metrics" {
		t.Errorf("path: got %q, want /v1/metrics", gotPath)
	}
	if gotContentType != "application/x-protobuf" {
		t.Errorf("Content-Type: got %q, want application/x-protobuf", gotContentType)
	}
}

// TestMarshalOTLPMetricsProtobuf_RoundTrip verifies the metrics JSON-shaped
// payload maps onto the OTLP proto Gauge with int and double values preserved.
func TestMarshalOTLPMetricsProtobuf_RoundTrip(t *testing.T) {
	d := 12.5
	p := otlpMetricsPayload{ResourceMetrics: []resourceMetric{{
		Resource: otlpResource{Attributes: []otlpAttribute{stringAttr("service.name", "agent-telemetry")}},
		ScopeMetrics: []scopeMetric{{
			Scope: otlpScope{Name: "agent-telemetry/client"},
			Metrics: []otlpMetric{
				{Name: "agent_pr_total_tokens", Gauge: gaugeData{DataPoints: []numberDataPoint{
					{Attributes: []otlpAttribute{stringAttr("pr_url", "u")}, TimeUnixNano: "1715600000000000000", AsInt: "150"},
				}}},
				{Name: "agent_pr_tokens_per_session", Gauge: gaugeData{DataPoints: []numberDataPoint{
					{Attributes: []otlpAttribute{stringAttr("pr_url", "u")}, TimeUnixNano: "1715600000000000000", AsDouble: &d},
				}}},
			},
		}},
	}}}

	raw, err := marshalOTLPMetricsProtobuf(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got metricspb.MetricsData
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metrics := got.ResourceMetrics[0].ScopeMetrics[0].Metrics
	byName := map[string]*metricspb.Metric{}
	for _, m := range metrics {
		byName[m.Name] = m
	}
	tot := byName["agent_pr_total_tokens"].GetGauge().DataPoints[0]
	if tot.GetAsInt() != 150 {
		t.Errorf("total_tokens: got %d, want 150 (int, not double)", tot.GetAsInt())
	}
	if tot.TimeUnixNano != 1715600000000000000 {
		t.Errorf("timeUnixNano: got %d", tot.TimeUnixNano)
	}
	tps := byName["agent_pr_tokens_per_session"].GetGauge().DataPoints[0]
	if tps.GetAsDouble() != 12.5 {
		t.Errorf("tokens_per_session: got %v, want 12.5", tps.GetAsDouble())
	}
}

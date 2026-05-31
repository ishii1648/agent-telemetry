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
	mu sync.Mutex
	// A metrics flush now POSTs two gauge families (pr_metrics and
	// weekly_session_metrics, issue 0053) to /v1/metrics, so bodies accumulate
	// across calls and decodeMetrics merges them into one payload.
	metricsBodies [][]byte
	metricsPath   string
	gaugeCalls    int
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
		e.metricsBodies = append(e.metricsBodies, body)
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

// seedTopLevelSession appends one top-level (non-subagent, non-ghost) session
// that is NOT a merged PR, with the given timestamp and ask_user_question count.
// Unlike seedMergedPR it does not (re)apply the schema, so it can add a session
// to a DB seedMergedPR already initialized — used to prove the weekly session
// grain counts sessions pr_metrics (is_merged=1) never would.
func (e *metricsTestEnv) seedTopLevelSession(sessionID, timestamp string, totalInputTokens, askUserQuestion int) {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sessions
		(session_id, coding_agent, agent_version, user_id, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, backfill_checked, is_merged, review_comments, changes_requested)
		VALUES (?, 'claude', 'v1', 'alice', ?, '', 'u/r', 'feat/y', '', '', '', '', '', '', 0, 0, 0, 0)`,
		sessionID, timestamp); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_stats
		(session_id, coding_agent, tool_use_total, mid_session_msgs, ask_user_question, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens, model, is_ghost)
		VALUES (?, 'claude', 0, 0, ?, ?, 0, 0, 0, 0, 'claude-sonnet-4-6', 0)`,
		sessionID, askUserQuestion, totalInputTokens); err != nil {
		e.t.Fatal(err)
	}
}

// decodeMetrics merges every captured /v1/metrics body into one payload so a
// test can assert on a metric regardless of which gauge family's POST carried it
// (pr_metrics and weekly_session_metrics ride separate batches, issue 0053).
func (e *metricsTestEnv) decodeMetrics() otlpMetricsPayload {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.metricsBodies) == 0 {
		e.t.Fatal("no metrics request captured")
	}
	var merged otlpMetricsPayload
	for _, body := range e.metricsBodies {
		var p otlpMetricsPayload
		if err := json.Unmarshal(body, &p); err != nil {
			e.t.Fatalf("decode metrics body: %v", err)
		}
		merged.ResourceMetrics = append(merged.ResourceMetrics, p.ResourceMetrics...)
	}
	return merged
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

// dropView removes a relation from the test DB to simulate a DB whose derived
// VIEWs went missing (an old binary's layout, or an out-of-band DROP).
func (e *metricsTestEnv) dropView(name string) {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP VIEW IF EXISTS " + name); err != nil {
		e.t.Fatal(err)
	}
}

// TestFlush_WeeklySessionGauge verifies the Tier 3 session-grain gauge (issue
// 0053): a metrics target also sends weekly_session_metrics rows keyed by
// (week_start, coding_agent), counting EVERY top-level session — including a
// non-merged one that pr_metrics (is_merged=1) would never see — bucketed by the
// Asia/Tokyo Monday-start week carried as a label.
func TestFlush_WeeklySessionGauge(t *testing.T) {
	env := newMetricsTestEnv(t)
	// s1: a merged PR (also feeds pr_metrics), week of 2026-05-04. 150 tokens.
	env.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	// s2: a NON-merged top-level session in the SAME week, 10 tokens, 2 AUQ.
	// pr_metrics ignores it (is_merged=0); weekly session grain must count it.
	env.seedTopLevelSession("s2", "2026-05-08T09:00:00Z", 10, 2)
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
	if tr == nil || tr.MetricsSessionSeries != 1 {
		t.Fatalf("MetricsSessionSeries: got %+v, want 1 (one week × one agent)", tr)
	}

	p := env.decodeMetrics()
	// session_count counts BOTH sessions (the merged PR + the non-merged one),
	// proving the session grain differs from pr_metrics' merged-only session_count.
	cnt := gaugePoints(p, "agent_weekly_session_count")
	if len(cnt) != 1 {
		t.Fatalf("agent_weekly_session_count points: got %d, want 1", len(cnt))
	}
	if cnt[0].AsInt != "2" {
		t.Errorf("agent_weekly_session_count: got %q, want 2", cnt[0].AsInt)
	}
	if got := tagOf(cnt[0], "week_start"); got != "2026-05-04" {
		t.Errorf("week_start tag: got %q, want 2026-05-04 (JST Monday-start week)", got)
	}
	if got := tagOf(cnt[0], "coding_agent"); got != "claude" {
		t.Errorf("coding_agent tag: got %q", got)
	}
	// session_id must never be a tag (cardinality bound: weeks × agents).
	if got := tagOf(cnt[0], "session_id"); got != "" {
		t.Errorf("session_id must not be a gauge tag, got %q", got)
	}

	// total_tokens = 150 + 10 = 160.
	tot := gaugePoints(p, "agent_weekly_session_total_tokens")
	if len(tot) != 1 || tot[0].AsInt != "160" {
		t.Errorf("agent_weekly_session_total_tokens: got %+v, want one point of 160", tot)
	}
	// ask_user_question sum = 0 + 2 = 2 (base measure for aggregation-safe ratios).
	auq := gaugePoints(p, "agent_weekly_session_ask_user_question_total")
	if len(auq) != 1 || auq[0].AsInt != "2" {
		t.Errorf("agent_weekly_session_ask_user_question_total: got %+v, want one point of 2", auq)
	}
	// tokens_per_session = 160 / 2 = 80.0 (float convenience ratio).
	tps := gaugePoints(p, "agent_weekly_session_tokens_per_session")
	if len(tps) != 1 || tps[0].AsDouble == nil || *tps[0].AsDouble != 80.0 {
		t.Errorf("agent_weekly_session_tokens_per_session: got %+v, want 80.0", tps)
	}
	// ask_user_question_per_session = 2 / 2 = 1.0.
	aps := gaugePoints(p, "agent_weekly_session_ask_user_question_per_session")
	if len(aps) != 1 || aps[0].AsDouble == nil || *aps[0].AsDouble != 1.0 {
		t.Errorf("agent_weekly_session_ask_user_question_per_session: got %+v, want 1.0", aps)
	}
}

// TestFlush_MetricsHealsMissingPRMetricsView is the issue 0052 regression: a DB
// that has events but no pr_metrics VIEW must not crash the metrics flush with
// "no such table: pr_metrics". flush ensures the derived VIEWs non-destructively
// before reading them, so the gauge is sent and the events are preserved.
func TestFlush_MetricsHealsMissingPRMetricsView(t *testing.T) {
	env := newMetricsTestEnv(t)
	env.seedMergedPR("s1", "https://gh/pr/1", 100, 50, 4)
	// weekly_pr_metrics depends on pr_metrics; drop both so the VIEW is genuinely
	// absent (mirrors the broken-DB state the bug was hit on).
	env.dropView("weekly_pr_metrics")
	env.dropView("pr_metrics")
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"metrics\"]\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush must self-heal a missing pr_metrics VIEW, got: %v", err)
	}
	tr := res.PerAgent["claude"].Targets["dd"]
	if tr == nil || tr.MetricsSeries != 1 {
		t.Fatalf("MetricsSeries: got %+v, want 1 (VIEW recreated and read)", tr)
	}
	// The VIEW is back and the seeded events survived (the gauge value proves it).
	p := env.decodeMetrics()
	dps := gaugePoints(p, "agent_pr_total_tokens")
	if len(dps) != 1 || dps[0].AsInt != "150" {
		t.Errorf("agent_pr_total_tokens after heal: got %+v, want one point of 150", dps)
	}
}

// TestFlush_MetricsUninitializedDBHint verifies the actionable failure for a DB
// that was never built by sync-db (no events table). Rather than a cryptic
// "no such table: pr_metrics", flush surfaces a hint to run sync-db first.
func TestFlush_MetricsUninitializedDBHint(t *testing.T) {
	env := newFlushTestEnv(t)
	// No seed: the DB file does not exist / has no schema yet.
	env.writeConfig("[[export]]\n" +
		"id = \"dd\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n" +
		"signals = [\"metrics\"]\n")

	_, err := env.run()
	if err == nil {
		t.Fatal("flush against an uninitialized DB must error")
	}
	if !strings.Contains(err.Error(), "sync-db") {
		t.Errorf("error should hint at sync-db, got: %v", err)
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
	// The first flush POSTs the gauge families (pr_metrics + weekly session);
	// the exact batch count is an implementation detail, so assert the second
	// flush adds zero new POSTs rather than a fixed total.
	if env.gaugeCalls == 0 {
		t.Fatalf("first flush must send at least one gauge POST, got %d", env.gaugeCalls)
	}
	afterFirst := env.gaugeCalls

	res, err := env.run()
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if env.gaugeCalls != afterFirst {
		t.Errorf("second flush must not re-send: gauge calls got %d, want %d", env.gaugeCalls, afterFirst)
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

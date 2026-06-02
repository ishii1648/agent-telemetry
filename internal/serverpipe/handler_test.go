package serverpipe

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

func newTestHandler(t *testing.T) (*Handler, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent-telemetry.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, dir)
	t.Cleanup(func() { h.Close() })
	return h, db, dir
}

func TestEnsureSchema_StoresHash(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var h string
	db.QueryRow("SELECT value FROM schema_meta WHERE key='schema_hash'").Scan(&h)
	if h != schema.Hash {
		t.Errorf("schema_meta hash = %q, want %q", h, schema.Hash)
	}
}

// TestServeLogs_RejectsInvalidAndLogs verifies the OTLP ingest path: a valid
// record is appended to events, an invalid one (missing event_id) is counted
// in rejectedLogRecords and written to rejected.log (rather than silently
// dropped), and the response stays HTTP 200 (partial success, not retried).
func TestServeLogs_RejectsInvalidAndLogs(t *testing.T) {
	h, db, dir := newTestHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[
		{"timeUnixNano":"1715600000000000000","eventName":"agent.session.started","attributes":[
			{"key":"event_id","value":{"stringValue":"good-1"}},
			{"key":"session_id","value":{"stringValue":"s1"}},
			{"key":"coding_agent","value":{"stringValue":"claude"}},
			{"key":"repo","value":{"stringValue":"u/r"}}
		]},
		{"timeUnixNano":"1715600000000000000","eventName":"agent.session.started","attributes":[
			{"key":"session_id","value":{"stringValue":"s2"}},
			{"key":"coding_agent","value":{"stringValue":"claude"}}
		]}
	]}]}]}`

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/logs", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (partial success)", resp.StatusCode)
	}
	var lr logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}
	if lr.PartialSuccess.RejectedLogRecords != 1 {
		t.Errorf("rejectedLogRecords: got %d, want 1", lr.PartialSuccess.RejectedLogRecords)
	}

	var n int
	db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 1 {
		t.Errorf("valid record should be inserted: events count got %d, want 1", n)
	}

	data, err := os.ReadFile(filepath.Join(dir, "rejected.log"))
	if err != nil {
		t.Fatalf("read rejected.log: %v", err)
	}
	if !strings.Contains(string(data), "missing required attribute") {
		t.Errorf("rejected.log missing reason; got %q", string(data))
	}
}

// TestServeLogs_PRMetricsViewExists confirms events flushed via /v1/logs feed
// the derived pr_metrics VIEW (so Grafana reads the same shape it always has,
// now sourced from events rather than the removed /v1/metrics upsert).
func TestServeLogs_PRMetricsViewExists(t *testing.T) {
	h, db, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[
		{"timeUnixNano":"1715600000000000000","eventName":"agent.session.started","attributes":[
			{"key":"event_id","value":{"stringValue":"s-1"}},
			{"key":"session_id","value":{"stringValue":"abc-123"}},
			{"key":"coding_agent","value":{"stringValue":"claude"}},
			{"key":"repo","value":{"stringValue":"u/r"}},
			{"key":"branch","value":{"stringValue":"feat/x"}},
			{"key":"started_at","value":{"stringValue":"2026-03-01 10:00:00"}}
		]},
		{"timeUnixNano":"1715600000000000000","eventName":"agent.pr.observed","attributes":[
			{"key":"event_id","value":{"stringValue":"p-1"}},
			{"key":"session_id","value":{"stringValue":"abc-123"}},
			{"key":"coding_agent","value":{"stringValue":"claude"}},
			{"key":"pr_url","value":{"stringValue":"https://github.com/u/r/pull/1"}},
			{"key":"pr_title","value":{"stringValue":"feat: x"}},
			{"key":"is_merged","value":{"intValue":"1"}}
		]},
		{"timeUnixNano":"1715600000000000000","eventName":"agent.transcript.scanned","attributes":[
			{"key":"event_id","value":{"stringValue":"t-1"}},
			{"key":"session_id","value":{"stringValue":"abc-123"}},
			{"key":"coding_agent","value":{"stringValue":"claude"}},
			{"key":"input_tokens","value":{"intValue":"100"}},
			{"key":"tool_use_total","value":{"intValue":"5"}}
		]}
	]}]}]}`

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/logs", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&n); err != nil {
		t.Fatalf("query pr_metrics: %v", err)
	}
	if n != 1 {
		t.Errorf("pr_metrics rows = %d, want 1", n)
	}
}

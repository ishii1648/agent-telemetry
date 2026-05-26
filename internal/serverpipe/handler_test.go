package serverpipe

import (
	"bytes"
	"compress/gzip"
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

const testToken = "test-token"

func newTestHandler(t *testing.T) (*Handler, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent-telemetry.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, testToken, dir)
	t.Cleanup(func() { h.Close() })
	return h, db, dir
}

func samplePayload() Payload {
	return Payload{
		ClientVersion: "1.0.0",
		SchemaHash:    schema.Hash,
		Sessions: []Session{{
			SessionID:        "abc-123",
			CodingAgent:      "claude",
			AgentVersion:     "1.2.3",
			UserID:           "alice@example.com",
			Timestamp:        "2026-03-01 10:00:00",
			CWD:              "/tmp",
			Repo:             "u/r",
			Branch:           "feat/x",
			PRURL:            "https://github.com/u/r/pull/1",
			PRTitle:          "feat: x",
			Transcript:       "/tmp/t.jsonl",
			ParentSessionID:  "",
			EndedAt:          "2026-03-01 10:30:00",
			EndReason:        "exit",
			IsSubagent:       0,
			BackfillChecked:  1,
			IsMerged:         1,
			TaskType:         "feat",
			ReviewComments:   3,
			ChangesRequested: 1,
		}},
		TranscriptStats: []TranscriptStat{{
			SessionID:        "abc-123",
			CodingAgent:      "claude",
			ToolUseTotal:     5,
			MidSessionMsgs:   2,
			AskUserQuestion:  0,
			InputTokens:      100,
			OutputTokens:     20,
			CacheWriteTokens: 30,
			CacheReadTokens:  400,
			ReasoningTokens:  0,
			Model:            "claude-sonnet-4-5",
			IsGhost:          0,
		}},
	}
}

func postJSON(t *testing.T, h *Handler, p Payload, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	h.ServeIngest(w, req)
	return w
}

func TestServeIngest_HappyPath(t *testing.T) {
	h, db, _ := newTestHandler(t)
	w := postJSON(t, h, samplePayload())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SchemaMismatch {
		t.Fatal("schema_mismatch unexpectedly true")
	}
	if resp.ReceivedSessions != 1 {
		t.Errorf("received_sessions = %d, want 1", resp.ReceivedSessions)
	}

	var rows int
	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE session_id='abc-123' AND coding_agent='claude'").Scan(&rows)
	if rows != 1 {
		t.Errorf("sessions row count = %d, want 1", rows)
	}
	var input int64
	db.QueryRow("SELECT input_tokens FROM transcript_stats WHERE session_id='abc-123'").Scan(&input)
	if input != 100 {
		t.Errorf("input_tokens = %d, want 100", input)
	}
}

func TestServeIngest_SchemaMismatch(t *testing.T) {
	h, db, _ := newTestHandler(t)
	p := samplePayload()
	p.SchemaHash = "wrong-hash"
	w := postJSON(t, h, p)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.SchemaMismatch {
		t.Error("schema_mismatch = false, want true")
	}
	// DB must remain unchanged.
	var rows int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&rows)
	if rows != 0 {
		t.Errorf("sessions row count after mismatch = %d, want 0", rows)
	}
}

func TestServeIngest_Unauthorized(t *testing.T) {
	h, _, _ := newTestHandler(t)
	body, _ := json.Marshal(samplePayload())

	cases := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"wrong-prefix", "Token " + testToken},
		{"bad-token", "Bearer wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeIngest(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", w.Code)
			}
		})
	}
}

func TestServeIngest_MethodNotAllowed(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeIngest(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestServeIngest_UpsertReplacesExisting(t *testing.T) {
	h, db, _ := newTestHandler(t)
	postJSON(t, h, samplePayload())

	updated := samplePayload()
	updated.Sessions[0].ReviewComments = 99
	updated.TranscriptStats[0].InputTokens = 7777
	w := postJSON(t, h, updated)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rc int
	var input int64
	db.QueryRow("SELECT review_comments FROM sessions WHERE session_id='abc-123'").Scan(&rc)
	db.QueryRow("SELECT input_tokens FROM transcript_stats WHERE session_id='abc-123'").Scan(&input)
	if rc != 99 {
		t.Errorf("review_comments = %d, want 99", rc)
	}
	if input != 7777 {
		t.Errorf("input_tokens = %d, want 7777", input)
	}
}

func TestServeIngest_CollisionLogged(t *testing.T) {
	h, _, dir := newTestHandler(t)
	postJSON(t, h, samplePayload())
	// Second push with same composite PK → collision must be logged.
	postJSON(t, h, samplePayload())
	logPath := filepath.Join(dir, "collisions.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read collisions.log: %v", err)
	}
	if !strings.Contains(string(data), "session_id=abc-123") {
		t.Errorf("collisions.log missing entry; got %q", string(data))
	}
	if !strings.Contains(string(data), "coding_agent=claude") {
		t.Errorf("collisions.log missing coding_agent; got %q", string(data))
	}
}

func TestServeIngest_Gzip(t *testing.T) {
	h, db, _ := newTestHandler(t)
	body, _ := json.Marshal(samplePayload())
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(body)
	gz.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", &buf)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeIngest(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var rows int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&rows)
	if rows != 1 {
		t.Errorf("sessions row count = %d, want 1", rows)
	}
}

func TestServeIngest_BadJSON(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeIngest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
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

func TestServeIngest_PRMetricsViewExists(t *testing.T) {
	// Sanity: the upserted rows feed pr_metrics so Grafana can read them.
	h, db, _ := newTestHandler(t)
	postJSON(t, h, samplePayload())
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&n); err != nil {
		t.Fatalf("query pr_metrics: %v", err)
	}
	if n != 1 {
		t.Errorf("pr_metrics rows = %d, want 1", n)
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
	req.Header.Set("Authorization", "Bearer "+testToken)
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

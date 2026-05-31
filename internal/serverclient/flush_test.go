package serverclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/backfill"
	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// capturedRequest records what a fake intake received, so tests can assert the
// per-target encoding (Content-Type) and auth header wiring.
type capturedRequest struct {
	contentType string
	authHeader  string
	authValue   string
}

// flushTestEnv stands up a temp HOME, a DB seeded with raw events, a config
// pointing at a fake server, and lets the test control the OTLP partialSuccess
// response. The named auth header it asserts against defaults to Authorization.
type flushTestEnv struct {
	t          *testing.T
	home       string
	dbPath     string
	configPath string
	statePath  string
	server     *httptest.Server
	rejected   int
	rejectMsg  string
	status     int

	assertAuthHeader string
	mu               sync.Mutex
	requests         []capturedRequest
}

func newFlushTestEnv(t *testing.T) *flushTestEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(home, ".config", "agent-telemetry")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	env := &flushTestEnv{
		t:                t,
		home:             home,
		dbPath:           filepath.Join(home, ".claude", "agent-telemetry.db"),
		configPath:       filepath.Join(configDir, "config.toml"),
		statePath:        agent.Claude().StatePath(),
		status:           http.StatusOK,
		assertAuthHeader: "Authorization",
	}
	env.server = httptest.NewServer(http.HandlerFunc(env.handle))
	t.Cleanup(env.server.Close)
	env.writeConfig("[server]\nendpoint = \"" + env.server.URL + "\"\ntoken = \"test-token\"\n")
	return env
}

func (e *flushTestEnv) writeConfig(body string) {
	e.t.Helper()
	if err := os.WriteFile(e.configPath, []byte(body), 0644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *flushTestEnv) handle(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.requests = append(e.requests, capturedRequest{
		contentType: r.Header.Get("Content-Type"),
		authHeader:  e.assertAuthHeader,
		authValue:   r.Header.Get(e.assertAuthHeader),
	})
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(flushResponse{PartialSuccess: partialSuccess{
		RejectedLogRecords: e.rejected,
		ErrorMessage:       e.rejectMsg,
	}})
}

func (e *flushTestEnv) lastRequest() capturedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		e.t.Fatal("no requests captured")
	}
	return e.requests[len(e.requests)-1]
}

// seedEvents writes n events directly into the events table with ascending
// local_sequence (AUTOINCREMENT) and unique event_ids.
func (e *flushTestEnv) seedEvents(n int) {
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
	for i := 0; i < n; i++ {
		id := "evt-" + string(rune('a'+i))
		if _, err := db.Exec(
			`INSERT INTO events (event_id, occurred_at, session_id, coding_agent, event_name, attributes) VALUES (?, ?, ?, ?, ?, ?)`,
			id, "2026-05-10T10:00:00Z", "s1", "claude", "agent.session.started", `{"repo":"u/r"}`,
		); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *flushTestEnv) loadState() backfill.State {
	e.t.Helper()
	st, err := backfill.LoadState(e.statePath)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *flushTestEnv) saveState(st backfill.State) {
	e.t.Helper()
	if err := backfill.SaveState(e.statePath, st); err != nil {
		e.t.Fatal(err)
	}
}

func (e *flushTestEnv) run() (*FlushResult, error) {
	return RunFlush(context.Background(), FlushOptions{
		DBPath:     e.dbPath,
		ConfigPath: e.configPath,
		AgentName:  "claude",
		SinceLast:  true,
	})
}

// TestFlush_PartialSuccessAdvancesCursor pins the OTLP-correct behavior:
// rejectedLogRecords>0 on a 2xx response is a permanent rejection, so flush
// must NOT return an error and MUST advance the cursor (otherwise the same
// malformed records loop forever). The reject count is surfaced for warnings.
func TestFlush_PartialSuccessAdvancesCursor(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(3)
	env.rejected = 2
	env.rejectMsg = "missing event_id"

	res, err := env.run()
	if err != nil {
		t.Fatalf("partial success must not be a hard error, got: %v", err)
	}
	tr := res.PerAgent["claude"].Targets[legacyServerTargetID]
	if tr == nil {
		t.Fatalf("no server target result: %+v", res.PerAgent["claude"])
	}
	if tr.Rejected != 2 {
		t.Errorf("Rejected: got %d, want 2", tr.Rejected)
	}
	if tr.RejectedError != "missing event_id" {
		t.Errorf("RejectedError: got %q", tr.RejectedError)
	}
	if !tr.StateUpdated {
		t.Error("cursor should advance past rejected batch")
	}
	if got := env.loadState().FlushCursors[legacyServerTargetID]; got != 3 {
		t.Errorf("cursor: got %d, want 3 (must advance despite rejects)", got)
	}
}

// TestFlush_TransportErrorHoldsCursor verifies the complementary case: a
// non-2xx response is a delivery failure, so the cursor must stay put for a
// retry on the next flush.
func TestFlush_TransportErrorHoldsCursor(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(3)
	env.status = http.StatusServiceUnavailable

	_, err := env.run()
	if err == nil {
		t.Fatal("non-2xx response should surface an error")
	}
	if got := env.loadState().FlushCursors[legacyServerTargetID]; got != 0 {
		t.Errorf("cursor: got %d, want 0 (must not advance on transport failure)", got)
	}
}

// TestFlush_LegacyCursorSeed verifies an upgrade from pre-0042: a state.json
// with only the old last_flushed_sequence seeds the "server" target's cursor,
// so we do NOT re-send already-flushed events.
func TestFlush_LegacyCursorSeed(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(5)
	env.saveState(backfill.State{LastFlushedSequence: 3})

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr := res.PerAgent["claude"].Targets[legacyServerTargetID]
	if tr.Sent != 2 {
		t.Errorf("Sent: got %d, want 2 (events 4,5 only — legacy cursor seeded at 3)", tr.Sent)
	}
	if got := env.loadState().FlushCursors[legacyServerTargetID]; got != 5 {
		t.Errorf("cursor: got %d, want 5", got)
	}
}

// TestFlush_NoConfig verifies the opt-out path: no export target configured
// means no network call and a NoConfig result carrying the eligible count.
func TestFlush_NoConfig(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(3)
	env.writeConfig(`user = "alice@example.com"`)

	res, err := env.run()
	if err != nil {
		t.Fatalf("no-config flush must not error: %v", err)
	}
	ar := res.PerAgent["claude"]
	if !ar.NoConfig {
		t.Error("expected NoConfig")
	}
	if ar.Eligible != 3 {
		t.Errorf("Eligible: got %d, want 3", ar.Eligible)
	}
	if len(env.requests) != 0 {
		t.Errorf("no network call expected, got %d requests", len(env.requests))
	}
}

// TestFlush_ProtobufEncoding verifies a target with encoding=protobuf and a
// custom auth header sends application/x-protobuf with the raw token (no Bearer
// prefix) under the configured header name.
func TestFlush_ProtobufEncoding(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(2)
	env.assertAuthHeader = "dd-api-key"
	env.writeConfig("[[export]]\n" +
		"id = \"datadog\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"dd-secret\"\n" +
		"auth_header = \"dd-api-key\"\n" +
		"encoding = \"protobuf\"\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	tr := res.PerAgent["claude"].Targets["datadog"]
	if tr == nil || tr.Sent != 2 {
		t.Fatalf("datadog target: %+v", tr)
	}
	req := env.lastRequest()
	if req.contentType != "application/x-protobuf" {
		t.Errorf("Content-Type: got %q, want application/x-protobuf", req.contentType)
	}
	if req.authValue != "dd-secret" {
		t.Errorf("auth value: got %q, want raw token dd-secret (no scheme prefix)", req.authValue)
	}
	if got := env.loadState().FlushCursors["datadog"]; got != 2 {
		t.Errorf("cursor: got %d, want 2", got)
	}
}

// TestFlush_PerTargetCursorIndependence verifies the 0040 contract: when one
// target succeeds and another fails, only the successful target's cursor
// advances, and the next flush re-sends only to the still-behind target.
func TestFlush_PerTargetCursorIndependence(t *testing.T) {
	good := newFlushTestEnv(t) // reuse env scaffolding; we drive two backends
	good.seedEvents(3)

	// A second backend that always 503s.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)

	good.writeConfig("[[export]]\n" +
		"id = \"good\"\n" +
		"endpoint = \"" + good.server.URL + "\"\n" +
		"token = \"t\"\n\n" +
		"[[export]]\n" +
		"id = \"bad\"\n" +
		"endpoint = \"" + bad.URL + "\"\n" +
		"token = \"t\"\n")

	_, err := good.run()
	if err == nil {
		t.Fatal("expected an error from the failing target")
	}
	st := good.loadState()
	if st.FlushCursors["good"] != 3 {
		t.Errorf("good cursor: got %d, want 3 (advanced)", st.FlushCursors["good"])
	}
	if st.FlushCursors["bad"] != 0 {
		t.Errorf("bad cursor: got %d, want 0 (held)", st.FlushCursors["bad"])
	}
}

// TestFlush_TokenlessTargetSends pins issue 0051 end to end: a target with an
// endpoint but no token (the OSS observability recipe) is a real flush
// destination — it actually POSTs and advances its cursor, instead of being
// silently skipped with exit 0. The auth header still goes out, but with an
// empty value (no Bearer prefix on an empty token would be "Bearer ", so we
// assert the raw empty value only via a successful send + cursor advance).
func TestFlush_TokenlessTargetSends(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(2)
	env.writeConfig("[[export]]\n" +
		"id = \"oss-collector\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"signals = [\"logs\"]\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("tokenless flush must not error: %v", err)
	}
	ar := res.PerAgent["claude"]
	if ar.NoConfig {
		t.Fatal("tokenless target must not be treated as NoConfig")
	}
	tr := ar.Targets["oss-collector"]
	if tr == nil || tr.Sent != 2 {
		t.Fatalf("tokenless target should have sent 2 events: %+v", tr)
	}
	if len(env.requests) == 0 {
		t.Error("expected a network call for the tokenless target")
	}
	if got := env.loadState().FlushCursors["oss-collector"]; got != 2 {
		t.Errorf("cursor: got %d, want 2", got)
	}
}

// TestFlush_MisconfiguredTargetSurfaced pins the other half of issue 0051: a
// target present in config but missing an endpoint is skipped, yet its id is
// reported in FlushResult.Misconfigured (and on stderr via Summarize) so the
// user can tell a typo from an intentional opt-out.
func TestFlush_MisconfiguredTargetSurfaced(t *testing.T) {
	env := newFlushTestEnv(t)
	env.seedEvents(1)
	// One healthy target plus one with no endpoint at all.
	env.writeConfig("[[export]]\n" +
		"id = \"healthy\"\n" +
		"endpoint = \"" + env.server.URL + "\"\n" +
		"token = \"t\"\n\n" +
		"[[export]]\n" +
		"id = \"broken\"\n" +
		"signals = [\"logs\"]\n")

	res, err := env.run()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(res.Misconfigured) != 1 || res.Misconfigured[0] != "broken" {
		t.Errorf("Misconfigured: got %v, want [broken]", res.Misconfigured)
	}
	var buf strings.Builder
	res.Summarize(&buf)
	if !strings.Contains(buf.String(), "broken") {
		t.Errorf("Summarize should name the misconfigured target: %q", buf.String())
	}
}

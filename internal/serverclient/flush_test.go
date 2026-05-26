package serverclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/backfill"
	"github.com/ishii1648/agent-telemetry/internal/syncdb/schema"
)

// flushTestEnv stands up a temp HOME, a DB seeded with raw events, a config
// pointing at a fake /v1/logs server, and lets the test control the OTLP
// partialSuccess response.
type flushTestEnv struct {
	t          *testing.T
	dbPath     string
	configPath string
	statePath  string
	server     *httptest.Server
	rejected   int
	rejectMsg  string
	status     int
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
		t:          t,
		dbPath:     filepath.Join(home, ".claude", "agent-telemetry.db"),
		configPath: filepath.Join(configDir, "config.toml"),
		statePath:  agent.Claude().StatePath(),
		status:     http.StatusOK,
	}
	env.server = httptest.NewServer(http.HandlerFunc(env.handle))
	t.Cleanup(env.server.Close)
	body := "[server]\nendpoint = \"" + env.server.URL + "\"\ntoken = \"test-token\"\n"
	if err := os.WriteFile(env.configPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return env
}

func (e *flushTestEnv) handle(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(flushResponse{PartialSuccess: partialSuccess{
		RejectedLogRecords: e.rejected,
		ErrorMessage:       e.rejectMsg,
	}})
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
	ar := res.PerAgent["claude"]
	if ar.Rejected != 2 {
		t.Errorf("Rejected: got %d, want 2", ar.Rejected)
	}
	if ar.RejectedError != "missing event_id" {
		t.Errorf("RejectedError: got %q", ar.RejectedError)
	}
	if !ar.StateUpdated {
		t.Error("cursor should advance past rejected batch")
	}
	if got := env.loadState().LastFlushedSequence; got != 3 {
		t.Errorf("LastFlushedSequence: got %d, want 3 (cursor must advance despite rejects)", got)
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
	if got := env.loadState().LastFlushedSequence; got != 0 {
		t.Errorf("LastFlushedSequence: got %d, want 0 (cursor must not advance on transport failure)", got)
	}
}

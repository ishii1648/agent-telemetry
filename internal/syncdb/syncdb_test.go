package syncdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishii1648/agent-telemetry/internal/agent"
)

func TestRunWithPaths(t *testing.T) {
	dir := t.TempDir()

	// Create transcript files so sessions are not ghost
	t1Path := filepath.Join(dir, "t1.jsonl")
	os.WriteFile(t1Path, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":400},"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)
	t2Path := filepath.Join(dir, "t2.jsonl")
	os.WriteFile(t2Path, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":200,"output_tokens":40,"cache_creation_input_tokens":60,"cache_read_input_tokens":800},"content":[{"type":"tool_use","name":"Edit"}]}}`+"\n",
	), 0644)
	t3Path := filepath.Join(dir, "t3.jsonl")
	os.WriteFile(t3Path, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n",
	), 0644)

	// Create session-index.jsonl (is_merged=true for merged PR sessions)
	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","ended_at":"2026-03-01 12:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"feat/add-metrics","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"`+t1Path+`","parent_session_id":"","backfill_checked":false,"is_merged":true,"review_comments":3,"changes_requested":1}`+"\n"+
			`{"timestamp":"2026-03-01 11:00:00","ended_at":"2026-03-01 11:30:00","session_id":"s2","cwd":"/tmp","repo":"user/repo","branch":"feat/add-metrics","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"`+t2Path+`","parent_session_id":"","backfill_checked":false,"is_merged":true,"review_comments":3,"changes_requested":1}`+"\n"+
			`{"timestamp":"2026-03-01 12:00:00","session_id":"s3","cwd":"/tmp","repo":"ishii1648/dotfiles","branch":"main","pr_urls":["https://github.com/ishii1648/dotfiles/pull/5"],"transcript":"`+t3Path+`","parent_session_id":"","backfill_checked":false,"is_merged":true}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	err := RunWithPaths(indexPath, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Verify DB
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Check sessions count
	var sessionCount int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionCount)
	if sessionCount != 3 {
		t.Errorf("sessions count: got %d, want 3", sessionCount)
	}

	// Check new columns on sessions
	var isMerged int
	var taskType string
	var endedAt string
	var reviewComments int
	var changesRequested int
	db.QueryRow("SELECT is_merged, task_type, ended_at, review_comments, changes_requested FROM sessions WHERE session_id = 's1'").Scan(&isMerged, &taskType, &endedAt, &reviewComments, &changesRequested)
	if isMerged != 1 {
		t.Errorf("is_merged: got %d, want 1", isMerged)
	}
	if taskType != "feat" {
		t.Errorf("task_type: got %q, want %q", taskType, "feat")
	}
	if endedAt != "2026-03-01 12:00:00" {
		t.Errorf("ended_at: got %q", endedAt)
	}
	if reviewComments != 3 {
		t.Errorf("review_comments: got %d, want 3", reviewComments)
	}
	if changesRequested != 1 {
		t.Errorf("changes_requested: got %d, want 1", changesRequested)
	}

	// Check transcript_stats count
	var statsCount int
	db.QueryRow("SELECT COUNT(*) FROM transcript_stats").Scan(&statsCount)
	if statsCount != 3 {
		t.Errorf("transcript_stats count: got %d, want 3", statsCount)
	}

	// Check pr_metrics VIEW excludes dotfiles repo and only includes merged PRs
	var prMetricsCount int
	db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&prMetricsCount)
	if prMetricsCount != 1 {
		t.Errorf("pr_metrics count: got %d, want 1 (dotfiles excluded)", prMetricsCount)
	}

	// Check pr_metrics aggregation
	var prURL string
	var sessCount int
	var prTaskType string
	var prReviewComments int
	var prChangesRequested int
	var inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens, totalTokens int64
	var tokensPerSession, tokensPerToolUse, prPerMillionTokens float64
	db.QueryRow(`SELECT pr_url, task_type, session_count, review_comments, changes_requested,
		input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, total_tokens,
		tokens_per_session, tokens_per_tool_use, pr_per_million_tokens
		FROM pr_metrics`).Scan(
		&prURL, &prTaskType, &sessCount, &prReviewComments, &prChangesRequested,
		&inputTokens, &outputTokens, &cacheWriteTokens, &cacheReadTokens, &totalTokens,
		&tokensPerSession, &tokensPerToolUse, &prPerMillionTokens,
	)
	if prURL != "https://github.com/user/repo/pull/1" {
		t.Errorf("pr_url: got %s", prURL)
	}
	if prTaskType != "feat" {
		t.Errorf("task_type: got %q, want %q", prTaskType, "feat")
	}
	if sessCount != 2 {
		t.Errorf("session_count: got %d, want 2", sessCount)
	}
	if prReviewComments != 3 {
		t.Errorf("review_comments: got %d, want 3", prReviewComments)
	}
	if prChangesRequested != 1 {
		t.Errorf("changes_requested: got %d, want 1", prChangesRequested)
	}
	if inputTokens != 300 {
		t.Errorf("input_tokens: got %d, want 300", inputTokens)
	}
	if outputTokens != 60 {
		t.Errorf("output_tokens: got %d, want 60", outputTokens)
	}
	if cacheWriteTokens != 90 {
		t.Errorf("cache_write_tokens: got %d, want 90", cacheWriteTokens)
	}
	if cacheReadTokens != 1200 {
		t.Errorf("cache_read_tokens: got %d, want 1200", cacheReadTokens)
	}
	if totalTokens != 1650 {
		t.Errorf("total_tokens: got %d, want 1650", totalTokens)
	}
	if tokensPerSession != 825.0 {
		t.Errorf("tokens_per_session: got %.1f, want 825.0", tokensPerSession)
	}
	if tokensPerToolUse != 825.0 {
		t.Errorf("tokens_per_tool_use: got %.1f, want 825.0", tokensPerToolUse)
	}
	if prPerMillionTokens != 606.06 {
		t.Errorf("pr_per_million_tokens: got %.2f, want 606.06", prPerMillionTokens)
	}

	var concurrencyRows int
	db.QueryRow("SELECT COUNT(*) FROM session_concurrency_daily").Scan(&concurrencyRows)
	if concurrencyRows != 1 {
		t.Errorf("session_concurrency_daily count: got %d, want 1", concurrencyRows)
	}
	var avgConcurrent float64
	var peakConcurrent int
	db.QueryRow("SELECT avg_concurrent_sessions, peak_concurrent_sessions FROM session_concurrency_daily WHERE day = '2026-03-01'").Scan(&avgConcurrent, &peakConcurrent)
	if avgConcurrent != 1.5 {
		t.Errorf("avg_concurrent_sessions: got %.2f, want 1.50", avgConcurrent)
	}
	if peakConcurrent != 2 {
		t.Errorf("peak_concurrent_sessions: got %d, want 2", peakConcurrent)
	}

	var weeklyPeak int
	db.QueryRow("SELECT peak_concurrent_sessions FROM session_concurrency_weekly WHERE week_start = '2026-02-23'").Scan(&weeklyPeak)
	if weeklyPeak != 2 {
		t.Errorf("weekly peak_concurrent_sessions: got %d, want 2", weeklyPeak)
	}
}

func TestRunWithPaths_MergedFilter(t *testing.T) {
	dir := t.TempDir()

	tPath := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tPath, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)

	// Create sessions: one merged, one not merged
	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"feat/a","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"`+tPath+`","parent_session_id":"","is_merged":true}`+"\n"+
			`{"timestamp":"2026-03-01 11:00:00","session_id":"s2","cwd":"/tmp","repo":"user/repo","branch":"feat/b","pr_urls":["https://github.com/user/repo/pull/2"],"transcript":"`+tPath+`","parent_session_id":"","is_merged":false}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Only merged PR should appear in pr_metrics
	var count int
	db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&count)
	if count != 1 {
		t.Errorf("pr_metrics count: got %d, want 1 (only merged PR)", count)
	}

	var prURL string
	db.QueryRow("SELECT pr_url FROM pr_metrics").Scan(&prURL)
	if prURL != "https://github.com/user/repo/pull/1" {
		t.Errorf("expected merged PR only, got: %s", prURL)
	}
}

func TestRunWithPaths_JoinInflationFix(t *testing.T) {
	dir := t.TempDir()

	tPath := filepath.Join(dir, "t.jsonl")
	// Session with 5 tool_use entries
	os.WriteFile(tPath, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Edit"},{"type":"tool_use","name":"Write"},{"type":"tool_use","name":"Grep"},{"type":"tool_use","name":"Glob"}]}}`+"\n",
	), 0644)

	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"feat/x","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"`+tPath+`","parent_session_id":"","is_merged":true}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var toolUseTotal int
	db.QueryRow("SELECT tool_use_total FROM pr_metrics").Scan(&toolUseTotal)
	if toolUseTotal != 5 {
		t.Errorf("tool_use_total: got %d, want 5", toolUseTotal)
	}
}

func TestRunWithPaths_DummyPRURL(t *testing.T) {
	dir := t.TempDir()

	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"feat","pr_urls":["https://github.com/org/repo/pull/123"],"transcript":"","parent_session_id":"","backfill_checked":false}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	err := RunWithPaths(indexPath, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Dummy PR URL should be stored as empty
	var prURL string
	db.QueryRow("SELECT pr_url FROM sessions WHERE session_id = 's1'").Scan(&prURL)
	if prURL != "" {
		t.Errorf("dummy PR URL should be empty, got: %s", prURL)
	}
}

func TestRunForAgents_MixedClaudeAndCodex(t *testing.T) {
	dir := t.TempDir()

	// Claude transcript
	claudeT := filepath.Join(dir, "claude.jsonl")
	os.WriteFile(claudeT, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":40},"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)
	claudeIdx := filepath.Join(dir, ".claude", "session-index.jsonl")
	os.MkdirAll(filepath.Dir(claudeIdx), 0755)
	os.WriteFile(claudeIdx, []byte(
		`{"coding_agent":"claude","agent_version":"1.2.3","timestamp":"2026-03-01 10:00:00","ended_at":"2026-03-01 10:30:00","session_id":"c1","cwd":"/tmp","repo":"u/r","branch":"feat/x","pr_urls":["https://github.com/u/r/pull/1"],"transcript":"`+claudeT+`","parent_session_id":"","is_merged":true}`+"\n",
	), 0644)

	// Codex rollout (with reasoning + cached_input tokens)
	codexT := filepath.Join(dir, "rollout.jsonl")
	os.WriteFile(codexT, []byte(
		`{"timestamp":"2026-03-02T10:00:00Z","type":"session_meta","payload":{"id":"x1","cli_version":"0.128.0"}}`+"\n"+
			`{"timestamp":"2026-03-02T10:00:01Z","type":"turn_context","payload":{"model":"gpt-5.5"}}`+"\n"+
			`{"timestamp":"2026-03-02T10:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`+"\n"+
			`{"timestamp":"2026-03-02T10:00:03Z","type":"response_item","payload":{"type":"function_call","name":"exec_command"}}`+"\n"+
			`{"timestamp":"2026-03-02T10:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"cached_input_tokens":200,"output_tokens":80,"reasoning_output_tokens":50,"total_tokens":830}}}}`+"\n",
	), 0644)
	codexIdx := filepath.Join(dir, ".codex", "session-index.jsonl")
	os.MkdirAll(filepath.Dir(codexIdx), 0755)
	os.WriteFile(codexIdx, []byte(
		`{"coding_agent":"codex","agent_version":"0.128.0","timestamp":"2026-03-02 10:00:00","ended_at":"2026-03-02 10:05:00","session_id":"x1","cwd":"/tmp","repo":"u/r","branch":"feat/y","pr_urls":["https://github.com/u/r/pull/2"],"transcript":"`+codexT+`","parent_session_id":"","is_merged":true,"end_reason":"stop"}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	if err := runWithSources([]agentSource{
		{Agent: mustAgent("claude", filepath.Dir(claudeIdx)), IndexPath: claudeIdx},
		{Agent: mustAgent("codex", filepath.Dir(codexIdx)), IndexPath: codexIdx},
	}, dbPath); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Sessions: 2 rows, one per agent
	var n int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n)
	if n != 2 {
		t.Errorf("sessions count = %d, want 2", n)
	}

	// Codex agent_version stored
	var ver string
	db.QueryRow("SELECT agent_version FROM sessions WHERE coding_agent='codex'").Scan(&ver)
	if ver != "0.128.0" {
		t.Errorf("codex agent_version = %q", ver)
	}

	// Codex transcript_stats has reasoning_tokens
	var reasoning int64
	db.QueryRow("SELECT reasoning_tokens FROM transcript_stats WHERE coding_agent='codex'").Scan(&reasoning)
	if reasoning != 50 {
		t.Errorf("codex reasoning_tokens = %d, want 50", reasoning)
	}

	// pr_metrics groups by (pr_url, coding_agent) — 2 distinct rows
	db.QueryRow("SELECT COUNT(*) FROM pr_metrics").Scan(&n)
	if n != 2 {
		t.Errorf("pr_metrics rows = %d, want 2", n)
	}

	// Codex pr total_tokens includes reasoning
	var total int64
	db.QueryRow("SELECT total_tokens FROM pr_metrics WHERE coding_agent='codex'").Scan(&total)
	wantTotal := int64(500 + 80 + 200 + 50) // input + output + cache_read + reasoning
	if total != wantTotal {
		t.Errorf("codex total_tokens = %d, want %d", total, wantTotal)
	}
}

func TestRunForAgents_SessionIDCollisionAcrossAgents(t *testing.T) {
	// Same session_id in both agents must coexist (composite PK).
	dir := t.TempDir()
	claudeIdx := filepath.Join(dir, "claude.jsonl")
	os.WriteFile(claudeIdx, []byte(
		`{"coding_agent":"claude","timestamp":"2026-03-01 10:00:00","session_id":"shared","cwd":"/tmp","repo":"u/r","branch":"main","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n",
	), 0644)
	codexIdx := filepath.Join(dir, "codex.jsonl")
	os.WriteFile(codexIdx, []byte(
		`{"coding_agent":"codex","timestamp":"2026-03-02 10:00:00","session_id":"shared","cwd":"/tmp","repo":"u/r","branch":"main","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n",
	), 0644)
	dbPath := filepath.Join(dir, "agent-telemetry.db")
	err := runWithSources([]agentSource{
		{Agent: mustAgent("claude", dir), IndexPath: claudeIdx},
		{Agent: mustAgent("codex", dir), IndexPath: codexIdx},
	}, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	db, _ := sql.Open("sqlite", dbPath)
	defer db.Close()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE session_id='shared'").Scan(&n)
	if n != 2 {
		t.Errorf("collision: got %d rows, want 2", n)
	}
}

func TestRunWithPaths_IdempotentRerun(t *testing.T) {
	dir := t.TempDir()

	tPath := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tPath, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)

	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","ended_at":"2026-03-01 10:30:00","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat/x","pr_urls":["https://github.com/u/r/pull/1"],"transcript":"`+tPath+`","parent_session_id":"","is_merged":true,"review_comments":1}`+"\n",
	), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")

	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Mutate the source row so the second run must overwrite, not duplicate.
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","ended_at":"2026-03-01 10:30:00","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat/x","pr_urls":["https://github.com/u/r/pull/1"],"transcript":"`+tPath+`","parent_session_id":"","is_merged":true,"review_comments":7}`+"\n",
	), 0644)

	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatalf("second run: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var sessionRows, statsRows int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionRows)
	if sessionRows != 1 {
		t.Errorf("sessions row count: got %d, want 1 (UPSERT must not duplicate)", sessionRows)
	}
	db.QueryRow("SELECT COUNT(*) FROM transcript_stats").Scan(&statsRows)
	if statsRows != 1 {
		t.Errorf("transcript_stats row count: got %d, want 1 (UPSERT must not duplicate)", statsRows)
	}

	var reviewComments int
	db.QueryRow("SELECT review_comments FROM sessions WHERE session_id = 's1'").Scan(&reviewComments)
	if reviewComments != 7 {
		t.Errorf("review_comments: got %d, want 7 (latest value must overwrite)", reviewComments)
	}

	var hash string
	db.QueryRow("SELECT value FROM schema_meta WHERE key = 'schema_hash'").Scan(&hash)
	if hash == "" {
		t.Error("schema_meta schema_hash row missing after run")
	}
}

func TestRunWithPaths_StaleSchemaFallback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent-telemetry.db")

	// Pre-create a DB whose recorded hash does not match the embedded schema.
	// The legacy permission_events table from earlier versions is also created
	// to confirm the rebuild path still drops it cleanly.
	pre, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pre.Exec(`
CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO schema_meta (key, value) VALUES ('schema_hash', 'stale-hash-from-old-build');
CREATE TABLE permission_events (id INTEGER PRIMARY KEY);
`); err != nil {
		t.Fatalf("seed legacy db: %v", err)
	}
	pre.Close()

	tPath := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tPath, []byte(
		`{"type":"user","message":{"content":"hello"}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)

	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat/x","pr_urls":[],"transcript":"`+tPath+`","parent_session_id":"","is_merged":false}`+"\n",
	), 0644)

	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatalf("run with stale hash: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var hash string
	if err := db.QueryRow("SELECT value FROM schema_meta WHERE key = 'schema_hash'").Scan(&hash); err != nil {
		t.Fatalf("schema_meta read: %v", err)
	}
	if hash == "stale-hash-from-old-build" {
		t.Error("schema_hash was not refreshed after rebuild")
	}

	// Legacy permission_events table must be dropped by the rebuild.
	var legacyExists int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='permission_events'").Scan(&legacyExists)
	if legacyExists != 0 {
		t.Error("legacy permission_events table not dropped during rebuild")
	}

	var sessionRows int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionRows)
	if sessionRows != 1 {
		t.Errorf("sessions row count: got %d, want 1", sessionRows)
	}
}

// TestRunWithPaths_BusyTimeoutWaitsForWriter verifies that a concurrent
// sync-db invocation waits for an in-flight writer instead of failing
// immediately with SQLITE_BUSY. Without busy_timeout configured on the
// DSN, the goroutine below would error out the moment it hit the locked DB.
func TestRunWithPaths_BusyTimeoutWaitsForWriter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent-telemetry.db")
	indexPath := filepath.Join(dir, "session-index.jsonl")
	os.WriteFile(indexPath, []byte(
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"main","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n",
	), 0644)
	// Bootstrap schema + WAL so the lock holder doesn't race with ensureSchema.
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatalf("bootstrap run: %v", err)
	}

	// Hold a write lock from a separate connection.
	blocker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, coding_agent, agent_version, timestamp, cwd, repo, branch, pr_url, pr_title, transcript, parent_session_id, ended_at, end_reason, is_subagent, backfill_checked, is_merged, task_type, review_comments, changes_requested)
		VALUES ('lock','claude','','','','','','','','','','','',0,0,0,'',0,0)`,
	); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- RunWithPaths(indexPath, dbPath) }()

	// Give the goroutine time to hit the writer lock.
	time.Sleep(200 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("concurrent RunWithPaths failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent RunWithPaths timed out — busy_timeout not honored?")
	}
}

func mustAgent(name, dir string) *agent.Agent {
	return &agent.Agent{Name: name, DataDir: dir}
}

func TestExtractTaskType(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"feat/add-metrics", "feat"},
		{"fix/bug-42", "fix"},
		{"docs/update-readme", "docs"},
		{"chore/cleanup", "chore"},
		{"main", ""},
		{"develop", ""},
		{"release/v1.0", ""},
		{"feat", ""},
	}
	for _, tt := range tests {
		got := ExtractTaskType(tt.branch)
		if got != tt.want {
			t.Errorf("ExtractTaskType(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestShortenPRURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/user/repo/pull/42", "user/repo#42"},
		{"https://example.com/foo", "https://example.com/foo"},
	}
	for _, tt := range tests {
		got := ShortenPRURL(tt.input)
		if got != tt.want {
			t.Errorf("ShortenPRURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestRunWithPaths_DeterministicEventID guards the append-only events store
// against unbounded growth: the INSTEAD OF INSERT triggers derive event_id
// deterministically from content (at_event_id) and INSERT OR IGNORE, so
// re-running sync-db over unchanged source data must NOT append duplicate
// events. When content actually changes (is_merged flips), exactly one new
// snapshot row must appear and the view must reflect the latest value.
func TestRunWithPaths_DeterministicEventID(t *testing.T) {
	dir := t.TempDir()
	tPath := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tPath, []byte(
		`{"type":"user","message":{"content":"hi"}}`+"\n"+
			`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","name":"Read"}]}}`+"\n",
	), 0644)

	indexPath := filepath.Join(dir, "session-index.jsonl")
	row := func(merged bool) string {
		m := "false"
		if merged {
			m = "true"
		}
		return `{"timestamp":"2026-03-01 10:00:00","ended_at":"2026-03-01 12:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"feat/x","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"` + tPath + `","parent_session_id":"","backfill_checked":true,"is_merged":` + m + `}` + "\n"
	}
	os.WriteFile(indexPath, []byte(row(false)), 0644)

	dbPath := filepath.Join(dir, "agent-telemetry.db")
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	countEvents := func() int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := countEvents()
	if first == 0 {
		t.Fatalf("expected events after first sync, got 0")
	}

	// Re-running over identical source data must not append any new events.
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatal(err)
	}
	if got := countEvents(); got != first {
		t.Errorf("events grew on no-op re-sync: got %d, want %d (deterministic event_id should dedup)", got, first)
	}

	// Flipping is_merged is a genuine content change: exactly one new
	// agent.pr.observed snapshot should be appended, and the view latest-wins.
	os.WriteFile(indexPath, []byte(row(true)), 0644)
	if err := RunWithPaths(indexPath, dbPath); err != nil {
		t.Fatal(err)
	}
	if got := countEvents(); got != first+1 {
		t.Errorf("content change should append exactly one event: got %d, want %d", got, first+1)
	}
	var isMerged int
	db.QueryRow("SELECT is_merged FROM sessions WHERE session_id='s1'").Scan(&isMerged)
	if isMerged != 1 {
		t.Errorf("view should reflect latest is_merged: got %d, want 1", isMerged)
	}
}

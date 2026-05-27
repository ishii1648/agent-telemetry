package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

func TestExtractPRURLs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"no PR", `{"output":"hello"}`, nil},
		{"single PR in stdout", `{"stdout":"https://github.com/u/r/pull/42 created"}`, []string{"https://github.com/u/r/pull/42"}},
		{"multiple distinct PRs", `["https://github.com/u/r/pull/1","more https://github.com/u/r/pull/2"]`, []string{"https://github.com/u/r/pull/1", "https://github.com/u/r/pull/2"}},
		{"duplicate PRs deduped", `"https://github.com/u/r/pull/1 https://github.com/u/r/pull/1"`, []string{"https://github.com/u/r/pull/1"}},
		{"PR-like but issue ignored", `"https://github.com/u/r/issues/9"`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractPRURLs(json.RawMessage(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i, u := range c.want {
				if got[i] != u {
					t.Errorf("[%d] got %q, want %q", i, got[i], u)
				}
			}
		})
	}
}

func TestIsGHPRCreateCommand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"gh pr create", `{"command":"gh pr create --draft"}`, true},
		{"leading spaces", `{"command":"  gh pr create"}`, true},
		{"gh pr view ignored", `{"command":"gh pr view 42"}`, false},
		{"echo ignored", `{"command":"echo gh pr create"}`, false},
		{"missing command", `{"description":"gh pr create"}`, false},
		{"invalid json", `{`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isGHPRCreateCommand(json.RawMessage(c.in)); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestRunPostToolUse_AppendsURL(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}
	idx := a.SessionIndexPath()

	if err := os.WriteFile(idx,
		[]byte(`{"coding_agent":"codex","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n"),
		0644); err != nil {
		t.Fatal(err)
	}

	input := &HookInput{
		ToolInput:    json.RawMessage(`{"command":"gh pr create --draft"}`),
		SessionID:    "s1",
		ToolResponse: json.RawMessage(`{"stdout":"created https://github.com/u/r/pull/7"}`),
	}
	if err := RunPostToolUse(input, a); err != nil {
		t.Fatal(err)
	}

	_, sessions, err := sessionindex.ReadAll(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].PRURLs) != 1 || sessions[0].PRURLs[0] != "https://github.com/u/r/pull/7" {
		t.Fatalf("pr_urls not appended: %+v", sessions)
	}
}

func TestRunPostToolUse_NoURLIsNoOp(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}
	idx := a.SessionIndexPath()

	original := `{"coding_agent":"codex","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"transcript":"","parent_session_id":""}` + "\n"
	if err := os.WriteFile(idx, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	input := &HookInput{
		SessionID:    "s1",
		ToolInput:    json.RawMessage(`{"command":"gh pr create --draft"}`),
		ToolResponse: json.RawMessage(`{"stdout":"hello"}`),
	}
	if err := RunPostToolUse(input, a); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(idx)
	if string(got) != original {
		t.Errorf("file modified for no-URL response:\n  before: %q\n  after:  %q", original, got)
	}
}

// TestRunPostToolUse_SkipsPinnedSession verifies the core fix: once a
// session is pinned, regex-extracted PR URLs from tool output (e.g. a
// `gh pr view 999` for someone else's PR) must not pollute pr_urls.
func TestRunPostToolUse_SkipsPinnedSession(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}
	idx := a.SessionIndexPath()

	original := `{"coding_agent":"codex","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":["https://github.com/u/r/pull/42"],"pr_pinned":true,"transcript":"","parent_session_id":""}` + "\n"
	if err := os.WriteFile(idx, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// Tool output references a totally unrelated PR — exactly the
	// scenario where the old code mis-attributed pr_urls.
	input := &HookInput{
		ToolInput:    json.RawMessage(`{"command":"gh pr create --draft"}`),
		SessionID:    "s1",
		ToolResponse: json.RawMessage(`{"stdout":"viewed https://github.com/other/repo/pull/999"}`),
	}
	if err := RunPostToolUse(input, a); err != nil {
		t.Fatal(err)
	}

	_, sessions, err := sessionindex.ReadAll(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if len(sessions[0].PRURLs) != 1 || sessions[0].PRURLs[0] != "https://github.com/u/r/pull/42" {
		t.Fatalf("pr_urls leaked stray URL into pinned session: %v", sessions[0].PRURLs)
	}
	if !sessions[0].PRPinned {
		t.Fatal("pr_pinned must remain true")
	}
}

func TestRunPostToolUse_NonCreateCommandDoesNotScrapePRURL(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}
	idx := a.SessionIndexPath()

	original := `{"coding_agent":"codex","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"transcript":"","parent_session_id":""}` + "\n"
	if err := os.WriteFile(idx, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	input := &HookInput{
		SessionID:    "s1",
		ToolInput:    json.RawMessage(`{"command":"gh pr view 999"}`),
		ToolResponse: json.RawMessage(`{"stdout":"https://github.com/other/repo/pull/999"}`),
	}
	if err := RunPostToolUse(input, a); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(idx)
	if string(got) != original {
		t.Errorf("file modified for non-create command:\n  before: %q\n  after:  %q", original, got)
	}
}

// SessionStart should land in the agent's data dir, not under ~/.claude.
func TestRunSessionStart_WritesToAgentDir(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}

	input := &HookInput{
		SessionID:      "x1",
		CWD:            t.TempDir(), // not a git repo
		TranscriptPath: "/tmp/r.jsonl",
		CliVersion:     "0.128.0",
	}
	if err := RunSessionStart(input, a); err != nil {
		t.Fatal(err)
	}

	idx := a.SessionIndexPath()
	if _, err := os.Stat(idx); err != nil {
		t.Fatalf("session-index missing: %v", err)
	}
	_, sessions, err := sessionindex.ReadAll(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	if sessions[0].CodingAgent != "codex" {
		t.Errorf("coding_agent = %q", sessions[0].CodingAgent)
	}
	if sessions[0].AgentVersion != "0.128.0" {
		t.Errorf("agent_version = %q", sessions[0].AgentVersion)
	}
	// debug log written to <dir>/logs/...
	if _, err := os.Stat(filepath.Join(dir, "logs", "session-index-debug.log")); err != nil {
		t.Errorf("debug log missing: %v", err)
	}
}

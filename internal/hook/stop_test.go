package hook

import (
	"errors"
	"os"
	"testing"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

// withSpawnStub swaps the package-level spawnBackfillWorker with a fake for
// the duration of a test so RunStop never exec-s the real binary.
func withSpawnStub(t *testing.T, fn func(agentName, sessionID string) error) {
	t.Helper()
	orig := spawnBackfillWorker
	spawnBackfillWorker = fn
	t.Cleanup(func() { spawnBackfillWorker = orig })
}

func TestRunStop_SpawnsWorkerWithSession(t *testing.T) {
	var gotAgent, gotSession string
	called := 0
	withSpawnStub(t, func(a, s string) error {
		called++
		gotAgent, gotSession = a, s
		return nil
	})

	a := agent.Claude()
	if err := RunStop(&HookInput{SessionID: "s1"}, a); err != nil {
		t.Fatalf("RunStop: %v", err)
	}
	if called != 1 {
		t.Fatalf("spawn called %d times, want 1", called)
	}
	if gotAgent != a.Name {
		t.Errorf("agent forwarded = %q, want %q", gotAgent, a.Name)
	}
	if gotSession != "s1" {
		t.Errorf("session forwarded = %q, want s1", gotSession)
	}
}

func TestRunStop_SpawnErrorPropagates(t *testing.T) {
	withSpawnStub(t, func(_, _ string) error { return errors.New("exec failed") })
	if err := RunStop(&HookInput{SessionID: "s1"}, agent.Claude()); err == nil {
		t.Fatal("expected spawn error to propagate")
	}
}

func TestRunStop_CodexOverwritesEndedAt(t *testing.T) {
	// Codex has no SessionEnd, so the Stop hook records ended_at/end_reason
	// synchronously (the only synchronous write left in the hot path).
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}
	idx := a.SessionIndexPath()
	if err := os.WriteFile(idx, []byte(
		`{"coding_agent":"codex","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	withSpawnStub(t, func(_, _ string) error { return nil })

	if err := RunStop(&HookInput{SessionID: "s1"}, a); err != nil {
		t.Fatalf("RunStop: %v", err)
	}

	_, sessions, err := sessionindex.ReadAll(idx)
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].EndedAt == "" {
		t.Fatal("ended_at should be set for Codex Stop")
	}
	if sessions[0].EndReason != "stop" {
		t.Errorf("end_reason = %q, want stop", sessions[0].EndReason)
	}
}

func TestRunStop_ClaudeDoesNotWriteEndedAt(t *testing.T) {
	// Claude has a dedicated SessionEnd hook; Stop must not touch ended_at.
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}
	idx := a.SessionIndexPath()
	if err := os.WriteFile(idx, []byte(
		`{"coding_agent":"claude","session_id":"s1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"transcript":"","parent_session_id":""}`+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	withSpawnStub(t, func(_, _ string) error { return nil })

	if err := RunStop(&HookInput{SessionID: "s1"}, a); err != nil {
		t.Fatalf("RunStop: %v", err)
	}

	_, sessions, _ := sessionindex.ReadAll(idx)
	if sessions[0].EndedAt != "" {
		t.Fatal("Claude Stop must not write ended_at (SessionEnd owns that)")
	}
}

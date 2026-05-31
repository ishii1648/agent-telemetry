package setup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ishii1648/agent-telemetry/internal/agent"
)

func TestRunWith_ClaudeOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWith(&buf, agent.Claude()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "session-start --agent claude") {
		t.Errorf("missing claude session-start example:\n%s", out)
	}
	// Stop は応答ターンごとに発火するので非ブロッキング登録 (async) を必須にする。
	if !strings.Contains(out, "hook stop --agent claude\", \"async\": true") {
		t.Errorf("claude Stop example must register with \"async\": true:\n%s", out)
	}
	if strings.Contains(out, "PostToolUse") {
		t.Error("Claude output should not include PostToolUse")
	}
}

func TestRunWith_CodexOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWith(&buf, agent.Codex()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"session-start --agent codex",
		"stop --agent codex",
		"post-tool-use --agent codex",
		"SessionEnd イベントが無いため",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("codex output missing %q\n%s", want, out)
		}
	}
}

func TestRunWith_NilShowsBoth(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWith(&buf, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Claude Code") || !strings.Contains(out, "Codex CLI") {
		t.Errorf("nil agent should show both:\n%s", out)
	}
}

func TestHookSpecsFor(t *testing.T) {
	if got := HookSpecsFor(agent.NameClaude); len(got) != len(ClaudeHookSpecs) {
		t.Errorf("claude specs count = %d", len(got))
	}
	if got := HookSpecsFor(agent.NameCodex); len(got) != len(CodexHookSpecs) {
		t.Errorf("codex specs count = %d", len(got))
	}
	if got := HookSpecsFor(""); len(got) != len(ClaudeHookSpecs) {
		t.Errorf("default → claude specs, got %d", len(got))
	}
}

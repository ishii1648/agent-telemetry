package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ishii1648/agent-telemetry/internal/agent"
)

// MaxInputBytes caps a single hook stdin payload. A hook JSON is tiny in
// practice (tool_input + tool_response, typically << 1 MB); 50 MB matches
// serverpipe.MaxPayloadBytes and is pure insurance against an unbounded
// io.ReadAll on a hostile/runaway agent session (see issue 0060). It is a
// var, not a const, so tests can shrink it without generating 50 MB.
var MaxInputBytes int64 = 50 * 1024 * 1024

// HookInput is the unified hook payload accepted from both Claude Code and
// Codex CLI. Fields that exist for only one agent are present as optional;
// downstream code reads them through HookInput.AgentVersion etc. so the
// per-agent quirks stay isolated.
type HookInput struct {
	// Common
	SessionID       string          `json:"session_id"`
	ToolName        string          `json:"tool_name"`
	ToolInput       json.RawMessage `json:"tool_input"`
	ToolResponse    json.RawMessage `json:"tool_response"`
	CWD             string          `json:"cwd"`
	TranscriptPath  string          `json:"transcript_path"`
	ParentSessionID string          `json:"parent_session_id"`
	Reason          string          `json:"reason"`

	// Claude
	Version string `json:"version"`

	// Codex (best-effort field names; Codex does not publish a stable
	// hook-input contract yet, so we capture every plausible synonym
	// and pick the first non-empty one).
	Model      string `json:"model"`
	TurnID     string `json:"turn_id"`
	Source     string `json:"source"`
	CliVersion string `json:"cli_version"`
}

// AgentVersion returns the agent version string, preferring the hook
// input field, then the per-agent env var, then empty. Hook-time should
// never shell out for a version string — the goal is sub-millisecond hooks.
func (h *HookInput) AgentVersion(agentName string) string {
	switch agentName {
	case agent.NameCodex:
		if h.CliVersion != "" {
			return h.CliVersion
		}
		if v := os.Getenv("CODEX_CLI_VERSION"); v != "" {
			return v
		}
	default:
		if h.Version != "" {
			return h.Version
		}
		if v := os.Getenv("CLAUDE_CODE_VERSION"); v != "" {
			return v
		}
	}
	return ""
}

// ReadInput reads and parses JSON hook input from stdin.
//
// Empty stdin is treated as a non-error: the legacy Claude Stop hook
// fires without any stdin payload, and we don't want to break it.
//
// The read is bounded by MaxInputBytes so a hostile or runaway session
// cannot pin CPU/memory by feeding an endless stdin stream (issue 0060).
func ReadInput() (*HookInput, error) {
	return readInput(os.Stdin)
}

func readInput(r io.Reader) (*HookInput, error) {
	// +1 so we can distinguish "exactly at the cap" from "over the cap".
	data, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxInputBytes {
		return nil, fmt.Errorf("hook input exceeds %d bytes", MaxInputBytes)
	}
	if len(data) == 0 {
		return &HookInput{}, nil
	}
	var input HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, err
	}
	return &input, nil
}

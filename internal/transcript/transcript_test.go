package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
	f.Close()
	return p
}

func TestParse_BasicStats(t *testing.T) {
	p := writeTempTranscript(t, []string{
		`{"type":"user","message":{"content":"hello"}}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":400},"content":[{"type":"tool_use","name":"Read"},{"type":"tool_use","name":"Edit"}]}}`,
		`{"type":"user","message":{"content":"fix this"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-5","usage":{"input_tokens":200,"output_tokens":40,"cache_creation_input_tokens":60,"cache_read_input_tokens":800},"content":[{"type":"tool_use","name":"ask-user-question"}]}}`,
	})

	stats := ParseClaude(p)
	if stats.ToolUseTotal != 3 {
		t.Errorf("tool_use_total: got %d, want 3", stats.ToolUseTotal)
	}
	if stats.MidSessionMsgs != 1 {
		t.Errorf("mid_session_msgs: got %d, want 1", stats.MidSessionMsgs)
	}
	if stats.AskUserQuestion != 1 {
		t.Errorf("ask_user_question: got %d, want 1", stats.AskUserQuestion)
	}
	if stats.InputTokens != 300 {
		t.Errorf("input_tokens: got %d, want 300", stats.InputTokens)
	}
	if stats.OutputTokens != 60 {
		t.Errorf("output_tokens: got %d, want 60", stats.OutputTokens)
	}
	if stats.CacheWriteTokens != 90 {
		t.Errorf("cache_write_tokens: got %d, want 90", stats.CacheWriteTokens)
	}
	if stats.CacheReadTokens != 1200 {
		t.Errorf("cache_read_tokens: got %d, want 1200", stats.CacheReadTokens)
	}
	if stats.Model != "claude-opus-4-5" {
		t.Errorf("model: got %q, want %q", stats.Model, "claude-opus-4-5")
	}
	if stats.IsGhost {
		t.Error("should not be ghost")
	}
}

func TestParse_MissingUsage(t *testing.T) {
	p := writeTempTranscript(t, []string{
		`{"type":"user","message":{"content":"hello"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`,
	})

	stats := ParseClaude(p)
	if stats.InputTokens != 0 || stats.OutputTokens != 0 || stats.CacheWriteTokens != 0 || stats.CacheReadTokens != 0 {
		t.Errorf("missing usage should produce zero token stats: %+v", stats)
	}
	if stats.Model != "" {
		t.Errorf("missing model: got %q, want empty", stats.Model)
	}
}

func TestParse_GhostSession(t *testing.T) {
	// No user messages at all
	p := writeTempTranscript(t, []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
	})

	stats := ParseClaude(p)
	if !stats.IsGhost {
		t.Error("should be ghost")
	}
}

func TestParse_ExcludesToolResultOnly(t *testing.T) {
	p := writeTempTranscript(t, []string{
		`{"type":"user","message":{"content":"initial prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result"}]}}`,
		`{"type":"user","message":{"content":"real message"}}`,
	})

	stats := ParseClaude(p)
	// tool_result-only message should not count as mid-session
	if stats.MidSessionMsgs != 1 {
		t.Errorf("mid_session_msgs: got %d, want 1 (tool_result excluded)", stats.MidSessionMsgs)
	}
}

func TestParse_ExcludesLocalCommand(t *testing.T) {
	p := writeTempTranscript(t, []string{
		`{"type":"user","message":{"content":"initial"}}`,
		`{"type":"user","message":{"content":"<local-command-foo>bar</local-command-foo>"}}`,
	})

	stats := ParseClaude(p)
	if stats.MidSessionMsgs != 0 {
		t.Errorf("mid_session_msgs: got %d, want 0 (local-command excluded)", stats.MidSessionMsgs)
	}
}

func TestParse_DecodedSizeCapTruncatesGracefully(t *testing.T) {
	// Line 1 sets model=EARLY with tokens; many padding lines follow; the
	// final line sets model=LATE. With MaxDecodedBytes shrunk so the scan
	// stops before the final line, parsing must degrade gracefully:
	// EARLY's stats survive and LATE is never reached (no crash/hang).
	lines := []string{
		`{"type":"assistant","message":{"model":"EARLY","usage":{"input_tokens":100}}}`,
	}
	for i := 0; i < 200; i++ {
		lines = append(lines, `{"type":"user","message":{"content":"padding padding padding padding"}}`)
	}
	lines = append(lines, `{"type":"assistant","message":{"model":"LATE"}}`)
	p := writeTempTranscript(t, lines)

	orig := MaxDecodedBytes
	MaxDecodedBytes = 500 // well below the final line's offset
	defer func() { MaxDecodedBytes = orig }()

	stats := ParseClaude(p)
	if stats.Model == "LATE" {
		t.Errorf("model: got %q, want a pre-cap value (final line should be cut off)", stats.Model)
	}
	if stats.InputTokens != 100 {
		t.Errorf("input_tokens: got %d, want 100 (early line within cap)", stats.InputTokens)
	}
}

func TestParse_NonExistentFile(t *testing.T) {
	stats := ParseClaude("/nonexistent/path/transcript.jsonl")
	if !stats.IsGhost {
		t.Error("non-existent file should produce ghost stats")
	}
}

func TestParse_EmptyPath(t *testing.T) {
	stats := ParseClaude("")
	if !stats.IsGhost {
		t.Error("empty path should produce ghost stats")
	}
}

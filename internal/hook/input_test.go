package hook

import (
	"strings"
	"testing"
)

func TestReadInput_WithinCap(t *testing.T) {
	in, err := readInput(strings.NewReader(`{"session_id":"abc","tool_name":"Read"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.SessionID != "abc" || in.ToolName != "Read" {
		t.Errorf("parsed wrong fields: %+v", in)
	}
}

func TestReadInput_EmptyStdinIsNotError(t *testing.T) {
	in, err := readInput(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty stdin should not error: %v", err)
	}
	if in.SessionID != "" {
		t.Errorf("empty stdin should yield zero-value input, got %+v", in)
	}
}

func TestReadInput_ExceedsCapErrors(t *testing.T) {
	orig := MaxInputBytes
	MaxInputBytes = 16 // shrink so we don't allocate 50 MB in a test
	defer func() { MaxInputBytes = orig }()

	// 17 valid-JSON bytes (would parse if not capped) exceeds the 16-byte cap.
	oversized := `{"a":"bbbbbbbb"}` + " " // 17 bytes
	if _, err := readInput(strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized hook input should return an error")
	}
}

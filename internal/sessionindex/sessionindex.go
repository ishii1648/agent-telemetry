package sessionindex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Session represents a single entry in session-index.jsonl.
type Session struct {
	CodingAgent      string   `json:"coding_agent"`
	AgentVersion     string   `json:"agent_version"`
	UserID           string   `json:"user_id,omitempty"`
	Timestamp        string   `json:"timestamp"`
	SessionID        string   `json:"session_id"`
	CWD              string   `json:"cwd"`
	Repo             string   `json:"repo"`
	Branch           string   `json:"branch"`
	IsDefaultBranch  bool     `json:"is_default_branch,omitempty"`
	PRURLs           []string `json:"pr_urls"`
	PRPinned         bool     `json:"pr_pinned,omitempty"`
	PRTitle          string   `json:"pr_title,omitempty"`
	Transcript       string   `json:"transcript"`
	ParentSessionID  string   `json:"parent_session_id"`
	EndedAt          string   `json:"ended_at"`
	EndReason        string   `json:"end_reason"`
	BackfillChecked  bool     `json:"backfill_checked"`
	IsMerged         bool     `json:"is_merged"`
	ReviewComments   int      `json:"review_comments"`
	ChangesRequested int      `json:"changes_requested"`
}

// AgentName returns the coding agent name, defaulting to "claude" when the
// field is empty (legacy entries written before the Codex CLI work).
func (s Session) AgentName() string {
	if s.CodingAgent == "" {
		return "claude"
	}
	return s.CodingAgent
}

// User returns the user_id, defaulting to "unknown" when the field is empty
// (legacy entries written before user attribution).
func (s Session) User() string {
	if s.UserID == "" {
		return "unknown"
	}
	return s.UserID
}

// IndexFile returns the default path to ~/.claude/session-index.jsonl.
//
// Deprecated: prefer agent.SessionIndexPath() so Codex sessions land under
// ~/.codex/. Kept for callers that have not been threaded through with an
// agent yet.
func IndexFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "session-index.jsonl")
}

// ReadAll reads all sessions from the JSONL file.
// Invalid lines are preserved as raw JSON for round-trip safety.
func ReadAll(path string) ([]json.RawMessage, []Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var raws []json.RawMessage
	var sessions []Session
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var s Session
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			// preserve invalid lines as-is
			raws = append(raws, json.RawMessage(line))
			sessions = append(sessions, Session{})
			continue
		}
		raws = append(raws, json.RawMessage(line))
		sessions = append(sessions, s)
	}
	return raws, sessions, scanner.Err()
}

func ReadAllBytes(path string) ([]json.RawMessage, []Session, error) {
	return ReadAll(path)
}

// WriteAll writes sessions back to the JSONL file atomically.
// 一時ファイルに書き込んでから rename することで、書き込み途中で失敗しても
// 既存の session-index.jsonl が truncate / 部分書き込み状態にならないようにする。
func WriteAll(path string, raws []json.RawMessage) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".session-index-*.jsonl.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	w := bufio.NewWriter(tmp)
	for i, raw := range raws {
		if i > 0 {
			if err := w.WriteByte('\n'); err != nil {
				tmp.Close()
				cleanup()
				return err
			}
		}
		if _, err := w.Write(raw); err != nil {
			tmp.Close()
			cleanup()
			return err
		}
	}
	if err := w.WriteByte('\n'); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func WriteAllBytes(path string, raws []json.RawMessage) error {
	return WriteAll(path, raws)
}

func AppendRawLine(path string, raw json.RawMessage) error {
	_, err := WithLockedIndex(path, func(raws []json.RawMessage, sessions []Session) ([]json.RawMessage, bool, error) {
		next := append(append([]json.RawMessage(nil), raws...), raw)
		return next, true, nil
	})
	return err
}

// NormalizeRepo removes trailing ".git" suffix.
func NormalizeRepo(repo string) string {
	return strings.TrimSuffix(repo, ".git")
}

// MarshalSession marshals a Session to JSON without escaping non-ASCII.
func MarshalSession(s Session) (json.RawMessage, error) {
	buf, err := json.Marshal(s)
	return json.RawMessage(buf), err
}

// UnmarshalSession decodes raw JSON into a Session, preserving extra fields.
func UnmarshalSession(raw json.RawMessage) (Session, error) {
	var s Session
	err := json.Unmarshal(raw, &s)
	return s, err
}

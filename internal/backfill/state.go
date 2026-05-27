package backfill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State holds the cursor for incremental backfill processing.
//
// PushedSessionVersions is owned by the serverclient push pipeline (issue 0028)
// but lives in the same on-disk file. Keeping it on this struct lets either
// package round-trip the file without a custom merge: backfill leaves the map
// untouched and push leaves the cursor fields untouched. Keys are
// `<coding_agent>:<session_id>` (composite to survive UUID collisions across
// agents), values are SHA-256 hashes of the canonicalized payload.
type State struct {
	LastBackfillOffset    int                  `json:"last_backfill_offset"`
	LastMetaCheck         time.Time            `json:"last_meta_check"`
	LastWorkerRun         time.Time            `json:"last_worker_run,omitempty"`
	MetaURLChecks         map[string]time.Time `json:"meta_url_checks,omitempty"`
	PushedSessionVersions map[string]string    `json:"pushed_session_versions,omitempty"`
	LastFlushedSequence   int64                `json:"last_flushed_sequence,omitempty"`
}

// StatePath returns ~/.claude/agent-telemetry-state.json (Claude default).
//
// Deprecated: use agent.StatePath() so Codex cursors land under ~/.codex/.
// Kept for callers that have not been threaded through with an agent yet.
func StatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agent-telemetry-state.json")
}

// LoadState reads the state file. Returns zero state if the file doesn't exist.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, nil
	}
	return s, nil
}

// SaveState writes the state file atomically.
func SaveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "agent-telemetry-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

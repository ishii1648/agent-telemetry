package hook

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

// spawnBackfillWorker launches the detached backfill worker. It is a
// package-level seam so tests can exercise RunStop without exec-ing the real
// binary (which, under `go test`, would be the test binary itself).
var spawnBackfillWorker = defaultSpawnBackfillWorker

// RunStop handles the Stop hook event for the given agent.
//
// Stop sits on the user's response cycle, so its synchronous work is kept to
// local writes only:
//   - Codex has no SessionEnd event, so the last Stop fire is the de-facto
//     SessionEnd: ended_at / end_reason are overwritten here every tick.
//   - Everything that touches GitHub (PR pin, URL backfill, merge-status
//     refresh) and the SQLite rebuild is pushed into a detached worker
//     spawned via `agent-telemetry backfill --detach`. The hook fire-and-
//     forgets it and returns in a few milliseconds without waiting.
//
// `--agent <name>` and `--pin-session=<id>` are forwarded so the worker scopes
// its cursor / session-index correctly and resolves this session's PR first.
func RunStop(input *HookInput, a *agent.Agent) error {
	if a == nil {
		a = agent.Claude()
	}

	if a.Name == agent.NameCodex && input != nil && input.SessionID != "" {
		endedAt := time.Now().Format("2006-01-02 15:04:05")
		if _, err := sessionindex.UpdateEnd(a.SessionIndexPath(), input.SessionID, endedAt, "stop"); err != nil {
			return fmt.Errorf("update ended_at: %w", err)
		}
	}

	sessionID := ""
	if input != nil {
		sessionID = input.SessionID
	}
	if err := spawnBackfillWorker(a.Name, sessionID); err != nil {
		return fmt.Errorf("backfill detach: %w", err)
	}
	return nil
}

// defaultSpawnBackfillWorker fire-and-forgets `agent-telemetry backfill
// --detach`. The binary is resolved via os.Executable() rather than relying on
// "agent-telemetry" being on PATH, since the hook may have been registered
// with an absolute path. stdio goes to /dev/null so the child cannot pollute
// the hook's own output, and the `--detach` entry point re-spawns the real
// worker under setsid (see cmd/agent-telemetry/main.go).
func defaultSpawnBackfillWorker(agentName, sessionID string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "agent-telemetry"
	}
	args := []string{"backfill", "--detach", "--agent", agentName}
	if sessionID != "" {
		args = append(args, "--pin-session="+sessionID)
	}
	cmd := exec.Command(exe, args...)
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer devnull.Close()
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	return cmd.Start()
}

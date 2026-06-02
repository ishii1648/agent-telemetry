package hook

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

// RunSessionEnd handles the SessionEnd hook (Claude only — Codex has no
// SessionEnd; the Stop hook covers that case).
//
// Records session end metadata for parallel-session metrics, then refreshes
// SQLite. Failure to find the session is silent (e.g. transcript-only ghost
// sessions never had a SessionStart entry).
func RunSessionEnd(input *HookInput, a *agent.Agent) error {
	if a == nil {
		a = agent.Claude()
	}
	endedAt := time.Now().Format("2006-01-02 15:04:05")
	updated, err := sessionindex.UpdateEnd(a.SessionIndexPath(), input.SessionID, endedAt, input.Reason)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}
	// Self-invocation resolves the running binary via os.Executable() rather
	// than relying on "agent-telemetry" being on PATH, matching the Stop hook's
	// worker spawn (see internal/hook/stop.go). This avoids picking up a
	// same-named binary planted earlier on PATH in an untrusted environment.
	// Falls back to a bare PATH lookup only if the path cannot be determined.
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "agent-telemetry"
	}
	if out, err := exec.Command(exe, "sync-db").CombinedOutput(); err != nil {
		return fmt.Errorf("sync-db: %w\n%s", err, out)
	}
	return nil
}

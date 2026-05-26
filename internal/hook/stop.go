package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

// pinPRTimeout caps the gh pr view call inside the Stop hook. Stop is
// blocking on the user's response cycle, so we accept "could not resolve
// PR this tick" rather than stall the prompt.
const pinPRTimeout = 8 * time.Second

// prLookupFn is the seam used to mock `gh pr list` in tests. Production
// code uses the default ghPRLookup; tests inject a fake to exercise
// pinPRForSession's branching without touching the real GitHub CLI.
type prLookupFn func(cwd, branch string) (*prViewJSON, error)

var prLookup prLookupFn = ghPRLookup

// RunStop handles the Stop hook event for the given agent.
//
// Behavior:
//   - Both agents: resolve the current branch's PR via `gh pr view` once
//     and pin it on the session entry. This is "early binding" — pinning
//     here prevents PostToolUse regex scrapes (which fire on `gh pr view`,
//     `gh pr list`, or stray PR URLs in user messages) from polluting
//     pr_urls later, and prevents same-branch reuse from cross-attaching
//     a new PR onto an old session.
//   - Codex: overwrite ended_at on every fire (Codex has no SessionEnd, so
//     the last Stop fire is the de-facto SessionEnd). end_reason is fixed
//     to "stop".
//   - Both agents: shell out to `agent-telemetry backfill` then
//     `agent-telemetry sync-db` so the SQLite DB is fresh by the time the
//     user looks at the dashboard. We exec the binary (instead of calling
//     the packages directly) to keep the sqlite dependency out of the
//     hook hot path and to match the original shell behaviour.
//
// `--agent <name>` is forwarded so the subprocesses scope their cursor /
// session-index correctly.
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

	args := []string{"backfill", "--detach", "--agent", a.Name}
	if input != nil && input.SessionID != "" {
		args = append(args, "--pin-session="+input.SessionID)
	}
	if err := exec.Command("agent-telemetry", args...).Start(); err != nil {
		return fmt.Errorf("backfill detach: %w", err)
	}
	return nil
}

// prViewJSON is the subset of `gh pr view --json ...` we consume for
// pinning. State / Comments / Reviews are pulled along to seed pr_meta in
// the same call so backfill's Phase 2 has nothing left to do for this
// session's URL.
type prViewJSON struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Comments []any  `json:"comments"`
	Reviews  []struct {
		State string `json:"state"`
	} `json:"reviews"`
}

// pinPRForSession resolves the PR for input's branch (via the session's
// recorded cwd) and pins it onto the session-index entry.
//
// Resolution strategy:
//  1. Look up the session's cwd from session-index. We use the recorded
//     cwd, not input.CWD, because Codex's Stop payload does not always
//     populate cwd, and the session-index entry written at SessionStart
//     is the authoritative source.
//  2. If the cwd is gone (worktree deleted, branch reused elsewhere) we
//     bail — there is nothing we can pin against safely.
//  3. Run `gh pr view --head <branch>` filtered by author=@me to avoid
//     attaching a teammate's PR on the same branch name.
func pinPRForSession(a *agent.Agent, input *HookInput) error {
	indexPath := a.SessionIndexPath()
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil
	}
	_, sessions, err := sessionindex.ReadAll(indexPath)
	if err != nil {
		return fmt.Errorf("read index: %w", err)
	}

	var sess *sessionindex.Session
	for i := range sessions {
		if sessions[i].SessionID == input.SessionID {
			sess = &sessions[i]
			break
		}
	}
	if sess == nil {
		return nil
	}
	if sess.PRPinned {
		return nil
	}
	if sess.Branch == "" {
		return nil
	}

	cwd := sess.CWD
	if cwd == "" {
		cwd = input.CWD
	}
	if cwd == "" || !isExistingDir(cwd) {
		return nil
	}

	pr, err := prLookup(cwd, sess.Branch)
	if err != nil {
		return err
	}
	if pr == nil || pr.URL == "" {
		return nil
	}

	if _, err := sessionindex.PinPR(indexPath, input.SessionID, pr.URL); err != nil {
		return fmt.Errorf("pin pr: %w", err)
	}
	// Seed PR meta in the same pass so backfill Phase 2 has nothing left
	// to do for this URL on the next tick.
	changesRequested := 0
	for _, r := range pr.Reviews {
		if r.State == "CHANGES_REQUESTED" {
			changesRequested++
		}
	}
	if _, err := sessionindex.UpdatePRMeta(indexPath, pr.URL, pr.State == "MERGED", len(pr.Comments), changesRequested, pr.Title); err != nil {
		return fmt.Errorf("update pr meta: %w", err)
	}
	return nil
}

// ghPRLookup runs `gh pr list --head <branch> --author @me` and returns
// the matching PR or nil when none is found. Errors propagate only for
// unexpected failures (gh missing, network error); a clean
// "no PR for this branch" returns (nil, nil).
func ghPRLookup(cwd, branch string) (*prViewJSON, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pinPRTimeout)
	defer cancel()

	// `gh pr view --head` does not exist; use `gh pr list --head <branch>`
	// limited to 1 result. This mirrors backfill's resolution path so the
	// pinned URL matches what backfill would have produced on the next
	// tick — keeping the two paths in agreement avoids surprising drift.
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--author", "@me",
		"--state", "all",
		"--json", "url,title,state,comments,reviews",
		"--limit", "1",
	)
	cmd.Dir = cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("gh pr list timed out after %s", pinPRTimeout)
		}
		return nil, fmt.Errorf("gh pr list: %w: %s", err, stderr.String())
	}

	var prs []prViewJSON
	if err := json.Unmarshal(stdout.Bytes(), &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

func isExistingDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
	"github.com/ishii1648/agent-telemetry/internal/userid"
)

// RunSessionStart handles the SessionStart hook event for the given agent.
// Records session metadata in <agent.DataDir>/session-index.jsonl.
func RunSessionStart(input *HookInput, a *agent.Agent) error {
	if a == nil {
		a = agent.Claude()
	}

	logDir := a.LogDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}
	raw, _ := json.Marshal(input)
	_ = appendFile(filepath.Join(logDir, "session-index-debug.log"), string(raw)+"\n")

	repo, branch, isDefaultBranch := extractGitInfo(input.CWD)

	resolvedUser, _ := userid.Resolve()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	entry := map[string]interface{}{
		"coding_agent":      a.Name,
		"agent_version":     input.AgentVersion(a.Name),
		"user_id":           resolvedUser,
		"timestamp":         timestamp,
		"session_id":        input.SessionID,
		"cwd":               input.CWD,
		"repo":              repo,
		"branch":            branch,
		"pr_urls":           []string{},
		"transcript":        input.TranscriptPath,
		"parent_session_id": input.ParentSessionID,
	}
	// 第1層 admission control: デフォルトブランチ上のセッションは構造的に PR を
	// 持たない（デフォルトブランチから PR は作らない）ので、backfill の candidate
	// から外して `gh pr list` を一度も呼ばないためのフラグを焼き付ける。値が false
	// のときは omitempty に合わせてキー自体を省く（既存レコードと同じ形を保つ）。
	if isDefaultBranch {
		entry["is_default_branch"] = true
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(a.DataDir, 0755); err != nil {
		return err
	}
	return sessionindex.AppendRawLine(a.SessionIndexPath(), data)
}

// extractGitInfo gets repo, branch, and whether the branch is the repo's
// default branch from a directory's git context.
func extractGitInfo(cwd string) (repo, branch string, isDefaultBranch bool) {
	if cwd == "" {
		return "", "", false
	}

	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return "", "", false
	}

	cmd = exec.Command("git", "-C", cwd, "remote", "get-url", "origin")
	if out, err := cmd.Output(); err == nil {
		remoteURL := strings.TrimSpace(string(out))
		if remoteURL != "" {
			repo = parseRepoFromRemote(remoteURL)
		}
	}

	if repo == "" {
		cmd = exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
		if out, err := cmd.Output(); err == nil {
			toplevel := strings.TrimSpace(string(out))
			repo = parseRepoFromPath(toplevel)
		}
	}

	cmd = exec.Command("git", "-C", cwd, "branch", "--show-current")
	if out, err := cmd.Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}

	isDefaultBranch = isRepoDefaultBranch(cwd, branch)

	return repo, branch, isDefaultBranch
}

// isRepoDefaultBranch reports whether branch is the repo's default branch.
//
// 名前の `main` / `master` ハードコード除外は採用しない（0035 却下案 D）。
// repo ごとに `trunk` / `dev` / `develop` 等の多様な命名がありうるため、ローカルで
// 完結する `git symbolic-ref refs/remotes/origin/HEAD` で実デフォルトブランチを動的
// 判定する（gh API は呼ばずコスト増を避ける）。origin/HEAD が未設定の repo（remote
// 無し / 未 fetch）に限って慣習的な `main` / `master` へフォールバックする。
func isRepoDefaultBranch(cwd, branch string) bool {
	if cwd == "" || branch == "" {
		return false
	}
	cmd := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if out, err := cmd.Output(); err == nil {
		def := strings.TrimSpace(string(out))
		def = strings.TrimPrefix(def, "origin/")
		if def != "" {
			return branch == def
		}
	}
	return branch == "main" || branch == "master"
}

var remoteRepoRe = regexp.MustCompile(`[:/]([^/]+/[^/]+?)(?:\.git)?$`)

// parseRepoFromRemote extracts org/repo from SSH or HTTPS remote URLs.
func parseRepoFromRemote(url string) string {
	m := remoteRepoRe.FindStringSubmatch(url)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

var pathRepoRe = regexp.MustCompile(`([^/]+/[^/@]+?)(?:@.*)?$`)

// parseRepoFromPath extracts org/repo from ghq-style directory paths.
func parseRepoFromPath(toplevel string) string {
	m := pathRepoRe.FindStringSubmatch(toplevel)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func appendFile(path string, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

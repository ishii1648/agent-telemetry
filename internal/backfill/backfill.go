package backfill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
	"github.com/ishii1648/agent-telemetry/internal/transcript"
)

type group struct {
	repo    string
	branch  string
	entries []sessionindex.Session
}

type result struct {
	group            group
	url              string
	title            string
	markChecked      bool
	isMerged         bool
	comments         int
	changesRequested int
}

// prJSON represents a PR entry from gh pr list --json output.
type prJSON struct {
	URL      string        `json:"url"`
	Title    string        `json:"title"`
	State    string        `json:"state"`
	Comments []interface{} `json:"comments"`
	Reviews  []reviewJSON  `json:"reviews"`
}

type reviewJSON struct {
	State string `json:"state"`
}

// MetaCheckInterval is the minimum duration between Phase 2 (merge status) checks.
const MetaCheckInterval = 1 * time.Hour
const WorkerCooldown = 5 * time.Minute
const DefaultGHCap = 20

type Options struct {
	Recheck      bool
	DetachedRun  bool
	PinSessionID string
	GCOnly       bool
	GHCap        int
}

// Run executes the backfill batch. It finds sessions without pr_urls,
// groups them by (repo, branch), and fetches PR URLs via gh pr list in parallel.
// It also updates is_merged and review_comments for sessions with existing pr_urls.
//
// When statePath is non-empty, cursor-based incremental processing is used:
// - Phase 1 only scans entries after last_backfill_offset
// - Phase 2 only runs if MetaCheckInterval has elapsed since last_meta_check
//
// Deprecated: prefer RunForAgent which threads the agent identity through
// to the Codex-specific ended_at backfill. Kept so callers that pre-date
// the Codex work continue to work for Claude.
func Run(indexPath string, recheck bool) error {
	return RunWithState(indexPath, StatePath(), recheck)
}

// RunForAgent runs Phase 1 (URL backfill), Codex-only ended_at backfill,
// and Phase 2 (PR meta refresh) for one agent. The state cursor lives at
// the agent's StatePath().
func RunForAgent(a *agent.Agent, recheck bool) error {
	return RunForAgentWithOptions(a, Options{Recheck: recheck})
}

func RunForAgentWithOptions(a *agent.Agent, opts Options) error {
	return runForAgent(a, a.SessionIndexPath(), a.StatePath(), opts)
}

// RunForAgents iterates every supplied agent. Errors from one agent do
// not abort the others — they are logged to stderr and the function
// returns the last error seen.
func RunForAgents(agents []*agent.Agent, recheck bool) error {
	return RunForAgentsWithOptions(agents, Options{Recheck: recheck})
}

func RunForAgentsWithOptions(agents []*agent.Agent, opts Options) error {
	var lastErr error
	for _, a := range agents {
		if err := RunForAgentWithOptions(a, opts); err != nil {
			fmt.Fprintf(os.Stderr, "backfill[%s]: %v\n", a.Name, err)
			lastErr = err
		}
	}
	return lastErr
}

// RunWithState is like Run but accepts an explicit state file path (for testing).
func RunWithState(indexPath, statePath string, recheck bool) error {
	return runForAgent(agent.Claude(), indexPath, statePath, Options{Recheck: recheck})
}

func runForAgent(a *agent.Agent, indexPath, statePath string, opts Options) error {
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil
	}
	if opts.GHCap <= 0 {
		opts.GHCap = DefaultGHCap
	}
	if opts.DetachedRun {
		f, ok, err := tryGlobalLock()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("backfill-worker: skip (another worker is running)")
			return nil
		}
		defer unlockGlobal(f)
	}

	_, sessions, err := sessionindex.ReadAll(indexPath)
	if err != nil {
		return err
	}

	// Load cursor state
	state, err := LoadState(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if opts.DetachedRun && !opts.Recheck && !opts.GCOnly && !state.LastWorkerRun.IsZero() && time.Since(state.LastWorkerRun) < WorkerCooldown {
		fmt.Printf("backfill-worker: skip (cooldown %s)\n", WorkerCooldown)
		return nil
	}
	if state.MetaURLChecks == nil {
		state.MetaURLChecks = make(map[string]time.Time)
	}

	batch := sessionindex.Batch{
		PinPRs:      make(map[string]string),
		SessionURLs: make(map[string][]string),
		PRMetas:     make(map[string]sessionindex.PRMeta),
		EndUpdates:  make(map[string]sessionindex.EndUpdate),
	}

	// Phase 1: Fetch PR URLs for sessions without them.
	// Cursor skips bulk-resolved entries, but cursor-below entries that are
	// still pending (pr_urls 空 かつ !backfill_checked) must be retried each
	// run — otherwise sessions whose PR was created after the previous Stop
	// hook would never get their URL filled in.
	offset := state.LastBackfillOffset
	if offset > len(sessions) || opts.Recheck || opts.GCOnly {
		offset = 0
	}
	target := append([]sessionindex.Session(nil), sessions[offset:]...)
	for _, s := range sessions[:offset] {
		if len(s.PRURLs) == 0 && !s.BackfillChecked {
			target = append(target, s)
		}
	}

	capBudget := opts.GHCap
	if opts.PinSessionID != "" && !opts.GCOnly {
		used, err := runPinBackfill(sessions, opts.PinSessionID, &batch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill-pin: %v\n", err)
		}
		capBudget -= used
		if capBudget < 0 {
			capBudget = 0
		}
	}
	if err := runURLBackfill(indexPath, target, opts.Recheck, opts.GCOnly, capBudget, &batch); err != nil {
		return err
	}

	// Codex-only: backfill ended_at from rollout JSONL when the Stop hook
	// missed the final tick (process killed, hook crashed, ...).
	if a != nil && a.Name == agent.NameCodex {
		if err := backfillCodexEndedAt(indexPath, &batch); err != nil {
			fmt.Fprintf(os.Stderr, "backfill: ended_at補完: %v\n", err)
		}
	}

	// Update cursor: advance to total session count
	state.LastBackfillOffset = len(sessions)

	// Phase 2: Update merge status and review comments for sessions with pr_urls
	// Only run if enough time has elapsed since last check
	now := time.Now()
	if !opts.GCOnly && (now.Sub(state.LastMetaCheck) >= MetaCheckInterval || opts.Recheck) {
		if err := runMetaBackfill(indexPath, sessions, opts.GHCap, state.MetaURLChecks, &batch); err != nil {
			return err
		}
		state.LastMetaCheck = now
	} else {
		fmt.Printf("backfill-meta: スキップ（前回チェックから %s 未経過）\n",
			MetaCheckInterval)
	}

	if len(batch.PinPRs) > 0 || len(batch.SessionURLs) > 0 || len(batch.MarkChecked) > 0 || len(batch.PRMetas) > 0 || len(batch.EndUpdates) > 0 {
		if _, err := sessionindex.ApplyBatch(indexPath, batch); err != nil {
			if errors.Is(err, sessionindex.ErrLockBusy) {
				fmt.Println("backfill: skip write (session index busy)")
				return nil
			}
			return err
		}
	}
	if opts.DetachedRun {
		state.LastWorkerRun = now
	}
	// Save cursor state
	if statePath != "" {
		if err := SaveState(statePath, state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	return nil
}

func runURLBackfill(indexPath string, sessions []sessionindex.Session, recheck bool, gcOnly bool, capBudget int, batch *sessionindex.Batch) error {
	_ = indexPath
	// Collect entries with empty pr_urls. Pinned sessions are excluded
	// unconditionally — their pr_urls is authoritative (Stop hook bound
	// it via gh pr view) and we must not re-resolve via (repo, branch).
	var entries []sessionindex.Session
	for _, s := range sessions {
		if s.PRPinned {
			continue
		}
		// 第1層 admission control: repo の実デフォルトブランチ上のセッションは
		// 構造的に PR を持たない（デフォルトブランチから PR は作らない）。candidate
		// に入れず `gh pr list` を一度も呼ばない。`backfill_checked` ではなく
		// `is_default_branch` フラグが永続スキップを担うので recheck でも除外する。
		// フラグ未設定の過去セッションは引き続き第2層（horizon）+ `--gc` で収束する。
		if s.IsDefaultBranch {
			continue
		}
		if len(s.PRURLs) == 0 && (!s.BackfillChecked || recheck) {
			entries = append(entries, s)
		}
	}

	if len(entries) == 0 {
		fmt.Println("backfill: URL対象エントリなし（全件 pr_urls 補完済み or backfill_checked 済み）")
		return nil
	}

	// Group by (repo, branch)
	type key struct{ repo, branch string }
	groupMap := make(map[key][]sessionindex.Session)
	for _, e := range entries {
		if e.Repo == "" || e.Branch == "" {
			continue
		}
		k := key{e.Repo, e.Branch}
		groupMap[k] = append(groupMap[k], e)
	}

	var groups []group
	for k, es := range groupMap {
		groups = append(groups, group{repo: k.repo, branch: k.branch, entries: es})
	}
	sort.Slice(groups, func(i, j int) bool {
		return newestGroupTime(groups[i]).After(newestGroupTime(groups[j]))
	})
	if !recheck && !gcOnly && capBudget >= 0 && len(groups) > capBudget {
		groups = groups[:capBudget]
	}

	fmt.Printf("backfill: %d エントリ / %d グループを処理中...\n", len(entries), len(groups))
	if gcOnly {
		for _, g := range groups {
			for _, e := range g.entries {
				if e.SessionID != "" && shouldMarkChecked(e, time.Now()) {
					batch.MarkChecked = append(batch.MarkChecked, e.SessionID)
				}
			}
		}
		fmt.Printf("backfill-gc: %d セッションを markChecked 予定\n", len(batch.MarkChecked))
		return nil
	}

	// Parallel fetch with max 8 workers
	results := make(chan result, len(groups))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for _, g := range groups {
		wg.Add(1)
		go func(g group) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := fetchPR(g)
			results <- r
		}(g)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	found, skipped, retried := 0, 0, 0
	for r := range results {
		if r.url != "" {
			found++
			for _, e := range r.group.entries {
				if e.SessionID != "" {
					batch.SessionURLs[e.SessionID] = []string{r.url}
				}
			}
			// Also set merge info right away
			batch.PRMetas[r.url] = sessionindex.PRMeta{IsMerged: r.isMerged, ReviewComments: r.comments, ChangesRequested: r.changesRequested, Title: r.title}
		} else if r.markChecked {
			skipped++
			for _, e := range r.group.entries {
				if e.SessionID != "" {
					batch.MarkChecked = append(batch.MarkChecked, e.SessionID)
				}
			}
		} else {
			retried++
		}
	}

	fmt.Printf("backfill: 完了 — URL取得成功 %d グループ / cwd消滅スキップ %d グループ / 再試行待ち %d グループ\n",
		found, skipped, retried)
	return nil
}

func newestGroupTime(g group) time.Time {
	var newest time.Time
	for _, e := range g.entries {
		t := sessionTime(e)
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

func sessionTime(s sessionindex.Session) time.Time {
	for _, v := range []string{s.EndedAt, s.Timestamp} {
		if v == "" {
			continue
		}
		if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func shouldMarkChecked(s sessionindex.Session, now time.Time) bool {
	t := sessionTime(s)
	if t.IsZero() {
		return false
	}
	return now.Sub(t) >= 24*time.Hour
}

// runMetaBackfill updates PR metadata for sessions that have pr_urls.
func runMetaBackfill(indexPath string, sessions []sessionindex.Session, capBudget int, metaURLChecks map[string]time.Time, batch *sessionindex.Batch) error {
	_ = indexPath
	// Collect unique pr_urls that need meta update.
	type prInfo struct {
		url         string
		cwd         string // any cwd from sessions with this URL, for running gh commands
		lastChecked time.Time
	}
	seen := make(map[string]bool)
	var targets []prInfo
	for _, s := range sessions {
		if len(s.PRURLs) == 0 {
			continue
		}
		if s.IsMerged {
			continue
		}
		url := s.PRURLs[len(s.PRURLs)-1]
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		targets = append(targets, prInfo{url: url, cwd: s.CWD, lastChecked: metaURLChecks[url]})
	}

	if len(targets) == 0 {
		return nil
	}

	sort.Slice(targets, func(i, j int) bool {
		return targets[i].lastChecked.Before(targets[j].lastChecked)
	})
	if capBudget > 0 && len(targets) > capBudget {
		targets = targets[:capBudget]
	}

	fmt.Printf("backfill-meta: %d PR のメタデータを更新中...\n", len(targets))

	type metaResult struct {
		url              string
		title            string
		isMerged         bool
		comments         int
		changesRequested int
		ok               bool
	}

	results := make(chan metaResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for _, t := range targets {
		wg.Add(1)
		go func(t prInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// `gh pr view <full-url>` は full URL でリポジトリを解決するため
			// cwd の git remote に依存しない。信頼境界を固定するため、攻撃者が
			// 用意しうるセッション記録由来の cwd ではなく常に信頼できる作業
			// ディレクトリ（HOME / 不可なら TempDir）で実行する（[0061]）。
			// リポジトリ cwd 固有の git/gh 設定・hook を拾わせない狙いも兼ねる。
			// 副次効果として、リネームや worktree 削除で元 cwd が消えた古い
			// セッションでも meta を更新できる。
			cwd := trustedWorkdir()
			if cwd == "" {
				results <- metaResult{url: t.url}
				return
			}

			pr, err := fetchPRByURL(t.url, cwd)
			if err != nil {
				results <- metaResult{url: t.url}
				return
			}
			results <- metaResult{
				url:              t.url,
				title:            pr.Title,
				isMerged:         pr.State == "MERGED",
				comments:         len(pr.Comments),
				changesRequested: countChangesRequested(pr.Reviews),
				ok:               true,
			}
		}(t)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	updated := 0
	for r := range results {
		if r.ok {
			batch.PRMetas[r.url] = sessionindex.PRMeta{IsMerged: r.isMerged, ReviewComments: r.comments, ChangesRequested: r.changesRequested, Title: r.title}
			metaURLChecks[r.url] = time.Now()
			updated++
		}
	}

	fmt.Printf("backfill-meta: 完了 — %d PR 更新\n", updated)
	return nil
}

// fetchPR fetches PR URL, state, and comments for a branch group.
func fetchPR(g group) result {
	// `gh pr list --head` はリポジトリを cwd の git remote から解決するため、
	// セッション記録の cwd で実行せざるを得ない（gh pr view と違い full URL を
	// 持たない）。信頼境界: ここでの cwd は自分のセッション記録（session-index）
	// 由来であり、それ自体が telemetry の信頼ドメイン内。攻撃者が cwd を差し替えた
	// 場合でも no-shell argv / 8s timeout / global lock の緩和で被害を限定する
	// （[0061]）。Use the last entry's cwd (matches Python behavior)。
	cwd := g.entries[len(g.entries)-1].CWD
	if cwd == "" || !isDir(cwd) {
		return result{group: g, markChecked: true}
	}

	cmd := exec.Command("gh", "pr", "list",
		"--head", g.branch,
		"--author", "@me",
		"--state", "all",
		"--json", "url,title,state,comments,reviews",
		"--limit", "1",
	)
	cmd.Dir = cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return result{group: g}
		}
		prs := parsePRList(stdout.Bytes())
		if len(prs) == 0 || !strings.Contains(prs[0].URL, "github.com") {
			return result{group: g}
		}
		pr := prs[0]
		return result{
			group:            g,
			url:              pr.URL,
			title:            pr.Title,
			isMerged:         pr.State == "MERGED",
			comments:         len(pr.Comments),
			changesRequested: countChangesRequested(pr.Reviews),
		}
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		return result{group: g}
	}
}

func runPinBackfill(sessions []sessionindex.Session, sessionID string, batch *sessionindex.Batch) (int, error) {
	for _, s := range sessions {
		if s.SessionID != sessionID {
			continue
		}
		// 第1層 admission control: 現セッションがデフォルトブランチ上なら pin の
		// `gh pr list` も呼ばない（構造的に PR を持たないため空振り確定）。
		if s.PRPinned || s.Branch == "" || s.IsDefaultBranch {
			return 0, nil
		}
		r := fetchPR(group{repo: s.Repo, branch: s.Branch, entries: []sessionindex.Session{s}})
		if r.url == "" {
			return 1, nil
		}
		batch.PinPRs[sessionID] = r.url
		batch.PRMetas[r.url] = sessionindex.PRMeta{
			IsMerged:         r.isMerged,
			ReviewComments:   r.comments,
			ChangesRequested: r.changesRequested,
			Title:            r.title,
		}
		return 1, nil
	}
	return 0, nil
}

func globalLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "agent-telemetry-backfill.lock")
	}
	return filepath.Join(home, ".agent-telemetry", "backfill.lock")
}

func tryGlobalLock() (*os.File, bool, error) {
	path := globalLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

func unlockGlobal(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// fetchPRByURL fetches PR metadata for an existing PR URL using gh pr view.
func fetchPRByURL(prURL, cwd string) (*prJSON, error) {
	cmd := exec.Command("gh", "pr", "view", prURL,
		"--json", "url,title,state,comments,reviews",
	)
	cmd.Dir = cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("gh pr view: %w: %s", err, stderr.String())
		}
		var pr prJSON
		if err := json.Unmarshal(stdout.Bytes(), &pr); err != nil {
			return nil, err
		}
		return &pr, nil
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("timeout")
	}
}

func parsePRList(data []byte) []prJSON {
	var prs []prJSON
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil
	}
	return prs
}

func countChangesRequested(reviews []reviewJSON) int {
	count := 0
	for _, r := range reviews {
		if r.State == "CHANGES_REQUESTED" {
			count++
		}
	}
	return count
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// trustedWorkdir returns a directory under the user's own control for running
// cwd-independent `gh` commands (e.g. `gh pr view <full-url>`), so they never
// execute inside an untrusted repository cwd that an attacker may have planted
// git/gh config or hooks in. Prefers HOME, falls back to TempDir, and returns
// "" if neither is usable (caller skips the fetch). See [0061].
func trustedWorkdir() string {
	if home, err := os.UserHomeDir(); err == nil && isDir(home) {
		return home
	}
	if tmp := os.TempDir(); isDir(tmp) {
		return tmp
	}
	return ""
}

// backfillCodexEndedAt reads each Codex session whose ended_at is empty
// and fills it from the rollout JSONL's last event timestamp. This catches
// the case where the Codex process was killed before the Stop hook fired.
func backfillCodexEndedAt(indexPath string, batch *sessionindex.Batch) error {
	_, sessions, err := sessionindex.ReadAll(indexPath)
	if err != nil {
		return err
	}
	updated := 0
	for _, s := range sessions {
		if s.SessionID == "" || s.EndedAt != "" || s.Transcript == "" {
			continue
		}
		_, _, lastTS, ok := transcript.ReadCodexMeta(s.Transcript)
		if !ok || lastTS.IsZero() {
			continue
		}
		endedAt := lastTS.Format("2006-01-02 15:04:05")
		batch.EndUpdates[s.SessionID] = sessionindex.EndUpdate{EndedAt: endedAt, Reason: "stop"}
		updated++
	}
	if updated > 0 {
		fmt.Printf("backfill-codex: ended_at を %d 件補完\n", updated)
	}
	return nil
}

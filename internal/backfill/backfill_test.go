package backfill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishii1648/agent-telemetry/internal/sessionindex"
)

func writeTestIndex(t *testing.T, dir string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, "session-index.jsonl")
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

func TestRunWithState_CursorAdvances(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
		`{"timestamp":"2026-03-01 11:00:00","session_id":"s2","cwd":"/tmp","repo":"user/repo","branch":"feat","pr_urls":["https://github.com/user/repo/pull/2"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
	})
	statePath := filepath.Join(dir, "state.json")

	// First run: cursor should advance to 2
	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastBackfillOffset != 2 {
		t.Fatalf("expected offset 2, got %d", state.LastBackfillOffset)
	}
}

func TestRunWithState_CursorSkipsProcessed(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
	})
	statePath := filepath.Join(dir, "state.json")

	// Pre-set cursor to 1 (already processed)
	SaveState(statePath, State{
		LastBackfillOffset: 1,
		LastMetaCheck:      time.Now(),
	})

	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := LoadState(statePath)
	if state.LastBackfillOffset != 1 {
		t.Fatalf("expected offset 1, got %d", state.LastBackfillOffset)
	}
}

func TestRunWithState_RecheckResetsCursor(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
	})
	statePath := filepath.Join(dir, "state.json")

	// Pre-set cursor
	SaveState(statePath, State{LastBackfillOffset: 1, LastMetaCheck: time.Now()})

	// --recheck should reset cursor to 0 and process all
	err := RunWithState(indexPath, statePath, true)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := LoadState(statePath)
	if state.LastBackfillOffset != 1 {
		t.Fatalf("expected offset 1 after recheck, got %d", state.LastBackfillOffset)
	}
}

func TestRunWithState_Phase2SkippedIfRecent(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true,"is_merged":false}`,
	})
	statePath := filepath.Join(dir, "state.json")

	recentTime := time.Now().Add(-10 * time.Minute)
	SaveState(statePath, State{
		LastBackfillOffset: 0,
		LastMetaCheck:      recentTime,
	})

	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := LoadState(statePath)
	// LastMetaCheck should NOT be updated (Phase 2 was skipped)
	if state.LastMetaCheck.After(recentTime.Add(time.Second)) {
		t.Fatalf("Phase 2 should have been skipped, but LastMetaCheck was updated to %v", state.LastMetaCheck)
	}
}

func TestRunWithState_Phase2RunsAfterInterval(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true,"is_merged":false}`,
	})
	statePath := filepath.Join(dir, "state.json")

	// Set last check to 2 hours ago
	oldTime := time.Now().Add(-2 * time.Hour)
	SaveState(statePath, State{
		LastBackfillOffset: 0,
		LastMetaCheck:      oldTime,
	})

	before := time.Now()
	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := LoadState(statePath)
	// LastMetaCheck should be updated (Phase 2 ran)
	if state.LastMetaCheck.Before(before) {
		t.Fatalf("Phase 2 should have run, but LastMetaCheck was not updated")
	}
}

func TestRunWithState_IndexNotExist(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "nonexistent.jsonl")
	statePath := filepath.Join(dir, "state.json")

	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatalf("expected nil error for missing index, got: %v", err)
	}
}

func TestRunWithState_CursorBeyondLength(t *testing.T) {
	dir := t.TempDir()
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/tmp","repo":"user/repo","branch":"main","pr_urls":["https://github.com/user/repo/pull/1"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
	})
	statePath := filepath.Join(dir, "state.json")

	// Cursor beyond current length (e.g., entries were removed)
	SaveState(statePath, State{LastBackfillOffset: 999, LastMetaCheck: time.Now()})

	err := RunWithState(indexPath, statePath, false)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := LoadState(statePath)
	// Should reset to current length
	if state.LastBackfillOffset != 1 {
		t.Fatalf("expected offset 1 after reset, got %d", state.LastBackfillOffset)
	}
}

// TestRunWithState_RetriesPendingBelowCursor verifies that entries below the
// cursor that are still pending (pr_urls empty, !backfill_checked) get retried
// on subsequent runs — these are sessions whose PR was created *after* the
// previous Stop hook, and they must not be stranded by the cursor.
func TestRunWithState_RetriesPendingBelowCursor(t *testing.T) {
	dir := t.TempDir()
	// Two entries: index 0 is pending (no URL, not yet checked), index 1
	// is settled (has URL, checked). Cursor sits at 2 (past both).
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/nonexistent/path/abc","repo":"user/repo","branch":"feat-pending","pr_urls":[],"transcript":"","parent_session_id":"","backfill_checked":false}`,
		`{"timestamp":"2026-03-01 11:00:00","session_id":"s2","cwd":"/tmp","repo":"user/repo","branch":"feat","pr_urls":["https://github.com/user/repo/pull/2"],"transcript":"","parent_session_id":"","backfill_checked":true}`,
	})
	statePath := filepath.Join(dir, "state.json")
	SaveState(statePath, State{LastBackfillOffset: 2, LastMetaCheck: time.Now()})

	if err := RunWithState(indexPath, statePath, false); err != nil {
		t.Fatal(err)
	}

	// s1's cwd is bogus → fetchPR returns markChecked=true.
	// Verify s1 was actually picked up (backfill_checked flipped to true).
	_, sessions, err := readBackForTest(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var s1 *testSession
	for i := range sessions {
		if sessions[i].SessionID == "s1" {
			s1 = &sessions[i]
			break
		}
	}
	if s1 == nil {
		t.Fatal("session s1 not found")
	}
	if !s1.BackfillChecked {
		t.Fatalf("expected s1.backfill_checked=true after retry, got false (cursor stranded the pending entry)")
	}
}

type testSession struct {
	SessionID       string `json:"session_id"`
	BackfillChecked bool   `json:"backfill_checked"`
}

func readBackForTest(path string) ([]struct{}, []testSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var out []testSession
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s testSession
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		out = append(out, s)
	}
	return nil, out, nil
}

// TestRunURLBackfill_SkipsPinnedSession verifies that a pinned session is
// not re-resolved by the (repo, branch) grouping path. Even if the entry
// satisfied the "empty pr_urls" filter (which it shouldn't, since pinned
// sessions always have one URL), pr_pinned is the canonical exclusion
// signal — no `gh pr list` should fire for these.
func TestRunURLBackfill_SkipsPinnedSession(t *testing.T) {
	dir := t.TempDir()
	// Pinned session with a placeholder pr_urls entry to make the test
	// also exercise the case where the URL list is non-empty (so the
	// existing pr_urls!=nil filter alone does NOT skip it). pr_pinned is
	// the load-bearing flag.
	indexPath := writeTestIndex(t, dir, []string{
		`{"timestamp":"2026-03-01 10:00:00","session_id":"s1","cwd":"/nonexistent/path/abc","repo":"user/repo","branch":"feat-pinned","pr_urls":["https://github.com/user/repo/pull/42"],"pr_pinned":true,"transcript":"","parent_session_id":"","backfill_checked":false}`,
	})

	_, sessions, err := sessionindex.ReadAll(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	// Force the pr_urls slice to be empty in our local copy so the
	// existing `len(s.PRURLs) == 0` filter would otherwise pick it up.
	// pr_pinned is the load-bearing flag that must skip the entry.
	for i := range sessions {
		sessions[i].PRURLs = nil
	}

	batch := sessionindex.Batch{
		PinPRs:      make(map[string]string),
		SessionURLs: make(map[string][]string),
		PRMetas:     make(map[string]sessionindex.PRMeta),
		EndUpdates:  make(map[string]sessionindex.EndUpdate),
	}
	if err := runURLBackfill(indexPath, sessions, false, false, DefaultGHCap, &batch); err != nil {
		t.Fatal(err)
	}

	// File must remain untouched: pinned session was never grouped, so
	// no Update / MarkChecked call could have fired. Verify pr_pinned
	// and pr_urls survive intact.
	_, after, err := sessionindex.ReadAll(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after[0].PRPinned {
		t.Fatal("pr_pinned should remain true after backfill")
	}
	if len(after[0].PRURLs) != 1 || after[0].PRURLs[0] != "https://github.com/user/repo/pull/42" {
		t.Fatalf("pr_urls altered: %v", after[0].PRURLs)
	}
	if after[0].BackfillChecked {
		t.Fatal("backfill_checked must NOT be set on pinned session (no API call should have fired)")
	}
}

func TestParsePRList_ChangesRequested(t *testing.T) {
	prs := parsePRList([]byte(`[
		{
			"url": "https://github.com/user/repo/pull/1",
			"state": "MERGED",
			"comments": [{}, {}],
			"reviews": [
				{"state": "APPROVED"},
				{"state": "CHANGES_REQUESTED"},
				{"state": "COMMENTED"},
				{"state": "CHANGES_REQUESTED"}
			]
		}
	]`))

	if len(prs) != 1 {
		t.Fatalf("len(prs) = %d, want 1", len(prs))
	}
	if got := countChangesRequested(prs[0].Reviews); got != 2 {
		t.Fatalf("countChangesRequested = %d, want 2", got)
	}
}

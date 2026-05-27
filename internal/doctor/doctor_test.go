package doctor

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishii1648/agent-telemetry/internal/agent"
	"github.com/ishii1648/agent-telemetry/internal/configpath"
	"github.com/ishii1648/agent-telemetry/internal/userid"
)

// fakeLoader returns a SettingsLoader that yields the given commands per
// (agent name → event → command) lookup.
func fakeLoader(byAgent map[string]map[string][]string) func(*agent.Agent) map[string][]string {
	return func(a *agent.Agent) map[string][]string {
		return byAgent[a.Name]
	}
}

func envWith(t *testing.T, agents []*agent.Agent, lookOK bool, settings map[string]map[string][]string) Env {
	t.Helper()
	look := func(string) (string, error) { return "/usr/local/bin/agent-telemetry", nil }
	if !lookOK {
		look = func(string) (string, error) { return "", errors.New("not found") }
	}
	return Env{
		LookPath:       look,
		BinaryName:     "agent-telemetry",
		Agents:         agents,
		SettingsLoader: fakeLoader(settings),
		LegacyPaths:    func() []string { return nil },
		UserResolver:   func() (string, userid.Source) { return "alice@example.com", userid.SourceConfig },
	}
}

func TestRun_ClaudeAllChecksPass(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}

	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFailure() {
		t.Fatalf("expected no failures, got: %s", buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"binary at /usr/local/bin/agent-telemetry",
		"[claude] data dir at " + dir,
		"[claude] hook registration:",
		"SessionStart: session-start ✓",
		"Stop: stop ✓",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestRun_CodexOptionalDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameCodex, DataDir: dir}

	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameCodex: {
			"SessionStart": {"agent-telemetry hook session-start --agent codex"},
			"Stop":         {"agent-telemetry hook stop --agent codex"},
			// PostToolUse intentionally missing (optional)
		},
	})

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFailure() {
		t.Fatalf("missing optional should not fail: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "post-tool-use ⚠ (optional") {
		t.Fatalf("optional warning missing:\n%s", buf.String())
	}
}

func TestRun_BothAgentsListed(t *testing.T) {
	dir := t.TempDir()
	c := &agent.Agent{Name: agent.NameClaude, DataDir: filepath.Join(dir, ".claude")}
	x := &agent.Agent{Name: agent.NameCodex, DataDir: filepath.Join(dir, ".codex")}
	os.MkdirAll(c.DataDir, 0755)
	os.MkdirAll(x.DataDir, 0755)

	env := envWith(t, []*agent.Agent{c, x}, true, map[string]map[string][]string{
		agent.NameClaude: {},
		agent.NameCodex:  {},
	})

	var buf bytes.Buffer
	if _, err := RunWith(&buf, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[claude] hook registration") ||
		!strings.Contains(buf.String(), "[codex] hook registration") {
		t.Errorf("expected both agents listed:\n%s", buf.String())
	}
}

func TestRun_BinaryMissing(t *testing.T) {
	dir := t.TempDir()
	env := envWith(t, []*agent.Agent{{Name: agent.NameClaude, DataDir: dir}}, false, nil)
	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasFailure() {
		t.Fatal("expected failure when binary missing")
	}
}

func TestRun_LegacyArtifactsSurfacedAsWarning(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}

	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {
				"hitl-metrics hook session-start",
			},
			"SessionEnd": {"agent-telemetry hook session-end"},
			"Stop":       {"agent-telemetry hook stop"},
		},
	})
	env.LegacyPaths = func() []string {
		return []string{"/home/user/.claude/hitl-metrics.db"}
	}

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFailure() {
		t.Fatalf("legacy artifacts must not fail doctor: %s", buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"legacy hitl-metrics artifacts detected",
		"hitl-metrics.db",
		"hitl-metrics hook session-start",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
}

func TestRun_UserIDResolved(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}

	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFailure() {
		t.Fatalf("user_id check should not cause failure: %s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "user_id: alice@example.com") || !strings.Contains(out, "source: config") {
		t.Errorf("user_id line missing/wrong:\n%s", out)
	}
}

func TestRun_UserIDUnknownSurfacedAsWarning(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}

	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})
	env.UserResolver = func() (string, userid.Source) { return userid.Unknown, userid.SourceUnknown }

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasFailure() {
		t.Fatalf("unknown user_id is a warning, not a failure: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "user_id: unknown") {
		t.Errorf("expected unknown warning:\n%s", buf.String())
	}
}

func TestRun_ConfigPathPreferredFound(t *testing.T) {
	a := &agent.Agent{Name: agent.NameClaude, DataDir: t.TempDir()}
	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})
	env.ConfigPathStatus = func() configpath.MigrationStatus {
		return configpath.MigrationStatus{
			Preferred:       "/x/.config/agent-telemetry/config.toml",
			Legacy:          "/x/.claude/agent-telemetry.toml",
			PreferredExists: true,
		}
	}

	var buf bytes.Buffer
	if _, err := RunWith(&buf, env); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "✓ config: /x/.config/agent-telemetry/config.toml") {
		t.Errorf("expected pass line for preferred path:\n%s", out)
	}
	if strings.Contains(out, "legacy file still present") {
		t.Errorf("legacy reminder should be hidden when legacy missing:\n%s", out)
	}
}

func TestRun_ConfigPathLegacyOnlyEmitsMigrationWarning(t *testing.T) {
	a := &agent.Agent{Name: agent.NameClaude, DataDir: t.TempDir()}
	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})
	env.ConfigPathStatus = func() configpath.MigrationStatus {
		return configpath.MigrationStatus{
			Preferred:    "/x/.config/agent-telemetry/config.toml",
			Legacy:       "/x/.claude/agent-telemetry.toml",
			LegacyExists: true,
		}
	}

	var buf bytes.Buffer
	r, err := RunWith(&buf, env)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ConfigPath.NeedsMigration() {
		t.Error("NeedsMigration should be true when only legacy exists")
	}
	out := buf.String()
	if !strings.Contains(out, "reading legacy") || !strings.Contains(out, "migrate to") {
		t.Errorf("expected migration warning:\n%s", out)
	}
}

func TestRun_ConfigPathBothExistShowsCleanupReminder(t *testing.T) {
	a := &agent.Agent{Name: agent.NameClaude, DataDir: t.TempDir()}
	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {
			"SessionStart": {"agent-telemetry hook session-start"},
			"SessionEnd":   {"agent-telemetry hook session-end"},
			"Stop":         {"agent-telemetry hook stop"},
		},
	})
	env.ConfigPathStatus = func() configpath.MigrationStatus {
		return configpath.MigrationStatus{
			Preferred:       "/x/.config/agent-telemetry/config.toml",
			Legacy:          "/x/.claude/agent-telemetry.toml",
			PreferredExists: true,
			LegacyExists:    true,
		}
	}

	var buf bytes.Buffer
	if _, err := RunWith(&buf, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "legacy file still present") {
		t.Errorf("expected cleanup reminder:\n%s", buf.String())
	}
}

func TestIsRegistered_MatchesLooseCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		sub  string
		want bool
	}{
		{"exact", "agent-telemetry hook session-start", "session-start", true},
		{"with --agent", "agent-telemetry hook session-start --agent codex", "session-start", true},
		{"absolute path", "/usr/local/bin/agent-telemetry hook stop", "stop", true},
		{"different sub", "agent-telemetry hook session-end", "session-start", false},
		{"unrelated", "/other/script.sh stop", "stop", false},
		{"legacy hitl-metrics", "hitl-metrics hook session-start", "session-start", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRegistered([]string{tc.cmd}, tc.sub)
			if got != tc.want {
				t.Fatalf("isRegistered(%q, %q) = %v, want %v", tc.cmd, tc.sub, got, tc.want)
			}
		})
	}
}

func TestRun_BacklogHintAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}
	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {"Stop": {"agent-telemetry hook stop"}},
	})
	env.BacklogCounter = func(*agent.Agent) int { return backlogDrainThreshold + 1 }

	var buf bytes.Buffer
	if _, err := RunWith(&buf, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "backfill --gc") {
		t.Errorf("expected --gc drain hint, got:\n%s", buf.String())
	}
}

// TestCountBacklog_ExcludesDefaultBranch verifies the backlog count mirrors the
// backfill candidate filter: default-branch sessions are structurally never PR
// candidates (excluded by runURLBackfill and never markChecked / retired by
// --gc), so counting them would produce an unresolvable --gc hint.
func TestCountBacklog_ExcludesDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session-index.jsonl")
	lines := []string{
		// counts: PR-less, unchecked, unpinned, not default branch
		`{"timestamp":"2026-03-01 10:00:00","session_id":"feat1","cwd":"/tmp","repo":"u/r","branch":"feat","pr_urls":[],"backfill_checked":false}`,
		// excluded: default branch (structurally never a candidate)
		`{"timestamp":"2026-03-01 10:00:00","session_id":"def1","cwd":"/tmp","repo":"u/r","branch":"main","is_default_branch":true,"pr_urls":[],"backfill_checked":false}`,
		// excluded: already has a URL
		`{"timestamp":"2026-03-01 10:00:00","session_id":"url1","cwd":"/tmp","repo":"u/r","branch":"feat2","pr_urls":["https://github.com/u/r/pull/1"],"backfill_checked":false}`,
		// excluded: already checked
		`{"timestamp":"2026-03-01 10:00:00","session_id":"chk1","cwd":"/tmp","repo":"u/r","branch":"feat3","pr_urls":[],"backfill_checked":true}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}
	if got := countBacklog(a); got != 1 {
		t.Errorf("countBacklog = %d, want 1 (default-branch / url'd / checked excluded)", got)
	}
}

func TestRun_NoBacklogHintBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	a := &agent.Agent{Name: agent.NameClaude, DataDir: dir}
	env := envWith(t, []*agent.Agent{a}, true, map[string]map[string][]string{
		agent.NameClaude: {"Stop": {"agent-telemetry hook stop"}},
	})
	env.BacklogCounter = func(*agent.Agent) int { return 5 }

	var buf bytes.Buffer
	if _, err := RunWith(&buf, env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "backfill --gc") {
		t.Error("did not expect --gc hint below threshold")
	}
}

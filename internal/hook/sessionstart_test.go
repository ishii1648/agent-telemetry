package hook

import (
	"os/exec"
	"testing"
)

func gitInit(t *testing.T, dir, initialBranch string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", initialBranch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("commit", "--allow-empty", "-m", "init")
}

// TestIsRepoDefaultBranch_FallbackByName covers the no-remote case: origin/HEAD
// is unset, so detection falls back to the conventional main/master names.
func TestIsRepoDefaultBranch_FallbackByName(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, "main")

	if !isRepoDefaultBranch(dir, "main") {
		t.Error("main should be treated as default when origin/HEAD is unset")
	}
	if isRepoDefaultBranch(dir, "feature-x") {
		t.Error("feature branch must not be treated as default")
	}
	if isRepoDefaultBranch(dir, "") {
		t.Error("empty branch must not be treated as default")
	}
}

// TestIsRepoDefaultBranch_DynamicFromOriginHEAD proves the detection is dynamic
// (reads the repo's actual default branch) rather than a main/master name
// hardcode: with origin/HEAD pointing at "trunk", trunk is default and main is
// not.
func TestIsRepoDefaultBranch_DynamicFromOriginHEAD(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, "trunk")

	// Set origin/HEAD -> origin/trunk locally (no real remote needed; symbolic-ref
	// just writes a symref that `git symbolic-ref --short` resolves).
	cmd := exec.Command("git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set origin/HEAD: %v\n%s", err, out)
	}

	if !isRepoDefaultBranch(dir, "trunk") {
		t.Error("trunk should be detected as the repo's default branch")
	}
	if isRepoDefaultBranch(dir, "main") {
		t.Error("main must not be default when origin/HEAD points at trunk")
	}
}

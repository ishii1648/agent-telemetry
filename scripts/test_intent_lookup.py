#!/usr/bin/env python3
"""scripts/intent-lookup の振る舞いを fixture git repo + assertions で検証する。

外部依存なし（標準ライブラリ unittest のみ）。fixture は TemporaryDirectory に
fresh な git repo を作って組み立てる。

intent-lookup は path → issue の対応を **issue 本文の grep**（手書きの
affected_paths ではなく）と git 履歴から動的に求める。そのため fixture issue は
本文中に対象 path を言及することでヒット対象になる。
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "scripts" / "intent-lookup"


def run_script(*args, cwd: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        cwd=str(cwd), capture_output=True, text=True,
    )


def run_git(*args, cwd: Path, env: dict | None = None) -> str:
    full_env = os.environ.copy()
    if env:
        full_env.update(env)
    res = subprocess.run(
        ["git", *args], cwd=str(cwd), capture_output=True, text=True,
        env=full_env, check=True,
    )
    return res.stdout


def write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content).lstrip("\n"), encoding="utf-8")


def make_fixture_repo(work: Path) -> dict:
    """Create a deterministic fixture git repo with issues + commits.

    issues reference their target paths in the BODY (no affected_paths
    frontmatter), since lookup matches by grepping the issue text.
    """
    env = {
        "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
        "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
        "GIT_AUTHOR_DATE": "2026-01-01T00:00:00Z",
        "GIT_COMMITTER_DATE": "2026-01-01T00:00:00Z",
    }
    run_git("init", "-q", "-b", "main", cwd=work, env=env)
    (work / "issues" / "closed").mkdir(parents=True)
    (work / "issues" / "pending").mkdir(parents=True)
    (work / "internal" / "hook").mkdir(parents=True)
    (work / "internal" / "syncdb").mkdir(parents=True)
    (work / "internal" / "other").mkdir(parents=True)
    (work / "cmd" / "agent-telemetry").mkdir(parents=True)
    (work / "issues" / "SEQUENCE").write_text("0006\n")

    # 0001 closed — mentions internal/hook/stop.go + internal/syncdb/ in body
    write(work / "issues" / "closed" / "0001-bug-fixture-stop.md", """
        ---
        decision_type: design
        tags: [hooks, fixture]
        closed_at: 2026-01-02
        ---

        # Fixture: Stop hook の何かを直す

        Created: 2026-01-01

        ## 概要

        fixture の stop hook 概要文。`internal/hook/stop.go` と `internal/syncdb/`
        に effect を持つ短い段落。

        ## 対応方針

        最小限の変更で signal handling を堅牢化する。

        ## 解決方法

        ### 段階 1: シグナル捕捉
        SIGTERM をメインループで処理するように変更。

        ### 段階 2: idempotent化
        多重呼び出しに耐える。
        """)

    # 0002 open — mentions internal/hook/ directory in body (ancestor-dir recall)
    write(work / "issues" / "0002-feat-fixture-hook-area.md", """
        ---
        decision_type: implementation
        tags: [hooks, fixture, broad]
        ---

        # Fixture: hook ディレクトリ全体の話

        Created: 2026-01-01

        ## 概要

        `internal/hook/` ディレクトリ全体を扱う広い範囲の fixture。
        """)

    # 0003 pending — unrelated path
    write(work / "issues" / "pending" / "0003-design-fixture-unrelated.md", """
        ---
        decision_type: design
        tags: [unrelated, fixture]
        ---

        # Fixture: 関係ない issue

        Created: 2026-01-01

        ## 概要

        `cmd/agent-telemetry/main.go` の話。stop hook とは無関係。
        """)

    # 0005 pending — mentions internal/other/, must NOT match an internal/hook query
    # (guards against a bare top-level component "internal" leaking into grep keys)
    write(work / "issues" / "pending" / "0005-design-fixture-other-internal.md", """
        ---
        decision_type: design
        tags: [fixture]
        ---

        # Fixture: 別の internal サブツリー

        Created: 2026-01-01

        ## 概要

        `internal/other/thing.go` の設計。hook とは別領域。
        """)

    # frontmatter なし issue (skip されるべき)
    write(work / "issues" / "no-frontmatter.md", """
        # 古い形式の issue

        frontmatter を持たない。`internal/hook/stop.go` に言及するが
        intent-lookup は skip するはず。
        """)

    # 内部ファイル + commits
    (work / "internal" / "hook" / "stop.go").write_text("package hook\n")
    (work / "cmd" / "agent-telemetry" / "main.go").write_text("package main\n")

    run_git("add", "-A", cwd=work, env=env)
    run_git("commit", "-q", "-m", textwrap.dedent("""
        feat(hook): add stop.go

        intent: capture stop signals
        decision: handle SIGTERM in main loop
    """).strip(), cwd=work, env=env)

    env2 = dict(env)
    env2["GIT_AUTHOR_DATE"] = "2026-01-02T00:00:00Z"
    env2["GIT_COMMITTER_DATE"] = "2026-01-02T00:00:00Z"
    (work / "internal" / "hook" / "stop.go").write_text("package hook // updated\n")
    run_git("-c", "commit.gpgsign=false", "commit", "-q", "-am",
            textwrap.dedent("""
                fix(hook): tweak stop.go

                constraint: must remain idempotent
                learned: subagent termination races with parent
            """).strip(), cwd=work, env=env2)

    env3 = dict(env)
    env3["GIT_AUTHOR_DATE"] = "2026-01-03T00:00:00Z"
    env3["GIT_COMMITTER_DATE"] = "2026-01-03T00:00:00Z"
    with (work / "internal" / "hook" / "stop.go").open("a") as f:
        f.write("// noop\n")
    run_git("-c", "commit.gpgsign=false", "commit", "-q", "-am",
            "chore(hook): noop comment", cwd=work, env=env3)

    return env


def make_rename_fixture(work: Path) -> dict:
    """Fixture for rename-aware lookup: file is renamed but issue still references old path."""
    env = {
        "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
        "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
        "GIT_AUTHOR_DATE": "2026-02-01T00:00:00Z",
        "GIT_COMMITTER_DATE": "2026-02-01T00:00:00Z",
    }
    run_git("init", "-q", "-b", "main", cwd=work, env=env)
    (work / "issues" / "closed").mkdir(parents=True)
    (work / "issues" / "pending").mkdir(parents=True)
    (work / "old").mkdir()
    (work / "issues" / "SEQUENCE").write_text("0002\n")

    # commit 1: create old/file.go; issue references old/file.go in body
    (work / "old" / "file.go").write_text("package old\n// substantial content\n" * 5)
    write(work / "issues" / "closed" / "0001-design-old-path-decision.md", """
        ---
        decision_type: design
        tags: [rename, fixture]
        closed_at: 2026-02-01
        ---

        # Fixture: old path で書かれた issue

        Created: 2026-02-01

        ## 概要

        後の rename を見越した issue。本文は古い path `old/file.go` を参照したまま。
        """)
    run_git("add", "-A", cwd=work, env=env)
    run_git("commit", "-q", "-m", "feat: add old/file.go", cwd=work, env=env)

    # commit 2: rename to new/file.go (substantial content preserved → git follows)
    env2 = dict(env)
    env2["GIT_AUTHOR_DATE"] = "2026-02-02T00:00:00Z"
    env2["GIT_COMMITTER_DATE"] = "2026-02-02T00:00:00Z"
    (work / "new").mkdir()
    run_git("mv", "old/file.go", "new/file.go", cwd=work, env=env2)
    run_git("-c", "commit.gpgsign=false", "commit", "-q", "-m",
            "refactor: rename old/ -> new/", cwd=work, env=env2)

    return env


class IntentLookupTests(unittest.TestCase):

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.work = Path(self._tmp.name)
        self.env = make_fixture_repo(self.work)

    def tearDown(self):
        self._tmp.cleanup()

    # ----- lookup: JSON structure -----
    def test_lookup_json_path_normalized(self):
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        self.assertEqual(r.returncode, 0, msg=r.stderr)
        data = json.loads(r.stdout)
        self.assertEqual(data["path"], "internal/hook/stop.go")

    def test_lookup_matches_exact_and_ancestor_dir_mentions(self):
        # 0001 mentions the exact file; 0002 mentions the parent dir internal/hook/
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        ids = sorted(i["id"] for i in data["issues"])
        self.assertEqual(ids, ["0001", "0002"])

    def test_lookup_excludes_unrelated(self):
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        ids = [i["id"] for i in data["issues"]]
        self.assertNotIn("0003", ids)

    def test_bare_top_level_component_does_not_overmatch(self):
        # 0005 mentions internal/other/...; querying internal/hook/stop.go must NOT
        # pull it in (grep keys are {internal/hook/stop.go, internal/hook}, never "internal")
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        ids = [i["id"] for i in data["issues"]]
        self.assertNotIn("0005", ids)

    def test_lookup_frontmatter_preserved(self):
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        i1 = next(i for i in data["issues"] if i["id"] == "0001")
        self.assertEqual(i1["frontmatter"]["decision_type"], "design")
        self.assertEqual(i1["frontmatter"]["closed_at"], "2026-01-02")
        i2 = next(i for i in data["issues"] if i["id"] == "0002")
        self.assertEqual(i2["status"], "open")

    # ----- lookup: section excerpts -----
    def test_lookup_includes_section_excerpts(self):
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        i1 = next(i for i in data["issues"] if i["id"] == "0001")
        self.assertIn("fixture の stop hook 概要文", i1["sections"]["summary"])
        self.assertIn("最小限の変更", i1["sections"]["approach"])
        # resolution starts with `### 段階 1` heading; excerpt should pull body line too
        self.assertIn("### 段階 1", i1["sections"]["resolution"])
        self.assertIn("SIGTERM をメインループで処理", i1["sections"]["resolution"])
        # but NOT 段階 2 (excerpt cap)
        self.assertNotIn("段階 2", i1["sections"]["resolution"])

    def test_lookup_full_includes_all_subheadings(self):
        r = run_script("--full", "--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        i1 = next(i for i in data["issues"] if i["id"] == "0001")
        self.assertIn("段階 1", i1["sections"]["resolution"])
        self.assertIn("段階 2", i1["sections"]["resolution"])
        self.assertIn("idempotent", i1["sections"]["resolution"])

    def test_markdown_renders_excerpts(self):
        r = run_script("internal/hook/stop.go", cwd=self.work)
        self.assertIn("**概要 (excerpt):**", r.stdout)
        self.assertIn("fixture の stop hook 概要文", r.stdout)
        self.assertIn("**対応方針 (excerpt):**", r.stdout)

    # ----- lookup: commits -----
    def test_lookup_commits_have_actions(self):
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        # 2 commits with action lines (noop excluded)
        self.assertEqual(len(data["commits"]), 2)
        all_actions = [a for c in data["commits"] for a in c["actions"]]
        self.assertTrue(any("intent: capture stop signals" in a for a in all_actions))
        self.assertTrue(any("learned:" in a for a in all_actions))

    # ----- lookup: directory query -----
    def test_dir_query_matches_mentions_within(self):
        # querying the directory matches both the exact-file issue and the dir issue
        r = run_script("--format=json", "internal/hook/", cwd=self.work)
        data = json.loads(r.stdout)
        ids = sorted(i["id"] for i in data["issues"])
        self.assertEqual(ids, ["0001", "0002"])

    def test_sibling_dir_mention_matches_via_ancestor(self):
        # internal/syncdb/foo.go → grep keys {.../foo.go, internal/syncdb};
        # 0001 mentions internal/syncdb/ → matched. 0002 (internal/hook) not.
        r = run_script("--format=json", "internal/syncdb/foo.go", cwd=self.work)
        data = json.loads(r.stdout)
        ids = sorted(i["id"] for i in data["issues"])
        self.assertEqual(ids, ["0001"])

    # ----- lookup: no-match -----
    def test_no_match_returns_empty(self):
        r = run_script("--format=json", "totally/unrelated/path", cwd=self.work)
        self.assertEqual(r.returncode, 0)
        data = json.loads(r.stdout)
        self.assertEqual(len(data["issues"]), 0)
        self.assertEqual(len(data["commits"]), 0)

    # ----- markdown structure -----
    def test_markdown_top_structure(self):
        r = run_script("internal/hook/stop.go", cwd=self.work)
        self.assertIn("# Intent for", r.stdout)
        self.assertIn("## Issues", r.stdout)
        self.assertIn("## Commits", r.stdout)
        self.assertIn("#0001", r.stdout)
        self.assertIn("#0002", r.stdout)
        self.assertIn("**lookup index**", r.stdout)

    # ----- CLI -----
    def test_help_exits_zero(self):
        r = run_script("--help", cwd=self.work)
        self.assertEqual(r.returncode, 0)

    def test_no_arg_fails(self):
        r = run_script(cwd=self.work)
        self.assertNotEqual(r.returncode, 0)

    def test_lint_flag_removed(self):
        # --lint was removed; argparse should reject it
        r = run_script("--lint", cwd=self.work)
        self.assertNotEqual(r.returncode, 0)

    # ----- frontmatter-less skip -----
    def test_frontmatter_less_skipped_in_lookup(self):
        # no-frontmatter.md mentions internal/hook/stop.go but must be skipped
        r = run_script("--format=json", "internal/hook/stop.go", cwd=self.work)
        data = json.loads(r.stdout)
        for i in data["issues"]:
            self.assertNotIn("no-frontmatter", i["path"])


class RenameAwareTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.work = Path(self._tmp.name)
        self.env = make_rename_fixture(self.work)

    def tearDown(self):
        self._tmp.cleanup()

    def test_query_new_path_finds_issue_with_old_path(self):
        # query the new path; rename resolution adds old/file.go to grep keys,
        # which the issue body still references → matched.
        r = run_script("--format=json", "new/file.go", cwd=self.work)
        self.assertEqual(r.returncode, 0, msg=r.stderr)
        data = json.loads(r.stdout)
        ids = [i["id"] for i in data["issues"]]
        self.assertIn("0001", ids)
        # resolved_paths should contain the historical old path
        self.assertIn("new/file.go", data["resolved_paths"])
        self.assertTrue(any("old/file.go" in p for p in data["resolved_paths"]))

    def test_query_old_path_still_finds_issue(self):
        # querying the old path itself: file no longer exists, but the body references it
        r = run_script("--format=json", "old/file.go", cwd=self.work)
        self.assertEqual(r.returncode, 0, msg=r.stderr)
        data = json.loads(r.stdout)
        ids = [i["id"] for i in data["issues"]]
        self.assertIn("0001", ids)


if __name__ == "__main__":
    unittest.main()

---
decision_type: process
tags: [retro, task-management, issues, todo]
closed_at: 2026-05-08
---

# TODO.md の廃止 — issues/ への一本化

Created: 2026-05-08
Retro-converted: 2026-05-10 (from docs/history.md §10)

## 概要

`TODO.md` を削除し、開発タスク管理を `issues/` ディレクトリ（per-issue Markdown + `SEQUENCE` 採番）に一本化した。

## 根拠

- `issues/` 体制を導入した時点で `TODO.md` が CLAUDE.md / AGENTS.md の 3 本柱（spec / design / history）の外側にある orphan になっていた
- tmux-sidebar の同種リファクタ（[ishii1648/tmux-sidebar#49](https://github.com/ishii1648/tmux-sidebar/pull/49)）を参考に同じ整理を当て、計測ツール側でも採番済みの per-issue ファイルでタスクを扱う形に揃えた
- TODO.md の「実装タスク」と「検討中」の境界が曖昧で、設計判断保留と着手可能タスクの区別がつきにくかった

## 問題

- `TODO.md` の「実装タスク」（受け入れ条件あり、すぐ着手可能）と「検討中」（仕様未確定）を、それぞれ open issue / pending issue にマッピングする必要
- `todo-cleanup` hook（main ブランチで TODO.md の完了タスクを自動削除）と関連サブコマンドが残置される
- impl skill が `TODO.md` をパースする実装になっており、入力ソースの差し替えが必要

## 対応方針

`TODO.md` を削除し、内容を `issues/` 配下の open / pending に分配する。`todo-cleanup` hook は削除。impl skill は `issues/*.md`（`closed/` と `pending/` を除外）を入力源に書き換え。

## 解決方法

### 移行内容

- `TODO.md` の `## 実装タスク` → `issues/<NNNN>-<cat>-<slug>.md` として open
  - `issues/0003-feat-grafana-agent-comparison-panel.md`
  - `issues/0004-chore-goreleaser-release-verification.md`
- `TODO.md` の `## 検討中` → `issues/pending/<NNNN>-design-<slug>.md` として保留
  - `issues/pending/0005-design-stop-hook-path-independence.md`
  - `issues/pending/0006-design-local-env-ci-reproducibility.md`
  - `issues/pending/0007-design-bash-context-monitoring.md`
  - `issues/pending/0008-design-retro-pr-integration.md`
- `issues/SEQUENCE` を `9` に bump

### 廃止に伴うコード/設定変更

- `internal/hook/todocleanup.go` と関連テストを削除
- `agent-telemetry hook todo-cleanup` サブコマンドを `cmd/agent-telemetry/main.go` から削除
- `internal/setup/` の `ClaudeHookSpecs` から `todo-cleanup` を除去。既存ユーザの `~/.claude/settings.json` から `agent-telemetry uninstall-hooks` で取り除けるよう `LegacyClaudeSubcommands = []string{"todo-cleanup"}` を追加
- `docs/spec.md` / `docs/usage.md` の hook 表から `todo-cleanup` 行を削除
- 旧 project-local hook（`.claude/settings.json` と `.codex/hooks.json` で `bash hooks/todo-cleanup-check.sh` を呼んでいた）を削除

### impl skill の入力ソース変更

- `.claude/skills/impl/SKILL.md` と `.agents/skills/impl/SKILL.md` を `TODO.md` パースから `issues/*.md`（`closed/` と `pending/` を除外）の列挙に書き換え
- 受け入れ条件は `## 受け入れ条件` セクションの `- [ ]` 行から抽出する
- Claude Code 用 / Codex 用の SKILL.md は別ファイルのまま個別に保持

## 採用しなかった代替

- **`TODO.md` を archive として残す**: 中身は完了タスク + history と重複する rationale だけになっており、リンク切れと混乱の温床になる
- **`todo-cleanup` hook を `issues/closed/` の整理に転用**: `issues/` のライフサイクルは `git mv` ベースの手動運用が前提（AGENTS.md 参照）。自動削除が要件と噛み合わない
- **ADR 形式へ戻す**: 既に `docs/archive/adr/` に保存されている過去 23 件で十分。新規判断は `docs/design.md` + Contextual Commits + 必要なら history.md という現行ルールを維持

## 後続の発展

- [0011](0011-feat-structured-intent-on-issues.md) で issue が「タスク兼意思決定記録」に拡張され、frontmatter / `make intent` 逆引きが導入された

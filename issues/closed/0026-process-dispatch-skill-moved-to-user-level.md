---
decision_type: process
tags: [retro, dispatch, skills, workflow]
closed_at: 2026-05-10
---

# `/dispatch` skill を user skill 化 — 本リポジトリから削除

Created: 2026-05-10
Retro-converted: 2026-05-10 (from docs/history.md「廃止された設計と理由」)

## 概要

[ADR-017](../../docs/archive/adr/017-automated-implementation-session-dispatch.md) で導入した `/dispatch` skill（本リポジトリ専用の `.claude/skills/dispatch/`）を、user skill（`~/.claude/skills/dispatch/`）として汎用化し、本リポジトリ内の skill 定義は削除した。

## 根拠

- 設計/実装セッションの自動 dispatch は本ツールに固有のフローではなく、複数のリポジトリで再利用したかった
- リポジトリ専用 skill として保持すると、別プロジェクトで使う際にコピペが必要になる
- user skill 化することで、Claude Code の skill 解決機構（`~/.claude/skills/`）を素直に使える

## 問題

- ADR-017 のステータスは「採用済み」のままで、`.claude/skills/dispatch/` の存在を前提に書かれている
- ADR-017 が指している `TODO.md` 入力ソースの記述が、その後 [0023](0023-process-deprecate-todo-md.md)（TODO.md 廃止）で実態と乖離した

## 対応方針

`.claude/skills/dispatch/` を本リポジトリから削除し、user skill (`~/.claude/skills/dispatch/`) に移管。フロー自体は維持。本リポジトリで残す類似機能（worktree + tmux 並列ディスパッチ）は `impl` skill (`.claude/skills/impl/`) に集約する。

## 解決方法

- 本リポジトリの `.claude/skills/dispatch/` を削除
- ADR-017 自身は archive にあり、書き換えは不要（歴史的経緯として残す）
- 並列ディスパッチフローは `impl` skill が引き継ぎ。0023 で `impl` の入力ソースを `TODO.md` から `issues/*.md` の列挙に書き換え済み
- 本リポジトリで `/dispatch` を呼び出すと user skill が解決される（Claude Code の通常動作）

## 採用しなかった代替

- **本リポジトリに `.claude/skills/dispatch/` を残す**: 別プロジェクトでの再利用にコピーが必要で、user skill 化の利点が消える
- **ADR-017 を「廃止」ステータスに変更**: フロー自体は廃止していない（user skill としてアクティブ）。「リポジトリから削除」だけが事実なので、history と本 retro issue で記録する方が正確

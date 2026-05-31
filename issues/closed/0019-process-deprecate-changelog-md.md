---
decision_type: process
tags: [retro, documentation, release-notes, changelog]
closed_at: 2026-05-02
---

# CHANGELOG.md → history.md / GitHub Release への一本化

Created: 2026-05-02
Retro-converted: 2026-05-10 (from docs/history.md §6)

## 概要

ADR から spec/design/history へ移行した直後、CHANGELOG.md と history.md が並走していた。両者の役割（WHAT vs WHY）は理屈の上では分離していたが、実態は重複が多く境界が曖昧だった。CHANGELOG.md を廃止し、リリース note・WHY・コミット judgement・WHAT log を 4 つの異なる store に再分配した。

## 根拠

- goreleaser によるバイナリ配布で GitHub Release が事実上のリリースノートとして機能していた
- CHANGELOG.md と history.md の境界（WHAT / WHY）は理屈の上では分離していたが、実運用では ADR 番号参照や同一イベントの重複記載で崩れていた
- 個人ツールであり、外部利用者向けの「CHANGELOG.md を見れば変更が分かる」需要が薄い

## 問題

- CHANGELOG.md と history.md の二重管理は、片方を更新し忘れた際の整合性が取りにくい
- `todo-cleanup` hook が「TODO.md 完了タスク → CHANGELOG.md 移送」を行っていた

## 対応方針

CHANGELOG.md を廃止し、4 つの store に役割を再分配する。

| 種類 | 行き先 |
|---|---|
| リリース単位の WHAT | GitHub Release（タグ push 時に goreleaser が自動生成） |
| 方針転換の WHY | `docs/history.md` |
| 1 コミット内の判断 | Contextual Commits のアクション行 |
| 個別コミットの WHAT | `git log` |

## 解決方法

- `CHANGELOG.md` を削除
- `todo-cleanup` hook の動作を「TODO.md 完了タスク → CHANGELOG.md 移送」から「TODO.md からの削除のみ」に変更（後にこの hook 自体も [0023](0023-process-deprecate-todo-md.md) で廃止）
- `git-ship` skill の CHANGELOG チェックステップを削除
- `docs/history.md` をプロジェクトの大方針転換を記録する narrative store として位置付け

## 採用しなかった代替

- **CHANGELOG.md を archive として残す**: 閲覧導線が増えるだけで価値なし
- **GitHub Release だけに集約**: タグ単位の WHAT のみで、リリース単位を跨ぐ大方針転換の文脈が失われる

## 後続の発展

- [0011](0011-feat-structured-intent-on-issues.md) で意思決定の primary store が `issues/` に移行し、history.md は事後ナラティブ要約に役割を絞る方針に再定義
- 0013 で history.md の retro 化を実施し、本 issue 自体もその対象として retro 化
- [0027](0027-process-deprecate-history-md.md) で `docs/history.md` 自体を廃止。本 issue が確立した 4 store split（GitHub Release / history.md / Contextual Commits / `git log`）は 3 store split に簡素化された（WHY の行き先は `issues/closed/` の retro issue）

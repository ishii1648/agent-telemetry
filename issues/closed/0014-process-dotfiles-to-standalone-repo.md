---
decision_type: process
tags: [retro, packaging, dotfiles, repository-structure]
closed_at: 2026-03-09
---

# dotfiles 内ディレクトリ → 別リポジトリへ分離

Created: 2026-03-09
Retro-converted: 2026-05-10 (from docs/history.md §1)

## 概要

当初は dotfiles リポジトリの `configs/claude/scripts/` 配下に hook スクリプトが他の Claude Code スクリプトと混在していた。`hitl-metrics/` 専用ディレクトリへの集約で結合度を下げ、最終的に独立リポジトリへ完全分離した。

## 根拠

- dotfiles の ADR 全 14 件中、本ツール由来のものが大半を占めていた。dotfiles 本来のスコープ（shell/editor/環境設定）と比較して相対的に過大
- 開発プロセス（CI / リリース / バージョニング）が dotfiles と独立して進化しはじめており、結合状態を維持するメリットが消えた
- 別リポジトリ化することで、本ツール独自のリリースサイクル（goreleaser によるバイナリ配布など）が dotfiles に影響を与えなくなる

## 問題

- 初期判断（[ADR-013](../../docs/archive/adr/013-claude-stats-directory-isolation.md)）では「ディレクトリ隔離（案A）」を採用し「別リポジトリ分離（案B）」は却下していたが、隔離後の実態として結合度が想定以上に低下したため、案 B を覆して採用するに至った

## 対応方針

ディレクトリ隔離を先行し、その後 ADR-013 の案 B（別リポジトリ分離）に移行する 2 段階アプローチ。

## 解決方法

- `hitl-metrics/` を独立リポジトリ（`github.com/ishii1648/hitl-metrics`、後に `agent-telemetry` にリネーム → [0021](0021-spec-rename-hitl-metrics-to-agent-telemetry.md)）として切り出し
- dotfiles 側の ADR は archive 扱いとし、新規 ADR は本リポジトリで管理
- ADR-013 のステータスは「部分廃止 — 案 A 実施・案 B 当初却下するも後に覆して採用」として記録

## 採用しなかった代替

- dotfiles 内に留める: ADR の占有率と開発プロセスの独立性から実態と合わなかった
- monorepo 化（dotfiles と本ツールを 1 リポジトリにまとめる）: dotfiles の責務範囲を逸脱し、リリース戦略が衝突する

## 参照

- [ADR-013](../../docs/archive/adr/013-claude-stats-directory-isolation.md)

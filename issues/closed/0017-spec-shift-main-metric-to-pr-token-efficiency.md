---
decision_type: spec
tags: [retro, metrics, dashboard, breaking]
closed_at: 2026-04-25
---

# permission UI 計測中心 → PR トークン効率中心

Created: 2026-04-25
Retro-converted: 2026-05-10 (from docs/history.md §4; date approximate, around ADR-023 adoption)

## 概要

主指標を `perm_rate`（permission UI 発生率）から `total_tokens` / `pr_per_million_tokens` に切り替え、permission UI 系の計測基盤（`permission_events` テーブル・`PermissionRequest` hook・関連 Grafana panel）を全廃した。

## 根拠

- Claude Code の auto mode 進化により permission UI 発生率は構造的に低下し続ける。短期的にも長期的にも改善対象として陳腐化が早い
- トークン効率（PR あたりの総 token 消費）はモデル世代の変化に対しても安定した指標で、coding agent の改善余地を持続的に測れる
- permission UI 計測は実装が複雑（hook 種別ごとに発火条件が異なる、`PermissionRequest` で安定化したが本質的な負債は残っていた）

## 問題

- 既存の dashboard / VIEW / hook がすべて perm_rate 前提で組まれていた
- `permission_events` テーブルと `PermissionRequest` hook の廃止は破壊的変更で、過去データの再集計が不可能になる

## 対応方針

PR 単位のトークン消費効率を主指標に据え、permission UI 計測系を完全廃止する。個人ツールであり外部利用者がいないため、後方互換は取らずに破壊的変更で押し切る。

## 解決方法

- `transcript_stats` テーブルに token 使用量カラム（input / output / cache）を追加（[ADR-020](../../docs/archive/adr/020-add-token-usage-columns-to-transcript-stats.md) を凍結→ ADR-023 で実現）
- `pr_metrics` VIEW を `total_tokens` / `pr_per_million_tokens` 中心に再構築
- `permission_events` テーブル・`PermissionRequest` hook・関連 Grafana panel をすべて削除
- 関連 ADR（ADR-003 / 007 / 008 / 009 / 012 / 014 / 022）を「廃止 (ADR-023)」または「一部廃止」ステータスに更新

## 採用しなかった代替

- **両指標の並走**: 計測・dashboard の複雑性が累積し、主指標の意味が曖昧になる
- **perm_rate を残しつつ token 効率を主に**: auto mode で perm_rate がほぼゼロに張り付くため、計測コストに対する情報量が少ない

## 参照

- [ADR-023](../../docs/archive/adr/023-pr-token-efficiency-metrics.md)
- [ADR-020](../../docs/archive/adr/020-add-token-usage-columns-to-transcript-stats.md)（Draft、ADR-023 で実現）

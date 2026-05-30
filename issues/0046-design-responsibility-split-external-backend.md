---
decision_type: design
affected_paths:
  - docs/design.md
depends_on: [0040, 0042, 0043]
tags: [design, otel, export, responsibility]
---

# `docs/design.md` に client / server / Collector / 外部 backend の責務分担を追記

Created: 2026-05-30

## 概要

[0040] の child issue (5)。外部 backend 経路が加わったことで責務が増えたため、`docs/design.md` に分担を明文化する。

## 根拠

[0040] で確定した責務:

- **client**: 設定可能な OTLP export と **`pr_metrics` のローカル集約（gauge 化）** を担う
- **server**: OTLP receiver + SQLite ingest だけ。cross-event 集約は引き続き SQLite VIEW（gauge は server を経由しない）
- **外部 backend**: gauge の格納・表示と event-level 集計を担う。**cross-event 集約は backend 側では行わない**（log-metric backend は join しないため）
- **collector レシピ採用時のみ Collector**: router として fanout と raw events 側の意味分類昇格を担う。direct では Pipeline が同等を担う

## 対応方針

[0042] / [0043] の実装が固まったあとに着手する（後続 PR で 1 セクション追記）:

1. `docs/design.md` の既存「採用しなかった代替」/「外部 backend」周辺に責務分担セクションを追加
2. cross-event 集約が client / SQLite VIEW にロックインされる理由（join 不可）を明記
3. direct / collector で何が差し替わって何が同じか（attribute 整形は両者で、facet/measure 化は Datadog 側のみ）を 1 図 or 表で

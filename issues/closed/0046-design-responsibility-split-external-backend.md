---
decision_type: design
depends_on: [0040, 0042, 0043]
tags: [design, otel, export, responsibility]
closed_at: 2026-05-30
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

Completed: 2026-05-30

## 解決方法

`docs/design.md` の「サーバ側集約パイプライン > 採用しなかった代替」直後（外部 backend / OTLP Metrics 部分採用を論じる論理的位置）に **「外部 backend 経路の責務分担（client / server / Collector / backend）」** サブセクションを追加した。

- **4 主体の責務**を 1 表で明文化: client（設定可能な OTLP export + `pr_metrics` ローカル集約の gauge 化）/ server（OTLP Logs receiver + SQLite ingest だけ、cross-event 集約は SQLite VIEW、gauge は経由しない）/ 外部 backend（gauge 格納・表示 + event-level 集計、cross-event 集約はしない）/ Collector（collector レシピ時のみ router fanout + raw events 側の attribute 整形、direct では Datadog Logs Pipeline）
- **cross-event 集約が client + SQLite VIEW にロックインされる理由**（events が `session_id` で join 必須なのに log-metric backend / stateless Collector は join 不可）を 1 段落で明記
- **direct / collector の差分表**: exporter（protobuf 切替 / JSON 据え置き）と credential 保持が差し替わり、attribute 整形は両方で実施（置き場所のみ差）、facet/measure 化と cross-event 集約は両レシピ共通という対応を 1 表で示した
- attribute 分類の表本体は複製せず `docs/spec.md`「OTLP export の attribute 意味分類」を、representation 対応は `docs/metrics.md`（[0045]）を参照に留めた

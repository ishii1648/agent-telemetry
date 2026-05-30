---
decision_type: spec
affected_paths:
  - docs/metrics.md
depends_on: [0040, 0043, 0044]
tags: [otel, export, metrics, docs]
---

# `docs/metrics.md` に backend 上の representation 対応を追記

Created: 2026-05-30

## 概要

[0040] の child issue (4)。`docs/metrics.md` の各メトリクスについて、外部 backend 上での representation を「`pr_metrics` 系は client 集約の gauge をそのまま使う / event-level 系は raw events から backend formula で出す」のどちらかで明示する。

## 根拠

[0040] で 2 representation（raw events Logs / pre-aggregated gauge）に分かれた帰結。metrics カタログを読む利用者が「どの metric を backend のどの object（log facet/measure か gauge series か）から作るか」を即座に判断できる状態を目指す。

## 対応方針

[0043] / [0044] の実装が固まったあとに着手する（任意 issue 扱い、優先度低）:

1. `docs/metrics.md` の各 metric 行に representation 列を追加（gauge / event-level）
2. event-level 系は backend formula の例（Datadog count / sum query 例）を参考程度に添える

## 触らない

- spec 本体（attribute 分類）は [0044]
- 責務分担の design.md 追記は [0046]

Completed: 2026-05-30

## 解決方法

`docs/metrics.md` のメトリクスカタログに **representation** 列を追加し、各メトリクスを gauge / event-level の 2 表現に分類した。既存カラム構成（メトリクス名 / 型 / 値 / 説明）を尊重し、列追加に留めた。

- **representation 説明セクション追加**（「外部 backend 上の representation」）: gauge（[0043] の client 集約 OTLP Metrics gauge）/ event-level（raw events Logs + backend formula）の 2 表現を定義。join 不可ゆえに 2 表現へ分けた決定理由は親 [0040] を 1 行参照に留め本文は冗長化させない
- **event-level の実 query 例**: Datadog log-based metric（`agent.transcript.scanned` から `sum of @input_tokens` を `@coding_agent`/`@model`/`@repo` で group by）を参考程度に併記。snapshot 系の latest-wins 重複への注意も 1 行添付
- **3 カタログ表に列追加**:
  - セッション単位（`sessions` + `transcript_stats`）: 全行 event-level
  - PR 単位（`pr_metrics`）: 全行 gauge（[0043] の client 集約 gauge をそのまま使う）
  - 同時実行数（`session_concurrency_*`）: event-level（`started`/`ended` の区間計算で再現、単純な count/sum では出ない旨を注記）

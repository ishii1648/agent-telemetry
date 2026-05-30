---
decision_type: spec
affected_paths:
  - docs/spec.md
  - deploy/otel-collector/
depends_on: [0040]
tags: [otel, export, semantic-conventions, datadog, attributes]
# 0042 が生成する collector レシピへ processor サンプルを追加配置する path
lint_ignore_missing:
  - deploy/otel-collector/
---

# attribute の意味分類を `docs/spec.md` に追記（backend 非依存 + Datadog リファレンス）

Created: 2026-05-30

## 概要

[0040] の child issue (3)。[0040] 本文「対応方針 > attribute の意味分類（B）」の対応表を `docs/spec.md` の正規仕様として固定する。あわせて 2 配布形式（direct 用の Datadog Logs Pipeline 設定 / collector 用の Collector processor サンプル）を同梱する。

## 根拠

詳細な分類根拠は [0040] 本文（B）に集約済み。要点:

- 低 cardinality 次元タグ / 単調増加だが gauge 識別に必須な次元（`pr_url`）/ 高 cardinality 識別子（facet only）/ 数値 measure / OTel resource 規約の 5 群
- raw events（Logs）側の全 attribute が対象。`pr_metrics` gauge の tag は VIEW projection に限られ別物
- attribute 整形（rename / drop）と facet/measure 化は層が違う。**facet/measure 化は Datadog 側 index 設定でしか実現できず Collector processor では代替不可**

## 対応方針

1. `docs/spec.md` に「OTLP export の attribute 意味分類」セクションを追加（backend 非依存の分類 + Datadog 用 concrete マッピングを 1 つの表で）
2. `deploy/otel-collector/` に attribute rename / resource 付与 / 高 cardinality drop の processor サンプルを置く
3. Datadog Logs Pipeline 設定（remapper）を direct レシピ向けに同梱（場所: `deploy/datadog/` 等）
4. **facet / measure 化は recipe を問わず Datadog 側成果物として** 別ファイル（手順 or terraform / API ペイロード）に切り出す

## 触らない

- gauge tag の VIEW projection 拡張は [0042] で（gauge は本 issue 範囲外）
- 他 backend（New Relic / Honeycomb / Grafana Cloud）への concrete マッピングは後続 issue

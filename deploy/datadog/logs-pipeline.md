# Datadog Logs Pipeline 設定（direct レシピの attribute 整形）

**direct レシピ向け**の attribute 整形（rename / resource 付与 / 高 cardinality の drop）を Datadog **Logs Pipeline** の processor で行う手順。collector レシピを使う場合は OTel Collector processor 側（[`../otel-collector/`](../otel-collector/)）が同等の整形を担うので、この Pipeline は不要。

> この層は **attribute 整形まで**。属性を検索 facet・集計 measure に昇格する **facet / measure 化は別層**で、Logs Pipeline では実現できない → [`facets-measures.md`](./facets-measures.md)。

分類の正本: [`docs/spec.md`「OTLP export の attribute 意味分類」](../../docs/spec.md#otlp-export-の-attribute-意味分類)

## 前提

- agent-telemetry の OTLP Logs は LogRecord の `eventName`（`agent.session.started` 等）と flat な attribute（`input_tokens` / `repo` / `session_id` …）を載せている。
- OTLP intake 経由の log には `service` / `env` / `version` が resource attribute（群 5）から付く。Pipeline ではこれらを前提に整形する。

## Pipeline processor（順序）

`Logs > Configuration > Pipelines` で新規 Pipeline を作り、フィルタを `service:agent-telemetry` にして以下の processor を順に追加する。

| # | processor | 目的 | 対象 attribute |
|---|---|---|---|
| 1 | **Attribute Remapper** | rename: `task_type` を予約外の安定キーへ正規化（例: そのまま `task_type`、必要なら別名へ） | 群 1 |
| 2 | **Attribute Remapper → Tag** | 群 1 の低 cardinality 属性を **tag** に昇格 | `coding_agent` / `agent_version` / `user_id` / `repo` / `task_type` / `end_reason` / `model` / `pr_state` / `is_merged` / `is_subagent` / `is_ghost` |
| 3 | **Attribute Remapper → Tag** | 群 2: gauge 主キー兼次元。tag 化するが cardinality コストに注意 | `pr_url` |
| 4 | **Attribute Remapper（drop）** | 群 3 の高 cardinality 識別子と低価値属性を **tag 化せず** に残す or 削る | `session_id` / `parent_session_id` / `branch` / `pr_title` / `cwd` / `transcript` / `pr_pinned` / `backfill_checked` |

> **群 3（`session_id` 等）は tag に昇格しない。** 検索 facet には [`facets-measures.md`](./facets-measures.md) で個別に facet 化する。tag にすると custom metric / index の cardinality が破綻する。
>
> **群 4（数値 measure）は Pipeline では昇格できない。** `input_tokens` 等を集計対象にするのは measure 設定（[`facets-measures.md`](./facets-measures.md)）の仕事で、Logs Pipeline remapper の役割ではない。

## `is_subagent` の導出

`is_subagent` は raw attribute には存在しない（`parent_session_id` の非空判定で導出する）。Pipeline で tag 化したい場合は **Category Processor** で `parent_session_id` の有無から `is_subagent:true|false` を生成する:

- `@parent_session_id:*` にマッチ → `is_subagent:true`
- それ以外 → `is_subagent:false`

## Terraform

上記 Pipeline を IaC で管理する場合は [`logs-pipeline.tf`](./logs-pipeline.tf)（`datadog_logs_custom_pipeline`）を参照。

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
| 1 | **Category Processor** | `is_subagent` / `task_type` を導出（raw attribute に無い、下記参照） | `parent_session_id` → `is_subagent`、`branch` → `task_type` |
| 2 | **Attribute Remapper → Tag**（属性ごとに 1 つ） | 群 1 の低 cardinality 属性を **tag** に昇格 | `coding_agent` / `agent_version` / `user_id` / `repo` / `end_reason` / `model` / `pr_state` / `is_merged` / `is_ghost` |
| 3 | **Attribute Remapper → Tag** | 群 2: gauge 主キー兼次元。tag 化するが cardinality コストに注意 | `pr_url` |
| 4 | **Attribute Remapper（drop）** | 群 3 の高 cardinality 識別子と低価値属性を **tag 化せず** に残す or 削る | `session_id` / `parent_session_id` / `branch` / `pr_title` / `cwd` / `transcript` / `pr_pinned` / `backfill_checked` |

> **#2 は属性ごとに 1 つの Remapper が要る。** `attribute_remapper` は複数 `sources` を**単一 target へ統合**する processor なので、複数の属性を並べて空 target に流すと 1 つの tag に畳まれてしまう。各属性をそれぞれ同名 tag へ昇格するには属性 1 つにつき Remapper 1 つを作る（Terraform 例は [`logs-pipeline.tf`](./logs-pipeline.tf) の `dynamic "processor"` を参照）。
>
> **#1 は #4 の drop より前に置く。** `task_type` / `is_subagent` の導出元（`branch` / `parent_session_id`）は #4 で drop されるため、導出を先に済ませる。
>
> **群 3（`session_id` 等）は tag に昇格しない。** 検索 facet には [`facets-measures.md`](./facets-measures.md) で個別に facet 化する。tag にすると custom metric / index の cardinality が破綻する。
>
> **群 4（数値 measure）は Pipeline では昇格できない。** `input_tokens` 等を集計対象にするのは measure 設定（[`facets-measures.md`](./facets-measures.md)）の仕事で、Logs Pipeline remapper の役割ではない。

## 派生次元タグ（`is_subagent` / `task_type`）の導出

`is_subagent` / `task_type` は raw attribute には存在せず、それぞれ `parent_session_id` / `branch` から導出する。Pipeline で tag 化したい場合は **Category Processor** を使う:

- `is_subagent`: `@parent_session_id:*` にマッチ → `is_subagent:true`、それ以外 → `is_subagent:false`
- `task_type`: `branch` のプレフィックスで分類 — `@branch:feat\/*` → `feat`、`@branch:fix\/*` → `fix`、`@branch:docs\/*` → `docs`、`@branch:chore\/*` → `chore`

## Terraform

上記 Pipeline を IaC で管理する場合は [`logs-pipeline.tf`](./logs-pipeline.tf)（`datadog_logs_custom_pipeline`）を参照。

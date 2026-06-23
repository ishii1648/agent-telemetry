# Datadog レシピ（direct export + 共通 index 設定）

`agent-telemetry flush` の OTLP Logs を Datadog で受けるための成果物。direct / collector いずれの配布レシピでも、Datadog 側で attribute を一級の tag / facet / measure にするための設定をここに集約する。

分類の正本は [`docs/spec.md` の「OTLP export の attribute 意味分類」](../../docs/spec.md#otlp-export-の-attribute-意味分類)。設計根拠は [`issues/closed/0040-design-pluggable-otlp-export-backends.md`](../../issues/closed/0040-design-pluggable-otlp-export-backends.md) 本文（B）。

## ファイル構成

| ファイル | 層 | 対象レシピ | 内容 |
|---|---|---|---|
| [`logs-pipeline.md`](./logs-pipeline.md) | attribute 整形（rename / resource 付与 / drop） | **direct** | Datadog Logs Pipeline（remapper）の設定手順。collector レシピでは Collector processor 側が担うので不要 |
| [`logs-pipeline.tf`](./logs-pipeline.tf) | 同上 | **direct** | 上記 Pipeline の Terraform（`datadog_logs_custom_pipeline`）payload |
| [`facets-measures.md`](./facets-measures.md) | **facet / measure 化** | **direct / collector 共通** | 属性を検索 facet・集計 measure に昇格する手順。**Collector processor では代替不可**なので recipe を問わず必要 |

## 層の切り分け（重要）

attribute の Datadog への落とし込みは 2 層に分かれる:

1. **attribute 整形**（rename / resource 付与 / 高 cardinality の drop）
   - **direct レシピ**: Datadog **Logs Pipeline remapper** で行う → [`logs-pipeline.md`](./logs-pipeline.md)
   - **collector レシピ**: **OTel Collector processor** で行う → [`../otel-collector/`](../otel-collector/)（Datadog Logs Pipeline は不要）
2. **facet / measure 化**（属性を検索 facet・集計 measure に昇格）
   - **direct / collector 共通**で **Datadog 側 index 設定**が必要 → [`facets-measures.md`](./facets-measures.md)
   - **Collector processor では代替できない**（collector レシピでも Datadog 側設定は別途必要）

## direct レシピの credential モデル

direct は client（agent-telemetry flush）が Datadog Intake に直送する。submit-only の `DD-API-KEY` を client 設定に置く。個人 / 小チーム向け（根拠は [0040] の使い分け表）。team / 多 client では client に秘密を持たせない collector レシピ（[`../otel-collector/`](../otel-collector/)）を推奨。

> **direct は `agent-telemetry flush` 本体で動作する**。[0042] / [0043] / [0047] で `encoding = "protobuf"` + `auth_header = "dd-api-key"` を `internal/serverclient` に実装済みで、Datadog の OTLP/HTTP protobuf intake へ追加実装なしで送れる。具体的な `config.toml` 設定例・site → endpoint 早見表・payload 上限などの運用注意は [setup/datadog](https://ishii1648.github.io/agent-telemetry/setup/datadog/) を参照。本ディレクトリは Datadog 側の受け設定（attribute 整形 / facet / measure）のみを扱う。

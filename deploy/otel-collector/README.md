# OTel Collector レシピ（collector 経由 export）

`agent-telemetry flush` が出力する OTLP/HTTP Logs を **OTel Collector 経由** で外部 backend（ここでは Datadog をリファレンス）へ fanout するサンプル。client は既存の JSON OTLP exporter のままで、Collector が protobuf + `DD-API-KEY` への変換と attribute 整形を担う。

このディレクトリの processor サンプルは [`docs/spec.md` の「OTLP export の attribute 意味分類」](../../docs/spec.md#otlp-export-の-attribute-意味分類)を Collector 側で適用するためのもの。設計判断の根拠は [`issues/closed/0040-design-pluggable-otlp-export-backends.md`](../../issues/closed/0040-design-pluggable-otlp-export-backends.md) 本文（B）。

## このレシピが担う範囲（と担わない範囲）

attribute の整形は **2 層**に分かれる（spec 参照）。Collector が担うのは **attribute 整形まで**:

| 層 | Collector で可能か | 置き場所 |
|---|---|---|
| **attribute 整形**（resource 付与 / rename / 高 cardinality の drop） | ✅ 可能（`collector-config.yaml`） | このディレクトリ |
| **facet / measure 化**（属性を検索 facet・集計 measure に昇格） | ❌ **不可** | [`../datadog/facets-measures.md`](../datadog/facets-measures.md) |

> **重要**: facet / measure 化は Datadog 側の index 設定でしか実現できず、**Collector processor では代替できない**。collector レシピを採っても、Datadog 上で attribute を一級の facet / measure にするには [`../datadog/facets-measures.md`](../datadog/facets-measures.md) の手順が別途必要。

## 構成

```
agent-telemetry flush ──(OTLP/HTTP Logs, JSON)──▶ OTel Collector ──(protobuf + DD-API-KEY)──▶ Datadog
                                                        │
                                                        └─(任意)──▶ SQLite ingest / 他 backend
```

- client 側 `config.toml` の `[server] endpoint` を Collector の OTLP receiver に向ける（既存の JSON exporter のまま）。
- `DD_API_KEY` 環境変数を Collector プロセスに渡す。**client は backend credential を一切持たない**（team / 多 client 向けの secret モデル。根拠は [0040] 「なぜ team では collector か」）。

## 使い方

1. `DD_API_KEY` / `DD_SITE`（既定 `datadoghq.com`）/ `DEPLOY_ENV` を環境変数で設定。
2. `collector-config.yaml` で Collector を起動（[OpenTelemetry Collector Contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib) ディストリビューションが必要 — `datadog` exporter / `transform` / `resource` processor を含む）。
3. client 側 `config.toml` の `[server] endpoint` を Collector（例 `http://collector.internal:4318`）に向ける。
4. Datadog 上で facet / measure を有効化（[`../datadog/facets-measures.md`](../datadog/facets-measures.md)）。

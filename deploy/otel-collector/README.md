# OTel Collector レシピ（collector 経由 export）

`agent-telemetry flush` が出力する OTLP/HTTP Logs を **OTel Collector 経由** で外部 backend（ここでは Datadog をリファレンス）へ fanout するサンプル。client は既存の JSON OTLP exporter のままで、Collector が protobuf + `DD-API-KEY` への変換と attribute 整形を担う。

export target 配列・endpoint モデル・encoding の外部契約は [`docs/spec.md` の「サーバ送信」](../../docs/spec.md)、attribute の意味分類は [「OTLP export の attribute 意味分類」](../../docs/spec.md#otlp-export-の-attribute-意味分類)を参照。設計判断の根拠は [`issues/closed/0040-design-pluggable-otlp-export-backends.md`](../../issues/closed/0040-design-pluggable-otlp-export-backends.md) 本文（transport は 0042、attribute 整形は 0044）。

## direct と collector の使い分け

| 規模 | レシピ | 根拠 |
|---|---|---|
| 個人 / 小チーム（想定主用途） | **direct** | 追加プロセス無し。client の OTLP exporter を backend に直接向け、submit-only credential を自分のマシンに置く。Datadog direct logs は protobuf 必須（[`../datadog/`](../datadog/) 参照） |
| team / 多 client | **collector**（本ディレクトリ） | Collector が credential を 1 箇所で集約し、**client は backend secret を一切持たない**。encoding 変換・fanout・buffer/retry を一貫処理 |

collector レシピの隠れた利点: **client は既定の `encoding = "json"` exporter のまま**で済む。Datadog direct OTLP Logs intake は protobuf 必須だが、その変換は Collector の `datadog` exporter が担う。

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

- client 側 `config.toml` の export target を Collector の OTLP receiver に向ける（既存の JSON exporter のまま）。
- `DD_API_KEY` 環境変数を Collector プロセスに渡す。**client は backend credential を一切持たない**（team / 多 client 向けの secret モデル。根拠は [0040] 「なぜ team では collector か」）。

client 側設定（Collector を指す export target。Datadog key は client に置かない）:

```toml
[[export]]
id = "collector"
endpoint = "http://collector.internal:4318"   # base URL; client が /v1/logs を補完
token = "${COLLECTOR_TOKEN}"                  # Collector の前段に置く認証
# encoding は既定 "json" のまま（レガシー [server] 経路と同じ）
```

レガシー `[server]` セクションでも同じく動く（安定 ID `"server"` の単一 target に正規化される）。

## 使い方

1. `DD_API_KEY` / `DD_SITE`（既定 `datadoghq.com`）/ `DEPLOY_ENV` を環境変数で設定。
2. `collector-config.yaml` で Collector を起動（[OpenTelemetry Collector Contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib) ディストリビューションが必要 — `datadog` exporter / `transform` / `resource` processor を含む）。再現性のため image は `otel/opentelemetry-collector-contrib:0.153.0` に pin している（`:latest` だと OTTL の挙動が版で動く）。ローカル検証は同梱の `docker-compose.yaml`:

   ```fish
   set -x DD_API_KEY <your-datadog-api-key>
   set -x DD_SITE datadoghq.com
   set -x DEPLOY_ENV dev
   docker compose up
   ```

3. client 側 `config.toml` の export target を Collector（例 `http://collector.internal:4318`）に向ける。
4. Datadog 上で facet / measure を有効化（[`../datadog/facets-measures.md`](../datadog/facets-measures.md)）。

## スコープ外

- **`pr_metrics` gauge**（OTLP Metrics）— child issue **0043**。本レシピは raw events（OTLP Logs）の fanout のみ。

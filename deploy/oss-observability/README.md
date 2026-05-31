# OSS observability レシピ（Collector → Mimir / Loki / Grafana）

`agent-telemetry flush` の OTLP/HTTP **Logs と Metrics** を **OTel Collector 経由**で OSS backend に push し、Grafana の PromQL / LogQL で確認するローカル E2E 構成（issue [0050](../../issues/closed/0050-feat-oss-observability-local-compose.md)）。

Datadog レシピ（[`../otel-collector/`](../otel-collector/)）と同じ「Collector が backend へ push する」構成を、credential 不要の OSS で再現するのが目的。Prometheus scrape（pull 型）では Datadog exporter と運用感・失敗点がずれるため、あえて push 経路で揃える。

## データフロー

```text
hooks
  -> local SQLite
  -> agent-telemetry flush
  -> OTel Collector (:4318, OTLP/HTTP JSON)
       -> Mimir  (:9009 /otlp/v1/metrics)  # pr_metrics gauge
       -> Loki   (:3100 /otlp/v1/logs)     # raw events logs
  -> Grafana (:13001)  # PromQL / LogQL
```

- **metrics**（`pr_metrics` gauge / OTLP Metrics）→ Mimir。client がローカル `pr_metrics` VIEW を評価して PR 単位の pre-aggregated gauge を送る（[docs/spec.md「`pr_metrics` gauge representation」](../../docs/spec.md#pr_metrics-gauge-representationotlp-metrics)）。
- **logs**（raw events / OTLP Logs）→ Loki。専用 exporter ではなく Loki 3.x の native OTLP endpoint へ `otlphttp` で送る。
- client は既定の `encoding = "json"` のまま。Collector が backend へ fanout する。

## 使い方

1. compose を起動。リポジトリルートから `make oss-up`（停止は `make oss-down`）で 1 コマンド起動できる。SQLite + Grafana（`make grafana-up`）に代わるローカル第一級の可視化選択肢（issue [0055](../../issues/0055-design-local-otel-visualization-migration.md) ①）。Grafana は `:13001` で上がる。port を変えたい場合、make 経由は `OSS_GRAFANA_PORT=<port> make oss-up`（recipe が compose の `GRAFANA_PORT` に渡すため `make oss-up GRAFANA_PORT=<port>` は効かない）。port / 環境を細かく変えたい・生コマンドで上げたい場合は `DEPLOY_ENV`（Grafana 上の `deployment.environment` 表示用、未設定なら `dev`）を設定して:

   ```fish
   cd deploy/oss-observability
   set -x DEPLOY_ENV dev
   docker compose up
   ```

   Grafana が `:13001`（`GRAFANA_PORT` で上書き可。既存 SQLite dashboard の `:13000` と分けてある）で上がる。匿名ログイン有効・Admin。

2. client 側 `config.toml` に export target を追加（[`config.toml.example`](config.toml.example) 参照）:

   ```toml
   [[export]]
   id = "oss-collector"
   endpoint = "http://localhost:4318"
   encoding = "json"
   signals = ["logs", "metrics"]
   ```

3. flush を実行（リポジトリルートからは `make oss-flush` がビルド + `sync-db` + `flush` を一括実行する。導入済みバイナリが古い場合に便利）:

   ```fish
   agent-telemetry flush
   ```

4. Grafana（<http://localhost:13001>）の **agent-telemetry (OSS)** フォルダの `agent-telemetry (OSS backend)` dashboard を開く。dashboard は次で構成される:
   - **状態評価（Tier 2）** — merged PRs / total tokens / PR per 1M tokens の stat と週別 merged PR 数 trend。既存 `agent_pr_*` gauge を `last_over_time` で集約した近似。⚠ `pr_metrics`（`is_merged = 1` 限定）由来なので「merged-PR に寄与した分」であり全 session 総量ではない（各パネル description に明記）。
   - **PR 単位の外れ値検出（Tier 1）** — PR 別 token スコアカード / session_count / tokens per tool_use。
   - **Raw events（Tier 1）** — Loki の OTLP Logs。

   session-grain（top-level sessions 数・週別 tokens/session 等の Tier 3）は新規 export が要るため未表示（[issues/0053](../../issues/0053-design-otel-dashboard-tier2-tier3-restore.md)）。並列度（Tier 4）は [issues/0054](../../issues/closed/0054-design-abandon-concurrency-metrics-otel.md) で abandon。

## 確認クエリ

`agent_pr_*` は flush した瞬間だけ push される **sparse gauge** なので、素の instant クエリ（`sum(agent_pr_total_tokens)` 等）は最後の flush から Prometheus lookback delta（既定 5 分）を超えると空になる。range 集計は必ず `last_over_time(metric[<range>])` で最終値を拾う（[docs/metrics.md](../../docs/metrics.md) の gauge range 集計の前提と整合）:

| backend | データソース | クエリ例 |
|---|---|---|
| Mimir | PromQL | `sum by (coding_agent) (max by (pr_url, coding_agent, user_id) (last_over_time(agent_pr_total_tokens[$__range])))`（total tokens; 安定キー dedup） |
| Mimir | PromQL | `topk(10, last_over_time(agent_pr_total_tokens[$__range]))` |
| Mimir | PromQL | `count(group by (pr_url) (last_over_time(agent_pr_total_tokens[$__range])))`（merged PR 数） |
| Loki | LogQL | `{service_name="agent-telemetry"}` |

`pr_metrics` gauge の metric 名は `agent_pr_*`（`agent_pr_total_tokens` / `agent_pr_fresh_tokens` / `agent_pr_session_count` / `agent_pr_tokens_per_session` ほか）。dimension（label）は `pr_url` / `coding_agent` / `user_id` / `task_type` / `model`（[docs/spec.md](../../docs/spec.md#pr_metrics-gauge-representationotlp-metrics)）。

> **OTLP → Prometheus 変換**: resource attribute の `service.name` は Mimir で `job` ラベルに、`deployment.environment` は `target_info` に載る。gauge の metric 名には suffix を付けない（Mimir の suffix 付与は既定 off）。

## Datadog レシピとの対応

| 項目 | Datadog レシピ | 本レシピ |
|---|---|---|
| receiver | OTLP/HTTP `:4318` | 同じ |
| client encoding | `json`（Collector が protobuf 変換） | `json` |
| metrics 送信先 | （scope 外 / 別 issue） | Mimir `otlphttp` |
| logs 送信先 | Datadog exporter | Loki `otlphttp`（native OTLP） |
| credential | `DD_API_KEY` を Collector に渡す | 不要 |
| attribute 整形 | OTTL で rename / drop | resource 付与のみ（最小） |

Datadog レシピは attribute 整形（OTTL での rename / 高 cardinality drop）まで担うが、本レシピは OSS baseline として **resource 付与のみ**に絞る。整形が必要なら Datadog レシピの `transform/agent_telemetry` を移植できる。

## Kubernetes への橋渡し

各 service / config / volume は manifest に移しやすい単位で切ってある（対応方針 5）:

| compose | Kubernetes |
|---|---|
| `otel-collector` service + `collector-config.yaml` | Deployment + ConfigMap |
| `mimir` / `loki` service + `*.yaml` | StatefulSet + ConfigMap + PVC |
| `mimir-data` / `loki-data` volume | PVC |
| `grafana` provisioning | ConfigMap（datasource / dashboard provider）+ dashboard ConfigMap |

本レシピは single-process / filesystem storage / replication=1 の **検証用最小構成**。本番では multitenancy・object storage・replication を有効化する。

---
decision_type: implementation
affected_paths:
  - deploy/oss-observability/
  - docs/spec.md
  - docs/design.md
  - site/content/setup/server/
tags: [otel, collector, oss, mimir, loki, grafana]
closed_at: 2026-05-31
---

# OSS observability backend のローカル compose レシピを追加する

Created: 2026-05-31

## 概要

Datadog に送る構成と可能な限り近い OSS 検証環境として、
`agent-telemetry flush` から OTel Collector を経由し、Mimir / Loki / Grafana
で確認できるローカル compose レシピを追加する。

想定するデータフロー:

```text
hooks
  -> local SQLite
  -> agent-telemetry flush
  -> OTel Collector
      -> Mimir  # pr_metrics gauge
      -> Loki   # raw events logs
  -> Grafana
```

## 根拠

現在の Datadog collector レシピは、Collector が backend へ push する構成を
前提にしている。Prometheus scrape だけの検証では Collector 以降が pull 型に
変わり、Datadog exporter と運用感・失敗点がずれる。

一方、既存実装はすでに `[[export]]` target、OTLP Logs、`pr_metrics` の OTLP
Metrics gauge、target ごとの cursor を持っている。したがって client 側の
送信実装を大きく変えず、Collector の exporter と backend compose を足すだけで
Datadog に近い OSS baseline を作れる。

## 問題

- `deploy/otel-collector/` は Datadog exporter をリファレンスにした recipe で、
  Mimir / Loki / Grafana を含むローカル E2E 構成がない。
- 既存の top-level `docker-compose.yaml` は SQLite datasource の Grafana 用で、
  PromQL / LogQL で `pr_metrics` gauge と raw events を確認する経路ではない。
- Datadog 以外の backend で、`signals = ["logs", "metrics"]` が実際に
  Collector 経由で扱えるかを開発時に再現できない。

## 対応方針

1. `deploy/oss-observability/` にローカル compose レシピを追加する。
2. Collector は OTLP receiver で client から受け、metrics は Mimir、logs は
   Loki に push する。Loki は専用 exporter ではなく native OTLP endpoint へ
   `otlphttp` で送る構成を優先する。
3. Grafana provisioning は Mimir / Loki datasource と最小 dashboard を同梱する。
   既存 SQLite dashboard は置き換えず、OSS backend 検証用の別 dashboard とする。
4. client 側の `config.toml` 例は `endpoint = "http://localhost:4318"`、
   `encoding = "json"`、`signals = ["logs", "metrics"]` を基本にする。
5. サーバ deploy への橋渡しとして、compose の service / config / volume 境界を
   Kubernetes manifest に移しやすい単位に揃える。

## 採用しなかった代替

- Prometheus scrape のみ: OSS としては軽いが、Datadog exporter と同じ
  Collector push 経路の検証にならない。
- Mimir のみ: `pr_metrics` gauge は確認できるが、raw events logs の経路が
  Datadog Logs 相当として検証できない。
- 既存 `deploy/otel-collector/` を直接拡張: Datadog recipe と OSS recipe の目的が
  混ざるため、新規ディレクトリに分ける。

Completed: 2026-05-31

## 解決方法

`deploy/oss-observability/` に Collector push 型のローカル E2E compose レシピを追加した（対応方針 1〜5 を実装）。

- Collector は OTLP/HTTP `:4318` で受け、metrics は Mimir の OTLP ingest（`/otlp/v1/metrics`）、logs は Loki 3.x の native OTLP（`/otlp/v1/logs`）へ `otlphttp` で push（対応方針 2）。
- Grafana は Mimir(PromQL)/Loki(LogQL) datasource と専用 dashboard `agent-telemetry (OSS backend)` を provisioning。既存 SQLite dashboard とは別系統（Grafana port を `:13001` に分離、対応方針 3）。
- client `config.toml.example` は `endpoint = "http://localhost:4318"` / `encoding = "json"` / `signals = ["logs", "metrics"]`（対応方針 4）。
- service / config / volume は Kubernetes（Deployment + ConfigMap + PVC）に移しやすい単位で分割（対応方針 5、README に対応表）。
- 再現性のため Collector(0.153.0) / Mimir(2.14.2) / Loki(3.3.2) / Grafana(11.5.2) を image pin。`docs/spec.md`・`docs/design.md`・`site/content/setup/server/` も同 PR で同期更新。

E2E では合成 OTLP payload で `agent_pr_total_tokens` gauge が dimension 付きで Mimir に届き、`service.name` が Mimir の `job` ラベル・Loki の `service_name` ラベルへ昇格することを確認済み。

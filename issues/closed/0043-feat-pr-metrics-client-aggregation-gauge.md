---
decision_type: implementation
depends_on: [0040, 0042]
tags: [otel, export, metrics, gauge, pr-metrics]
closed_at: 2026-05-30
---

# `pr_metrics` の client 側集約 + OTLP Metrics gauge 送信

Created: 2026-05-30

## 概要

[0040] の child issue (2)。log-metric backend は record 間 join をしないため、現行 SQLite VIEW（`pr_metrics`、cross-event join + latest-wins + sum）を raw events だけで再現できない。よって **client がローカル VIEW を評価し、PR 単位（`pr_url` / `coding_agent` / `user_id`）の pre-aggregated 値を OTLP Metrics gauge（last-value）として送る**。

## 根拠

設計判断は [0040] 本文（A、`pr_metrics` 集約の節）に集約されている。要点:

- 当初の pre-aggregated 却下の前提（「集約はユーザ側で組める」）が join 不可で崩れたための翻意
- **gauge は冪等 upsert ではない**: 同一 timestamp & dimensions の点のみ last-write-wins。新 timestamp 再送は series 内の別点。range 集計で `last` を取る前提に依存し、naive な SUM は二重計上
- `session_id` を tag に出さないので cardinality は PR 数止まり
- gauge tag は VIEW projection に等しく（`pr_url` / `coding_agent` / `user_id` / `task_type` / `model`）、`repo` / `pr_state` / `branch` を載せたいなら **VIEW の projection 拡張が前提**

## 対応方針

1. [0040] の spike（gauge temporality / timestamp 設計）を実機検証し、送信 timestamp の決め方を確定（PR 最終更新時刻 vs 送信時刻、再計算値の過去レンジ扱い、Datadog の delta 要件との関係）
2. VIEW projection の拡張要否を判断（`repo` / `model` 等を gauge tag に載せるか）。必要なら `internal/syncdb/schema/schema.sql` を更新
3. client にローカル VIEW を評価して gauge LogRecord（Metrics signal）を組み立てる経路を追加。target 配列（[0041]）の representation = `pr_metrics_gauge` として既存 cursor 機構に乗せる
4. `session_count` / `tokens_per_session` / `tokens_per_tool_use` 等の ratio もこの行に含める（backend 側 formula で出せるようにする）

## 触らない

- raw events（Logs）の export target 拡張は [0041]
- backend 側の dashboard / monitor 構築（ユーザ責務）

Completed: 2026-05-30

## 解決方法

着手前の必須 spike を確定し、[0042] の target 配列・per-target cursor 機構の上に gauge representation を実装した。

### spike の結論

- **timestamp**: gauge 各点は **送信時刻（flush 実行時刻）** でスタンプする。`pr_metrics` は PR 単位の累積スナップショットで、flush ごとに最新値を sample する位置づけ。PR 最終更新時刻だと「PR メタは不変だがトークン累積だけ増えた」ケースで同一 timestamp 上書きが起き進化を捕捉できないため不採用。送信時刻なら各 flush が series 内の新しい点になり、backend の `last` time-aggregation が常に最新スナップショットを返す
- **delta temporality**: gauge は temporality フィールドを持たない（OTLP の delta/cumulative 要件は sum/histogram のみ、Datadog の delta 要件も gauge 対象外）。よって設定不要
- **二重計上回避**: 1 flush 内で PR（`pr_url`/`coding_agent`/`user_id`）ごとに 1 点だけ送り dimension set を重複させない → backend で `last` が一意。naive SUM 禁止を spec に明記

### 実装

- **VIEW 評価（`internal/serverclient/metrics.go` 新規）**: ローカル `pr_metrics` VIEW を `WHERE coding_agent = ?` で評価し（VIEW が merged/non-subagent/non-ghost/repo フィルタと GROUP BY を内包）、各行を `agent_pr_*`（`docs/metrics.md` カタログ名）の OTLP Metrics gauge data point に変換。base measure + ratio（`session_count`/`tokens_per_session`/`tokens_per_tool_use`/`per_million_tokens`）を同梱。NULL ratio は点を送らない。tag は VIEW projection（`pr_url`/`coding_agent`/`user_id`/`task_type`/`model`）のみで `session_id` は出さない
- **metrics cursor（`internal/backfill/state.go`）**: state に `metrics_cursors: {target_id: max_local_sequence}` map を追加（`flush_cursors` とは別）。gauge は VIEW 全再評価＋全 PR 再送なので、cursor は「最後に gauge flush した時点の max(local_sequence)」を記録し、新規イベントが無い flush は skip（冗長な同値点を送らない）。送信成功時に現在の max まで前進、transport 失敗時は据え置き
- **encoding（`internal/serverclient/proto.go`）**: `marshalOTLPMetricsProtobuf` を追加し、JSON 構造体 → `metricspb.MetricsData`（Gauge / NumberDataPoint, AsInt/AsDouble）変換。logs と同じく proto types のみで full SDK 不採用
- **flush（`internal/serverclient/flush.go`）**: `runFlushForAgent` を logs/metrics 2 representation に分割。target を signal で partition（両方指定可、各 representation 別 cursor）。HTTP 送信は logs/metrics で `doPost` を共有し response decode（`rejectedLogRecords` / `rejectedDataPoints`）のみ分岐。`FlushTargetResult` に Metrics* フィールドを追加、`Summarize` に metrics 行と up-to-date 表示を追加。`NoConfig` は「logs も metrics も対象 target が無い」場合に修正
- **docs/spec.md**: 「`pr_metrics` gauge representation」節を追加（client 集約の理由・gauge 意味論・timestamp=送信時刻・`last` 前提・dimension・metric 一覧・`metrics_cursors`・JSON 例）。`signals`・endpoint モデル・flush フラグ・state 例・attribute 分類節を metrics 対応に同期。VIEW projection 拡張は本 issue では行わず後続送り

### 検証

`go test ./...` 全パス（gauge 送信の tag/値・float ratio・metrics-only cursor 独立・logs+metrics 両 cursor・up-to-date skip・transport 失敗 cursor 据え置き・protobuf Content-Type/round-trip）。`go vet` / `gofmt` clean、`make intent-lint` clean。CLI スモークで logs+metrics 両 representation の dry-run 二段出力を確認。

### 採用しなかった代替

- **PR 最終更新時刻で timestamp を打つ**: 累積値が増えてもメタ不変だと同一 timestamp 上書きになり進化を取りこぼす。送信時刻スタンプに決定
- **ratio を送らず base measure のみ**: backend formula 非対応でも効率指標が出るよう ratio も同梱（formula 対応 backend は base から再計算可）
- **VIEW projection を `repo`/`pr_state`/`branch` に拡張**: 合否条件（PR 単位 gauge が送れて backend で last 集計できる）には不要なため最小限に留め後続 issue 送り
- **gauge も logs の `flush_cursors` を流用**: representation で cursor 意味が異なる（差分送信 vs 全再送）ため `metrics_cursors` を分離

---
decision_type: spec
affected_paths:
  - docs/spec.md
  - docs/design.md
  - cmd/agent-telemetry/
  - cmd/agent-telemetry-server/
  - internal/syncdb/
  - internal/sessionindex/
supersedes: [0009]
tags: [server, transport, event-sourcing, otel, append-only]
---

# metrics 転送を append-only イベント列 + OTel に移行する

Created: 2026-05-13

## 概要

サーバ送信を、現状の「`sessions` / `transcript_stats` 集計行を `INSERT OR REPLACE` で upsert」モデルから、**append-only なイベント列を OTLP/HTTP で転送する** モデルに作り直す。

`is_merged` などの状態は「最新の `agent.pr.observed` イベントが現在状態」として表現し、生イベントは追記のみで mutation しない。`sessions` / `transcript_stats` / `pr_metrics` などのテーブルは events から組み立てる **derived VIEW** に位置付け直し、ダッシュボード JSON は変更不要にする。

## 根拠

metrics は本来 append-only で、観測したものを時系列に並べるのが自然な形。にもかかわらず現状の設計（[0009] で確定）は mutable な集計行で状態を表現しており、そこから次の摩擦を生んでいる:

- **後追い更新（`is_merged` / `pr_url` / `review_comments` / `pr_title`）の SHA256 hash 追跡**: `state.json.pushed_session_versions` で行 hash を保持し、変化を検出して再送信する仕組みが必要になっている。本来「PR がマージされた」というイベントを 1 行追記すれば済む。
- **`INSERT OR REPLACE` 起点のスキーマ整合**: クライアントとサーバで同一 `schema_hash` を保つ必要があり、不一致時はサーバが受信拒否する設計になっている。新メトリクス追加のたびに「サーバ先行デプロイ → 全クライアント更新 → `--full` 再送」を全員に強いる。
- **OTel エコシステムから切れている**: `docs/metrics.md` で OpenMetrics カタログを整理しているのに、収集経路は独自 JSON。Prometheus / OpenTelemetry の標準ツール群（Collector / exporter / Tempo / Loki）に接続できない。
- **replay / reprocessing が不可**: 過去の生イベントから状態を再構築する手段がない（集計行に変換した時点で生イベントは捨てている）。新メトリクスの遡及反映が「クライアントを更新して `sync-db --recheck && push --full`」という重い操作になっている。

append-only に移すと:

- 後追い更新は「`agent.pr.observed` イベントを 1 行追記する」だけ。hash 追跡は不要
- `event_id` の冪等性で重複排除が完結する（client 側で一意 ID を採番、server は `INSERT OR IGNORE`）
- 観測軸の追加は新イベント名 / 新属性を増やすだけ。`events` table 自体の DDL は安定で、サーバ・クライアントのスキーマ同期要件が緩む
- OTel SDK / Collector / Prometheus exporter / Grafana Loki / Tempo に乗れる
- events を SoR にすれば、view の再定義だけで「同じ生データから別軸の集計」を作れる（reprocessing 可能）

## 問題

- 既存実装（[0028]-[0031] / [0033]-[0034] で実装済み）の差し替えが必要。client `push` 経路・server ingest 経路・`state.json` フィールド・`schema_hash` 整合の仕組みが対象
- 既存ローカル DB / サーバ DB の data migration が必要。集計行を擬似イベント列に展開する一回限りの migration を用意する
- ダッシュボード（Grafana JSON）はクエリを書き換えたくない。`sessions` / `transcript_stats` / `pr_metrics` / `session_concurrency_*` を **events 集約の VIEW** として再定義することで、クエリ側は無変更で済ませる
- OTel SDK / Collector を依存に取り込む判断。`go.opentelemetry.io/otel` 系の go module 追加が必要（cgo フリーで `modernc.org/sqlite` 方針に整合する）
- イベント粒度の選択。tool 使用などを「1 イベント = 1 tool call」にすると 1 セッションあたり数百イベントになる。trade-off と最初の実装の落とし所を決める必要がある（→ 対応方針）

## 対応方針

外部契約・内部設計の両方が変わるため、まず `docs/spec.md` と `docs/design.md` を新方針で書き換える（本 issue の PR）。実装は別 child issue に分解する。

### 目指す形

- **イベント schema**: 4 種類に集約する。「1 tool call = 1 event」のような細粒度は最初から取らず、Stop 完了時点の transcript 解析結果を **snapshot event** として 1 件出す方針にする。粒度が必要になったら追加イベント（例: `agent.tool.used`）を増やす
  - `agent.session.started` — SessionStart hook で emit。attrs: `agent_version`, `user_id`, `cwd`, `repo`, `branch`, `transcript`, `parent_session_id`, `started_at`
  - `agent.session.ended` — SessionEnd hook (Claude) または最終 Stop hook (Codex) で emit。attrs: `ended_at`, `end_reason`
  - `agent.transcript.scanned` — Stop hook 後 / `sync-db` が transcript を読み終わったタイミングで emit。attrs: `tool_use_total`, `mid_session_msgs`, `ask_user_question`, `input_tokens`, `output_tokens`, `cache_write_tokens`, `cache_read_tokens`, `reasoning_tokens`, `model`, `is_ghost`（**snapshot**: 同一セッションで複数回 emit されうる。view は latest-wins）
  - `agent.pr.observed` — Stop hook の early pin と backfill cycle で emit。attrs: `pr_url`, `pr_title`, `pr_state`, `is_merged`, `review_comments`, `changes_requested`, `pr_pinned`（**snapshot**: 同上）
- **transport**: OTLP/HTTP `POST /v1/logs`（OTel Logs / Events）。client は OTel SDK で emit、server は OTLP Logs receiver を持つ
- **server storage**: `events` テーブル（`event_id` PK, append-only, `INSERT OR IGNORE`）+ events から集約する `sessions` / `transcript_stats` / `pr_metrics` / `session_concurrency_*` VIEW（dashboard 互換性を維持）
- **client storage**: ローカル `~/.claude/agent-telemetry.db` も同じ二層構造（`events` table が SoR、`sessions` / `transcript_stats` は VIEW）に揃える
- **idempotency**: 各イベントに一意な `event_id` を付ける。サーバは `INSERT OR IGNORE` で重複排除し、migration の再実行対策は `source_row_hash` 等の別キーで担う
- **flush 経路**: 既存 `agent-telemetry push --since-last` を `agent-telemetry flush` に rename。`state.json` の `last_flushed_sequence` を見て差分 emit。Stop hook の hot path に network I/O を載せない方針は維持（cron / launchd / systemd timer 起動）
- **migration**: 一度限りの `agent-telemetry migrate-to-events` を提供。既存 `sessions` / `transcript_stats` 行を `agent.session.started` / `agent.session.ended` / `agent.transcript.scanned` / `agent.pr.observed` の擬似イベント列に展開する。サーバ側にも同等のサブコマンドを置く

### 消えるもの（[0009] からの差分）

- `pushed_session_versions` の SHA-256 hash 追跡 → `last_flushed_sequence` で代替
- `schema_hash` mismatch によるサーバ受信拒否 → 新メトリクスは新属性の追加で表現できるため。`events` table 自体の DDL は安定。VIEW の再定義はサーバ・クライアントで非同期に行ってよい
- `INSERT OR REPLACE` 起点の upsert dance → `INSERT OR IGNORE` の append-only ingest
- `agent-telemetry push --full` の運用要請（新メトリクス追加時の遡及反映）→ events は既に保管されているので、サーバ側 VIEW を更新すれば過去分も自動で集計される

### 残る制約

- snapshot 系イベント（`agent.transcript.scanned` / `agent.pr.observed`）は同一セッションで複数行が events に残る。VIEW は `MAX(occurred_at)` で latest-wins を取る。row 数の増加は session 数 × snapshot 回数で線形（毎セッション数イベント、年間でも数 MB 規模）
- snapshot を出し直すかどうかの判定は client 側責任（backfill が `is_merged` の変化を検出したら新 `pr.observed` を出す等）。client が判定をサボると更新が反映されない
- OTLP/HTTP の OTel Logs 表現は実装的に `body` か `attributes` のどちらに値を入れるか選ぶ。本 issue では「semantic conventions に合わせて attributes に flat に入れる」を初期方針とする

### 段階実装の見通し（本 PR の対象外、child issue で分解する）

1. server: OTLP Logs receiver + `events` table + 既存 view を events 集約で再現
2. client: hook から OTel SDK 経由で emit、ローカルも `events` table + VIEW に揃える
3. ローカル / サーバ data migration コマンド（`migrate-to-events`）
4. 旧 `push` 経路の deprecate（1 リリース併走 → 削除）
5. `docs/metrics.md` の OpenMetrics カタログを events 由来の表現に合わせる

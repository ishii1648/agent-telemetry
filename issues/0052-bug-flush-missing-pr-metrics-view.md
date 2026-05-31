---
decision_type: implementation
affected_paths:
  - internal/serverclient/flush.go
  - internal/serverclient/metrics.go
  - internal/syncdb/syncdb.go
tags: [flush, metrics, pr_metrics, schema, sqlite]
---

# flush の metrics パスが pr_metrics VIEW 不在で落ちる

Created: 2026-05-31

## 概要

`agent-telemetry flush` の metrics 送信パス（`signals` に `metrics` を含む target）が、ローカル DB に `pr_metrics` VIEW が無いと `load pr_metrics: SQL logic error: no such table: pr_metrics` で異常終了（exit 1）する。flush 自体は DB スキーマを保証しないため、VIEW が無い状態の DB では metrics を送れない。

## 根拠

- flush のパスは DB を `sql.Open` するだけで `ensureSchema` を呼ばない: `runFlush`（`cmd/agent-telemetry/main.go`）→ `serverclient.RunFlush`（`flush.go:174` の `sql.Open`）。
- `pr_metrics` VIEW を作るのは `internal/syncdb` の `ensureSchema`（`schema.sql` フル適用）だけで、これを呼ぶのは `sync-db` / `migrate-to-events` のみ。
- `metrics.go` の `LoadPRMetrics` は `FROM pr_metrics` を直接引く（VIEW 前提）。
- さらに `ensureSchema` は `schema_meta.schema_hash` が一致すると `schema.sql` 適用を **skip** するため（`syncdb.go:74-90`）、一度 VIEW が欠落した DB は `sync-db` を再実行しても hash 一致なら復活しない。`DELETE FROM schema_meta WHERE key='schema_hash'` してから sync-db する必要があった。

## 問題

- `flush` を単独で（直前に `sync-db` せずに）実行すると metrics パスが落ちる。logs パスは VIEW 非依存なので通るが、metrics だけ失敗して exit 1 になる。
- 旧バイナリ（flush 非対応の v0.0.6 等）が hook で sync-db を走らせている環境では、新旧スキーマの差で VIEW が壊れる競合も起きうる（検証中に実際に発生）。
- E2E 検証では VIEW 再生成（schema_hash クリア + sync-db）→ flush を毎回連続実行する必要があった。

## 対応方針

- flush の metrics パスが VIEW を要求するなら、`RunFlush` が DB オープン時に schema を保証する（サーバ側 `serverpipe.OpenDB` のように `ensureSchema` 相当を呼ぶ、または flush 前に sync-db フェーズを挟む）。serverclient → syncdb 依存の是非を含めて設計する。
- VIEW 不在を fatal error にせず、「sync-db を先に実行してください」と案内する穏当な失敗にすることも検討する。
- `ensureSchema` の hash 一致 skip が、VIEW 欠落 DB を自己修復できない点（DROP された aggregate VIEW を検出して再適用する経路が無い）も併せて見直す。

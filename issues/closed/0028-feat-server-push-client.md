---
decision_type: implementation
tags: [server, push, client]
closed_at: 2026-05-10
---

# クライアント側 push 実装 — agent-telemetry push でサーバへ集計値を送る

Created: 2026-05-10

## 概要

`agent-telemetry push --since-last` を新設し、`sync-db` 完了後の **集計値**（`sessions` 行 + `transcript_stats` 行）を独自 HTTP JSON でサーバへ送信する経路を実装する。`session-index.jsonl` の生行や transcript JSONL（会話本体）は送らない。仕様の外部契約は `docs/spec.md ## サーバ送信`、設計判断は `docs/design.md ## サーバ側集約パイプライン` を参照する。

## 根拠

[0009](0009-feat-server-side-metrics-pipeline.md) でサーバ送信の方針が確定した。クライアント側で必要な要素は (1) `[server]` 設定読み込み、(2) ローカル DB から差分行を抽出、(3) `pushed_session_versions` による差分検知、(4) HTTP 送信、(5) `state.json` の更新 の 5 つに整理できる。サーバ側の ingest 実装（[0029-feat-server-ingest.md](0029-feat-server-ingest.md)）と並列で進められるよう、独立 issue として分離する。

## 対応方針

- `internal/serverclient/` 新規パッケージ
  - `agent-telemetry.toml` の `[server]` セクション読み込み（既存 TOML パーサを拡張）
  - `pushed_session_versions` の読み書き（`agent-telemetry-state.json` 拡張、欠落時は空マップ）。キーは `<coding_agent>:<session_id>` 形式の文字列（複合 PK `(session_id, coding_agent)` を反映、Claude / Codex 間の UUID 衝突で hash が上書きされないようにする）
  - クライアント DB から `sessions` / `transcript_stats` の差分行を抽出（進行中セッションは除外: `ended_at` または `end_reason` が空のものは対象外）
  - 差分検知: 各セッションの `sessions` 行 + 対応する `transcript_stats` 行を JSON canonicalize → SHA-256 → `pushed_session_versions[<coding_agent>:<session_id>]` と比較
  - HTTP POST `/v1/metrics`（gzip optional、Bearer 認証、`schema_hash` 添付、50 MB を超えるバッチは自動分割）
  - レスポンスの `schema_mismatch: true` を検出した場合はユーザに binary 更新を促す
- `cmd/agent-telemetry/main.go` に `push` サブコマンドを追加
  - フラグ: `--since-last`（既定）/ `--full` / `--dry-run` / `--agent <claude|codex>`
- `user_id` は payload の `sessions` 行に既に含まれているため、別途 Resolver を呼ぶ必要はない
- `[server]` 設定欠落時は stderr に warning を出して exit code 0 で終了する（cron で叩いて壊れないこと）

## 受け入れ条件

- [x] `agent-telemetry push --dry-run` が対象セッション件数と payload サイズを表示する
- [x] `agent-telemetry push --since-last` でテストサーバに到達し、レスポンスが `{received_sessions, skipped_sessions, schema_mismatch}` の形で返る（`internal/serverclient/push_test.go` の httptest.Server で検証。0029 の実サーバ統合は別途）
- [x] 2 回連続で `push` を実行すると、2 回目は差分検知で送信ゼロになる
- [x] backfill が `is_merged` を更新した後の `push` で、該当セッションだけ再送される（`sessions` 行 hash の不一致を検出）
- [x] 進行中セッション（`ended_at` 空）は送信対象から除外される
- [x] `[server]` 設定欠落時は warning を出して exit code 0 で終了する
- [x] サーバが古いスキーマで `schema_mismatch: true` を返した場合、クライアントは exit code 非ゼロで終了し原因をログに記録する
- [x] `go test ./...` が通る

依存: [0029-feat-server-ingest.md](0029-feat-server-ingest.md)（サーバ側 ingest 実装、本番環境での E2E 検証に必要）

Completed: 2026-05-10

## 解決方法

`internal/serverclient/` を新規追加し、`config.go` / `payload.go` / `push.go` の 3 ファイルに分けた。`config.go` は `agent-telemetry.toml` の `[server]` セクションを読む（`userid` の最小 TOML パーサと同じ方針で `[server]` セクション内の `endpoint` / `token` だけ拾う）。`payload.go` は SQLite から `sessions` + `transcript_stats` を JOIN して読み出し、`(session, stats)` ペアを SHA-256 hash する（json.Marshal がフィールド順を保つため、構造体定義順をスキーマと揃えれば canonical form になる）。`push.go` は HTTP POST `/v1/metrics`（gzip 4KB 以上 / Bearer 認証 / 50 MB 自動分割 / `schema_mismatch` でエラー終了）と `state.json` 更新を担う。

`pushed_session_versions` は `backfill.State` に `map[string]string` として相乗りさせた。理由: 状態ファイルは 1 つで、push と backfill が別々に round-trip しても両方のフィールドを保持できる方が単純（push 側は cursor を、backfill 側は version 上書きしないので互いに干渉しない）。キーは `<coding_agent>:<session_id>` の複合形式で、Claude / Codex 間の UUID 衝突に対する保険として効く。

`cmd/agent-telemetry/main.go` に `push` サブコマンドを追加。`--since-last` / `--full` / `--dry-run` / `--agent` を受け、`serverclient.Run` を呼ぶ。`schema_mismatch` 時は exit code 2、`[server]` 未設定時は stderr warning + exit code 0、その他のエラーは exit code 1。

### 採用しなかった代替

- **`pushed_session_versions` を独立 state ファイル `agent-telemetry-push-state.json` に分離**: 既存 `backfill.SaveState` が JSON 全体を書き直すため、別ファイルにするか同一ファイル内に同居させる必要があった。同居の方が「state.json 1 ファイル」という外部契約を保てるので、構造体に相乗りさせた
- **`encoding/json` の代わりに自前 canonicalizer**: 集計値は struct 経由で marshal するので、Go の struct field 順保証 + map キーソートで十分 deterministic。RFC 8785 完全準拠は overkill
- **gzip を常時適用**: 数 KB 以下のペイロードでは gzip header overhead で逆に膨らむケースがある。4KB 閾値で切替

### 残タスク

- 0029 の実サーバ ingest と組み合わせた E2E はサーバ側完了後に `e2e/` 配下で検証する
- 0030 の運用ドキュメントで cron / launchd / systemd timer のサンプルを書く際、本実装の exit code 仕様（0 = ok / 1 = error / 2 = schema_mismatch）を参照すること

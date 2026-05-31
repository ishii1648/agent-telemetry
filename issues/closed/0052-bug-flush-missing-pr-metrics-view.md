---
decision_type: implementation
tags: [flush, metrics, pr_metrics, schema, sqlite]
closed_at: 2026-05-31
---

# flush の metrics パスが pr_metrics VIEW 不在で落ちる

Created: 2026-05-31
Completed: 2026-05-31

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

## 解決方法

スキーマ DDL を耐久層と派生層に分離し、**派生 VIEW を非破壊で再生成できる経路**を作って自己修復に充てた。

- `internal/syncdb/schema/schema.sql` を `schema.sql`（events / schema_meta テーブル + index）と `views.sql`（全 VIEW + トリガ）に分割。`schema.SQL = schema.sql + views.sql` を合成し、`genhash` は合成文字列に対してハッシュを計算する。
- `schema.EnsureViews` を追加（`views.sql` が宣言する全 VIEW / トリガの有無を `sqlite_master` で確認し、1 つでも欠落していれば `views.sql` のみ適用＝events を触らない非破壊修復）。期待リストは `views.sql` の `CREATE VIEW/TRIGGER` から正規表現で導出しドリフトを防ぐ。`pr_metrics` だけを見るとトリガ欠落時の `INSERT INTO sessions` 失敗や weekly VIEW 欠落を取りこぼすため全関係を検査する。events テーブル自体が無ければ `schema.ErrEventsTableMissing` を返す。
- `serverclient.RunFlush` は metrics target が設定されているときに限り DB オープン直後に `schema.EnsureViews` を呼ぶ。これで `flush` 単独でも VIEW を保証し、未 sync な DB では「`sync-db` を先に実行してください」と案内して終わる（cryptic な `no such table` を出さない）。logs-only flush は VIEW 非依存なのでスキップ。
- hash 一致 skip の自己修復: `syncdb.ensureSchema` / `serverpipe.EnsureSchema` の hash 一致パスを `schema.EnsureViews` 呼び出しに置き換え、DROP された VIEW を hash を触らず復旧できるようにした。**ハッシュ一致時に full `schema.SQL` は流さない**（events を消すため）のが要点。

回帰テスト: VIEW 欠落 DB に対する flush の自己修復（`TestFlush_MetricsHealsMissingPRMetricsView`）、未 sync DB の案内エラー（`TestFlush_MetricsUninitializedDBHint`）、hash 一致での sync-db 自己修復（`TestRunWithPaths_HealsMissingViewOnHashMatch`）。

## 採用しなかった代替

- **flush で full `schema.SQL` を適用**: `schema.SQL` 先頭の `DROP TABLE events` が client では `sync-db` で復元できるが server では受信済み events を消すため、修復は VIEW 限定に閉じた。
- **VIEW 不在を即 fatal にするだけの穏当失敗**: events がある DB では自己修復した方が UX が良い。fatal 案内は events テーブル自体が無い（修復不能）ケースに限定した。

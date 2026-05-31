---
decision_type: implementation
tags: [sqlite, concurrency, sync-db]
closed_at: 2026-05-08
---

# Stop hook 多重発火時の sync-db が SQLITE_BUSY で失敗する

Created: 2026-05-08

## 概要

サブエージェント終了や複数 tmux ペインの Claude セッション同時 idle で Stop hook が並行発火すると、各 hook が起動する `agent-telemetry sync-db` プロセスが同じ `~/.claude/agent-telemetry.db` に同時に書き込もうとし、後発のプロセスが `database is locked (5) (SQLITE_BUSY)` で即時失敗する。

実際に発生したエラー:

```
Stop hook error: Failed with non-blocking status code: hook error: sync-db: exit status 1
sync-db: agent=claude — 3080 セッションを処理中...
sync-db error: insert session claude/c0473c52-09f2-47d6-b137-955f060360ed: database is locked (5) (SQLITE_BUSY)
```

## 根拠

- `pr_metrics` を含むダッシュボードは sync-db の成功を前提にしている。Stop hook が失敗するとセッション追加分が DB に反映されず、最新の指標が古いまま表示される
- Stop hook の失敗はユーザに `Stop hook error: ...` として可視化されるため、UX を直接損なう
- 並行発火は計測ツール側ではコントロールできない（Claude Code 本体・サブエージェント・複数ペインの挙動に依存）。計測ツール側で衝突を吸収する責務を持つ必要がある
- セッション数の増加とともに sync-db の所要時間（ライタロック保持時間）も伸びるため、放置すると競合確率も増大する

## 問題

`internal/syncdb/syncdb.go` の `runWithSources` で SQLite を `sql.Open("sqlite", dbPath)` で開いており、**`busy_timeout` が未設定**。`modernc.org/sqlite` ドライバは busy_timeout 未設定だとライタロック競合時に SQLITE_BUSY を即時返す。

連鎖の経路:

1. 並行発火した Stop hook が各々 `agent-telemetry hook stop` → `exec.Command("agent-telemetry", "sync-db")` を起動 (`internal/hook/stop.go:41`)
2. 1 つの sync-db プロセスが 3000+ セッションを 1 トランザクションで `INSERT OR REPLACE` し、ライタロックを長時間保持
3. WAL モードは「読み込み×書き込み」は並行可だが「書き込み×書き込み」は依然として直列化される
4. busy_timeout 未設定のため、後続プロセスは即時 SQLITE_BUSY で落ちる

## 対応方針

`busy_timeout` を DSN の `_pragma` クエリで設定し、コネクションプールから払い出される全コネクションに 30 秒の待機を適用する。これで後続 sync-db プロセスはライタロック解放を待ってリトライするため、SQLITE_BUSY 即時失敗を防げる。

```go
db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)")
```

検討した代替案:

| 案 | 採否 |
|---|---|
| A: DSN `_pragma=busy_timeout(30000)` | 採用。driver 標準で全コネクションに適用される |
| B: `SetMaxOpenConns(1)` + `PRAGMA busy_timeout` | 却下。コネクションプール削減の副作用が読みにくい |
| C: ファイルロック (`flock`) で sync-db を排他化 | 検討余地あり。並行を許す代わりに排他化する選択肢。busy_timeout で済む範囲では不要 |
| D: 3000+ 件の `INSERT OR REPLACE` を差分同期に分割 | 中長期の改善案。ロック保持時間そのものを短縮する根本対処だが、本 issue のスコープ外 |

C / D は将来必要になれば別 issue で扱う。

Completed: 2026-05-08

## 解決方法

- `internal/syncdb/syncdb.go` の `runWithSources` で SQLite を開く DSN に `?_pragma=busy_timeout(30000)` を付与。`modernc.org/sqlite` ドライバはこの query string を新規コネクションごとに `PRAGMA busy_timeout=30000` として実行するため、`database/sql` のコネクションプールから払い出されるすべてのコネクションに 30 秒の待機が効く
- 回帰テスト `TestRunWithPaths_BusyTimeoutWaitsForWriter` を追加。書き込みロックを別コネクションで保持した状態で goroutine から `RunWithPaths` を起動し、ロック解放後に成功することを確認する。修正なしでは同じテストが `database is locked (5) (SQLITE_BUSY)` で FAIL することを再現確認済み
- 関連 PR: #34

`go test ./...` 全 pass。`busy_timeout` は SQLITE_BUSY を「即時失敗」から「最大 30 秒待機」に変えるだけで、根本的なロック保持時間そのものは短縮しない。3000+ 件の `INSERT OR REPLACE` の差分同期化（D 案）や flock による排他化（C 案）は将来必要になった時点で別 issue として扱う。

# agent-telemetry 設計

この文書は `docs/spec.md` の振る舞いをどう実現するかを記述する。
ユーザ視点の外部契約（CLI・データモデル・hook 仕様）は `docs/spec.md` を正とする。
過去の実装の経緯と廃止された設計は `issues/closed/` の retro issue に分離する。

---

## 全体構成

3 層構成。データ収集層は agent ごとにアダプタを分離する。

```
[データ収集層]   Go subcommand hooks (agent アダプタ)
                ├ ~/.claude/session-index.jsonl  (claude)
                └ ~/.codex/session-index.jsonl   (codex)
                ~/.{claude,codex}/agent-telemetry-state.json
       │
       ▼
[データ変換層]   agent-telemetry CLI
                backfill / sync-db (agent ごとに走査して 1 つの DB に集約)
       │
       ▼
[client SoR]    SQLite (~/.claude/agent-telemetry.db): events + 派生 VIEW
                ([0055] で client 側 SoR に降格。otel 経路の Grafana は直接読まない)
       │
       ▼
[可視化層]       flush → OTel Collector → Mimir/Loki → Grafana
                (otel 一本化。SQLite datasource 直結は legacy として残置)
```

| 層 | パッケージ | 主な責務 |
|---|---|---|
| Agent アダプタ | `internal/agent/`, `internal/agent/claude/`, `internal/agent/codex/` | データディレクトリ・hook 入力スキーマ・transcript 形式の差を吸収 |
| データ収集 | `internal/hook/` | SessionStart / SessionEnd / Stop / PostToolUse hook の処理。中間ファイル書き込み |
| データ変換 | `internal/sessionindex/`, `internal/backfill/`, `internal/transcript/`, `internal/syncdb/` | session-index と transcript を読み、SQLite を再構築 |
| 配布補助 | `internal/setup/`, `internal/doctor/` | セットアップ案内と検証（agent 別）。旧 `internal/install/` をリネーム |
| エントリポイント | `cmd/agent-telemetry/` | CLI dispatch（`--agent` パース） |

---

## Agent アダプタ層

### 抽象化のスコープ

agent 間で異なるのは次の 4 点のみ。それぞれ `internal/agent/` のインタフェースで吸収する。

| 観点 | Claude Code | Codex CLI |
|---|---|---|
| データディレクトリ | `~/.claude/` | `$CODEX_HOME` または `~/.codex/` |
| hook 入力スキーマ | `session_id` / `transcript_path` / `cwd` / `hook_event_name` | 同上 + `model` / `turn_id` / `source` 等。フィールド名は概ね共通 |
| SessionEnd 相当 | `SessionEnd` hook あり | `SessionEnd` 相当なし。Stop hook の最終発火で代替 |
| transcript 形式 | `~/.claude/projects/**/<session_id>.jsonl`。assistant message に `usage.*` トークン | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl[.zst]`。`event_msg.payload.type=="token_count"` で累積トークン |

session-index.jsonl のスキーマ・SQLite モデル・PR URL 検出ロジック・backfill の cursor 設計はすべて共通化する。agent 差分は **読み込み元と transcript パーサだけ** に閉じ込める。

### `internal/agent/` インタフェース

```go
type Agent interface {
    Name() string                       // "claude" or "codex"
    DataDir() string                    // ~/.claude or $CODEX_HOME
    SessionIndexPath() string           // <DataDir>/session-index.jsonl
    StatePath() string                  // <DataDir>/agent-telemetry-state.json
    ParseHookInput(io.Reader) (HookInput, error)
    ParseTranscript(path string) (TranscriptStats, error)
}
```

`Agent` の実装は `claude.New()` / `codex.New()` で取得し、CLI 側は `--agent` フラグまたは検出ロジックでインスタンスを選ぶ。

### Codex の SessionEnd 不在を Stop hook で代替

Codex には `SessionEnd` イベントが存在しない。代わりに `Stop` hook が応答完了ごとに発火するため、次の方針で `ended_at` を運用する:

1. Codex の `Stop` hook 発火ごとに `ended_at` を **常に上書き**（最後の Stop が事実上 SessionEnd 相当になる）
2. `end_reason` は `stop` で固定
3. Stop hook が呼ばれずプロセスが kill された場合は backfill フェーズで rollout JSONL の **最終 event タイムスタンプ** を読み、`ended_at` が空のセッションに反映する

(2) (3) によってプロセス強制終了でも終了時刻が失われない。session_concurrency_* VIEW の精度を保つために重要。

### `agent_version` の取得

`sessions.agent_version` は agent ごとに次の方法で取得する。取得失敗は空文字列として扱い、hook を失敗させない。

| agent | 一次ソース | フォールバック |
|---|---|---|
| Claude | SessionStart hook input の `version` フィールド（存在する場合） | 環境変数 `CLAUDE_CODE_VERSION` → 空文字列 |
| Codex | SessionStart hook input の `version` 系フィールド（要 Spike 確認） | rollout JSONL の最初のメタイベント内のバージョン情報 → 環境変数 → 空文字列 |

**hook 内で `--version` 等の外部コマンドは呼ばない**。hook の高速性を損なうため。hook input または環境変数で取れない場合は空のまま記録し、必要なら backfill フェーズで rollout / transcript の先頭メタから補完する。

`agent_version` は `pr_metrics` VIEW には集約しない。理由:

- 1 PR 内で複数セッション → 複数バージョンが混在しうる（バージョンアップを跨いで作業が続く場合）
- 平均化すると意味が壊れる
- バージョン跨ぎ比較は session レベルで集計するのが正しい（例: 「version A の全 session の token 効率」vs 「version B」）

ダッシュボードでは PR 別スコアカードの詳細展開で `sessions` を JOIN して表示し、テンプレート変数による絞り込みは将来必要になったタイミングで `sessions` ベースのパネルに対して個別追加する。

### `.jsonl.zst` 透過デコード

Codex は古い rollout JSONL を zstd 圧縮することがある。`internal/transcript/` に reader wrapper を追加し、拡張子で分岐する:

- `.jsonl` → `os.Open` のまま
- `.jsonl.zst` → `klauspost/compress/zstd` でストリーム解凍

依存追加: `github.com/klauspost/compress`。modernc.org/sqlite と同じく cgo フリーで Go プロジェクトの方針に整合する。

---

## データ収集層

### Go サブコマンド統一

hook はすべて `agent-telemetry hook <event> [--agent <claude|codex>]` の Go サブコマンドで実装する。Shell スクリプトは持たず、`settings.json` / `config.toml` には `{"type":"command","command":"agent-telemetry hook session-start --agent claude"}` のような形で登録する。

`--agent` を省略した場合は `claude` を既定値として扱い、既存の `~/.claude/settings.json` 登録（agent 引数なし）の後方互換を保つ。

理由:

- Shell 5 本に分散していた tool annotation・JSON パース・git 操作のロジックを `internal/hook/` の共通関数に集約できる
- バイナリへの埋め込み (`embed`) と展開 (`ExtractHooks`) が不要になり、配布が「PATH 上にバイナリがあること」だけで完結する
- awk による複雑なパース（旧 `todo-cleanup-check.sh` の 80 行、現在は `todo-cleanup` 系統ごと廃止済み）を Go テストでカバーできる
- Go バイナリの起動コスト（〜10 ms）は hook の発火間隔に対して無視できる

### Stop hook の非同期 worker 起動

`Stop` hook の同期パスは **ローカル書き込みのみ**（数 ms で return）で、`gh` を 1 回も呼ばない。GitHub に触れる処理（PR の pin / URL 補完 / マージ判定）と `sync-db` の SQLite 再構築はすべて detached worker に退避する。Stop は `agent-telemetry backfill --detach --agent <a> [--pin-session=<id>]` を fire-and-forget で spawn して即座に return し、worker の完了を待たない。

同期パスに残すのはローカル書き込みだけ:

- Codex は `SessionEnd` が無く Stop が de-facto SessionEnd なので、`ended_at` / `end_reason` を毎 Stop で上書きする（`internal/sessionindex.UpdateEnd`）
- それ以外（pin / backfill / sync-db）はすべて worker 側

spawn の要点:

- worker バイナリは `os.Executable()` で解決する（PATH 上の `agent-telemetry` に依存しない。絶対パスで hook 登録されていても動く）。stdio は `/dev/null` に向けて hook 自身の出力を汚さない
- `backfill --detach` 入口が `setsid` で実 worker (`backfill --worker`) を再 spawn するため、hook プロセスが終了しても worker は生き残る

#### hook プロセス自体の待ちを `async: true` で外す（Claude のみ）

detached worker は **worker の重い処理** を非ブロッキングにするが、Claude Code が **`agent-telemetry hook stop` プロセスの exit を待つ** こと自体は消せない。`Stop` は応答ターンごとに発火するため、毎ターン Go バイナリのコールドスタート分（数十 ms）がユーザの次入力をブロックする。

そこで Claude Code の `Stop` hook を `"async": true` で登録する（v2.1.0+ の Command hook フィールド。[hooks reference](https://code.claude.com/docs/en/hooks)）。Claude Code は hook をバックグラウンド起動して exit を待たないため、front-door の待ちが消える。detached worker と相補的: worker は重い処理を、`async` は hook プロセスの待ちを、それぞれ隠す。

- `async` 時は stdout / exit code が無視される。`RunStop` の error return は元々 non-fatal（telemetry は best-effort）なので実害なし。`asyncRewake`（exit 2 で Claude を再起床）は**使わない** — 非ブロッキングの逆になるため
- Codex には効かない。Codex の `Stop` は de-facto SessionEnd で `ended_at` を同期更新する別系統（hook 登録も `~/.codex/` 側）。`async` は Claude Code 固有フィールド
- `SessionEnd`（Claude のみ・セッション終了時 1 回）の `sync-db` は同期のまま残す。発火が 1 回で体感が小さく、終了フェーズで background hook が完了前に kill されるリスクがドキュメント未定義なため
- 登録判定（`isRegistered`）は `command` 文字列のみを見るため、`async` キー追加で誤検知しない。加えて doctor は `HookSpec.Async`（Claude の Stop のみ true）の hook について `settings.json` の `"async"` を別途読み、**registered だが async でない場合に hint を出す**（`✗`/failure ではなく `⚠` の情報行。telemetry は async 無しでも動くため `doctor` の exit code には影響しない）。Codex は async 概念が無いため対象外

詳細な意思決定は [issues/closed/0056-design-stop-hook-async-nonblocking.md](../issues/closed/0056-design-stop-hook-async-nonblocking.md)。

このモデルにより Claude / Codex のランタイムが収束する（Codex に `SessionEnd` が無い制約が worker トリガを Stop に一本化することで設計上消える）。詳細は [issues/closed/0039-bug-stop-hook-backfill-rate-limit.md](../issues/closed/0039-bug-stop-hook-backfill-rate-limit.md)。過去の fire-and-forget / launchd cron の経緯は [issues/closed/0020-design-backfill-evolution-to-stop-hook.md](../issues/closed/0020-design-backfill-evolution-to-stop-hook.md) を参照。

### PR の確定は worker で early binding

PR と session の紐づけは worker 内の `runPinBackfill` が `gh pr list --head <branch>` を 1 回叩いて確定する（Stop が `--pin-session=<id>` で対象セッションを渡す）。1 件取れた場合は `pr_urls` を `[<url>]` で置き換え、`pr_pinned: true` を立てる。pinned 状態に入ったセッションは以降 PostToolUse / `update` / `backfill` の URL 追記をすべて拒否する。late binding（後続 tick の Phase 1 経由）は **PR 未作成のままセッションが終わったケースのフォールバック** として残す。

PostToolUse の PR URL 抽出は `tool_input.command` の先頭が `gh pr create` のときだけ実行する。これは `gh pr create` 直後の stdout に含まれる URL を、Stop/worker の pin が失敗した場合の軽量フォールバックとして拾うための限定経路である。`gh pr view` / `gh pr list` / ユーザが貼った任意の URL など、PR 作成以外の Bash 出力は `pr_urls` に追記しない。

worker 起動が Stop に対して非同期になったため early binding はターン直後ではなく数 ms〜次の worker run のタイミングで効くが、ダッシュボードはターン中に見るものではないので即時性は不要（pin の同期実行は正当化されない）。

このルールが解決する誤接続:

- **PostToolUse 正規表現の汚染** — `gh pr view 999` や `gh pr list` の出力、ユーザが Bash で他人の PR URL を貼ったケースで `pr_urls` 末尾に無関係な PR が付き、`sync-db` が末尾を採用するため誤った PR に紐づく問題。pin 後はすべての append が no-op になり、pin 前 / pin 失敗時も PostToolUse が `gh pr create` 以外の出力を抽出しないため塞がる。
- **ブランチ再利用** — 同一ブランチで別 PR を使い回す運用で、新 PR の URL が古いセッションに付与される問題。pin の時点で `(repo, branch)` 解決を済ませているため、後から作られた別 PR の影響を受けない。

実装の要点:

- pin 経路は `internal/sessionindex.PinPR`（worker のバッチ集約経由で `applyPinPR`）に集約。`pr_urls` の追加 (`Update` / `UpdateByBranch`) は内部で `PRPinned == true` をスキップする。
- pin の lookup は Phase 1 と同じ `gh pr list --head <branch> --author @me --state all --limit 1` を `cwd` で実行する。よって pin した URL と Phase 1 が同 run で解決する URL は一致する。
- `cwd` 不在 / git リポジトリでない / branch 空 / `gh` がエラーの場合は best-effort で skip し、worker を落とさない。fallback として Phase 1 が次 run で再試行する。
- PostToolUse の fallback は `gh pr create` stdout だけを拾う。`gh` wrapper / alias / `hub pr create` / browser flow のような別経路は拾わず、必要なら `agent-telemetry update <session_id> <url>` で手動補完する。
- `Phase 2` の meta 取得は pinned セッションも対象に含める（`is_merged` / `review_comments` の更新は継続したい）。pin で抑止するのは **URL の追記だけ**。

### `session-index.jsonl` の追記モデル

`session-index.jsonl` は append-only に近い扱いで、SessionStart で新規 1 行を追加し、SessionEnd / backfill / `update` ではマッチする `session_id` の行を読み直して書き戻す。

#### 並行書き込みと flock

backfill を非同期 worker に出すと、複数プロセス（並列 worktree の Stop spawn / SessionStart の append / `update`）が同時に index を書き換えうる。`WriteAll` は tmp + `os.Rename` でアトミックだが、それだけでは **ロスト更新**（プロセス 1 が ReadAll → 2 が ReadAll → 1 が WriteAll → 2 が WriteAll で 1 の変更が消える）を防げない。そこで `<index>.lock` を使った per-file flock を導入し、待ちを最小化する方式を採る（`internal/sessionindex/lock.go`）:

- **ロックは「書き込み」だけ。`gh` は絶対にロック外**: worker は `gh` の結果をメモリ（`sessionindex.Batch`）に集約し、最後に flock を取って **最新 index を re-read → 差分を merge/apply → 1 回だけ WriteAll** する。ロック保持は数 ms（秒オーダーの `gh` とは無関係）。
- **worker の重い書き換えは try-lock-skip**（`ApplyBatch` = `TryWithLockedIndex`）。取れなければ待たず譲り、候補は state に残って次の保持者が処理する。「lock して待つ」ではなく「lock して譲る」ので並列実行待ちが出ない。
- **1 run = 1 WriteAll**: item ごとの ReadAll+WriteAll を廃し、Phase 1 / Phase 2 / pin / ended_at の全変更を Batch に集約してから一括適用する。
- **SessionStart の append も同じ flock を共有**（`AppendRawLine`）。`O_APPEND` は原子的だが rewrite の ReadAll→WriteAll 中に挟まると消えるため、同一 flock で整合させる。待ちは「進行中 rewrite 1 回（数 ms）」が最大。

global single-flight ロック（`~/.agent-telemetry/backfill.lock`）は別物で、全 agent 共有の `gh` バースト制御（後述「backfill のレート制御」）。per-file flock は書き込み整合性、global lock は `gh` 同時実行抑制と、スコープが異なる。

---

## データ変換層

### `sync-db` の incremental イベント追記戦略

`sync-db` は通常実行で `events` テーブルに `INSERT OR IGNORE` でイベントを追記するだけで、テーブル / VIEW の DROP & CREATE は行わない。スキーマ DDL は `internal/syncdb/schema/` 配下に **2 ファイルで** 集約する: `schema.sql` が耐久層（`events` / `schema_meta` テーブル + index、データを保持する破壊的 DDL）、`views.sql` が派生層（全 VIEW + `INSTEAD OF INSERT` トリガ、events から導出するため drop & create が冪等・非破壊）。`schema.SQL = schema.sql + views.sql` を `schema.go` で合成し、その合成文字列に対する SHA256 を `go:generate`（`genhash`）で `schema_hash.go` に埋め込む。起動時に埋め込みハッシュと DB の `schema_meta` テーブルに保存されたハッシュを比較する:

| 状態 | DDL 実行 | events への書き込み |
|---|---|---|
| ハッシュ一致 + 派生 VIEW/トリガ健在 | しない | `INSERT OR IGNORE` のみ |
| ハッシュ一致 + 派生 VIEW/トリガ欠落 | `views.sql` のみ再適用（events は触らない＝非破壊。`schema.EnsureViews`）| `INSERT OR IGNORE` のみ |
| ハッシュ不一致 / `schema_meta` 不在 | `schema.SQL`（= tables + views）を全実行（events テーブルを DROP & CREATE）| `INSERT OR IGNORE` 後に新ハッシュを `schema_meta` へ書き込む |

ハッシュ一致時でも `views.sql` が宣言する **全 VIEW / トリガ**（`pr_metrics` だけでなく `weekly_*` / `session_concurrency_*` や `sessions_insert_events` 等の `INSTEAD OF INSERT` トリガ）の有無を `sqlite_master` で確認し、1 つでも欠落していれば `views.sql` を再適用する自己修復を入れている（`schema.EnsureViews`。期待リストは `views.sql` の `CREATE VIEW/TRIGGER` から正規表現で導出し、DDL 追加時のドリフトを防ぐ）。`pr_metrics` だけを見ると、トリガ欠落時に後続の `INSERT INTO sessions` が失敗したり、weekly VIEW 欠落でダッシュボードが壊れたままになるケースを取りこぼす。これは旧設計の「ハッシュ一致なら全 DDL skip」が、DROP された派生関係を復旧できず `DELETE FROM schema_meta` の手作業を強いていた問題への対処（[0052]）。**ハッシュ一致時に full `schema.SQL` を流さない**のが要点で、`schema.SQL` 先頭の `DROP TABLE events` は client なら `sync-db --recheck` で再構築できるが server では受信済み events を消すため、修復は VIEW 限定に閉じる。

理由:

- ソース・オブ・レコードは `session-index.jsonl` と transcript JSONL。`events` table はそれらから組み立てた **構造化キャッシュ**（SoR ではなく derive 可能なため、最悪削除して `sync-db --recheck` で再生成できる）
- VIEW 定義の変更は events 再投入を伴わない（VIEW を DROP & CREATE するだけで済む）。events table の DDL が変わるケースだけが「重い」マイグレーション
- DB 破損時はファイルを消して `sync-db --recheck` で回復する
- VIEW を毎回再定義しないため、`sync-db` 実行中も Grafana のクエリは VIEW を見失わない

新メトリクスの追加は次の 3 通り:

- **既存イベントに属性を追加** — `agent.transcript.scanned` に新フィールドを増やす。events DDL は変更不要、VIEW 定義のみ更新
- **新イベント名を追加** — `agent.tool.used` のような細粒度イベントを増やす場合。events DDL は変更不要、新 VIEW を作るか既存 VIEW を `event_name = '...'` で JOIN
- **events table の DDL 変更** — 想定されるのは index 追加など。`schema_hash` 不一致でフル再構築

中間ファイルが SoR である構造は、hook の書き込みを軽量に保つためでもある。hook はセッション中に同期実行されるため、JSONL への追記だけに留める。events への追記と OTel emit は `sync-db` / `flush` に委譲する。

`schema.sql` / `views.sql` の編集忘れによるハッシュ未更新を防ぐため、CI（`.github/workflows/schema-hash.yml`）で `go generate ./... && git diff --exit-code` を実行する（`genhash` は両ファイルの合成に対してハッシュを計算する）。

`flush` の metrics パス（`signals` に `metrics` を含む target）は `pr_metrics` VIEW を client 側で評価するが、`flush` は source からの再構築を行わない。VIEW を作るのは `sync-db` だけなので、旧 binary 由来の DB や VIEW が DROP された DB に対して `flush` 単独実行すると `no such table: pr_metrics` で落ちていた（[0052]）。対処として `RunFlush` は **metrics target が設定されているときに限り** DB オープン直後に `schema.EnsureViews` を呼び、派生 VIEW を非破壊で保証する。events テーブル自体が無い（= 一度も `sync-db` していない）DB では VIEW を定義できないため `schema.ErrEventsTableMissing` を返し、「`sync-db` を先に実行してください」と案内する。logs-only flush は VIEW 非依存なので保証をスキップする（未 sync な DB では logs パスも同じ events 欠落で失敗するため、二重に案内しない）。

### `backfill` の cursor + 2 フェーズ設計

`agent-telemetry-state.json` に `last_backfill_offset`（JSONL の処理済み行数）と `last_meta_check`（Phase 2 の最終実行時刻）を保存する。

| フェーズ | 対象 | 実行条件 |
|---|---|---|
| Phase 1: URL 補完 | cursor 以降の新規エントリ + cursor 以下で `pr_urls` 空かつ `backfill_checked=0` のリトライ待ちエントリ | 毎回 |
| Phase 2: マージ判定 | `pr_urls` を持ち **未マージ**（`is_merged != true`）の PR の `is_merged` / `review_comments` / `changes_requested` の再チェック | `last_meta_check` から一定時間経過時のみ |

`--recheck` 指定時は cursor を無視してフルスキャンする。

cursor が古くても結果に影響はない。`backfill_checked` フラグが API 呼び出しの永続スキップを担うため、cursor は単なる効率化のヒントとして扱う。Stop hook 起動時にまだ PR が作られていなかったセッション（リトライ待ち）は cursor の進行とは独立して毎回再評価する — そうしないと PR が後から作られたときに永久に取りこぼす。

### backfill のレート制御（single-flight / cooldown / cap）

GitHub の secondary rate limit は総量でなく **バースト / 同時実行** で発火する。Stop が高頻度イベントなので、worker 1 起動あたりの `gh` 発行を小さく保ち、かつ起動自体を間引く:

- **global single-flight**（`~/.agent-telemetry/backfill.lock`、try-lock-skip）: 全 agent 共有の 1 個。取れなければ worker は即 exit。並列 worktree / Claude と Codex の worker が同時に `gh` を撃つのを防ぐ（secondary rate limit はアカウント共有なので agent 横断にする）。flock はプロセス死で自動解放されるので stale にならない。
- **worker cooldown**（`last_worker_run` から `WorkerCooldown`）: single-flight は同時実行制御であって頻度制御ではない。Stop が連続しても worker run 自体を cooldown で間引く。cooldown skip でも Stop 同期パスは spawn 後に待たない。
- **gh cap**（`DefaultGHCap`）: Phase 1 / Phase 2 双方に効く 1 起動あたりの上限。非定常（backlog 消化直後・並列 worktree 大量流入直後）でも 1 Stop の `gh` 呼び出しが上限を超えない。
  - **Phase 1**: PR が付いた可能性の高い順＝ **newest-first** で cap 件。あふれた分は次 run に回る。
  - **Phase 2**: per-URL の `last_meta_check`（state の `meta_url_checks`）で **oldest-checked-first** に cap 件。単純 cap で先頭 N 件だけ更新され続ける starvation を避ける。

### 移行 drain `backfill --gc`

deploy 後の既存 backlog（再現環境で 2390）は GC 適用前なので初回数 Stop が大バーストになる。これを cap 無しの一括パス `agent-telemetry backfill --gc` で 1 回 drain する。`--gc` は `gh` を呼ばず、`COALESCE(ended_at, timestamp)` から 24h 以上経過した PR-less・未 checked セッションを一括で `backfill_checked` にする（`ended_at` 空の Claude セッションも `timestamp` フォールバックで age out する。詳細は [issues/0035-bug-backfill-no-pr-infinite-retry.md](../issues/closed/0035-bug-backfill-no-pr-infinite-retry.md)）。`doctor` が backlog 件数を見て案内する。backlog のカウント（`countBacklog`）は backfill candidate と同じフィルタを使い、`is_default_branch` のセッションを除外する——第1層で構造除外されるデフォルトブランチは `--gc` でも markChecked されないため、数えると解消不能な `--gc` 案内になってしまう。

### PR タイトルの取得

backfill は `gh pr list` / `gh pr view` の `--json` 引数に `title` を含めて、`is_merged` / `review_comments` / `changes_requested` と同じ呼び出しで PR タイトルを取得する。追加の API 呼び出しは発生しない（同じレスポンスから別フィールドを抽出するだけ）。

取得した `title` は `sessionindex.UpdatePRMeta` を介して同一 `pr_url` を持つ全セッションの `pr_title` フィールドに転写する。`sync-db` は `sessions.pr_title` カラムへ単純コピーし、`pr_metrics` VIEW では `MAX(s.pr_title)` で集約する。

空文字列での上書きはしない。`gh` が title を返さなかった（タイトルが空 / API エラーで取得失敗）場合に既存の `pr_title` を消さないため、`UpdatePRMeta` は `prTitle == ""` のとき `pr_title` フィールドを書き換えずスキップする。

### `(repo, branch)` グルーピングと候補の絞り込み（第1層 admission control + 第2層 horizon）

backfill は `pr_urls` が空のセッションを `(repo, branch)` でグループ化し、`gh pr list` を 1 回だけ実行する。同一ブランチで複数セッションがあっても API 呼び出しは 1 回。「PR が未作成のまま放置されたブランチ」を毎 Stop で無限に再 probe しないため、候補は 2 段で絞る（[issues/0035-bug-backfill-no-pr-infinite-retry.md](../issues/closed/0035-bug-backfill-no-pr-infinite-retry.md)）。

**第1層 — 実デフォルトブランチの admission control**: repo の**実際のデフォルトブランチ上のセッションは構造的に PR を持たない**（デフォルトブランチから PR は作らない）ので、そもそも候補に入れず `gh pr list` を一度も呼ばない。判定は SessionStart の `extractGitInfo`（既に git を叩いている）で `git symbolic-ref refs/remotes/origin/HEAD` を使って repo ごとの実デフォルトブランチを動的に求め、現セッションの branch と一致するかを `is_default_branch` フラグとして session entry に焼き付ける（gh API は呼ばない）。origin/HEAD 未設定の repo に限り慣習的な `main` / `master` へフォールバックする。**branch 名 `main` / `master` のハードコード除外は採用しない**——`trunk` / `dev` / `develop` 等の命名多様性に対応するため、名前一致ではなく実デフォルトブランチの動的判定にする。`is_default_branch` を持つセッションは Phase 1 の候補収集でも pin（現セッションの `gh pr list`）でも除外され、`backfill_checked` ではなくこのフラグが永続スキップを担うので `--recheck` でも候補に入らない。

**第2層 — `COALESCE(ended_at, timestamp)` の 24h horizon**: 第1層を通過した候補（abandoned な feature ブランチ等）は、基準時刻から 24h 以上経過していれば `backfill_checked: true` をセットして以降スキップする（`shouldMarkChecked`）。基準時刻は `ended_at` 単独ではなく `COALESCE(ended_at, timestamp)`——Claude の ~15% は SessionEnd 不発で `ended_at` が恒久的に空になるため、空なら SessionStart で必ず入る `timestamp` にフォールバックして全セッションが必ず age out するようにする。24h 以内は markChecked せず次 run で再 probe する（完了直後の遅延 PR 作成を救済する窓）。

両者は排他でなく補完: 第1層が構造的 never-PR を 0 秒で弾き、第2層が時間経過した abandoned ブランチを retire する。第1層は新規セッション向けの入口フィルタなので、`is_default_branch` フラグを持たない過去のデフォルトブランチセッションは引き続き第2層 + `backfill --gc` で収束する（過去分を遡ってフラグ付けはしない）。`is_default_branch` は backfill が読む `session-index.jsonl` にのみ持たせ、ダッシュボード用途が無いため `sessions` テーブル（DB）へは伝播しない。

### 並列化

1 起動内では `(repo, branch)` グループの `gh pr list` 呼び出しを goroutine で 8 並列実行する。プロセス内の並列度であって、プロセス間のバースト制御は前述の global single-flight + cooldown + cap が担う。

- primary な認証済みレート制限（5,000 req/h）に対し総量は誤差。問題は secondary rate limit（バースト）であり、cap で 1 起動の件数を、single-flight で同時起動を抑える
- 書き込みは `gh` 完了後にメモリ集約した Batch を 1 回の WriteAll で反映する（前述「並行書き込みと flock」）

### `pr_urls` の採用ルール

`session-index.jsonl` の `pr_urls` は配列だが、`sync-db` がセッション → PR の単一 URL に変換する際は **配列の最後の 1 件** を採用する。

`update` / `backfill` が PR URL を追記する順序が結果に影響するため、辞書順ソートはしない。

通常の運用では worker の pin により `pr_urls` は要素 1 件で確定する（前述「PR の確定は worker で early binding」）。late binding（pin 失敗 → Phase 1 経由）でも要素 1 件になる。複数要素になるのは、`gh pr create` の出力に複数の GitHub PR URL が含まれる異常ケースに限られ、pin 後は `[<確定 URL>]` で置き換えられるため一過性で残らない。

### transcript パース

`internal/transcript/Parse()` が agent ごとのアダプタを呼び分けて 1 セッション分の `TranscriptStats` を返す。出力スキーマは agent 共通。

#### Claude (`internal/transcript/claude.go`)

- `tool_use_total`: assistant の tool_use エントリ数
- `mid_session_msgs`: 2 件目以降の `type:"user"` で `tool_result` のみで構成されないもの
- `ask_user_question`: `tool_use.name == "ask-user-question"` の件数
- `usage` 系トークン: assistant message の `usage.input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens` の合計
- `reasoning_tokens`: 0 固定
- `model`: 最後に観測した model
- `is_ghost`: `type:"user"` エントリが 0 件

#### Codex (`internal/transcript/codex.go`)

- `tool_use_total`: `event_msg.payload.type == "tool_call"` の件数（Bash / apply_patch / MCP tool 全て含む）
- `mid_session_msgs`: 2 件目以降の `event_msg.payload.type == "user_message"`（あるいは `UserPromptSubmit` 同等イベント）
- `ask_user_question`: 0 固定（Codex に AskUserQuestion 相当のツールが無いため。将来 PermissionRequest を流用するなら別途検討）
- token 系: `event_msg.payload.type == "token_count"` イベントの **最終累積値** を採用（input / output / cache_read / cache_write / reasoning）。途中 turn 単位の差分が必要な指標が無いため累積値で十分
- `reasoning_tokens`: `token_count.reasoning` の最終値
- `model`: rollout JSONL の最初のメタイベントから取得
- `is_ghost`: `event_msg.payload.type == "user_message"` が 0 件

`usage` / `token_count` いずれも欠落時は 0。Claude の古い transcript と Codex の新しい rollout を混ぜても sync-db が落ちない。

---

## ユーザ識別子

### 取得経路と優先順位

`session-index.jsonl` の `user_id` を埋める際に参照するソースの優先順位は次のとおり。**先に値が取れたものを採用** する。

| 優先 | ソース | 用途 |
|---|---|---|
| 1 | 環境変数 `AGENT_TELEMETRY_USER` | CI / コンテナでの決定的な上書き口 |
| 2 | `config.toml` の `user` キー（`~/.config/agent-telemetry/config.toml`、`XDG_CONFIG_HOME` 上書き対応、旧 `~/.claude/agent-telemetry.toml` を fallback） | 永続的な人間識別子。設定ファイル管理ツール等で複数マシンに同一値を配る前提 |
| 3 | `git config --global user.email` | フォールバック。`--global` のみ参照する |
| 4 | （取得失敗）`unknown` | hook を失敗させない |

`git config --local` を **意図的に見ない**。理由:

- リポジトリごとに別 email を設定している運用（OSS と業務でメールを分ける）で、同一人物が分裂するのを避ける
- hook の cwd が git リポジトリでないケース（`~/` で起動、temp dir で起動）で取得が揺れる
- マシン跨ぎで人物を束ねるという user attribution の本来目的と逆方向

`internal/userid/` で Resolver を実装。hook と sync-db から共通で呼び出す。`Resolve()` は (識別子, 取得元) を返し、doctor が「どのソースから来たか」を表示できるようにする。

### 形式とハッシュ化

`user_id` の形式は **任意の文字列**（メールアドレスでも pseudonym でも UUID でも可）。ハッシュ化は **しない**。理由:

- ハッシュ化は join 不可で、複数マシンからの集約に困る（束ねるためのキーとして使えない）
- 表示と保存を分離したいケースは、ユーザが TOML に pseudonym を書くだけで成立する
- 組織内利用での人間可読性を阻害したくない

PII 取り扱いをどうしても分離したい場合は、TOML の `user` キーに pseudonym を入れて運用する選択肢がある。サーバ側のアクセス制御は 0009 の AuthN/AuthZ 設計で扱う（0010 のスコープ外）。

### 欠損時の扱い

- ローカル運用では `unknown` で記録し、hook を失敗させない
- 異なる人物の `unknown` レコードが集約から区別できない問題は、サーバ送信時のゲート判定（0009）で対処する
- `pr_metrics` などの VIEW では `unknown` を集計から除外しない（ローカル単独運用で `user_id` が未設定でもダッシュボードが空にならないよう、`unknown` も 1 ユーザとして扱う）

### 既存 `session-index.jsonl` レコードへの埋め戻し

`sync-db` は読み込んだレコードに `user_id` フィールドが欠落していれば、`internal/userid.Resolve()` の現在値で埋め、JSONL に書き戻す。これで JSONL を SoR として一貫させられる。マイグレーションコマンドは追加しない（既存方針通りスキーマハッシュ不一致で再構築）。

注意: 過去にマシン A で記録されたセッションをマシン B の DB に取り込んだ場合、現在の `user_id` で埋まる。これは仕様割り切り（過去ログのマシン間移動はサポートしない）。

### `pr_metrics` VIEW の集約軸

GROUP BY に `user_id` を加えて (`pr_url`, `coding_agent`, `user_id`) で集約する。同一 PR を複数ユーザが触ったケース（pair coding / 引き継ぎ）で人物別に分離するため。単独利用時は 1 PR = 1 行のままで結果は変わらない。

`session_concurrency_*` VIEW は既存互換のため `user_id` を集約軸に追加しない。user 別の同時実行数が必要になったら別 VIEW として追加する。

---

## データモデル設計

### session_id の名前空間

session_id は agent ごとに発行される UUID であり、衝突確率は実用上無視できるが、保証はない。`sessions` テーブルおよび `transcript_stats` テーブルの PRIMARY KEY を (`session_id`, `coding_agent`) の複合キーにすることで、衝突時にも区別できるようにする。

`session_id` を `claude:<uuid>` のように prefix 化する案も検討したが、外部出力（ログ、Grafana 表示）で生 UUID を扱える方が運用上扱いやすいため複合 PK 方式を採用する。

### `is_subagent` 判定

Claude Code の SessionStart hook は Task サブエージェントでは発火しない（Spike で確認済み）ため、`parent_session_id` フィールドはほとんど空になる。代わりに transcript ファイル名のパス構造（`{session_id}/subagents/agent-{agent_id}.jsonl`）からも判定可能だが、現状は `parent_session_id` の有無を主指標としている。

Codex はサブエージェント概念を持たないため `parent_session_id` は常に空・`is_subagent = 0` となる。

### `is_ghost` 判定

Claude Code はファイル編集履歴のスナップショットとして UUID 名の JSONL を作る場合があり、これらは `type:"user"` を含まない。SessionStart hook がこれらを「セッション」として記録してしまう問題を吸収するため、transcript 内に `type:"user"` が 1 件もない場合は `is_ghost = 1` にして PR 集計から除外する。

### LEFT JOIN 膨張バグの回避

`pr_metrics` VIEW では `transcript_stats` を session と 1:1 で JOIN する。permission_events のような 1:N の補助テーブルを LEFT JOIN すると `tool_use_total` が N 倍に膨張する事例があったため、N:1 の集約が必要な補助テーブルは事前集計サブクエリで結合する。

現在のスキーマは permission_events を持たないため該当箇所はないが、将来 1:N の補助テーブルを追加する際はこの方針を維持する。

### `task_type` の自動抽出（集計軸からは廃止）

`branch` カラムを `^(feat|fix|docs|chore)/` でマッチして `sessions.task_type` を埋める。マッチしないブランチは空文字列。

ADR-024 で task_type を集計軸から廃止したため、`pr_metrics` の集約・ダッシュボード panel ではこのカラムを使わない。schema にカラムは残すが、用途は SQL 実行時の任意フィルタと、過去の集計を再現する場合の後方互換に限定する。

### `session_concurrency_*` VIEW

`sessions.timestamp` と `sessions.ended_at` の区間重なりから同時実行数を算出する。`ended_at` が空のセッションは現在時刻で打ち切る。subagent / ghost / 運用ノイズリポジトリを除外する。

**並列度の観察軸（`agent_concurrent_sessions_{avg,peak}`）は SQLite + ローカル分析でのみ参照可能**で、otel+grafana 一本化後のメイン dashboard には持ち込まない（[0054]）。`session_concurrency_daily` / `session_concurrency_weekly` / `session_intervals` の各 VIEW は client 側 SoR / ローカル分析の一部として残すが、Grafana 表示からは落とす。理由は「区間（interval）の重なり」が OTLP Metrics gauge の flush 時点スナップショットから原理的に再構成できないため（任意レンジ性・peak の非分解性。`max_over_time` で出せるのは「日次 peak の最大」であり、レンジ全体の真の peak とは意味が異なる）。pre-bucket gauge での近似は誤読を招くため明示的に abandon する。詳細は [issues/closed/0054-design-abandon-concurrency-metrics-otel.md](../issues/closed/0054-design-abandon-concurrency-metrics-otel.md) を参照。

---

## 可視化層

### otel 一本化と SQLite の client 側 SoR 降格（[0055]）

ローカル可視化は **otel+grafana（Mimir/Loki）に一本化** し、SQLite を Grafana datasource に直結する旧経路は legacy として残置するに留めた（[issues/closed/0055-design-local-otel-visualization-migration.md](../issues/closed/0055-design-local-otel-visualization-migration.md)）。位置づけは次の 2 層に分かれる:

- **client 側 SoR** = SQLite (`~/.claude/agent-telemetry.db`)。append-only な `events` テーブルと、そこから導出する集約 VIEW (`sessions` / `transcript_stats` / `pr_metrics` / `weekly_session_metrics` / `session_concurrency_*`) を保持する。`flush` はこの SoR を読んで OTLP/HTTP（logs + gauge metrics）に projection する。VIEW も DDL も削除しない（[0054] 既定）。
- **可視化** = `flush` → OTel Collector → Mimir（gauge）/ Loki（raw events）→ Grafana。`make oss-up` で 1 コマンド起動でき、ローカル第一級の可視化選択肢（[0055] ①）。メイン dashboard は SQLite を読まず Mimir/Loki だけを読む。

**なぜ降格するか**: 中央サーバ・複数マシン集約・外部 backend（Datadog 等）はいずれも otel（OTLP）が共通言語であり、ローカルだけ SQLite datasource を主経路に残すと「ローカルと集約で dashboard・クエリ言語・gauge 意味論が二重化」する。SQLite を SoR に閉じ、可視化を otel に寄せることでローカルと集約のランタイムが収束する（[0053] / [0054] の一本化方針のローカル分が [0055]）。SQLite を完全削除しない理由は、client 側の cross-event join（`pr_metrics` 等）が外部 backend の下流で再現不能なため SoR としては不可欠だから（責務分担と cross-event join のロックインは [0040] および本節「外部 backend 経路の責務分担」を参照）。

**legacy SQLite datasource 経路の残置**: frser-sqlite-datasource で `grafana/dashboards/agent-telemetry.json` を直接読む構成（`make grafana-up` / site の setup/local「方法 A/B/C」）は、export を設定せず SQLite 単独で任意日付範囲の SQL 集計を見たい低レベル用途のために残す。otel 経路と機能が重複するが、撤去はしない（後方互換・オフライン分析の余地）。E2E スクショは SQLite dashboard を `make grafana-screenshot`、OSS otel dashboard を `make oss-screenshot`（[0055] ⑤）で別々にカバーする。

### SQLite + Grafana の選定（legacy datasource 経路の背景）

> 以下は SQLite を Grafana datasource に直結する **legacy 経路**の選定経緯。otel 一本化後のメイン可視化は上節のとおり Mimir/Loki を読むが、残置する SQLite datasource 経路の設計判断として記録を残す。

Prometheus + Grafana ではなく SQLite + Grafana を採用していた。理由は「任意の日付範囲で PR 別に集計する」という用途が SQL の典型ユースケースであり、Prometheus の「現在状態のスクレイプ」モデルとは合わないため。otel 一本化（[0055]）では、この「任意レンジ集計」を sparse gauge の `last_over_time([$__range])` と `week_start` ラベルで近似する（gauge range 集計の前提は `docs/metrics.md`、ティア整理は [0053]）。

ClickHouse / Loki も候補だが、個人利用規模では SQLite で十分。

### datasource uid の固定化

ダッシュボード JSON の datasource は `uid: agent-telemetry` で固定し、Grafana provisioning が解決しない `${DS_*}` テンプレート変数は使わない。`__inputs` セクションも持たない。

### 週別 time series のプロット位置

週別パネルの SQL では `time = strftime('%s', week_start, '+3 days', '+12 hours')` を返し、データポイントを **週の中央（木曜 12:00 UTC = JST 木曜 21:00）** にプロットする。`week_start` 自身（月曜 00:00 UTC = JST 月曜 09:00）をそのまま `time` に使うと、Grafana の time range（例: Last 7 days）の `__from` に対して JST 月曜の朝が境界より前に位置し、X 軸範囲外で描画されない週が出てしまう。中央プロットなら通常の time range で確実に範囲内に入り、また「週の代表値」としても直感的。

合わせて WHERE 句は `week_start BETWEEN date('${__from:date:iso}', '-7 days') AND date('${__to:date:iso}')` と `__from` を 7 日緩める。データポイント時刻が `__from` ～ `__to` に入る週でも `week_start` は最大 6 日前になりうるため。

### E2E スクリーンショット

`make grafana-screenshot` が Docker Compose で Grafana + Image Renderer を起動し、Render API でパネルごとに PNG を取得する。Playwright 等のブラウザ自動操作は採用しない（Go プロジェクトに異質な依存を持ち込まないため、また Image Renderer で十分なため）。

スクショ対象は 2 系統あり、dashboard ごとに別 make ターゲットでカバーする:

| dashboard | backend | make ターゲット | 出力する README/docs アセット |
|---|---|---|---|
| `grafana/dashboards/agent-telemetry.json` | SQLite（frser-sqlite-datasource、legacy 経路） | `make grafana-screenshot` | `.outputs/grafana-screenshots/` のパネル PNG（README ヒーローは出力しない） |
| `deploy/oss-observability/grafana/dashboards/agent-telemetry-oss.json` | Mimir/Loki（otel 一本化後のメイン） | `make oss-screenshot`（[0055] ⑤） | `docs/assets/dashboard-full.png`（README ヒーロー） |

otel 一本化（[0055]）で README ヒーローは **OSS otel dashboard** に切り替えた。`docs/assets/dashboard-full.png` の owner は `make oss-screenshot` で、SQLite 用 `make grafana-screenshot` はヒーローを上書きしない（過去に SQLite 版で上書きされる事故を防ぐため、`e2e/screenshot.sh` から hero コピーを外した）。SQLite dashboard（legacy）を変更したら `make grafana-screenshot`、OSS dashboard を変更したら `make oss-screenshot` を実行する（CLAUDE.md の必須作業）。

**OSS dashboard の決定的スクショ（`make oss-screenshot` / `e2e/oss-screenshot.sh`、[0055] ⑤）**: SQLite e2e と同じ fixture（`e2e/testdata/`、`make grafana-fixtures` が `time.Now()` 基準にシフト）を SoR にし、その値を **fixture→Mimir/Loki に決定的に投入**して OSS dashboard を render する。要点:

- **HOME サンドボックス flush**: CLI は DB / config / state パスを `os.UserHomeDir()` 由来で解決し `--db`/`--config` flag を持たないため、`HOME=<tmp>` を切って `<tmp>/.claude/agent-telemetry.db`（fixture コピー）と `<tmp>/.config/agent-telemetry/config.toml`（localhost collector を指す credential 不要 target）を置き、ビルドしたツリーの binary で `flush --full --agent claude` する。実 `~/.claude` / 実 config / 実 state を一切触らない。
- **clean stack で Loki 重複を排除**: gauge は `last_over_time` で最新値を拾うので再 flush しても値は不変だが、Loki は raw events を冪等排除しないため再 flush でログが二重化する。スクショ stack は `docker compose down -v`（volume 破棄）→ up → 単発 flush で毎回まっさらから投入し、2 回実行してもログ件数まで含めて同一データになるようにする。
- **ingest 待ち**: Collector の batch（5s）と Mimir/Loki の ingest を待つため、render 前に Mimir `/<...>/query?query=agent_pr_total_tokens` と Loki `query` が非空を返すまでポーリングする。`mimir.yaml` は `multitenancy_enabled: false` なので `X-Scope-OrgID` ヘッダは不要。
- **port / project 分離**: OSS compose の collector / Mimir / Loki / Grafana host port を env で可変にし、スクショ stack は `make oss-up`（既定 port）と別 project・別 port で並走できる。Image Renderer は本番 `make oss-up` に積まず、スクショ用 overlay（`docker-compose.screenshot.yaml`）でだけ足す。
- **決定性の定義**: SQLite e2e と同じく「**fixture が同じなら同じデータ・同じパネル値**」を保証する（絶対時刻は `now` 基準で毎回ずれるため pixel 一致は狙わない。time 軸パネルの x ラベルは run ごとに数分ずれる）。
- **単発 flush で「週別 merged PR 数」(Tier 2 timeseries, panel 20) は空になる**: gauge 点は flush 時刻（≒ `now`）に打たれるが、`[1w]` step の range クエリは epoch 整列（UTC 木曜起点）で最後の評価点が `now` の最大 7 日手前に落ちるため、`now` の点を窓に含められず "No data" になる。これは Tier 2 weekly trend が **複数回 flush を時間をかけて重ねる縦断利用**を前提にした近似（[0053]）である帰結で、単発 fixture flush では構造的に空（＝ run をまたいで安定的に空なので決定性は保たれる）。`week_start` ラベルで集計する Tier 3 の週次 barchart はこの問題が無く単発 flush でも全週が出る。README ヒーローでこのパネルが空なのは想定どおりで、tool の不具合ではない。

### otel+grafana dashboard の Tier 2（merged-PR gauge 集約でヘッドラインを復元）

SQLite を Grafana datasource から外し otel+grafana に一本化する移行（[0054]）で、メイン dashboard を一旦 Tier 1（PR 単位 gauge ＋ raw logs）のみに絞った。ヘッドライン stat / trend のうち、既存の `agent_pr_*` gauge だけで近似できるものを Tier 2 として OSS dashboard に復元する（新規 export 不要）。詳細・ティア整理は [issues/closed/0053-design-otel-dashboard-tier2-tier3-restore.md](../issues/closed/0053-design-otel-dashboard-tier2-tier3-restore.md)。

採用した式（`agent_pr_total_tokens` を代表に、いずれも sparse gauge なので `last_over_time([$__range])` で range 内最終値を拾う。naive な `sum`/instant では flush 直後 5 分しか出ない・二重計上になる、は metrics.md の gauge range 集計の前提と整合）:

- total tokens stat: `sum(max by (pr_url, coding_agent, user_id) (last_over_time(agent_pr_total_tokens[$__range])))`
- merged PRs stat: `count(group by (pr_url) (last_over_time(agent_pr_total_tokens[$__range])))`
- PR / 1M tokens stat: `count(group by (pr_url)(...)) * 1e6 / sum(max by (pr_url, coding_agent, user_id)(...))`
- 週別 merged PR 数 trend: `count by (coding_agent)(group by (pr_url, coding_agent)(last_over_time(...[$__interval])))` を 1w step の range クエリで評価

**volatile label の二重計上対策**: gauge の系列 identity は `(pr_url, coding_agent, user_id, task_type, model)` だが、client の集約 grain は `(pr_url, coding_agent, user_id)`（`task_type`/`model` は代表値ラベル）。range 内で代表 `model`/`task_type` が変わると同一 PR が複数系列として残り、素の `sum` は累積 `total_tokens` を水増しする。そのため sum 系（total tokens / PR per 1M の分母）は `max by (pr_url, coding_agent, user_id)`（= 累積値の最新を表す max）で安定キーに畳んでから合算する。`count`/`group by` 系（merged PRs・週別 trend）は元々畳むため影響を受けない。

**semantic drift を各パネル description に明示する**のが Tier 2 採用の条件。`agent_pr_*` は `pr_metrics`（`is_merged = 1` 限定）の集約なので、これらは「全 session の総量」ではなく「merged-PR に寄与した分」になり、非 PR・未マージ・放棄 session を取りこぼす。曖昧なまま出すと「全活動の総量」と誤読されるため、誤読を防ぐ description を必須とした（merged PRs stat だけは merged 限定の母集団そのものを数えるので近似ではなく一致）。週別 trend は SQLite 版の weekday-0（JST 月曜）起点カレンダー週と境界がずれる rolling/step バケットになる近似で、これも description に記す。

### otel+grafana dashboard の Tier 3（session-grain 週次 gauge で復元）

Tier 2 が `pr_metrics`（merged 限定・PR 単位）止まりなので、top-level session 数や session 単位の token 効率は出せない。これらは **session-grain の新規 export**でしか出せない（LogQL 集約は cross-event join を backend で再現できず不可、[0043]）。`weekly_session_metrics` VIEW を client 側で評価し、`agent_weekly_session_*` gauge として `pr_metrics` gauge と同じ `metrics` signal・同じ cursor・同じ `/v1/metrics` flush で送る（実装は `internal/serverclient/session_metrics.go`、representation 仕様は `docs/spec.md`「session-grain 週次 gauge representation」節）。

**週次 weekday-0 バケット問題**: SQLite の `weekly_session_metrics` は `date(timestamp, 'weekday 0', '-6 days')`（Asia/Tokyo 月曜起点週）でバケットする。PromQL `[1w]` 窓は epoch 整列（UTC 木曜起点）でこの暦週と境界がずれる。解決方針として **client 側（SQLite）で週次集約し、`week_start` を gauge label に載せる**を採った。dashboard は `week_start` label で group するだけで `[1w]` 窓を使わない。recording rule で backend 再集約する案は UTC 境界を再導入し、点を `week_start` に back-date する案は Mimir ingest の out-of-order 却下リスク（区間外サンプル拒否）があるため、いずれも却下した。

**集約安全な ratio**: ratio（tokens_per_session 等）を `coding_agent` 跨ぎで平均すると誤るため、dashboard は base measure（`agent_weekly_session_total_tokens` / `_count` / `_ask_user_question_total`）から `sum by (week_start)(分子) / sum by (week_start)(分母)` で再計算する。この分子確保のため `weekly_session_metrics` に `ask_user_question` 生サム列を追加した（[0043] の「base と ratio を両方送る」方針に揃え、gauge には便宜 ratio も同梱）。

---

## 配布補助

### `setup` コマンド

hook の自動登録はしない。ユーザが手動（または個人の設定管理ツール経由）で `~/.claude/settings.json` / `~/.codex/config.toml` を管理する前提に整合させるため。`setup [--agent <claude|codex>]` は agent 別の登録例を表示するだけで、書き込みは一切行わない。

過去 `install` / `install --uninstall-hooks` / `uninstall-hooks` サブコマンドで settings への書き込みを提供していたが、いずれも廃止した。`setup` が書き込まないのに `--uninstall-hooks` だけが破壊的という非対称、およびユーザ側の設定一元管理との二重管理を解消するため。残置 entry の手動削除手順は [site の setup/install](https://ishii1648.github.io/agent-telemetry/setup/install/) を参照。

### `doctor` コマンド

検出された agent ごとに binary の PATH 配置・データディレクトリの存在・hook 登録状況をチェックする。Claude は `~/.claude/settings.json` の JSON、Codex は `~/.codex/config.toml`（および `~/.codex/hooks.json`）の TOML/JSON を読む。

未登録の hook は warning として表示するが**自動修復はしない**。ユーザ側の設定一元管理の前提を壊さないため。

---

## サーバ側集約パイプライン

### 全体方針 — append-only events + OTLP/HTTP

ローカル `~/.claude/agent-telemetry.db` に閉じていたメトリクスを、複数マシン・複数ユーザのデータを統合できるサーバへ送る経路を、**append-only なイベント列の OTLP/HTTP 転送** として設計する。クライアントはローカルで蓄積した `events` テーブルから未送信行を抽出して OTel Logs として送り、サーバは `event_id` で冪等に追記する。`sessions` / `transcript_stats` / `pr_metrics` 等の集計はサーバ・クライアントの両方で **events からの VIEW** として組み立てる。

旧設計（`sessions` / `transcript_stats` 行を `POST /v1/metrics` で upsert）は [0009] / [0028]-[0031] で実装したが、`is_merged` / `pr_url` / `review_comments` 等の後追い更新を `pushed_session_versions` の SHA-256 hash 追跡で実現せざるを得ず、また `schema_hash` 不一致でサーバが受信拒否する設計が新メトリクス追加時の運用負荷を生んでいた。これらの摩擦はすべて「mutable な行で状態を表現していた」ことに起因しており、metrics 本来の append-only な性質に揃えれば消える ([0038]).

### 送信するもの — events のみ

クライアントは `~/.claude/agent-telemetry.db` の `events` から差分行を抽出して OTel Logs として送る。`session-index.jsonl` の生行・transcript JSONL（会話本体）・rollout JSONL は **送らない**。後追い更新（`is_merged` 等）は **新しい `agent.pr.observed` イベントを追記する** ことで表現し、過去 events 行の mutation はしない。サーバ側 VIEW が同一 `(session_id, coding_agent)` で `MAX(occurred_at)` の `agent.pr.observed` を採用するため、最新状態が自動的に反映される。

理由:

- 送信サイズは依然として小さい（1 セッションあたり events 数〜十数件 × 1 KB 程度。月数 MB）
- サーバ側に集計ロジックを持たない点は変わらない（OTLP Logs receiver + `INSERT OR IGNORE` のみ）
- transcript（会話本体）はサーバに渡らないため、プライバシー観点は旧設計と同じく議論不要
- 過去 events が server / client の両方に残るので、新メトリクスを増やす場合は VIEW の再定義だけで遡及反映できる（旧設計で必要だった「全クライアントを新 binary に更新 → `push --full`」運用は不要になる）

### 採用しなかった代替

- **旧設計（`sessions` 行 upsert + SHA-256 hash 追跡）の維持**: 後追い更新のたびに行 hash を計算 → 比較 → 再送、というロジックが本質的に「mutable state を transport で表現する」hack で、events 1 件追記で済む話を複雑化していた。新メトリクス追加時の `schema_mismatch` 全停止も運用負荷が大きい
- **OTLP Metrics signal の採用（server への内部転送では不採用 / 外部 backend 向けは [0040] で部分採用）**: tool_used / mid_session_msgs などを Counter として送る選択肢はあるが、(1) tool 1 回 = 1 event の細粒度は最初から取らず snapshot に集約したい、(2) Counter / Log の二系統に分けると server の ingest と VIEW 構築が複雑になる、ため **server への内部転送は Logs（events）に統一する**。ただし外部 observability backend（Datadog 等）は record 間 join をしないため、events を流すだけでは `pr_metrics`（`session_id` で cross-event join + latest-wins + sum している）を再現できない。このため [0040] では **client がローカル VIEW で集約した `pr_metrics` を OTLP Metrics gauge として外部 backend にのみ送る経路を併用する**決定をした（server 経路は Logs のまま据え置き）。「後で Metrics が必要になれば endpoint を追加する」という当初の含みの発動であり、実装は [0040] の child issue で行う
- **raw JSONL 転送 + サーバ側 transcript 解析**: 送信サイズ膨張・プライバシー観点・サーバ側のパーサ保守の 3 点が大きく、旧設計の議論で既に却下されている（[0009]）。append-only 化でもこの判断は変わらない
- **イベント table を持たず、行 mutation で済ます append-only シミュレーション**: 一見「集計行に `updated_at` を持たせて INSERT OR REPLACE すれば append-only っぽくなる」が、過去の状態を保てないので replay ができず、events table に置き換えるべき以上のものは生まれない

### 外部 backend 経路の責務分担（client / server / Collector / backend）

外部 observability backend（Datadog 等）への export 経路（[0040] / [0042] / [0043]）が加わり、集約・整形をどの主体が担うかが増えた。混線を避けるため責務を 4 主体に固定する。内部転送（client → server → Grafana/SQLite）は従来どおりで、本節は **外部 backend 向けに増えた経路**の責務を明文化する。

| 主体 | 担うこと | 担わないこと |
|---|---|---|
| **client**（`internal/serverclient/`） | 設定可能な OTLP export（export target 配列）と、**`pr_metrics` のローカル集約（gauge 化）**。1 差分スキャンから raw events（OTLP Logs）と pre-aggregated `pr_metrics`（OTLP Metrics gauge, PR 単位）の 2 projection を作り、宛先ごとの独立カーソルで送る | — |
| **server**（`internal/serverpipe/`） | OTLP Logs receiver + SQLite ingest（`INSERT OR IGNORE`）だけ。cross-event 集約は SQLite VIEW（`sessions` / `transcript_stats` / `pr_metrics`）が担う | **gauge は server を経由しない**（gauge は client から外部 backend / Collector に直行）。ingest に集約ロジックを持たない |
| **外部 backend**（Datadog 等） | client が送った gauge の格納・表示と、raw events の **event-level 集計**（素の token 推移・流量・カウント・イベント種別ごとの count） | **cross-event 集約は行わない**（log-metric backend は record 間 join をしないため、効率指標を backend formula で再現しない） |
| **Collector**（collector レシピ採用時のみ） | router として選んだ宛先への fanout と、raw events 側の **attribute 整形**（意味分類昇格の前処理: rename / resource 付与 / 高 cardinality drop）。direct ではこの整形を Datadog Logs Pipeline が同等に担う | **集約はしない**（stateless で cross-event join できない ＝ router であって aggregator ではない） |

**cross-event 集約が client + SQLite VIEW にロックインされる理由（join 不可）**: `pr_metrics`（`total_tokens` / `fresh_tokens` / `per_million_tokens` 等）は、token が `agent.transcript.scanned`、`pr_url` / `is_merged` が `agent.pr.observed`、`user_id` / `repo` が `agent.session.started` に分散した events を `session_id` で cross-event join し、latest-wins + sum して算出する。log-metric backend（Datadog Logs to Metrics 等）も Collector も **record 間 join をしない / できない**ため、normalized な raw events を流すだけでは backend 側で `pr_metrics` を組み立てられない。したがって cross-event 集約は **join を実行できる主体——ローカル SQLite VIEW を持つ client（外部 backend 向け）、または同 VIEW を持つ server（Grafana 向け）——にロックインされる**。外部 backend 向けには client がローカル VIEW を評価し、PR 単位の pre-aggregated 値を OTLP Metrics gauge（last-value）として送る（server 側集約に寄せる案は SQLite ingest を必須化し Datadog-only 個人と矛盾するため採らない）。gauge の temporality / timestamp の扱いは `docs/spec.md`「`pr_metrics` gauge representation」、設計判断の根拠は [0040] 本文を参照。

**direct と collector で差し替わるもの / 同じもの**: attribute 整形は両レシピで実施するが置き場所が違い、facet / measure 化は Collector processor では代替できず Datadog 側 index 設定が両レシピ共通で必須になる。

| 観点 | direct | collector | 差し替わるか |
|---|---|---|---|
| client の OTLP exporter | protobuf + `dd-api-key`（Datadog direct logs は protobuf 必須＝実装切替を伴う） | 既存 JSON exporter のまま（Collector が protobuf + `dd-api-key` 変換を担う） | **差し替わる** |
| credential 保持 | client（submit-only key を自マシンに置く） | Collector が 1 箇所で保持し client は backend credential を持たない | **差し替わる** |
| attribute 整形（rename / resource 付与 / drop） | Datadog Logs Pipeline remapper | Collector processor | **両方で実施**（置き場所のみ差し替わる） |
| facet / measure 化（検索 facet・集計 measure 昇格） | Datadog 側 index 設定 | Datadog 側 index 設定（Collector processor では代替不可） | **同じ**（recipe を問わず Datadog 側） |
| cross-event 集約（`pr_metrics` gauge） | client ローカル VIEW | client ローカル VIEW | **同じ**（client に固定） |

attribute 意味分類の表本体と 2 配布形式（direct 用 Datadog Logs Pipeline / collector 用 Collector processor サンプル）は `docs/spec.md`「OTLP export の attribute 意味分類」を正とし、本節には複製しない。各メトリクスが「client 集約 gauge をそのまま使う / raw events から backend formula で出す」のどちらで表現されるかは `docs/metrics.md` を参照する。

#### OSS observability ローカルスタック（`deploy/oss-observability/`）

collector レシピと同じ「Collector が backend へ push する」経路を、credential 不要の OSS（Mimir / Loki / Grafana）でローカル E2E 再現する検証用構成（[issues/0050](../issues/closed/0050-feat-oss-observability-local-compose.md)）。Prometheus の scrape（pull）型で検証すると Datadog exporter と失敗点（OTLP push の経路・`service.name` の昇格）がずれるため、あえて Collector push 経路に揃え、metrics は Mimir・logs は Loki に流す（`service.name` は Mimir で `job` ラベル・Loki で `service_name` ラベルに昇格する）。既存 SQLite ダッシュボードとは別系統の検証用ダッシュボードとして切り、single-process・filesystem storage の検証用最小構成にとどめることで、service / config / volume を Kubernetes manifest に移しやすい単位で分割している。

`make oss-up` / `oss-down` / `oss-flush`（`Makefile`）でこの compose を 1 コマンドで起動・停止し、token 不要の `config.toml.example` で hook の OTLP export（flush → Collector:4318）を流せる。これにより本スタックは検証専用ではなく、ローカル単独利用での **第一級の可視化選択肢**として使える（issue [0055](../issues/closed/0055-design-local-otel-visualization-migration.md) ① runtime cutover）。SQLite を client 側 SoR に降格し otel 経路の Grafana からは読まない明文化（[0055] ③、本書「## 可視化層」）、site の otel 前提書換（④）、OSS dashboard の決定的スクショ（⑤、`make oss-screenshot`）、README ヒーローの OSS 画像差し替え（⑥）まで完了し、SQLite + Grafana（`make grafana-up`）は legacy datasource 経路として残置する。

### プロトコル — OTLP/HTTP Logs

OTel SDK / Collector エコシステムに乗ることを優先し、独自 JSON ではなく **OTLP/HTTP JSON エンコード** を採用する。クライアントは `go.opentelemetry.io/otel/sdk/log` + `go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp` を使い、`/v1/logs` に POST する。サーバは自前の OTLP Logs receiver を `internal/serverpipe/` に持つ（OTel Collector を間に挟まないことで運用構成を単純化する）。

```
POST /v1/logs
Authorization: Bearer <api_key>
Content-Type: application/json
Content-Encoding: gzip   (optional)

(OTLP/HTTP Logs payload — resourceLogs[*].scopeLogs[*].logRecords[*])
```

各 logRecord は次の semantic に従う:

- `eventName` = `agent.session.started` / `agent.session.ended` / `agent.transcript.scanned` / `agent.pr.observed`
- `attributes` に `event_id` / `session_id` / `coding_agent` と、イベント固有属性を flat に格納
- `timeUnixNano` = `occurred_at` の epoch nano
- `body` は使わない（属性に統一）

サーバの ingest ハンドラは payload を分解して events table に `INSERT OR IGNORE`。受信形式が OTLP なので、将来 OTel Collector を経由する構成や、Loki / Tempo などへの fanout も自然に追加できる。

### 差分検知 — `state.json` の `last_flushed_sequence`

`agent-telemetry-state.json` に新フィールドを追加する:

```json
{
  "last_backfill_offset": 123,
  "last_meta_check": "...",
  "last_flushed_sequence": 12345
}
```

- クライアント側 `events.local_sequence` は挿入順に増える整数。クライアントは `events` から `local_sequence > last_flushed_sequence` の行を抽出して送る
- cursor 前進は **transport の成否** で決める（OTLP partialSuccess では再送しない）:
  - **HTTP 2xx**（`rejectedLogRecords` の値に関わらず）→ `last_flushed_sequence` を送信した最大 `local_sequence` に進める。`rejectedLogRecords > 0` は「サーバが永続的に拒否した不正レコード」（validation 失敗で `rejected.log` に記録済み）であり、OTLP 仕様上 retry しない前提。cursor を据え置くと同じ不正データを無限再送する無限ループになるため、件数を warning に出して前進する（`internal/serverclient/flush.go`）
  - **ネットワークエラー / 非2xx（5xx / 429 / 401）** → 配送失敗。`last_flushed_sequence` を **進めず** 次回 flush で同じ範囲を再送する。受理済み events は server 側 `INSERT OR IGNORE` で無害にスキップされるので全件再送は安全
- backfill が新しい `agent.pr.observed` イベントを events に追記すると、それは `last_flushed_sequence` より大きい `local_sequence` になり、次の flush で自動的に拾われる。SHA-256 hash 計算は不要
- 既存 state.json にこのフィールドが欠けていれば 0 扱い（次の flush で全 events を送る。サーバ側で冪等にスキップされる）
- 進行中セッション（`agent.session.ended` 未着）の events も送る。旧設計のように「最後の Stop 発火まで送信対象から除外」する制約は不要（events 単位で送れるため、進行中の状態もダッシュボードに反映できる）

### `event_id` の deterministic 採番（content hash）

`event_id` は **イベント内容から deterministic に導出する content hash**。`sessions` / `transcript_stats` への INSERT を events に変換する INSTEAD OF INSERT トリガ（`internal/syncdb/schema/schema.sql`）が、`at_event_id(event_name ‖ coding_agent ‖ session_id ‖ attributes_json)` を呼んで採番する（`at_event_id` は SHA-256 hex を返す deterministic scalar function。client / server 双方が import する `internal/syncdb/schema` の `funcs.go` で登録し、トリガ発火時に解決される。read 専用の Grafana / 生 sqlite3 はトリガを起こさないので関数を必要としない）。

トリガの INSERT は **`INSERT OR IGNORE`**。これにより:

- **同一内容の再導出は dedup される**: `sync-db` は session-index の全セッションを毎回 `INSERT OR REPLACE INTO sessions` で流すが、内容が変わらなければ `event_id` が一致して events に積まれない。これがないとランダム ID 採番では sync-db 実行のたびに 1 セッション ×4 イベントが無限増殖し、flush も重複再送する（[0038] のレビューで顕在化）
- **内容が変わった snapshot は新 row として積まれる**: `is_merged` の反転や再 scan で attributes が変われば hash が変わり、新しい `event_id` の row が追記される。VIEW は `MAX(local_sequence)` で latest-wins を取る
- 一発もの（`agent.session.started` / `ended`）も content hash なので、後追いで `user_id` 等が埋め戻された場合は新 row が積まれて latest-wins で反映される

`event_id` はサーバ側の冪等性キー（`INSERT OR IGNORE` の衝突キー）であり、**差分送信の cursor には使わない**。理由は content hash が時系列ソート不可で、`event_id > last_flushed_*` のような辞書順 cursor が破綻するため。cursor は採番方式と無関係に単調増加する `local_sequence` が担う（前節）。

`migrate-to-events` は実体としては `sync-db` と同じ経路（`syncdb.RunForAgents`）を一度走らせるだけで、同じ deterministic `event_id` で events を再生成する。content hash により再実行しても重複しないため、別途 `source_row_hash` のような重複防止キーは持たない。

### 送信タイミング — 独立コマンド `agent-telemetry flush --since-last`

Stop hook 経路には載せない方針は維持する。理由は旧設計と同じ:

- Stop hook は backfill / sync-db を detached worker に退避して同期パスを数 ms に保っている。送信を hook 経路に戻すと、worker 化で得た非ブロッキング性を再び失う
- 送信失敗が Stop hook の挙動に直接影響すると、デバッグ困難な fail mode を生む

ユーザは以下のいずれかで起動する:

- macOS launchd / Linux systemd timer / cron で定期実行（5〜30 分間隔）
- 手動実行（必要なときだけ）

[site の setup/server](https://ishii1648.github.io/agent-telemetry/setup/server/) に launchd plist と systemd timer のサンプルを置く。

### 認証 — 単一 API key

旧設計と同じ。サーバ起動時に `AGENT_TELEMETRY_SERVER_TOKEN` 環境変数で API key を渡し、クライアントは `~/.config/agent-telemetry/config.toml` の `[server] token` で同値を持つ。`user_id`（人物識別）は events の `agent.session.started` 属性に含まれる。**API key の認証**（信頼境界）と **`user_id` 経路**（集計軸）は責務を分ける。

### 信頼境界と identity のスコープ（[0058]）

現行の認証は **単一の共有 write token** で信頼境界を表現する。`internal/serverpipe/handler.go` は Bearer token の一致だけを検証し、payload 中の `event_id` / `session_id` / `coding_agent` / `eventName` / 全属性（`user_id` を含む）はクライアント申告をそのまま受理する。つまり **`user_id` は認可された identity ではなく単なる集計次元**であり、token を持つ任意のクライアントは他ユーザを騙る forged イベントを送って共有ダッシュボードを汚染したり、高カーディナリティ属性を注入して backend コストを押し上げられる。

これは **single-team / 単一 token 構成では意図的に受容するリスク** とする。前提は「token を共有する全員が相互に信頼できる小規模チームであり、各クライアントは自分の正しい `user_id` を申告する」こと。この前提が成り立つ範囲では、per-client identity・署名・per-user 認可境界を持たない単純さを優先する。

意図的に許容している脅威（single-team スコープ内では受容）:

- **`user_id` 偽装** — 侵害された / 悪意あるクライアントが他ユーザ名義のイベントを送れる。dashboard の per-user 集計は「自己申告ベース」であり監査証跡ではない
- **イベント詐称・汚染** — token 保持者は任意の `event_id` / `session_id` / イベント種別 / 属性を投入でき、集約 DB を任意に汚染できる
- **高カーディナリティ注入による backend コスト増** — 属性値が無制限のため、ラベル爆発で Mimir/Loki 等の backend コストを押し上げられる（security review §3 の Medium「cost spike」と関連）

> **isolation を約束するデプロイには現行構成は不十分**。テナント間 / ユーザ間の分離を SLA として謳う運用に広げる場合は、下記「将来オプション」のいずれかを満たすことを前提条件とする。本節は「どこまでが受容リスクで、どこからが追加要件か」の境界を固定するための記録であり、実装は本 issue のスコープ外（別 PR）。

将来オプション（per-user isolation を約束するデプロイ向け。実装可否は別 PR で判断）:

- **per-user token** — ユーザ / クライアントごとに異なる token を発行し、サーバ側で token → 許可 `user_id` を対応付ける。`user_id` の申告値が token に紐づく identity と不一致なら reject する（`user_id` を認可済み identity に昇格）
- **token scoping** — token に「書き込み可能な `user_id` / `coding_agent` の範囲」などのスコープを持たせ、範囲外の属性を持つイベントを reject する
- **監査ログ** — 受理 / reject したイベントを送信元 token・申告 `user_id`・受信時刻つきで追記し、後から偽装・汚染を追跡できるようにする（現状の `rejected.log` を identity 軸へ拡張）
- **backend カーディナリティ制御** — 属性キー / 値の allowlist・値長上限・per-token のラベル種別数上限を設け、高カーディナリティ注入で backend コストが暴走しないようにする

単一共有 token そのものの運用（rotation・配布・漏洩時対応）は別途 [0057] のスコープ。本節は identity と認可境界の方針に集中する。

### サーバ側 — OTLP Logs receiver + events table

新設するパッケージ:

```
cmd/
  agent-telemetry-server/main.go     # HTTP server エントリポイント
internal/
  serverpipe/                        # OTLP Logs receiver（受信 → INSERT OR IGNORE）
```

サーバ側のデータ配置:

```
<server_data_dir>/
  agent-telemetry.db                 # 全 user 集約 SQLite (events table + VIEW)
  rejected.log                       # 認証失敗 / 不正 payload のログ
```

ingest ハンドラの責務:

1. Bearer token を検証
2. OTLP Logs payload をパースして `eventName` / `attributes` / `timeUnixNano` を取り出す
3. events table に `INSERT OR IGNORE`（`event_id` PK で重複排除）
4. OTLP/HTTP の標準 `partialSuccess` レスポンスを返す（`rejectedLogRecords`、`errorMessage`）

`internal/syncdb/schema.sql`（events DDL + 派生 VIEW 定義）をサーバ binary にも埋め込み、起動時に `schema_meta` ハッシュ比較で DDL 再構築する仕組みはクライアントと同じ。`schema_hash` 不一致でクライアント送信を全停止させるロジックは持たない（events table の DDL は安定で、新メトリクスは新属性の追加で表現できるため）。

サーバの SQLite は Grafana datasource として読み込まれる。本番形態は k8s pod を想定し、Grafana の **設定資産**（`grafana/dashboards/agent-telemetry.json` と `grafana/provisioning/datasources/*.yaml`）はローカル `docker-compose.yaml` の volume mount と k8s ConfigMap mount の **両方から同じファイルを参照** する。これによりダッシュボード変更が両環境に同時反映され、二重メンテナンスを避ける。datasource の `uid: agent-telemetry` を踏襲し、VIEW の出力スキーマも旧設計と同じなのでクエリ JSON は無変更で動く。

### 新メトリクス追加の運用

旧設計の「サーバ先行デプロイ → 全クライアント binary 更新 → `push --full`」運用は不要になる。流れ:

1. 新属性 / 新イベントを emit するクライアント binary を順次配布（旧クライアントは無変更でも既存 events を送り続ける）
2. サーバ binary 側の VIEW 定義を更新（events の新属性を引いて新カラムを生やす）。サーバ起動時に `schema_meta` ハッシュ比較で `schema.sql` が再適用され VIEW が再定義される
3. 既存セッションについて新属性を遡及反映したい場合は、クライアントで `sync-db --recheck` を実行すると `agent.transcript.scanned` 等の snapshot イベントが新属性付きで再 emit される。次の `flush` で events に新行が追記され、VIEW の latest-wins で過去セッションも新カラムが埋まる

新メトリクスの大半は **events の JSON 属性追加だけで完結し schema.sql を触らない**ため、上記 1 のクライアント差し替えのみで済む（サーバ DB 無変更）。

> **既知の制約（要フォローアップ）**: 上記 2 のように `schema.sql`（VIEW / index）を変更すると `schema_hash` が変わり、サーバの `EnsureSchema`（`internal/serverpipe/db.go`）は不一致時に `schema.sql` を再適用する。その SQL は冒頭で `DROP TABLE events` してから作り直すため、**現状はサーバ集約 events が全消去される**。クライアントはローカル `events` を SoR として保持しているので全クライアントの `flush --full` で復旧可能（`INSERT OR IGNORE` で冪等）だが、無自覚にデプロイすると一時的にダッシュボードが空になる。本来は events table と VIEW/trigger の DDL を分離し、VIEW のみの変更では events を drop しない再定義に留めるべき。サーバ schema 進化を非破壊にする改修は今後の課題として本節に記録する（events table の DDL は安定という前提が成り立つうちは顕在化しないが、VIEW 追加時の運用手順は上記注記に従う）。

events table 本体の DDL に互換破壊変更を入れる場合のみ、新 endpoint（例: `/v2/logs`）を切るか、`migrate-to-events` のような明示的 migration を用意する運用とする。

### 衝突セッションの扱い

複数マシンの同一ユーザで session_id が衝突する確率は UUID として実用上ゼロ。物理コピーされた DB を別マシンから再 flush したケースだけが実際の衝突源になる。サーバは `event_id` PK で `INSERT OR IGNORE` する（同一 events は重複排除される）。本当に異なる events が同一 session_id で来た場合（衝突）は events に両方残り、VIEW 側の `MAX(occurred_at)` で最新が採用される。衝突セッションの可視化が必要になった時点で別 VIEW を追加する。

### VIEW の materialization

`sessions` / `transcript_stats` / `pr_metrics` / `session_concurrency_*` は最初は単純な SQL VIEW として定義する。events が増えてもダッシュボードのクエリレイテンシが許容範囲内なら materialization は不要。

events 数が大きくなって VIEW のオンザフライ集約が重くなった場合の選択肢:

- **trigger ベースのマテリアライズドテーブル**: events への INSERT 時に対応する `sessions_mv` / `transcript_stats_mv` 行を upsert する trigger を貼る。クエリは MV テーブルを見る
- **バッチリフレッシュ**: 定期的に `INSERT OR REPLACE INTO sessions_mv SELECT * FROM sessions` で MV を更新

最初はオンザフライ VIEW で進め、ベンチマークで顕在化したら materialization に切り替える。

### 旧 push 経路からの移行（完了）

[0028] / [0029] で実装した「`sessions` 行 / `transcript_stats` 行を `POST /v1/metrics` で送る」経路は本仕様で廃止済み。移行は次の手順で完了した:

1. クライアント・サーバとも一度だけ `agent-telemetry migrate-to-events` / `agent-telemetry-server migrate-to-events` を実行
   - 既存 `sessions` 行 → `agent.session.started` + `agent.session.ended` + `agent.pr.observed` の擬似イベント列に展開
   - 既存 `transcript_stats` 行 → `agent.transcript.scanned` の擬似イベントに展開
   - `event_id` は通常 event と同じ deterministic content hash（`at_event_id`）で振る。content hash が再実行時の重複防止を兼ねるため、別途 `source_row_hash` のようなキーは持たない（前節「`event_id` の deterministic 採番」）
   - `occurred_at` は対応するカラム（`timestamp` / `ended_at` 等）から推定。不明分は migration 実行時刻
   - 実体は `sync-db` と同じ経路（`syncdb.RunForAgents`）を一度走らせるだけ。既存 `sessions` / `transcript_stats` 行ではなく session-index / transcript を読み直して events を deterministic に再生成する
2. 既存 `sessions` / `transcript_stats` テーブルを VIEW に差し替える（`INSTEAD OF INSERT` トリガで `sync-db` の行書き込みを events へリダイレクト）
3. 旧 `agent-telemetry push` / 旧 `POST /v1/metrics` ハンドラを **v0.0.10 で 1 リリース併走させ、次リリース（[0038] 完了 PR）で削除した**。あわせて client の `serverclient.Run` / `Payload` / `SplitBatches` / `schema_hash` mismatch (`ErrSchemaMismatch`)、server の `ServeIngest` / `INSERT OR REPLACE` upsert / `findCollidingSessions` / `collisions.log`、`state.json` の `pushed_session_versions` を撤去。`sync-db` が書き込みに使う `INSTEAD OF INSERT` トリガと `schema_meta` の DDL バージョニングは現行経路で必要なため残す

### 配布形態 — Go binary + Docker image + k8s manifest

旧設計と同じ。`cmd/agent-telemetry-server/` を goreleaser で配布し、Docker image を `ghcr.io/ishii1648/agent-telemetry-server` で自動更新、k8s manifest を `deploy/k8s/` に Kustomize ベースで提供する。OTLP Logs receiver は単純な HTTP server なので、TLS 終端は Ingress / k8s Service / リバースプロキシ側に寄せる。

### 送信量とストレージ

| ケース | サイズ（events のみ、無圧縮） |
|---|---|
| 個人 1 日（10〜30 セッション × events 数〜十数件 × 1 KB） | 30〜400 KB |
| 個人 1 ヶ月 | 1〜12 MB |
| 5 人チーム 1 ヶ月 | 5〜60 MB |

events 単位なので旧設計（集計値のみ）より体積は数倍だが、ネットワーク・ストレージともに依然として極小。GC は実用上不要（数年分でも数 GB 規模）。

---

## 既知の制約

- `Stop` hook 同期パスは detached worker を spawn して即 return する（`gh` ゼロ）。pin / backfill / sync-db の遅延は worker 側に隠れ、ユーザの応答サイクルをブロックしない
- `pr_urls` の順序保持と「最後の 1 件」採用の整合性は早期 pin（`pr_pinned`）で根本対処済みだが、未 pin セッションへの追記順序は検証ケースが限定的なため、引き続きデータの偏りがあれば再評価する
- `session-index.jsonl` の書き戻しは tmp + `os.Rename` でアトミックかつ per-file flock で並行書き込みのロスト更新を防ぐ（前述「並行書き込みと flock」）。global single-flight は `gh` バーストのみを抑え、書き込み整合性とはスコープが別
- transcript のパス取得失敗時は当該セッションの `transcript_stats` が空になるが、`sessions` 行は残る
- Codex の SessionEnd 不在を Stop hook で代替するため、Stop hook を経由せずプロセスが kill された場合は最後の Stop 発火時刻が `ended_at` になる（rollout JSONL 最終 event での補正は backfill 経由）
- Codex の `ask_user_question` 相当指標が無いため、agent を跨いだ「仕様不明瞭さ」比較はできない
- サーバ送信を有効化する場合、`agent-telemetry flush --since-last` の定期起動を cron / launchd / systemd timer で自前運用する必要がある（Stop hook hot path に乗せないことの代償）
- backfill が後追い更新を検出した時点で新しい `agent.pr.observed` イベントを events に追記する責務がクライアント側にある。backfill が動かないと最新状態がサーバへ反映されない
- events のオンザフライ VIEW 集約は events 数が大きくなるとクエリレイテンシに効く。materialization 切替の閾値は実測で決める（最初は VIEW のまま運用）
- サーバ認証は単一 API key。複数ユーザでの read/write 権限分離（user 別 RLS、OIDC 等）は将来課題
- サーバは単一共有 token のみで認証し per-client identity を持たない。`user_id` は自己申告の集計次元であり、token 保持者は他ユーザ偽装・イベント汚染・高カーディナリティ注入が可能。single-team 構成では意図的な受容リスクとし、isolation を約束するデプロイ向けの追加要件（per-user token / token scoping / 監査ログ / backend カーディナリティ制御）は「信頼境界と identity のスコープ（[0058]）」に列挙した（実装は別 PR）

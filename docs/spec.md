# agent-telemetry 仕様

この文書は agent-telemetry の外部契約を記述する。
実装方法や設計判断は `docs/design.md`、過去の意思決定の経緯は `issues/closed/` の retro issue に分離する。
セットアップ手順と日常運用は site の [setup/install](https://ishii1648.github.io/agent-telemetry/setup/install/) / [setup/usage](https://ishii1648.github.io/agent-telemetry/setup/usage/) を参照する（source: `site/content/setup/`）。

---

## 概要

agent-telemetry は **Claude Code および Codex CLI** の **PR 単位のトークン消費効率** を計測するデータ収集ツールである。
各エージェントの hook が記録したセッションイベントとトランスクリプトを SQLite に変換し、収集したメトリクスを SQL から参照可能にする。可視化はユーザの任意とし、リポジトリ同梱の Grafana ダッシュボードはあくまで参考実装である。

データフロー:

```
Claude Code hooks → ~/.claude/session-index.jsonl + transcript JSONL ┐
                                                                     ├→ agent-telemetry backfill / sync-db
Codex CLI hooks   → ~/.codex/session-index.jsonl  + rollout JSONL    ┘
                                                                     → ~/.claude/agent-telemetry.db (SQLite)
```

DB は単一の `~/.claude/agent-telemetry.db` に集約する。後方互換のためファイル位置は変更せず、`sessions.coding_agent` カラムで `claude` / `codex` を区別する。

---

## hook の登録と役割

hook は `agent-telemetry hook <event> --agent <claude|codex>` のサブコマンド形式で呼び出す。`agent-telemetry` バイナリが PATH 上にある必要がある。登録はユーザが手動で行い（個人の設定管理ツール経由でも可）、`agent-telemetry setup` は登録例を表示するだけで自動登録はしない。

`--agent` を省略した場合は `claude` を既定値とする（既存登録の後方互換）。

### Claude Code

`~/.claude/settings.json` に登録する。

| hook イベント | サブコマンド | 役割 |
|---|---|---|
| `SessionStart` | `agent-telemetry hook session-start --agent claude` | セッション開始メタデータを `~/.claude/session-index.jsonl` に追記 |
| `SessionEnd` | `agent-telemetry hook session-end --agent claude` | 終了時刻と終了理由を `~/.claude/session-index.jsonl` に追記し、SQLite を同期 |
| `Stop` | `agent-telemetry hook stop --agent claude` | 応答完了時に `backfill --detach` を spawn して即 return（非同期）。PR の pin / backfill / sync-db は detached worker 側で実行 |

### Codex CLI

`~/.codex/config.toml` に `[features] codex_hooks = true` を設定したうえで `[[hooks.<Event>]]` を追加するか、`~/.codex/hooks.json` を配置する。Codex には `SessionEnd` イベントが存在しないため、終了時刻は `Stop` hook で逐次更新する（最後の `Stop` 発火が SessionEnd 相当となる）。

| hook イベント | サブコマンド | 役割 |
|---|---|---|
| `SessionStart` (`startup\|resume`) | `agent-telemetry hook session-start --agent codex` | セッション開始メタデータを `~/.codex/session-index.jsonl` に追記 |
| `Stop` | `agent-telemetry hook stop --agent codex` | 応答完了時に `ended_at` を同期更新し（Codex の de-facto SessionEnd）、`backfill --detach` を spawn して即 return（非同期）。PR の pin / backfill / sync-db は detached worker 側 |
| `PostToolUse` | `agent-telemetry hook post-tool-use --agent codex` | `tool_input.command` が `gh pr create` のときだけ `tool_response` から PR URL を抽出し `pr_urls` に追記（`pr_pinned: true` のセッションでは無視される） |

`Stop` hook の同期パスはローカル書き込み（Codex の `ended_at` のみ）と worker spawn だけで、`gh` を 1 回も呼ばず数 ms で return する。`gh` を伴う処理は detached worker に退避し、worker 側で global single-flight（同時実行抑制）・cooldown（頻度抑制）・gh cap（1 起動の件数上限）でレート制御する。両 agent とも worker トリガは Stop のみで、hook 登録構成は従来から変更しない。詳細は `docs/design.md ## Stop hook の非同期 worker 起動`。

---

## CLI

```
agent-telemetry setup [--agent <claude|codex>]            セットアップ案内を表示（hook 登録はユーザが手動で行う）
agent-telemetry doctor                                    検出された agent ごとに binary / data dir / hook 登録を検証（自動修復はしない）
agent-telemetry backfill [--recheck] [--gc]               検出された agent すべての pr_urls / is_merged / review_comments を補完
agent-telemetry sync-db                                   検出された agent すべての JSONL/transcript → SQLite 変換（毎回フル再構築）
agent-telemetry update <session_id> <url>...              session-index.jsonl に PR URL を追加（重複排除）
agent-telemetry update --mark-checked <session_id>...     backfill_checked フラグをセット
agent-telemetry update --by-branch <repo> <branch> <url>  同一 repo+branch の全セッションに URL を追加
agent-telemetry hook <event> [--agent <claude|codex>]     hook サブコマンド（settings.json / config.toml から呼ばれる、既定 claude）
agent-telemetry flush [--since-last|--full] [--dry-run]   未送信のイベントをサーバへ OTLP/HTTP で flush（要 [server] 設定）
agent-telemetry migrate-to-events                         既存 session-index / transcript から events DB を再生成
agent-telemetry version                                   version を表示
```

`setup` は何も書き込まず案内表示のみを行う。hook 登録の編集はユーザが手動で行う（既存の自動登録エントリも手動で削除する）。

`backfill --recheck` は cursor を無視してフルスキャンする。`backfill --gc` は `gh` を呼ばず、`COALESCE(ended_at, timestamp)` から 24h 以上経過した PR-less・未 checked セッションを一括で `backfill_checked` にする移行 drain（deploy 後に 1 回手動実行する。`doctor` が backlog 件数を見て案内する）。`--detach` / `--worker` は Stop hook が内部で使う detached worker 起動用フラグ。

agent の検出は次の優先順位で行う:

1. `--agent` フラグ（hook 経路では必須に近い）
2. 環境変数 `AGENT_TELEMETRY_AGENT`（`claude` / `codex`）
3. データディレクトリの存在（`~/.claude/session-index.jsonl` および `~/.codex/session-index.jsonl` の有無）

`backfill` / `sync-db` / `doctor` は検出された agent **すべて** を対象とする。明示的に絞り込むには `--agent` を指定する。

---

## データファイル

agent ごとに収集元を分離し、SQLite DB は単一に集約する。

| ファイル | 形式 | 役割 |
|---|---|---|
| `~/.claude/session-index.jsonl` | JSON Lines | Claude Code セッション単位のメタデータ |
| `~/.claude/agent-telemetry-state.json` | JSON | Claude Code 用 backfill の cursor |
| `~/.config/agent-telemetry/config.toml` | TOML | user 識別子などのユーザ設定。両 agent で共通（後述「ユーザ設定ファイル」）。`XDG_CONFIG_HOME` が設定されていれば `$XDG_CONFIG_HOME/agent-telemetry/config.toml`。旧パス `~/.claude/agent-telemetry.toml` は fallback として読まれる（将来削除予定、stderr に migration warning を出す） |
| `~/.claude/projects/**/<session-id>.jsonl` | JSON Lines | Claude Code transcript |
| `~/.codex/session-index.jsonl` | JSON Lines | Codex CLI セッション単位のメタデータ |
| `~/.codex/agent-telemetry-state.json` | JSON | Codex CLI 用 backfill の cursor |
| `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl[.zst]` | JSON Lines (任意で zstd 圧縮) | Codex CLI rollout transcript |
| `~/.claude/agent-telemetry.db` | SQLite | sync-db が生成・更新する集計 DB（両 agent を集約）。実行ごとに最新の JSONL/transcript を `sessions` / `transcript_stats` に upsert する |
| `~/.claude/logs/session-index-debug.log` | テキスト | hook のデバッグログ（agent を問わず共通） |

`session-index.jsonl` の形式は agent 共通。`agent-telemetry-state.json` の cursor も agent ごとに独立して管理する。

### `session-index.jsonl` のレコード

```json
{
  "coding_agent": "claude",
  "agent_version": "1.2.3",
  "user_id": "ishii1492@gmail.com",
  "timestamp": "2026-02-27 12:34:56",
  "session_id": "xxx-yyy-zzz",
  "cwd": "/path/to/project",
  "repo": "org/repo",
  "branch": "feature-xxx",
  "is_default_branch": false,
  "pr_urls": ["https://github.com/org/repo/pull/123"],
  "pr_pinned": true,
  "pr_title": "feat: add metrics dashboard",
  "transcript": "/path/to/transcript.jsonl",
  "parent_session_id": "",
  "ended_at": "2026-02-27 13:00:00",
  "end_reason": "exit",
  "backfill_checked": true,
  "is_merged": true,
  "review_comments": 0,
  "changes_requested": 0
}
```

- `coding_agent` は `claude` または `codex`。欠落時は `claude` として扱う（後方互換）。
- `agent_version` は agent 自身が報告するバージョン文字列（取得不能なら空文字列）。バージョン跨ぎでの効率比較に使う。
- `user_id` はセッションを記録したユーザの識別子。SessionStart hook が後述の優先順位で解決して埋める。欠落時は `unknown` として扱う（後方互換）。`sync-db` は欠落レコードに対して現在の解決値で埋め戻し、JSONL にも書き戻す。
- `pr_urls` は PostToolUse / Stop / `update` / `backfill` から重複排除しつつ追記される。PostToolUse は `gh pr create` の出力だけを抽出対象にする。`sync-db` は配列の最後の 1 件を採用する。
- `pr_pinned: true` は backfill worker が `gh pr list --head <branch>` で確定した PR にセッションが束縛されたことを示す（Stop が `--pin-session` で対象を渡し、worker 内の pin が解決する）。pinned レコードに対しては PostToolUse / `update` / `backfill` の URL 追記は **すべて no-op** になる（誤接続防止）。欠落時は `false` として扱う（後方互換）。
- `pr_title` は backfill が `gh pr view --json title` で取得する PR タイトル。欠落時 / 取得失敗時は空文字列として扱う（後方互換）。
- `is_default_branch: true` は SessionStart 時点でこのセッションの branch が repo の**実デフォルトブランチ**（`git symbolic-ref refs/remotes/origin/HEAD` で動的判定。未設定時のみ `main` / `master` フォールバック）だったことを示す。デフォルトブランチは構造的に PR を持たないので、backfill は候補に入れず `gh pr list` を一度も呼ばない（第1層 admission control）。欠落 / `false` 時は通常のセッションとして扱う（後方互換）。branch 名のハードコードではなく動的判定なので `trunk` / `dev` 等にも対応する。
- `backfill_checked: true` のレコードは backfill で再 API 呼び出しされない。`gh pr list` が空を返したグループのうち `COALESCE(ended_at, timestamp)` から 24h 以上経過したセッション（第2層 horizon）にセットされ、abandoned ブランチを時間で retire する。`is_default_branch` を持たない過去のデフォルトブランチセッションもこの horizon + `backfill --gc` で収束する。
- Codex の場合: `end_reason` は Stop hook の最終発火を記録するため `stop` 固定。`transcript` は `~/.codex/sessions/.../rollout-*.jsonl[.zst]` のフルパス。
- 後方互換: 古いレコードに新フィールドが欠けていても扱える（欠落値は 0 / false / 空文字列、`user_id` のみ `unknown`）。

### `config.toml`（ユーザ設定ファイル）

`~/.config/agent-telemetry/config.toml`（`XDG_CONFIG_HOME` が設定されていれば `$XDG_CONFIG_HOME/agent-telemetry/config.toml`）に以下のキーを置ける。両 agent から共通参照される。

旧バージョンが書き出した `~/.claude/agent-telemetry.toml` も fallback として読まれる（新パスが存在しないときに限る）。fallback ヒット時は同一プロセスで 1 回だけ stderr に migration warning を出す。旧パスのサポートは将来のリリースで削除予定。

```toml
user = "ishii1492@gmail.com"
```

| キー | 型 | 説明 |
|---|---|---|
| `user` | string | `session-index.jsonl` の `user_id` フィールドに焼き付ける識別子。形式は任意（メールアドレス / pseudonym / UUID 等）。複数マシンで同一人物として束ねたい場合はマシン間で同じ値を揃える運用 |
| `[server]` セクション / `[[export]]` 配列 | table / array-of-tables | サーバ送信（export）を有効化する場合のみ設定する。詳細は本文書「サーバ送信」節を参照 |

ファイルが存在しない・キーが欠落・パース不能の場合は無視して次の優先順位にフォールバックする（hook を失敗させない）。

---

## SQLite データモデル

データは **append-only な `events` テーブル** を SoR とし、`sessions` / `transcript_stats` / `pr_metrics` / `session_concurrency_*` は events を集約した **derived VIEW** として組み立てる。Stop / SessionEnd / backfill の各 hook は対応するイベントを **追記** するだけで、過去の events 行は mutation しない。`is_merged` のような後追い更新は `agent.pr.observed` イベントの追加で表現し、VIEW 側で `MAX(occurred_at)` を取って latest-wins を解決する。

`sync-db` はクライアント側の transcript パース結果を `agent.transcript.scanned` イベントとして events に書き込み、必要に応じて VIEW を再定義する。`schema.sql` は events テーブル DDL と VIEW 定義を埋め込み、SHA256 ハッシュを `schema_meta` と比較して不一致時のみフル再構築する（実装詳細は `docs/design.md`）。明示的なマイグレーションコマンドは持たない（events への一回限りの初期投入だけ `agent-telemetry migrate-to-events` で行う）。

### `events` テーブル

| カラム | 型 | 説明 |
|---|---|---|
| `event_id` | TEXT | UNIQUE。イベント内容から deterministic に導出する content hash（`sha256(event_name ‖ coding_agent ‖ session_id ‖ attributes)`）。冪等性の衝突キーであり、flush 差分検知の cursor には使わない。**同一内容の再導出は同じ `event_id`** になるため `INSERT OR IGNORE` で dedup され、`sync-db` 反復でも events が増殖しない。内容が変われば別 hash → 新 snapshot row（詳細は `docs/design.md`） |
| `local_sequence` | INTEGER | PRIMARY KEY AUTOINCREMENT。クライアント側 DB 内の挿入順序 cursor。`flush --since-last` はこれを使う。サーバ側 DB では受信順の監査用途のみ |
| `occurred_at` | TEXT | イベント発生時刻（ISO8601）。snapshot 系イベントは観測時点を入れる |
| `received_at` | TEXT | サーバが受信した時刻。クライアント側 DB では空 |
| `session_id` | TEXT | 対象セッション ID |
| `coding_agent` | TEXT | `claude` または `codex` |
| `event_name` | TEXT | `agent.session.started` / `agent.session.ended` / `agent.transcript.scanned` / `agent.pr.observed`（拡張時はここに追加） |
| `attributes` | TEXT | JSON。各イベント名ごとの属性を flat に格納（後述「イベント名と属性」） |

INDEX: `(session_id, coding_agent, event_name, occurred_at, local_sequence)`, `(event_name, occurred_at)`。

書き込みは常に `INSERT OR IGNORE`。`event_id` が deterministic content hash のため、同一内容の再導出・再着信・再送はすべて同じ `event_id` に潰れて重複しない。

#### イベント名と属性

| `event_name` | 性質 | 主な属性 |
|---|---|---|
| `agent.session.started` | one-shot | `agent_version`, `user_id`, `cwd`, `repo`, `branch`, `transcript`, `parent_session_id`, `started_at` |
| `agent.session.ended` | one-shot | `ended_at`, `end_reason` |
| `agent.transcript.scanned` | snapshot（複数回 emit 可） | `tool_use_total`, `mid_session_msgs`, `ask_user_question`, `input_tokens`, `output_tokens`, `cache_write_tokens`, `cache_read_tokens`, `reasoning_tokens`, `model`, `is_ghost` |
| `agent.pr.observed` | snapshot（複数回 emit 可） | `pr_url`, `pr_title`, `pr_state`, `is_merged`, `review_comments`, `changes_requested`, `pr_pinned` |

snapshot 系は同一セッションで複数行が events に残り、VIEW 側で `occurred_at` 最大の 1 行を採用する。新メトリクスを増やす場合は対応する属性を増やすか、新イベント名を追加するだけでよく、events テーブル自体の DDL 変更は不要。

#### 派生 VIEW としての `sessions` / `transcript_stats`

以下の「`sessions` テーブル」「`transcript_stats` テーブル」のカラム定義は **VIEW の出力スキーマ** を指す。dashboard JSON / SQL クエリから見える形は append-only 移行前と同じだが、実体は events からの集約クエリ（`agent.session.started` の値 ← `agent.session.ended` の値 ← 最新 `agent.transcript.scanned` ← 最新 `agent.pr.observed`）。

### `sessions` テーブル

| カラム | 型 | 説明 |
|---|---|---|
| `session_id` | TEXT | エージェント発行のセッション ID |
| `coding_agent` | TEXT | `claude` または `codex` |
| `agent_version` | TEXT | agent 自身が報告するバージョン文字列（取得不能なら空） |
| `user_id` | TEXT | セッションを記録したユーザの識別子。`session-index.jsonl` の `user_id` から sync 時に転写。欠落時は `unknown` |
| `timestamp` | TEXT | セッション開始時刻（ISO8601） |
| `cwd` | TEXT | 作業ディレクトリ |
| `repo` | TEXT | リポジトリ（`org/repo` 形式） |
| `branch` | TEXT | ブランチ名 |
| `pr_url` | TEXT | PR URL（`pr_urls` 配列の最後の 1 件） |
| `pr_title` | TEXT | PR タイトル。backfill が `gh pr view --json title` で取得（取得不能なら空） |
| `transcript` | TEXT | transcript ファイルパス |
| `parent_session_id` | TEXT | 親セッション ID。サブエージェント判定用 |
| `ended_at` | TEXT | セッション終了時刻 |
| `end_reason` | TEXT | SessionEnd hook の終了理由（Codex は `stop` 固定） |
| `is_subagent` | INTEGER | `parent_session_id` 非空なら 1 |
| `backfill_checked` | INTEGER | backfill 処理済みなら 1 |
| `is_merged` | INTEGER | PR がマージ済みなら 1 |
| `task_type` | TEXT | ブランチプレフィックス（feat/fix/docs/chore） |
| `review_comments` | INTEGER | PR レビューコメント数 |
| `changes_requested` | INTEGER | CHANGES_REQUESTED レビュー回数 |

`sessions` は events を `agent.session.started` を起点に集約した VIEW。`coding_agent` / `agent_version` / `user_id` / `cwd` / `repo` / `branch` / `transcript` / `parent_session_id` は `agent.session.started` の属性、`ended_at` / `end_reason` は同一セッションの `agent.session.ended` 属性、`pr_*` 系・`is_merged` / `review_comments` / `changes_requested` は最新（`MAX(occurred_at)`）の `agent.pr.observed` 属性から組み立てる。`is_subagent` は `parent_session_id != ''`、`backfill_checked` は `agent.pr.observed` が 1 件でもあるか / `pr_pinned` で導出する。論理的なキーは (`session_id`, `coding_agent`) で、events への JOIN で復元できる。

### `transcript_stats` テーブル

| カラム | 型 | 説明 |
|---|---|---|
| `session_id` | TEXT | セッション ID |
| `coding_agent` | TEXT | `claude` または `codex` |
| `tool_use_total` | INTEGER | ツール呼び出し総数 |
| `mid_session_msgs` | INTEGER | mid-session ユーザーメッセージ数（tool_result 除外） |
| `ask_user_question` | INTEGER | AskUserQuestion 呼び出し回数（Codex では常に 0） |
| `input_tokens` | INTEGER | 入力トークン合計 |
| `output_tokens` | INTEGER | 出力トークン合計 |
| `cache_write_tokens` | INTEGER | cache write トークン合計 |
| `cache_read_tokens` | INTEGER | cache read トークン合計 |
| `reasoning_tokens` | INTEGER | reasoning トークン合計（Claude では常に 0、Codex のみ非ゼロ） |
| `model` | TEXT | セッション内で最後に観測した model |
| `is_ghost` | INTEGER | ユーザー発話相当のエントリが 0 件なら 1 |

`transcript_stats` は最新（`MAX(occurred_at)`）の `agent.transcript.scanned` イベントから組み立てる VIEW。論理的なキーは (`session_id`, `coding_agent`)。snapshot 系イベントとして events に積まれるため、`sync-db --recheck` 等でクライアントが再パースして新しい snapshot を emit すると VIEW の値が自動で更新される（過去 snapshot は events に残り、replay 可能）。

トークンの収集元:

- Claude: assistant message の `usage.input_tokens` / `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens`
- Codex: rollout JSONL の `event_msg.payload.type == "token_count"` の最終累積値（input / output / cache_read / cache_write / reasoning）

いずれも該当フィールド欠落時は 0 として扱う。

### `pr_metrics` VIEW

PR 単位の集約ビュー。次のフィルタ条件を適用する。

| 条件 | 理由 |
|---|---|
| `pr_url != ''` | PR 未作成セッションを除外 |
| `is_subagent = 0` | サブエージェントセッションを除外 |
| `is_merged = 1` | 未マージ・放棄 PR を除外（最終成果物のみ） |
| `is_ghost = 0` | ゴーストセッションを除外 |
| `repo NOT IN (...)` | 運用ノイズリポジトリを除外（除外対象は `internal/syncdb/schema/schema.sql` で固定列挙） |

集約カラム: `pr_url`, `pr_title`, `coding_agent`, `user_id`, `model`, `session_count`, `tool_use_total`, `mid_session_msgs`, `ask_user_question`, `input_tokens`, `output_tokens`, `cache_write_tokens`, `cache_read_tokens`, `reasoning_tokens`, `review_comments`, `changes_requested`, `total_tokens`, `fresh_tokens`, `tokens_per_session`, `tokens_per_tool_use`, `pr_per_million_tokens`

`pr_title` は同一 PR に紐づく全セッションで等しい想定だが、安全のため `MAX(s.pr_title)` で集約する（未取得セッションが空文字列を返しても、取得済みセッションのタイトルが採用される）。

`task_type` は集約軸から外れている（ADR-024 で「定量指標は task_type を集計軸に使わない」方針が採用されたため）。`sessions.task_type` カラム自体は後方互換と任意フィルタの余地として残す。

GROUP BY は (`pr_url`, `coding_agent`, `user_id`)。同一 PR が複数 agent / 複数ユーザから触られた場合はそれぞれ別行になる（実運用上ほぼ発生しないが意味的に分離する。pair coding で人物が分かれた場合の集計を正しく扱うため）。

`total_tokens` は input / output / cache write / cache read / reasoning token の合計。`fresh_tokens` は `cache_read_tokens` を除いた合計（input / output / cache write / reasoning）で、長時間セッションで `cache_read_tokens` が支配的になり「重さ」の体感と乖離する問題に対する代替指標。`pr_per_million_tokens` は 100 万 token あたりに完了した PR 数。

### `session_concurrency_daily` / `session_concurrency_weekly` VIEW

トップレベルセッションの同時実行数を時間軸で集約する。`sessions.timestamp` と `sessions.ended_at` の区間重なりから算出し、subagent / ghost / 運用ノイズリポジトリを除外する。`coding_agent` ごとに別行で集約する。

集約カラム: `day` または `week_start`, `coding_agent`, `avg_concurrent_sessions`, `peak_concurrent_sessions`

---

これらのカラム/VIEW のメトリクス名・型・ラベル一覧、および何を観察したいか・どう解釈すべきかは `docs/metrics.md` を参照する。

---

## サーバ送信

サーバ送信は **オプトイン** 機能。`~/.config/agent-telemetry/config.toml` に `[server]` または `[[export]]` が設定された場合のみ有効になる。設定なしのローカル単独利用は従来通り動作する（旧パス `~/.claude/agent-telemetry.toml` も fallback として読まれる）。

転送モデルは **append-only イベント列の OTLP/HTTP flush**。クライアントはローカル `events` テーブルから未送信行を抽出し、OTel Logs 形式で **1 つ以上の export target**（中央サーバ / OTel Collector / backend の OTLP intake）に送る。中央サーバ（`agent-telemetry-server`）は受信した events を冪等に追記し、`sessions` / `transcript_stats` / `pr_metrics` などは VIEW として組み立てる。実装方針・差分検知・配布形態の詳細は `docs/design.md ## サーバ側集約パイプライン` を参照。本節はクライアント・サーバの外部契約のみ記述する。

> **pluggable export（[0040] / [0042]）**: 旧来の「単一 `[server]` + 固定 Bearer + JSON OTLP Logs」を、**endpoint + 可変 auth header + encoding/protocol + signal/representation を持つ export target の配列**に拡張した。`[server]` は安定 ID `"server"` の単一 target として後方互換に正規化される。**direct**（client → backend intake 直送）と **collector**（client → OTel Collector → fanout）の 2 デプロイレシピをサポートし、Datadog をリファレンス実装とする。本節は raw events（OTLP Logs）の representation を記述する。`pr_metrics` gauge（OTLP Metrics）の representation は [0043] で追加する。

### 送信するデータ — events のみ

クライアントは `events` テーブルから `last_flushed_sequence` より後に挿入された行を抽出して送る。`session-index.jsonl` の生行や transcript JSONL / rollout JSONL（会話本体）は **送らない**。`is_merged` / `pr_url` / `review_comments` 等の後追い更新は、backfill が新しい `agent.pr.observed` イベントを追記し、それが次の flush で送られることで反映される。

### クライアント側設定 — export target

`~/.config/agent-telemetry/config.toml`（旧パス: `~/.claude/agent-telemetry.toml`）に export target を設定する。2 つの書式があり、併存も可能:

**(1) レガシー単一サーバ（後方互換）** — `[server]` セクション:

```toml
[server]
endpoint = "https://telemetry.example.com"
token = "xxx"
```

`[server]` は安定 ID `"server"`・`Authorization: Bearer <token>`・`encoding = "json"`・`signals = ["logs"]` の単一 target に正規化される。既存設定はそのまま動く。

**(2) export target 配列** — `[[export]]`（array-of-tables、複数可）:

```toml
# 中央サーバ（既存の [server] と等価）
[[export]]
id = "central"
endpoint = "https://telemetry.example.com"
token = "xxx"

# Datadog direct（protobuf + dd-api-key）
[[export]]
id = "datadog"
endpoint = "https://otlp.datadoghq.com"
token = "${DD_API_KEY}"
auth_header = "dd-api-key"
encoding = "protobuf"
signals = ["logs"]
```

| キー | 型 | 既定 | 説明 |
|---|---|---|---|
| `id` | string | （必須） | target の**安定 ID**。per-target cursor（後述 `flush_cursors`）のキー。URL を変えても cursor を保つため endpoint とは別に持つ。重複は起動エラー |
| `endpoint` | string | （必須） | **base URL**（signal path を含めない）。クライアントが signal path を補完する（**endpoint モデル**節を参照） |
| `token` | string | （必須） | 認証用 credential。`${VAR}` のみ環境変数展開する（秘密を config に直書きしない direct レシピ向け） |
| `auth_header` | string | `Authorization` | credential を載せる HTTP ヘッダ名。Datadog は `dd-api-key` |
| `auth_scheme` | string | `Authorization` 時は `Bearer`、それ以外は空 | ヘッダ値の prefix。空なら raw token（`dd-api-key: <token>`）、非空なら `<scheme> <token>`（`Authorization: Bearer <token>`） |
| `encoding` | string | `json` | wire encoding。`json`（自前サーバ / Collector）または `protobuf`（Datadog direct logs は protobuf 必須） |
| `signals` | array | `["logs"]` | 送る representation。本仕様では `logs`（raw events / OTLP Logs）。`metrics`（`pr_metrics` gauge）は [0043] |

設定された target が 1 つも無い、または全 target の `endpoint`/`token` が空の場合、`agent-telemetry flush` は warning を stderr に出して exit code 0 で終了する（cron で叩いて壊れないこと）。`signals` に `logs` を含まない target は本仕様の logs flush では skip される（[0043] の metrics flush が扱う）。

#### endpoint モデル（base + signal path 補完）

target の `endpoint` は **base URL** とし、クライアントが signal ごとの path を補完する（実装時の解釈ブレを防ぐため 1 つに固定）:

| signal | 補完される path | 例（base = `https://otlp.datadoghq.com`） |
|---|---|---|
| logs | `/v1/logs` | `https://otlp.datadoghq.com/v1/logs` |
| metrics（[0043]） | `/v1/metrics` | `https://otlp.datadoghq.com/v1/metrics` |

Datadog の OTLP intake（`https://otlp.datadoghq.com` + `/v1/logs`・`/v1/metrics`）も自前サーバ（base + `/v1/logs`）もこのモデルに一致する。「signal ごとの完全 URL を設定する」方式は採らない。

#### direct / collector の 2 デプロイレシピ

| レシピ | client encoding | credential の所在 | 補足 |
|---|---|---|---|
| **direct** | backend に依存（Datadog logs は `protobuf`） | client（submit-only key） | 個人 / 小チーム向け。追加プロセス無し。Datadog direct は protobuf + `dd-api-key` |
| **collector** | `json`（変更不要） | Collector が 1 箇所集約（client は秘密なし） | team / 多 client 向け。Collector が protobuf + `dd-api-key` 変換と fanout を担う。レシピは `deploy/otel-collector/` |

### `agent-telemetry flush` のフラグ

| フラグ | 説明 |
|---|---|
| `--since-last`（既定） | `state.json` の `flush_cursors[target_id]`（各 target の cursor）より後に挿入された events を OTLP/HTTP で送信 |
| `--full` | cursor を無視して events 全体を全 target に送信（サーバ初期化や障害復旧で使う。冪等なので二重送信は害がない） |
| `--dry-run` | 送信せず target ごとの対象件数とサイズだけ表示 |
| `--agent <claude\|codex>` | agent を絞り込む。省略時は検出された全 agent |

進行中セッション（`agent.session.ended` が未着）の events も送る。サーバ側 VIEW が「`session.ended` が無いセッションは `ended_at = NULL`」として表現するため、進行中の状態もダッシュボードに反映できる（旧設計と異なり、Stop 完了を待つ必要がない）。

### `agent-telemetry-state.json` への追加フィールド — per-target cursor

```json
{
  "last_backfill_offset": 123,
  "last_meta_check": "...",
  "last_flushed_sequence": 12345,
  "flush_cursors": {
    "server": 12345,
    "datadog": 12000
  }
}
```

- `flush_cursors` は **`{target_id: last_flushed_sequence}` の map**。各 target は自身の cursor を**独立**に持ち、その target への送信成功時のみ前進する（[0040] per-target cursor 契約）。`event_id` は冪等性キーであり差分 cursor には使わない（cursor は単調増加する `local_sequence`）
- **独立前進**: target A 成功 / B 失敗の部分失敗時、A の cursor だけ進み B は据え置く。次回 flush で B の範囲のみ再送する（A は二重送信しない）。受理済み events は受信側 `INSERT OR IGNORE` で無害にスキップされる
- **新規 target**: cursor 不在は 0 扱い（= 全 events を送る。冪等にスキップされるが raw-logs を新サーバに向ける初期同期は重い）
- **レガシー seed**: `flush_cursors` に `"server"` エントリが無い場合に限り、旧フィールド `last_flushed_sequence` を `"server"` target の cursor として seed する（pre-0042 binary からのアップグレードで全履歴を再送しないため）。`last_flushed_sequence` 自体は後方互換のため残す
- **target removal**: 設定から消えた target の cursor は次回 save 時に GC される。**rename** は安定 ID を保てば cursor 継続（ID を変えると新規 target 扱いで cursor=0）
- backfill が新しい `agent.pr.observed` イベントを events に追記すると、それは各 target の cursor より大きい `local_sequence` を持ち、次の flush で自動的に拾われる

### プロトコル — OTLP/HTTP Logs

target は base endpoint + signal path（`/v1/logs`）に POST する。auth header / encoding は target 設定に従う:

- **auth header**: 既定 `Authorization: Bearer <token>`。`auth_header` / `auth_scheme` で `dd-api-key: <token>`（raw token）等に切替
- **encoding**: 既定 `json`（`Content-Type: application/json`）。`encoding = "protobuf"` の target は **OTLP/HTTP protobuf**（`Content-Type: application/x-protobuf`）で送る。論理的な payload は両者同一で、wire serialization と Content-Type のみが異なる。Datadog direct logs intake は protobuf 必須なので protobuf を使う
- **gzip** は両 encoding で optional（`Content-Encoding: gzip`）

JSON encoding の例（自前サーバ / Collector 宛て）:

```
POST /v1/logs
Authorization: Bearer <api_key>
Content-Type: application/json
Content-Encoding: gzip   (optional)

{
  "resourceLogs": [{
    "resource": {
      "attributes": [
        {"key": "service.name",       "value": {"stringValue": "agent-telemetry"}},
        {"key": "service.version",    "value": {"stringValue": "x.y.z"}}
      ]
    },
    "scopeLogs": [{
      "scope": {"name": "agent-telemetry/client"},
      "logRecords": [
        {
          "timeUnixNano": "1715600000000000000",
          "observedTimeUnixNano": "1715600000000000000",
          "severityNumber": 9,
          "eventName": "agent.session.started",
          "attributes": [
            {"key": "event_id",     "value": {"stringValue": "01HXYZ..."}},
            {"key": "session_id",   "value": {"stringValue": "..."}},
            {"key": "coding_agent", "value": {"stringValue": "claude"}},
            {"key": "user_id",      "value": {"stringValue": "..."}},
            {"key": "repo",         "value": {"stringValue": "org/repo"}},
            ...
          ]
        },
        { "eventName": "agent.transcript.scanned", ... },
        { "eventName": "agent.pr.observed", ... }
      ]
    }]
  }]
}
```

- payload は **OTLP/HTTP**（既定 JSON エンコード）に準拠する（OTel collector / Prometheus / Loki / Tempo などの標準ツールでそのまま受け取れることを優先）。`encoding = "protobuf"` の target には同一 payload を OTLP protobuf でシリアライズして送る
- `eventName` は本文書「`events` テーブル」の `event_name` と 1:1。属性も同じセマンティクス
- `resource.attributes` の `service.name` は **`agent-telemetry` 固定**（agent 別 service にはしない）。`claude` / `codex` の区別は各 logRecord の `coding_agent` 属性で表現する（[0040] の service.name 決定）
- `event_id` 属性はクライアントが一意に採番。サーバは `event_id` で `INSERT OR IGNORE`（重複は害なく排除される）
- HTTP gzip は **optional**。1 セッションあたり events 数〜十数件 × 1 KB 程度なので、無圧縮でも数百 KB に収まるケースが多い
- 1 リクエストあたり最大 50 MB（保険）。events だけなので通常は超えない

レスポンス:

```json
{
  "partialSuccess": {
    "rejectedLogRecords": 0,
    "errorMessage": ""
  }
}
```

OTLP/HTTP の標準 `partialSuccess` レスポンスをそのまま使う（自前サーバ宛て / JSON）。target の cursor 前進は **transport の成否** で判断し、partial success では再送しない（OTLP 仕様で partial success は「サーバが永続的に拒否した不正レコード」を表し、クライアントは retry しない前提）。cursor は **target ごと**に独立して前進する（`flush_cursors[target_id]`）。

| サーバ応答 | 意味 | クライアント挙動 |
|---|---|---|
| HTTP 2xx + `rejectedLogRecords == 0` | 全件受理 | その target の cursor を送信した最大 `local_sequence` に進める |
| HTTP 2xx + `rejectedLogRecords > 0` | 一部レコードが **永続的に拒否**（`event_id` / `session_id` / `coding_agent` / `event_name` 欠落などの validation 失敗） | サーバが `rejected.log` に記録済み。クライアントは cursor を **進め**（同じ不正データを再送しても通らず無限ループになるため retry しない）、`errorMessage` と件数を warning として stderr に出す |
| ネットワークエラー / 非2xx（5xx / 429 / 401 等） | 配送自体が失敗 | バッチ全体を失敗扱いにし、その target の cursor を **進めない**。次回 flush で同じ範囲を再送。受理済み events は `INSERT OR IGNORE` で無害にスキップされる。他 target の cursor 前進には影響しない |

> backend（Datadog protobuf 等）は OTLP の `ExportLogsServiceResponse`（protobuf）を返すため、クライアントは JSON `partialSuccess` を decode しない（2xx を全件受理として扱う）。`partialSuccess` の解釈は JSON encoding の自前サーバ宛てに限る。

`rejectedLogRecords` は reject **件数**しか返さず、どの record が拒否されたかは示さない。永続拒否は同一データの再送で解消しないため、cursor を据え置く設計は無限ループになる。よって永続拒否はサーバ側 `rejected.log` への記録に委ね、クライアントは前進する。スキーマ不一致での全拒否は廃止（events table の DDL は安定で、新メトリクスは新属性で追加可能なため）。

### サーバ binary

サーバは `agent-telemetry-server` という別 binary で提供する。

```
agent-telemetry-server [--data-dir <path>] [--listen <addr>]
```

| フラグ | 既定 | 説明 |
|---|---|---|
| `--data-dir` | `/var/lib/agent-telemetry` | サーバが集約 DB を保管する root |
| `--listen` | `:8443` | HTTP listen アドレス |

環境変数 `AGENT_TELEMETRY_SERVER_TOKEN` で API key を受け取る。未設定時は起動時にエラー終了する。

### サーバ側データ配置

| ファイル | 形式 | 役割 |
|---|---|---|
| `<data_dir>/agent-telemetry.db` | SQLite | 全 user 集約 DB。`events` テーブル（SoR、`INSERT OR IGNORE`）+ 派生 VIEW（`sessions` / `transcript_stats` / `pr_metrics` / `session_concurrency_*`）を本文書のスキーマで保持 |
| `<data_dir>/rejected.log` | テキスト | 不正な payload / 認証失敗のログ |

サーバはクライアントから受信した events を `event_id` で冪等に追記するだけで、transcript 解釈や集計は行わない。VIEW の中身（`sessions` 等を events からどう組み立てるか）はサーバとクライアントで共有する（`internal/syncdb/schema/schema.sql`）。

サーバの SQLite は Grafana datasource として読み込まれる。datasource の `uid: agent-telemetry` を踏襲し、ローカル Grafana のダッシュボード JSON をそのまま再利用する。VIEW の出力スキーマは旧設計と同じなので、ダッシュボード JSON 側の SQL は無変更で動く。

### 新メトリクス追加と遡及反映

events は **events table の DDL を変えずに新属性 / 新イベント名を増やせる** ため、旧設計の「サーバ先行デプロイ → 全クライアント binary 更新 → `push --full`」運用は不要。流れ:

1. 新属性 / 新イベントを emit するクライアントを順次配布（旧クライアントは無変更でも events を送り続ける）
2. サーバ binary 側の VIEW 定義を更新（events の新属性を引いて新カラムを生やす）
3. 既存 events に対しては「未来の新イベントは存在しない」が、`agent.transcript.scanned` のような snapshot 系を再 emit すれば過去にも遡及で新属性が乗る

`schema_hash` mismatch によるサーバ受信拒否は廃止。events table の DDL に互換破壊変更が入る場合のみ、新 endpoint（例: `/v2/logs`）を切る運用とする。

### 旧 push 経路からの移行（完了）

[0009] / [0028]-[0031] で実装した「`sessions` / `transcript_stats` 集計行を `POST /v1/metrics` で送る」経路は本仕様で廃止済み。`agent-telemetry push` / `agent-telemetry-server` の `/v1/metrics` ハンドラ・`schema_hash` mismatch 受信拒否・`INSERT OR REPLACE` upsert・`collisions.log` は **v0.0.10 を 1 リリース併走させたのち削除した**（[0038]）。現行はクライアント `agent-telemetry flush` → サーバ `/v1/logs`（OTLP/HTTP Logs）の一本のみ。

移行が必要なユーザは旧 push 経路を含む v0.0.10 のうちに `agent-telemetry migrate-to-events`（session-index / transcript から events DB を再生成）と `agent-telemetry-server migrate-to-events`（events schema 確定）を済ませる。展開後は `sessions` / `transcript_stats` が events 由来の VIEW として提供される。

### サーバ MVP の非目標

- user 別の read/write 権限分離（RLS / OIDC）— 信頼境界 = チーム内を前提
- transcript 本体のサーバ保管 — events のみを送る方針なのでそもそも保管しない。会話ログを共有したいケースは別ツールで対応
- write API 以外の提供（read API・専用 UI）— Grafana から直接 SQLite を読む構成
- OTel Metrics / Traces signal の受信 — Logs（events）のみで完結する。tool 使用などを Counter 化したい場合は後追いで Metrics signal の endpoint を追加する

---

## 環境変数

| 変数 | 説明 |
|---|---|
| `AGENT_TELEMETRY_AGENT` | hook / CLI のデフォルト agent（`claude` / `codex`）。`--agent` が省略され、かつ自動検出を行わない経路で参照する |
| `AGENT_TELEMETRY_USER` | `session-index.jsonl` の `user_id` を上書きする。CI / コンテナで決定的に設定したい場合に使う。最優先のソース（`config.toml` の `user` キーや git config より優先される） |
| `AGENT_TELEMETRY_SERVER_TOKEN` | サーバ binary `agent-telemetry-server` 起動時の Bearer 認証用 API key。クライアント側 `config.toml` の `[server] token` と一致させる。サーバ側で必須、クライアント側では参照しない |
| `XDG_CONFIG_HOME` | クライアント側で `config.toml` の置き場所を上書きする。設定されている場合は `$XDG_CONFIG_HOME/agent-telemetry/config.toml` を読み、無ければ `~/.config/agent-telemetry/config.toml` を読む |
| `CODEX_HOME` | Codex CLI のホームディレクトリ。未指定なら `~/.codex`。Codex 標準と同じ |

---

## 非目標

- 個別の API 課金額の算出（モデルごとの単価変動が大きいため、token 量のみを記録する）
- permission UI 表示回数や `perm_rate` の計測（Claude Code の auto mode 進化で改善対象としての価値が低いと判断したため廃止）
- 未マージ PR や PR なしセッションの効率指標（`pr_metrics` のスコープ外）
- 明示的なマイグレーションコマンド（スキーマ変更は `sync-db` がハッシュ比較で透過的に再構築する）

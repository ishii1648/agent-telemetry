# agent-telemetry 計測フレームワーク

この文書は agent-telemetry が **何を観察するために** どのメトリクスを収集しているかを整理する。
メトリクス名・型・ラベルの一覧は本文書末尾「メトリクスカタログ」を、SQLite テーブル/VIEW のカラム定義は `docs/spec.md ## SQLite データモデル` を、実装は `docs/design.md` を参照する。

---

## 計測の前提

agent-telemetry は **HITL 型コーディングエージェント（Claude Code / Codex CLI）の効率を PR 単位で観察する** ためのツールである。
"効率" は単一指標では測れないが、本ツールは観察軸を **トークン効率** と **開発生産性** の 2 軸に絞り込む。横断軸として **エージェント間比較** と **モデル / バージョン跨ぎ比較** を併用する。`pr_metrics` VIEW のフィルタ（merged のみ・subagent / ghost / 運用ノイズリポジトリ除外）はすべての軸の前提として効く。

| 軸 | 答えたい疑問 |
|---|---|
| 1. トークン効率 | 1 PR を完了するのに何 token かかっているか |
| 2. 開発生産性 | エージェントが詰まらず PR をマージまで到達させられているか |
| 横断 A. エージェント間比較 | claude vs codex の差はどこに出るか |
| 横断 B. モデル / バージョン跨ぎ | バージョンアップで効率がどう変わったか |

---

## 1. トークン効率

**疑問**: 1 PR を完了するのに何 token かかっているか。token あたり何 PR を出せているか。

**主要指標**:
- `agent_pr_total_tokens` — PR 内の全セッション合計トークン（input / output / cache_write / cache_read / reasoning の和）
- `agent_pr_fresh_tokens` — `cache_read` を除いた合計（input / output / cache_write / reasoning）。長時間セッションで cache_read が支配的になると total_tokens が「重さ」の体感と乖離するため、代替の効率指標として併用する
- `agent_pr_per_million_tokens` — 100 万 token あたりに完了した PR 数。効率の逆数指標として最も読みやすい
- `agent_pr_tokens_per_session` — PR 内の平均セッションサイズ。セッションを分割して進めるかどうかの傾向

**補助指標**:
- `agent_pr_tokens_per_tool_use` — ツール 1 回あたりのトークン消費。**単独で良し悪しを評価せず**、token 効率の傾向理解と異常検出に使う。例: 高 `reasoning_tokens` × 低 `tool_use_total` のセッションは「思考の空回り」の兆候
- `agent_pr_tool_use_total` — PR 内の全ツール呼び出し数。`tokens_per_tool_use` の分母

**内訳指標**:
- `agent_pr_input_tokens_total` / `agent_pr_output_tokens_total` / `agent_pr_cache_write_tokens_total` / `agent_pr_cache_read_tokens_total` / `agent_pr_reasoning_tokens_total`
- 単体では軸として扱わないが、total / fresh の異常時に内訳に降りて原因分解する用途

**解釈の注意**:
- 集計対象は `is_merged = 1` の PR のみ。未マージ PR は ROI 不明のため除外している（`pr_metrics` VIEW のフィルタ）
- リファクタ系 PR（差分は小さいが議論が長い）と feature 系 PR は素直に比較できないことがある。`task_type` ラベルで絞り込むか、PR 別スコアカードで個別に見る
- `model` 跨ぎだとキャッシュ動作・出力傾向が違うため、モデル混在の単純平均は誤読しやすい
- total / fresh どちらで見るかは目的次第。「課金や物理 token 量」を見たいなら total、「実質的な作業量」を見たいなら fresh
- `cache_read_tokens` が大きい = 効率が良いとは限らない。長大なコンテキストで自然と増える側面があるため、`fresh_tokens` を主軸にする運用が安全
- `cache_write_tokens` が異常に大きい = キャッシュヒットしておらず毎回書き直している兆候。プロンプト構造の安定性を疑う材料として使う

---

## 2. 開発生産性

**疑問**: エージェントが正しい道筋を一発で見つけられているか。レビューでどれくらい差し戻されているか。同時に何セッション捌けているか。

**主要指標**:
- `agent_session_mid_session_msgs_total` — セッション中のユーザ追加メッセージ数（道筋修正の頻度）
- `agent_session_ask_user_question_total` — 仕様確認ツールの発火回数（Claude のみ、Codex は 0 固定）
- `agent_pr_review_comments` — PR レビューコメント数
- `agent_pr_changes_requested` — CHANGES_REQUESTED レビュー回数
- `agent_concurrent_sessions_avg` / `agent_concurrent_sessions_peak` — 日次・週次の同時実行数。ユーザのマルチタスク上限を測る

**解釈の注意**:
- `mid_session_msgs` が高い = **エージェントが正しい道筋を見つけられない**、もしくはユーザが auto モードを信用しきれていない
- `ask_user_question` は agent 非対称（Codex に相当ツールがない）。agent 跨ぎ比較に使ってはいけない
- `changes_requested` は人間レビュアの厳しさ・PR 規模に依存する。同一レビュア・同一規模帯での時系列比較が安全
- 同時実行数は **手元のマシン能力 × 自分のマルチタスク能力** の上限を測る。peak が高い時期にトークン効率が落ちていれば「並列やりすぎ」のサイン
- `ended_at` が空のセッションは現在時刻で打ち切る扱いのため、進行中セッションの含まれる時間帯は同時実行数が膨らむ
- ghost / subagent は `pr_metrics` から除外されるが、生のセッション数で見る場合はフィルタを意識する必要がある（`agent_session_is_ghost` / `agent_session_is_subagent` で分離）

---

## 横断 A. エージェント間比較

`coding_agent` ラベルで claude / codex を区別。

**比較できる指標**:
- token 系（input / output / cache_read / cache_write）— ただしキャッシュ意味論差に注意
- `tool_use_total` / `mid_session_msgs` / `review_comments` / `changes_requested`
- PR 効率系すべて（`total_tokens` / `fresh_tokens` / `per_million_tokens`）

**比較してはいけない指標**:
- `ask_user_question` — Codex に相当ツール無しで 0 固定（agent 非対称）
- `reasoning_tokens` — Claude は extended thinking を分離計上できず 0 固定（API 構造的制約）。Codex 側だけ reasoning が乗るため絶対値比較は誤読
- `total_tokens` の絶対値も上記の影響を受ける。agent 跨ぎでは `fresh_tokens` から `reasoning_tokens` を引いた値、もしくは内訳ごとの並列比較が安全

agent 跨ぎでは `model` / `agent_version` の混在で平均化が壊れやすい。ラベル絞り込みを必ず併用する。

---

## 横断 B. モデル / バージョン跨ぎ比較

`agent_version` は session レベルでのみ集計可能。`pr_metrics` には集約しない（1 PR 内でバージョンが混在しうるため平均が無意味になる）。

想定ユースケース: 「version A の全 session の token 効率」vs「version B の全 session」を session ベースのクエリで比較する。詳細は `docs/design.md ## agent_version の取得` を参照。

`model` ラベルは PR 単位でも保持されるが、PR 内でモデル切替が起きた場合は最後に観測した model が記録される点に注意。

---

## 観察しないこと

| 非目標 | 理由 |
|---|---|
| 個別 API 課金額 | モデル単価変動が大きく、token 量の方が安定指標 |
| permission UI 表示回数 | Claude Code の auto mode 進化で改善ターゲットとしての価値が低下 |
| 未マージ PR / PR 無しセッションの効率 | ROI 不明のため `pr_metrics` から除外 |
| `task_type` 軸の集計 | カテゴリ間で性質が違いすぎ、平均値が誤読されやすいため集計軸から廃止（`sessions.task_type` カラム自体は任意フィルタ用に残す） |
| キャッシュヒット率を単独軸として | 長文コンテキストでヒット率が機械的に上がる傾向があり、運用の良し悪しとの相関が弱い。`fresh_tokens` の構成要素としてのみ扱う |
| reasoning トークンの「思考量」を単独軸として | Codex のみ計上で agent 跨ぎ比較不能。Claude は API 構造的に分離計上できない。さらにユーザが介入できる項目ではなく運用判断に使えない |
| ツール利用パターンを単独軸として | `tokens_per_tool_use` 自体に良し悪しは無い。token 効率の補助指標として残し、独立軸では立てない |
| ghost / subagent セッションの効率 | 実体のないセッション。生のメトリクスは残すが PR 単位集計から除外 |

---

## メトリクス → 観察軸 逆引き表

| メトリクス | 主用途軸 |
|---|---|
| `agent_pr_total_tokens` / `agent_pr_fresh_tokens` / `agent_pr_per_million_tokens` / `agent_pr_tokens_per_session` | 1. トークン効率 |
| `agent_pr_tool_use_total` / `agent_pr_tokens_per_tool_use` | 1. トークン効率（補助） |
| `agent_pr_input_tokens_total` / `agent_pr_output_tokens_total` / `agent_pr_cache_write_tokens_total` / `agent_pr_cache_read_tokens_total` / `agent_pr_reasoning_tokens_total` | 1. トークン効率（内訳） |
| `agent_session_input_tokens_total` / `agent_session_output_tokens_total` / `agent_session_cache_write_tokens_total` / `agent_session_cache_read_tokens_total` / `agent_session_reasoning_tokens_total` | 1. トークン効率（session 粒度の内訳） |
| `agent_session_mid_session_msgs_total` / `agent_session_ask_user_question_total` | 2. 開発生産性 |
| `agent_pr_review_comments` / `agent_pr_changes_requested` | 2. 開発生産性 |
| `agent_concurrent_sessions_avg` / `agent_concurrent_sessions_peak` | 2. 開発生産性（並列上限） |
| `agent_session_started_timestamp_seconds` / `agent_session_ended_timestamp_seconds` | 並列上限の区間計算 |
| `agent_session_is_subagent` / `agent_session_is_ghost` | フィルタ条件 |
| `agent_session_pr_merged` / `agent_session_pr_review_comments` / `agent_session_pr_changes_requested` | フィルタ / 2. 開発生産性の session 粒度 |

---

## メトリクスカタログ

メトリクス名・型・ラベルの一覧。SoR は append-only な `events` テーブル（`agent.session.started` / `agent.session.ended` / `agent.transcript.scanned` / `agent.pr.observed` の 4 イベント）で、各メトリクスはイベントの属性を `sessions` / `transcript_stats` / `pr_metrics` などの **派生 VIEW** が latest-wins で surface した値と 1:1 で対応する（イベント schema は `docs/spec.md ## SQLite データモデル`、VIEW 定義は `internal/syncdb/schema/schema.sql` を参照）。型は時系列指標としての性格を表す（`counter` = 単調増加、`gauge` = 瞬時値）。クライアント間でのイベント転送は OTLP/HTTP Logs（`POST /v1/logs`）だが、Grafana 出力は VIEW を直接 SQL で参照する（Prometheus / OpenMetrics 形式の pull endpoint は備えない）。

各メトリクス群とソースイベントの対応:

| メトリクス群 | ソースイベント | surface する VIEW |
|---|---|---|
| `agent_session_{tool_use,*_tokens,mid_session_msgs,ask_user_question,is_ghost}` 系 + `model` | `agent.transcript.scanned`（snapshot） | `transcript_stats` |
| `agent_session_started_timestamp_seconds` + ラベル（`repo` / `branch` / `agent_version` / `parent_session_id` 等） | `agent.session.started` | `sessions` |
| `agent_session_ended_timestamp_seconds` / `end_reason` | `agent.session.ended` | `sessions` |
| `agent_session_pr_{merged,review_comments,changes_requested}` + `pr_url` | `agent.pr.observed`（snapshot） | `sessions` |
| `agent_pr_*` 系 | 上記の集約 | `pr_metrics` |
| `agent_concurrent_sessions_*` | `started` / `ended` の区間重なり | `session_concurrency_*` |

snapshot 系（`agent.transcript.scanned` / `agent.pr.observed`）は同一セッションで複数行が events に残り、VIEW が `MAX(local_sequence)` で最新を採る。

### 外部 backend 上の representation

各メトリクスは外部 observability backend 上で 2 通りの representation のどちらかで再現する（join 不可ゆえに 2 表現へ分けた帰結は親 issue [0040] を参照）。カタログ各表の **representation** 列がメトリクスごとの分類を示す。

- **gauge** — client がローカル `pr_metrics` VIEW を集約し、PR 単位の pre-aggregated 値を OTLP Metrics gauge（last-value）として送る（[0043]）。`agent_pr_*` 系がこれにあたり、backend では同名の gauge series をそのまま参照する（range 集計は `last` を取る前提。naive な SUM は二重計上になる）
- **event-level** — raw events（OTLP Logs）の attribute を backend の measure / 次元タグに昇格し（attribute の意味分類は [0044] / `docs/spec.md`）、backend formula（count / sum / 区間計算）で算出する。session 単位・同時実行数の各メトリクスがこれにあたる

event-level の backend formula 例（Datadog の log-based metric。`agent.transcript.scanned` ログから token 流量を出す場合）:

```
metric 名 : agent.session.input_tokens
集計      : sum of @input_tokens   （measure）
group by  : @coding_agent, @model, @repo   （次元タグ）
```

snapshot 系（`transcript.scanned` / `pr.observed`）は同一 session で複数ログが残るため、event-level で count / sum を取る際は latest-wins の重複を考慮する（PR 単位の正しい集約は join が要るので gauge 側で担保する。だからこその 2 表現分け）。

### セッション単位（`sessions` + `transcript_stats` VIEW）

すべてのセッション単位メトリクスは次の共通ラベル集合を持つ:

| ラベル | 値の例 |
|---|---|
| `coding_agent` | `claude` / `codex` |
| `session_id` | エージェント発行の UUID |
| `model` | セッション内で最後に観測した model（例 `claude-sonnet-4-6`） |
| `agent_version` | agent 自身が報告するバージョン（取得不能なら空） |
| `repo` | `org/repo` |
| `branch` | ブランチ名 |
| `pr_url` | PR URL（未作成なら空） |
| `task_type` | `feat` / `fix` / `docs` / `chore` / 空 |
| `parent_session_id` | 親セッション ID（top-level なら空） |
| `end_reason` | SessionEnd hook の終了理由（Codex は `stop` 固定） |

| メトリクス名 | 型 | 値 | representation | 説明 |
|---|---|---|---|---|
| `agent_session_tool_use_total` | counter | int | event-level | セッション内のツール呼び出し回数 |
| `agent_session_mid_session_msgs_total` | counter | int | event-level | セッション中の人間追加メッセージ数（`tool_result` のみで構成されるエントリは除外） |
| `agent_session_ask_user_question_total` | counter | int | event-level | AskUserQuestion ツール発火回数（Codex は常に 0） |
| `agent_session_input_tokens_total` | counter | int | event-level | 入力トークン |
| `agent_session_output_tokens_total` | counter | int | event-level | 出力トークン |
| `agent_session_cache_write_tokens_total` | counter | int | event-level | プロンプトキャッシュへの書き込みトークン |
| `agent_session_cache_read_tokens_total` | counter | int | event-level | プロンプトキャッシュからの読み込みトークン |
| `agent_session_reasoning_tokens_total` | counter | int | event-level | reasoning トークン（Claude は常に 0。Anthropic API が thinking を `output_tokens` から分離しないため） |
| `agent_session_started_timestamp_seconds` | gauge | int | event-level | セッション開始時刻（Unix epoch） |
| `agent_session_ended_timestamp_seconds` | gauge | int | event-level | セッション終了時刻（Unix epoch、未設定なら 0） |
| `agent_session_is_subagent` | gauge | 0\|1 | event-level | `parent_session_id` が非空なら 1 |
| `agent_session_is_ghost` | gauge | 0\|1 | event-level | transcript に user 相当エントリが 0 件なら 1 |
| `agent_session_pr_merged` | gauge | 0\|1 | event-level | セッションの PR が merged なら 1 |
| `agent_session_pr_review_comments` | gauge | int | event-level | セッションの PR のレビューコメント数 |
| `agent_session_pr_changes_requested` | gauge | int | event-level | セッションの PR の CHANGES_REQUESTED レビュー数 |

### PR 単位の集約（`pr_metrics` VIEW）

`pr_url != ''` AND `is_subagent = 0` AND `is_merged = 1` AND `is_ghost = 0` AND `repo NOT IN (...)`（運用ノイズリポジトリの除外、列挙は `internal/syncdb/schema/schema.sql`）でフィルタした PR スコープの集約値。

ラベル: `pr_url`, `coding_agent`, `model`

representation は全行 **gauge**（client がローカル `pr_metrics` VIEW を集約し OTLP Metrics gauge として送る。[0043]）。backend では同名 gauge series を `last` で参照する。

| メトリクス名 | 型 | 値 | representation | 説明 |
|---|---|---|---|---|
| `agent_pr_session_count` | gauge | int | gauge | PR に寄与したセッション数 |
| `agent_pr_tool_use_total` | counter | int | gauge | PR 内全セッションのツール呼び出し合計 |
| `agent_pr_mid_session_msgs_total` | counter | int | gauge | PR 内全セッションの mid_session_msgs 合計 |
| `agent_pr_ask_user_question_total` | counter | int | gauge | PR 内全セッションの AskUserQuestion 合計 |
| `agent_pr_input_tokens_total` | counter | int | gauge | PR 全セッションの input トークン合計 |
| `agent_pr_output_tokens_total` | counter | int | gauge | PR 全セッションの output トークン合計 |
| `agent_pr_cache_write_tokens_total` | counter | int | gauge | PR 全セッションの cache write トークン合計 |
| `agent_pr_cache_read_tokens_total` | counter | int | gauge | PR 全セッションの cache read トークン合計 |
| `agent_pr_reasoning_tokens_total` | counter | int | gauge | PR 全セッションの reasoning トークン合計 |
| `agent_pr_total_tokens` | counter | int | gauge | input + output + cache_write + cache_read + reasoning の合計 |
| `agent_pr_fresh_tokens` | counter | int | gauge | input + output + cache_write + reasoning（`cache_read` を除外、実質的な作業量に近い） |
| `agent_pr_review_comments` | gauge | int | gauge | PR レビューコメント数 |
| `agent_pr_changes_requested` | gauge | int | gauge | CHANGES_REQUESTED レビュー数 |
| `agent_pr_tokens_per_session` | gauge | float | gauge | PR 内の平均セッショントークン（派生） |
| `agent_pr_tokens_per_tool_use` | gauge | float | gauge | PR 内のツール 1 回あたりトークン（派生） |
| `agent_pr_per_million_tokens` | gauge | float | gauge | 100 万 token あたりに完了した PR 数（効率の逆数指標、派生） |

### 同時実行数（`session_concurrency_daily` / `session_concurrency_weekly` VIEW）

トップレベルセッション（subagent / ghost / 運用ノイズリポジトリを除外）の同時実行数を時間軸で集約。

ラベル: `coding_agent`, `bucket`（`day` または `week`）, `bucket_start`（ISO8601 日付）

| メトリクス名 | 型 | 値 | representation | 説明 |
|---|---|---|---|---|
| `agent_concurrent_sessions_avg` | gauge | float | event-level | bucket 内の平均同時実行数 |
| `agent_concurrent_sessions_peak` | gauge | int | event-level | bucket 内のピーク同時実行数 |

同時実行数は `started` / `ended` の区間重なりから算出するため、backend では `agent_session_started/ended_timestamp_seconds`（event-level）の生ログに対する区間計算で再現する。単純な count / sum では出ない点に注意（`pr_metrics` gauge には含めない）。

---

## 収集パイプライン

メトリクス数は 30+ あるが、収集経路は **4 カテゴリ** に集約される。各メトリクスがどのカテゴリに属するかは末尾の逆引き表を参照。コードファイル参照は調査の起点として記載する（コード変更で関数名・行番号はずれるため、シグネチャは git で追うこと）。

### 全体図

```
┌──────────────────────────┐
│ Hook (event-driven)      │── A ──┐
└──────────────────────────┘       │
                                   ▼
                           session-index.jsonl ─┐
                                   ▲            │
┌──────────────────────────┐       │            │
│ gh pr list / pr view     │── C ──┘            │
└──────────────────────────┘                    ▼
                                          ┌─────────┐
┌──────────────────────────┐              │ sync-db │── INSERT OR IGNORE ──→ SQLite
│ transcript JSONL(.zst)   │── B ────────→│         │   (events;
└──────────────────────────┘              └─────────┘    sessions /
                                                              │       transcript_stats
                                                          D (VIEW)        は VIEW)
                                                              ↓
                                                      pr_metrics 等
```

A と C はどちらも session-index.jsonl に書き込むが、A は hook が即時に書く揮発しない事実（時刻・branch・cwd 等）、C は外部 API で後から確定する状態（PR 状態）を担う。B は session-index.jsonl を経由せず transcript を sync-db が直接読み、append-only な `events` テーブルに deterministic な `event_id`（content hash）で `INSERT OR IGNORE` 追記する（同一内容の再 sync は dedup される）。D は events に蓄積した値を VIEW（`sessions` / `transcript_stats` / `pr_metrics` 等）で組み合わせるだけで新しい I/O は発生しない。

### A. Hook 書き込み

イベント駆動で session-index.jsonl に 1 行追加 / 該当行を読み直して更新。ローカル情報（hook input、env、git）のみ扱い、ネットワーク I/O や外部コマンド呼び出しは原則しない（hook hot path を守るため）。

| 項目 | 内容 |
|---|---|
| 収集主体 | `internal/hook/{sessionstart,sessionend,stop}.go` |
| 書き込み先 | `<DataDir>/session-index.jsonl` |
| タイミング | SessionStart / SessionEnd / Stop hook の各発火時 |
| 該当メトリクスのソース | `started_timestamp_seconds`, `ended_timestamp_seconds`, `parent_session_id`（→ D で `is_subagent` 派生）, ラベル群（`coding_agent`, `repo`, `branch`, `agent_version`, `user_id`, `end_reason`） |

#### 代表例: `agent_session_started_timestamp_seconds`

1. SessionStart hook が `internal/hook/sessionstart.go: RunSessionStart` で発火
2. `time.Now().Format("2006-01-02 15:04:05")` を `timestamp` フィールドに書き込む
3. 同じ JSON エントリに `session_id` / `cwd` / `parent_session_id` / transcript path / `resolved user_id` / `extractGitInfo` で取得した `repo`・`branch` を詰めて session-index.jsonl に append
4. sync-db が `agent.session.started` イベントの `started_at` 属性として events に追記。`sessions` VIEW が `timestamp` カラムとして surface し、Grafana 出力時に Unix epoch に変換

git 情報の取得は `git rev-parse --is-inside-work-tree` で git 配下を確認 → `git remote get-url origin` → `git branch --show-current` の順で best-effort。git 外で起動した場合は両方空文字列で続行する（hook を失敗させない）。

#### 代表例: `ended_at`（`agent_session_ended_timestamp_seconds` の元データ）

- Claude: SessionEnd hook が `time.Now()` を書き込む
- Codex: SessionEnd 相当が無いので Stop hook 発火ごとに `ended_at` を **常に上書き** する（最後の Stop が事実上 SessionEnd）。プロセス kill された場合は backfill が rollout JSONL の最終 event timestamp で補完する（C にまたがる経路）

### B. Transcript パース

agent ごとに異なる会話ログ（Claude: `~/.claude/projects/**/<session_id>.jsonl` / Codex: `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl[.zst]`）を sync-db が後追いで読み、トークン・ツール・メッセージ系の集計値を `agent.transcript.scanned` イベントとして `events` テーブルに `INSERT OR IGNORE` 追記する（`transcript_stats` VIEW がそれを latest-wins で surface する）。

| 項目 | 内容 |
|---|---|
| 収集主体 | `internal/transcript/{claude,codex}.go` の `ParseClaude` / `ParseCodex` |
| トリガ | `agent-telemetry sync-db` 実行時（Stop hook → backfill → sync-db のチェーンで発火 / 手動 / cron） |
| 書き込み先 | SQLite `events` テーブル（`agent.transcript.scanned`）。`transcript_stats` は events 由来の VIEW |
| 該当メトリクスのソース | token 系全部（input / output / cache_write / cache_read / reasoning）, `tool_use_total`, `mid_session_msgs`, `ask_user_question`, `is_ghost`, `model` |

#### 代表例: `agent_session_input_tokens_total`（Claude / Codex で集計方式が異なる）

**Claude (`ParseClaude`)**
- transcript JSONL を 1 行ずつ走査
- `type:"assistant"` のエントリの `message.usage.input_tokens` を **加算** していく（per-message → 合計）
- 同様に `output_tokens` / `cache_creation_input_tokens` / `cache_read_input_tokens` を別フィールドに加算
- `reasoning_tokens` は **0 固定**（Anthropic API が thinking を output から分離しない API 構造的制約）

**Codex (`ParseCodex`)**
- rollout JSONL を（`.zst` の場合は `klauspost/compress/zstd` でストリーム解凍しつつ）走査
- `event_msg.payload.type == "token_count"` イベントの **最終出現値をそのまま採用**（イベント自体が累積値を保持するので加算してはいけない）
- `cache_write_tokens` は **0 固定**（OpenAI API が cache 書き込みを分離報告しない）
- `token_count.reasoning_output_tokens` を `reasoning_tokens` に転写

**コードを読む際の最大の落とし穴**: 「Claude は加算 / Codex は最終値採用」の差。どちらもセッション全体の合計を意味するが、ロジック上は逆方向。修正時は agent 別に分けて考える。

#### 代表例: `agent_session_tool_use_total`

- Claude: assistant message 内の `content[].type == "tool_use"` エントリ数の合計
- Codex: response item の `type == "*tool_call"`（MCP / Bash / apply_patch 全て含む）の合計

#### 代表例: `is_ghost`

transcript に `type:"user"`（Claude）/ `payload.type == "user_message"`（Codex）が **0 件なら 1**。Claude が編集スナップショット用に作る user message を含まない JSONL を「ghost session」として除外するためのフラグ。`pr_metrics` VIEW のフィルタに使う。

### C. 外部 API → session-index 書き戻し

`gh` CLI を呼んで GitHub から PR メタデータを取り、session-index.jsonl の該当行に書き戻す。Stop hook での **early binding（pin）** と backfill での **後追い回収** の二段構え。Phase 2 のメタ更新だけ 1h cadence で走る。

| 項目 | 内容 |
|---|---|
| 収集主体 | `internal/hook/stop.go: pinPRForSession` + `internal/backfill/backfill.go` |
| 外部呼び出し | `gh pr list --head <branch> --author @me --state all --json url,title,state,comments,reviews --limit 1`（URL 解決） / `gh pr view <url> --json ...`（URL 既知のメタ更新） |
| 書き込み先 | session-index.jsonl の `pr_urls`, `pr_pinned`, `is_merged`, `review_comments`, `changes_requested`, `pr_title`, `backfill_checked` |
| タイミング | Stop hook 発火時（pin） / `agent-telemetry backfill` Phase 1（URL 取得・毎回） / Phase 2（メタ更新・前回から `MetaCheckInterval = 1h` 経過時のみ） |
| 該当メトリクスのソース | `pr_url` ラベル, `agent_session_pr_merged`, `agent_session_pr_review_comments`, `agent_session_pr_changes_requested`（および D で集約される `agent_pr_*`） |

#### 代表例: `pr_url` ラベル

1. SessionStart hook が `pr_urls: []` で初期化（A）
2. Stop hook が `pinPRForSession` で `gh pr list --head <branch> --author @me --limit 1` を 8s タイムアウトで実行。1 件取れたら `pr_urls = [url]` かつ `pr_pinned = true`。同じレスポンスから `is_merged` / `review_comments` / `changes_requested` / `title` も seed する
3. PostToolUse hook が `tool_response` JSON を正規表現 `https://github\.com/[^/\s]+/[^/\s]+/pull/\d+` でスクレイプして append（ただし `pr_pinned == true` のセッションは `sessionindex.Update` 内で弾かれるので no-op）
4. `agent-telemetry backfill` Phase 1 が `pr_urls` 空の sessions を `(repo, branch)` でグループ化し、各グループ 1 回だけ並列（最大 8）で `gh pr list` を叩いて未解決ぶんを埋める。永続的に PR が無いブランチ（main/master 等）は `backfill_checked = true` で永続スキップ
5. Phase 2 が 1h cadence で `gh pr view <url>` を叩いて merge 状態と review 数を更新（URL 取得はしない）

`pr_pinned` フラグは「この URL は確定済みなので以降の append/上書きを禁止する」というロックの役割。同じブランチを再利用して別 PR を作った場合に過去 session が後発の PR に誤接続するのを防ぐ。

#### 代表例: `agent_session_pr_merged`

**hook では確定しない**。Stop hook の pin 段階で seed されるが、PR 作成直後は `state == "OPEN"` で 0。**backfill Phase 2 でしか更新されない** ため、マージ直後にダッシュボードに反映されるまで最大 1h 遅延する。これを縮めたい場合は `agent-telemetry backfill --recheck` を手動実行する。

### D. SQL 派生

A〜C で集めた値を SQLite VIEW で集計・派生させる。元データを増やさず、見せ方を増やす層。マテリアライズせず、Grafana がクエリした瞬間に評価する。

| 項目 | 内容 |
|---|---|
| 定義場所 | `internal/syncdb/schema.sql`（埋め込み・SHA256 ハッシュで管理） |
| 評価タイミング | Grafana / 手動クエリ実行時 |
| 該当メトリクス | `is_subagent`, `agent_pr_total_tokens`, `agent_pr_fresh_tokens`, `agent_pr_session_count`, `agent_pr_tokens_per_session`, `agent_pr_tokens_per_tool_use`, `agent_pr_per_million_tokens`, `agent_concurrent_sessions_{avg,peak}` ほか PR 集約系全般 |

#### 代表例: `agent_pr_total_tokens`

`pr_metrics` VIEW 内で:

```sql
SUM(input_tokens + output_tokens + cache_write_tokens + cache_read_tokens + reasoning_tokens)
  AS total_tokens
GROUP BY pr_url, coding_agent, model
```

入力は B 由来（`transcript_stats`）、フィルタは A・C 由来（`is_merged = 1`, `is_subagent = 0`, `is_ghost = 0`, 運用ノイズリポジトリ除外）。`agent_pr_fresh_tokens` は `cache_read_tokens` だけを除いた同形式。

#### 代表例: `agent_concurrent_sessions_avg` / `peak`

`session_concurrency_daily` / `session_concurrency_weekly` VIEW で `sessions.timestamp` と `sessions.ended_at` の区間重なりから算出。`ended_at` が空のセッションは現在時刻で打ち切る扱いのため、進行中セッションを含む時間帯は値が膨らむ。subagent / ghost / 運用ノイズリポジトリを除外。

#### 代表例: `agent_session_is_subagent`

1 行の派生: `CASE WHEN parent_session_id != '' THEN 1 ELSE 0 END`。`parent_session_id` は A（SessionStart hook が書き込み）由来。ただし Claude の Task サブエージェントは SessionStart hook を発火しないため、**実運用ではほぼ常に 0** になる。

---

## カテゴリ逆引き

| メトリクス / フィールド | カテゴリ | 主ソース |
|---|---|---|
| `agent_session_started_timestamp_seconds` | A | session-index.jsonl（SessionStart hook） |
| `agent_session_ended_timestamp_seconds` | A | session-index.jsonl（SessionEnd / Stop hook） |
| `agent_session_input/output/cache_write/cache_read/reasoning_tokens_total` | B | transcript（ParseClaude/ParseCodex） |
| `agent_session_tool_use_total` | B | transcript |
| `agent_session_mid_session_msgs_total` | B | transcript |
| `agent_session_ask_user_question_total` | B | transcript（Claude のみ、Codex は 0 固定） |
| `agent_session_is_ghost` | B | transcript（user message 件数） |
| `agent_session_pr_merged` | C | session-index.jsonl（backfill Phase 2） |
| `agent_session_pr_review_comments` | C | session-index.jsonl（backfill Phase 2） |
| `agent_session_pr_changes_requested` | C | session-index.jsonl（backfill Phase 2） |
| `pr_url` ラベル | C | session-index.jsonl（Stop hook pin → backfill Phase 1 fallback） |
| `agent_session_is_subagent` | D | `parent_session_id != ''` の SQL 派生（A の値が入力） |
| `agent_pr_*` 系すべて | D | `pr_metrics` VIEW（A/B/C を束ねる集約） |
| `agent_concurrent_sessions_{avg,peak}` | D | `session_concurrency_*` VIEW（A の `timestamp` / `ended_at` から区間重なり計算） |

カテゴリの境界は **「どこから来るか」だけ見ると曖昧になる**（例: `is_subagent` の元データ `parent_session_id` は A だが、メトリクスとしては D の派生）。**「どの層が値を確定させるか」** で分類するのが認識の整理として正確。Hook（A）/ Transcript（B）/ External API（C）/ SQL（D）と読み替えるとブレない。

---
decision_type: design
affected_paths:
  - internal/backfill/backfill.go
  - internal/hook/stop.go
  - internal/hook/sessionend.go
  - internal/sessionindex/sessionindex.go
  - internal/sessionindex/update.go
  - internal/syncdb/syncdb.go
  - internal/doctor/doctor.go
  - cmd/agent-telemetry/main.go
tags: [backfill, hooks, stop-hook-cost, rate-limit, blocking-ux]
---

# Stop hook での高頻度 gh 実行が rate limit と応答ブロッキングを引き起こす

Created: 2026-05-26

## 概要

`~/.claude/settings.json` の Stop フックに登録された `agent-telemetry hook stop --agent claude` が、毎ターン `backfill + sync-db` を **同期的に** 実行する。`backfill` は `session-index.jsonl` 内の `backfill_checked != true` かつ `pr_urls` 未設定のセッションに対して `gh pr list` / `gh pr view` を呼ぶ。

ここから、同じ根（**高頻度イベントである Stop hook で gh をブロッキング実行している**）に由来する 2 つの症状が出ている。本 issue は両方をまとめて解く。

- **問題 1 — GitHub rate limit**: 未チェックセッションが溜まっていると Stop ごとに大量の gh 呼び出しがバーストし、GitHub secondary rate limit を頻繁にヒットする。
- **問題 2 — 応答ブロッキング**: `stop.go` は `exec.Command(...).CombinedOutput()` で backfill / sync-db の完了を待つ。Stop はユーザ応答サイクル上のブロッキングフックなので、gh 呼び出し（pin の `gh pr list` 8s timeout + backfill バッチ）が遅いとき、その間ユーザが次の入力に進めず待たされる。

## 根拠

### 計測値（再現環境）

- `session-index.jsonl` の総セッション数: 3632
- うち `backfill_checked != true` & `pr_urls` 未設定: 2390
- → Stop イベントごとに最大 2390 セッション由来の `(repo, branch)` group に対して `gh pr list` が候補になり得る

### 影響

**問題 1（rate limit）**

- primary REST API quota は 6/5000 程度しか使っていないのに secondary rate limit が発動する（総量ではなくバーストが原因）
- Claude Code セッション中の `gh pr list` / `gh pr create` が連鎖的に失敗する
- 発見の経緯: 別セッションで `gh pr list` が secondary rate limit に当たり、原因を辿ったところ Stop フック内の `agent-telemetry` が原因と判明

**問題 2（ブロッキング）**

- Stop hook が同期実行のため、backlog が溜まっているとターン終了のたびに backfill バッチ完了まで待たされ、次の入力に進めない体感遅延が出る
- **gh の per-call レイテンシ自体が floor**: cap で件数を絞っても、同期のままなら N 件 × (gh CLI 起動 + ネットワーク RTT) が wall-clock で積む。**cap は rate limit（バースト）用、async は blocking 用で役割が直交**——cap では blocking 時間は消えない
- **pin が同期かつ最初に無条件で走る**: `RunStop`(`stop.go:54`) は backfill より前に `pinPRForSession`（同期 `gh pr list`、8s timeout）を呼ぶ。よって backfill をいくら async/cap/GC しても、**un-pinned セッションの pin レイテンシ floor は一切減らない**。pin を「ダッシュボード即時性のため同期に残す」正当化は成り立たない（ダッシュボードはターン中に見るものではなく、即時 vs 数秒後の UX 価値が無い）
- agent 間制約: Claude には低頻度の `SessionEnd` があるが Codex には無く（`setup.go` 参照）、Stop が事実上の SessionEnd を兼ねる。よって「Stop から退避する」だけでは Codex を救えず、Stop に留めたままブロッキングを解く解法も必要になる

### 既存 issue との関係

[0035-bug-backfill-no-pr-infinite-retry.md](0035-bug-backfill-no-pr-infinite-retry.md) は「PR 未作成ブランチが毎 Stop で probe される」問題で、24h horizon で markChecked する方向で対応方針が確定済み。ただし 0035 が解決しても以下は残るため本 issue が必要:

- markChecked がまだ立っていない期間（特に backlog 消化中）に Stop hook が毎ターン大量の `gh` を叩く問題は別途レート制御が必要
- そもそも「Stop フックという高頻度イベントで backfill を走らせる」設計選択自体が再考対象

## 現在の PR 収集戦略（前提整理）

PR URL とメタデータの収集経路は **4 つ** に分かれている。本 issue の対応方針を考えるうえで、どこを削ると何を失うのかを整理しておく。

| # | 経路 | コード | 呼び出すコマンド | 頻度 | 役割 |
|---|---|---|---|---|---|
| 1 | Stop hook の pin | `internal/hook/stop.go:104` (`pinPRForSession`) | `gh pr list --head <branch> --author @me --state all --limit 1` | Stop ごと、ただし `pr_pinned` 済みは skip | **early binding**: ブランチから PR を 1 件確定して `pr_urls = [<url>]` で固定。同一呼び出しで `pr_meta`（state / comments / reviews）もシード |
| 2 | PostToolUse の regex scrape | `internal/hook/posttooluse.go:24` (`RunPostToolUse`) | なし（tool_response を正規表現マッチ） | tool 実行ごと（Codex のみ） | Codex 用 fallback: `gh pr create` の stdout などから URL を拾って `pr_urls` に append。pinned セッションでは [[update]] が no-op になる |
| 3 | Backfill Phase 1 | `internal/backfill/backfill.go:166` (`runURLBackfill`) | `gh pr list --head <branch> --author @me --state all --limit 1`（`(repo, branch)` 単位） | Stop ごと（候補があれば） | **late binding fallback**: Stop hook 時点で PR 未作成だったセッションを `(repo, branch)` でグループ化して後追い解決 |
| 4 | Backfill Phase 2 | `internal/backfill/backfill.go:263` (`runMetaBackfill`) | `gh pr view <url> --json ...`（PR URL 単位） | Stop ごと、ただし `last_meta_check` から `MetaCheckInterval = 1h` 経過時のみ | `is_merged` / `review_comments` / `changes_requested` / `pr_title` の **メタ更新**。pinned セッションも対象 |

補助経路: `agent-telemetry update <session_id> <url>` / `update --by-branch` は手動経路で、`gh` は叩かない。

### この issue の rate limit を生んでいる経路

- **経路 3 (Phase 1)** が主犯。`backfill_checked != true` かつ `pr_urls` 未設定のセッションを毎 Stop で `(repo, branch)` グループとして再 probe する。0035 が解決すれば 24h horizon で markChecked が立つので候補数は急減するが、**backlog 消化中** や **24h 以内の新規セッションが大量に流入した直後** は本 issue の対応が必要。
- **経路 4 (Phase 2)** は 1h スロットルが効いているため、本 issue の rate limit には寄与しにくい。ただし backlog 消化で `pr_urls` がまとめて埋まった直後の Phase 2 は対象 PR 数が一気に増えるので、Phase 1 と同様に呼び出し数の上限を検討する価値はある。
- **経路 1 (pin)** は 1 セッション 1 回かつ pinned 済みは skip なので、定常状態では Stop ごとに 1 回しか叩かれない。0035 とは独立に効くので残してよい。
- **経路 2 (regex)** は `gh` を呼ばないのでコスト無関係。

### 制約: 経路 4 (Phase 2) は merged-PR metrics の唯一の事後 refresh 経路

`is_merged` / `review_comments` / `changes_requested` / `pr_title` を書く `UpdatePRMeta` は経路 1 (`stop.go:158`) / 経路 3 (`backfill.go:236`) / 経路 4 (`backfill.go:346`) の 3 つから呼ばれるが、**URL 確定後にメタを再取得できるのは経路 4 (Phase 2) だけ**。経路 1 は `pr_pinned` で skip、経路 3 は `len(pr_urls)==0` のセッションのみ対象なので、一度 URL が紐づいたセッションには二度と触れない。

これがクリティカルなのは、**経路 1・3 が seed するのは作業中スナップショット**だから:

- pin / Phase 1 が走るのはコーディング作業中で、その時点で PR は通常 **まだマージされておらず**（マージはレビュー後＝後日）、レビューコメントも CHANGES_REQUESTED も 0。
- `pr_metrics` VIEW は **`is_merged = 1` を WHERE フィルタ**にしている（`schema.sql:150,167`、`metrics.md:41`）。
- → Phase 2 を止めると `is_merged` が 1 に上がらず、PR が集計母集団に入らない。失われるのは review 数だけでなく **`agent_pr_*` / `pr_metrics` ダッシュボード一式**。

Phase 2 の本質は「自セッションではなく、後続の任意のセッションの Stop で過去 PR をマージされるまで再チェックし続ける **cross-session refresh**」。よって高頻度 Stop で広く薄く回る現設計には合理性があり、**頻度・件数の制御は可だが経路自体を削ると metrics が壊れる**。打ち手 a（backfill を SessionEnd / 明示コマンドのみ）は、Codex に SessionEnd が無く、Claude でも SessionEnd 時点では大半が未マージという理由で、この制約と正面衝突する。

### 対応方針との関係

- 打ち手 **a** = 経路 3+4 を Stop hook から外す（経路 1 と 2 は残す）。
- 打ち手 **b** = 経路 3+4 を Stop hook で呼ぶがレート制御を入れる。
- 打ち手 **d** = `setup` のデフォルト hook を「Stop = 経路 1+2、SessionEnd = 経路 3+4」に分離する。

経路 1（pin）を残せば、ユーザが直近のセッションでダッシュボードを見るときに PR が既に紐づいているという UX は維持される。経路 3（Phase 1, URL の late binding）は遅延・スロットルしてもダッシュボードの新鮮度がせいぜい数分〜SessionEnd まで遅れるだけで損失は小さい。一方 **経路 4（Phase 2, メタ refresh）は性質上 cross-session かつ後日に効く**ため「遅延しても数分」ではなく、上記制約のとおり経路を残したうえで件数上限をかける形にする必要がある。

## 問題

| 観点 | 現状 | 課題 | 関連 |
|---|---|---|---|
| 実行頻度 | Stop ターンごとに backfill | 一日数十〜数百回。バーストになりやすい | 問題 1 |
| バッチサイズ | 候補全件（未 markChecked 全部） | バックログ消化中は 1 回で 2000+ | 問題 1 |
| レート制御 | なし | secondary rate limit を踏むまで気付けない | 問題 1 |
| 監視 | なし | `gh api rate_limit` の事前 check もなし | 問題 1 |
| 実行モデル | `CombinedOutput()` で同期実行 | gh 完了までユーザ応答サイクルがブロックされる | 問題 2 |
| backfill 多重起動 | 同期のため暗黙に直列化 | 単純に非同期化すると並行 backfill でバーストが悪化する（直列化が失われる） | 問題 1 × 2 |

## 対応方針

下記「確定: 収束アーキテクチャ」が主軸。以下の旧 a〜d は問題 1 単独前提の初期整理で、収束モデルに吸収・更新された（履歴として残す）:

- **a（Stop で backfill 起動しない）**: 部分採用。Stop は backfill を**同期実行しない**が、detached worker は **Stop から spawn する**（gh は worker 側で async）。「SessionEnd のみ」ではない（Codex に無いため）。
- **b（cap・スロットル）**: 採用。「確定: gh 呼び出し上限（cap）」節で具体化。
- **c（事前 rate_limit probe）**: **不採用**（「window 絞り込みが主レバー」節のとおり over-engineering）。
- **d（Stop と SessionEnd を分離した opt-in hook）**: **不要**。収束モデルでは Stop が worker を spawn するだけで両 agent 対応でき、**hook 登録構成は現状維持**（settings.json 移行不要）。

### 確定: 収束アーキテクチャ（gh 完全 async + 2 段ロック + single-flight）

中核の発見: **gh を完全に async + throttle 付き single-flight にすると低頻度 hook が不要になり、Codex に `SessionEnd` が無い制約が設計上消える**（throttle が「たまに走る」を担保するのでトリガが毎 Stop でよい）。結果、Claude / Codex のアーキは収束する。

ランタイムモデル（両 agent 共通）:

```
Stop hook（毎ターン、同期部分は数 ms で return）
├─ [同期] ローカル書き込みのみ（per-file flock を µs〜ms 保持）
│    • Codex: ended_at 更新（Stop が de-facto SessionEnd）
└─ [spawn] hook が `agent-telemetry backfill --detach` を exec + setsid して即 return
        worker（detached, std→/dev/null or log, no-wait）
        ├─ global single-flight を try-lock → 取れなければ即 exit（待たず譲る）
        ├─ gh 群（ロック外）: pin(現セッション) + Phase1(late URL) + Phase2(open PR meta)
        │    → 結果をメモリ集約。cap / GC 第1層第2層 / Phase2 throttle をここで適用
        ├─ per-file flock → 最新 index を re-read → 集約差分を merge/apply → 1 回の WriteAll → 解放
        ├─ sync-db（ローカル、no gh。ここも worker 側）
        └─ global lock 解放（flock はプロセス死で自動解放＝stale lock 無し）
```

決定事項:
- **worker 起動 = hook が spawn**（`stop` subcommand が `backfill --detach` を exec + setsid）。責務分離が素直。同期ホットパス総コスト＝ローカル書き込み + spawn ≈ 10ms 未満。
- **pin は worker 内に吸収**（pin と Phase1 はほぼ同じ `gh pr list`）。同期パスから gh が完全に消える。
- **sync-db も worker 末尾へ**（問題 2 の sync-db 同期コストも解消）。
- **Claude の worker トリガは Stop のみ**（Codex と対称・実装共通化）。SessionEnd は `ended_at` 書き込みのみに留め、worker トリガには使わない。最終状態の確定は最後の Stop に委ねる。
- **single-flight は同時実行制御であり、頻度制御ではない**。Stop が連続した場合に毎回 worker が走り続けないよう、worker 側で短い cooldown を持つ。cooldown skip でも Stop 同期パスは spawn 後に待たない。

2 つのロック（スコープが異なる。混同しない）:

| ロック | スコープ | 目的 | 方式 |
|---|---|---|---|
| per-file flock | agent ごとの `session-index.jsonl`（Claude / Codex は別ファイル） | 書き込み整合性（ロスト更新防止） | append も worker も共有。書き込み時のみ ms 保持 |
| global single-flight | 全 agent 共有の 1 個（例 `~/.agent-telemetry/backfill.lock`） | gh バースト制御（rate limit 残余 1） | try-lock-skip。取れなければ worker 即 exit |

global を agent 横断にするのは gh secondary rate limit が**アカウント共有**だから。Claude と Codex の worker が同時に gh を撃たない。flock はプロセス死で自動解放されるので worker が kill されても stale にならない。

収束後に残る agent 差分（これだけ）:

| 項目 | Claude | Codex |
|---|---|---|
| `ended_at` 書き込み | SessionEnd hook（1 回） | Stop で毎回上書き + rollout 復元 |
| worker トリガ | Stop のみ | Stop のみ |
| PostToolUse（経路 2 regex scrape） | 不要 | あり（no gh、書き込みは flock 共有） |
| backfill / GC / cap / lock / worker | 完全共通 | 完全共通 |

移行 drain（残余 2）: **専用コマンド `agent-telemetry backfill --gc` を deploy 後 1 回手動実行**して既存 backlog（再現環境 2390）を horizon / 第1層で一括 markChecked（cap 無しの一括パス）。通常 worker の cap とは別経路。`doctor` から案内する。

### 確定: gh 呼び出し上限（cap）の方針

打ち手 b の「上限」を具体化したもの。**cap は Phase 1 / Phase 2 の両方（or 1 起動全体）に効くグローバル上限**として置く。「Phase 2 のみ」では不十分——理由が Phase ごとに異なる:

- **Phase 1 (`runURLBackfill`)**: candidate は別途の GC（第1層=デフォルトブランチ即時除外、第2層=`COALESCE(ended_at, timestamp)` 基準の freshness 窓。詳細は [0035](0035-bug-backfill-no-pr-infinite-retry.md)）で steady state は bound される。cap が守るのは **非定常**——backlog 消化直後・新規セッション大量流入直後（並列 worktree 等）。ここは窓の内側で起きるので GC では防げず、cap が無いと rate limit が再発する。
- **Phase 2 (`runMetaBackfill`)**: ターゲット（`pr_urls` を持つ全 URL）は **単調増加**し、1h スロットルは間隔を絞るだけで一度走ると全 URL に `gh pr view` を撃つ。cap に加えて構造対策として **`is_merged = true`（terminal）を refresh 対象から除外**し、「open な PR のみ refresh」へ縮める（現状 `backfill.go:271-281` は is_merged を見ず全件 re-check）。

cap 実装時の付帯事項（starvation 回避）:

- **Phase 1**: PR が付いた可能性の高い順＝ **newest-first で N 件**。あふれた分は次 Stop に回る（窓内なのでいずれ処理される）。
- **Phase 2**: 現状 `last_meta_check` は State にグローバル 1 個しかなく、単純 cap だと毎回同じ先頭 N 件だけ refresh して残りが永久に更新されない。**per-URL（or per-session）の last-meta-check を持たせ oldest-checked-first で N 件** 回す（cap 導入とセットの設計項目）。

### 確定: 問題 2 の解決原則 — 同期ホットパスに gh をゼロにする

問題 2 の解は「backfill を async 化する」ではなく **「Stop の同期部分はローカル書き込み（sub-ms）だけにし、pin を含む全 gh を fire-and-forget に出す」**。

- cap（rate limit 用）と async（blocking 用）は **役割が直交**。cap で件数を絞っても同期実行なら gh per-call レイテンシが積むため、blocking は cap では消えない。
- **pin も async 側へ**。pin を同期に残す正当化（ダッシュボード即時性）は成り立たない（前述）。同期に残してよいのは `ended_at` / メタのローカル書き込みのみ。
- 「同期のまま速い共通パス（throttle skip 時のみ速い）」案は、実際に走る時に gh レイテンシが積むので不採用。gh を含む処理は throttle 有無に関わらず async/detached に出す。

### 確定: rate limit 対策は「window 絞り込み」が主レバー。共有予算管理は不要

secondary rate limit は総量でなく**バースト/同時実行**で発火する。対策の本質は「自分の gh 発行を小さく保つ」ことであり、他消費者（statusline / ユーザの対話的 gh / 別 agent）の**予算を会計することではない**。

window を絞った後（GC 第1層+第2層 / Phase 2 terminal 除外 / cap / worker cooldown）の定常 footprint は pin 1 + Phase 1 0〜数件 + Phase 2 数件（1h スロットル）＝ 1 桁〜低 2 桁/worker run で、secondary 閾値に対して誤差。Stop が高頻度に連続しても cooldown と single-flight により worker run 自体も間引く。よって:

- **落とす（over-engineering）**: 共有予算の token bucket / 会計、および打ち手 **c**（`gh api rate_limit` 事前 probe）。事前 probe 自体が 1 API 呼び出しで「小さく保つ」方針と矛盾気味。残すなら「踏んだ後の reactive cooldown」を最小実装する程度。
- **残余 1 — 並列の concurrency 重複排除**: N 並列 worktree の同時 Stop は瞬間バーストが N 倍。ただし「予算管理」ではなく**安価な global single-flight / lock** の話。Phase 2 は既存の 1h State スロットルがプロセス間 dedup を概ね担うが、**Phase 1 はプロセス間スロットルが無い**のが残余。rate limit 観点なら window が小さい前提で保険レベルだが、**`session-index.jsonl` の書き込み整合性の観点では必須**（下記「並行書き込み」節）。
- **残余 2 — 移行直後の一括 drain（transient）**: 既存 backlog（再現環境 2390）は GC 適用前なので初回数 Stop が大バースト。bounded な一括 retire パスで対処する。予算管理とは別軸。

### 確定: `session-index.jsonl` の並行書き込み安全性とロック方式

async 方向（pin / backfill を fire-and-forget で並列化）に倒すと、新たに**ロスト更新**が顕在化する。`WriteAll`(`sessionindex.go:96`) は tmp+`os.Rename` で**アトミックだがロック（flock）が無い**。アトミック rename は「壊れたファイル」は防ぐが**ロスト更新は防がない**: プロセス 1 が ReadAll → 2 が ReadAll → 1 が WriteAll → 2 が WriteAll で 1 の変更が消える。同期 Stop では暗黙に直列化されていたものが、並列化で表面化する。

ただし**ファイル全体を素朴にロックすると並列実行待ちが出る**。これを避けるロック方式を採る:

- **① ロックは「書き込み」だけ。gh は絶対にロック外**: gh で取得した結果をメモリに溜め、最後にロックを取る。ロック内では必ず最新 index を re-read し、メモリ上の取得結果を差分として merge/apply してから 1 回だけ `WriteAll` する。ロック保持は数 ms（秒オーダーの gh とは無関係）。
- **② backfill の重い書き換えは try-lock = 取れなければ待たず skip**（blocking-wait ではない）。skip された候補は state に残り次の保持者が処理する。これで**並列実行待ちが発生しない**（「lock して待つ」ではなく「lock して譲る」）。
- **③ item ごと書き換えを廃止し 1 run = 1 回の WriteAll にバッチ**: 現状 `runURLBackfill` は結果 1 件ごとに `Update`＋`UpdatePRMeta`（各々フル ReadAll+WriteAll）を呼ぶ（`backfill.go:230,236`）。全変更をメモリ集約し最後に最新 index へ差分適用する。ロック保持窓を「件数回」→「1 回」に縮める（ロックと無関係に効く改善）。
- **④ SessionStart の append も同じ flock を µs だけ共有**: `O_APPEND` は原子的だが、rewrite の ReadAll→WriteAll 中に append が挟まると上書きで消える。同一 flock を取れば整合し、待ちは「進行中 rewrite 1 回（数 ms）」が最大。

結論: flock は使うが **try-lock-skip ＋ スコープを書き込みに限定 ＋ lock 内 re-read/merge/write ＋ バッチ化 ＋ append も共有 flock**。これで待ちは「数 ms 級の書き込み 1 回ぶん」に収まり、各自が払う gh レイテンシ（秒）に対して誤差。index-record GC で N を bound すれば O(N) 書き込み自体も小さく保てる。

## 受け入れ条件

問題 1（rate limit）:

- [ ] Stop hook は同期 backfill を呼ばず、detached worker の 1 起動あたりの `gh` 呼び出し数に明確な上限がある
- [ ] cap が Phase 1 / Phase 2 の両方に効く（Phase 1 のみ・Phase 2 のみではない）。新規セッション大量流入直後でも 1 Stop の `gh` 呼び出しが上限を超えない
- [ ] Phase 2 が `is_merged = true` の PR を refresh 対象から除外し、open な PR のみ re-check する
- [ ] cap 導入後も starvation しない（Phase 1 = newest-first、Phase 2 = oldest-checked-first で全件が順に処理される）
- [ ] backfill バックログが 2000+ ある状態で 10 回連続 Stop しても、secondary rate limit に当たらない
- [ ] backfill が新しいセッション（< 24h）の救済を取りこぼさない（0035 の horizon と整合）

問題 2（ブロッキング）:

- [ ] **Stop hook の同期パスが `gh` を 1 回も呼ばない**（pin 含む全 gh は async/detached worker 側。同期部分はローカル書き込み + worker spawn のみ）
- [ ] Stop hook が `backfill --detach` を spawn して即 return し、worker 完了を待たない（同期ホットパス ≈ 10ms 未満）
- [ ] backlog が大きい状態でも Stop hook が gh / backfill の完了を待ってユーザの応答サイクルをブロックしない
- [ ] worker が global single-flight（try-lock-skip）で、並列 Stop / 別 agent と同時に gh を撃たない（取れなければ即 exit）
- [ ] worker が cooldown を持ち、Stop が連続しても single-flight lock 取得ごとに gh batch が走り続けない
- [ ] 並列 backfill / append が `session-index.jsonl` のロスト更新を起こさない（flock 共有、lock 内 re-read/merge/write）。かつ flock は try-lock-skip ＋ 書き込みスコープ限定 ＋ 1 run 1 WriteAll で、並列実行待ちを生まない

収束アーキ / agent 対称:

- [ ] Claude / Codex とも worker トリガは Stop のみ。**hook 登録構成は現状から変更しない**（既存ユーザの settings.json 移行不要）
- [ ] Claude の SessionEnd は `ended_at` 書き込みのみで worker をトリガしない
- [ ] `agent-telemetry backfill --gc`（移行 drain）が既存 backlog を horizon / 第1層で一括 markChecked し、deploy 後 1 回で 2390+ を収束させられる。`doctor` が案内する

共通:
- [ ] Phase 2 (経路 4) の事後 refresh 経路が維持され、PR がマージ後に `is_merged = 1` へ更新される（`pr_metrics` 母集団＝ `agent_pr_*` / `agent_session_pr_*` が空にならない）。頻度・件数の制御は可だが経路自体は残す
- [ ] `doctor` が旧構成（同期 backfill を呼ぶ hook 等）を検出して案内する
- [ ] `docs/design.md` の Stop hook hot path 節と backfill 節を収束アーキに合わせて更新

## 参照

- 関連 issue: [0035-bug-backfill-no-pr-infinite-retry.md](0035-bug-backfill-no-pr-infinite-retry.md)（24h horizon で markChecked する別アプローチ）
- 関連: [closed/0020-design-backfill-evolution-to-stop-hook.md](closed/0020-design-backfill-evolution-to-stop-hook.md)（cron → Stop hook 移行の経緯）
- 暫定対処済の類例: statusline.js の `gh pr view` には別途 10 分キャッシュを入れている

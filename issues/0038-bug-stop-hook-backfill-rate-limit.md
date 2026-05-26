---
decision_type: design
affected_paths:
  - internal/backfill/backfill.go
  - internal/hook/stop.go
  - cmd/agent-telemetry/main.go
tags: [backfill, hooks, stop-hook-cost, rate-limit, blocking-ux]
---

# Stop hook での高頻度 gh 実行が rate limit と応答ブロッキングを引き起こす

Created: 2026-05-26

## 概要

`~/.claude/settings.json` の Stop フックに登録された `agent-telemetry hook stop --agent claude` が、毎ターン `backfill + sync-db` を **同期的に** 実行する。`backfill` は `session-index.jsonl` 内の `backfill_checked != true` かつ `pr_urls` 未設定のセッションに対して `gh pr list` / `gh pr view` を呼ぶ。

ここから、同じ根（**高頻度イベントである Stop hook で gh をブロッキング実行している**）に由来する 2 つの症状が出ている。本 issue は両方をまとめて解く。

- **問題 1 — GitHub rate limit**: 未チェックセッションが溜まっていると Stop ごとに大量の gh 呼び出しがバーストし、GitHub secondary rate limit を頻繁にヒットする。
- **問題 2 — 応答ブロッキング**: `stop.go` は `exec.Command(...).CombinedOutput()` で backfill / sync-db の完了を待つ。Stop はユーザ応答サイクル上のブロッキングフックなので、gh 呼び出し（pin の `gh pr view` 8s timeout + backfill バッチ）が遅いとき、その間ユーザが次の入力に進めず待たされる。

## 根拠

### 計測値（再現環境）

- `session-index.jsonl` の総セッション数: 3632
- うち `backfill_checked != true` & `pr_urls` 未設定: 2390
- → Stop イベントごとに最大 2390 件分の `gh pr view` が候補になり得る

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

> NOTE: 以下の a〜d は **問題 1（rate limit）単独** を前提に書かれた旧整理。問題 2（ブロッキング）の追加と、Codex に `SessionEnd` が無い agent 間制約を踏まえると優先順位・主軸は組み直す必要がある（特に「a を主軸 = Stop から退避」は Codex で成立しない）。解決策の再設計は別途行う。

複数の打ち手を組み合わせる。優先順位は a > b > c > d。

a. **Stop hook では backfill を起動しない**: backfill 自体は `SessionEnd` フックや明示的な `agent-telemetry backfill` コマンドのみで十分。Stop ごとの実行は過剰。Stop hook はメタデータ記録 + pin のみに絞る

b. **backfill に上限・スロットルを追加**:
   - 1 回の起動で処理する最大セッション数（例: `--max 20`）
   - 直近 N 秒以内に backfill が走った場合はスキップする mtime ガード
   - 呼び出し間 sleep

c. **`gh api rate_limit` を事前 probe**: `secondary_rate_limit.remaining` がしきい値以下なら backfill 全体を skip

d. **Stop フックを軽量版にして backfill を opt-in 化**: setup 時のデフォルト hook を「Stop = メタデータ + pin のみ」「SessionEnd = backfill」に分離

a を主軸（Stop hook の責務縮小）、b をフォールバック（既存ユーザ向けの暫定軽減）、d を新規 setup の挙動変更として実施するのが妥当か。**c（事前 rate_limit probe）は下記「window 絞り込みが主レバー」節のとおり over-engineering として落とす**（後回しではなく不採用）。

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

window を絞った後（GC 第1層+第2層 / Phase 2 terminal 除外 / cap）の定常 footprint は pin 1 + Phase 1 0〜数件 + Phase 2 数件（1h スロットル）＝ 1 桁〜低 2 桁/Stop で、secondary 閾値に対して誤差。よって:

- **落とす（over-engineering）**: 共有予算の token bucket / 会計、および打ち手 **c**（`gh api rate_limit` 事前 probe）。事前 probe 自体が 1 API 呼び出しで「小さく保つ」方針と矛盾気味。残すなら「踏んだ後の reactive cooldown」を最小実装する程度。
- **残余 1 — 並列の concurrency 重複排除**: N 並列 worktree の同時 Stop は瞬間バーストが N 倍。ただし「予算管理」ではなく**安価な global single-flight / lock** の話。Phase 2 は既存の 1h State スロットルがプロセス間 dedup を概ね担うが、**Phase 1 はプロセス間スロットルが無い**のが残余。window が小さい前提なら **必須でなく保険**レベル。
- **残余 2 — 移行直後の一括 drain（transient）**: 既存 backlog（再現環境 2390）は GC 適用前なので初回数 Stop が大バースト。bounded な一括 retire パスで対処する。予算管理とは別軸。

## 受け入れ条件

問題 1（rate limit）:

- [ ] Stop hook が backfill を直接呼ばないか、呼ぶとしても 1 起動あたりの `gh` 呼び出し数に明確な上限がある
- [ ] cap が Phase 1 / Phase 2 の両方に効く（Phase 1 のみ・Phase 2 のみではない）。新規セッション大量流入直後でも 1 Stop の `gh` 呼び出しが上限を超えない
- [ ] Phase 2 が `is_merged = true` の PR を refresh 対象から除外し、open な PR のみ re-check する
- [ ] cap 導入後も starvation しない（Phase 1 = newest-first、Phase 2 = oldest-checked-first で全件が順に処理される）
- [ ] backfill バックログが 2000+ ある状態で 10 回連続 Stop しても、secondary rate limit に当たらない
- [ ] backfill が新しいセッション（< 24h）の救済を取りこぼさない（0035 の horizon と整合）

問題 2（ブロッキング）:

- [ ] **Stop hook の同期パスが `gh` を 1 回も呼ばない**（pin 含む全 gh は async/detached 側。同期部分はローカル書き込みのみ）
- [ ] backlog が大きい状態でも Stop hook が gh / backfill の完了を待ってユーザの応答サイクルをブロックしない
- [ ] 非同期化する場合、backfill の多重起動が防がれている（single-flight 等で並行バーストを誘発しない）

共通:
- [ ] Phase 2 (経路 4) の事後 refresh 経路が維持され、PR がマージ後に `is_merged = 1` へ更新される（`pr_metrics` 母集団＝ `agent_pr_*` / `agent_session_pr_*` が空にならない）。頻度・件数の制御は可だが経路自体は残す
- [ ] `setup` コマンドの hook 登録が新方針に合わせて更新される（変更がある場合）。`doctor` も旧構成を検出して案内する
- [ ] `docs/design.md` の Stop hook hot path 節と backfill 節を実態に合わせて更新

## 参照

- 関連 issue: [0035-bug-backfill-no-pr-infinite-retry.md](0035-bug-backfill-no-pr-infinite-retry.md)（24h horizon で markChecked する別アプローチ）
- 関連: [closed/0020-design-backfill-evolution-to-stop-hook.md](closed/0020-design-backfill-evolution-to-stop-hook.md)（cron → Stop hook 移行の経緯）
- 暫定対処済の類例: statusline.js の `gh pr view` には別途 10 分キャッシュを入れている

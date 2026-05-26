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
- pin の `gh pr view` は best-effort だが 8s timeout を持つため、PR 解決が遅い / gh が詰まっているときは Stop ごとに最大 8s 上乗せされる
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

### 対応方針との関係

- 打ち手 **a** = 経路 3+4 を Stop hook から外す（経路 1 と 2 は残す）。
- 打ち手 **b** = 経路 3+4 を Stop hook で呼ぶがレート制御を入れる。
- 打ち手 **d** = `setup` のデフォルト hook を「Stop = 経路 1+2、SessionEnd = 経路 3+4」に分離する。

経路 1（pin）を残せば、ユーザが直近のセッションでダッシュボードを見るときに PR が既に紐づいているという UX は維持される。経路 3+4 を遅延・スロットルしてもダッシュボードの新鮮度はせいぜい数分〜SessionEnd まで遅れるだけで、ユースケース上の損失は小さい。

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

a を主軸（Stop hook の責務縮小）、b をフォールバック（既存ユーザ向けの暫定軽減）、d を新規 setup の挙動変更として実施するのが妥当か。c は実装コストの割に効果が薄いため後回し。

## 受け入れ条件

問題 1（rate limit）:

- [ ] Stop hook が backfill を直接呼ばないか、呼ぶとしても 1 起動あたりの `gh` 呼び出し数に明確な上限がある
- [ ] backfill バックログが 2000+ ある状態で 10 回連続 Stop しても、secondary rate limit に当たらない
- [ ] backfill が新しいセッション（< 24h）の救済を取りこぼさない（0035 の horizon と整合）

問題 2（ブロッキング）:

- [ ] backlog が大きい状態でも Stop hook が gh / backfill の完了を待ってユーザの応答サイクルをブロックしない
- [ ] 非同期化する場合、backfill の多重起動が防がれている（single-flight 等で並行バーストを誘発しない）

共通:
- [ ] `setup` コマンドの hook 登録が新方針に合わせて更新される（変更がある場合）。`doctor` も旧構成を検出して案内する
- [ ] `docs/design.md` の Stop hook hot path 節と backfill 節を実態に合わせて更新

## 参照

- 関連 issue: [0035-bug-backfill-no-pr-infinite-retry.md](0035-bug-backfill-no-pr-infinite-retry.md)（24h horizon で markChecked する別アプローチ）
- 関連: [closed/0020-design-backfill-evolution-to-stop-hook.md](closed/0020-design-backfill-evolution-to-stop-hook.md)（cron → Stop hook 移行の経緯）
- 暫定対処済の類例: statusline.js の `gh pr view` には別途 10 分キャッシュを入れている

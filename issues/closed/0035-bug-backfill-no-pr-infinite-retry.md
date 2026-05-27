---
decision_type: design
affected_paths:
  - internal/backfill/backfill.go
  - internal/sessionindex/update.go
  - internal/hook/stop.go
  - docs/design.md
  - docs/spec.md
tags: [backfill, hooks, stop-hook-cost, doc-code-drift]
closed_at: 2026-05-28
---

# backfill が PR 未作成ブランチを毎 Stop で無限再試行する

Created: 2026-05-11

## 概要

`internal/backfill/backfill.go` の `fetchPR` (L358-403) は、`gh pr list --head <branch> --author @me --state all --limit 1` が **空** を返したケースで `markChecked` を立てない。これにより:

- `feature/foo` で PR を作らずに worktree が残存 → Stop hook ごとに `gh pr list` を無限に叩く
- `main` / `master` セッション（dotfiles 等）→ こちらも毎 Stop で probe される

`docs/design.md:206` と `docs/spec.md:136` は「PR が存在しないブランチ（`main` / `master` 等）は初回チェック後に `backfill_checked: true` をセットして永続スキップする」と書いているが、code 側はその実装を持っていない（一度も書かれたことがない）。**ドキュメントが先行し、実装が追いついていない doc/code drift**。

加えて、Stop hook の pin lookup と backfill の group probe が **同 tick で同じ `gh pr list` を 2 回叩く**（`docs/design.md:147` 既述）。pin が「PR なし」を確定した直後でも、backfill が独立に再 probe する。

## 根拠

### 経緯（なぜ現状の実装になっているか）

- 原 Python (`batch/session-index-backfill-batch.py`, e1196eb 以前) は **cron 実行前提**で「`cwd` 存在 + PR 未発見 → 次 cron で再試行」をコメント明記の上で意図的に実装していた。cron tick 数（1 日数回）×グループ数のコストは許容範囲だった
- 2026-03 の Go rewrite (e1196eb) はこの挙動を素直に移植
- 2026-04-29 [issues/closed/0020-design-backfill-evolution-to-stop-hook.md](0020-design-backfill-evolution-to-stop-hook.md) で backfill を Stop hook から呼ぶよう移行 → retry 頻度が **cron tick 数 → Stop 回数** に急増
- 同じ流れで `docs/design.md:204-206` に「main/master は永続スキップ」が書かれたが、code 側の `fetchPR` は touch されず Python 起源の挙動が残った
- 2026-05-08 [issues/closed/0022-design-pr-resolve-early-binding.md](0022-design-pr-resolve-early-binding.md) で Stop hook pin が導入され、PR 作成済セッションは pin で即解決するようになった。しかし「PR 未作成ブランチの probe」は backfill フォールバック側に残ったため、本 issue の cost が浮き彫りになった

### 影響

- 個人 repo / dotfiles 利用で `~/.claude/session-index.jsonl` に main/master 系の old session が累積するほど、Stop hook hot path 内の backfill フェーズが線形にコスト増（`(repo, branch)` グループ単位なので最悪 8 並列で頭打ちだが、それでも `gh pr list` 1 回あたり通信遅延が乗る）
- abandoned `feature/*` worktree も同様
- ユーザに見える症状: Stop hook の応答完了後の待ち時間が無駄に伸びる、`gh` API rate limit に余分な圧

### `ended_at` の信頼性（2026-05 実測、E の前提に直結）

E の horizon は `ended_at` を基準にするが、`ended_at` の計測は agent で非対称（[0039](0039-bug-stop-hook-backfill-rate-limit.md) の調査）:

- **Claude**: SessionEnd hook (`sessionend.go:22`) が発火時に 1 回だけ書く。kill / 端末強制クローズ / クラッシュでは発火せず空のまま。**補完経路なし**。
- **Codex**: SessionEnd が無いので Stop hook が毎回上書き (`stop.go:62`) → 最終アクティビティ時刻。さらに `backfillCodexEndedAt` が rollout JSONL から復元。

ローカル実測（237 件）:

| 観点 | 値 |
|---|---|
| Claude の `ended_at` 空き | 32 / 207 ≈ **15%** |
| backlog（PR 未設定 & 未 checked 76 件）中の空き | 16 / 76 ≈ **21%** |
| Codex(相当) の空き | 0 / 30 |

→ **pure-`ended_at` horizon だと、Claude の恒久的に空な ~15% が markChecked されず永久 retry のまま残る**（backlog ほど空き率が高いのが致命的）。E の基準時刻は `ended_at` 単独ではなく `COALESCE(ended_at, timestamp)` にする必要がある（後述）。

## 問題

| シナリオ | 現状 (毎 Stop) | あるべき姿 |
|---|---|---|
| `main` / `master` セッションの累積 | 毎 Stop で `gh pr list` を group probe | 24h 経過後の old session は markChecked、新規分のみ 1 回 probe |
| `feature/foo` で PR 未作成のまま完了 | 同上 | 24h 経過後 markChecked、それ以前は遅延 PR 作成救済のため retry |
| Stop hook の pin が「PR なし」を確定した直後の同 tick | backfill が独立に再 probe（pin と同じ call を 2 回） | 同 tick 内では pin 結果を信用して probe skip |

## 対応方針

採用: **第1層（実デフォルトブランチの admission control）+ E（horizon）+ G（pin 結果を同 tick 内で再利用）の組み合わせ**。第1層 = 入口で candidate に入れない、E = 入った candidate を時間で諦める、という 2 段の GC（[0039](0039-bug-stop-hook-backfill-rate-limit.md) の GC 設計と対応。第1層/第2層の用語は 0039 と共通）。

### 第1層 — 実デフォルトブランチの admission control（新規採用）

repo の**実際のデフォルトブランチ上のセッションは構造的に PR を持たない**（デフォルトブランチから PR は作らない）ので、candidate に入れず probe しない。これは却下案 D の「`main`/`master` ハードコード除外」とは別物——**名前一致ではなく repo ごとの実デフォルトブランチを動的判定**するので、`trunk`/`dev`/`develop` 等の命名多様性に左右されない（D の却下理由を回避）。E と排他ではなく補完: 第1層は構造的 never-PR を 0 秒で弾き、E は abandoned feature branch を時間で諦める。

未解決の実装論点（要決定）:
- デフォルトブランチの判定方法とコスト。`gh repo view --json defaultBranchRef` や `git symbolic-ref refs/remotes/origin/HEAD` は呼び出しコスト or ref 未設定の懸念がある。**SessionStart の `extractGitInfo` 時点（既に git を叩いている）で「このセッションの branch == repo のデフォルトブランチか」を判定して flag を session entry に持たせる**のが追加コスト最小の候補。
- ボリューム効果は環境依存（このマシンは master 作業が多いだけで、デフォルトブランチがボリュームゾーンと一般化はできない）。採用根拠は「構造的に正しい入口フィルタ」であって volume bet ではない。

### E (主軸)

`fetchPR` で `gh pr list` が空を返した group について、group 内の session のうち以下を満たすものを markChecked する:

- 基準時刻 `COALESCE(ended_at, timestamp)` から 24h 以上経過している

基準時刻 24h 以内のセッションは markChecked しない（次 tick で再 probe = 遅延 PR 作成救済のため）。

**基準時刻は `ended_at` 単独ではなく `COALESCE(ended_at, timestamp)`**: Claude の ~15% は SessionEnd 不発で `ended_at` が恒久的に空（前述「`ended_at` の信頼性」）。`ended_at` 単独だとこの 15% が永久に markChecked されず、本 issue の無限 retry がそのまま残る。`ended_at` が空なら SessionStart で必ず入る `timestamp`（session 開始時刻）にフォールバックすることで全セッションが必ず age out する。フォールバックの副作用は「開始時刻基準で数時間早く諦める」だけで、retire 側に倒れるため安全。

これにより:
- main/master のような永続 PR-less group: 古い session は 24h 後に group 一括 markChecked、新規 session のみ 1 tick だけ probe → 自然収束
- abandoned `feature/*`: 24h 後に markChecked
- 完了直後 (~24h 以内) に手動 `gh pr create` する late-binding シナリオ: 引き続き救済

horizon は定数化（例: `MarkCheckedHorizon = 24 * time.Hour`）し、運用感を見て tune 可能にする。

### G (補助、別 PR でも可)

Stop hook の `pinPRForSession` が「PR なし」を確定した `(repo, branch)` を、同 tick 内の backfill フェーズに in-memory で渡し、当該 group の probe を skip する。これで Stop hook hot path の `gh pr list` 重複呼び出しを 1 回減らす。

### 却下した代替案

| 案 | 却下理由 |
|---|---|
| A: empty → 即 markChecked | 24h 以内の遅延 PR 作成を取りこぼす。retry 価値の中で最も大きい時間帯を捨てる |
| B: 試行回数 N | session entry に counter 追加（schema 変更）。`(repo, branch)` group との運用がぎこちない（min(attempts) を取る等の追加ロジック必要）。E に対する利点なし |
| C: `last_checked_at` フィールド追加 | schema 変更のコストに見合わない。`ended_at` の再利用で同等の効果 |
| D: branch 名 `main` / `master` を **hardcode** 除外 | branch 命名は org / 個人で多様（`trunk` / `dev` / `develop`）。汎用性を損なう。**ただし「repo の実デフォルトブランチを動的判定して除外」する第1層は採用**（D が却下したのは名前ハードコードであって、デフォルトブランチ除外という発想自体ではない） |
| F: pin が「PR なし」と判定した時点で session を即 markChecked | pin 直後 ~24h の遅延 PR 作成を救済不可。E と同じ目的を pin 側で（不適切に早く）やってしまう |

## 受け入れ条件

修正された後の振る舞いを以下で検証する:

- [x] repo の実デフォルトブランチ上のセッションは candidate に入らず `gh pr list` が一度も呼ばれない（第1層。branch 名のハードコードではなく動的判定）
- [x] `ended_at` が空のセッション（SessionEnd 不発の Claude 等）でも `timestamp` フォールバックで 24h 経過後に markChecked される（pure-`ended_at` だと永久 retry する穴を塞ぐ）— **0039 で解決済**（`sessionTime` = `COALESCE(ended_at, timestamp)`）
- [x] PR 未作成 main セッションは、`COALESCE(ended_at, timestamp)` から 24h 経過後の Stop hook 1 回で当該 session が `backfill_checked = true` になる — **0039 で解決済**（`shouldMarkChecked`）
- [x] 24h 以内のセッションは markChecked されず、次 tick で再 probe される（遅延 PR 作成の救済窓を維持）— **0039 で解決済**
- [x] PR 未作成の `feature/foo` セッションでも同様に 24h horizon が効く（branch 名に依存しない）— **0039 で解決済**
- [x] `(repo, branch)` グループ内に新旧 session が混在する場合、24h 経過した session のみが markChecked され、新規 session は markChecked されない（group 全体一括ではなく per-session 判定）— **0039 で解決済**
- [x] Stop hook の pin lookup が「PR なし」を確定した直後の同 tick の backfill では、当該 `(repo, branch)` の `gh pr list` が **追加で呼ばれない**（G 採用時）— **moot（0039 で収束アーキへ移行し pin の同期 gh パスを全廃。pin は worker の `runPinBackfill` に吸収済みで、`pinPRForSession` / `ghPRLookup` は削除済み）**
- [x] `agent-telemetry backfill --recheck` は引き続き markChecked を無視してフルスキャンする（既存挙動の回帰なし）— 第1層の構造除外は recheck でも効くが backfill_checked 無視のフルスキャン挙動は維持（`TestRunWithState_RecheckResetsCursor` / `TestRunURLBackfill_DefaultBranchExcludedUnderRecheck`）
- [x] `docs/design.md` の「`(repo, branch)` グルーピングと候補の絞り込み」節を、第1層 admission control + 第2層 horizon を反映した記述に更新する（horizon 部分は 0039 が既に更新済、本 PR で第1層を追記）
- [x] `docs/spec.md` の `backfill_checked` / `is_default_branch` 説明を、第1層 + horizon 条件を含む形に更新する
- [x] 既存の `internal/sessionindex/update_test.go` の `TestMarkChecked_*` を回帰させない
- [x] `internal/backfill/backfill.go` の horizon 判定 unit test（`TestGCOnly_MarksStaleUncheckedOnly`）は 0039 で追加済。本 PR は第1層の unit test（`TestRunURLBackfill_SkipsDefaultBranch` / `TestRunURLBackfill_DefaultBranchExcludedUnderRecheck` / `TestRunPinBackfill_SkipsDefaultBranch`）と動的判定 test（`internal/hook` の `TestIsRepoDefaultBranch_*`）を追加

## 参照

- 過去の意思決定: [0020](0020-design-backfill-evolution-to-stop-hook.md) (cron→Stop hook 移行) / [0022](0022-design-pr-resolve-early-binding.md) (Stop hook pin 導入) / [0001](0001-bug-pr-session-misattribution.md) (関連バグ)
- 該当コード: `internal/backfill/backfill.go:358-403` (`fetchPR`), `internal/sessionindex/update.go:131-176` (`MarkChecked`)
- doc/code drift 箇所: `docs/design.md:204-206`, `docs/spec.md:136`
- 詳細な調査メモ: `.outputs/claude/backfill-markchecked-investigation.md`（local 出力、commit しない）

---

Completed: 2026-05-28

## 解決方法

本 issue は **第1層（実デフォルトブランチの admission control）** と **第2層（24h horizon）+ G（pin 重複）** に分かれ、後者は先行 merge した [0039](0039-bug-stop-hook-backfill-rate-limit.md) が既に巻き取っていた。本 PR では残りの第1層のみを実装した。

### 第1層 — 実デフォルトブランチの admission control（本 PR）

repo の実デフォルトブランチ上のセッションは構造的に PR を持たない（デフォルトブランチから PR は作らない）ので、入口で candidate に入れず `gh pr list` を一度も呼ばない。

- **判定の場所と手段**: `internal/hook/sessionstart.go` の `extractGitInfo`（既に git を叩いている）で `isRepoDefaultBranch` を呼び、`git symbolic-ref --short refs/remotes/origin/HEAD` で repo ごとの実デフォルトブランチを動的に解決して現セッションの branch と比較する。gh API は呼ばない（コスト増を避ける）。origin/HEAD 未設定の repo に限り慣習的な `main` / `master` へフォールバックする。
- **フラグの永続化**: `sessionindex.Session` に `IsDefaultBranch bool json:"is_default_branch,omitempty"` を追加し、SessionStart で `true` のときだけ entry に焼き付ける（`false` は omitempty に合わせキー省略で既存レコードと同形を保つ）。backfill が読む `session-index.jsonl` にのみ持たせ、ダッシュボード用途が無いため `serverpipe` / `sessions` テーブル（DB）へは伝播させない。
- **candidate からの除外**: `internal/backfill/backfill.go` の `runURLBackfill`（Phase 1 候補収集）と `runPinBackfill`（現セッションの pin）の両方で `IsDefaultBranch` を除外。`backfill_checked` ではなくこのフラグが永続スキップを担うので `--recheck` でも候補に入らない。
- **却下/決定**: 案 D（branch 名 `main`/`master` の **ハードコード**除外）は却下し、実デフォルトブランチの**動的判定**を採用（`trunk`/`dev`/`develop` 等の命名多様性に対応）。G（pin の同 tick 重複呼び出し回避）は 0039 が収束アーキで pin の同期 gh パスを全廃したため **moot**。

### 0039 が解決済みの分（本 PR では触らない）

- 第2層 horizon: `backfill.go` の `shouldMarkChecked` + `sessionTime` が `COALESCE(ended_at, timestamp)` の 24h horizon を実装済み（`ended_at` 空の Claude も `timestamp` フォールバックで age out）。`backfill --gc` が一括 drain を担う。
- `docs/design.md` / `docs/spec.md` の horizon 記述。本 PR は同節へ第1層を追記して整合させた。

### 過去 entry の扱い

第1層は新規セッション向けの入口フィルタ。`is_default_branch` フラグを持たない過去のデフォルトブランチセッションは遡ってフラグ付けせず、従来どおり第2層 horizon + `backfill --gc` で収束させる（両者は補完関係）。

### テスト

- `internal/backfill/backfill_test.go`: `TestRunURLBackfill_SkipsDefaultBranch` / `TestRunURLBackfill_DefaultBranchExcludedUnderRecheck` / `TestRunPinBackfill_SkipsDefaultBranch`
- `internal/hook/sessionstart_test.go`: `TestIsRepoDefaultBranch_FallbackByName`（origin/HEAD 未設定 → 名前フォールバック）/ `TestIsRepoDefaultBranch_DynamicFromOriginHEAD`（origin/HEAD→trunk で動的判定が名前ハードコードでないことを確認）
- 既存 `TestMarkChecked_*` / `TestGCOnly_*` は回帰なし。`go build ./... && go vet ./... && go test ./...` 緑。

---
decision_type: design
tags: [pr-binding, hooks, sessionindex]
closed_at: 2026-05-08
---

# PR ↔ session 紐づけが PostToolUse 正規表現で汚染される

Created: 2026-05-07

## 概要

`internal/hook/posttooluse.go` の PR URL 抽出が、tool_response 内の **任意の** GitHub PR URL を正規表現でスクレイプして `pr_urls` に append する設計になっている。これにより `gh pr view 999`、`gh pr list`、ユーザが Bash の引数として URL を貼った場合など、自分が作成した PR と無関係な URL が `pr_urls` 末尾に追加される。

`internal/syncdb` 側は `pr_urls` 配列の **末尾** を採用するため、SQLite 上の session ↔ PR の紐づけが他人の PR にすり替わる。`pr_metrics` VIEW は merged PR をスコープに集計するため、間違った PR が集計対象になっていた。

加えて、同一ブランチを別 PR で使い回す運用（feature ブランチで PR を close → 同じブランチ名で別 PR を作成）でも、後から `gh pr list --head <branch>` の解決結果で古いセッションに新 PR が紐づいてしまう（`backfill` の late binding 経路）。

## 根拠

- `pr_metrics` は本ツールの主指標。session ↔ PR の対応が崩れると、PR あたりのトークン消費効率（`pr_per_million_tokens`）・review_comments・changes_requested すべての値が誤った PR に集約される
- 個人プロジェクトで規模は小さいが、`gh pr view` で他人の PR を覗くケースは日常的に発生する。再現性が高く、検出も困難（dashboard の数値がおかしいと感じるまで気づけない）
- `pr_urls` 配列で末尾採用するルール (`docs/design.md` の `pr_urls` 採用ルール節) は、汚染源 (PostToolUse の無差別抽出) を塞がない限り根本対処にならない

## 問題

1. **PostToolUse 正規表現の汚染** — `internal/hook/posttooluse.go:14-37` の `prURLRe` が tool_response 中の PR URL を全件抽出して `pr_urls` に append する。`gh pr view 999` / `gh pr list` / 貼り付けた URL すべてが対象。
2. **ブランチ再利用** — `internal/backfill/backfill.go` の `(repo, branch)` グルーピング解決は、現在の `gh pr list --head <branch>` 結果を採用するため、ブランチを使い回した古いセッションに新 PR が紐づく。

両方とも `pr_urls` 配列に「自分の PR と無関係な URL」が混入し、末尾採用で誤接続が発生する点が共通の根本原因。

## 対応方針

Stop hook 時点で `gh pr list --head <branch> --author @me` を 1 回叩いて PR を確定し、`pr_pinned: true` で session に束縛する **early binding** に切り替える。

- pinned 後は PostToolUse / `update` / `backfill` の URL append をすべて no-op にする
- pin 失敗（PR 未作成 / `gh` エラー / cwd 不在）は best-effort で skip し、既存の backfill late binding にフォールバック
- `Phase 2` の meta 取得（`is_merged` / `review_comments`）は pinned セッションも継続対象にする

検討した代替案（[0022-design-pr-resolve-early-binding.md](0022-design-pr-resolve-early-binding.md) に詳細）:

| 案 | 採否 |
|---|---|
| A: `pr_urls` 末尾採用を「先頭採用」に変更 | 却下。汚染源が残る |
| B: PostToolUse の URL 抽出条件を絞る | 補助的。pin で根治するため不要 |
| C: Stop hook で pin する | 採用 |
| D: session_id と PR HEAD SHA の連結 | overkill、将来の選択肢 |

Completed: 2026-05-08

## 解決方法

- `internal/sessionindex/sessionindex.go` に `Session.PRPinned` (`pr_pinned` JSON タグ) を追加し、`remarshalWithUpdate` の order map に組み込んで JSON 順序を固定した
- `internal/sessionindex/update.go` に `PinPR(indexPath, sessionID, prURL)` 関数を追加。`pr_urls` を `[prURL]` で **置換**（append ではない）し `pr_pinned: true` を立てる
- `Update` / `UpdateByBranch` を pinned セッションで no-op にして、PostToolUse / `update` / branch ベース backfill からの URL 追記を物理的に塞いだ
- `internal/hook/stop.go` に `pinPRForSession` を追加し、Stop hook の hot path で `gh pr list --head <branch> --author @me --state all --limit 1` を 1 回叩く。失敗は stderr に流して継続（best-effort）。テスト用に `prLookup` を package var で差し替え可能化
- `internal/backfill/backfill.go` の `runURLBackfill` グルーピング前で pinned セッションを除外
- `docs/spec.md` に `pr_pinned` フィールドを追加。Stop hook 役割表を更新
- `docs/design.md` に「PR の確定は Stop hook で early binding」節を追加。`pr_urls` 採用ルール節も pin 前提に書き換え
- `docs/history.md` の 9 番として 2026-05-07 セクションを追加（4 案の検討と採用理由）
- 単体テスト追加: `TestPinPR_*`, `TestUpdate_SkipsPinnedSession`, `TestUpdateByBranch_SkipsPinnedSession`, `TestRunPostToolUse_SkipsPinnedSession`, `TestRunURLBackfill_SkipsPinnedSession`, および `internal/hook/stop_test.go` 全体（pin の各分岐: PR 取得成功 / 該当なし / lookup error / cwd 不在 / 既 pinned / branch 空 / index 不在）

`go test ./...` 全 pass、`go vet ./...` clean、PR #33 で merge 予定。

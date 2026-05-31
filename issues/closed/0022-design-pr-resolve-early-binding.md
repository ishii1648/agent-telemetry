---
decision_type: design
tags: [retro, pr-binding, hooks, sessionindex]
closed_at: 2026-05-07
---

# PR resolve を Stop hook の early binding に切替

Created: 2026-05-07
Retro-converted: 2026-05-10 (from docs/history.md §9)

## 概要

PR と session の紐づけを「`(repo, branch)` キーで `gh pr list --head <branch>` を late binding」モデルから、Stop hook 時点で `gh pr list --head <branch> --author @me` を 1 回叩いて pin する **early binding** モデルに切り替えた。

## 根拠

late binding モデルには 2 つの構造的欠陥があった:

1. **PostToolUse 正規表現の汚染** — `internal/hook/posttooluse.go` が tool_response から PR URL を無差別に正規表現抽出して `pr_urls` に append する。`gh pr view 999` で他人の PR を見ただけ、または Bash で URL を貼っただけで、無関係な PR が末尾に追加される。`sync-db` は末尾を採用するため誤接続が発生していた
2. **ブランチ再利用での誤接続** — 同一ブランチを別 PR で使い回す運用で、後から作られた PR の URL が古いセッションに紐づく

## 問題

`pr_metrics` は本ツールの主指標で、session ↔ PR の対応が崩れると `pr_per_million_tokens` / `review_comments` / `changes_requested` すべての値が誤った PR に集約される。dashboard の数値がおかしいと感じるまで気づけず、検出が困難。

検討案:

| 案 | 内容 | 採否 |
|---|---|---|
| A | `pr_urls` 末尾採用を「先頭採用」に変更 | 却下。PostToolUse の append 順を逆にしただけで、汚染元は塞がらない |
| B | PostToolUse の URL 抽出条件を絞る（自分の `gh pr create` 経路のみ拾う） | 補助的。pin で根本解決するため今回は実装しない |
| C | Stop hook で `gh pr list --head <branch>` を 1 回叩いて pin する | **採用**。両欠陥を同時に塞げる |
| D | session_id と PR の HEAD SHA を連結して結合する | 将来の選択肢として記録。現状 overkill |

## 対応方針

採用案 (C) の要点:

- `Stop` hook 時点で branch 単位の PR 解決を 1 回だけ実行し、`pr_pinned: true` で session に束縛する
- pinned セッションは PostToolUse / `update` / `backfill` の URL append をすべて no-op にする
- pin 失敗（PR 未作成 / `gh` エラー / cwd 消滅）は backfill のフォールバックに委ねる。late binding を完全に廃止するわけではなく、責務を「PR 未作成セッションのリトライ専用」に絞る
- `Phase 2` の meta 取得は pinned セッションも引き続き対象（`is_merged` / `review_comments` の継続更新が必要）

## 解決方法

詳細は [0001](0001-bug-pr-session-misattribution.md) の `## 解決方法` を参照（同じバグを同じ PR で fix した）。要点のみ再掲:

- `internal/sessionindex/sessionindex.go` に `Session.PRPinned` フィールド追加、JSON 順序を固定
- `internal/sessionindex/update.go` に `PinPR(indexPath, sessionID, prURL)` を追加し、`pr_urls` を `[prURL]` で**置換**して `pr_pinned: true` を立てる
- `Update` / `UpdateByBranch` を pinned セッションで no-op にして、PostToolUse / `update` / branch ベース backfill からの URL 追記を物理的に塞いだ
- `internal/hook/stop.go` に `pinPRForSession` を追加。Stop hook hot path で `gh pr list --head <branch> --author @me --state all --limit 1` を 1 回叩く（best-effort）
- `internal/backfill/backfill.go` で pinned セッションを除外
- `docs/spec.md` / `docs/design.md` の `pr_urls` 採用ルール節を pin 前提に書き換え

## 採用しなかった代替

上記 A / B / D の通り。詳細は対応方針の表を参照。

## 参照

- [0001](0001-bug-pr-session-misattribution.md)（同 PR で fix されたバグ issue）

---
decision_type: design
tags: [hooks, pr-binding, posttooluse, retro-revisit]
closed_at: 2026-05-28
---

# PostToolUse の PR URL 抽出が pin 失敗時に他人の PR を紐づけるリスク

Created: 2026-05-11

## 概要

PostToolUse hook (`internal/hook/posttooluse.go:14`) は `tool_response` 中の任意の GitHub PR URL を `https://github\.com/[^/\s]+/[^/\s]+/pull/\d+` の正規表現で抽出して `pr_urls` に append する。Stop hook の pin (`internal/hook/stop.go` の `pinPRForSession`) が成功すれば `pr_urls` は `[<pinned>]` で置換 (`internal/sessionindex/update.go:88` の `PinPR`) されるため、append された他人の URL は消える。一方、**pin が失敗するシナリオ**では PostToolUse が拾った無関係な URL が `pr_urls` に残り、backfill Phase 2 の `fetchPRByURL` と `sync-db` の末尾採用を経由して、issue [0001](0001-bug-pr-session-misattribution.md) と同じ「session ↔ PR の誤接続」が再発する。

このリスクは [0022](0022-design-pr-resolve-early-binding.md) の検討表で「案 B（PostToolUse の URL 抽出条件を絞る）」として挙げられたが、「pin で根本解決するため不要」として却下された。今回の指摘は **pin 失敗時には pin が root fix になっていない** という観点で、案 B が塞ぐリスクが pin だけでは塞がっていないことを示す。

## 背景

- [0001](0001-bug-pr-session-misattribution.md): PostToolUse 正規表現の無差別抽出が `pr_urls` を汚染し、`sync-db` の末尾採用で誤接続が発生していたバグ
- [0022](0022-design-pr-resolve-early-binding.md): 上記を「Stop hook で `gh pr list --head <branch> --author @me` を 1 回叩いて pin する early binding」で根治した設計判断。pinned セッションは PostToolUse / `update` / `backfill` からの URL append をすべて no-op にする
- 現状の `docs/design.md:137-142` も「pin 後はすべての append が no-op になるため塞がる」と pin を前提に書いている

## 問題

pin が失敗するシナリオでは `pr_pinned: false` のままで PostToolUse の append が有効になる。具体的には `internal/hook/stop.go` 上で次のいずれかが発生したケース:

| pin 失敗の経路 | 該当箇所 | 帰結 |
|---|---|---|
| `gh pr list` が 8 秒タイムアウト | `internal/hook/stop.go:19` (`pinPRTimeout`), `internal/hook/stop.go:188-193` | `pr_pinned` が立たないまま Stop hook 完了 |
| cwd が消えている（worktree 削除済み） | `internal/hook/stop.go:135` (`isExistingDir`) | `pinPRForSession` が早期 return。pin 未試行 |
| `branch` が空 | `internal/hook/stop.go:127-129` | 同上 |
| `gh` 不在 / auth 切れ / network error | `internal/hook/stop.go:188-193` | `prLookup` が error を返し best-effort skip (`internal/hook/stop.go:55-58`) |
| `gh pr create` 直後にセッションが Stop に到達せず終了 | Stop hook 自体が走らない | pin が動作する機会がない |
| pin 前の PostToolUse fire | PostToolUse は Bash 1 回ごとに発火、Stop は session 末尾のみ | pin 前に append されれば、pin 失敗時にそのまま残る |

これらのケースでは PostToolUse の regex 抽出が `pr_urls` に書いた URL がそのまま残り、以下を経由して誤接続に繋がる:

1. `internal/sessionindex/update.go:39` の `Update` は `pr_pinned == true` だけスキップする (L57-59)。pin 失敗 → `pr_pinned == false` → append が通る
2. backfill Phase 2 (`fetchPRByURL`) が `pr_urls` 内の URL すべてを meta 取得対象にし、他人の PR の `is_merged` / `review_comments` を session に書き込む
3. `sync-db` が `pr_urls` の末尾を採用するルール (`docs/design.md:217`) で、他人の PR と session が紐づく
4. `pr_metrics` VIEW で `pr_per_million_tokens` / `review_comments` / `changes_requested` が誤った PR に集約される（[0001](0001-bug-pr-session-misattribution.md) の症状）

つまり pin はリスクを「通常時には」消しているだけで、PostToolUse 抽出そのもののリスクは消していない。pin の **フォールバックとしての価値**（`gh pr create` 直後の URL を PostToolUse が拾って後続 backfill に渡す経路）と **汚染リスク**（無差別 regex）が同じ regex 経路に同居している点が構造的な弱さ。

## issue 0022 との関係

[0022](0022-design-pr-resolve-early-binding.md) の検討表:

> | B | PostToolUse の URL 抽出条件を絞る（自分の `gh pr create` 経路のみ拾う） | 補助的。pin で根本解決するため今回は実装しない |

この判断は「pin が常に成功する」という暗黙の仮定に立っている。実際には上記のとおり pin 失敗経路が複数存在し、そのすべてで PostToolUse の汚染リスクが復活する。0022 当時に考慮されていなかった視点を本 issue で再検討する。

## 検討案

| 案 | 内容 | トレードオフ |
|---|---|---|
| A | PostToolUse の `extractPRURLs` を **`tool_input.command` が `gh pr create ...` のときだけ** 走らせる（[0022](0022-design-pr-resolve-early-binding.md) 案 B の後付け実装） | pin 成功時の挙動は変わらない（Update は pinned で no-op）。pin 失敗時のリスクだけ消える。実装コスト軽。「`gh` の wrapper / alias」「`hub pr create`」「ghq + git push 経由の `--web` flow」など他の PR 作成経路を取りこぼす可能性 |
| B | PostToolUse の URL 抽出を完全削除 | pin 失敗時のフォールバック価値（`gh pr create` 直後の URL 捕捉 → 次 backfill での確定）を捨てる。汚染リスクはゼロになる。失った fallback は `agent-telemetry update <url>` CLI の手動補完で埋める想定 |
| C | 現状維持 | pin 失敗を稀と仮定し続ける。仮定を裏付けるには **pin 成功率のテレメトリ**（pin 試行回数 / 成功回数 / 失敗理由内訳）が必要。観測装置が現状ないなら判断材料が不足 |

意思決定に必要な追加データ: **pin 成功率**。現在 `internal/hook/stop.go:55-59` は pin 失敗を stderr に流すだけで、構造化テレメトリには出していない。集計可能な形で観測する仕組みが先に必要なら、本 issue の前段に観測 issue を別途立てる。

## 受け入れ条件

- [ ] 案 A / B / C のいずれかを採用し、その理由を本 issue の `## 解決方法` に記載する
- [ ] 採用案を反映して `docs/design.md` の「PR の確定は Stop hook で early binding」節（L137-142 周辺）を更新する。pin 失敗時の振る舞いと、PostToolUse 抽出のスコープを明記する
- [ ] 採用案の実装は別 issue / 別 PR で行ってよい（本 issue は設計判断の記録までをスコープとする）
- [ ] 不採用案は本 issue の `## 採用しなかった代替` に理由付きで記録する
- [ ] 案 C を採用する場合、pin 成功率のテレメトリ追加 issue を別途 open し、本 issue から参照する

## 参照

- [0001](0001-bug-pr-session-misattribution.md) — PostToolUse 汚染による PR ↔ session 誤接続バグ（root cause）
- [0022](0022-design-pr-resolve-early-binding.md) — Stop hook pin で 0001 を根治した設計判断（本 issue で前提を再検討する対象）
- 該当コード:
  - `internal/hook/posttooluse.go:14` (`prURLRe` 正規表現)
  - `internal/hook/posttooluse.go:24-38` (`RunPostToolUse`)
  - `internal/hook/stop.go:104-162` (`pinPRForSession`)
  - `internal/hook/stop.go:168-203` (`ghPRLookup`、8 秒タイムアウト)
  - `internal/sessionindex/update.go:39-78` (`Update`、`pr_pinned` チェック)
  - `internal/sessionindex/update.go:88-129` (`PinPR`、`pr_urls` 置換)
- 既存ドキュメント: `docs/design.md:137-142` (pin 設計), `docs/design.md:215-221` (`pr_urls` 採用ルール)

Completed: 2026-05-28

## 解決方法

案 A を採用した。PostToolUse の PR URL 抽出は `tool_input.command` が `gh pr create` で始まる Bash 実行に限定し、`gh pr view` / `gh pr list` / 任意に貼られた PR URL は `pr_urls` に追記しない。

これにより `gh pr create` stdout を拾う pin 失敗時の軽量 fallback は残しつつ、PostToolUse の無差別 regex が他人の PR URL を session に残す経路を閉じた。pin 成功時は従来どおり `pr_pinned: true` により URL 追記が no-op になるため挙動は変わらない。

`docs/spec.md` と `docs/design.md` には、PostToolUse 抽出のスコープと pin 失敗時の fallback 境界を反映した。

## 採用しなかった代替

- 案 B（PostToolUse 抽出の完全削除）は不採用。`gh pr create` 直後の stdout を拾える価値があり、抽出対象を作成コマンドに限定すれば主要な汚染リスクは避けられるため。
- 案 C（現状維持）は不採用。pin 失敗経路は複数あり、pin 成功率の観測が無い状態で無差別抽出を残すのは 0001 の再発リスクを説明できないため。

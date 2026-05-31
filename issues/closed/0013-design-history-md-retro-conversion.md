---
decision_type: process
tags: [intent, history, retro, decision-record]
closed_at: 2026-05-10
---

# docs/history.md の既存 11 件を issue 化するか残置するかの方針決定

Created: 2026-05-10

## 概要

0011 で意思決定の primary store を `issues/` に集約し、`make intent P=<p>` で
コードから逆引きできるようにした。`docs/history.md` には 0011 以前から積まれた
過去の意思決定 11 件があり、これらは現状 issue 化されていないため、
`make intent` の対象外。retro 的に issue として起こすか、history.md に
ナラティブとして残置するか、ハイブリッドにするかを未決のまま放置すると、
新規決定は issue / 過去決定は history.md という分断が固定化する。

## 根拠

- 0011 後の運用では「issue が primary、history.md は事後ナラティブ要約」と
  CLAUDE.md で明文化したが、既存 11 件はその新ルールの前に書かれている
- `make intent` は `affected_paths` を見るので、history.md にしかない決定は
  Claude / Codex から逆引きできない（最も agentic に重要な過去決定が
  検索網に乗らない）
- 一方、history.md は「人間が大方針を筋立てたナラティブ」として書かれており、
  機械可読な issue 形式に全 11 件を分解すると冗長 / コンテキスト喪失のリスク

## 問題

- どの 11 件が issue 化に値するか（agentic に逆引きされる蓋然性で選別すべきか、
  全件機械的に起こすべきか、選別基準は何か）
- issue 化する場合、`affected_paths` が現存する path とずれる可能性
  （rename / 削除済み）。`--lint` で warning にはなるが、retro 化時に
  人手で path を解決する必要がある
- issue 化しない場合、history.md エントリにも `make intent` 相当の
  逆引きを効かせる仕組みが要るか（例: history.md 内の各節に
  affected_paths メタを付ける拡張）
- 既に書かれた history.md の節を issue にコピーする労力 vs 得られる
  逆引きヒット率の見積り

## 対応方針

未確定。pending として保留する。決定すべき軸:

1. **選別基準**: 全件起こす / `affected_paths` を引きやすそうな実装系の決定だけ起こす / 起こさず history.md 拡張で対応
2. **保留候補**: history.md L1-L11（番号付きの 11 件）を一覧化し、それぞれに
   「この決定は将来 `make intent` で引かれそうか」を Yes/No 評価する
3. **ハイブリッド時の重複ルール**: 同じ決定が history.md と issue 双方に
   存在する場合、どちらを正とするか（issue が正、history.md は要約のみ
   残す等）

着手前に、まず 0012 (Hugo docs site) の段階で history.md がどう扱われるか
（site 上の表示形態）を見極めると判断材料が増える。

## Pending 2026-05-10

0011 を実装した直後の判断: retro 化の労力対効果は実運用してみないと分から
ない。`make intent` で過去決定が引けないことが実害として顕在化したタイミング
（例: ある PR で過去判断を見落として手戻りが発生した、等）で着手するのが
順当。0012 の Hugo build 方針が固まれば、history.md の表示形態と issue の
表示形態を合わせる必要が出るので、その時点で再検討する。

Completed: 2026-05-10

## 解決方法

pending 期間は短かったが、0012 で Hugo docs site の方針が固まり「issues/ を
input として load する正本にする」方向が確定したため、`make intent` の逆引き
網に過去 11 件を乗せる価値が顕在化した。同日に retro 化を実施。

### 採用基準

- **全件 sweep**: 11 件すべてを issue 化。選別すると「どの基準で外したか」を
  後から再検討するコストが膨らむ。retro 化の単発コスト（約 2.5 時間）が許容範囲
  だったため一括処理を選んだ
- **closed/ 直行**: すでに完了した決定なので open を経由しない。`closed_at` は
  元の決定日（履歴値）、`Created:` も元日付。冒頭に
  `Retro-converted: 2026-05-10 (from docs/history.md §N)` を 1 行入れて出自を明示
- **history.md の narrative は 1〜3 文要約 + retro issue リンクに圧縮**:
  CLAUDE.md / AGENTS.md の現行方針（「人間が大方針を筋立てたナラティブ要約」）
  に整合。プロジェクト史の流れは保ちつつ、詳細は issue 側に集約

### retro issue の採番マッピング

| # | 元 history.md | retro issue | decision_type |
|---|---|---|---|
| 1 | §1 dotfiles 分離 | [0014](0014-process-dotfiles-to-standalone-repo.md) | process |
| 2 | §2 SQLite+Grafana | [0015](0015-design-sqlite-grafana-as-visualization-backend.md) | design |
| 3 | §3 Go サブコマンド統一 | [0016](0016-design-shell-hooks-to-go-subcommands.md) | design |
| 4 | §4 token 効率中心 | [0017](0017-spec-shift-main-metric-to-pr-token-efficiency.md) | spec |
| 5 | §5 マルチエージェント | [0018](0018-spec-multi-coding-agent-support.md) | spec |
| 6 | §6 CHANGELOG 廃止 | [0019](0019-process-deprecate-changelog-md.md) | process |
| 7 | §7 backfill 変遷 | [0020](0020-design-backfill-evolution-to-stop-hook.md) | design |
| 8 | §8 リポジトリリネーム | [0021](0021-spec-rename-hitl-metrics-to-agent-telemetry.md) | spec |
| 9 | §9 PR resolve early binding | [0022](0022-design-pr-resolve-early-binding.md) | design |
| 10 | §10 TODO.md 廃止 | [0023](0023-process-deprecate-todo-md.md) | process |
| 11 | §11 user_id 導入 | [0024](0024-spec-introduce-user-id-field.md) | spec |

### 同梱変更

- `issues/SEQUENCE` を 14 → 25 に bump
- `docs/history.md` の §1〜§11 を 1〜3 文要約 + retro issue リンクに圧縮
- 「ADR 索引」「残っている有効な決定の要点」「廃止された設計と理由」セクションは現状維持（history.md ならではの俯瞰価値）

### 採用しなかった代替

- **選別して一部のみ retro 化**: 「どの基準で外したか」を後から再検討するコストが、全件 sweep の追加コストを上回る
- **history.md エントリに `affected_paths` メタを追加して逆引き対応**: フォーマット拡張が必要で、issue 側の frontmatter スキーマと二重管理になる
- **history.md の narrative を全削除**: プロジェクト史の流れを失う。閲覧導線としての価値も消える

## 後続の発展

- 同日中の進化として [0027](0027-process-deprecate-history-md.md) で `docs/history.md` 自体を廃止する判断に至った。本 issue で残した「ナラティブ要約 + retro issue リンク」90 行が、結局 retro issue の `## 概要` セクションを 11 件並べたものでしかなく chronological view 以外の付加価値がなかったため。本 issue の「採用しなかった代替: history.md の narrative を全削除」は当時の判断としては正しかったが、実際にナラティブ要約を残してみた結果として情報量の薄さが顕在化したため、結論を覆して 0027 で削除に振り切った

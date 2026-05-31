---
decision_type: process
supersedes: [0013, 0019]
tags: [process, documentation, history, primary-store]
closed_at: 2026-05-10
---

# `docs/history.md` を廃止 — issues/closed/ に一本化

Created: 2026-05-10

## 概要

[0013](0013-design-history-md-retro-conversion.md) で 11 件のナラティブ要約と retro issue リンクに圧縮した `docs/history.md` を、さらに進めて完全削除する。「方針転換の WHY」の正本は `issues/closed/` の retro issue とし、CLAUDE.md / AGENTS.md / spec.md / design.md からの参照も全て issues/closed/ に書き換える。

## 根拠

- 0013 完了後の `docs/history.md` (90 行) の §1〜§11 ナラティブ要約は、対応する retro issue の `## 概要` セクションとほぼ同内容。chronological view 以外の付加価値がない
- 0011 で「issue が primary store、history.md は事後ナラティブ要約」と位置付けたが、retro 化が完了した今は「ナラティブ要約」も結局 retro issue の概要を 11 件並べたものでしかない（順序保持以外の情報差分なし）
- chronological view が必要なら、`ls issues/closed/ | sort` か、将来の Hugo build (0011 段階4) で `closed_at` 順に並べる view で機械的に再構築できる
- 0019 で確立した「リリース note / WHY / コミット judgement / WHAT log」の 4 store split のうち、WHY を担っていた history.md を廃止することで 3 store に簡素化できる

## 問題

- CLAUDE.md / AGENTS.md / spec.md / design.md の 10 箇所で history.md が参照されている
- 0011 / 0013 / 0019 の retro issue が history.md を「現在の store」として記述している（0027 で廃止されたことを各 issue で forward reference する必要）
- CLAUDE.md L5 / AGENTS.md L146 は history.md の特定セクション（§8 / §9）を deep link しているため、retro issue (0021 / 0022) へのリダイレクトが必要

## 対応方針

`docs/history.md` を完全削除し、参照を全て issues/closed/ に書き換える。chronological view は再構築せず、必要になったら 0011 段階4（Hugo build-time view）で対応する。

決定の記録方針も更新:

| 種類 | 旧 | 新 |
|---|---|---|
| 仕様の変更 | spec.md 更新 | spec.md 更新（不変）|
| 実装方針の変更 | design.md 更新、大きな転換は history.md にも追記 | design.md 更新、大きな転換は issues/closed/ に retro issue として記録 |
| 過去の経緯として残す価値がある転換 | history.md に追記 | issues/closed/<NNNN>-<cat>-<slug>.md として記録 |
| 1 コミット内の判断 | Contextual Commits | Contextual Commits（不変）|

## 解決方法

### 削除対象

- `docs/history.md` を `git rm`

### 参照更新（10 箇所）

- **CLAUDE.md**
  - L5: `docs/history.md` §8 への deep link → [0021](0021-spec-rename-hitl-metrics-to-agent-telemetry.md) へリダイレクト
  - L12: doc 構成リストから `docs/history.md` 行を削除
  - L19: 「大きな方針転換は `docs/history.md` にも追記する」→「大きな方針転換は `issues/closed/` に retro issue として記録する」
  - L28: 「大きな転換の場合は `docs/history.md` にも追記」→「大きな転換は `issues/closed/` に retro issue として記録」
  - L51: `docs/history.md` の役割定義段落を削除（「issue が primary store」だけ残す）
- **AGENTS.md**: CLAUDE.md と同等の 5 箇所（L9 / L16 / L25 / L42 / L146）を同パターンで更新
- **docs/spec.md** L4: 「過去の経緯は `docs/history.md`」→「過去の経緯は `issues/closed/` の retro issue」
- **docs/design.md**
  - L5: 同上
  - L133: 「詳細は `docs/history.md` を参照」→ [0020](0020-design-backfill-evolution-to-stop-hook.md) を参照

### retro issue の forward reference 追記

- **0011**: 「`docs/history.md` の位置付け変更」節と受け入れ条件で history.md の役割を述べているため、本 issue (0027) によるさらなる発展を `## 後続の発展` で追記
- **0013**: 「history.md の retro 化」が本 issue で完全削除に進化した旨を `## 後続の発展` で追記
- **0019**: 「方針転換の WHY → docs/history.md」とした 4 store split が、本 issue (0027) で 3 store split に簡素化された旨を `## 後続の発展` で追記

## 採用しなかった代替

- **§1〜§11 を 1 行テーブルに圧縮**: history.md 内部のみのクリーンアップでメタ更新コストは低いが、将来同じ議論が再来する可能性が高く、本質的な決着にならない
- **history.md を残しつつ §1〜§11 を auto-generate**: Hugo build で `closed_at` 順に並べる view を作る案。0011 段階4 の領域で、本 issue とは独立。必要になった時点で別 issue として切り出す
- **0013 の `## 解決方法` に追記して済ませる**: 元 issue (0013) のスコープは「11 件の retro 化方針」で、history.md 自体の廃止は別軸の意思決定。捻じ込むと意思決定の追跡可能性が落ちる

## 影響を受ける既存仕様

- `CLAUDE.md` の「ドキュメント構成」「意思決定の記録方針」「設計セッション」の各節
- `AGENTS.md` の同等の節
- `docs/spec.md` / `docs/design.md` 冒頭の参照案内
- 0011 / 0013 / 0019 の retro issue（forward reference のみ、本文は変更しない）

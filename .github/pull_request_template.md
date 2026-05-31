<!--
本 repo は意思決定の primary store を `issues/` に置く運用です。
PR description はこのテンプレに沿って薄く保ち、「なぜ」「方針」「却下案」
などの重い文脈は issue / commit body に書いてください。

参考: AGENTS.md「issues について」, CLAUDE.md「意思決定の記録方針」
-->

## 関連 issue

<!--
必須。実装対象の issue ファイルへのリンクを貼る。
複数 issue を解決する場合はすべて列挙する。
新規 issue を伴わない軽微な変更（typo / chore）の場合は "(N/A — chore)" でも可。
CI (.github/workflows/intent.yml) が issue リンク（issues/NNNN-...）または "(N/A" の
記載を必須化し、欠落した PR は merge をブロックする。
-->

- `issues/<id>-<cat>-<slug>.md`

## 概要

<!--
1-2 文で何を変えたかを書く。**「なぜ」「方針」は issue 側で確認できるはず**
なのでここでは深追いしない。レビュアーへの導線になる粒度で十分。
-->

## テスト計画

<!--
チェックリストでの簡潔な記述で十分。実行ログは PR コメントに貼ってよい。
-->

- [ ] 

## レビュー時のメモ（任意）

<!--
レビュアーに先回りで伝えたい注意点があれば。なければ削除してよい。
- 後方互換性に関わる変更
- 別 PR にすべきだったが事情があって含めた変更
- フォローアップとして残した課題（issue 番号でリンク）
-->

---

<!--
書かないもの（issue / commit body に振り分ける）:
- 「なぜこの変更が必要か」の長文 → issue 本文（`## 概要` / `## 根拠`）
- 採用しなかった代替案 → commit body の `rejected(scope): ...` 行
- 実装中に得た学び → commit body の `learned(scope): ...` 行
- 行レベル / 関数レベルの判断 → commit body の `decision(scope): ...` 行
-->

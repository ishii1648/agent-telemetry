---
name: intent-lookup
description: >-
  Use when about to edit a file or directory in this repo and you want to know
  the past intent for that path: issues whose body mentions the path, plus
  Contextual Commits action lines from `git log --follow`. Trigger on phrases
  like "intent", "意図を調べて", "なぜこのコードはこうなった", "この path の
  意思決定履歴", or when starting non-trivial edits to internal/ / cmd/ / docs/
  / grafana/ files.
---

# intent-lookup

コードの特定 path に紐づく過去の意思決定を逆引きする。path → issue の対応は
**issue 本文の grep** と **git 履歴**から動的に求める。手書きの path インデックス
（旧 `affected_paths` frontmatter）には依存しない（ゼロメンテ）。

## 位置づけ

これは **意図記録への逆引き索引** であって、意図そのものではない。意図そのもの
は issue 本文・docs・commit body 側にある。本ツールは「変更しようとしている
path に言及する決定の候補」を見落とさないための入口を提供する。precision より
**recall** に振っており、無関係寄りの候補が混じり得る前提で読むこと。本文を読んで
取捨選択するのは呼び出し側の役割。

## いつ使うか

- 既存のファイルを編集する直前。なぜ現状の実装になっているか・過去にどんな代替案が検討・却下されたかを context として読み込む
- バグ修正で「同じ path で過去に近い修正があったか」を確かめたい
- 大きめのリファクタ前に、既存の制約（`constraint:` 行や `rejected:` 行）を見落とさないように

逆に：1 行の typo 修正や、自分が直近で触ったばかりの path には不要。

## 呼び方

```bash
scripts/intent-lookup <path> [--format=markdown|json] [--full]
```

または Makefile 経由：

```bash
make intent P=<path>                       # markdown 出力（既定: 抜粋付き）
make intent P=<path> FORMAT=json           # JSON 出力
make intent P=<path> FULL=1                # 抜粋ではなく本文全文を含める
```

`<path>` はファイルでもディレクトリでも可（リポジトリルートからの相対 path）。
末尾 `/` の有無は問わない。

## 動作

1. クエリ path を `git log --follow --name-only` で展開し、過去の rename を考慮した path 候補集合を作る（**rename-aware**: issue 側が現 path を知らなくても、本文が旧 path を参照していれば拾える）
2. path 候補とその祖先ディレクトリ（2 コンポーネント以上。`internal/` のような top-level 単独は除く）を grep キーにする
3. `issues/`, `issues/closed/`, `issues/pending/` の各 issue 全文に対し、grep キーが部分文字列として出現する issue を集める
4. issue 本文から「概要 / 根拠 / 問題 / 対応方針 / 解決方法」セクションの先頭段落を抜粋（`--full` で全文）
5. `git log --follow -- <path>` から各 commit を取得し、body の `intent:` /
   `decision:` / `rejected:` / `constraint:` / `learned:` 行を抽出
6. issue 一覧と commit 一覧をまとめて出力

## 出力の読み方

- **Issues** セクション — path に言及している issue。`decision_type`、tags、`supersedes`、close 日付に加え、本文の **概要 / 対応方針 / 解決方法** の抜粋を含む。詳細を読む必要があるかどうかの判定材料として使う
- **Commits** セクション — 1 コミットで完結した micro decision。Contextual Commits の action 行のみ抜粋
- 出力先頭の `Rename-aware: also matched against N historical path(s)` は、現 path だけでなく旧 path も対象にしたことを示す

両方で 0 件なら、その path には記録された意図がない。新しい決定を残すなら
`issues/<NNNN>-...` に書き起こすか、commit body に action 行を入れる。

抜粋を見て「もっと読まないと判断できない」と感じたら、`--full` を付けて再実行する
か、`file:` に出ている issue ファイルを直接開く。

## 使用例

```bash
# 特定ファイルへの過去の意図（抜粋）
make intent P=internal/hook/stop.go

# 抜粋では足りない時は全文
make intent P=internal/hook/stop.go FULL=1

# ディレクトリ全体の意思決定（broader）
make intent P=internal/syncdb/

# JSON で取って context として読み込む
make intent P=internal/sessionindex/ FORMAT=json
```

## why の辿り方（このツール以外）

意図の主たる辿り方は git blame → commit → PR → PR description の issue リンク →
issue 本文。本ツールはその入口を補助するもので、唯一の経路ではない。PR には必ず
関連 issue へのリンクを貼る（CI が PR description の issue リンクを必須化している）。

## 依存

- Python 3.9+（標準ライブラリのみ。yq / jq / awk は不要）
- `git`

開発環境に標準で入っている前提。追加 install は不要。

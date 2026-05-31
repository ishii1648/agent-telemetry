---
decision_type: design
supersedes: [0011]
tags: [intent, decision-record, tooling, ci, process]
---

# affected_paths / intent-lint の廃止 — grep 縮退と PR→issue の CI 担保

Created: 2026-05-31

## 概要

[0011](0011-feat-structured-intent-on-issues.md) で導入した「コードから why を辿る仕組み」のうち、issue frontmatter の手書き path インデックス（`affected_paths`）と、その腐敗を検出する `intent-lint`（`lint_ignore_broad` / `lint_ignore_missing` を含む）を**廃止**する。

代わりに:

- `scripts/intent-lookup` は **path → issue 全文 grep ＋ `git log --follow` の commit action 行**だけを返すゼロメンテ版に縮退する（手書きインデックス不要）
- why の鎖は **git blame → commit → PR → PR description の issue リンク → issue 本文**で辿る。PR→issue リンクの欠落を CI で防ぐ

## 根拠

仕組みの目的は「coding agent がある程度の実装粒度で過去の意図を確認できる」こと。これに対し path 単位の厳密管理は過剰だった：

1. **受益者と消費者のミスマッチ** — `affected_paths` の厳密さが上げるのは逆引きの precision。だが消費者の agent はノイズ耐性が高く、候補に無関係な issue が混じっても本文を読んで取捨選択できる。効くのは recall（関連する意図に辿り着けるか）であって precision ではない。precision のために人手で path を磨く（`lint_ignore_broad` を書く）のは agent の強みと逆行する投資
2. **lint 一式の存在＝腐敗の証拠** — `affected_path_missing` / `affected_path_broad` 警告と `lint_ignore_*` 例外機構が必要になっている時点で、手書き path インデックスは保守コストが高く腐りやすいと示している
3. **規模が索引の自動化を正当化しない** — issue は数十件規模。agent は issues/ を全文 grep すれば足りる。`affected_paths` は将来の規模を先取りした投資だが、その規模で効くのは本文の検索性であって path 索引の厳密さではない

逆引きの自動トリガー（編集前に意図を引く skill）自体は価値があるので残す。捨てるのは「手書きインデックスとその lint」だけ。

## 問題

- `affected_paths` を消すと「コード領域 → 関連 issue」の機械突合が消える。recall=0 の領域（過去に却下した設計を再導入する事故）を防ぐ担保が要る
- `intent-lint` は間接的に「why をどこかに書いたか」を意識させる装置でもあった。撤去するなら規律を別の形で担保する必要がある

## 対応方針

### grep 縮退（recall は grep で担保）

`scripts/intent-lookup <path>` を次の 2 経路に縮退する（手書き frontmatter 非依存）:

- **経路A'**: query path（＋ `git log --follow --name-only` で解決した rename 旧 path 候補）を、各 issue の全文に対する部分文字列 grep でマッチし、ヒット issue を列挙
- **経路B（維持）**: `git log --follow` で対象 path に触れた commit の Contextual Commits action 行（`intent:/decision:/rejected:/constraint:/learned:`）を抽出

`--lint` / `--strict`、`affected_paths` 突合、`lint_ignore_*` は削除。本文セクション抜粋・rename-aware・`--full`・`--format=json` は維持。

### PR→issue を CI で担保（規律の代替装置）

`intent-lint` の「why を意識させる」役割を、PR description の issue リンク強制で置き換える。`.github/workflows/intent.yml` に、PR body が issue リンク（`issues/\d{4}-`）または `(N/A` を含むことを検査する job を追加。欠落で merge ブロック。1 コミット完結の chore は `(N/A — chore)` 明記で通す（既存規約と整合）。

### frontmatter 掃除

全 issue から `affected_paths` / `lint_ignore_broad` / `lint_ignore_missing` を削除。`decision_type` / `supersedes` / `tags` / `closed_at` は分類・grep 補助として残す。

## 採用しなかった代替

- **path 索引を「design/spec の横断決定だけ」に縮小して残す**: 中間案。だが「どの決定が横断的か」の線引き自体が新たな手作業を生み、lint を残せば腐敗も残る。grep で recall を担保できる以上、索引を半端に残す利点が薄い
- **intent-lookup を完全撤去し生 grep / agent の自律に委ねる**: 最もシンプルだが、編集前に意図を引く自動トリガーが消え recall=0 事故の保険が薄くなる。skill という入口は残す方を取った
- **PR template の N/A 許容を廃し全 PR issue リンク必須**: issue 駆動を徹底できるが、1 コミット完結の chore まで issue を強制するのは「厳密管理は過剰」という本 issue の論旨と矛盾する

## 影響を受ける既存仕様

- `scripts/intent-lookup` / `scripts/test_intent_lookup.py`
- `Makefile`（`intent-lint` / `test-intent` target）
- `.github/workflows/intent.yml`
- `.claude/skills/intent-lookup/SKILL.md`
- 全 issue の frontmatter
- `AGENTS.md`「issues について」/ frontmatter フィールド表、`CLAUDE.md` 意思決定の記録方針、`.github/pull_request_template.md`

Completed: 2026-05-31

## 解決方法

- `scripts/intent-lookup` を grep 縮退: lint 一式（`--lint`/`--strict`/`Finding`/`lint_issues`/`is_broad_path` 等）と `affected_paths` 突合を削除。query path（`git log --follow` で rename 解決）＋その祖先ディレクトリ（2 コンポーネント以上。top-level 単独は除外）を grep キーに issue 全文をマッチする経路に置換。commit action 行抽出・本文セクション抜粋・rename-aware・`--full`・`--format=json` は維持
- `scripts/test_intent_lookup.py` を grep 経路に刷新（lint / `affected_paths` overlap / `lint_ignore` 系を削除し、本文 path 言及マッチ・祖先 dir recall・top-level 過剰マッチ防止・rename の 19 ケースに）
- `Makefile` から `intent-lint` target を削除（`intent` / `test-intent` は維持）
- `.github/workflows/intent.yml` を再構成: lint job を削除し、PR description に issue リンク（`issues/NNNN-`）または `(N/A` を必須化する `pr-issue-link` job を追加。`test` job（grep 版の regression 防止）は維持
- `.claude/skills/intent-lookup/SKILL.md` を grep 版に書き換え（位置づけ・rename-aware は維持）
- 全 issue（47 ファイル）の frontmatter から `affected_paths` / `lint_ignore_broad` / `lint_ignore_missing`（および付随する frontmatter コメント）を削除。`decision_type` / `supersedes` / `tags` / `closed_at` は維持
- `AGENTS.md` / `CLAUDE.md` / `.github/pull_request_template.md` を grep 縮退・PR→issue リンク CI 強制に同期
- [0011](0011-feat-structured-intent-on-issues.md) 末尾に Superseded 節を追記して双方向参照を手書きで張った

### 採用しなかった代替

「対応方針」末尾の節を参照（path 索引を design/spec だけに縮小する中間案、intent-lookup の完全撤去、PR の N/A 許容廃止）。いずれも recall を grep で担保できる前提のもとで利点が薄いか、本 issue の論旨と矛盾するため不採用。

### 検証

- `make test-intent` → 19 ケース全 pass
- `make intent P=internal/hook/stop.go` 等の手動実行で issue（本文 path 言及）＋ commit action 行が返ることを確認
- `make intent-lint` が `No rule to make target` で失敗（撤去確認）
- PR body 検査ロジックを「リンクあり / N/A / 無し」の 3 ケースで確認（無しのみ block）
- `actionlint .github/workflows/intent.yml` OK / `go test ./...` 全 pass（コード本体は不変）

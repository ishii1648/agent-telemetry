---
decision_type: process
tags: [intent, decision-record, frontmatter, tooling, python]
closed_at: 2026-05-10
---

# 構造化された意図蓄積 — issues/ への frontmatter 導入と path 逆引き

Created: 2026-05-09

## 概要

`issues/<NNNN>-*.md` の意味を「タスク」から「**タスク兼意思決定記録**」に拡張する。issue に frontmatter（`decision_type` / `affected_paths` / `supersedes` / `tags` / `closed_at`）を追加し、意思決定の構造化された primary store とする。コードを編集している場所から関連する過去の意図を逆引きできる CLI を提供する。

## 根拠

agentic engineering で最も参照される情報は「なぜその仕様・設計・実装にしたのか」。これはコード・spec・design いずれにも登場せず、将来の実装変更やバグ修正の際に決定的に重要となる。

現状の意図は 4 箇所に分散している（`docs/history.md` / `docs/archive/adr/` / Contextual Commits の action 行 / `issues/closed/*.md` の `## 解決方法`）が、以下の構造的な弱点を抱える：

1. **`history.md` は事後編集**で「これは history に書くべき」という判断漏れで永久に欠落する。capture が optional なので decay する
2. **粒度が大方針に偏る**。history.md は 11 件の pivot しか拾えず、API 命名・retry 戦略・フィールド追加の根拠・フラグ既定値の根拠などの micro decision は commit body にしか残らない
3. **コードからの逆引きが存在しない**。`internal/agent/codex/parser.go` を編集する時に、その場所に effect を持つ過去の意思決定を引くインターフェースがない（`git blame` → commit body → 隣接 issue を辿る職人芸でしか辿れない）

issues/ への移行で得られた基盤（**永続化された連番** / **ライフサイクル** / **closed の `## 解決方法`**）を活用すれば、上記 3 点を構造的に解決できる。

## 問題

- frontmatter 規約が未定義（書き手によってバラバラになる）
- 既存の open / closed / pending issues に frontmatter が入っていない（retrofit が必要）
- code path から intent を逆引きするインターフェースがない
- `AGENTS.md` の「issues について」が「タスクの記録」前提のまま（拡張された意味論が未反映）
- `CLAUDE.md` の「意思決定の記録方針」が history.md / commit / spec / design の 4 分類のままで、issue が意思決定 store として位置づけられていない

## 対応方針

意思決定の primary store を **issues/** に集約する。段階 1 が前提、段階 3 まで終われば「コードを触る Claude が当該箇所の過去の意思決定を取れる」状態になる。

### 段階 1: frontmatter 規約

`AGENTS.md` の「issue ファイルの構造」セクションに以下の frontmatter を追記する：

```yaml
---
decision_type: spec | design | implementation | process
affected_paths:
  - internal/agent/codex/
  - cmd/agent-telemetry/setup.go
supersedes: [0023]
tags: [hooks, multi-agent, packaging]
closed_at: YYYY-MM-DD
---
```

セマンティクス:

- `decision_type` — 意思決定の層（`spec`=外部契約 / `design`=内部設計 / `implementation`=実装 detail / `process`=開発プロセス）
- `affected_paths` — path snapshot。rename は `git log --follow` で逆引き側が吸収するため path 自体は更新しない
- `supersedes` — 過去 issue 番号の配列。supersededBy への双方向参照は索引側で生成
- `tags` — free-form。当面は明示的なタグ語彙統制を置かず、出現頻度から事後に整理する
- `closed_at` — close 時に確定。open / pending では省略可

合意済みの線引き: **「複数コミット or 後続が参照しそうな決定」**を issue 化する。1 コミットで完結する判断は Contextual Commits の action 行で十分（既存規約のまま）。

### 段階 2: 既存 issue の retrofit

- `issues/closed/*.md` (0001, 0002, 0010) に frontmatter を埋める
- `issues/<id>-*.md` (0003, 0004, 0009) に open 用の最小 frontmatter を入れる（`closed_at` 省略）
- `issues/pending/*.md` (0005-0008) にも同様
- `docs/history.md` の 11 件は本 issue では触らない。retro 化（過去判断を遡って issue として起こすか、history.md に残置するか）は [0013](0013-design-history-md-retro-conversion.md) で扱う

### 段階 3: path 逆引き script

**production binary (`agent-telemetry`) に subcommand を生やさない** — telemetry 計測 / hook という本体の責務範囲を超え、distribution（goreleaser で end user に配布される binary）に dev/docs 用ツールを混ぜることになるため。

実装は **Python 3 の単一スクリプト**（標準ライブラリのみ、約 500 行）。最初は bash script を試したが、frontmatter スキャン・rename 耐性のための path 候補展開・本文セクション抜粋・lint mode を載せていく過程で `jq` subprocess 連発と bash 3.2 の連想配列なし問題で破綻した。Go binary は overkill（型安全性が load-bearing になるほどスキーマは複雑ではない / 将来の Hugo build-time 連携も script を外部コマンドとして呼ぶ形で十分）。

```
scripts/intent-lookup            # Python 3 単一スクリプト（shebang `#!/usr/bin/env python3`）
scripts/test_intent_lookup.py    # 標準ライブラリ unittest によるテスト（fixture git repo を tempdir に組む）
.claude/skills/intent-lookup/    # Claude Code から呼ぶ skill（script を wrap）
Makefile                         # `make intent P=...` / `make intent-lint` / `make test-intent` を thin wrapper として追加
```

- `cmd/agent-telemetry/` には触れない。`.goreleaser.yaml` も変更しない
- 人間は `make intent P=internal/agent/codex/` で invoke
- Claude Code は `.claude/skills/intent-lookup/` 経由で自然に呼ぶ
- 依存: Python 3.9+ と `git` のみ。`yq` / `jq` / `awk` への外部依存はなし（frontmatter は内蔵ミニ YAML パーサーで処理 — issue の frontmatter スキーマは固定的なので 80 行程度で済む）

#### 位置づけ — 「逆引き索引」であって「意図そのもの」ではない

このツールが返すのは **意図記録への候補一覧**。意図の本体は issue 本文・docs・commit body 側にある。索引は「変更しようとしている path に紐づく決定の候補を見落とさない」ための入口を提供する。AGENTS.md / CLAUDE.md / SKILL.md / 出力ヘッダの全てでこの位置づけを明示する。

#### 機能要件

- 入力 path に対し `affected_paths` が前方一致する open / closed / pending issues を一覧
- **本文抜粋**: 各 issue の「概要 / 根拠 / 問題 / 対応方針 / 解決方法」セクションの先頭段落を抜粋（`### 段階 N` のサブヘディングで始まる場合はその下の段落も取り込む）。`--full` で全文
- **rename 耐性**: クエリ path を `git log --follow --name-only` で展開し、過去の rename を考慮した path 候補集合と issue.affected_paths を overlap 判定する（issue 側を rename 後に書き換えなくても現在 path で逆引きできる）
- 併せて `git log --follow -- <path>` で commit body から `intent:` / `decision:` / `rejected:` / `constraint:` / `learned:` 行を抽出してマージ表示
- 既定出力は markdown（人も Claude も読める）
- `--format=json` で機械可読出力（Claude が context として読み込みやすくする / 将来 0012 段階の Hugo build へ食わせる中間データとしても流用可）

#### `--lint` mode

`scripts/intent-lookup --lint` で `issues/` 全体を walk し、frontmatter の健全性を検査する。affected_paths 由来のインデックスは人手保守なので、欠落・存在しない path・broad path などで徐々に腐る。これを早期に検出する。

検査項目:

- `frontmatter_missing` / `frontmatter_invalid`
- `decision_type_invalid`（enum 外）
- `affected_paths_empty`
- `affected_path_missing`（repo に存在しない path; rename 後の更新漏れ・typo を検出）
- `affected_path_broad`（top-level dir 単独。top-level 単独 file は対象外）

exit code は **errors のみ常に gating（exit 2）、warnings は既定で表示のみ exit 0、`--strict` 指定時のみ exit 1**。ローカル開発では既定（warnings は表示のみ）、CI (`.github/workflows/intent.yml`) では `make intent-lint STRICT=1` で warnings まで gating する。これにより新規 PR で broad / missing path を含む issue を作っても merge できなくなる。同 workflow で `make test-intent` も走らせ、script ロジックの regression も防ぐ。

加えて、legitimate な broad / missing path は frontmatter の `lint_ignore_broad` / `lint_ignore_missing` で path 単位に抑制できる（理由は YAML コメントで併記）。これにより実 repo の lint warnings は 0 件まで落とせ、warning が出たら本当に対応すべきケースだけになる。各 ignore は `affected_paths` の path と完全一致でしか効かない（partial match で意図しない suppress が起きないように）。

### 粒度ガイドライン（AGENTS.md「issue ファイルの構造」に明文化）

issue が肥大化すると読む側が重くなり、index としての価値も落ちる。AGENTS.md に以下を追加:

- open 時: 「なぜ・方針」中心。具体的なファイル名・関数名は書かず PR / commit body に任せる
- close 時: 解決方法・採用しなかった代替は要点だけ。実装ログは commit body へ
- 200 行を超えそうなら issue で記録すべき意思決定を再考する
- 例外: 規約そのものを作る meta issue（本 issue 0011 自身）は規約と実装例がセットで価値を持つため高密度を許容。**通常の issue でこの密度を求めない**

#### テスト

`scripts/test_intent_lookup.py`（標準ライブラリ `unittest` のみ。pytest 不要）。`tempfile.TemporaryDirectory` + `subprocess.run("git", ...)` で fixture git repo を組み、CLI を subprocess で叩いて出力を assertion する。lookup / 本文抜粋 / lint / rename-aware の 4 系統で 21 ケース。

### 段階 4 (本 issue の対象外): 人間用俯瞰 view

人間用俯瞰 view（timeline / supersedes グラフ / tag filter / 全文検索）の提供は別 issue として切り出す。**0012 (Hugo docs site) を前提**とし、`site/content/intent/` 配下に issues/ の frontmatter から生成する形で実装する（hand-rolled HTML は採用しない — 機能追加ごとに JS ライブラリを足す負債化を避けるため）。

Claude は引き続き `issues/<id>-*.md` の frontmatter / 本文を直接読むため、段階 3 までで agentic 編集サイクルの要求は満たされる。段階 4 は人間用 view 限定の付加価値。

### `docs/history.md` の位置付け変更

段階 3 完了後の history.md は「**書く場所**」ではなく「**人間が大方針を筋立てたナラティブ要約**」に役割を絞る。新規エントリは原則 issue 側に書き、history.md には issue へのリンクと一文要約のみ追記する形が望ましい。これは段階 2 完了後に CLAUDE.md の「意思決定の記録方針」を更新する形で明文化する。

## 受け入れ条件

- [x] `AGENTS.md` の「issue ファイルの構造」に frontmatter 仕様を追記し、各フィールドの語彙とセマンティクスを文書化
- [x] `AGENTS.md` の「issues について」冒頭で issue が「タスク兼意思決定記録」であることと、`make intent` が **逆引き索引** である旨を明記
- [x] `CLAUDE.md` の「意思決定の記録方針」を更新し、micro decision の線引き（「複数コミット or 後続が参照しそうな決定」）と issue が primary store である旨を明記。`make intent P=<p>` の表記不整合（PATH= → P=）を修正
- [x] 既存 closed issues 3 件 (0001, 0002, 0010) に frontmatter を retrofit
- [x] 既存 open issues 3 件 (0003, 0004, 0009) と pending issues 4 件 (0005-0008) にも最小 frontmatter を追加
- [x] `scripts/intent-lookup` を Python 3 標準ライブラリのみで実装（frontmatter 走査 + 本文セクション抜粋 + `git log --follow` の Contextual Commits 行抽出 + rename-aware path 解決）
- [x] **`cmd/agent-telemetry/` / `.goreleaser.yaml` には一切変更を加えない**
- [x] `--format=json` / 既定 markdown 出力の両方をサポート
- [x] `--full` / `--lint` を追加し、抜粋 vs 全文の切替と frontmatter 健全性検査を提供
- [x] `Makefile` に `make intent P=<p>` / `make intent-lint` / `make test-intent` target を追加（script の thin wrapper）
- [x] `.claude/skills/intent-lookup/` を新設・更新（Claude Code 経由で自然に呼べるようにし、「逆引き索引」位置づけを明示）
- [x] `scripts/test_intent_lookup.py`（unittest）でテスト。lookup / 本文抜粋 / lint / rename-aware / lint_ignore の 5 系統 26 ケース
- [x] CI workflow `.github/workflows/intent.yml` で `make intent-lint STRICT=1` と `make test-intent` を gating（PR で `issues/**` / script / Makefile が触られたら自動実行）
- [x] `.github/pull_request_template.md` を新設し、PR description が「関連 issue リンク + 概要 + テスト計画」の薄いフォームに固まるようにする（issue 肥大化対策と同じ方向で PR の肥大化も構造的に防ぐ）
- [x] 段階 4（HTML view 生成）は本 issue に含めず、別 issue として切り出す

## 進行方針・PR 分割

- 段階 1（規約）+ 段階 2（retrofit）は同 PR に含めて良い（規約と適用例がセットで意味を持つため）
- 段階 3（CLI）は別 PR に分ける
- close 時に frontmatter 必須化する hook / lint は本 issue では入れない（運用後に必要性が出たら別 issue）

## 影響を受ける既存仕様

- `AGENTS.md` の「issues について」セクション
- `CLAUDE.md` の「意思決定の記録方針」セクション
- `issues/<id>-*.md` のファイル形式（frontmatter 追加）
- `Makefile`（`make intent` target 追加）
- `scripts/intent-lookup`（新規 Python 3 single-file script）
- `scripts/test_intent_lookup.py`（新規 unittest テスト）
- `.claude/skills/intent-lookup/`（新規）

**変更しない**:
- `cmd/agent-telemetry/`（production binary は telemetry / hook 計測の責務に閉じる）
- `.goreleaser.yaml`（end user に配布する binary に dev tool を混ぜない）

Completed: 2026-05-10

## 解決方法

### 段階 1: frontmatter 規約

- `AGENTS.md` の「issues について」冒頭で、`issues/` が「タスク兼意思決定記録」の primary store であることと、`make intent P=<p>` が **意図記録への逆引き索引** として機能する（意図そのものは issue 本文・docs・commit body 側にある）ことを明記。「複数コミット or 後続が参照しそうな決定」を issue 化する線引きも追記
- `AGENTS.md` の「issue ファイルの構造」セクションに frontmatter テンプレートと各フィールド（`decision_type` / `affected_paths` / `supersedes` / `tags` / `closed_at`）の表を追加。`affected_paths` の rename 耐性は逆引き側（`git log --follow --name-only`）が吸収する旨を明記
- `CLAUDE.md` の「意思決定の記録方針」を 4 分類から 5 分類に更新し、issue が primary store / `docs/history.md` は事後ナラティブ要約に役割を絞る旨を明記。`make intent P=<p>`（PATH= ではない）の正しい変数名を明示

### 段階 2: 既存 issue の retrofit

- closed 3 件 (0001, 0002, 0010)・open 3 件 (0003, 0004, 0009)・pending 4 件 (0005-0008) すべてに frontmatter を追加
- 本 issue (0011) と新規 0012 (Hugo docs site) にも揃って frontmatter を入れた（一貫性維持）

### 段階 3: path 逆引き script（Python 3 単一スクリプト）

- `scripts/intent-lookup` を Python 3 標準ライブラリのみで実装（約 500 行）。当初 bash で書き始めたが、frontmatter スキャン・rename 耐性・本文セクション抜粋・lint mode を載せていく過程で `jq` subprocess 連発と bash 3.2 の連想配列なし問題で破綻したため Python に切り替えた
- frontmatter は内蔵ミニ YAML パーサー（scalar / inline list / block list / inline int list / quoted string）。issue の frontmatter スキーマは固定的なので外部 YAML ライブラリは不要
- prefix 一致は **bidirectional**（`entry ⊆ query` か `query ⊆ entry` のどちらでも overlap 扱い）。ファイル指定 / ディレクトリ指定どちらでも自然に拾える
- **rename-aware path 解決**: クエリが file の場合、`git log --follow --name-only --format=` で過去の rename 履歴を辿り、得られた path 候補すべてを overlap 判定対象にする。これにより issue 側の `affected_paths` を rename 後に書き換えなくても、現在 path で逆引きできる。出力には `resolved_paths: [...]` を含めて何が候補に乗ったか可視化する
- **本文セクション抜粋**: 各 issue の `## 概要` / `## 根拠` / `## 問題` / `## 対応方針` / `## 解決方法` の先頭段落を取り出して結果に同梱（`### 段階 N` のサブヘディングで始まる場合はその下の段落も取り込む）。`--full` で全文に切り替え可能
- `--format=markdown`（既定）と `--format=json`（Claude が context として読み込みやすい / 将来 0012 の Hugo build で中間データとして流用可）の両方に対応
- 出力ヘッダに `> This is a **lookup index**, not the intent itself` を必ず入れて位置づけを明示
- 依存: Python 3.9+ と `git` のみ。dev 環境前提なので distribution 経路には載せない（`cmd/agent-telemetry/` / `.goreleaser.yaml` は不変）
- `.claude/skills/intent-lookup/SKILL.md` を新設・更新（Claude Code がコード編集前に自然に呼ぶための trigger 文書、`--full` / `--lint` の使い方も記載）

### 段階 3.5: `--lint` mode

`affected_paths` は人手保守なので、欠落・古い path・broad path で徐々に腐る。これを早期に検出するため `scripts/intent-lookup --lint` を実装した。検査項目:

- `frontmatter_missing` / `frontmatter_invalid`
- `decision_type_invalid`（enum 外）
- `affected_paths_empty`
- `affected_path_missing`（repo に存在しない path; `Path.exists()` と `git ls-files` の両方で見る）
- `affected_path_broad`（top-level dir 単独。top-level 単独 file は対象外）

exit code は 0=clean / 1=warnings / 2=errors。`make intent-lint` を thin wrapper として追加。CI 等で gating に使える。

### 段階 4 を別 issue に切り出し

人間用俯瞰 view は 0012 (Hugo docs site) を前提に切り出す方針が issue 本文で確定済み。本 issue の対象外。

### Makefile target の変数名

issue 本文の例では当初 `make intent PATH=<p>` だったが、`PATH` は Make の実行時 PATH と衝突して recipe が壊れるため、実装では `make intent P=<p>` に変更した（FORMAT=json / FULL=1 も同様に追加）。AGENTS.md / CLAUDE.md / SKILL.md / 0011 すべて `P=` 表記に統一済み。

### 採用しなかった代替

- **bash で全部書く**: 一度書き始めたが、`jq` subprocess の連発・bash 3.2 の連想配列なし・複数行 YAML スキャンを awk でやる脆さで保守不能になり Python に切り替えた。Python 3 は dev 環境に標準で入っているため依存追加コストはゼロ
- **PyYAML 依存**: issue frontmatter のスキーマは固定的（scalar / list だけ）なので 80 行程度の内蔵パーサーで足りる。dev 依存を 1 個でも減らす方を取った
- **pytest 等の外部テストフレームワーク**: 標準ライブラリ `unittest` で十分（fixture は `tempfile.TemporaryDirectory` + `subprocess.run`）
- **frontmatter 必須化を pre-commit hook で強制**: 本 issue のスコープ外。代わりに `--lint` で「検出はする / 強制はしない」運用にし、CI 側で gating したくなった時に `make intent-lint` を呼ぶ形にできる
- **PR close 時に変更ファイルと issue.affected_paths が交差しているかチェック**: GitHub Actions / CI 側の話で範囲が違う。本 issue では扱わない
- **`previous_paths` frontmatter で rename を明示**: 各 issue の frontmatter を rename ごとに更新する手間が大きい。`git log --follow --name-only` で逆引き側が自動で吸収する方を取った
- **`cmd/agent-telemetry/` への subcommand 追加 / `.goreleaser.yaml` 変更**: production binary の責務（telemetry / hook 計測）を逸脱するため明示的に除外（受け入れ条件にも記載）

### 検証

- `make test-intent` → 21 ケース全 pass。lookup（JSON 構造 / bidirectional prefix overlap / 関係ない issue の除外 / action 行のないコミットの除外 / markdown 構造 / no-arg / frontmatter 欠落 skip）+ 本文抜粋（excerpt が `### 段階 N` を伴う場合は本文も取り込む / `--full` で全段階を含む）+ `--lint`（frontmatter 欠落 / 不正 decision_type / broad / missing / top-level file は broad ではない）+ rename-aware（git mv 後に新 path / 旧 path どちらで引いても元 issue がヒット）の 4 系統
- `./scripts/intent-lookup internal/hook/stop.go` / `make intent-lint` 等の手動実行で実 repo に対する出力も期待通り（実 repo の lint は 6 件の legitimate な warning を返す: 0012 の `site/` 未作成 / 0005 の存在しない path / 0008 の broad `examples/` など）

## 後続の発展

- [0027](0027-process-deprecate-history-md.md) で `docs/history.md` 自体を廃止し、本 issue で位置付けた「issue が primary、history.md は事後ナラティブ要約」の二段構えを「issues/closed/ 一本化」に簡素化した。本 issue の「`docs/history.md` の位置付け変更」節と受け入れ条件にある history.md 関連の記述は、その時点で superseded された

## Superseded 2026-05-31 by [0047](0047-design-deprecate-affected-paths-intent-lint.md)

本 issue が導入した `affected_paths`（手書き path インデックス）と `intent-lint`（腐敗検出・`lint_ignore_*` 例外機構）は [0047](0047-design-deprecate-affected-paths-intent-lint.md) で廃止した。path 厳密マッチ（precision 投資）は agent の用途に対して過剰で、lint 一式の保守コストに見合わなかった。`intent-lookup` は「path → issue 全文 grep ＋ commit action 行」のゼロメンテ版に縮退し、why の鎖は git blame → PR → issue リンク（CI で強制）で辿る方式に移行した。frontmatter 規約（`decision_type` / `supersedes` / `tags` / `closed_at`）と issue 駆動・Contextual Commits は維持。

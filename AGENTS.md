# agent-telemetry

Codex の PR 単位のトークン消費効率を追跡・可視化する計測ツール（hook・CLI・ダッシュボード）。

## ドキュメント構成

- `docs/spec.md` — 外部契約（CLI コマンド・hook 仕様・データモデル・ダッシュボード）
- `docs/design.md` — 実装方針と設計判断
- `issues/closed/` — 過去の意思決定記録（retro issue を含む正本）
- `docs/archive/adr/` — 旧 ADR 形式の意思決定記録（参照のみ）
- `site/content/explain/` — 仕組み解説 docs（visualize 主体、gh-pages へ配信）
- `site/content/setup/` — セットアップ・運用 docs（install / server / usage、gh-pages へ配信）

`docs/` は agent が input として load する **reference** の正本（spec / design / metrics の 3 本）。`site/content/` は人間向けの **解説 docs** と **セットアップガイド** で、配信先は GitHub Pages (`https://ishii1648.github.io/agent-telemetry/`)。`site/content/explain/` が visualize 主体の how it works、`site/content/setup/` が install / server / usage の how-to。site から docs への参照は GitHub の markdown URL を直接張る（Hugo 内に取り込まない）。

新規の設計判断は ADR を作成しない。`docs/design.md` を更新し、Contextual Commits のアクション行で「なぜ」を記録する。大きな方針転換は `issues/<NNNN>-<cat>-<slug>.md` として retro / 設計 issue に起こす（実装が完了したら `issues/closed/` に move する）。

設計変更と実装はブランチを分ける必要はない。同一 PR で `docs/`・`site/`・コードを一緒に更新してよい。`docs/spec.md` / `docs/design.md` は変更が発生した PR で同期更新する（merge 後に main で別途追従する運用は取らない）。

## 開発規約

### 意思決定の記録方針

- 仕様の変更 → `docs/spec.md` を更新
- 実装方針の変更 → `docs/design.md` を更新
- 過去の意思決定の経緯として残す価値がある転換 → `issues/closed/<NNNN>-<cat>-<slug>.md` として記録
- 1 コミット内で完結する判断 → Contextual Commits のアクション行で記録
- chore / リファクタなど意思決定を伴わない変更 → アクション行不要

### コミット

Contextual Commits を使用。Conventional Commits プレフィックス + 構造化されたアクション行でコミットの意図を記録する。

### ブランチ命名

`feat/`, `fix/`, `docs/`, `chore/` + kebab-case（例: `feat/add-sync-db`）

### issues について

`issues/` は **タスク兼意思決定記録** の primary store。バグ・課題・設計判断はここに Markdown としてライフサイクル管理する。GitHub Issues は使わない（ローカルで完結し、`git log` と同じ粒度で追跡できるため）。

各 issue は frontmatter で「どの層の決定か（spec / design / implementation / process）」「どの code path に effect を持つか（`affected_paths`）」を構造化して持つ。`make intent P=<p>` / `scripts/intent-lookup <p>` は、この frontmatter とコミットの Contextual Commits 行をマージして引く **意図記録への逆引き索引** として機能する（意図そのものは issue 本文・docs・commit body 側にある。逆引きは候補を見落としにくくするための入口）。「複数コミット or 後続が参照しそうな決定」は issue 化する。1 コミットで完結する判断は Contextual Commits の action 行で十分。

#### ディレクトリ構成

```
issues/
  SEQUENCE                     # 次に発番する番号（整数 1 行）
  NNNN-<category>-<slug>.md    # open な issue
  closed/
    NNNN-<category>-<slug>.md  # 解決済み
  pending/
    NNNN-<category>-<slug>.md  # 設計判断保留・外部依存待ち
```

`issues/closed/` と `issues/pending/` には `.gitkeep` を置き、空でも git に残す。

#### 命名規則

`{seqnum}-{category}-{short-description}.md`

- `seqnum` は 4 桁ゼロパディング（`0001`, `0042`）。9999 を超えたら 5 桁に拡張する
- `seqnum` は `issues/SEQUENCE` の値を使う。issue を新規作成したら同コミットで `+1` する
- `category` は `bug` / `feat` / `doc` / `chore` / `design` のいずれか。`design` は pending に置かれることが多い
- `short-description` は kebab-case の英数字。例: `0001-bug-pr-session-misattribution.md`

#### issue ファイルの構造

```markdown
---
decision_type: spec | design | implementation | process
affected_paths:
  - internal/agent/codex/
  - cmd/agent-telemetry/setup.go
supersedes: [0023]
tags: [hooks, multi-agent, packaging]
closed_at: YYYY-MM-DD
---

# <タイトル>

Created: YYYY-MM-DD

## 概要

## 根拠

## 問題

## 対応方針
```

具体的なコード変更は PR 側に書く。ここに書くのは「なぜ対応する必要があるか」と「どの方針で進めるか」までに留める。

##### frontmatter フィールド

| フィールド | 型 | セマンティクス |
|---|---|---|
| `decision_type` | enum | `spec`（外部契約）/ `design`（内部設計）/ `implementation`（実装 detail）/ `process`（開発プロセス）。意思決定の層 |
| `affected_paths` | string[] | path snapshot。issue が effect を持つ code / docs path（`/` 終端でディレクトリを表す）。rename は逆引き側が `git log --follow --name-only` で旧 path 候補を解決して吸収するため、path 自体は更新しなくてよい（更新してもよい） |
| `supersedes` | int[] | 過去 issue 番号の配列（例: `[0023]`）。supersededBy への双方向参照は索引側で生成 |
| `tags` | string[] | free-form。当面は明示的な語彙統制を置かず、出現頻度から事後に整理する |
| `closed_at` | date | close 時に確定（`YYYY-MM-DD`）。open / pending では省略可 |
| `lint_ignore_broad` | string[] | `make intent-lint` の `affected_path_broad` 警告を path 単位で抑制（legitimate な broad path のみ。理由を YAML コメントで併記） |
| `lint_ignore_missing` | string[] | `make intent-lint` の `affected_path_missing` 警告を path 単位で抑制（rename 予定 / 将来生成される path に使う。理由を YAML コメントで併記） |

`affected_paths` は「`make intent P=<p>` で逆引きしたときに、この issue がヒットして欲しい path」を書く。粒度はファイル単位でもディレクトリ単位でもよい（broad な決定は親ディレクトリを、ピンポイントな決定は具体的なファイルを）。top-level dir 単独（例: `internal/`, `docs/`, `cmd/`）は逆引き時にノイズ massive になるため避ける（`make intent-lint` が警告する）。例外的に legitimate な broad / missing は `lint_ignore_broad` / `lint_ignore_missing` で **理由をコメント併記の上で** 抑制する。lint warning を放置すると索引としての信頼性が落ちるため、warning が出たら抑制 / 修正 / path の具体化のいずれかで対応する。

#### 粒度の目安

- **open 時**: 「なぜ取り組むか（根拠）」と「どの方針で進めるか（対応方針）」を中心に書く。実装手順や具体的なファイル名・関数名は書かない（PR / commit body に任せる）
- **close 時**: `## 解決方法` と `## 採用しなかった代替` は **要点だけ**。実装ログ・コード差分の説明・行レベルの判断は commit body と PR description に書く
- **目安**: open + close 合わせて 200 行を超えそうなら、本当に issue で記録すべき意思決定は何か再考する（実装詳細を切り出して別 issue / PR description に移す）
- **例外**: 規約そのものを作る meta issue（例: 0011 自身）は、規約と実装例がセットで価値を持つため高密度になることを許容する。通常の issue でこの密度を求めない

#### ライフサイクル

| 状態 | 場所 | 移動契機 | 同時に書く内容 |
|---|---|---|---|
| open | `issues/<id>-<cat>-<slug>.md` | 検出時に新規作成 | Created / 概要 / 根拠 / 問題 / 対応方針 |
| closed | `issues/closed/<id>-<cat>-<slug>.md` | 修正 PR の最終コミットで `git mv` | 末尾に `Completed: YYYY-MM-DD` と `## 解決方法` を追記 |
| pending | `issues/pending/<id>-<cat>-<slug>.md` | 設計判断・外部依存待ちで `git mv` | 末尾に `## Pending YYYY-MM-DD` と保留理由を追記 |
| reopen | `git mv issues/closed/<id>-... issues/<id>-...` | close 後に再発見 | 末尾に `## Reopen YYYY-MM-DD` と再発の経緯を追記。`## 解決方法` は経緯として残す |

#### コミットルール

- 番号が小さい open issue から順に対応する
- issue を新規作成したら同コミットで `issues/SEQUENCE` も `+1` する（失念防止）
- 1 issue の close ごとに 1 コミットを基本とする
- 関連 PR の説明冒頭に `issues/<id>-...` へのリンクを貼る（参考: [issues/closed/0022-design-pr-resolve-early-binding.md](issues/closed/0022-design-pr-resolve-early-binding.md) のリンク作法）。`.github/pull_request_template.md` がそのフォームを enforce する — PR description は薄く保ち、「なぜ」「方針」「却下案」は issue 側 / commit body 側に書く

#### バグ発見時のフロー

1. `issues/SEQUENCE` を読み次の番号を決める
2. `issues/<NNNN>-bug-<slug>.md` に再現手順・原因仮説・影響範囲を書く
3. `issues/SEQUENCE` を `+1` する
4. 1 コミットでまとめる（実装着手前に記録するためのコミット）
5. 別ブランチで修正 → 修正 PR の最終コミットで `git mv issues/<id>-... issues/closed/<id>-...` し、`Completed:` と `## 解決方法` を追記する

### テスト

```fish
go test ./...                          # 全テスト
make grafana-screenshot                # E2E: Grafana スクリーンショット検証
```

### ダッシュボード変更時の必須作業

- `grafana/dashboards/agent-telemetry.json` の表示を変更した場合は、必ず `make grafana-screenshot` を実行して README 用スクリーンショット（`docs/assets/dashboard-*.png`）も同じ変更に合わせて更新する（`grafana-screenshot` は `grafana-up-e2e` 経由で fixture データを使うので、画像が決定的に再現される）。
- スクリーンショット生成でポート競合が起きる場合は `GRAFANA_PORT=<unused-port> make grafana-screenshot` を使う。
- 実データで動作確認したい場合は `make grafana-up`（`~/.claude/agent-telemetry.db` を mount）。E2E と同じコンテナを使うので、切替時は片方が再作成される。

### docs site（`site/`）

- 開発ツール（Hugo extended / Go）は aqua で管理。初回 / 別マシンでは `aqua i` で揃える（`aqua.yaml` がリポジトリ直下）
- ローカル確認: `make docs-serve`（既定 port 1313、`HUGO_PORT=<n>` で上書き）
- ビルド検証: `make docs-build`
- theme は Hugo modules で導入（`site/go.mod` + `[module.imports]` in `hugo.toml`）。初回・theme 更新時は `make docs-mod-update`
- main push 時に `.github/workflows/docs-deploy.yml` が `gh-pages` ブランチへ自動 deploy する（CI も aqua 経由で同じ Hugo / Go バージョンを使う）
- PR を open / 更新するたびに `gh-pages/pr-preview/pr-<N>/` 以下に preview 版が deploy され、PR コメントに URL が投稿される。PR を close すると preview は自動削除（`rossjrw/pr-preview-action`）

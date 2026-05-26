# agent-telemetry

Claude Code および Codex CLI の PR 単位のトークン消費効率を追跡・可視化する計測ツール（hook・CLI・ダッシュボード）。

> 旧称 `hitl-metrics`。リネームの意思決定は [issues/closed/0021-spec-rename-hitl-metrics-to-agent-telemetry.md](issues/closed/0021-spec-rename-hitl-metrics-to-agent-telemetry.md) を参照。`doctor` は旧名の hook 登録も検出する（互換性のため）。

## ドキュメント構成

- `docs/spec.md` — 外部契約（CLI コマンド・hook 仕様・データモデル）
- `docs/metrics.md` — 計測フレームワーク（観察軸・解釈・OpenMetrics カタログ）
- `docs/design.md` — 実装方針と設計判断
- `issues/closed/` — 過去の意思決定記録（retro issue を含む正本）
- `docs/archive/adr/` — 旧 ADR 形式の意思決定記録（参照のみ）
- `site/content/explain/` — 仕組み解説 docs（visualize 主体、gh-pages へ配信）
- `site/content/setup/` — セットアップ・運用 docs（install / server / usage、gh-pages へ配信）

`docs/` は Claude が input として load する **reference** の正本（spec / design / metrics の 3 本）。`site/content/` は人間向けの **解説 docs** と **セットアップガイド** で、配信先は GitHub Pages (`https://ishii1648.github.io/agent-telemetry/`)。`site/content/explain/` が visualize 主体の how it works、`site/content/setup/` が install / server / usage の how-to。site から docs への参照は GitHub の markdown URL を直接張る（Hugo 内に取り込まない）。

新規の設計判断は ADR を作成しない。`docs/design.md` を更新し、Contextual Commits のアクション行で「なぜ」を記録する。大きな方針転換は `issues/<NNNN>-<cat>-<slug>.md` として retro / 設計 issue に起こす（実装が完了したら `issues/closed/` に move する）。

設計変更と実装はブランチを分ける必要はない。同一 PR で `docs/`・`site/`・コードを一緒に更新してよい。`docs/spec.md` / `docs/design.md` は変更が発生した PR で同期更新する（merge 後に main で別途追従する運用は取らない）。

## 開発規約

### 意思決定の記録方針

意思決定の primary store は `issues/`。frontmatter の `decision_type` / `affected_paths` で構造化される。`make intent P=<p>` はコードから関連 issue / コミットへの **逆引き索引** として使う（意図そのものは issue 本文・docs・commit body 側にあり、`--full` で本文を取得できる）。

- **複数コミット or 後続が参照しそうな決定** → `issues/<NNNN>-...` に書く（frontmatter で `decision_type` と `affected_paths` を埋める）
- 仕様の変更 → `docs/spec.md` を更新（issue にも `decision_type: spec` で記録）
- 実装方針の変更 → `docs/design.md` を更新（issue にも `decision_type: design` で記録）
- 1 コミット内で完結する判断 → Contextual Commits のアクション行で記録（issue 化不要）
- chore / リファクタなど意思決定を伴わない変更 → アクション行不要

### コミット

Contextual Commits を使用。Conventional Commits プレフィックス + 構造化されたアクション行でコミットの意図を記録する。

### ブランチ命名

`feat/`, `fix/`, `docs/`, `chore/` + kebab-case（例: `feat/add-sync-db`）

### バグ・課題管理

`issues/` 配下で Markdown ライフサイクル管理する。命名規則・SEQUENCE 運用・ディレクトリ構成・close/reopen/pending の手順は `AGENTS.md` の「issues について」セクションを正とする。CLAUDE.md と AGENTS.md の二重管理を避けるため、ルールの本体は AGENTS.md 側のみに置く。

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

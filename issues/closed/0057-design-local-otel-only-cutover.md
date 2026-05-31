---
decision_type: design
supersedes: []
related: [0055, 0050, 0053, 0054, 0032, 0029, 0030]
tags: [otel, grafana, sqlite, datasource, local, makefile, cutover, docs, screenshot]
closed_at: 2026-06-01
---

# ローカル可視化を otel(OSS) 一本化する正式移行（legacy SQLite datasource ローカル経路の撤去 + make ターゲット改名）

Created: 2026-06-01

## 概要

[0055](0055-design-local-otel-visualization-migration.md) でローカル可視化を otel(Collector→Mimir/Loki→Grafana) に一本化したが、SQLite を Grafana datasource として直接読む **ローカル利便レイヤ**（ルート `docker-compose.yaml` + frser-sqlite-datasource mount・site setup/local「方法 A/B/C」・SQLite 専用スクショ基盤 `e2e/screenshot.sh`）は legacy として残置していた。本 issue はその残置レイヤを **実削除**し、otel スタックを唯一のローカル可視化導線にする正式移行（cutover）を記録する。あわせて otel スタックの make ターゲットを canonical な `grafana-*` に改名する。

これは「SQLite + Grafana の完全 purge」ではなく「ローカル利便レイヤの撤去 + ローカル可視化の otel 化」である。

## 根拠

- 残置した SQLite datasource ローカル経路は otel 経路と機能が完全に重複し、dashboard 2 系統・スクショ 2 系統・compose 2 種を二重メンテする負債になっていた（[0055] ④ では暫定的に残置）。
- ローカルの第一級導線は既に otel スタック（[0055] ① runtime cutover, #106）、README ヒーローも OSS otel dashboard（[0055] ⑥, #107）に移行済みで、SQLite datasource ローカル経路を残す積極的理由が無くなった。
- SQLite 自体は **client 側 SoR**（append-only `events` + 派生 VIEW）として不可欠なので残す（[0054] / [0055] ③）。撤去するのは「SQLite を Grafana datasource として *直接読む* ローカル経路」だけ。
- 旧 SQLite 用ターゲットを撤去すると `grafana-*` という名前が空くため、ローカル可視化の事実上の主役である otel スタックのターゲットを `grafana-*` に改名し、利用者が「ローカルで Grafana を上げる」操作を直感的な名前で呼べるようにする。

## 対応方針（実施内容）

- **Makefile**: SQLite datasource 用のローカルターゲット（旧 `grafana-up` / `grafana-up-e2e` / `grafana-down` / `grafana-fixtures` / `grafana-screenshot`）と、それらのみが使う変数（`AGENT_TELEMETRY_DB` / `GRAFANA_PORT`〔旧定義〕 / `GRAFANA_E2E_PORT` / `COMPOSE_PROJECT_REAL` / `COMPOSE_PROJECT_E2E`）を削除。otel スタックのターゲットを旧 `oss-up` / `oss-down` / `oss-flush` / `oss-screenshot` から canonical な `grafana-up` / `grafana-down` / `grafana-flush` / `grafana-screenshot` に改名し、make 変数 `OSS_COMPOSE` / `OSS_GRAFANA_PORT` / `COMPOSE_PROJECT_OSS` も `GRAFANA_COMPOSE` / `GRAFANA_PORT` / `COMPOSE_PROJECT_GRAFANA` に改名（user 指定 Option 2: ターゲット + make 変数まで）。`lint-dashboard` / `intent` / `docs-*` / `build` 系は残置。
- **削除ファイル**: ルート `docker-compose.yaml`（旧 SQLite grafana-up 専用 + frser-sqlite-datasource mount）、`e2e/screenshot.sh`（SQLite render 専用）、スクショ分析 skill 2 つ（`.claude/skills/grafana-screenshot/`・`.agents/skills/grafana-screenshot/`）。
- **維持（重要）**: `e2e/gen_testdb_test.go`（`TestGenTestDB`）と `e2e/testdata/` は **otel スクショ（`e2e/oss-screenshot.sh`）が依存**するため削除しない（[0055] ⑤ の決定的スクショは fixture DB を `TestGenTestDB` で生成し HOME サンドボックス flush で Mimir/Loki に投入する）。`grafana/dashboards/agent-telemetry.json` と `grafana/provisioning/**` は **サーバ k8s 経路**（[0029] / [0030] が ConfigMap で配布）で生きているので残置。`lint-dashboard` ターゲット / workflow も残す。`e2e/oss-screenshot.sh` のファイル名・`deploy/oss-observability/` ディレクトリ名・`agent-telemetry-oss.json` 等の "oss" 命名は据え置き（改名は make コマンド面のみ）。
- **docs**: `site/content/setup/local/index.md` から legacy「方法 A/B/C」節を削除し otel 前提に統一。`docs/design.md` ## 可視化層 / E2E スクショ節、`docs/spec.md` 概要・サーバ送信、`site/content/setup/server/index.md` のクロス参照（「ローカル grafana-up と同じ」→「同梱 dashboard JSON を ConfigMap で配るので同一」）、`CLAUDE.md` / `AGENTS.md` のダッシュボード必須作業・テストコマンド、`deploy/oss-observability/README.md`、`examples/README.md`、open issue `issues/0003` の受け入れ条件を otel 前提 + `grafana-*` コマンド名へ追従。
- **README ヒーロー**: `docs/assets/dashboard-full.png` は [0055] ⑥（#107）で既に OSS otel dashboard 画像。本 cutover で OSS dashboard JSON は変更しないため画像は据え置き（再生成が必要な場合は `make grafana-screenshot` で決定的に再現可能）。

## 解決方法

上記をすべて実施し、`go test ./...` / `make lint-dashboard` が通ることを確認して close する。サーバ k8s 経路の SQLite 降格は引き続き別件 [0032](../pending/0032-design-grafana-storage-decoupling.md)。

---
decision_type: design
tags: [otel, grafana, mimir, loki, sqlite, datasource, local, screenshot, readme, docs]
related: [0040, 0050, 0053, 0054]
closed_at: 2026-06-01
---

# ローカル可視化を SQLite datasource から otel+grafana へ移行する（runtime cutover が本体・README/スクショは末端）

Created: 2026-05-31

## 概要

SQLite を Grafana datasource として参照する経路を廃し、可視化を otel+grafana (Mimir/Loki) に一本化する方針（[0053](0053-design-otel-dashboard-tier2-tier3-restore.md) / [0054](0054-design-abandon-concurrency-metrics-otel.md)）の **ローカル単独利用（個人ローカル経路）分** を追跡する。現状この経路は **runtime としてまだ移行できていない**：既定のローカル可視化は依然 SQLite + Grafana で、otel はオプトイン export + 検証用 compose に留まる。README ヒーロー画像の差し替えはこの移行の**末端**であり、本体は runtime の cutover である。

## 根拠

- 一本化の決定はローカル経路（[0053] 背景の「個人ローカル ④」）も対象に含む。だが実装済みは (a) OTLP export 配管（**オプトイン**, [0040](0040-design-pluggable-otlp-export-backends.md)/0042/0043）、(b) OSS 検証 compose（[0050](0050-feat-oss-observability-local-compose.md)）、(c) otel dashboard の Tier 1+2 パネル（[0053]）まで。
- 既定のローカルデータフローは **SQLite のまま**：hook → `~/.claude/agent-telemetry.db` → Grafana が frser-sqlite-datasource で `grafana/dashboards/agent-telemetry.json` を読む（`docs/spec.md`「サーバ送信はオプトイン。設定なしのローカル単独利用は従来通り」）。
- `site/content/setup/local/index.md` は otel / collector / flush に一切触れず、純粋に SQLite+Grafana 導入を案内している。
- `deploy/oss-observability/` は compose 自身のコメントどおり「別系統の**検証** dashboard」（:13001）であり、ローカル既定デプロイではない。データ投入は `flush → collector(:4318) → Mimir/Loki` を手で配線する必要がある。

## 問題

ローカル経路の移行に必要な、未実装の作業（README は最後の 1 項）：

1. **runtime cutover（本体）**: ローカルで otel 可視化を「既定 or 第一級の文書化済み選択肢」にする。最小構成（collector→Mimir/Loki→Grafana）を `make` 1 コマンド級で立ち上げ、ローカル hook の export を摩擦なく流す導線を用意する（現状 export はオプトインで多コンポーネント手配線）。
2. **Tier 3 parity**: ~~session-grain export（[0053] の Tier 3）が無く、otel dashboard は SQLite dashboard と feature parity に達していない~~ → **完了（2026-05-31）**: `weekly_session_metrics` を `agent_weekly_session_*` gauge で export し、OSS dashboard に top-level sessions / 週別 token・tokens per session・ask_user_question per session を追加した（[0053] close）。週次は client 側集約＋`week_start` label で weekday-0 バケット問題を解決。
3. **SQLite の位置づけ確定**: SQLite を client 側 SoR に降格し Grafana から読まない、を docs/コードで明文化（VIEW は残置 = [0054] 既定）。
4. **docs 追従**: `site/content/setup/local/index.md` を otel 前提へ書き換え（SQLite datasource 手順の扱いも整理）。
5. **決定的スクショパイプライン**: OSS dashboard には fixture→Mimir の決定的スクショ手段が無い（`docs/design.md`）。`make grafana-screenshot` / `e2e/screenshot.sh` は SQLite dashboard 専用。
6. **README ヒーロー差し替え（末端）**: 上記が揃ってから `docs/assets/dashboard-full.png` を OSS dashboard 画像へ差し替える。

## 対応方針

- 上の 1→6 の順に進める。README/スクショ（5・6）は runtime cutover（1）と parity（2）の **後** に行う。先に画像だけ替えるのは負債（非再現・parity 未達・PII〔メール/private repo 名〕混入）。
- 規模が大きいので、実装着手時に runtime cutover（1）と Tier 3（2, [0053]）を独立 PR / 子 issue に切ることを検討する。Tier 3（2）は本 PR で完了済み。runtime cutover（1）は別 PR が担当。本 issue はローカル経路移行の傘として全体像と順序を保持する（傘 issue 自体は全項完了まで open）。
- それまで README ヒーローは現状の SQLite dashboard 画像を据え置く（既に [0054] で post-concurrent-removal 状態に再生成済み。これ以上 SQLite スクショは磨かない。PR #102 closed の教訓）。
- **着手状況（2026-05-31）**: ① runtime cutover に着手（branch `feat/issue-0055-local-otel-runtime-cutover`）。`make oss-up` / `oss-down` / `oss-flush` で Collector→Mimir/Loki→Grafana の最小スタックを 1 コマンド起動し、token 不要の `config.toml.example` で hook export（flush → Collector:4318）を流す導線を整備。②Tier3 / ③SQLite 降格明文化 / ④docs 全面書換 / ⑤決定的スクショ / ⑥README ヒーロー差し替えは別 PR。本傘 issue は B/C/D 完了まで open のまま。

Completed: 2026-06-01

## 解決方法

傘 issue の全 6 項（①②は先行 PR、③④⑤⑥ を本 PR）が完了したので close する。

- **① runtime cutover** — #106 でマージ済み（`make oss-up` / `oss-down` / `oss-flush`）。
- **② Tier 3 parity** — #105（[0053] Tier 3）でマージ済み（`agent_weekly_session_*` gauge + OSS dashboard パネル）。
- **③ SQLite の位置づけ確定** — `docs/spec.md`「概要」「サーバ送信」と `docs/design.md ## 可視化層`（新節「otel 一本化と SQLite の client 側 SoR 降格」）で、SQLite を client 側 SoR（append-only `events` + 派生 VIEW）に降格し、otel 経路の Grafana は直接読まないことを明文化。VIEW / DDL は残置（[0054] 既定）。SQLite datasource 直結は legacy 経路として残す。
- **④ docs 追従** — `site/content/setup/local/index.md` を otel 前提（`make oss-up` → Mimir/Loki/Grafana、credential 不要 collector target、`make oss-flush`、OSS dashboard）に全面書換。SQLite datasource 手順（方法 A/B/C）は「legacy: SQLite datasource を直接読む経路」として残置整理。site→docs 参照は GitHub markdown URL。
- **⑤ 決定的スクショパイプライン** — `make oss-screenshot` / `e2e/oss-screenshot.sh` を新設。SQLite e2e と同じ fixture（`e2e/testdata/`）を **HOME サンドボックス flush**（実 `~/.claude`・実 config・実 state を触らない）で Collector→Mimir/Loki に決定的投入し、Grafana `/render` で撮る。スクショ用 overlay `docker-compose.screenshot.yaml`（Image Renderer を本番 `oss-up` に積まず overlay で追加）と、OSS compose の collector/Mimir/Loki host port の env 可変化で `make oss-up` と別 port・別 project で並走可能。`down -v`→up→単発 flush で Loki ログの重複を排除し、2 回実行してデータ（sent=83 / pr_series=13 / session_series=3 / distinct series=13）が同一になることを確認。メモリ記載の落とし穴（v0.0.6 flush 非対応＝ソースビルド / token 必須＝collector は credential 不要 target / VIEW 欠落＝flush の `EnsureViews` / Loki 古サンプル却下＝`reject_old_samples: false` / Mimir tenant＝`multitenancy_enabled: false` で `X-Scope-OrgID` 不要）を全て踏まえた。
- **⑥ README ヒーロー差し替え** — ⑤ のパイプラインで `docs/assets/dashboard-full.png` を OSS otel dashboard 画像に再生成。fixture ベースで PII（実メール / private repo 名）混入なし（synthetic な alice/bob/carol@example.com と public repo の PR URL のみ）。README alt text / 本文と `CLAUDE.md`「ダッシュボード変更時の必須作業」を otel 前提・スクショ 2 系統（OSS=ヒーロー / SQLite=legacy）に追従。

**既知の見え方**: Tier 2「週別 merged PR 数」timeseries（panel 20）は単発 fixture flush では構造的に "No data"（gauge は flush 時刻に打たれ、`[1w]` epoch 整列 step の最終評価点が `now` の手前に落ちるため）。これは Tier 2 weekly trend が縦断利用前提の近似（[0053]）である帰結で、run をまたいで安定的に空＝決定性は保たれる。`week_start` ラベルで集計する Tier 3 週次 barchart は単発でも全週出る。詳細は `docs/design.md ## 可視化層 > E2E スクリーンショット`。

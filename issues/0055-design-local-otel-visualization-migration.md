---
decision_type: design
tags: [otel, grafana, mimir, loki, sqlite, datasource, local, screenshot, readme, docs]
related: [0040, 0050, 0053, 0054]
---

# ローカル可視化を SQLite datasource から otel+grafana へ移行する（runtime cutover が本体・README/スクショは末端）

Created: 2026-05-31

## 概要

SQLite を Grafana datasource として参照する経路を廃し、可視化を otel+grafana (Mimir/Loki) に一本化する方針（[0053](closed/0053-design-otel-dashboard-tier2-tier3-restore.md) / [0054](closed/0054-design-abandon-concurrency-metrics-otel.md)）の **ローカル単独利用（個人ローカル経路）分** を追跡する。現状この経路は **runtime としてまだ移行できていない**：既定のローカル可視化は依然 SQLite + Grafana で、otel はオプトイン export + 検証用 compose に留まる。README ヒーロー画像の差し替えはこの移行の**末端**であり、本体は runtime の cutover である。

## 根拠

- 一本化の決定はローカル経路（[0053] 背景の「個人ローカル ④」）も対象に含む。だが実装済みは (a) OTLP export 配管（**オプトイン**, [0040](closed/0040-design-pluggable-otlp-export-backends.md)/0042/0043）、(b) OSS 検証 compose（[0050](closed/0050-feat-oss-observability-local-compose.md)）、(c) otel dashboard の Tier 1+2 パネル（[0053]）まで。
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

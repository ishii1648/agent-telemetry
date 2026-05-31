---
decision_type: design
tags: [retro, dashboard, sqlite, grafana, visualization]
closed_at: 2026-03-15
---

# 純 Python ダッシュボード → SQLite + Grafana

Created: 2026-03-15
Retro-converted: 2026-05-10 (from docs/history.md §2; date approximate, around ADR-015 adoption)

## 概要

`permission-ui-server.py` が集計と表示を 1 ファイルに混載していたダッシュボードを、SQLite を中間 store としつつ Grafana を可視化基盤とする構成に置き換えた。

## 根拠

- 集計ロジックと UI が単一スクリプトに混在し、メトリクス追加のたびに HTML テンプレートを書き換える負担が大きかった
- 「任意の日付範囲で PR 別集計」が主要な用途で、Prometheus の時系列モデルとは粒度が合わなかった（PR は離散的な単位で時系列ではない）
- Grafana の SQLite データソースを使えば、JSONL → SQLite の単純変換だけで任意 SQL による集計と可視化が両立できる

## 問題

- Prometheus を採用すると、PR 単位の集計を再構築するために exporter 経由で時系列化する必要があり、本来の用途と乖離する
- 純 Python の HTML 出力を維持すると、新しい panel 種別（heatmap / table / bar gauge）を実装するたびにフロントエンド作業が発生する

## 対応方針

- 集計層: SQLite を中間 store とし、JSONL → SQLite の sync 処理を `internal/syncdb/` に集約
- 可視化層: Grafana + SQLite データソースを provisioning で配備
- panel 設計は Grafana JSON で記述し、SQL クエリで集計を完結させる

## 解決方法

- `internal/syncdb/` で session-index.jsonl と transcript JSONL を SQLite に sync
- `grafana/dashboards/agent-telemetry.json` で dashboard を JSON 管理
- `grafana/provisioning/datasources/` で SQLite データソースを provisioning 配備
- 集計クエリは Grafana panel の SQL で完結し、Go 側は raw データ供給に専念

## 採用しなかった代替

- **Prometheus + exporter**: PR 単位の離散集計と時系列モデルが合わず、却下
- **純 Python HTML を維持**: panel 追加コストが高止まりし、metrics 体系の進化を妨げる
- **DuckDB**: 当時 Grafana datasource プラグインが未成熟だった（SQLite は標準対応）

## 参照

- [ADR-015](../../docs/archive/adr/015-dashboard-visualization-backend-selection.md)

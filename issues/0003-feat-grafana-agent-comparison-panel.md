---
decision_type: implementation
tags: [grafana, dashboard, multi-agent]
---

# Grafana ダッシュボードに agent 別比較 stat パネルを追加

Created: 2026-05-08
Model: Opus 4.7

## 概要

Codex CLI 対応の一環として、Grafana ダッシュボード `grafana/dashboards/agent-telemetry.json` に agent 別の比較 stat パネル（avg tokens / PR と PR / 1M tokens）を追加する。

## 根拠

`sessions.coding_agent` で claude / codex を区別する仕組みは入っているが、ダッシュボード上で agent 別の token 効率を直感的に比較する手段が無い。「Claude と Codex のどちらで PR を回すと効率が良いか」を一目で確認できるようにするため、avg tokens / PR と PR / 1M tokens を agent 別に並べる stat パネルが必要。

## 対応方針

- `grafana/dashboards/agent-telemetry.json` のヘッドラインセクションに stat パネルを追加
  - `avg_tokens_per_pr` を `coding_agent` で GROUP BY して並べて表示
  - `pr_per_million_tokens` を同様に並べて表示
- パネル配置はヘッドラインセクション末尾（trend / pr 詳細セクションの前）
- `make grafana-screenshot` を実行して `docs/assets/dashboard-*.png` を更新する

## 受け入れ条件

- [ ] agent 別比較 stat パネルが追加され、`make grafana-up-e2e` で表示確認できる
- [ ] `make grafana-screenshot` で `docs/assets/dashboard-*.png` を更新済み

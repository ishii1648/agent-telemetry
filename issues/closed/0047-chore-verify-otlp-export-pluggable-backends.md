---
decision_type: process
affected_paths:
  - internal/serverclient/
  - internal/serverpipe/
  - deploy/otel-collector/
  - deploy/datadog/
depends_on: [0040, 0042, 0043, 0044, 0045, 0046]
tags: [otel, export, pluggable-backend, verification, e2e, datadog]
closed_at: 2026-05-30
---

# OTLP export pluggable backend の e2e 動作検証

Created: 2026-05-30

## 概要

0040 ファミリー（[0040] 設計 / [0042] target 配列 / [0043] gauge / [0044] attribute
分類 / [0045] metrics representation / [0046] 責務分担）が main に merge されたのを受け、
実 Datadog アカウント無しで end-to-end の動作検証を行った。検証ログ・スクリプト・
結果は `.outputs/claude/otlp-verify/` に保存（summary は `SUMMARY.md`）。

## 検証結果（全項目 PASS）

1. **ローカル e2e（server 経由）**: 既存 server（OTLP Logs receiver + SQLite ingest）
   互換 OK。per-target cursor の独立前進を実証（srvB を落として新イベント追加 →
   flush で srvA だけ前進・srvB は cursor 据え置き → srvB 復帰で該当 target のみ
   catch-up、両者が client と一致）。gauge は PR 単位（`pr_url`/`coding_agent`/
   `user_id`/`task_type`/`model`）で出力され `session_id` は tag に一切現れない。
   - 注: 実 `agent-telemetry-server` は `/v1/logs` のみで `/v1/metrics` を持たない
     （gauge は外部 backend 向け、server 経路は SQLite VIEW でローカル集約）。
     0040/0046 の責務分担どおりで bug ではない。gauge 流通は mock receiver で確認。
2. **collector レシピ（Datadog なし）**: contrib collector を docker 起動し
   client → collector → file exporter で raw events + gauge が通過。attribute 整形
   （`service.name`/`deployment.environment` 付与、`session_id`/`branch`/`pr_title` 等の
   drop、`is_subagent`/`task_type` の派生、`env` alias）がすべて processor で実動作。
3. **direct レシピ（protobuf wire）**: `encoding=protobuf` + `dd-api-key` header の
   target で dry-run の path 選択を確認、mock endpoint への送信を OTLP protobuf として
   decode 成功（logs 112件 / metrics 16 gauge）。`${DD_API_KEY}` の env 展開・raw-token
   scheme（Authorization 非漏洩）も確認。httpbin でも外部到達を確認。
4. **既存 dashboard 非破壊**: `make grafana-screenshot` で全パネル描画 OK。
   `dashboard-pr-scorecard.png` は committed と md5 一致。`dashboard-full.png` の byte 差は
   `now-30d..now` 相対窓 + fixture `refTime=now` による時間軸アーティファクト（同一 URL
   連続レンダは byte 一致＝決定的、dashboard JSON / schema VIEW は 0040 ファミリーで
   未変更）であり regression ではない。
5. **doctor / 設定検証**: 重複 `target_id` / `id` 欠落は `flush` 起動時に fail-fast
   （exit 1）。一方で **`encoding` / `signals` の不正値は未検証で silent に通る**問題を
   発見 → [0048]。collector recipe の OTTL context-prefix / `:latest` pin → [0049]。

## 発見した課題

- [0048] export target config の `encoding` / `signals` 値が未検証（bug）
- [0049] collector レシピの OTTL context prefix 無し / image `:latest` pin（chore）

いずれも検証スコープ（送信経路・representation・cursor・非破壊）の根幹は PASS で、
上記は周辺の validation / recipe 将来互換に関する指摘。

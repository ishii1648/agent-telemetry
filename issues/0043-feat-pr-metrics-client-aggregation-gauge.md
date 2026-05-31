---
decision_type: implementation
depends_on: [0040, 0042]
tags: [otel, export, metrics, gauge, pr-metrics]
---

# `pr_metrics` の client 側集約 + OTLP Metrics gauge 送信

Created: 2026-05-30

## 概要

[0040] の child issue (2)。log-metric backend は record 間 join をしないため、現行 SQLite VIEW（`pr_metrics`、cross-event join + latest-wins + sum）を raw events だけで再現できない。よって **client がローカル VIEW を評価し、PR 単位（`pr_url` / `coding_agent` / `user_id`）の pre-aggregated 値を OTLP Metrics gauge（last-value）として送る**。

## 根拠

設計判断は [0040] 本文（A、`pr_metrics` 集約の節）に集約されている。要点:

- 当初の pre-aggregated 却下の前提（「集約はユーザ側で組める」）が join 不可で崩れたための翻意
- **gauge は冪等 upsert ではない**: 同一 timestamp & dimensions の点のみ last-write-wins。新 timestamp 再送は series 内の別点。range 集計で `last` を取る前提に依存し、naive な SUM は二重計上
- `session_id` を tag に出さないので cardinality は PR 数止まり
- gauge tag は VIEW projection に等しく（`pr_url` / `coding_agent` / `user_id` / `task_type` / `model`）、`repo` / `pr_state` / `branch` を載せたいなら **VIEW の projection 拡張が前提**

## 対応方針

1. [0040] の spike（gauge temporality / timestamp 設計）を実機検証し、送信 timestamp の決め方を確定（PR 最終更新時刻 vs 送信時刻、再計算値の過去レンジ扱い、Datadog の delta 要件との関係）
2. VIEW projection の拡張要否を判断（`repo` / `model` 等を gauge tag に載せるか）。必要なら `internal/syncdb/schema/schema.sql` を更新
3. client にローカル VIEW を評価して gauge LogRecord（Metrics signal）を組み立てる経路を追加。target 配列（[0041]）の representation = `pr_metrics_gauge` として既存 cursor 機構に乗せる
4. `session_count` / `tokens_per_session` / `tokens_per_tool_use` 等の ratio もこの行に含める（backend 側 formula で出せるようにする）

## 触らない

- raw events（Logs）の export target 拡張は [0041]
- backend 側の dashboard / monitor 構築（ユーザ責務）

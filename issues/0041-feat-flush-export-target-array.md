---
decision_type: implementation
affected_paths:
  - internal/serverclient/
  - cmd/agent-telemetry/
  - docs/spec.md
  - deploy/otel-collector/
depends_on: [0040]
tags: [otel, export, pluggable-backend, datadog, collector]
---

# flush を export target 配列に拡張（OTLP/HTTP pluggable export）

Created: 2026-05-30

## 概要

[0040] の child issue (1)。現行 `[server].endpoint + 固定 Bearer + JSON OTLP Logs`（`internal/serverclient/`）を、**endpoint + 可変 auth header + encoding/protocol + signal/representation を持つ export target の配列**に拡張する。direct / collector の 2 デプロイレシピを docs に同梱し、Datadog をリファレンス実装として 1 本通す。

## 根拠

設計判断は [0040] 本文（A）に集約されている。要点だけ再掲する:

- target 配列化に伴い state は `{target_id: last_flushed_sequence}` の map に拡張（per-target cursor、独立前進）。`target_id` は **設定の安定 ID**（URL 変更で別 target 扱いにならない）
- endpoint モデルは「base + signal path を補完」/「signal ごとの完全 URL」のいずれか 1 つを spec で確定する（実装時の解釈ブレ防止）
- **Datadog direct は protobuf + `DD-API-KEY` を要求**する。現行の手組み JSON poster では送れないため、**OTLP SDK / protobuf exporter への切替**を伴う
- **collector レシピは client の既存 JSON exporter のまま**動かせる（Collector が protobuf + `DD-API-KEY` 変換を担う）
- 個人 / 小チームは direct + submit-only credential、team / 多 client は collector で credential を 1 箇所集約（client は秘密なし＝RUM 等価）

## 対応方針

実装着手前に [0040] の spike を回し、encoding/protocol 要件・auth header の正規化・endpoint モデルを確定してから着手する:

1. config schema（`[[export]]` または同等）の仕様を `docs/spec.md` に追記
2. `internal/serverclient/` を target 配列ベースに refactor、state の per-target cursor 化と migration
3. OTLP SDK 切替（Datadog direct 用 protobuf path）。JSON path も既存互換として残す
4. `deploy/otel-collector/` に collector レシピ（router fanout、Datadog exporter、SQLite ingest は任意宛先）を同梱
5. docs（site）に direct / collector の how-to を追加。Datadog を例示

## 触らない

- gauge 送信（child issue [0042] で）
- attribute の意味分類の spec 反映（child issue [0043] で）
- raw events 側の representation は本 issue 範囲。`pr_metrics` gauge representation は [0042] で別 cursor として加わる

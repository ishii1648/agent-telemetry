---
decision_type: implementation
depends_on: [0040]
tags: [otel, export, pluggable-backend, datadog, collector]
closed_at: 2026-05-30
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

- gauge 送信（child issue [0043] で）
- attribute の意味分類の spec 反映（child issue [0044] で）
- raw events 側の representation は本 issue 範囲。`pr_metrics` gauge representation は [0043] で別 cursor として加わる

Completed: 2026-05-30

## 解決方法

着手前の必須 spike（[0040] 積み残し）を文献ベースで確定し、実装した。

### spike の結論

- **Datadog OTLP intake の encoding/protocol**: **Logs intake**（`https://otlp.datadoghq.com/v1/logs`）は **protobuf 必須** + `dd-api-key` header（[docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/logs/)）。**Metrics intake**（`/v1/metrics`）は JSON/protobuf 両対応・gauge は last-value（[0043] 用、[docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/)）。よって現行の手組み JSON poster では Datadog direct logs を送れず、protobuf encoding path の追加が必須と確認
- **endpoint モデル**: Datadog も自前サーバも `base + signal path（/v1/logs）` 補完で一致 → **「base + signal path 補完」に固定**し `docs/spec.md` に明記（「signal ごとの完全 URL」案は不採用）
- **service.name**: 単一 `service.name=agent-telemetry` + `coding_agent` 次元タグ分離を採用（[0040] B が `coding_agent` を低 cardinality 次元タグに分類済み。agent 別 service は service map を断片化するため不採用）

### 実装

- **config（`internal/serverclient/config.go`）**: `[server]` 単一宛先を `[[export]]` 配列（`id` / `endpoint` / `token` / `auth_header` / `auth_scheme` / `encoding` / `signals`）に拡張。`[server]` は安定 ID `"server"` の単一 target に後方互換正規化。`token` は `${VAR}` 環境変数展開対応。`id` 重複・`id` 欠落は起動エラー
- **per-target cursor（`internal/backfill/state.go`）**: state に `flush_cursors: {target_id: last_flushed_sequence}` map を追加。各 target は送信成功時のみ独立前進（部分失敗で他 target に波及しない）。旧 `last_flushed_sequence` は残し、`"server"` cursor 不在時の seed に使う（アップグレードで全履歴を再送しない）
- **encoding（`internal/serverclient/proto.go` 新規）**: OTLP proto types（`go.opentelemetry.io/proto/otlp` + `google.golang.org/protobuf`）のみ追加し、既存の JSON payload 構造体 → proto message 変換 + `proto.Marshal` で protobuf encoding path を実装。full OTel SDK は不採用（per-target cursor / batching / partialSuccess 契約を温存し encoding だけ差し替えるため）。JSON path は自前サーバ / Collector 向けに温存
- **flush（`internal/serverclient/flush.go`）**: `runFlushForAgent` を target ループにリファクタ。target ごとに cursor → `LoadEvents` → `splitEventBatches` → encoding 分岐（json / protobuf）+ 可変 auth header/scheme で POST → 成功時のみ cursor 前進。`FlushResult` を agent × target の二段構造に拡張し `Summarize` を更新
- **collector レシピ（`deploy/otel-collector/`）**: OTLP receiver → batch → `datadog` exporter（protobuf + `DD-API-KEY` 変換は Collector が担う）+ 任意で自前サーバへ fanout する `config.yaml` / `docker-compose.yaml` / `README.md`。client は既存 JSON exporter のまま
- **docs/spec.md**: 「サーバ送信」節を export target 配列に拡張。endpoint モデル固定・`auth_header`/`auth_scheme`/`encoding`/`signals` の意味と既定値・`flush_cursors` per-target cursor・protobuf encoding・direct/collector 2 レシピ・`service.name` 固定を反映

### 検証

`go test ./...` 全パス（config 配列パース・後方互換 `[server]`・per-target cursor 独立前進/部分失敗・legacy seed・protobuf marshal round-trip・NoConfig）。`go vet` / `gofmt` clean。CLI スモークテストで 2 target（json/protobuf）の dry-run 二段出力と NoConfig メッセージを確認。`make intent-lint` clean。

## 採用しなかった代替

- **full OTel SDK（`otlploghttp` exporter）採用**: exporter が batching/retry を内包し JSON encoding 非対応のため、per-target cursor / partialSuccess の自前制御と衝突する。proto types のみ追加して encoding だけ差し替える方式に決定
- **endpoint を「signal ごとの完全 URL」で設定**: Datadog / 自前サーバとも base + signal path で一致するため、解釈ブレ防止に base-endpoint 補完モデル 1 つへ固定
- **agent 別 `service.name`（agent-telemetry-claude 等）**: service map を断片化し dashboard を agent ごとに複製しがち。単一 service + `coding_agent` 次元タグに決定

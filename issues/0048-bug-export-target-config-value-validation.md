---
decision_type: implementation
affected_paths:
  - internal/serverclient/config.go
  - internal/serverclient/flush.go
  - internal/doctor/doctor.go
tags: [otel, export, config, validation, datadog]
---

# export target config の `encoding` / `signals` 値が未検証で不正値が silent に通る

Created: 2026-05-30

## 概要

`LoadConfig`（`internal/serverclient/config.go`）は export target 配列のうち
**`id` の重複・欠落は fail-fast で弾く**（`assertUniqueIDs` / `missing required id`）が、
**`encoding` と `signals` の値は allow-list 検証していない**。結果、不正値が
silent に受理され、誤った挙動／誤解を招く出力になる。0040 ファミリー（OTLP export
pluggable backend）の e2e 検証（項目5）で発見。

## 再現

### (a) 不正 `encoding`

```toml
[[export]]
id = "badenc"
endpoint = "http://127.0.0.1:9"
token = "t1"
encoding = "xml"
```

```
$ agent-telemetry flush --agent claude --dry-run
flush[claude→badenc] dry-run: sent=112 ... encoding=xml   # exit 0
```

- `normalizeTarget` は空のときだけ `"json"` を補完し、非空値は素通し。
- `encodeBatch`（`flush.go:682`）の `switch` は `case "protobuf"` 以外を
  `default:`（=JSON）で処理するため、`"xml"` は **実際には JSON で送信**される。
- 一方 dry-run/summary は `tr.Encoding`（=`"xml"`）をそのまま表示するので、
  **「encoding=xml と表示されるが中身は JSON」という wire とラベルの不一致**が起きる。
  Content-Type も `application/json` になり、protobuf 必須の backend（Datadog
  direct）に対して気付かず誤送信し得る。

### (b) 不正 `signals`

```toml
[[export]]
id = "badsig"
endpoint = "http://127.0.0.1:9"
token = "t1"
signals = ["traces"]
```

```
$ agent-telemetry flush --agent claude --dry-run
flush[claude]: export target 設定なし — eligible=112 sent=0 (skipped network)   # exit 0
```

- `"traces"` は `SendsLogs()` / `SendsMetrics()` のどちらにも該当せず、target が
  `logsTargets`/`metricsTargets` のどちらにも入らない（`flush.go:199-210`）。
- 全 target が representation を持たないと `NoConfig=true` 経路に落ち、
  **「export target 設定なし」と誤報告**される。ユーザは export を設定したつもりでも
  **何も送られず、エラーも warning も出ない**（silent no-op）。

### doctor は検知しない

`doctor` は `checkConfigPath` で **config の path 存在のみ**を見ており、
`LoadConfig` を呼ばないため、上記いずれも検知しない（重複 id すら doctor では
出ない。fail-fast するのは `flush` 起動時のみ）。

## 影響

- `encoding` 誤値: protobuf 前提 backend に JSON を送って気付けない／ラベルが嘘になる（中）
- `signals` 誤値: export 設定が silent に無効化され「設定なし」と誤報告（中〜低）
- doctor が export config の妥当性を一切見ない（運用上の盲点）

## 対応方針（案）

- `LoadConfig`（または `normalizeTarget`）で `encoding ∈ {json, protobuf}`、
  `signals ⊆ {logs, metrics}` を allow-list 検証し、`id` 検証と同様に
  fail-fast させる（未知値は明示エラー）。
- 併せて `doctor` に export target の lint（`LoadConfig` を呼んでエラーを表示、
  representation を持たない target の警告）を足すか検討する。
- 関連実装: [0042] flush export target 配列化。

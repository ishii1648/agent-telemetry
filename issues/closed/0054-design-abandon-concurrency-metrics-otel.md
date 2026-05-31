---
decision_type: design
tags: [otel, grafana, concurrency, dashboard, fidelity-loss, tier4]
related: [0050, 0053]
---

# 並列度メトリクス（peak / avg concurrent）は otel+grafana では諦める

Created: 2026-05-31

## 決定

SQLite を Grafana datasource として参照する経路を廃し可視化を otel+grafana に一本化する方針（決定の全体像は本 issue 群 ＋ dispatch の `sqlite-removal-timing` 検討）に伴い、**`session_concurrency_daily` 由来の peak / avg concurrent パネルは otel+grafana では復元せず abandon する**。SQLite VIEW 自体（`session_intervals` / `session_concurrency_daily` / `session_concurrency_weekly`）は client 側 SoR の一部として残してよいが、**メイン dashboard からは落とす**。

## 理由（原理的な fidelity 喪失）

`session_concurrency_daily` は、各セッションの開始時刻について「その時刻に active な（`started_at <= t < ended_at`）セッション数」を相関サブクエリで数え、日次で AVG / MAX を取る（`internal/syncdb/schema/schema.sql`）。これは **区間（interval）の重なり**の計算であり、以下の理由で OTLP Metrics gauge の flush 時点スナップショットからは再構成できない:

1. **任意レンジ性**: Grafana のレンジ境界は query-time に決まる。「任意のレンジでの peak 同時実行数」は、flush 時点で値を固定する gauge では保持できない（どのレンジを選ぶか送信時に知り得ない）。
2. **非分解性**: peak は bucket をまたいで単純合成できない。日次 peak を pre-bucket して export すれば `max_over_time` で「**日次 peak の最大**」までは出せるが、これは「レンジ全体の真の peak」とは異なり、日境界をまたぐピークやサブ日次のピークを取りこぼす。
3. これは「PromQL への移植コスト」ではなく **計算モデルの非互換**。client 側で interval-overlap を gauge 化しても上記 1〜2 は解けない。

## 影響

- メイン dashboard の `peak concurrent` stat（旧 `grafana/dashboards/agent-telemetry.json` id=26）は otel 版に持ち込まない。
- 並列度の観察軸（`docs/metrics.md` の `agent_concurrent_sessions_{avg,peak}`）は **SQLite + ローカル分析**でのみ参照可能、という制約を `docs/design.md` に明記する。
- 将来どうしても必要になった場合の選択肢（受容コスト付き）: (a) 日次 peak の pre-bucket gauge を export し「日次 peak の max」で妥協、(b) raw events(Loki) の start/end timestamp から query-time に近似計算（LogQL では現実的でない）、(c) 並列度専用の別 backend。いずれも本 issue 時点では採らない。

## 採用しなかった代替

- **日次 peak gauge を export して近似で残す**: 「日次 peak の max」は真の任意レンジ peak と意味が異なり、誤読を招く。明示的に abandon する方が誠実
- **session_concurrency VIEW ごと削除**: client SoR / ローカル分析では有用なので VIEW は残す。落とすのは「メイン dashboard への表示」だけ

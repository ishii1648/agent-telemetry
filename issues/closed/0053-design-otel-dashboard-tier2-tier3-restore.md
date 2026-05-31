---
decision_type: design
tags: [otel, grafana, mimir, dashboard, metrics-export, tier2, tier3]
related: [0040, 0043, 0050, 0054]
closed_at: 2026-05-31
---

# otel+grafana 一本化後に Tier 2/3 パネルを復元する（session-grain export ＋ 集計表現）

Created: 2026-05-31

## 背景

SQLite を Grafana datasource として参照する経路（server/team ③・個人ローカル ④ の両方）を廃し、可視化を **otel+grafana（Mimir/Loki）に一本化**する決定をした（決定記録は [0054]、SQLite の責務分解は dispatch の `sqlite-removal-timing` 設計検討）。SQLite（events SoR ＋ `pr_metrics` 等の集約 VIEW）は client 側に残すが、Grafana は直接読まない。

一本化にあたり、メイン dashboard が依存していた指標を「prom/loki で残せるか」で 4 ティアに整理した。**Tier 1（PR 単位 gauge ＋ raw logs）のみを当面のメイン dashboard に表示**し、Tier 2/3 は本 issue で follow-up とする（Tier 4 は [0054] で abandon）。

### ティア整理（メイン dashboard の各パネル）

| パネル | 依存 VIEW | grain | 現状 export | ティア |
|---|---|---|---|---|
| PR スコアカード / PR 別 session_count / tokens_per_tool_use | `pr_metrics` | PR 単位 | ✅ `agent_pr_*` | **Tier 1（表示済み）** |
| raw events | events | 生 | ✅ Loki | **Tier 1（表示済み）** |
| total tokens / merged PRs / PR per 1M / 週別 merged PR 数 | `sessions`/`pr_metrics`/`weekly_pr_metrics` | PR or 混在 | △ | **Tier 2** |
| top-level sessions 数 / 週別 token 消費 / 週別 tokens per session / ask_user_question per session | `sessions` / `weekly_session_metrics` | **session 単位** | ❌ | **Tier 3** |
| peak / avg concurrent | `session_concurrency_daily` | 区間重なり | ❌ | Tier 4 → [0054] で abandon |

## やること

### Tier 2 — 既存 gauge の PromQL 集約で近似（新規 export 不要）

> **進捗（実装済み）**: OSS dashboard（`deploy/oss-observability/grafana/dashboards/agent-telemetry-oss.json`）に「状態評価」row を追加し、total tokens / merged PRs / PR per 1M tokens の stat ＋ 週別 merged PR 数 trend を `last_over_time` 集約で復元。semantic drift を各パネル description に明記。実 Mimir に対し 4 式の PromQL を検証済み。Tier 3（session-grain export）は本 PR 対象外で issue は open のまま。

`agent_pr_*` gauge を `last_over_time(...[$__range])` で集約してヘッドライン stat / trend を出す。**ただし `pr_metrics` は `is_merged = 1` 限定**なので、「全 session の総量」ではなく「merged-PR に寄与した分」になる意味のズレを description に明示する（非 PR・未マージ・放棄 session を取りこぼす）。

- total tokens stat: `sum(max by (pr_url, coding_agent, user_id) (last_over_time(agent_pr_total_tokens[$__range])))`
- merged PRs stat: `count(group by (pr_url) (last_over_time(agent_pr_total_tokens[$__range])))`（merged 限定なので近似ではなく一致）
- PR / 1M tokens: `count(group by (pr_url)(...)) * 1e6 / sum(max by (pr_url, coding_agent, user_id)(...))`
- 週別 merged PR 数: gauge の `count` 時系列（weekday 起点バケットは要工夫）

**sum 系は安定キーで dedup する**（実装時に確定）: gauge 系列 identity は `(pr_url, coding_agent, user_id, task_type, model)` だが集約 grain は `(pr_url, coding_agent, user_id)` で `task_type`/`model` は代表値ラベル。range 内で代表 `model`/`task_type` が変わると同一 PR が複数系列で残り、素の `sum` が累積 `total_tokens` を二重計上する。total tokens / PR per 1M の分母は `max by (pr_url, coding_agent, user_id)`（累積値の最新＝max）で畳んでから合算する。`count`/`group by` 系（merged PRs・週別 trend）は元々畳むため影響なし。

合否: 上記 stat/trend が出せ、semantic drift が description に明記されている。

### Tier 3 — session-grain export を新設（client/spec 拡張が必要）

> **進捗（実装済み）**: `weekly_session_metrics` VIEW を client 側で評価し `agent_weekly_session_*` gauge として OTLP Metrics で送る representation を新設（`metrics` signal・`metrics_cursors` を `pr_metrics` gauge と共有、同一 `/v1/metrics` flush）。`week_start`（JST 月曜起点）を label に載せて weekday-0 バケット問題を解決。OSS dashboard に Tier 3 row（top-level sessions / 週別 token 消費 / 週別 tokens per session / 週別 ask_user_question per session）を追加。詳細は本 issue 末尾「解決方法」。

`pr_metrics` の session_count は「PR 寄与分」のみで top-level session 数とは別物。`weekly_session_metrics` も session-grain。これらは新しい export がないと出せない。

- `agent_session_*`（または session-grain の集約 gauge）を client から OTLP Metrics で export する representation を設計（[0043] の `pr_metrics` gauge representation に倣う）
  - 候補: top-level session 数、session-grain total_tokens、tokens_per_session、ask_user_question
- **週次 weekday-0 バケット**（`date(x,'weekday 0','-6 days')`、Asia/Tokyo）は PromQL の `[1w]` 窓と境界がずれる。recording rule か client 側で週次集約してから送るか、いずれかの方針を決める
- `docs/spec.md` / `docs/metrics.md` に新 metric を追記

合否: top-level sessions 数・週別 token 消費・週別 tokens/session が otel+grafana で復元でき、SQLite dashboard と意味が一致する。

## 段階方針

Tier 2 は既存 gauge だけで出せるので軽い（先行可）。Tier 3 は client/spec 拡張を伴うので別 PR に分けてよい。fidelity parity を急がない場合は Tier 2 のみ採用し、Tier 3 は需要が出たら着手でも可（[0054] の段階的縮小方針に沿う）。

## 採用しなかった代替

- **Tier 2/3 を即メイン dashboard に載せる**: semantic drift（merged 限定）や未 export を曖昧にしたまま出すと「全活動の総量」と誤読される。Tier 1 のみに絞り、本 issue で明示的に扱う
- **session-grain を raw logs(Loki) の LogQL 集約で代用**: LogQL の集約は SQL より貧弱で、cross-event join（[0043] §458 のロックイン）を backend で再現できないため不可

Completed: 2026-05-31

## 解決方法

Tier 2 は先行 PR で実装済み。本 close は **Tier 3（session-grain export）** の完了をもって行う。

### session-grain 週次 gauge representation（Tier 3）

`weekly_session_metrics` VIEW（top-level session を JST 月曜起点週で集約）を client 側で評価し、`agent_weekly_session_*` gauge として OTLP Metrics で送る。`pr_metrics` gauge（[0043]）に倣い、同じ `metrics` signal・同じ `metrics_cursors`・同じ `/v1/metrics` flush に相乗りする（2 gauge family を 1 flush で送信）。

- **実装**: `internal/serverclient/session_metrics.go`（VIEW 評価 + gauge payload 構築）、`flush.go`（`flushMetricsToTarget` が pr_metrics に続けて weekly session を送信、`MetricsSessionSeries` を結果に追加）。byte 分割は `splitGaugeBatches`（row 型 generic）に共通化。
- **送る metric**: base measure（`agent_weekly_session_count` / `agent_weekly_session_total_tokens` / `agent_weekly_session_ask_user_question_total`）＋ 派生 ratio（`agent_weekly_session_tokens_per_session` / `agent_weekly_session_ask_user_question_per_session`）。dimension は `week_start` / `coding_agent` のみ（`session_id` は出さず cardinality を週×agent で有界化）。`weekly_session_metrics` に `ask_user_question` 生サム列を追加（ratio を base から集約安全に再計算するための分子。schema_hash 再生成）。

### 週次 weekday-0 バケット問題の解決方針

**client 側（SQLite）で週次集約し、`week_start` を gauge label に載せる**方針を採用。dashboard は `week_start` label で group するだけで PromQL `[1w]` 窓を使わない。

- **却下: Mimir recording rule で backend 再集約** — `[1w]` は UTC 木曜起点で、SQLite の JST 月曜起点暦週と境界がずれる問題が再発する
- **却下: 点を `week_start` に back-date して時間軸で表現** — Mimir ingest の out-of-order 却下リスク（gotchas メモリ #5 と同種）。送信時刻スタンプ＋label 方式なら回避できる

### dashboard（OSS）

`deploy/oss-observability/grafana/dashboards/agent-telemetry-oss.json` に Tier 3 row を追加: top-level sessions（stat, `sum(last_over_time(agent_weekly_session_count[$__range]))`）/ 週別 token 消費・週別 tokens per session・週別 ask_user_question per session（barchart, xField=`week_start`）。ratio パネルは agent 跨ぎ集約で誤らないよう base measure から再計算（`sum by (week_start)(total) / sum by (week_start)(count)`）。

### 検証

`go test ./...` 全パス（weekly session gauge の tag/値・float ratio・非マージ top-level session の計上・week_start バケット・既存 metrics テストの 2-POST 対応）。`go vet` / `gofmt` clean。OSS dashboard JSON は決定的スクショ手段が無い（0055-⑤）ため、パネル追加と PromQL idiom（Tier 2 で実 Mimir 検証済みの `last_over_time` 集約）の踏襲に留め、README スクショ差替（0055-⑥）は本 PR では行わない。

`docs/spec.md`（「session-grain 週次 gauge representation」節）/ `docs/metrics.md`（`agent_weekly_session_*` カタログ）を同期更新。

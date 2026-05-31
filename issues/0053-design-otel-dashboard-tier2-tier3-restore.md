---
decision_type: design
tags: [otel, grafana, mimir, dashboard, metrics-export, tier2, tier3]
related: [0040, 0043, 0050, 0054]
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

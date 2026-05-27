---
decision_type: design
affected_paths:
  - docs/spec.md
  - docs/design.md
  - docs/metrics.md
  - deploy/otel-collector/
depends_on: [0038]
tags: [datadog, otel, export, observability-backend, semantic-conventions]
---

# agent-telemetry events を Datadog に export 可能にする（dashboard はユーザ構築前提）

Created: 2026-05-26

## 概要

[0038] で metrics 転送を append-only events + OTLP/HTTP に移行したあと、その events stream を **Datadog に届け、属性が tag / facet / measure として一級に使える状態にする** までを本 issue のスコープとする。

本 issue の deliverable は次の 2 つ:

- **A. 送信経路**: OTLP/HTTP で emit している events を Datadog に届ける経路（client 直送 / OTel Collector 経由 / やらない の比較と方針確定）
- **B. attribute mapping**: 独自 event の attribute を Datadog の tag / facet / measure にどうマッピングするかの仕様化

**dashboard / monitor は本 issue では提供も保守もしない。** ユーザが Datadog 上で、本 issue が担保する metrics（measure）とフィルタ次元（tag）をもとに**自由に構築する前提**とする。本 issue は「ユーザが dashboard / monitor を組める状態」を A + B で担保するところまでで閉じる。

> **単一 issue で扱う理由**: 「送信先をプラガブルにしたい（OTel のメリット最大化）」と「Datadog で運用に使いたい」は、手段（A の Collector ファンアウト）と目的（ユーザが Datadog で運用する）の関係にあり、対等な 2 目的ではない。さらに「ユーザが dashboard を組める」が A + B で担保できるため、Datadog 固有の dashboard を別 issue に切り出す必要がない。よって分割せず 1 本にまとめる。

## 根拠

[0038] で OTLP/HTTP に乗り換えると、wire format が OTel 標準になるため「送信先を差し替えるだけで別 backend に流せる」というのが理屈の上では成立する。ユーザの直近の関心も「OTel 対応によって送信先をプラガブルにし、Datadog に流せるか」にある。

ただし「流せる」と「ユーザが Datadog で意味のある dashboard を組める」の間には gap があり、次の 2 点を片付けないと、生 logs を grep できるだけで運用には使えない:

- **送信経路（A）**: events をどの OTLP signal で送るか（OTLP **Logs**＝[0038] の決定のまま Datadog 側で Logs to Metrics するか、OTLP **Metrics** に変換するか）と、どの経路で Datadog Intake に届けるか
- **attribute mapping（B）**: OTLP Logs として送る場合、attribute → Datadog tag / facet / measure の mapping を仕様化しないと、cardinality 爆発（`session_id` を tag にしてしまう等）や、数値属性が `@input_tokens` のような log attribute として埋もれて集計対象にならない事態を招く

「Datadog で metrics を表示できる」と言うときのユーザの実用上の要求は通常「コーディングエージェント運用状況を Datadog の monitor / dashboard 経由で日常運用に組み込みたい」である。本 issue はその dashboard 自体を提供するのではなく、**ユーザが自前で組めるだけの素材（A の届け先 + B の mapping）を渡す**ことでこの要求に応える。

なお独自イベント名（`agent.session.started` 等）が Datadog の OOTB facet に乗らない点は、**Datadog 固有の障害ではなく、任意のアプリ独自テレメトリに共通の前提コスト** でしかない。OOTB integration は Postgres / nginx など既知ミドルウェア向けであり、独自イベントは命名が何であれ自前で facet / metric を定義する。命名を OTel 標準に寄せてもこのコストは消えない（→ `gen_ai.*` 寄せの検討は後述のとおり却下）。

また「Datadog を first-class でサポートしない（やらない）」も明確な選択肢として残す。Grafana + SQLite で完結する個人 / 小チーム運用が想定主用途であり、Datadog 連携は OSS 利用者の中の Datadog 利用者だけが必要とする dual-use 機能でしかない。判断は「対応方針」節で行う。

## 問題

[0038] が完了した時点で残る、Datadog 連携固有の障害を 2 つに整理する。

### A. 送信経路と OTLP signal

events をどの OTLP signal で、どの経路で Datadog に届けるか。

- [0038] は OTLP **Logs** で emit する決定。Logs のままなら Datadog 側で [Logs to Metrics](https://docs.datadoghq.com/logs/log_configuration/logs_to_metrics/) で metric 化する
- あるいは events を **OTLP Metrics に変換して送る**。Datadog は素直に metric として受けるが、OTLP Logs で emit する [0038] の決定との整合性をどう取るかが論点になる

送信経路（client 直送 / Collector ファンアウト / やらない）の比較と推奨は「対応方針 > 上位の選択」で行う。

> **集約（`pr_metrics` 相当）はユーザ側で組む。** `total_tokens` / `fresh_tokens` / `per_million_tokens` 等は現状 agent-telemetry-server の **SQLite VIEW** で集約している。Datadog 側では、ユーザが measure 上の Datadog formula（`total_tokens = input + output + cache_write + cache_read + reasoning`）と tag フィルタ（merged 限定 / subagent 除外 / ghost 除外 / ノイズ repo 除外）で**自前再現する**前提とする。本 issue は再現に必要な次元を B の mapping で漏れなく露出するところまでを担保し、集約定義そのものを Datadog 側に同梱・保守はしない。pre-aggregated metric を我々が別経路で送る案（旧 (b)）は、dashboard 非提供方針と矛盾する scope creep のため採らない。

> **却下: event 名を OTel `gen_ai.*` semantic conventions に寄せる案。** `gen_ai.*` は個々の LLM 呼び出しを記述する規約であり、agent-telemetry の中核概念（PR 単位のトークン効率・transcript scan の latest-wins snapshot）は構造的にマップできない。部分的に寄せても二重命名が増えるだけで OOTB 認識の利得が出ないため採らない。独自命名は維持し、facet / metric は自前定義する前提で進める。

### B. attribute → Datadog tag / facet / measure mapping の仕様化

OTLP Logs を Datadog に送ると、各 attribute は Datadog の log attribute（`@key`）にマップされる。これを **tag / facet / measure** に昇格しないと、検索・集計・monitor 対象として一級にならない。一方で `session_id` のような高 cardinality 属性を tag にすると Datadog の課金 / index 設計を破壊する。

ここを spec として固定しないと、Datadog 利用者ごとに mapping がブレて、組織を跨いだ dashboard 再利用ができない。**「ユーザが dashboard を組める」を実際に担保するのはこの mapping** であり、本 issue の core deliverable。

仕様化が必要な観点（対応表は「対応方針 > attribute mapping（B）」に置く）:

- どの属性を **tag** にするか（低 cardinality + フィルタ次元）
- どの属性を **facet only** に留めるか（中〜高 cardinality）
- 数値属性の **measure 化**
- `service` / `env` / `version` への Datadog 規約マッピング

## 対応方針

### 上位の選択（A）

Datadog 連携を **first-class** で扱うかどうかをまず決める。3 つの選択肢を比較:

| 選択肢 | 概要 | 利点 | 欠点 |
|---|---|---|---|
| (1) **client 直送** | client（hook / `agent-telemetry flush`）から OTLP/HTTP で Datadog Intake にも直接 emit。`[server]` セクションと並列に `[datadog]` セクションを追加 | server を経由しないので Datadog 利用者は自分の API key だけで完結。server を運用していない個人ユーザも Datadog を使える | API key を全 client に配布する必要がある（チームでは secret 管理が辛い）。事業者 API 呼び出しがクライアント数だけ増える |
| (2) **OTel Collector ファンアウト** | server の手前 / 中で OTel Collector を挟み、events を SQLite ingest と Datadog Intake に同時 export。client 側は OTLP/HTTP を Collector に向けるだけ | secret は Collector に閉じる。Collector の processor で attribute rename / cardinality 制限 / sampling を一元実装できる。Grafana 経路（SQLite）と Datadog 経路を並列に保てる | Collector の運用が増える（軽量とはいえプロセス 1 個追加）。client から見ると endpoint が server から Collector に変わるので spec を更新する必要がある |
| (3) **やらない** | OTLP/HTTP 標準に乗せておけば「自分で Collector を立てて Datadog に送る」ことは利用者側でいつでもできる、として公式 first-class サポートはしない | 実装ゼロ。docs の Datadog 言及は「OTLP の任意 export 先として動作する見込み」程度で済む | Datadog 利用者は自前で全部設計（attribute mapping / dashboard / monitor）する必要がある。組織横断の活用は事実上不可能 |

**推奨は (2) OTel Collector ファンアウト**。理由:

- secret 管理が中央集約できる
- attribute rename / measure 昇格 / cardinality 制限を Collector の processor で書けるので、agent-telemetry 本体には Datadog 固有のコードを入れずに済む（Datadog 以外の backend に増やすのも同じ仕組みでカバーできる）
- Grafana 経路を切らずに並列稼働できる（client 直送だと SQLite ingest との二重送信が必要になる）
- OSS 個人ユーザは Collector を立てない選択肢（=実質 (3) と等価）も維持できる

(1) は API key 配布の運用負荷で実質ペイしない。(3) は「公式に何を保証するか」がぼやけるので、少なくとも attribute mapping（B）は spec / docs に書き残しておきたい（その作業は本 issue で行う）。

### attribute mapping（B）

(2) を採用する前提で、attribute → tag / facet / measure の対応を Datadog `data/processors`（または Logs Pipeline の Remapper）／Collector processor で次のように定義する。これを `docs/spec.md` に仕様として固定する:

- **tag**（低 cardinality + フィルタ次元）: `service` / `env` / `version` / `coding_agent` / `model` / `agent_version` / `repo` / `task_type` / `end_reason` / `pr_state`
- **facet only**（中〜高 cardinality、tag に昇格しない）: `branch` / `pr_url` / `pr_title` / `user_id` / `session_id` / `parent_session_id`
- **measure**（数値）: `input_tokens` / `output_tokens` / `cache_write_tokens` / `cache_read_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `ask_user_question` / `review_comments` / `changes_requested`
- **Datadog 規約**: `service=agent-telemetry`, `env=<deploy environment>`, `version=<agent_version>`

`is_merged` / `is_subagent` / `is_ghost` は 0/1 の数値だが、ユーザが `pr_metrics` 相当を Datadog 上で再現する際の**フィルタ次元として強く使う**ため **tag 化**（`is_merged:true` 等）する。

> **担保のガードレール**: ユーザが SQLite VIEW の集約（`total_tokens = input + output + cache_* + reasoning`、merged 限定 / subagent・ghost 除外 / ノイズ repo 除外）を Datadog formula で自前再現できることが、本 issue の合否条件。そのためには **全フィルタ次元（`is_merged` / `is_subagent` / `is_ghost` / `repo`）が tag、全数値が measure** として漏れなく上記に含まれている必要がある。mapping を確定する際はこの完全性を必ず検証する。

### dashboard / monitor はユーザが構築する

dashboard / panel / monitor は本 issue では提供・保守しない。ユーザが B で露出した measure / tag をもとに Datadog 上で自由に構築する。参考として、想定される観察軸は `docs/metrics.md`（トークン効率 / 開発生産性 / 横断 A,B）と Grafana 版 dashboard を参照できる。

Grafana 版（`grafana/dashboards/agent-telemetry.json`、SQLite datasource 前提）は**そのまま残す**。ローカル / OSS 個人利用は引き続き SQLite で完結し、Datadog 経路はそれと並列に動く。Grafana JSON → Datadog dashboard JSON の自動変換は行わない（クエリ言語が違いすぎる）。

### 段階実装の見通し（child issue 分解候補）

本 issue 自体は spec/docs の更新と方針確定までを想定し、実装は次の child issue に分解する。順序は依存関係に従う:

1. **OTel Collector ファンアウト構成の確立（A）**: `deploy/otel-collector/`（or docs の how-to）に Datadog exporter + SQLite ingest（agent-telemetry-server）への dual export を書く。client 側 `[server] endpoint` 設定の意味を Collector 向けに整理する
2. **attribute → Datadog tag/facet/measure mapping を `docs/spec.md` に追記（B）**: 上記対応表を仕様として固定。Collector processor のサンプル設定も spec に同梱する
3. **`docs/metrics.md` にユーザ向け再構築ガイドを追記（任意）**: 各メトリクスについて「Datadog 上では Logs to Metrics で生成 / OTLP measure をそのまま使う / 集約は Datadog formula で再定義」のどれでユーザが組めるかを参考として明示
4. **`docs/design.md` に server / Collector / Datadog の責務分担を追記**: 「server は OTLP receiver + SQLite ingest だけ。Datadog 側集約はユーザの Datadog backend が担う。Collector は fanout と attribute mapping を担う」という分担を明文化

実装の前段で確認したい spike:

- Datadog OTLP Logs Intake が attribute の cardinality / 数値型をどこまで素直に受けるか（実機検証）
- `service.name` / `service.version` の semantic conventions を agent-telemetry の `coding_agent` / `agent_version` にマップするときに、`service.name=agent-telemetry-claude` のように agent 別 service にするか、単一 service + tag 分離にするか

### 触らない・後続 PR に回すもの

- `docs/spec.md` / `docs/design.md` の本文は本 issue 単体では更新しない（本 issue を ack した後の child issue / 段階実装の中で更新する）
- **Datadog dashboard / monitor の提供・保守**（ユーザ構築前提。本 issue は A + B で構築可能性のみを担保する）
- 集約定義（merged 限定等）の Datadog 側への同梱（ユーザが formula で自前再現する）
- Datadog 以外の backend（New Relic / Honeycomb / Grafana Cloud）への展開（同じ Collector 構成で原理的には可能だが、本 issue では Datadog のみを対象に絞る。横展開が必要になったら別 issue を起こす）

## 前提

本 issue は [0038] の実装（OTLP/HTTP + events table + flush 経路の rename + migration）が一通り完了している前提で着手する。[0038] が pending / 実装中の間は本 issue も pending として扱ってよい。

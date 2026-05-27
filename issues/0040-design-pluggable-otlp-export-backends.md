---
decision_type: design
affected_paths:
  - docs/spec.md
  - docs/design.md
  - docs/metrics.md
depends_on: [0038]
tags: [otel, export, pluggable-backend, observability-backend, datadog, semantic-conventions]
---

# OTLP events のエクスポート先バックエンドをプラガブルにする

Created: 2026-05-26

## 概要

[0038] で metrics 転送を append-only events + OTLP/HTTP に移行し、wire format が OTel 標準になった。これを活かし、**ユーザがエクスポート先の observability backend をプラガブルに選択できる** ようにするための実装・設計方針を定める。これが本 issue の主目的であり、特定の backend を表示すること自体ではない。

本 issue の deliverable は **backend 非依存の 2 本**:

- **A. 送信経路（fanout）**: OTLP/HTTP で emit している events を、SQLite ingest（既存）と任意の外部 backend に同時に届ける経路を確立する
- **B. attribute の意味分類**: 独自 event の attribute を「低 cardinality の次元 / 高 cardinality の識別子 / 数値の measure」に分類し、各 backend のタグ・索引・メトリクスへ落とせる形に仕様化する

**Datadog はこの方針の最初のリファレンス実装** として 1 本通す。A + B が「ユーザが実 backend 上で自前の dashboard / monitor を組める状態」を担保できることを、Datadog という具体例で検証・例示する。Datadog 固有の用語（tag / facet / measure）は B の generic な意味分類の concretization として扱い、New Relic / Honeycomb / Grafana Cloud 等も同じ A + B の仕組みで後続 issue として足せる前提とする。

**dashboard / monitor は本 issue では提供も保守もしない。** どの backend を選んでも、ユーザが B で露出した measure / 次元タグをもとに自由に構築する前提とする。本 issue は「ユーザが dashboard / monitor を組める状態」を A + B で担保するところまでで閉じる。

> **位置づけ**: 「送信先をプラガブルにしたい（OTel のメリット最大化）」が目的、Datadog はそれを検証・例示する reference 実装。pluggability だけを抽象的に追うと検証対象がなく YAGNI になりやすいが、Datadog を最初の reference 実装として 1 本通すことでこれを回避する。よって本件は分割せず 1 issue にまとめる。

## 根拠

[0038] で OTLP/HTTP に乗り換えると、wire format が OTel 標準になるため「送信先を差し替えるだけで別 backend に流せる」というのが理屈の上では成立する。ユーザの関心も「OTel 対応によってエクスポート先をプラガブルにできるか」にある。

ただし「流せる」と「ユーザがその backend で意味のある dashboard を組める」の間には backend 非依存の gap が 2 つあり、これを埋めて初めてプラガブルが実用になる:

- **送信経路（A）**: events をどの OTLP signal で送るか（OTLP **Logs**＝[0038] の決定のまま backend 側で Logs to Metrics するか、OTLP **Metrics** に変換するか）と、どの経路で外部 backend に届けるか
- **attribute の意味分類（B）**: attribute をそのまま log attribute として流すと、cardinality 爆発（`session_id` を次元タグにしてしまう等）や、数値属性が集計対象にならず埋もれる事態を招く。どの属性が低 cardinality の次元で、どれが高 cardinality の識別子で、どれが数値 measure かを backend 非依存に分類しておく必要がある

「backend で metrics を表示できる」と言うときのユーザの実用上の要求は通常「コーディングエージェント運用状況を monitor / dashboard 経由で日常運用に組み込みたい」である。本 issue はその dashboard 自体を提供するのではなく、**ユーザが選んだ backend で自前で組めるだけの素材（A の届け先 + B の意味分類）を渡す**ことでこの要求に応える。

なお独自イベント名（`agent.session.started` 等）が backend の OOTB 連携（既知のタグ・facet）に乗らない点は、**特定 backend 固有の障害ではなく、任意のアプリ独自テレメトリに共通の前提コスト** でしかない。OOTB integration は Postgres / nginx など既知ミドルウェア向けであり、独自イベントは命名が何であれ自前で次元 / metric を定義する。命名を OTel 標準に寄せてもこのコストは消えない（→ `gen_ai.*` 寄せの検討は後述のとおり却下）。

また「外部 backend を first-class でサポートしない（OTLP 標準に乗せておくだけ）」も明確な選択肢として残す。Grafana + SQLite で完結する個人 / 小チーム運用が想定主用途であり、外部 backend 連携は OSS 利用者の中でそれを必要とする人だけの dual-use 機能でしかない。判断は「対応方針」節で行う。

## 問題

[0038] が完了した時点で残る、外部 backend への export 固有の障害を 2 つに整理する。いずれも backend 非依存で、Datadog はその具体的な検証対象。

### A. 送信経路と OTLP signal

events をどの OTLP signal で、どの経路で外部 backend に届けるか。

- [0038] は OTLP **Logs** で emit する決定。Logs のままなら backend 側で Logs to Metrics 相当（Datadog なら [Logs to Metrics](https://docs.datadoghq.com/logs/log_configuration/logs_to_metrics/)）で metric 化する
- あるいは events を **OTLP Metrics に変換して送る**。多くの backend は素直に metric として受けるが、OTLP Logs で emit する [0038] の決定との整合性をどう取るかが論点になる

送信経路（client 直送 / Collector ファンアウト / やらない）の比較と推奨は「対応方針 > 上位の選択」で行う。

> **集約（`pr_metrics` 相当）はユーザ側で組む。** `total_tokens` / `fresh_tokens` / `per_million_tokens` 等は現状 agent-telemetry-server の **SQLite VIEW** で集約している。外部 backend 側では、ユーザが measure 上の式（Datadog なら formula、`total_tokens = input + output + cache_write + cache_read + reasoning`）と次元タグのフィルタ（merged 限定 / subagent 除外 / ghost 除外 / ノイズ repo 除外）で**自前再現する**前提とする。本 issue は再現に必要な次元を B の意味分類で漏れなく露出するところまでを担保し、集約定義そのものを backend 側に同梱・保守はしない。pre-aggregated metric を我々が別経路で送る案は、dashboard 非提供方針と矛盾する scope creep のため採らない。

> **却下: event 名を OTel `gen_ai.*` semantic conventions に寄せる案。** `gen_ai.*` は個々の LLM 呼び出しを記述する規約であり、agent-telemetry の中核概念（PR 単位のトークン効率・transcript scan の latest-wins snapshot）は構造的にマップできない。部分的に寄せても二重命名が増えるだけで OOTB 認識の利得が出ないため採らない。独自命名は維持し、次元 / metric は自前定義する前提で進める。

### B. attribute の意味分類の仕様化

OTLP Logs を外部 backend に送ると、各 attribute は backend の log attribute にマップされる。これを **次元タグ / 索引（facet）/ measure** に昇格しないと、検索・集計・monitor 対象として一級にならない。一方で `session_id` のような高 cardinality 属性を次元タグにすると、backend の課金 / index 設計を破壊する。

ここを backend 非依存の意味分類として spec で固定しないと、利用者ごと・backend ごとに mapping がブレて、組織や backend を跨いだ dashboard 再利用ができない。**「ユーザが dashboard を組める」を実際に担保するのはこの意味分類** であり、本 issue の core deliverable。

仕様化が必要な観点（対応表と Datadog へのリファレンスマッピングは「対応方針 > attribute の意味分類（B）」に置く）:

- どの属性を **低 cardinality の次元タグ** にするか（フィルタ・group-by 用）
- どの属性を **高 cardinality の識別子（索引のみ、次元タグに昇格しない）** に留めるか
- 数値属性の **measure 化**
- `service` / `env` / `version` への OTel resource 規約マッピング

## 対応方針

### 上位の選択（A）

外部 backend 連携を **first-class** で扱うかどうかをまず決める。3 つの選択肢を比較:

| 選択肢 | 概要 | 利点 | 欠点 |
|---|---|---|---|
| (1) **client 直送** | client（hook / `agent-telemetry flush`）から OTLP/HTTP で外部 backend Intake にも直接 emit。`[server]` セクションと並列に backend 設定セクションを追加 | server を経由しないので利用者は自分の API key だけで完結。server を運用していない個人ユーザも使える | API key を全 client に配布する必要がある（チームでは secret 管理が辛い）。backend ごとに client 側設定が増え、プラガブルにするほど client が複雑化 |
| (2) **OTel Collector ファンアウト** | server の手前 / 中で OTel Collector を挟み、events を SQLite ingest と外部 backend Intake に同時 export。client 側は OTLP/HTTP を Collector に向けるだけ。**backend 追加は Collector の exporter を足すだけ** | secret は Collector に閉じる。Collector の processor で attribute rename / cardinality 制限 / sampling を一元実装でき、**プラガブルの基盤がここに集約する**。Grafana 経路（SQLite）と外部 backend 経路を並列に保てる | Collector の運用が増える（軽量とはいえプロセス 1 個追加）。client から見ると endpoint が server から Collector に変わるので spec を更新する必要がある |
| (3) **やらない（OTLP 標準に乗せておくだけ）** | OTLP/HTTP 標準に乗せておけば「自分で Collector を立てて任意 backend に送る」ことは利用者側でいつでもできる、として公式 first-class サポートはしない | 実装ゼロ。docs の言及は「OTLP の任意 export 先として動作する見込み」程度で済む | 利用者は自前で全部設計（意味分類 / dashboard / monitor）する必要がある。組織横断の活用は事実上不可能 |

**推奨は (2) OTel Collector ファンアウト**。理由:

- **プラガブルの基盤が Collector に集約する**: backend 追加は exporter を足すだけで、agent-telemetry 本体には backend 固有のコードを入れずに済む
- attribute rename / measure 昇格 / cardinality 制限（B の意味分類）を Collector の processor で backend 非依存に書ける
- secret 管理が中央集約できる
- Grafana 経路を切らずに並列稼働できる（client 直送だと SQLite ingest との二重送信が必要になる）
- OSS 個人ユーザは Collector を立てない選択肢（=実質 (3) と等価）も維持できる

(1) は API key 配布の運用負荷に加え、backend ごとに client 設定が増えてプラガブル化に逆行するため実質ペイしない。(3) は「公式に何を保証するか」がぼやけるので、少なくとも意味分類（B）は spec / docs に書き残しておきたい（その作業は本 issue で行う）。

### attribute の意味分類（B）

(2) を採用する前提で、attribute → 意味分類の対応を Collector processor（backend 非依存）で定義し、`docs/spec.md` に仕様として固定する。各分類は backend のタグ・索引・メトリクスへ落ちる。**Datadog をリファレンス実装としたときの concrete マッピング**を併記する:

- **低 cardinality の次元タグ**（フィルタ・group-by 用、Datadog では tag）: `service` / `env` / `version` / `coding_agent` / `model` / `agent_version` / `repo` / `task_type` / `end_reason` / `pr_state`
- **高 cardinality の識別子**（索引のみ、次元タグに昇格しない、Datadog では facet only）: `branch` / `pr_url` / `pr_title` / `user_id` / `session_id` / `parent_session_id`
- **数値 measure**（Datadog では measure）: `input_tokens` / `output_tokens` / `cache_write_tokens` / `cache_read_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `ask_user_question` / `review_comments` / `changes_requested`
- **OTel resource 規約**: `service.name=agent-telemetry`, `env=<deploy environment>`, `version=<agent_version>`（Datadog の `service` / `env` / `version` に対応）

`is_merged` / `is_subagent` / `is_ghost` は 0/1 の数値だが、ユーザが `pr_metrics` 相当を再現する際の**フィルタ次元として強く使う**ため **次元タグ扱い**（Datadog なら `is_merged:true` 等）にする。

> **担保のガードレール**: どの backend を選んでも、ユーザが SQLite VIEW の集約（`total_tokens = input + output + cache_* + reasoning`、merged 限定 / subagent・ghost 除外 / ノイズ repo 除外）を再現できることが、本 issue の合否条件。そのためには **全フィルタ次元（`is_merged` / `is_subagent` / `is_ghost` / `repo`）が次元タグ、全数値が measure** として漏れなく上記に含まれている必要がある。意味分類を確定する際はこの完全性を必ず検証する。

### dashboard / monitor はユーザが構築する

dashboard / panel / monitor は本 issue では提供・保守しない。どの backend でも、ユーザが B で露出した measure / 次元タグをもとに自由に構築する。参考として、想定される観察軸は `docs/metrics.md`（トークン効率 / 開発生産性 / 横断 A,B）と Grafana 版 dashboard を参照できる。

Grafana 版（`grafana/dashboards/agent-telemetry.json`、SQLite datasource 前提）は**そのまま残す**。ローカル / OSS 個人利用は引き続き SQLite で完結し、外部 backend 経路はそれと並列に動く。Grafana JSON → 各 backend dashboard JSON の自動変換は行わない（クエリ言語が違いすぎる）。

### 段階実装の見通し（child issue 分解候補）

本 issue 自体は spec/docs の更新と方針確定までを想定し、実装は次の child issue に分解する。順序は依存関係に従う:

1. **OTel Collector ファンアウト構成の確立（A、backend 非依存）**: `deploy/otel-collector/`（or docs の how-to）に、SQLite ingest（agent-telemetry-server）+ 外部 backend exporter への dual export を書く。client 側 `[server] endpoint` 設定の意味を Collector 向けに整理する。**最初の外部 exporter として Datadog exporter を例示**する
2. **attribute の意味分類を `docs/spec.md` に追記（B、backend 非依存 + Datadog リファレンス）**: 上記対応表を仕様として固定。Collector processor のサンプル設定も spec に同梱する
3. **`docs/metrics.md` にユーザ向け再構築ガイドを追記（任意）**: 各メトリクスについて「Logs to Metrics 相当で生成 / OTLP measure をそのまま使う / backend 側の式で再定義」のどれでユーザが組めるかを参考として明示
4. **`docs/design.md` に server / Collector / 外部 backend の責務分担を追記**: 「server は OTLP receiver + SQLite ingest だけ。外部 backend 側集約はユーザが担う。Collector は fanout と意味分類を担う」という分担を明文化

実装の前段で確認したい spike（Datadog をリファレンスとして実機検証する）:

- Datadog OTLP Logs Intake が attribute の cardinality / 数値型をどこまで素直に受けるか（実機検証。他 backend の傾向を測る最初のサンプル）
- `service.name` / `service.version` の semantic conventions を agent-telemetry の `coding_agent` / `agent_version` にマップするときに、`service.name=agent-telemetry-claude` のように agent 別 service にするか、単一 service + 次元タグ分離にするか

### 触らない・後続 PR に回すもの

- `docs/spec.md` / `docs/design.md` の本文は本 issue 単体では更新しない（本 issue を ack した後の child issue / 段階実装の中で更新する）
- **dashboard / monitor の提供・保守**（backend を問わずユーザ構築前提。本 issue は A + B で構築可能性のみを担保する）
- 集約定義（merged 限定等）の backend 側への同梱（ユーザが backend の式で自前再現する）
- **Datadog 以外の backend（New Relic / Honeycomb / Grafana Cloud）のリファレンス実装**: A の Collector fanout と B の意味分類は backend 非依存なので、同じ仕組みで exporter を足せば原理的に対応可能。ただし本 issue では **リファレンス実装を Datadog 1 本に絞る**（検証対象を 1 つに固定するため）。横展開は同じ A + B 上で後続 issue として起こす

## 前提

本 issue は [0038] の実装（OTLP/HTTP + events table + flush 経路の rename + migration）が一通り完了している前提で着手する。[0038] が pending / 実装中の間は本 issue も pending として扱ってよい。

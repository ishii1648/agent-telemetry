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

- **A. 設定可能な OTLP export**: client が OTLP/HTTP を **設定可能な宛先（複数 endpoint + 任意の auth header）** に投げられる 1 能力を確立する。これ自体が「OTLP 標準に乗っているので任意 backend に向けられる」を内包し、その上で **direct（client → backend Intake 直送）** と **collector（client → OTel Collector → fanout）** の 2 つを **デプロイレシピ**として docs に同梱する。direct/collector は実装分岐ではなく宛先設定の違い
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

なお Grafana + SQLite で完結する個人 / 小チーム運用が想定主用途であり、外部 backend 連携は OSS 利用者の中でそれを必要とする人だけの dual-use 機能でしかない。だからといって「やらない」を独立の選択肢に立てる必要はなく、**「設定可能な OTLP export」という 1 能力を持てば、使わないユーザは宛先を向けなければ実質「やらない」と等価**になる（外部 backend を要する人だけが direct / collector レシピを使う）。具体の使い分けは「対応方針」節で行う。

## 問題

[0038] が完了した時点で残る、外部 backend への export 固有の障害を 2 つに整理する。いずれも backend 非依存で、Datadog はその具体的な検証対象。

### A. OTLP export 能力と signal / 宛先

events をどの OTLP signal で、どの宛先に届けるか。

- [0038] は OTLP **Logs** で emit する決定。Logs のままなら backend 側で Logs to Metrics 相当（Datadog なら [Logs to Metrics](https://docs.datadoghq.com/logs/log_configuration/logs_to_metrics/)）で metric 化する
- あるいは events を **OTLP Metrics に変換して送る**。多くの backend は素直に metric として受けるが、OTLP Logs で emit する [0038] の決定との整合性をどう取るかが論点になる

宛先は「設定可能な OTLP export」1 能力の上で **direct（backend Intake 直送）/ collector（Collector 経由 fanout）** をデプロイレシピとして両対応する。どちらを使うかの指針（個人 / team での使い分けと secret モデル）は「対応方針 > export 能力と 2 つのデプロイレシピ」で扱う。

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

### export 能力と 2 つのデプロイレシピ（A）

当初は「(1) client 直送 / (2) Collector / (3) やらない」の **択一**として整理していたが、実体は択一ではない。0038 後、client は既に **OTLP exporter → 設定可能な宛先（+ 任意の auth header）** を持つ。client から見ると direct も collector も「OTLP を URL に投げる」だけで差が無く、Collector を挟むかは **実装分岐ではなくデプロイ選択**。

したがってツールが提供するのは **「設定可能な OTLP export」という 1 能力**で、これが旧 3 択を畳む:

| 旧選択肢 | 統合後の位置づけ |
|---|---|
| (3) やらない | 設定可能な OTLP export を持てば、ユーザは**任意の宛先に自分で向けられる**。(3) は能力に**内包**され、独立した「やらない」選択肢ではなくなる |
| (1) client 直送 | その export を backend Intake に向ける **デプロイレシピ** |
| (2) Collector ファンアウト | その export を Collector に向け、Collector が SQLite ingest + backend に fanout する **デプロイレシピ** |

**両レシピをサポートする。** 使い分けの指針:

| 規模 | レシピ | 根拠 |
|---|---|---|
| 個人 / 小チーム（想定主用途） | **direct** | 追加プロセス無し。client の OTLP exporter を backend に向け、submit-only の credential を自分のマシンに置くだけ。Datadog RUM 同様、client-side telemetry は正当なパターン |
| team / 多 client | **collector** | Collector が credential を 1 箇所で保持し、**client は backend credential を一切持たない**。SQLite ingest と backend への fanout / buffer / retry を一貫処理 |

> **なぜ team では collector か（credential モデルの帰結）**: Datadog の ingestion API key (`DD-API-KEY`) は **org-wide でスコープ不可**（scopes は read/管理用の Application key にのみ適用、[API and Application Keys](https://docs.datadoghq.com/account_management/api-app-keys/)）。「ログ送信のみ」の key は作れない。submit 専用ではあるが、漏洩時は org 全体 rotate・濫用（コスト増幅）の blast-radius が残り、RUM client token のような app スコープ / 公開耐性 / 個別失効は無い。RUM が安全なのは token そのものではなく「**信頼できない多数クライアントは強い秘密を持たず、信頼できる intake だけが特権 submit する**」アーキテクチャ由来であり、それを OTLP で再現するのが collector レシピ（client は秘密なし＝RUM 等価）。個人は公開環境ではないので direct + submit-only key で十分。

これにより、agent-telemetry 本体には backend 固有のコードを入れず（宛先と auth は設定）、Grafana 経路（SQLite）も両レシピで並列に保てる。少なくとも意味分類（B）は spec / docs に書き残す（その作業は本 issue で行う）。

### attribute の意味分類（B）

attribute → 意味分類の対応（**spec 本体は 1 つ**）を `docs/spec.md` に backend 非依存で固定する。各分類は backend のタグ・索引・メトリクスへ落ちる。**Datadog をリファレンス実装としたときの concrete マッピング**を併記する:

- **低 cardinality の次元タグ**（フィルタ・group-by 用、Datadog では tag）: `service` / `env` / `version` / `coding_agent` / `model` / `agent_version` / `repo` / `task_type` / `end_reason` / `pr_state`
- **高 cardinality の識別子**（索引のみ、次元タグに昇格しない、Datadog では facet only）: `branch` / `pr_url` / `pr_title` / `user_id` / `session_id` / `parent_session_id`
- **数値 measure**（Datadog では measure）: `input_tokens` / `output_tokens` / `cache_write_tokens` / `cache_read_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `ask_user_question` / `review_comments` / `changes_requested`
- **OTel resource 規約**: `service.name=agent-telemetry`, `env=<deploy environment>`, `version=<agent_version>`（Datadog の `service` / `env` / `version` に対応）

`is_merged` / `is_subagent` / `is_ghost` は 0/1 の数値だが、ユーザが `pr_metrics` 相当を再現する際の**フィルタ次元として強く使う**ため **次元タグ扱い**（Datadog なら `is_merged:true` 等）にする。

> **配布形式は 2 つ（spec 本体は 1 つ）**: この意味分類が実際に昇格を起こす場所はレシピで変わる。**direct** では mapping は backend 側で行うため、Datadog をリファレンスとした **Logs Pipeline remapper の import 可能な設定**として配る。**collector** では **Collector processor のサンプル設定**として配る。「両対応」の追加コストはこの **2 形式へのレンダリング**だけで、どれが次元タグ / 識別子 / measure かという spec 本体は共通。

> **担保のガードレール**: どの backend を選んでも、ユーザが SQLite VIEW の集約（`total_tokens = input + output + cache_* + reasoning`、merged 限定 / subagent・ghost 除外 / ノイズ repo 除外）を再現できることが、本 issue の合否条件。そのためには **全フィルタ次元（`is_merged` / `is_subagent` / `is_ghost` / `repo`）が次元タグ、全数値が measure** として漏れなく上記に含まれている必要がある。意味分類を確定する際はこの完全性を必ず検証する。

### dashboard / monitor はユーザが構築する

dashboard / panel / monitor は本 issue では提供・保守しない。どの backend でも、ユーザが B で露出した measure / 次元タグをもとに自由に構築する。参考として、想定される観察軸は `docs/metrics.md`（トークン効率 / 開発生産性 / 横断 A,B）と Grafana 版 dashboard を参照できる。

Grafana 版（`grafana/dashboards/agent-telemetry.json`、SQLite datasource 前提）は**そのまま残す**。ローカル / OSS 個人利用は引き続き SQLite で完結し、外部 backend 経路はそれと並列に動く。Grafana JSON → 各 backend dashboard JSON の自動変換は行わない（クエリ言語が違いすぎる）。

### 段階実装の見通し（child issue 分解候補）

本 issue 自体は spec/docs の更新と方針確定までを想定し、実装は次の child issue に分解する。順序は依存関係に従う:

1. **設定可能な OTLP export 能力 + 2 デプロイレシピの確立（A、backend 非依存）**: client の OTLP exporter を **設定可能な宛先（複数 endpoint + auth header）** に向けられるようにする。docs に **direct レシピ**（client → backend Intake 直送、submit-only credential を client 設定に）と **collector レシピ**（`deploy/otel-collector/` or how-to で SQLite ingest + backend exporter への fanout、client は Collector に向けるだけ）を同梱。**最初の backend として Datadog を例示**する
2. **attribute の意味分類を `docs/spec.md` に追記（B、backend 非依存 + Datadog リファレンス）**: 対応表を仕様本体として固定し、**2 配布形式**（direct 用の Datadog Logs Pipeline 設定 / collector 用の Collector processor サンプル）を同梱する
3. **`docs/metrics.md` にユーザ向け再構築ガイドを追記（任意）**: 各メトリクスについて「Logs to Metrics 相当で生成 / OTLP measure をそのまま使う / backend 側の式で再定義」のどれでユーザが組めるかを参考として明示
4. **`docs/design.md` に client / server / (Collector) / 外部 backend の責務分担を追記**: 「client は設定可能な OTLP export を担う。server は OTLP receiver + SQLite ingest だけ。外部 backend 側集約はユーザが担う。collector レシピを採る場合のみ Collector が fanout と意味分類の昇格を担う（direct レシピでは backend 側 Pipeline が担う）」という分担を明文化

実装の前段で確認したい spike（Datadog をリファレンスとして実機検証する）:

- Datadog OTLP Logs Intake が attribute の cardinality / 数値型をどこまで素直に受けるか（実機検証。他 backend の傾向を測る最初のサンプル）
- `service.name` / `service.version` の semantic conventions を agent-telemetry の `coding_agent` / `agent_version` にマップするときに、`service.name=agent-telemetry-claude` のように agent 別 service にするか、単一 service + 次元タグ分離にするか

### 触らない・後続 PR に回すもの

- `docs/spec.md` / `docs/design.md` の本文は本 issue 単体では更新しない（本 issue を ack した後の child issue / 段階実装の中で更新する）
- **dashboard / monitor の提供・保守**（backend を問わずユーザ構築前提。本 issue は A + B で構築可能性のみを担保する）
- 集約定義（merged 限定等）の backend 側への同梱（ユーザが backend の式で自前再現する）
- **Datadog 以外の backend（New Relic / Honeycomb / Grafana Cloud）のリファレンス実装**: A の設定可能な OTLP export（direct / collector）と B の意味分類は backend 非依存なので、宛先と意味分類のレンダリング先を変えれば原理的に対応可能。ただし本 issue では **リファレンス実装を Datadog 1 本に絞る**（検証対象を 1 つに固定するため）。横展開は同じ A + B 上で後続 issue として起こす

## 前提

本 issue は [0038] の実装（OTLP/HTTP + events table + flush 経路の rename + migration）が一通り完了している前提で着手する。[0038] が pending / 実装中の間は本 issue も pending として扱ってよい。

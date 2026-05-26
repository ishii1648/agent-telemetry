---
decision_type: design
affected_paths:
  - docs/spec.md
  - docs/design.md
  - docs/metrics.md
  - cmd/agent-telemetry/
  - cmd/agent-telemetry-server/
  - internal/syncdb/
depends_on: [0038]
tags: [datadog, otel, export, observability-backend, semantic-conventions]
---

# Datadog で agent-telemetry の metrics を表示できるようにする

Created: 2026-05-26

## 概要

[0038] で metrics 転送を append-only events + OTLP/HTTP に移行したあと、その events stream を **Datadog 側でも first-class に可視化できる** ようにするための実装・設計方針を整理する。

具体的な scope:

- OTLP/HTTP で emit している events を Datadog に届ける経路（client 直送 / OTel Collector 経由 / やらない の比較）
- Datadog 側で意味のある dashboard / monitor を作るために、現在の独自 event 名・attribute を semantic conventions / Datadog facet にどうマッピングするか
- 「集約は server 側 SQLite VIEW」モデルから「集約は Datadog backend」に移すために必要な再構築のリスト（pr_metrics 相当を Datadog 側で組み立てる）
- 既存の Grafana dashboard JSON との関係（並存させるか、Datadog 移行に伴って役割を分けるか）

Datadog の dashboard / monitor の **具体的な JSON 生成 / Terraform 化は本 issue の scope 外**。本 issue は「何を出すべきか」「どの軸で出すべきか」のリストアップまでで、実 backend へのプロビジョニングは child issue / 後続 PR で扱う。

## 根拠

[0038] で OTLP/HTTP に乗り換えると、wire format が OTel 標準になるため「送信先を差し替えるだけで別 backend に流せる」というのが理屈の上では成立する。ユーザの直近の関心も「OTel 対応によって送信先をプラガブルにし、Datadog に流せるか」にある。

ただし「流せる」と「Datadog で意味のある dashboard が組める」の間には次の gap があり、実際に Datadog で運用するなら設計判断が必要:

- events をどの OTLP signal で送るか（OTLP **Logs**＝[0038] の決定のまま Datadog 側で Logs to Metrics するか、OTLP **Metrics** に変換して送るか）で metric 化経路が変わる。さらに効率指標（`pr_metrics` の `total_tokens` / `fresh_tokens` / `per_million_tokens`）は agent-telemetry-server の **SQLite VIEW で集約** しており、events だけ流しても集約は backend 側で再現する必要がある。なお独自イベント名が Datadog の OOTB facet に乗らないこと自体は、任意のアプリ独自テレメトリに共通の前提コストであって Datadog 固有の障害ではない
- Grafana dashboard JSON は SQLite datasource 前提。Datadog で同じ可視化を出すなら別途 dashboard を作る必要がある（自動変換は現実的でない）
- OTLP Logs として送る場合、attribute → Datadog tag / facet / measure の mapping を仕様化しないと、cardinality 爆発（`session_id` を tag にしてしまう等）や、せっかくの数値属性が `@input_tokens` のような log attribute として埋もれる事態を招く

「Datadog で metrics を表示できる」と言うとき、ユーザの実用上の要求は通常「コーディングエージェント運用状況を Datadog の monitor / dashboard 経由で日常運用に組み込みたい」であって、「OTLP の生 logs を Datadog で grep できる」ではない。後者だけなら本 issue は不要。前者を狙うなら 4 点の障害をどう片付けるか合意しておく必要がある。

なお「Datadog を first-class でサポートしない（やらない）」も明確な選択肢として残す。Grafana + SQLite で完結する個人 / 小チーム運用が想定主用途であり、Datadog 連携は OSS 利用者の中の Datadog 利用者だけが必要とする dual-use 機能でしかない。判断は「対応方針」節で行う。

## 問題

[0038] が完了した時点で残る、Datadog 連携固有の障害を 3 つに整理する。

### 1. 送信形式（Logs か Metrics か）と集約の置き場所

当初は「event 名が独自 semantic convention」と「集約が SQLite VIEW に閉じている」を別問題に分けていたが、実体は **「events をどの OTLP signal で送り、`pr_metrics` 相当の集約をどこで作るか」という一本の設計判断** に畳める。

まず event 名（`agent.session.started` / `agent.transcript.scanned` / `agent.pr.observed` 等）が独自命名で Datadog の OOTB facet に乗らない点は、**Datadog 固有の障害ではなく、任意のアプリ独自テレメトリに共通の前提コスト** でしかない。OOTB integration は Postgres / nginx など既知ミドルウェア向けであり、独自イベントは命名が何であれ自前で facet / metric / dashboard を定義する。命名を OTel 標準に寄せてもこのコストは消えない（→ `gen_ai.*` 寄せの検討は後述のとおり却下）。

実際に決めるべきは送信形式と集約の置き場所:

- [0038] は OTLP **Logs** で emit する決定。Logs のままなら Datadog 側で [Logs to Metrics](https://docs.datadoghq.com/logs/log_configuration/logs_to_metrics/) で metric 化する
- あるいは `pr_metrics` 相当を **OTLP Metrics に変換して送る**。Datadog は素直に metric として受けるが、OTLP Logs で emit する [0038] の決定との整合性をどう取るかが論点になる

そのうえで `pr_metrics` / `session_concurrency_*` の集約は現状 **agent-telemetry-server の SQLite VIEW** に閉じており、Datadog に events を流すだけでは Datadog 上に存在しない。集約の置き場所として次の選択肢がある:

- **(a) backend 側で再構築**: Datadog の formula / Logs to Metrics で `total_tokens = input + output + cache_write + cache_read + reasoning` のような集約を再定義する。merged 限定・subagent 除外・ghost 除外・運用ノイズリポジトリ除外のフィルタも Datadog 側で書き直す
- **(b) pre-aggregated metric を送る**: client / server で SQLite VIEW を評価した結果を OTLP **Metrics** として別経路で送る。Datadog は素直に metric として受ける
- **(c) Datadog では生 events のみ。集約は Grafana 側を引き続き使い、Datadog 側は粗い health check（流量 / エラー率）に留める**

(a) は Datadog の式言語に集約定義が散らばるため、SQL VIEW との同期維持が運用負荷になる。(b) は pre-aggregated metric を 2 経路で持つ複雑性。(c) は最小限の投資で済むが、Datadog の monitoring 価値を取り切れない。

> **却下: event 名を OTel `gen_ai.*` semantic conventions に寄せる案。** `gen_ai.*` は個々の LLM 呼び出しを記述する規約であり、agent-telemetry の中核概念（PR 単位のトークン効率・transcript scan の latest-wins snapshot）は構造的にマップできない。部分的に寄せても二重命名が増えるだけで OOTB 認識の利得が出ないため採らない。独自命名は維持し、facet / metric は自前定義する前提で進める。

### 2. Grafana dashboard JSON が SQLite 前提

`grafana/dashboards/agent-telemetry.json` は datasource `uid: agent-telemetry`（SQLite）に対する SQL クエリで書かれている。Datadog に流すからといって Grafana 版が不要になるわけではない（ローカル単独利用は引き続き SQLite で完結する）が、Datadog 版を作るなら次の判断が要る:

- Grafana JSON から Datadog dashboard JSON への **自動生成は scope 外**（クエリ言語が違いすぎる、formula も別物）として、Datadog 版を **別途手作りする**
- Datadog 版で再現する pane を絞る（pr_metrics の主要 4 指標 + concurrency + agent 比較に留める等）
- Grafana 版を「ローカル / OSS 個人用」、Datadog 版を「チーム集約」と用途分離する

### 3. attribute → Datadog tag / facet mapping の仕様化が未整備

OTLP Logs を Datadog に送ると、各 attribute は Datadog の log attribute（`@key`）にマップされる。これを **tag / facet / measure** に昇格しないと、検索・集計・monitor 対象として一級にならない。一方で `session_id` のような高 cardinality 属性を tag にすると Datadog の課金 / index 設計を破壊する。

仕様化が必要な観点:

- どの属性を **tag** にするか（低 cardinality: `coding_agent` / `model` / `agent_version` / `repo` / `task_type` / `end_reason` / `pr_state` / `is_merged` / `is_subagent` / `is_ghost`）
- どの属性を **facet only / measure** に留めるか（中〜高 cardinality: `branch` / `pr_url` / `pr_title` / `user_id` / `session_id` / `parent_session_id`）
- 数値属性（`input_tokens` / `output_tokens` / `cache_*_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `review_comments` / `changes_requested`）の **measure 化**
- `service` / `env` / `version` への Datadog 規約マッピング（`service=agent-telemetry`, `env=<deploy environment>`, `version=<agent_version>`）

ここを spec として固定しないと、Datadog 利用者ごとに mapping がブレて、組織を跨いだ dashboard 再利用ができない。

## 対応方針

### 上位の選択

Datadog 連携を **first-class** で扱うかどうかをまず決める。3 つの選択肢を比較:

| 選択肢 | 概要 | 利点 | 欠点 |
|---|---|---|---|
| (1) **client 直送** | client（hook / `agent-telemetry flush`）から OTLP/HTTP で Datadog Intake にも直接 emit。`[server]` セクションと並列に `[datadog]` セクションを追加 | server を経由しないので Datadog 利用者は自分の API key だけで完結。server を運用していない個人ユーザも Datadog を使える | API key を全 client に配布する必要がある（チームでは secret 管理が辛い）。事業者 API 呼び出しがクライアント数だけ増える |
| (2) **OTel Collector ファンアウト** | server の手前 / 中で OTel Collector を挟み、events を SQLite ingest と Datadog Intake に同時 export。client 側は OTLP/HTTP を Collector に向けるだけ | secret は Collector に閉じる。Collector の processor で attribute rename / cardinality 制限 / sampling を一元実装できる。Grafana 経路（SQLite）と Datadog 経路を並列に保てる | Collector の運用が増える（軽量とはいえプロセス 1 個追加）。client から見ると endpoint が server から Collector に変わるので spec を更新する必要がある |
| (3) **やらない** | OTLP/HTTP 標準に乗せておけば「自分で Collector を立てて Datadog に送る」ことは利用者側でいつでもできる、として公式 first-class サポートはしない | 実装ゼロ。docs/spec.md / docs/design.md の Datadog 言及は「OTLP の任意 export 先として動作する見込み（公式 dashboard は同梱しない）」程度で済む | Datadog 利用者は自前で全部設計（attribute mapping / dashboard / monitor）する必要がある。組織横断の活用は事実上不可能 |

**推奨は (2) OTel Collector ファンアウト**。理由:

- secret 管理が中央集約できる
- attribute rename / measure 昇格 / cardinality 制限を Collector の processor で書けるので、agent-telemetry 本体には Datadog 固有のコードを入れずに済む（Datadog 以外の backend に増やすのも同じ仕組みでカバーできる）
- Grafana 経路を切らずに並列稼働できる（client 直送だと SQLite ingest との二重送信が必要になる）
- OSS 個人ユーザは Collector を立てない選択肢（=実質 (3) と等価）も維持できる

(1) は API key 配布の運用負荷で実質ペイしない。(3) は「公式に何を保証するか」がぼやけるので、少なくとも attribute mapping と「これを Datadog 側で再構築すべき」リストは spec / docs に書き残しておきたい（その作業は本 issue で行う）。

### Datadog 側で再構築が必要なもの

(2) を採用する前提で、Datadog 利用者が自分の organization 内で再構築する必要がある artefact をリストアップする。本 issue で **「何を出すべきか」だけ列挙し、実物の JSON / Terraform 生成は child issue で扱う**。

#### dashboard / panel（pr_metrics 相当）

`docs/metrics.md` の観察軸（トークン効率 / 開発生産性 / 横断 A,B）に揃える:

- 1 PR あたり総トークン分布（`total_tokens` / `fresh_tokens` の P50/P90、`coding_agent` 分割）
- 100 万トークンあたり PR 完了数（`per_million_tokens`、agent 比較）
- PR 内平均セッションサイズ（`tokens_per_session`）
- mid_session_msgs / changes_requested の推移
- 同時実行数（avg / peak、日次・週次）
- agent_version 別の token 効率比較（session 粒度）

#### monitor

- `agent.pr.observed` の `changes_requested >= N` でアラート（特定ユーザ・特定 repo に絞る用途）
- `agent.transcript.scanned` の `cache_write_tokens / cache_read_tokens` 比が異常な期間（プロンプト不安定の兆候）
- `agent.session.started` の流量低下（client 側送信パスが切れていないかの health check）

#### facet / tag / measure

「問題 3」で挙げた mapping を Datadog `data/processors`（または Logs Pipeline の Remapper）で次のように定義する想定:

- tag: `service` / `env` / `version` / `coding_agent` / `model` / `repo` / `task_type` / `end_reason` / `pr_state`
- facet（数値以外、tag に昇格しないもの）: `branch` / `pr_url` / `user_id` / `session_id` / `parent_session_id`
- measure（数値）: `input_tokens` / `output_tokens` / `cache_write_tokens` / `cache_read_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `ask_user_question` / `review_comments` / `changes_requested`

`is_merged` / `is_subagent` / `is_ghost` は 0/1 の数値だが、フィルタ用途に強く使うため **tag 化**（`is_merged:true` 等）する。

### 段階実装の見通し（child issue 分解候補）

本 issue 自体は spec/docs の更新と方針確定までを想定し、実装は次の child issue に分解する。順序は依存関係に従う:

1. **OTel Collector ファンアウト構成の確立**: `deploy/otel-collector/` 相当（or docs の how-to のみ）に Datadog exporter + SQLite ingest（agent-telemetry-server）への dual export を書く。client 側 `[server] endpoint` 設定の意味を Collector 向けに整理する
2. **attribute → Datadog tag/facet/measure mapping を `docs/spec.md` に追記**: 「問題 3」の対応表を仕様として固定。Collector processor のサンプル設定も spec に同梱する
3. **`docs/metrics.md` に Datadog 上の再構築指針を追記**: 各メトリクスについて「Datadog 上では Logs to Metrics で生成 / OTLP measure をそのまま使う / 集約は Datadog formula で再定義」のどれを取るかを明示
4. **Datadog dashboard サンプルの提供**: agent-telemetry org 用の dashboard JSON（Terraform 化は任意）を `examples/datadog/` に同梱する（本 issue の scope **外**、別 issue で扱う）
5. **`docs/design.md` に server / Collector / Datadog の責務分担を追記**: 「server は OTLP receiver + SQLite ingest だけ。Datadog 側集約は Collector + Datadog backend が担う」という分担を明文化

実装の前段で確認したい spike:

- Datadog OTLP Logs Intake が attribute の cardinality / 数値型をどこまで素直に受けるか（実機検証）
- `service.name` / `service.version` の semantic conventions を agent-telemetry の `coding_agent` / `agent_version` にマップするときに、`service.name=agent-telemetry-claude` のように agent 別 service にするか、単一 service + tag 分離にするか

### 触らない・後続 PR に回すもの

- `docs/spec.md` / `docs/design.md` の本文は本 issue 単体では更新しない（本 issue を ack した後の child issue / 段階実装の中で更新する）
- Datadog dashboard / monitor の具体的な JSON / Terraform 生成
- Datadog 以外の backend（New Relic / Honeycomb / Grafana Cloud）への展開（同じ Collector 構成で原理的には可能だが、本 issue では Datadog のみを対象に絞る。横展開が必要になったら別 issue を起こす）

## 前提

本 issue は [0038] の実装（OTLP/HTTP + events table + flush 経路の rename + migration）が一通り完了している前提で着手する。[0038] が pending / 実装中の間は本 issue も pending として扱ってよい。

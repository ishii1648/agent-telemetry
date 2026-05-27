---
decision_type: design
affected_paths:
  - docs/spec.md
  - docs/design.md
  - docs/metrics.md
  - internal/serverclient/
depends_on: [0038]
tags: [otel, export, pluggable-backend, observability-backend, datadog, semantic-conventions]
---

# OTLP events のエクスポート先バックエンドをプラガブルにする

Created: 2026-05-26

## 概要

[0038] で metrics 転送を append-only events + OTLP/HTTP に移行し、wire format が OTel 標準になった。これを活かし、**ユーザがエクスポート先の observability backend をプラガブルに選択できる** ようにするための実装・設計方針を定める。これが本 issue の主目的であり、特定の backend を表示すること自体ではない。

**本 issue（この PR）の完了条件は「下記 A/B の設計判断が issue 本文に記録され、[0038] と矛盾する `docs/design.md` の記述が同期されている」ことのみ。** export 能力・gauge 送信・`docs/spec.md`/`docs/metrics.md` 本文反映・docs の責務分担追記は **child issue で実装**する（「段階実装」参照）。以下の A/B は **本 issue で確定する設計判断**であって、本 PR で実装する成果物ではない:

- **A. 設定可能な OTLP export（2 representation）**: client が OTLP/HTTP を、**endpoint + auth header + encoding/protocol + signal/representation を持つ export target の配列**に投げられる能力を確立する。送る representation は 2 種:
  - **raw events**（OTLP **Logs**）: event-level 分析（token 推移・流量・カウント）と、任意で SQLite ingest（Grafana）向け
  - **pre-aggregated `pr_metrics`**（OTLP **Metrics** gauge, PR 単位）: 効率指標向け。**log-metric backend は record 間 join をしない**ため、cross-event join に依存する `pr_metrics` 相当は client のローカル VIEW で集約してから gauge で送る（理由は「問題」節）

  その上で **direct（client → backend Intake 直送）** と **collector（client → OTel Collector → fanout）** の 2 つを **デプロイレシピ**として docs に同梱する。**ただし「OTLP を URL に投げるだけ」ではない**: 現行 flush は OTLP Logs を **JSON encoding + Content-Type: application/json + 固定 Bearer**（`flush.go:392`）で手組みしているが、Datadog の OTLP intake は **protobuf + `DD-API-KEY` header** を前提とする。よって **Datadog direct は OTLP SDK / protobuf exporter への切替（実装追加）が必要**で、宛先設定だけでは動かない。一方 **collector レシピは client の既存 JSON exporter を変えずに済む**（Collector が protobuf + `DD-API-KEY` への変換を担う）— これは collector の隠れた利点。direct/collector の差は「実装分岐ではない」とは言い切れず、**direct-to-protobuf-backend のみ client の exporter 実装変更を伴う**点を明記する
- **B. attribute の意味分類**: 独自 event / gauge の attribute を「低 cardinality の次元タグ / 高 cardinality の識別子 / 数値の measure」に分類し、各 backend のタグ・索引・メトリクスへ落とせる形に仕様化する

**Datadog はこの方針の最初のリファレンス実装** として 1 本通す。A + B が「ユーザが実 backend 上で自前の dashboard / monitor を組める状態」を担保できることを、Datadog という具体例で検証・例示する。Datadog 固有の用語（tag / facet / measure）は B の generic な意味分類の concretization として扱い、New Relic / Honeycomb / Grafana Cloud 等も同じ A + B の仕組みで後続 issue として足せる前提とする。

**dashboard / monitor は本 issue では提供も保守もしない。** どの backend を選んでも、ユーザが B で露出した measure / 次元タグをもとに自由に構築する前提とする。本 issue は「ユーザが dashboard / monitor を組める状態」を A + B で担保するところまでで閉じる。

> **位置づけ**: 「送信先をプラガブルにしたい（OTel のメリット最大化）」が目的、Datadog はそれを検証・例示する reference 実装。pluggability だけを抽象的に追うと検証対象がなく YAGNI になりやすいが、Datadog を最初の reference 実装として 1 本通すことでこれを回避する。よって本件は分割せず 1 issue にまとめる。

## 根拠

[0038] で OTLP/HTTP に乗り換えると、wire format が OTel 標準になるため「送信先を差し替えるだけで別 backend に流せる」というのが理屈の上では成立する。ユーザの関心も「OTel 対応によってエクスポート先をプラガブルにできるか」にある。

ただし「流せる」と「ユーザがその backend で意味のある dashboard を組める」の間には backend 非依存の gap があり、これを埋めて初めてプラガブルが実用になる:

- **送信経路と representation（A）**: events をどの OTLP signal で送るか、どの経路で外部 backend に届けるか。さらに **log-metric backend は record 間 join をしない**ため、`pr_metrics` 相当（token は `transcript.scanned`、`pr_url`/`is_merged` は `pr.observed`、`user_id`/`repo` は `session.started` に分散し、SQLite VIEW が session_id で join + latest-wins + sum している）は **raw events を流すだけでは backend 側で組み立てられない**。効率指標は client 側で集約して送る必要がある
- **attribute の意味分類（B）**: attribute をそのまま log attribute として流すと、cardinality 爆発（`session_id` を次元タグにしてしまう等）や、数値属性が集計対象にならず埋もれる事態を招く。どの属性が低 cardinality の次元で、どれが高 cardinality の識別子で、どれが数値 measure かを backend 非依存に分類しておく必要がある

「backend で metrics を表示できる」と言うときのユーザの実用上の要求は通常「コーディングエージェント運用状況を monitor / dashboard 経由で日常運用に組み込みたい」である。本 issue はその dashboard 自体を提供するのではなく、**ユーザが選んだ backend で自前で組めるだけの素材（A の届け先 + B の意味分類）を渡す**ことでこの要求に応える。

なお独自イベント名（`agent.session.started` 等）が backend の OOTB 連携（既知のタグ・facet）に乗らない点は、**特定 backend 固有の障害ではなく、任意のアプリ独自テレメトリに共通の前提コスト** でしかない。OOTB integration は Postgres / nginx など既知ミドルウェア向けであり、独自イベントは命名が何であれ自前で次元 / metric を定義する。命名を OTel 標準に寄せてもこのコストは消えない（→ `gen_ai.*` 寄せの検討は後述のとおり却下）。

なお Grafana + SQLite で完結する個人 / 小チーム運用が想定主用途であり、外部 backend 連携は OSS 利用者の中でそれを必要とする人だけの dual-use 機能でしかない。だからといって「やらない」を独立の選択肢に立てる必要はなく、**「設定可能な OTLP export」という 1 能力を持てば、使わないユーザは宛先を向けなければ実質「やらない」と等価**になる（外部 backend を要する人だけが direct / collector レシピを使う）。具体の使い分けは「対応方針」節で行う。

## 問題

[0038] が完了した時点で残る、外部 backend への export 固有の障害を 2 つに整理する。いずれも backend 非依存で、Datadog はその具体的な検証対象。

### A. export の representation・signal・宛先

events をどの representation / signal で、どの宛先に届けるか。**representation は 2 種に分かれる**:

- **raw events（OTLP Logs）**: [0038] の決定どおり events をそのまま流す。event-level 分析（素の token 推移・流量・カウント）と、任意の SQLite ingest（Grafana）向け
- **pre-aggregated `pr_metrics`（OTLP Metrics gauge）**: 効率指標向け。後述の join 制約のため client 側で集約してから送る

宛先は「設定可能な OTLP export」能力の上で **direct（backend Intake 直送）/ collector（Collector 経由 fanout）** をデプロイレシピとして両対応する。どちらを使うかの指針（個人 / team での使い分けと secret モデル）は「対応方針 > export 能力と 2 つのデプロイレシピ」で扱う。

> **集約（`pr_metrics` 相当）は client 側で行い gauge として送る（join 不可ゆえ）。** `total_tokens` / `fresh_tokens` / `per_million_tokens` 等は現状 agent-telemetry-server の **SQLite VIEW** が cross-event join（`session_id`）+ latest-wins + sum で算出している。log-metric backend（Datadog Logs to Metrics 等）は **record 間 join をしない**ため、normalized な events（token は `transcript.scanned`、`pr_url`/`is_merged` は `pr.observed`、`user_id`/`repo` は `session.started` に分散）を流すだけでは backend 側で `pr_metrics` を組み立てられない。よって **client がローカル VIEW を評価し、PR 単位（`pr_url` / `coding_agent` / `user_id`）の pre-aggregated 値を OTLP Metrics gauge（last-value）として送る**。これは当初「scope creep」として却下した pre-aggregated 案の採用にあたるが、却下の前提（「集約はユーザ側で組める」）が join 不可で崩れたための翻意。`session_id` を tag に出さずに済むので cardinality は PR 数止まり。**ただし「gauge は冪等 upsert」は誤りなので注意**: Datadog は「**同一 timestamp かつ同一 dimensions の点のみ** last-write-wins」で、新 timestamp で再送すると **series 内の別の点**になる（DB row の upsert ではない）。さらに **Datadog OTLP metrics intake は sum/cumulative に delta temporality を要求**する（gauge は対象外だが [docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/) 参照）。よって再送・range 集計の正しさは「送信 timestamp の決め方」と「クエリ側で `last` の space-aggregation を取る前提」に依存し、**naive な SUM は二重に見える**。gauge / temporality / timestamp の扱いは実機 spike で固める（後述）。**denormalized な session-rollup を log で送り backend で集約する案（session-grain）は不採用**: latest-wins のため `session_id` を高 cardinality tag にせざるを得ず Datadog custom metric を膨張させ、log の sum が二重カウントしやすい。backend での自由 slice 柔軟性は Grafana/SQLite が既に担うため低価値。
>
> **これは [0038] の「OTLP Metrics signal 不採用・Logs 統一」決定（`docs/design.md` の「採用しなかった代替」）の改訂にあたる。** [0038] は「後で Metrics が必要になれば endpoint を追加する」と含みを残しており、本件はその発動。**server への内部転送は Logs（events）のまま**、**外部 backend 向けの `pr_metrics` gauge にのみ OTLP Metrics を併用**する、と範囲を限定する。この改訂は後続 child issue 送りにせず、**本 PR で `docs/design.md` を同期更新する**（下記「触らない」の例外）。

> **却下: event 名を OTel `gen_ai.*` semantic conventions に寄せる案。** `gen_ai.*` は個々の LLM 呼び出しを記述する規約であり、agent-telemetry の中核概念（PR 単位のトークン効率・transcript scan の latest-wins snapshot）は構造的にマップできない。部分的に寄せても二重命名が増えるだけで OOTB 認識の利得が出ないため採らない。独自命名は維持し、次元 / metric は自前定義する前提で進める。

### B. attribute の意味分類の仕様化

OTLP Logs を外部 backend に送ると、各 attribute は backend の log attribute にマップされる。これを **次元タグ / 索引（facet）/ measure** に昇格しないと、検索・集計・monitor 対象として一級にならない。一方で `session_id` のような高 cardinality 属性を次元タグにすると、backend の課金 / index 設計を破壊する。

ここを backend 非依存の意味分類として spec で固定しないと、利用者ごと・backend ごとに mapping がブレて、組織や backend を跨いだ dashboard 再利用ができない。意味分類（B）は **event-level 分析（raw events 側）を一級にする** 担保で、**効率指標（`pr_metrics`）は A の client 集約 gauge が担う** という二本立て（join 不可ゆえの役割分担）。両者が揃って初めて「ユーザがその backend で dashboard を組める」状態になる。

仕様化が必要な観点（対応表と Datadog へのリファレンスマッピングは「対応方針 > attribute の意味分類（B）」に置く）:

- どの属性を **低 cardinality の次元タグ** にするか（フィルタ・group-by 用）
- どの属性を **高 cardinality の識別子（索引のみ、次元タグに昇格しない）** に留めるか
- 数値属性の **measure 化**
- `service` / `env` / `version` への OTel resource 規約マッピング

## 対応方針

### export 能力と 2 つのデプロイレシピ（A）

当初は「(1) client 直送 / (2) Collector / (3) やらない」の **択一**として整理していたが、実体は択一ではない。ただし現状は **`[server].endpoint` + 固定 Bearer + JSON OTLP Logs**（`internal/serverclient/`）に限定されており、「設定可能な宛先 + 可変 auth + encoding/protocol + signal」を **client に持たせる拡張が必要**（「既にある」ではない。child issue の実装範囲を過小評価しないこと）。その能力を持たせれば、宛先を増やすのは設定で済み、Collector を挟むかは **デプロイ選択**になる（ただし「OTLP を URL に投げるだけ」で済むのは同 encoding/protocol の宛先に限る。Datadog direct は protobuf exporter 切替を伴う＝概要 A 参照）。

したがってツールが提供するのは **「設定可能な OTLP export」という 1 能力**で、これが旧 3 択を畳む:

| 旧選択肢 | 統合後の位置づけ |
|---|---|
| (3) やらない | 設定可能な OTLP export を持てば、ユーザは**任意の宛先に自分で向けられる**。(3) は能力に**内包**され、独立した「やらない」選択肢ではなくなる |
| (1) client 直送 | その export を backend Intake に向ける **デプロイレシピ** |
| (2) Collector ファンアウト | その export を Collector に向け、Collector が**選んだ宛先に fanout する デプロイレシピ**（backend のみ / backend + SQLite ingest / 複数 backend など任意。SQLite ingest は Grafana を併用する場合の任意の 1 宛先で必須ではない） |

**両レシピをサポートする。** 使い分けの指針:

| 規模 | レシピ | 根拠 |
|---|---|---|
| 個人 / 小チーム（想定主用途） | **direct** | 追加プロセス無し。client の OTLP exporter を backend に向け、submit-only の credential を自分のマシンに置くだけ。Datadog RUM 同様、client-side telemetry は正当なパターン |
| team / 多 client | **collector** | Collector が credential を 1 箇所で保持し、**client は backend credential を一切持たない**。選んだ宛先（backend、必要なら SQLite ingest も）への fanout / buffer / retry を一貫処理 |

> **なぜ team では collector か（credential モデルの帰結）**: Datadog の ingestion API key (`DD-API-KEY`) は **org-wide でスコープ不可**（scopes は read/管理用の Application key にのみ適用、[API and Application Keys](https://docs.datadoghq.com/account_management/api-app-keys/)）。「ログ送信のみ」の key は作れない。submit 専用ではあるが、漏洩時は org 全体 rotate・濫用（コスト増幅）の blast-radius が残り、RUM client token のような app スコープ / 公開耐性 / 個別失効は無い。RUM が安全なのは token そのものではなく「**信頼できない多数クライアントは強い秘密を持たず、信頼できる intake だけが特権 submit する**」アーキテクチャ由来であり、それを OTLP で再現するのが collector レシピ（client は秘密なし＝RUM 等価）。個人は公開環境ではないので direct + submit-only key で十分。

これにより backend 固有のもの（endpoint / auth header 名 / encoding/protocol / representation）は設定に追い出せ、Grafana 経路（SQLite）も両レシピで並列に保てる。意味分類（B）の**決定は本 issue 本文に記録**し、`docs/spec.md` への正式反映は child issue で行う（本 issue の deliverable 範囲は下記「段階実装」冒頭を参照）。

**送信経路の形（2 representation の帰結）**: 効率指標は client のローカル VIEW で集約するので、**集約は client 側に固定**される（collector は stateless で cross-event join できないため **router であって aggregator ではない**。server 側集約は SQLite ingest を必須化し Datadog-only 個人と矛盾するので採らない）。結果、同じローカル SoR から **raw events（Logs）と `pr_metrics` gauge（Metrics）の 2 projection** を作って別宛先に送る。現行 `[server].endpoint + Bearer 固定` を **export target 配列（endpoint + 可変 auth header + encoding/protocol + representation）** に拡張する。

**state / cursor の契約（child issue 1 の入力として本 issue で固定）**: 現行 state は `last_flushed_sequence` 1 本だが、target 配列化に伴い再送漏れ・重複を防ぐため次を決める:
- **per-target cursor**: state は `{target_id: last_flushed_sequence}` の map に拡張。`target_id` は **endpoint URL ではなく設定の安定 ID**（URL 変更で別 target 扱いにならないようにする）
- **独立前進**: 各 target の cursor は **その target への送信成功時のみ前進**。部分失敗（target A 成功 / B 失敗）で共有スキャンは進めず、B は次回再試行（A は二重送信しない）
- **新規 target の初期値**: 既定は cursor=0 から開始（gauge は現在値の再計算なので全 PR を埋め直すだけ、raw-logs を新 server に向ける場合は初期同期が重い旨を docs に明記。「now から」オプションは任意）
- **target rename / removal**: removal 時は state の該当 cursor を GC。rename は安定 ID を保てば cursor 継続
- **representation ごとに別 cursor**: 同一 target が raw events と gauge の両方を受ける場合も別カーソル（raw は append で重複防止に必須。gauge は再送が series の別点になる＝冪等な upsert ではない（前述）ので、timestamp の決め方とクエリ側 `last` 前提に依存）

### attribute の意味分類（B）

attribute → 意味分類の対応（**spec 本体は 1 つ**）を `docs/spec.md` に backend 非依存で固定する。各分類は backend のタグ・索引・メトリクスへ落ちる。**Datadog をリファレンス実装としたときの concrete マッピング**を併記する。**この分類は raw events（Logs）側の全 attribute を対象**とし、`pr_metrics` gauge が載せられる tag は別物（VIEW 出力に限られる。後述）:

- **低 cardinality の次元タグ**（有界・group-by 用、Datadog では tag）: `service` / `env` / `version` / `coding_agent` / `model` / `agent_version` / `repo` / `task_type` / `end_reason` / `pr_state` / **`user_id`**（`pr_metrics` の GROUP BY 軸（`pr_url`/`coding_agent`/`user_id`、`spec.md:270`）。cardinality はユーザ数で有界、group-by に必須なので次元タグ）
- **単調増加するが gauge の識別に必須な次元**（低 cardinality とは別枠）: **`pr_url`**。PR 単位 gauge の主キーで、低 cardinality タグ（`coding_agent` / `repo` 等の有界 group-by 軸）とは性質が違い、**PR が増えるほど timeseries が単調増加する**。`session_id`（無制限・再送のたび増殖）よりは穏当で、PR 単位メトリクスには不可欠なので tag として持つが、custom metric cardinality のコストドライバである点を運用上注意する（retention / rollup で古い PR を畳む等は backend 側の運用）
- **高 cardinality の識別子**（索引のみ、次元タグに昇格しない、Datadog では facet only）: `branch` / `pr_title` / `session_id` / `parent_session_id`
- **数値 measure**（Datadog では measure）: `input_tokens` / `output_tokens` / `cache_write_tokens` / `cache_read_tokens` / `reasoning_tokens` / `tool_use_total` / `mid_session_msgs` / `ask_user_question` / `review_comments` / `changes_requested`
- **OTel resource 規約**: `service.name=agent-telemetry`, `env=<deploy environment>`, `version=<agent_version>`（Datadog の `service` / `env` / `version` に対応）

`is_merged` / `is_subagent` / `is_ghost` は 0/1 の数値だが、event-level（raw events）分析でフィルタ次元として強く使うため、**raw events 側では次元タグ扱い**（Datadog なら `is_merged:true` 等）にする。

> **gauge が載せる tag は VIEW 出力に限られる（点: VIEW と同期必須）**: `pr_metrics` gauge の tag は現行 `internal/syncdb/schema/schema.sql` の VIEW projection（`GROUP BY pr_url, coding_agent, user_id` + `MAX(task_type)` / `MAX(model)`）に等しく、**`pr_url` / `coding_agent` / `user_id` / `task_type` / `model` のみ**。**`repo` / `pr_state` / `branch` は VIEW に出力されておらず、`is_merged` は WHERE で 1 固定（gauge では常に true ＝ tag にしても無意味）**。これらを gauge tag に載せたいなら **child issue 2 で VIEW の projection 拡張が必要**（上の raw-events 分類に列挙した tag をそのまま gauge に載せられる前提にしない）。

> **上記に現れない schema 項目の扱い（分類の網羅性）**:
> - **内部構造（export せず facet 化しない）**: `event_id` / `local_sequence` / `received_at`
> - **OTLP LogRecord の field（attribute ではない）**: `event_name`（`flush.go` で `LogRecord.EventName`）/ `occurred_at`（時刻）。event-level の「イベント種別ごとの count」は、backend が **LogRecord の event name を facet 化**して行う（attribute tag ではない点を spec に明記）
> - **低価値 attribute（既定では facet/tag 昇格しない）**: `cwd` / `transcript`（パス）/ `started_at` / `ended_at` / `pr_pinned`。必要になった時点で個別に昇格判断

> **配布形式（spec 本体は 1 つ、ただし「attribute 整形」と「facet/measure 化」は層が違う）**: この意味分類の適用は raw events（Logs）側の話で、2 つの層に分かれる。
> - **attribute 整形**（rename / resource 付与 / 高 cardinality の drop 等）: **collector** では Collector processor で、**direct** では Datadog Logs Pipeline remapper で行う。ここは配布形式が 2 つ（Collector processor サンプル / Logs Pipeline 設定）。
> - **facet / measure 化**（属性を検索 facet・集計 measure に昇格）: これは **Datadog 側の index 設定**であり、**Collector processor では代替できない**。よって **collector レシピでも Datadog 側の facet/measure 設定は別途必要**。collector の deliverable は attribute 整形までと切り分け、facet/measure 設定は recipe を問わず Datadog 側成果物として同梱する。
>
> なお `pr_metrics` gauge（Metrics）側は最初から metric なので facet/measure 概念は不要。本節の整形・facet 化は event-level 分析（Logs）の経路にのみ効く。

> **担保のガードレール（join 不可を踏まえた改訂）**: `pr_metrics` は cross-event join + latest-wins + sum に依存し、log-metric backend は join しないので **「measure + 次元タグの分類だけで backend が pr_metrics を再現できる」という当初の合否条件は成立しない（撤回）**。合否は representation ごとに 2 段で定義する:
> - **効率指標（`pr_metrics` 相当）**: client がローカル VIEW で集約し、**PR 単位の OTLP Metrics gauge（last-value、tags = `pr_url`/`coding_agent`/`user_id` + `repo`/`model`/`is_merged` 等）** として送れること。`session_count` / `tokens_per_session` / `tokens_per_tool_use` もこの行に含めるので backend 側 formula で出る
> - **event-level 分析**: raw events の attribute が B の意味分類どおり次元タグ / measure に落ち、backend で素の token 推移・流量・カウントを出せること（フィルタ次元 `is_merged` / `is_subagent` / `is_ghost` / `repo` が次元タグ、全数値が measure）

### dashboard / monitor はユーザが構築する

dashboard / panel / monitor は本 issue では提供・保守しない。どの backend でも、ユーザが B で露出した measure / 次元タグをもとに自由に構築する。参考として、想定される観察軸は `docs/metrics.md`（トークン効率 / 開発生産性 / 横断 A,B）と Grafana 版 dashboard を参照できる。

Grafana 版（`grafana/dashboards/agent-telemetry.json`、SQLite datasource 前提）は**そのまま残す**。ローカル / OSS 個人利用は引き続き SQLite で完結し、外部 backend 経路はそれと並列に動く。Grafana JSON → 各 backend dashboard JSON の自動変換は行わない（クエリ言語が違いすぎる）。

### 段階実装の見通し（child issue 分解候補）

**本 issue（この PR）の deliverable を一つに確定する**: ①設計判断を **issue 本文に記録**する（A の export 能力・2 representation・direct/collector・B の意味分類・join 不可の帰結）と、②[0038] と矛盾する `docs/design.md` の「OTLP Metrics 不採用・Logs 統一」記述の同期更新（矛盾回避のための例外、本 PR で実施済み）まで。**`docs/spec.md` / `docs/metrics.md` の本文反映、`docs/design.md` の責務分担追記、および実装はすべて下記 child issue に分解する**（本 issue では行わない）。順序は依存関係に従う:

1. **flush を export target 配列に拡張（A）**: 現行 `[server].endpoint + Bearer 固定 + JSON encoding`（`flush.go`、endpoint に `/v1/logs` を補完する base-endpoint モデル）を、**endpoint + 可変 auth header（Bearer / `DD-API-KEY` 等）+ encoding/protocol（JSON / protobuf）+ representation を持つ target の配列**にする。**endpoint モデルを明記する**: Datadog は signal ごとに別 path（Logs=`/v1/logs`、Metrics=`/v1/metrics`）なので、target の `endpoint` を「base + signal path を補完」とするか「signal ごとの完全 URL」とするかを spec で 1 つに固定し、実装時の解釈ブレを防ぐ。1 差分スキャンから raw events（Logs）と後述の gauge（Metrics）の 2 projection を、宛先ごとの独立カーソルで送る。docs に **direct レシピ**（client → backend 直送。**Datadog direct は現行の手組み JSON poster では不可で、OTLP SDK / protobuf exporter への切替が必要**。submit-only credential を client 設定に）と **collector レシピ**（`deploy/otel-collector/` or how-to で選んだ宛先へ router fanout。**client は既存 JSON exporter のままで、Collector が protobuf + `DD-API-KEY` 変換を担う**。SQLite ingest は Grafana 併用時の任意宛先で必須ではない）を同梱。**最初の backend として Datadog を例示**
2. **`pr_metrics` の client 側集約 + gauge 送信（A、効率指標）**: client がローカル `pr_metrics` VIEW を評価し、PR 単位の値を OTLP Metrics gauge（last-value）として送る経路を実装する。`session_count` / 各 ratio もこの行に含める
3. **attribute の意味分類を `docs/spec.md` に追記（B、backend 非依存 + Datadog リファレンス）**: 対応表を仕様本体として固定し、**2 配布形式**（direct 用の Datadog Logs Pipeline 設定 / collector 用の Collector processor サンプル）を同梱する。これは raw events 側（event-level 分析）の tag/measure 昇格に効く
4. **`docs/metrics.md` に backend 上の representation 対応を追記（任意）**: 各メトリクスについて「`pr_metrics` 系は client 集約の gauge をそのまま使う / event-level 系は raw events から backend formula で出す」のどちらかを明示
5. **`docs/design.md` に client / server / (Collector) / 外部 backend の責務分担を追記**: 「client は設定可能な OTLP export と **`pr_metrics` のローカル集約（gauge 化）** を担う。server は OTLP receiver + SQLite ingest だけ。外部 backend は gauge の格納・表示と event-level 集計を担う（cross-event 集約は backend 側では行わない）。collector レシピを採る場合のみ Collector が router として fanout と raw events 側の意味分類昇格を担う（direct では backend 側 Pipeline）」という分担を明文化

実装の前段で確認したい spike（Datadog をリファレンスとして実機検証する）:

- **Datadog OTLP intake の encoding/protocol 要件**: OTLP Logs / Metrics を JSON で受けるか protobuf 必須か、`DD-API-KEY` header / endpoint 形式（実機検証）。direct レシピが SDK/protobuf exporter 切替で済むか確認する
- Datadog OTLP Intake が attribute の cardinality / 数値型をどこまで素直に受けるか（実機検証。他 backend の傾向を測る最初のサンプル）
- **`pr_metrics` gauge の temporality / timestamp 設計**: gauge を送る際の timestamp（PR の最終更新時刻 vs 送信時刻）、同一 `pr_url` の再計算値を過去レンジでどう扱うか、Datadog の delta 要件（sum/cumulative は delta 必須、gauge は対象外）との関係を実機で確認し、range 集計で `last` を前提にして二重計上しない送り方を固める
- `service.name` / `service.version` の semantic conventions を agent-telemetry の `coding_agent` / `agent_version` にマップするときに、`service.name=agent-telemetry-claude` のように agent 別 service にするか、単一 service + 次元タグ分離にするか

### 触らない・後続 PR に回すもの

- `docs/spec.md` / `docs/design.md` の本文は本 issue 単体では原則更新しない（child issue / 段階実装の中で更新する）。**例外**: [0038] の「OTLP Metrics 不採用・Logs 統一」決定の改訂は、放置すると docs と本 issue が矛盾するため **本 PR で `docs/design.md` の該当「採用しなかった代替」記述を更新する**（responsibility split 等それ以外の design.md 本文は child issue 送り）
- **dashboard / monitor の提供・保守**（backend を問わずユーザ構築前提。本 issue は A + B で構築可能性のみを担保する）
- **backend 側での cross-event 集約の再現**（merged 限定等のフィルタ + join を backend formula で組ませる案）。log-metric backend は join しないので不可能であり、効率指標は client がローカル VIEW で集約して gauge 送信する（backend は gauge を格納・表示するだけ）
- **Datadog 以外の backend（New Relic / Honeycomb / Grafana Cloud）のリファレンス実装**: A の設定可能な OTLP export（direct / collector）と B の意味分類は backend 非依存なので、宛先と意味分類のレンダリング先を変えれば原理的に対応可能。ただし本 issue では **リファレンス実装を Datadog 1 本に絞る**（検証対象を 1 つに固定するため）。横展開は同じ A + B 上で後続 issue として起こす

## 前提

本 issue は [0038] の実装（OTLP/HTTP + events table + flush 経路の rename + migration）が一通り完了している前提で着手する。[0038] が pending / 実装中の間は本 issue も pending として扱ってよい。

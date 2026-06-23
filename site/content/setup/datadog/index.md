---
title: datadog
weight: 30
---

agent-telemetry から Datadog へ raw events（OTLP Logs）と PR 単位の効率 gauge（OTLP Metrics）を送るためのセットアップです。Datadog は agent-telemetry の **最初のリファレンス backend** であり、設定可能な OTLP export と attribute 意味分類（[issues/closed/0040](https://github.com/ishii1648/agent-telemetry/blob/main/issues/closed/0040-design-pluggable-otlp-export-backends.md)）を実 backend で例示する位置づけです。

中央サーバや SQLite を使う前提から先に把握したい場合は [local]({{< relref "/setup/local" >}}) / [server]({{< relref "/setup/server" >}}) を、外部契約は [docs/spec.md](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#サーバ送信) を、attribute → Datadog tag/facet/measure の対応は [docs/spec.md「OTLP export の attribute 意味分類」](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#otlp-export-の-attribute-意味分類) を参照してください。

## 前提条件

| 項目 | 用途 |
|---|---|
| Datadog アカウント | OTLP intake は **全プランで利用可能**（Free / Pro / Enterprise）。ただし custom metrics の保管・facet/measure 化は plan 上限の影響を受ける |
| Datadog API key（submit 用） | OTLP intake に同梱する `dd-api-key` header の値。Org Settings → API Keys から発行する。Datadog の API key は org-wide で **read scope は持たない submit-only key** だが、**スコープ縮小はできない**（漏洩時の blast radius は org 全体）— [issues/closed/0040](https://github.com/ishii1648/agent-telemetry/blob/main/issues/closed/0040-design-pluggable-otlp-export-backends.md) の direct/collector 選択指針を参照 |
| 自分の Datadog site の確認 | site ごとに OTLP endpoint が違う。Datadog 画面右下の avatar → My Preferences、または公式 [Getting Started with Datadog Sites](https://docs.datadoghq.com/getting_started/site/) を参照 |
| `agent-telemetry` CLI（[local]({{< relref "/setup/local" >}}) で導入済み） | flush の送信元 |

## direct と collector の使い分け

| 規模 | レシピ | 根拠 |
|---|---|---|
| 個人 / 小チーム（想定主用途） | **direct** | 追加プロセス無し。client の OTLP exporter を Datadog intake に直接向け、submit-only key を自分のマシンに置く。Datadog RUM 同様、client-side telemetry の正当パターン |
| team / 多 client | **collector** | OTel Collector が credential を 1 箇所で集約し、**client は backend credential を一切持たない**。encoding 変換・fanout・buffer/retry を一貫処理 |

direct は client から Datadog Intake へ直送、collector は client → OTel Collector → Datadog Intake の 2 段。Datadog 側の facet / measure 設定はどちらでも別途必要（[Datadog 側受け設定](#datadog-側受け設定) 参照）。**サーバ（`agent-telemetry-server`）を Datadog 経路に挟む構成はサポートしません**（[issues/closed/0046](https://github.com/ishii1648/agent-telemetry/blob/main/issues/closed/0046-design-responsibility-split-external-backend.md) 責務分担: server は SQLite ingest 専用、外部 backend への fanout は collector の責務）。

## 1. direct セットアップ

`agent-telemetry flush` から Datadog Intake へ OTLP/HTTP protobuf で直送します。

### 1-1. site → OTLP endpoint 早見表

| Datadog site | site parameter | OTLP base endpoint |
|---|---|---|
| US1 | `datadoghq.com` | `https://otlp.datadoghq.com` |
| US3 | `us3.datadoghq.com` | `https://otlp.us3.datadoghq.com` |
| US5 | `us5.datadoghq.com` | `https://otlp.us5.datadoghq.com` |
| EU1 | `datadoghq.eu` | `https://otlp.datadoghq.eu` |
| AP1 | `ap1.datadoghq.com` | `https://otlp.ap1.datadoghq.com` |
| AP2 | `ap2.datadoghq.com` | `https://otlp.ap2.datadoghq.com` |
| US1-FED | `ddog-gov.com` | `https://otlp.ddog-gov.com` |

site の最新一覧は公式 [Getting Started with Datadog Sites](https://docs.datadoghq.com/getting_started/site/) と OTLP intake docs（[Logs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/logs/) / [Metrics](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/)）を参照してください。**間違った site の endpoint を使うと `403 Forbidden` が返ります**。

> 末尾に `/v1/logs` や `/v1/metrics` を **付けないこと**。endpoint は base URL であり、`flush` 側で signal ごとに `/v1/logs` / `/v1/metrics` を補完します（[docs/spec.md endpoint モデル](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#endpoint-モデルbase--signal-path-補完)）。

### 1-2. config.toml に Datadog target を追加

`~/.config/agent-telemetry/config.toml`（`XDG_CONFIG_HOME` が設定されていれば `$XDG_CONFIG_HOME/agent-telemetry/config.toml`）に以下を追記します。

```toml
[[export]]
id = "datadog"
endpoint = "https://otlp.datadoghq.com"   # 上の早見表から自分の site を選ぶ。/v1/* は付けない
token = "${DD_API_KEY}"                   # 環境変数から読む（${VAR} は flush が展開）
auth_header = "dd-api-key"                # Datadog 固定。Authorization ではない
auth_scheme = ""                          # 空＝raw token（dd-api-key: <token>）
encoding = "protobuf"                     # Datadog OTLP logs intake は protobuf 必須
signals = ["logs", "metrics"]             # raw events と pr_metrics gauge の両方を送る
```

設定ポイント:

- `auth_header = "dd-api-key"` と `auth_scheme = ""` の組み合わせで `dd-api-key: <token>` 形式の header を送出します（`Authorization: Bearer <token>` ではないことに注意）。
- `encoding = "protobuf"` は **必須**。Datadog の OTLP logs intake は JSON を受理しません（[公式 docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/logs/) の "HTTP Protobuf exporter" 必須記述）。metrics intake は JSON も受理しますが、logs と揃えて protobuf にすると 1 target で 2 signal を一貫した wire 形式で送れます。
- `signals = ["logs", "metrics"]` で raw events と PR 単位 gauge の両方を 1 target で送ります。片方だけ送りたい場合はどちらかを外します（target ごとに別 cursor で前進）。
- `${DD_API_KEY}` は flush 起動時に環境変数から展開します。シェルや launchd / systemd の env で `DD_API_KEY` を export しておきます。**config に直書きしないでください**（dotfiles / リポジトリへの誤コミットを防ぐ）。

[server]({{< relref "/setup/server" >}}) の中央サーバや [local]({{< relref "/setup/local" >}}) の OSS collector と **同時に Datadog にも送りたい場合** は `[[export]]` を複数並べるだけです（target ごとに独立した cursor で前進し、片方失敗で他方は止まりません）:

```toml
[[export]]
id = "oss-collector"
endpoint = "http://localhost:4318"
encoding = "json"
signals = ["logs", "metrics"]

[[export]]
id = "datadog"
endpoint = "https://otlp.datadoghq.com"
token = "${DD_API_KEY}"
auth_header = "dd-api-key"
auth_scheme = ""
encoding = "protobuf"
signals = ["logs", "metrics"]
```

### 1-3. smoke test（dry-run → 実送信）

```fish
set -x DD_API_KEY <your-api-key>
agent-telemetry flush --dry-run        # 送信せず target ごとの件数とサイズだけ表示
```

期待出力（抜粋）:

```
flush[claude→datadog] dry-run: sent=N skipped=M batches=B payload=BYTES bytes encoding=protobuf
flush[claude→datadog] dry-run (metrics): pr_series=P session_series=W batches=B payload=BYTES bytes encoding=protobuf
```

`encoding=protobuf` が両方の行に出ていることを必ず確認してください（JSON にフォールバックしていると Datadog logs intake が `403` を返します）。

問題なければ実送信:

```fish
agent-telemetry flush --since-last
```

Datadog 画面で確認:

- **Logs Explorer**（[https://app.datadoghq.com/logs](https://app.datadoghq.com/logs)）で `service:agent-telemetry` を検索すると raw events が見えます。`@event_name`（OTLP の eventName field）で絞れます（`agent.session.started` / `agent.session.ended` / `agent.transcript.scanned` / `agent.pr.observed`）。
- **Metrics Explorer**（[https://app.datadoghq.com/metric/explorer](https://app.datadoghq.com/metric/explorer)）で `agent_pr_total_tokens` などの `agent_pr_*` / `agent_weekly_session_*` を検索すると gauge が描画されます。dimension（tag）は `pr_url` / `coding_agent` / `user_id` / `task_type` / `model`（[docs/spec.md `pr_metrics` gauge representation](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#pr_metrics-gauge-representationotlp-metrics) 参照）。

### 1-4. direct intake の payload 上限に注意

Datadog の direct OTLP intake は **uncompressed 500 KB / compressed 5 MB** の payload 上限を持ちます（[公式 docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/) の `413 Request Entity Too Large` 節）。`agent-telemetry flush` の内部上限は 50 MB なので、上限に達する前に Datadog 側で reject される可能性があります。

- **個人 / 小チーム（PR 数 < 数百）** では 1 batch < 数十 KB に収まる想定なので問題ありません。
- **大規模（PR 数が数百を超える）** で `413` が返る場合は collector 経由（次節）に切り替えてください。Collector の `batch` processor が Datadog Exporter 側で適切に分割してくれます。

## 2. collector セットアップ

OTel Collector を間に立て、client は `encoding = "json"` のままで送り、Collector が protobuf 変換と `dd-api-key` 注入を担います。レシピは [`deploy/otel-collector/`](https://github.com/ishii1648/agent-telemetry/tree/main/deploy/otel-collector) を参照（attribute 整形の OTTL も同梱）。

### 2-1. Collector を起動

```fish
cd deploy/otel-collector
set -x DD_API_KEY <your-api-key>
set -x DD_SITE datadoghq.com           # site 早見表の "site parameter" を入れる
set -x DEPLOY_ENV dev                  # resource attribute deployment.environment になる
docker compose up
```

詳細は [`deploy/otel-collector/README.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/otel-collector/README.md) を参照してください。Collector image は再現性のため `otel/opentelemetry-collector-contrib:0.153.0` に pin してあります（OTTL の path 記法が版で動くため）。

### 2-2. client の config.toml で Collector を指す

```toml
[[export]]
id = "collector"
endpoint = "http://collector.internal:4318"   # 自分の Collector の OTLP/HTTP receiver
encoding = "json"                              # Collector 宛ては JSON（既定）。protobuf 変換は Collector 側
signals = ["logs", "metrics"]
# Collector の前段に認証 proxy がある場合のみ token = "${COLLECTOR_TOKEN}" を足す
```

`DD_API_KEY` は Collector プロセスに渡し、**client には置きません**（team / 多 client 向けの credential モデル）。

### 2-3. k8s 参考デプロイ（Collector を Pod として立てる）

リポジトリ同梱の `collector-config.yaml` をそのまま ConfigMap として配り、`DD_API_KEY` は Secret で渡す最小構成です。本 binary（`agent-telemetry-server`）の k8s 例（[server]({{< relref "/setup/server" >}}) § 3）とは別物で、こちらは **OSS の OTel Collector contrib distribution** を立ち上げて Datadog Intake に送ります。

> Collector は `collector-config.yaml` の pipeline 定義に従って受信 → 整形 → forward するだけで cross-instance state を持たないので、`replicas` を増やしても多重カウントは起きません（gauge は per-flush の last-value、logs は冪等 dedup）。HA したい場合は `replicas: 2+` にできます。

ConfigMap と Secret はリポジトリのファイルから生成します（リポジトリ root で実行）:

```fish
kubectl create namespace agent-telemetry-collector

kubectl create configmap otel-collector-config -n agent-telemetry-collector \
  --from-file=collector-config.yaml=deploy/otel-collector/collector-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic datadog-credentials -n agent-telemetry-collector \
  --from-literal=DD_API_KEY=<your-api-key> \
  --dry-run=client -o yaml | kubectl apply -f -
```

そのうえで以下を `kubectl apply -f -` してください。`# REPLACE_ME` 箇所は cluster ごとに調整します。

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
  namespace: agent-telemetry-collector
spec:
  replicas: 1                    # 最小構成。stateless なので HA したい場合は 2+ に増やせる（上の note 参照）
  selector:
    matchLabels: {app: otel-collector}
  template:
    metadata:
      labels: {app: otel-collector}
    spec:
      containers:
        - name: collector
          # deploy/otel-collector/docker-compose.yaml と同じ pin。版を上げる際は collector-config.yaml の OTTL 互換を確認すること。
          image: otel/opentelemetry-collector-contrib:0.153.0
          args: ["--config=/etc/otelcol/collector-config.yaml"]
          ports:
            - {containerPort: 4318, name: otlp-http}
          env:
            - {name: DD_SITE,    value: datadoghq.com}   # REPLACE_ME: 自分の Datadog site parameter（site 早見表参照）
            - {name: DEPLOY_ENV, value: production}       # REPLACE_ME: resource attribute deployment.environment に入る値
            - name: DD_API_KEY
              valueFrom:
                secretKeyRef: {name: datadog-credentials, key: DD_API_KEY}
          volumeMounts:
            - {name: config, mountPath: /etc/otelcol}
          readinessProbe:
            httpGet: {path: /, port: otlp-http}           # 簡易チェック。`health_check` extension を collector-config に追加すれば専用 endpoint を使える
          resources:
            requests: {cpu: "100m", memory: "256Mi"}
            limits:   {cpu: "500m", memory: "512Mi"}
      volumes:
        - name: config
          configMap: {name: otel-collector-config}
---
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: agent-telemetry-collector
spec:
  type: ClusterIP                # cluster 内の client からのみ受ける。外部 client（VPN / VPC peering 越し）から送るなら NodePort / LoadBalancer / Ingress に変更
  selector: {app: otel-collector}
  ports:
    - {port: 4318, targetPort: otlp-http, name: otlp-http}
```

client 側 `config.toml` で endpoint を Service の DNS に向けます:

```toml
[[export]]
id = "collector"
endpoint = "http://otel-collector.agent-telemetry-collector.svc.cluster.local:4318"
encoding = "json"
signals = ["logs", "metrics"]
```

> **無認証 receiver の注意**: 上記 `collector-config.yaml` の OTLP receiver はアプリ層認証を持たないため、外部公開する場合は NetworkPolicy で送信元 namespace / Pod を絞るか、Ingress 側で mTLS / 認証 proxy を挟んでください。Collector レベルの per-client identity enforcement は本 PR のスコープ外で、[issues/closed/0058](https://github.com/ishii1648/agent-telemetry/blob/main/issues/closed/0058-design-server-per-client-identity-userid-spoofing.md) で別 PR 切り出し済みです。

> **OSS observability スタックを Datadog の代わりに使いたい場合** は [local]({{< relref "/setup/local" >}}) の手順 4 を参照してください。Mimir / Loki / Grafana がローカル compose で立ち上がり、Datadog credential 無しで PR 単位 gauge と raw events を検証できます。

## 3. Datadog 側受け設定

attribute を Datadog の一級タグ / 検索 facet / 集計 measure に昇格するための index 設定です。**direct / collector いずれのレシピでも必要**で、Collector processor では代替できません（Datadog 側の index 設定でしか実現できない）。

| 層 | 担当 | direct | collector |
|---|---|---|---|
| attribute 整形（rename / resource 付与 / 高 cardinality drop） | direct: Datadog Logs Pipeline / collector: OTel Collector processor | [`deploy/datadog/logs-pipeline.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/logs-pipeline.md) / [`logs-pipeline.tf`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/logs-pipeline.tf) | [`deploy/otel-collector/collector-config.yaml`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/otel-collector/collector-config.yaml) の OTTL transform |
| facet / measure 化（属性を検索 facet / 集計 measure に昇格） | Datadog 側 index 設定（recipe を問わず必要） | [`deploy/datadog/facets-measures.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/facets-measures.md) | 同左 |

attribute → tag / facet only / measure の分類は [docs/spec.md「OTLP export の attribute 意味分類」](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#otlp-export-の-attribute-意味分類)、Datadog レシピ全体の構成は [`deploy/datadog/README.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/README.md) を参照。

### 任意: resource attribute を tag に昇格する

direct で `service.name` / `service.version` resource attribute を Datadog の tag として出したい場合、Datadog OTLP metrics intake は optional の `dd-otel-metric-config` header で resource attribute → metric tag 変換を制御できます（[公式 docs](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest/metrics/) の "Configure the metric translator"）。`agent-telemetry flush` は現状この header を送出しないので、必要なら collector レシピ側（`deploy/otel-collector/collector-config.yaml` の `datadog` exporter 設定）で resource attribute → tag 昇格を行ってください。

## 4. cardinality 制御の注意

custom metric の cardinality はコストドライバなので、agent-telemetry は次の方針で抑えています:

- **`session_id` は gauge tag に出さない**（[docs/spec.md `pr_metrics` gauge representation](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#pr_metrics-gauge-representationotlp-metrics) の dimension）— 無制限に増殖するため。
- **`pr_url` は gauge tag に出る**が、PR が増えるたびに timeseries が単調増加するため Datadog の custom metric cardinality 限度に注意する。古い PR を retention / rollup で畳むのは Datadog 側の運用。
- **raw events（Logs）側では `branch` / `pr_title` / `session_id` / `parent_session_id` は facet only**（次元タグに昇格させない）。`deploy/otel-collector/collector-config.yaml` の OTTL は collector レシピで該当属性を drop し、direct レシピは Datadog Logs Pipeline で同等処理を入れます（[`deploy/datadog/logs-pipeline.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/logs-pipeline.md)）。

詳しい群分け（群 1〜群 5）は [docs/spec.md「OTLP export の attribute 意味分類」](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#otlp-export-の-attribute-意味分類) を正本としてください。

## 5. 定期 flush

`agent-telemetry flush --since-last` を cron / launchd / systemd timer で定期実行します。export target ごとに独立した cursor を持ち、Datadog target が一時的に失敗しても他 target に影響しません（[docs/spec.md per-target cursor 契約](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#agent-telemetry-statejson-への追加フィールド--per-target-cursor)）。

手順例（cron / launchd plist）は [server]({{< relref "/setup/server" >}}) の **7. flush の定期起動** をそのまま流用できます。Datadog target は `DD_API_KEY` を実行環境に export する必要があるので、launchd plist では `EnvironmentVariables` ブロックに、systemd unit では `Environment=DD_API_KEY=...`（ファイル権限注意）または `EnvironmentFile=` で渡してください。

## 6. トラブルシューティング

| 症状 | 原因 | 対処 |
|---|---|---|
| `403 Forbidden` | endpoint の site が違う / API key が間違っている / API key が削除済み | [site 早見表](#1-1-site--otlp-endpoint-早見表) で endpoint を確認、Datadog Org Settings → API Keys で key を再発行 |
| `413 Request Entity Too Large` | 1 batch が 500 KB（uncompressed）/ 5 MB（compressed）を超えた | PR 数が大規模なら collector 経由に切り替える（[2. collector セットアップ](#2-collector-セットアップ)） |
| logs intake で 400 系 | `encoding = "json"` のまま Datadog logs intake に送っている | `encoding = "protobuf"` に変更（logs intake は protobuf 必須） |
| `flush --dry-run` で `encoding=json` と出る | `encoding = "protobuf"` の指定が config.toml に無い / typo | config.toml の `[[export]]` に `encoding = "protobuf"` が入っているか確認 |
| `flush` の warning で「非ループバック宛てに http:// で平文送信します」 | endpoint が `http://` で書かれている | `https://` に修正（Datadog OTLP intake は TLS のみ。`http://` で出すと credential が平文で漏洩） |
| Metrics Explorer で `agent_pr_*` が出ない | metrics target に新規イベントが無く gauge が skip されている | `agent-telemetry flush --full` を 1 回叩いて cursor を無視して全 PR の現在 gauge 値を再送 |
| Datadog 側で `coding_agent` 等の tag が出ない | facet / measure 設定が未適用 | [`deploy/datadog/facets-measures.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/datadog/facets-measures.md) の手順を実行 |

実装の e2e 検証（[issues/closed/0047](https://github.com/ishii1648/agent-telemetry/blob/main/issues/closed/0047-chore-verify-otlp-export-pluggable-backends.md)）では direct（protobuf wire）/ collector（attribute 整形）両方の経路を mock receiver で PASS させています。本ページで詰まった場合は同 issue の検証ログも参考にしてください。

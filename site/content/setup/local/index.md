---
title: local
weight: 10
---

ローカルマシンに agent-telemetry を導入する手順です。

agent-telemetry は **ローカル単独で完結** します。本ページの手順を実施すると、`~/.claude/agent-telemetry.db`（client 側 SoR）に開発セッションが蓄積され、`flush` 経由で立ち上げたローカルの **otel スタック（OTel Collector → Mimir / Loki → Grafana）** で PR 単位の token 効率や開発生産性を可視化できます。

ローカル可視化は **otel+grafana に一本化** しました。SQLite (`~/.claude/agent-telemetry.db`) は append-only な `events` テーブルと集約 VIEW を保持する **client 側 SoR** で、Grafana は SQLite を直接読まず、`flush` がローカルの OTel Collector に送ったデータを Mimir/Loki 経由で読みます。SQLite を Grafana datasource に直結する旧経路は [legacy 経路](#legacy-sqlite-datasource-を直接読む経路) として残していますが、新規導入では otel スタックを推奨します。

複数マシンやチームメンバーで集計値を集約したい場合は、同じ `flush` の export target を中央 `agent-telemetry-server` や外部 backend に向けるだけで拡張できます（[server]({{< relref "/setup/server" >}})）。export を設定しなければデータ収集と SQLite 集約は従来どおり動き、どこへも送信しません。

動作の仕組みは [仕組み解説]({{< relref "/explain" >}}) と [docs/spec.md](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md) を参照してください。

## 前提条件

| ツール | 用途 |
|--------|------|
| Docker + Docker Compose | otel スタック（Collector / Mimir / Loki / Grafana）の 1 コマンド起動 |
| gh CLI | PR URL の自動補完（`backfill` コマンド） |
| Go 1.25+（任意） | ソースからビルドする場合 / `make oss-flush` を使う場合 |

> otel スタックは Grafana も含めて compose が提供するため、Grafana や SQLite プラグインを個別に用意する必要はありません（[legacy SQLite datasource 経路](#legacy-sqlite-datasource-を直接読む経路) を使う場合のみ Grafana 11+ と [frser-sqlite-datasource](https://github.com/fr-ser/grafana-sqlite-datasource) が必要です）。

## 1. CLI のインストール

[GitHub Releases](https://github.com/ishii1648/agent-telemetry/releases/latest) から OS/アーキテクチャに合ったアーカイブをダウンロードして展開します。

```fish
# macOS (Apple Silicon) の例
curl -L https://github.com/ishii1648/agent-telemetry/releases/latest/download/agent-telemetry_darwin_arm64.tar.gz | tar xz
mv agent-telemetry ~/.local/bin/
```

`~/.local/bin` が `$PATH` に含まれていることを確認してください。

> **ソースからビルドする場合（開発者向け）**
> ```fish
> git clone https://github.com/ishii1648/agent-telemetry.git
> cd agent-telemetry
> go build -o ~/.local/bin/agent-telemetry ./cmd/agent-telemetry/
> ```

> **`agent-telemetry setup` と `make install` の違い**
>
> - `make install` … バイナリ自体を `$PREFIX/bin` に配置する（`go build`）。
> - `agent-telemetry setup` … hook 登録の **手順を表示** するだけで、ファイルは書きません。

## 2. hook の登録

agent-telemetry が利用する hook は **手動** で登録します（個人の設定管理ツールから配布する形でも構いません）。`agent-telemetry setup` は登録例を表示するだけで自動登録はしません（ユーザが settings.json / config.toml を一元管理する構成と整合させるため）。

```fish
agent-telemetry setup                # 両 agent の登録例を表示
agent-telemetry setup --agent claude
agent-telemetry setup --agent codex
```

### Claude Code (`~/.claude/settings.json`)

```json
{
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "agent-telemetry hook session-start --agent claude"}]}
    ],
    "SessionEnd": [
      {"matcher": "", "hooks": [{"type": "command", "command": "agent-telemetry hook session-end --agent claude", "timeout": 10}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "agent-telemetry hook stop --agent claude", "async": true}]}
    ]
  }
}
```

`--agent` を省略しても既定値が `claude` のため動作します。`Stop` は応答ターンごとに発火するため `"async": true` を付けて登録し、Claude Code が hook プロセスの終了を待たずユーザの次操作に進めるようにします（Claude Code v2.1.0+ の Command hook フィールド）。

### Codex CLI (`~/.codex/hooks.json` または `~/.codex/config.toml`)

Codex には `SessionEnd` イベントが存在しないため、`Stop` hook が SessionEnd を兼ねます（最後の Stop 発火が事実上の SessionEnd）。`PostToolUse` hook は任意で、`gh pr create` 等の出力から PR URL を session-index に追記します。

```json
{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "agent-telemetry hook session-start --agent codex"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "agent-telemetry hook stop --agent codex"}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "agent-telemetry hook post-tool-use --agent codex"}]}
    ]
  }
}
```

`config.toml` 形式で書く場合は `[features] codex_hooks = true` を有効にした上で `[[hooks.SessionStart]]` / `[[hooks.Stop]]` を追加します。

### 検証

```fish
agent-telemetry doctor
```

binary の PATH 配置・データディレクトリ（`~/.claude/`, `~/.codex/`）の存在・hook 登録状況を agent ごとにチェックします。未登録の hook は warning として表示しますが、**自動修復は行いません**（ユーザの設定一元管理の前提を壊さないため）。

> **過去に `agent-telemetry install` / `hitl-metrics install` で自動登録した hook を取り除きたい場合**
>
> `~/.claude/settings.json` を直接編集して `agent-telemetry hook ...` / `hitl-metrics hook ...` を含むエントリを削除してください。`agent-telemetry doctor` が legacy hook を warning として一覧表示するので、それを参考にします。Codex 側 (`~/.codex/config.toml` / `~/.codex/hooks.json`) も同様に手動で削除します。

## 3. 初回データ生成

```fish
agent-telemetry backfill
agent-telemetry sync-db
```

`~/.claude/agent-telemetry.db` が生成されます（DB は両 agent を集約します。後方互換のためファイル位置は `~/.claude/` 直下のままです）。以降はセッション終了時に Stop hook が自動実行します。

特定 agent だけを処理したい場合は `--agent <claude|codex>` を付けます。省略時は検出された agent すべてを対象にします。

## 4. ローカル可視化（otel スタック）

リポジトリを clone した環境で、Collector → Mimir / Loki → Grafana の最小スタックを 1 コマンドで起動します。`flush` がローカルの Collector（認証不要）にデータを push し、Grafana がそれを Mimir（gauge metrics）/ Loki（raw events logs）経由で表示します。

> **⚠ ローカル限定・本番非対応**: この compose スタックは **開発用ローカル可視化専用** です。Grafana は anonymous access（ログイン不要・Admin）、OTLP receiver / Mimir / Loki は無認証で、公開ネットワークに晒すとダッシュボードの無認証閲覧と OTLP 注入を許します。誤公開を防ぐため compose は全 host port を `127.0.0.1`（loopback）に bind してあり、同一マシンからしか届きません。別ホストや `0.0.0.0` へ広げないこと。複数マシン / チームで集約する本番相当の経路は Bearer 認証付きの中央サーバ（[server]({{< relref "/setup/server" >}})）が担います。本番相当で使う場合の最低要件（Grafana 認証 / OTLP 認証 / TLS）は [`deploy/oss-observability/README.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/oss-observability/README.md) を参照してください。

### 4-1. config.toml に export target を追加

ローカル可視化は `flush` を localhost の Collector に向ける **credential 不要の export target** で成り立ちます。[`deploy/oss-observability/config.toml.example`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/oss-observability/config.toml.example) を `~/.config/agent-telemetry/config.toml` にコピーするか、以下を追記します:

```toml
[[export]]
id = "oss-collector"
endpoint = "http://localhost:4318"   # base URL; client が /v1/logs・/v1/metrics を補完
encoding = "json"                    # Collector 宛ては JSON（既定）
signals = ["logs", "metrics"]        # raw events(logs) と pr_metrics gauge(metrics) 両方
# token は不要（ローカル Collector は認証なし）
```

> ローカル Collector は認証なしのため `token` は省略します。`endpoint` さえあれば送信対象になります（[docs/spec.md「サーバ送信」](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md#サーバ送信)）。

### 4-2. スタックを起動して flush

```fish
make oss-up      # Collector(:4318) → Mimir/Loki → Grafana(:13001) を起動
make oss-flush   # ツリーをビルド → sync-db → flush（hook データを otel スタックへ投入）
```

`make oss-up` は Grafana を <http://localhost:13001>（匿名ログイン・Admin）で立ち上げます。停止は `make oss-down`。port を変えたい場合は `OSS_GRAFANA_PORT=<port> make oss-up`。

> `make oss-flush` は現在のツリーをビルドしてから `sync-db` → `flush` を実行します（導入済みバイナリが `flush` 非対応の古い版でも確実に流すため）。導入済みバイナリで流す場合は `agent-telemetry sync-db; agent-telemetry flush` を手で実行します。

### 4-3. dashboard を開く

Grafana（<http://localhost:13001>）の **agent-telemetry (OSS)** フォルダにある `agent-telemetry (OSS backend)` dashboard を開きます。次の 4 ブロックで構成されます:

- **状態評価（Tier 2）** — merged PRs / total tokens / PR per 1M tokens の stat と週別 merged PR 数 trend（`agent_pr_*` gauge を `last_over_time` で集約。`pr_metrics`（`is_merged = 1` 限定）由来のため merged-PR 寄与分）。
- **session-grain（Tier 3）** — top-level sessions 数と週別 token 消費 / tokens per session / ask_user_question per session（`agent_weekly_session_*` gauge 由来。非 PR・未マージを含む全 top-level session を session 単位で集計）。
- **PR 単位の外れ値検出（Tier 1）** — PR 別 token スコアカード / session_count / tokens per tool_use。
- **Raw events（Tier 1）** — Loki の OTLP Logs を LogQL でドリルダウン。

レシピの詳細・確認クエリ・Datadog レシピとの対応・Kubernetes への橋渡しは [`deploy/oss-observability/README.md`](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/oss-observability/README.md) を参照してください。

> **gauge は sparse 系列**: `agent_pr_*` / `agent_weekly_session_*` は flush した瞬間だけ push される sparse gauge です。素の instant クエリ（`sum(agent_pr_total_tokens)` 等）は最後の flush から Prometheus lookback delta（既定 5 分）を超えると空になるため、range 集計は必ず `last_over_time(metric[$__range])` で最終値を拾います（dashboard はこの idiom で実装済み）。

## legacy: SQLite datasource を直接読む経路

> **新規導入では非推奨**。otel スタック（上の手順 4）が第一級のローカル可視化です。以下は export を設定せず、SQLite (`~/.claude/agent-telemetry.db`) を Grafana の SQLite datasource で**直接** SQL 集計したい低レベル / オフライン用途のために残している経路です。otel 経路と機能は重複します。

frser-sqlite-datasource プラグインを入れた Grafana で `grafana/dashboards/agent-telemetry.json` を読みます。

### 方法 A: ローカル Grafana に手動設定

1. Grafana に [frser-sqlite-datasource](https://github.com/fr-ser/grafana-sqlite-datasource) プラグインをインストール

2. データソースを追加
   - Type: `SQLite`
   - Path: `~/.claude/agent-telemetry.db`（フルパスで指定）

3. ダッシュボードをインポート
   - Grafana の Import 画面で `grafana/dashboards/agent-telemetry.json` をアップロード
   - データソースに上記で作成した SQLite データソースを選択

### 方法 B: プロビジョニングファイルで自動設定

Grafana の設定ディレクトリにプロビジョニングファイルを配置します。

```fish
# データソース設定をコピー（パスを環境に合わせて編集）
cp grafana/provisioning/datasources/agent-telemetry.yaml /etc/grafana/provisioning/datasources/

# ダッシュボード設定をコピー
cp grafana/provisioning/dashboards/agent-telemetry.yaml /etc/grafana/provisioning/dashboards/

# ダッシュボード JSON をコピー
cp -r grafana/dashboards /var/lib/grafana/dashboards/agent-telemetry
```

データソース設定の `path` を自分の環境に合わせて変更してください。

```yaml
# grafana/provisioning/datasources/agent-telemetry.yaml
jsonData:
  path: /Users/<your-username>/.claude/agent-telemetry.db
```

### 方法 C: Docker（リポジトリ clone 環境向け）

リポジトリを clone した環境では、実 DB を mount した Grafana コンテナを 1 コマンドで起動できます。

```fish
make grafana-up          # ~/.claude/agent-telemetry.db を mount → http://localhost:13010
make grafana-down
```

別パスの DB を見たい場合は `AGENT_TELEMETRY_DB` で上書きします:

```fish
make grafana-up AGENT_TELEMETRY_DB=/custom/path/agent-telemetry.db
```

> **注意**: mount は読み書き可能です（SQLite が WAL モードのため `:ro` mount は不可）。frser-sqlite-datasource は SELECT のみで書き込みは行わないので実害はありませんが、Grafana コンテナに DB ファイルへの書き込み権限が渡る点を留意してください。

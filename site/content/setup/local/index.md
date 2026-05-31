---
title: local
weight: 10
---

ローカルマシンに agent-telemetry を導入する手順です。

> **⚠️ 改訂中（[issues/0055](https://github.com/ishii1648/agent-telemetry/blob/main/issues/0055-design-sqlite-grafana-datasource-removal.md)）**: 可視化を otel+grafana（Mimir/Loki）に一本化し、SQLite を Grafana datasource として直読みする経路（`frser-sqlite-datasource` プラグイン / `make grafana-up` / dashboard import）は撤去しました。本ページの **「Grafana で可視化」以降は旧 SQLite 版の手順**で、otel 版への書き換えは未了です（追跡: [issues/0056](https://github.com/ishii1648/agent-telemetry/blob/main/issues/0056-design-server-pipeline-otel-setup-docs.md)）。当面、otel backend のローカル可視化は [`deploy/oss-observability/`](https://github.com/ishii1648/agent-telemetry/tree/main/deploy/oss-observability) の compose を参照してください。hook 導入と `~/.claude/agent-telemetry.db` への蓄積（client SoR）は従来どおり有効です。

agent-telemetry は **ローカル単独で完結** します。本ページの手順を実施すると、`~/.claude/agent-telemetry.db` に集計結果が蓄積され、PR 単位の token 効率や開発生産性を otel+grafana（Mimir/Loki）で可視化できます。

複数マシンやチームメンバーで集計値を集約したい場合は、**オプトイン** で `agent-telemetry-server` に送信する経路を有効化できます（[server]({{< relref "/setup/server" >}})）。サーバ送信を設定しなくてもローカル利用は従来どおり動きます。

動作の仕組みは [仕組み解説]({{< relref "/explain" >}}) と [docs/spec.md](https://github.com/ishii1648/agent-telemetry/blob/main/docs/spec.md) を参照してください。

## 前提条件

| ツール | 用途 |
|--------|------|
| Grafana 11+ | ダッシュボード表示 |
| [frser-sqlite-datasource](https://github.com/fr-ser/grafana-sqlite-datasource) | Grafana の SQLite プラグイン |
| gh CLI | PR URL の自動補完（`backfill` コマンド） |
| Docker（任意） | E2E テスト用の Grafana 環境 |

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
      {"matcher": "", "hooks": [{"type": "command", "command": "agent-telemetry hook stop --agent claude"}]}
    ]
  }
}
```

`--agent` を省略しても既定値が `claude` のため動作します。

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

## 4. Grafana ダッシュボードの設定

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
make grafana-up          # ~/.claude/agent-telemetry.db を mount → http://localhost:13000
make grafana-down
```

別パスの DB を見たい場合は `AGENT_TELEMETRY_DB` で上書きします:

```fish
make grafana-up AGENT_TELEMETRY_DB=/custom/path/agent-telemetry.db
```

> **注意**: mount は読み書き可能です（SQLite が WAL モードのため `:ro` mount は不可）。frser-sqlite-datasource は SELECT のみで書き込みは行わないので実害はありませんが、Grafana コンテナに DB ファイルへの書き込み権限が渡る点を留意してください。

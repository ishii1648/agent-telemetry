---
title: architecture
weight: 10
---

agent-telemetry は 3 つの層で構成されます。各層の責務を分離してあるので、収集元の追加（新しい coding agent）や可視化先の差し替え（Grafana 以外）は、層をまたいだ変更を最小化できます。

## 全体図

```mermaid
%%{init: {'flowchart': {'nodeSpacing': 30, 'rankSpacing': 45}, 'themeVariables': {'fontSize': '14px'}}}%%
flowchart TB
    subgraph hooks["1. 収集 (hook)"]
        direction LR
        CC["Claude Code"]
        CX["Codex CLI"]
    end

    subgraph state["2. オンディスク (JSONL)"]
        direction LR
        CCJ["~/.claude/ session-index + transcript"]
        CXJ["~/.codex/ session-index + rollout"]
    end

    subgraph cli["3. 変換 (CLI)"]
        direction LR
        BF["backfill"]
        SD["sync-db"]
        BF --> SD
    end

    DB[("agent-telemetry.db (SQLite)")]

    subgraph viz["4. 可視化"]
        direction LR
        GR["Grafana"]
        SQL["SQL クライアント"]
    end

    CC --> CCJ
    CX --> CXJ
    CCJ --> cli
    CXJ --> cli
    cli --> DB
    DB --> GR
    DB --> SQL
```

## 各層の責務

### 1. 収集層（hook）

各 agent の hook が**セッションメタデータ**と**transcript**を agent ごとのデータディレクトリに書き出します。hook 自体はメトリクスを計算しません。**生イベントの保存**だけを担当します。

| agent | データディレクトリ | hook の登録先 |
|---|---|---|
| Claude Code | `~/.claude/` | `~/.claude/settings.json` |
| Codex CLI | `~/.codex/`（または `$CODEX_HOME`） | `~/.codex/config.toml` または `~/.codex/hooks.json` |

hook の詳細は [hooks]({{< relref "/explain/hooks" >}}) ページを参照してください。

### 2. オンディスク state

| ファイル | 役割 |
|---|---|
| `session-index.jsonl` | セッション 1 件 = 1 行の JSON。session_id / repo / branch / PR URL などを記録 |
| transcript JSONL（Claude） | assistant message ごとの token usage を含むフルトランスクリプト |
| rollout JSONL[.zst]（Codex） | `event_msg.payload.type == "token_count"` で累積 token を記録 |
| `agent-telemetry-state.json` | backfill の cursor。再 API 呼び出しを抑制 |

これらは agent ごとに**完全に分離**されており、単一の agent しか使わない環境でも他方は不要です。

### 3. 変換層（`agent-telemetry` CLI）

CLI は state を読んで SQLite に変換します。**通常は `Stop` hook が応答完了時に `backfill` → `sync-db` をブロッキングで連続実行する**ため、ユーザが明示的にコマンドを叩かなくても、エージェントの応答が返ってくるまでに DB が最新化されています（手動で再構築したい場合は [setup/local]({{< relref "/setup/local" >}}) 参照）。

- **`backfill`** — `gh` CLI を呼んで PR URL / merged / レビューコメント数などを補完。cursor を進めて再 API 呼び出しを避ける
- **`sync-db`** — JSONL と transcript を読んで `sessions` / `transcript_stats` を `INSERT OR REPLACE` で upsert（毎回フル再構築）

### 4. 可視化層

DB は `~/.claude/agent-telemetry.db` 1 ファイルに集約されます（agent は `coding_agent` カラムで区別）。これは **client 側の events SoR ＋ 集約エンジン**で、`pr_metrics` VIEW が PR 単位の集約（gauge 算出の前提）を提供します。`sqlite3` CLI / DBeaver 等での直接参照は可能ですが、**可視化は otel+grafana（Mimir/Loki）に一本化**しており、SQLite を Grafana datasource として直読みする経路は撤去しました（[issues/0055](https://github.com/ishii1648/agent-telemetry/blob/main/issues/0055-design-sqlite-grafana-datasource-removal.md)）。

client は `pr_metrics` の PR 単位 gauge（`agent_pr_*`）を OTLP Metrics で、raw events を OTLP Logs で送り、Grafana は Mimir(PromQL) / Loki(LogQL) を読みます。dashboard は [deploy/oss-observability/grafana/dashboards/agent-telemetry-oss.json](https://github.com/ishii1648/agent-telemetry/blob/main/deploy/oss-observability/grafana/dashboards/agent-telemetry-oss.json)（Tier 1: PR 単位 gauge + raw logs）。

## なぜこの構成か

- **agent ごとに収集元を分離、DB は集約** — 新規 agent を追加するときに既存の収集経路を壊さない。一方で集計は単一テーブルで完結する
- **hook は計算しない** — hook は agent プロセスをブロックする位置にあるので、永続化以外の計算を入れない方針。集計は CLI 側で `sync-db` 実行時に行う
- **`sync-db` は毎回フル再構築** — 差分更新のバグを設計から排除。スキーマハッシュが一致する限り `INSERT OR REPLACE` で安全に再生できる

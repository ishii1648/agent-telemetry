---
decision_type: design
tags: [grafana, sqlite, storage, deployment, architecture]
---

# Grafana / SQLite の物理結合をどう解消するか

Created: 2026-05-10

## 概要

`docs/setup-server.md` §4 の Grafana 同居版は「server と Grafana を同 pod の sidecar として置き、`ReadWriteOnce` PVC を二重 mount して同じ SQLite を共有する」構成を採用している。この構成自体は [0030](../closed/0030-doc-server-grafana-setup.md) で確定済みだが、その背景には Grafana の `frser-sqlite-datasource` が SQLite ファイルを直接 open する仕様がある。結果として、**汎用可視化ツールである Grafana と専用バックエンドである agent-telemetry-server が同 pod に物理結合** しており、責務分離・運用上の柔軟性で課題が残る。

別リポジトリ (homelab k3s への deploy 作業) でこの制約が改めて表面化したため、将来的に Grafana を別レイヤーへ分離するためのロードマップを issue として記録する。短期の方針は段階 0 を維持（sidecar のまま運用）、段階 2 / 3 の判断は実運用観測データが揃うまで保留する。

## 根拠

### 1. datasource 制約による sidecar 強制

- Grafana は `frser-sqlite-datasource` で SQLite ファイルを直接読む方式を採用している
- そのため Grafana プロセスは `agent-telemetry.db` への read アクセスを必要とする
- `ReadWriteOnce` PVC は別 pod から共有 mount できず、別 Deployment にするには **RWX 対応 StorageClass（EFS / Filestore / Azure Files）** が前提になる
- これは [0030 採用しなかった代替](../closed/0030-doc-server-grafana-setup.md) でも同じ理由で却下されており、homelab の単一ノード k3s のような構成では現実解にならない

結果として、Grafana を **別 pod / 別 namespace / 別 GitOps Application** に切り出すという、運用上自然な責務分離ができない状態にある。

### 2. `/query` endpoint 案の倒錯

k8s-lab 側で迂回策として「agent-telemetry-server に read-only `/query` HTTP endpoint を追加し、Grafana Infinity datasource から SQL を投げる」案が議論された。しかしこの方針には以下のトレードオフがある:

- 専用 ingest server が **SQL リレーを兼ねる** ことになり、書き込み専用の責務から逸脱する
- 認可（read-only enforcement / SQL injection 対策）をアプリ層で自前実装する必要がある
- Grafana からの読み取り QPS が単一プロセスに律速される
- replicas=1 / Recreate 制約（WAL 競合回避）を解除できず、可用性面でも sidecar と差が小さい

3-tier として **Grafana が RDBMS と直接話す** のが正攻法で、`/query` endpoint は短期の迂回策に位置づけるべきである。

### 3. SQLite 方言ロックイン

`grafana/dashboards/agent-telemetry.json` および `internal/syncdb/` 配下の集計クエリは `strftime` / `julianday` / `date('...', '+N days')` といった **SQLite 方言** に深く依存している。別 RDBMS への移行は機械翻訳ではなく、時刻関数・window 関数の差異吸収を伴う意味的な書き換えになるため、移行コストが高い。

## 問題

- 運用上自然な責務分離（Grafana を別レイヤーへ）が現状の sidecar 強制によって実現できない
- 短期の迂回策（`/query` endpoint）は server の責務を膨張させるトレードオフを伴う
- 長期の本格分離（別 RDBMS 採用）は、agent-telemetry の現価値である「**単一 Go binary、外部依存ゼロ、`.db` ファイル 1 個で完結**」を毀損するリスクがある

これらをどの順序で・どの境界で解消するかの方針が未確定である。

## 対応方針

### ロードマップ

| 段階 | 内容 | 状態 |
|---|---|---|
| 0 | sidecar 同居（[0030](../closed/0030-doc-server-grafana-setup.md) で確定、`docs/setup-server.md` §4） | 確定 |
| 1 | k8s-lab 等で sidecar のまま運用開始し、クエリパターン / メモリ使用量 / QPS を実測 | 進行中（k8s-lab 側） |
| 2 | server に read-only `/query` endpoint を追加。Grafana を Infinity datasource で **論理分離**（物理分離は段階 3 に持ち越す） | 未着手 |
| 3 | PostgreSQL backend を **dual-backend として併存追加**。CLI ローカル動作は SQLite を維持し、サーバ側は Postgres datasource で完全分離 | 未着手 |

段階 2 は実装規模が小さく、段階 3 への足場として有用。実運用で「どの dashboard query が遅いか / どの集計が単一プロセスを詰まらせるか」が見えてから着手するのが筋。段階 3 は **早すぎる最適化を避ける** ため、段階 1 で最低 1 ヶ月分の運用データを取ってから schema 設計に入る。

### PostgreSQL を選ぶ理由（MySQL との比較）

- dashboard SQL に時系列 group by / window 関数が多く、PostgreSQL の `date_trunc` / window 関数の方が SQLite からの移植が素直
- partial / functional index に対応しており、`WHERE is_subagent = 0` 系のクエリ（dashboard 全般で多用）に有効
- 将来 transcript metadata を JSON で保持する場合、`jsonb` + GIN index の生態系が成熟している
- SQLite の `strftime` + 文字列演算からの書き換えコストが MySQL より低い

### 単一バイナリ CLI 性の保護 — dual-backend pattern を必須化

agent-telemetry の現価値は「単一 Go binary、外部依存ゼロ、`.db` ファイル 1 個で完結」である。これを失うとローカル CLI 利用者の value proposition が崩れる。そのため、段階 3 では **dual-backend pattern を必須要件** とする:

- ローカル CLI: SQLite（現状維持、`.db` ファイル可搬性、`make grafana-up` 互換）
- サーバ: PostgreSQL
- storage layer を Go interface 化し、SQL は SQLite / PostgreSQL の 2 系統を維持
- データ移行コマンド（例: `agent-telemetry sync-db --target=postgres://...`）を検討

保守負担は概ね 2 倍になるが、CLI 性とサーバ分離性を両立するためのトレードオフとして受け入れる。

### 中庸案 — libSQL / sqld

[tursodatabase/libsql](https://github.com/tursodatabase/libsql) + sqld を採用すれば、SQLite 方言を維持したまま network 越しの read アクセスが可能になる。dashboard JSON もほぼ流用できる。ただし:

- Grafana datasource 側に libSQL 公式 plugin が無く、Infinity datasource + HTTP API ラッパー経由になる（段階 2 の `/query` endpoint 案と類似コストに収束する）
- 長期的な PostgreSQL 移行と比べると、生態系の成熟度・運用知見の蓄積で劣る

PostgreSQL 案の半分のコストで「Grafana 直結に近い体験」を得られる **retreat option** として記録しておく。本格分離が時期尚早と判断された場合の選択肢。

## Pending 2026-05-10

段階 2 / 3 のいずれを採用するか・着手時期の判断は、段階 1 で蓄積する運用データを見てから決める。最低限欲しい観測項目:

- ダッシュボード上の実クエリパターン（panel 別 latency、p95 / p99）
- SQLite ファイルサイズの成長カーブ
- Grafana sidecar のメモリ使用量
- WAL / `-shm` の挙動（Recreate 戦略の妥当性検証）

外部依存: k8s-lab 側での運用継続および計測データの取得。少なくとも 1 ヶ月程度のデータが揃った時点で判断材料を再評価し、open に昇格させるか、`docs/design.md` への反映で完結させるかを決める。

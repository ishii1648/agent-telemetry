---
decision_type: design
tags: [server, otel, grafana, sqlite, docs, follow-up]
related: [0028, 0029, 0030, 0055]
---

# server pipeline の処遇と setup/architecture docs の otel 一本化 rewrite

Created: 2026-05-31

## 背景

[0055] で SQLite を Grafana datasource として参照する経路（③ server/team・④ 個人ローカル）を撤去し、可視化を otel+grafana に一本化した。これにより 2 つの follow-up が発生する。

## 1. server pipeline（`internal/serverpipe/`）の処遇

`agent-telemetry-server` は client から OTLP Logs を受けて SQLite に ingest し、その SQLite を **Grafana が直読み**する前提だった（③）。③ 撤去により **ingest 先 SQLite に Grafana 読者が居なくなった**。

選択肢:

- **(a) 撤去**: server をやめ、team 集約は各 client が直接外部 backend（Mimir/Loki）へ push する構成に寄せる。`internal/serverpipe/`・`cmd/agent-telemetry-server/`・サーバ送信 spec を deprecate
- **(b) forward 化**: server を「OTLP Logs を受けて events を SQLite に貯めつつ外部 backend へ転送する receiver」に作り替える。集中バッファ／監査ログの価値を残す
- **(c) 現状維持（SQLite 集約のみ）**: Grafana 直読みはやめるが、SQLite を `sqlite3`/DBeaver 等で見る集約ストアとしては残す（可視化はしない）

判断材料: team 利用の実需要、外部 backend への直 push と中央集約のトレードオフ、[0029]/[0030] のサーバ設計意図。

## 2. setup / architecture docs の rewrite

[0055] の teardown で以下が旧 SQLite+Grafana フローのまま残っており、改訂中バナーのみ入れた状態。otel+grafana 一本化に合わせた本格 rewrite が必要:

- `site/content/setup/local/index.md`: 「frser-sqlite-datasource プラグイン導入 → `.db` を datasource 指定 → dashboard import → `make grafana-up`」の手順を、otel backend（`deploy/oss-observability/` の Collector+Mimir+Loki+Grafana）でのローカル可視化手順に差し替え
- `site/content/setup/server/index.md`: k8s sidecar（Grafana + SQLite 共有 PVC + `frser-sqlite-datasource`）の手順を、上記 server 処遇（1）の結論に沿って書き換え
- `site/content/explain/architecture/index.md`: 可視化層の記述は otel 経路に更新済み（軽微）。図（mermaid）の SQLite→Grafana エッジが残っていれば追従

## 合否

- server 処遇（a/b/c）が決定され、`internal/serverpipe/` と spec が整合
- setup/local・setup/server が otel+grafana 前提の手順に書き換わり、改訂中バナーが外れる
- gh-pages（lychee）が green

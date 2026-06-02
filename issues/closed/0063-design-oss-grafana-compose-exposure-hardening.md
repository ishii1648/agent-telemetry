---
decision_type: design
tags: [security, oss-observability, grafana, deployment]
---

# OSS/Grafana compose の anonymous access・無認証 OTLP receiver の公開リスク

Created: 2026-06-02

## 概要

セキュリティレビュー §3「供給網とローカル可視化」で High（公開時）と較正された残課題。`deploy/oss-observability` の Grafana/OSS compose はローカル用に anonymous access と未認証 OTLP receiver を有効化している。これらのポートを広く公開すると、ダッシュボードのデータ開示や OTLP 注入のリスクになる。

## 根拠

compose 例は開発・ローカル可視化用であって本番セキュリティコントロールではない。だが README/docs を見たユーザが OSS compose をそのまま外部公開すると、anonymous Grafana と無認証 OTLP intake が露出し High severity になる。「ローカル限定であること」を契約として明示し、誤公開を防ぐ必要がある。

## 問題

- Grafana の anonymous access が有効で、ポート公開時にダッシュボードが無認証閲覧される
- OTLP receiver が無認証で、公開時にイベント注入される
- compose 例が本番デプロイと誤解される余地がある

## 対応方針

- `deploy/oss-observability` の compose と `site/content/setup/` に「ローカル限定・本番非対応」を明示する（バインドを `127.0.0.1` に限定する例を示す）
- 本番相当で使う場合の最低要件（Grafana 認証 / OTLP 認証 / TLS）を docs に列挙する
- 設計判断として「compose は production hardening の対象外」を `docs/design.md` に固定する

## 解決方法

Completed: 2026-06-02

「compose は開発用ローカル可視化専用で production hardening の対象外」という契約を確定し、誤公開を多層で防ぐ形に固めた。hardening 自体は行わず（ローカルの 1 コマンド起動体験を壊さず、本番相当は Bearer 認証付きの中央 `agent-telemetry-server` が担うため）、代わりに次を実施:

- **loopback bind**: `deploy/oss-observability/docker-compose.yaml`（Collector `:4318` / Mimir `:9009` / Loki `:3100` / Grafana `:13001`）と top-level `docker-compose.yaml`（SQLite dashboard、Grafana `:13000`）の全 host port を `127.0.0.1:` 付き bind に変更。`docker compose config` で `host_ip: 127.0.0.1` を確認。既定で同一マシンからしか届かず、`0.0.0.0` 公開には明示操作を要求させる。
- **compose 内の明示**: 両 compose の冒頭バナーと、anonymous Grafana / 無認証 OTLP receiver の定義箇所に「ローカル限定・本番非対応」とリスクをコメントで明記。
- **docs の契約と最低要件**: `deploy/oss-observability/README.md` に「ローカル限定・本番非対応」節を追加し、本番相当で使う場合の最低要件（Grafana 認証 / OTLP・backend 認証 / TLS / ネットワーク境界 / ストレージ冗長化）を表で列挙。`site/content/setup/local`・`server` にも同契約の警告を追記。
- **design.md に固定**: `docs/design.md ## 可視化層` に「ローカル compose は production hardening の対象外（[0063]）」節を追加し、hardening しない理由（責務分離）と誤公開を防ぐ多層の契約を記録。

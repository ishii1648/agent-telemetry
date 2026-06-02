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

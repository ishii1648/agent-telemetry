---
decision_type: design
tags: [security, server, ingest]
closed_at: 2026-06-02
---

# agent-telemetry-server に TLS/レート制限/body タイムアウトが無く書き込み token が単一共有

Created: 2026-06-02

## 概要

セキュリティレビュー §3「Server ingest」で High と較正された残課題。`cmd/agent-telemetry-server/main.go` と `internal/serverpipe/handler.go` は bearer 認証・50MB body 上限・`ReadHeaderTimeout` まで備えるが、バイナリ単体では TLS・レート制限・request body の read/write timeout を持たず、書き込み token も単一共有。

## 根拠

operator が `--listen :8443` で公開すると `/v1/logs` はインターネット到達可能になりうる。設計上は ingress/proxy で TLS 終端・レート制限する前提だが、proxy 無しで直接公開された場合に gzip/slow-body DoS や token 総当たりの耐性が無く、High severity の露出となる。最低限「公開前提の運用条件」を spec/docs で明示し、緩和の有無を判断できる状態にしたい。

## 問題

- `internal/serverpipe/handler.go` に request body の read timeout / server の write timeout が無く、slow-body 接続でリソースを占有されうる
- レート制限がバイナリ内に無く、token 総当たり・大量 ingest を proxy 任せにしている
- 書き込み token が単一共有のため、漏洩時の影響範囲が全体に及ぶ（→ per-client identity は [[0058]] で扱う）

## 対応方針

- proxy 終端を前提とする運用条件を `docs/spec.md` / `site/content/setup/server.md` に明記する（TLS・レート制限は ingress 責務であることを契約として固定）
- バイナリ側で安価に閉じられる範囲（`http.Server` の `ReadTimeout`/`WriteTimeout`/`IdleTimeout` 設定）を追加検討する
- per-client identity / token scoping は別 issue [[0058]] に切り出す

Completed: 2026-06-02

## 解決方法

`decision_type: design` の方針どおり「TLS・レート制限は ingress/proxy の責務」を契約として固定し、binary 側は安価に閉じられる範囲だけを実装した。

- **契約の明文化**:
  - `docs/spec.md` に「サーバ binary の運用前提（信頼境界 / proxy 終端）」節を追加。proxy / ingress が担う関心事（TLS 終端・レート制限・接続元の制限）と、binary が単体で閉じる範囲（request timeout / payload 上限 / Bearer 認証）を表で固定。単一共有 token の影響範囲と per-client scoping は [0058] に切り出す旨を明記。
  - `site/content/setup/server/index.md` に「運用前提 — TLS / レート制限は proxy 側の責務」節を追加。`--listen` を直接公開しないこと、公開時は reverse proxy / ingress 背後に置くことを運用ガイドとして明記し、§3 の Ingress 例（cert-manager で TLS）と接続。
- **binary 側の最小実装**: `cmd/agent-telemetry-server/main.go` の `http.Server` に `ReadTimeout=60s` / `WriteTimeout=90s` / `IdleTimeout=120s` を追加（`ReadHeaderTimeout=10s` は既存）。slow-body / idle keep-alive 接続が goroutine を無期限に占有するのを防ぐ defence-in-depth。`WriteTimeout` は Go が request header 読了後に write deadline を開始する仕様に合わせ `ReadTimeout` より大きく取る。payload 上限（50 MB）・Bearer 定数時間比較は既存のまま。
- **非対象**: per-client identity / token scoping は [0058] の範疇として本 issue では扱わない。TLS / レート制限の binary 内蔵も「ingress 責務」の契約に倣い実装しない。

`go test ./...` 全 pass を確認。

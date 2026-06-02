---
decision_type: design
tags: [security, server, ingest]
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

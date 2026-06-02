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

## 解決方法

「TLS・レート制限は ingress/proxy 責務」という契約は #112 (`feat/0057-server-ingest-timeouts`) で先行して固定し、binary 側で安価に閉じられる範囲（`ReadTimeout=60s` / `WriteTimeout=90s` / `IdleTimeout=120s`）も同 PR で追加済み（`WriteTimeout` は Go が request header 読了後に write deadline を開始する仕様に合わせ `ReadTimeout` より大きく取る）。続く本 PR (#119) で「インターネット非公開を前提に書き込み token を廃止し、認証境界をネットワーク到達制御 + proxy に寄せる」最終形に切り替えた。

セキュリティと運用のバランス検討の結論:

1. **write token（`AGENT_TELEMETRY_SERVER_TOKEN`）を廃止** — 単一共有 token は per-client identity を持たず（[[0058]]）`user_id` 偽装もイベント汚染も防げない。実際に守れるのは「公開事故時の無認証書き込み（DoS / backend コスト増）」だけで、配布・rotation コストに見合わない。optional 化は default off で誰も有効化せず防御にならないため、削除を選んだ。
2. **既定 listen を loopback (`127.0.0.1:8443`) に変更** — 「インターネットに晒さない」を既定値で担保。loopback 以外へ bind すると起動時に警告ログを出す（`cmd/agent-telemetry-server/main.go` の `isLoopbackHost` 判定）。
3. **公開時は前段 proxy（TLS 終端 + 認証 + レート制限）を運用契約として明文化** — `docs/spec.md`「認証境界」、`docs/design.md`「認証境界 — ネットワーク到達制御 + proxy」節、`site/content/setup/server/`。
4. **#112 で導入した `ReadTimeout` / `WriteTimeout` / `IdleTimeout` は本 PR でも維持** — slow-body / slow-loris を安価に切る defence-in-depth（本格的なレート制限は proxy 責務）。

`/v1/logs` は書き込み専用で蓄積データを読み出す API を持たないため、誤公開でも漏えい（confidentiality）は起きない。残る integrity（汚染）/ availability（DoS・コスト）を上記 2〜4 で抑える。per-client identity / token scoping（偽装の根治）は [[0058]] のスコープ。

実装範囲: `cmd/agent-telemetry-server/main.go`（token 撤去・bind 既定・公開警告）、`internal/serverpipe/handler.go`（`checkAuth` / `Token` フィールド撤去）、`internal/serverpipe/handler_test.go`、`docs/spec.md` / `docs/design.md` / `site/content/setup/server/`。

Completed: 2026-06-02

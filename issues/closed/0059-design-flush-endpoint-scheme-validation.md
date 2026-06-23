---
decision_type: design
tags: [security, flush, serverclient, config]
---

# flush export endpoint の scheme 未検証で http:// 平文流出のリスク

Created: 2026-06-02

## 概要

セキュリティレビュー §3「Client export」で Medium と較正された残課題。`internal/serverclient/*` は endpoint の scheme と auth header を強く検証しない（config が operator 制御のため）。tokenless localhost collector は意図的にサポートする一方、誤った `http://` 利用でメタデータと token が平文で流出しうる。

## 根拠

config は operator 制御で SSRF はスコープ外だが、`http://` への誤設定は事故として起こりやすく、`user_id`/`cwd`/`repo`/`branch`/`pr_url` や bearer token が平文でネットワークに乗る。localhost 例外を保ったまま、非ループバック宛ての平文送信を検知して警告できると事故を防げる。

## 問題

- export target の scheme が `https` か検証されず、localhost 以外への `http://` 送信を素通しする
- auth header 付きの平文送信時に token 漏洩リスクがあるが警告が無い

## 対応方針

- `internal/serverclient/*` で非ループバック宛ての `http://`（特に auth header / token 付き）を検知して warn / refuse する方針を design に記録する
- tokenless localhost collector の意図的サポートは維持する（loopback は許容、それ以外は明示 opt-in）
- `doctor` で誤設定を検出するヒント追加も検討する

Completed: 2026-06-02

## 解決方法

「loopback は許容、それ以外の `http://` は warn」を方針として確定し、`docs/design.md`（`## サーバ送信 > export endpoint の scheme 検証`）と `docs/spec.md`（`[[export]]` 節）に記録した。**refuse ではなく warn を採用**: config は operator 制御で稀に意図的な平文ホップもありうるため、送信を止めて event を silent に落とすより、送信を通したうえで stderr に警告を出す方が事故と意図を切り分けやすい。将来 refuse したい場合は明示の opt-in フラグを足す前提とした。

実装:

- `internal/serverclient/security.go` を新設。`ExportTarget.IsInsecurePlaintext`（scheme が `http` かつ host が非 loopback の判定。`localhost` / `127.0.0.0/8` / `::1` を loopback 扱い）と `InsecurePlaintextTargets`（Configured な target だけ走査し、token 同伴を `HasCredential` で escalate）に判定を一本化。
- `flush`: `FlushResult.InsecureTargets` に積み、`Summarize` が非ループバック `http://` target を警告（token 付きは「token が平文で漏洩」と強い文言）。送信自体は継続する。
- `doctor`: `config.toml` の export target を読み、同じ判定で誤設定ヒントを表示（loopback 例外を明記）。warning 扱いで `HasFailure` には影響しない。`Env.ExportTargets` seam でテストは hermetic。
- テスト: `internal/serverclient/security_test.go`（判定ポリシー・`Summarize` 文言）、`internal/doctor/doctor_test.go`（非ループバック http warning、loopback 非フラグ、非 failure）。`go test ./...` green。

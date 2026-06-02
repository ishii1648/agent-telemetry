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

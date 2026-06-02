---
decision_type: design
tags: [security, server, multi-tenancy]
---

# server に per-client identity が無く user_id 偽装・dashboard 汚染が可能

Created: 2026-06-02

## 概要

セキュリティレビュー §3「データ整合性とマルチテナンシー」で High と較正された残課題。`internal/serverpipe/handler.go` は必須フィールドのみ検証して任意の追加属性を保存し、`user_id` は認可 identity でなく単なる集計次元。`AGENT_TELEMETRY_SERVER_TOKEN` を持つクライアントは `event_id`/`session_id`/`coding_agent`/`eventName`/全属性を任意に選べる。

## 根拠

信頼できる小規模チームでは許容できるが、侵害された 1 クライアントが他ユーザを騙る forged イベントを送って共有ダッシュボードを汚染したり `user_id` を偽装できる。per-user isolation を約束するデプロイへ広げる前に、identity と認可境界の方針を決めておく必要がある。

## 問題

- per-client identity・署名・認可境界が無い（書き込み token は単一共有 → [[0057]]）
- `user_id` がイベント属性に過ぎず、クライアントが任意に詐称できる
- 高カーディナリティラベル注入で backend コストを押し上げられる（Medium の cost spike とも関連）

## 対応方針

- 「single-team / 単一 token は受容リスク」という現行スタンスを `docs/design.md` に明記し、どの脅威を意図的に許容しているかを固定する
- per-user token / token scoping / 監査ログ / backend カーディナリティ制御を将来オプションとして design に列挙する（実装可否は別 PR）
- まずは scope を文書化し、isolation を約束するデプロイ向けの要件を切り分ける

---
decision_type: design
tags: [security, server, multi-tenancy]
closed_at: 2026-06-02
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

## 解決方法

設計判断の記録に集中し、実装は別 PR に切り出した（本 issue は `decision_type: design`）。`docs/design.md` の「サーバ側集約パイプライン > 認証 — 単一 API key」直後に **「信頼境界と identity のスコープ（[0058]）」** 節を新設し、次を固定した:

- **現行スタンスの明文化**: 単一共有 write token のみで信頼境界を表現し、`user_id` は認可済み identity ではなく自己申告の集計次元である、と明記。前提は「token を共有する全員が相互に信頼できる小規模チーム」。
- **意図的に許容する脅威の列挙**（single-team スコープ内では受容）: `user_id` 偽装 / イベント詐称・汚染 / 高カーディナリティ注入による backend コスト増。
- **将来オプションの列挙**（per-user isolation を約束するデプロイ向け、実装可否は別 PR）: identity enforcement（必須要件＝偽装・汚染を防ぐ認可境界）として per-user token / token scoping、補助要件（検知・コスト制御で認可境界ではない）として監査ログ / backend カーディナリティ制御を **層を分けて** 列挙。
- **isolation を約束するデプロイ向け要件の切り分け**: テナント間・ユーザ間分離を SLA として謳う運用は **identity enforcement のいずれか** を前提条件とすると注記。監査ログ・カーディナリティ制御だけでは偽装そのものを防げないため SLA を満たさないことも明記。
- **Collector を挟む production 構成での緩和可否**: cardinality / コスト制御（補助要件）は Collector processor が実効的な緩和になる。一方 `user_id` 偽装（必須要件）は「挟むだけ」では緩和にならず、per-client credential を Collector の auth extension で検証して認証 identity から `user_id` を `upsert` 上書きする場合のみ enforce できる（= per-client credential が前提で、Collector は実装配置先の一案にすぎない）。SQLite + Grafana の server path は Collector を経由しないため別途保護が要る、という整理を design に追記。

あわせて `## 既知の制約` に同節への 1 行ポインタを追加し可視性を確保した。単一共有 token そのものの運用（rotation・配布・漏洩時対応）は [[0057]] のスコープとして分離。

Completed: 2026-06-02

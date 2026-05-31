---
decision_type: spec
tags: [retro, multi-user, schema, server-aggregation]
closed_at: 2026-05-08
---

# user 識別子の導入 — マルチユーザー集約への布石

Created: 2026-05-08
Retro-converted: 2026-05-10 (from docs/history.md §11)

## 概要

ローカル単独利用前提（手元 `~/.claude/agent-telemetry.db` に閉じる）から、サーバ側で複数ユーザーのデータを集約する構成（[0009](0009-feat-server-side-metrics-pipeline.md)）に向けた前提整備として、`session-index.jsonl` と `sessions` テーブルに `user_id` フィールドを導入した。

## 根拠

- サーバ集約（0009）の AuthN/AuthZ は user 識別子の表現が決まらないと割れる。先に user_id の表現を確定させて 0009 を進めやすくする
- ローカル単独利用ではゲート不要だが、フィールド自体は早めに schema に入れておくと、後から retrofit するより負荷が低い
- pair coding で同一 PR を複数人が触るケースで、`pr_metrics` を user 別に集計できる土台が必要

## 問題

- user 識別子の取得元と優先順位（環境変数 / config / git config / fallback）の決定
- `git config --local` を見るかどうか（cwd 依存で人物が分裂するリスク）
- 形式（メール / pseudonym / UUID / ハッシュ化）の選択
- `pr_metrics` VIEW の集約軸を変えるかどうか
- 既存レコードの埋め戻し方針

## 対応方針

ローカル単独利用では `unknown` のままでも従来通り動作する形で、schema と JSONL に `user_id` を追加。サーバ送信時のゲートは [0009](0009-feat-server-side-metrics-pipeline.md) 側の責務として分離。

## 解決方法

主な設計判断:

| 判断 | 内容 |
|---|---|
| 取得順序 | `AGENT_TELEMETRY_USER` 環境変数 → `~/.claude/agent-telemetry.toml` の `user` キー → `git config --global user.email` → `unknown` |
| `git config --local` を見ない | cwd 依存で人物が分裂するのを避け、マシン跨ぎで同一人物を束ねる本来目的に合わせる（OSS と業務でメールを分ける運用と逆方向にならないため） |
| 形式は任意の文字列 | メール / pseudonym / UUID どれでも可。ハッシュ化はしない（join 不可で複数マシン集約に困る、PII 分離が必要なら TOML に pseudonym を入れる運用で十分） |
| 欠損時は `unknown` | hook を失敗させない。サーバ送信時のゲート判定は 0009 の責務 |
| 既存レコードへの埋め戻し | `sync-db` 実行時に `user_id` 欠落レコードを現在の解決値で埋め、JSONL に書き戻す。マイグレーションコマンドは追加しない |
| `pr_metrics` VIEW の集約軸に追加 | GROUP BY を `(pr_url, coding_agent, user_id)` に拡張し、pair coding で同一 PR を複数人が触った場合に意味的に正しく分離。単独利用時の集計結果は変わらない |
| `session_concurrency_*` VIEW は未変更 | 既存互換維持。user 別の同時実行数は将来必要になったら別 VIEW として追加 |

実装場所:

- `internal/userid/` で取得順序ロジックを集約
- `internal/sessionindex/` で JSONL に書き出し
- `internal/syncdb/schema/schema.sql` で `sessions.user_id` カラム追加、`pr_metrics` VIEW の GROUP BY 拡張
- `internal/syncdb/syncdb.go` の sync ループで埋め戻し

## 採用しなかった代替

- **`git config --local` を最優先**: リポジトリごとに別 email を設定する運用（OSS と業務でメールを分ける）で同一人物が分裂し、user attribution の本来目的と逆方向になるため不採用
- **メールアドレス固定（ハッシュ化なし）+ `user_display` 列追加**: 表示と保存を分離する案もあったが、TOML に pseudonym を書くだけで同等の運用ができるため列追加はしない（YAGNI）
- **0009 のサーバ仕様を待つ**: AuthN/AuthZ は user 識別子の表現が決まらないと割れるため、本 issue を先行確定して 0009 を進めやすくする

## 依存関係

- [0009](0009-feat-server-side-metrics-pipeline.md)（サーバ側転送）。本 issue 単独でも実害はないが、価値が顕在化するのは 0009 が動いてから
- [0018](0018-spec-multi-coding-agent-support.md) の `sessions` 複合 PRIMARY KEY を `(session_id, coding_agent)` のまま維持（user_id は集約軸であって主キー軸ではない）

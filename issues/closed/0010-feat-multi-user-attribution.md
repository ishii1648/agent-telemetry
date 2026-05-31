---
decision_type: spec
tags: [user-id, multi-user, schema]
closed_at: 2026-05-08
---

# 複数人のデータが混在しても誰のものか区別できるようにする

Created: 2026-05-08

## 概要

将来的に複数ユーザーの metrics を 1 つのサーバ／DB に集約することを前提に、各レコードがどのユーザーのものかを識別できるよう、データモデルと収集経路に user 識別子を追加したい。現状は単一ユーザー（手元 `~/.claude/agent-telemetry.db`）前提のため、user 列が存在しない。

## 根拠

- 0003（サーバ側転送・加工・表示）が実現すると、同一サーバに複数ユーザーのデータが混在する。識別子なしでは「PR 単位のトークン効率」を user 軸で切れず、ダッシュボードとして成立しない
- チーム比較・自分の経時変化・特定 PR のレビュアー単位分析など、`docs/metrics.md` の観察軸はいずれも user 識別を前提にしないと意味を持たない
- 同一ユーザーが複数マシン（自宅 / 業務 / CI）で Claude を使った場合に「同じ人」として束ねるためにも、安定した識別子が要る
- 現スキーマ（`session`, `pr_metrics` など）は user 列を持たない。後付けで列追加する場合のマイグレーションは早めに方針を決めたい

## 問題

仕様未確定の論点:

- **識別子の出所**: `git config user.email` / OS の `USER` 環境変数 / 専用の `~/.claude/agent-telemetry.toml` に書く ID / OIDC 等の外部認証から取得 — どれを正とするか
- **識別子の形式**: メールアドレスそのもの / ハッシュ化 / 不透明な UUID — プライバシーと運用性のトレードオフ
- **マシン跨ぎの同一視**: 同一人物が複数マシンから送ったデータを束ねるキーをどう共有するか（識別子の手動配布 / リポジトリ管理 / サーバ側 join）
- **欠損時の扱い**: 識別子が取得できない／設定されていない場合に hook を失敗させるのか、`unknown` で送るのか
- **データモデルへの反映範囲**: `session` / `pr_metrics` / 将来の集計テーブル各々に user 列を持つのか、外部キーで正規化するのか
- **既存データの扱い**: 既に手元 DB に蓄積されたレコードに後付けで user を埋める手段（`sync-db` で再付与 vs マイグレーション一発で全件埋める）
- **サーバ側の権限分離**: 自分のデータのみ書ける／チーム全員のデータを読める／他人のデータは読めない、といった粒度

## 対応方針

設計セッションで `docs/design.md` に方針を書き起こす。たたき台:

- 識別子は `git config user.email` を第一候補、`~/.claude/agent-telemetry.toml` の `user` キーで上書き可能とする。CI など git config が無い環境では設定ファイルを必須にする
- 形式はメールアドレスそのまま（ハッシュ化は組織内利用を阻害する。プライバシー要件が出てから検討）
- 欠損時は hook を失敗させず `unknown` として記録する。Stop hook の UX を壊したくないため
- スキーマ変更は `session` テーブルに `user TEXT NOT NULL DEFAULT 'unknown'` を足し、`pr_metrics` 等の派生は join で解決する方針を仮置き
- 既存ローカルデータは `sync-db` 実行時に「設定された user 値で埋める」ロジックを入れる

たたき台はあくまで叩きで、0009 のサーバ仕様確定と合わせて再検討する。`docs/spec.md` のデータモデルセクション更新が必須。

依存: 0009（サーバ側転送・加工・表示）— サーバが無ければ識別子の必要性自体が薄い。先行して 0010 を仮決めしておくことで、0009 のサーバ仕様検討を進めやすくする狙いはある（順序は逆でも成立する）。

Completed: 2026-05-08

## 解決方法

`session-index.jsonl` レコードと `sessions` テーブルに `user_id` フィールドを追加し、SessionStart hook 経由で取得・記録する経路を実装した。仕様は `docs/spec.md`、設計判断は `docs/design.md` の「ユーザ識別子」節、retro 化された決定記録は [0024-spec-introduce-user-id-field.md](0024-spec-introduce-user-id-field.md) に書き起こした。

### 確定した方針

- **取得順序**: `AGENT_TELEMETRY_USER` env → `~/.claude/agent-telemetry.toml` の `user` キー → `git config --global user.email` → `unknown`
- **`git config --local` は意図的に見ない**: cwd 依存で人物が分裂するのを避けるため
- **形式は任意文字列**: ハッシュ化しない。表示名分離もしない（pseudonym で代替可能）
- **欠損時は `unknown`**: hook は失敗させない。サーバ送信ゲートは 0009 の責務
- **`pr_metrics` VIEW の GROUP BY** に `user_id` を追加（pair coding で人物別に正しく分離するため）
- **既存データの埋め戻し**: `sync-db` が JSONL の欠落レコードを現在の解決値で埋め、JSONL にも書き戻す

### 主な変更点

- `internal/userid/` を新規追加（Resolver と最小 TOML パーサ + テスト）
- `internal/sessionindex.Session.UserID` フィールド + `User()` ヘルパ追加
- `internal/hook/sessionstart.go` で SessionStart 時に `user_id` を埋める
- `internal/syncdb/schema.sql` に `sessions.user_id` 追加・`pr_metrics` / `pr_merged_at_approx` の集約軸に user_id 追加・`idx_sessions_user_id` 追加
- `internal/syncdb/syncdb.go` で user_id 欠落レコードの埋め戻しと JSONL 書き戻し
- `internal/doctor/` に user 検証（解決結果と取得元を表示、`unknown` は warning）

### 採用しなかった代替

- `git config --local` を見る案: cwd 依存で同一人物が分裂するため
- メール固定 + ハッシュ化: 表示分離は TOML に pseudonym を書くだけで成立するため
- `user_display` 列を追加: YAGNI、必要になったら追加

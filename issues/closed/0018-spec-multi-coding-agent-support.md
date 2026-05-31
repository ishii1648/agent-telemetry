---
decision_type: spec
tags: [retro, multi-agent, codex, schema, breaking]
closed_at: 2026-05-02
---

# Claude Code 単一エージェント → マルチコーディングエージェント対応

Created: 2026-05-02
Retro-converted: 2026-05-10 (from docs/history.md §5)

## 概要

Claude Code 専用ツールとして暗黙にハードコードされていたデータディレクトリ・hook 入力スキーマ・transcript 形式を、Codex CLI も同時に扱える構造に拡張した。両エージェントを単一の SQLite DB に集約しつつ、`sessions.coding_agent` カラムで区別する。

## 根拠

- Codex CLI が SessionStart / Stop / PostToolUse 等の lifecycle hooks と、`event_msg.payload.type=="token_count"` で累積トークン記録を提供する rollout JSONL をサポートしはじめた
- 同一の Grafana datasource で複数 agent を比較できれば、agent ごとの token 効率や同時実行性をそのまま並べて評価できる
- Claude / Codex 両方を併用する開発スタイルが一般化しつつある中で、別 DB で運用すると比較分析がしにくい

## 問題

- 既存スキーマ・JSONL 形式・hook 入力が暗黙に Claude 前提（`~/.claude/` 固定、PostToolUse スキーマなど）
- Codex には Claude のような明示的な SessionEnd hook がない
- Claude / Codex 両方で UUID が衝突する可能性がある（両方共 v4 UUID）
- 既存ユーザーの `~/.claude/settings.json` 登録（フラグなし）を壊さない必要

## 対応方針

DB は単一に集約し、agent ごとの差分は adapter 層で吸収する。`coding_agent` カラムで区別。

主な設計判断:

| 判断 | 内容 | 理由 |
|---|---|---|
| DB を単一集約 | `~/.claude/agent-telemetry.db` を維持し `sessions.coding_agent` で区別 | Grafana datasource の後方互換 |
| データ収集元を agent ごとに分離 | `~/.claude/session-index.jsonl` と `~/.codex/session-index.jsonl` を別ファイル | adapter で読み込み元と transcript パーサだけを差し替え |
| PRIMARY KEY を複合化 | `(session_id, coding_agent)` | UUID 衝突への保険 |
| Codex の SessionEnd 不在を Stop hook で代替 | `ended_at` を Stop hook で毎回上書き | プロセス kill のケースは backfill が rollout JSONL の最終 event でフォールバック |
| `reasoning_tokens` カラム追加 | Claude では 0 固定、Codex では token_count イベントの reasoning を実値 | Codex の reasoning が Claude の cache とは別軸 |
| `--agent` フラグ既定値を `claude` | 既存 `~/.claude/settings.json` 登録が壊れない | フラグなし呼び出しの後方互換 |
| `install` → `setup` リネーム + `uninstall-hooks` 独立化 | CLI 表面が変わる節目で認知的負荷を一掃 | `install` が破壊的に見える誤解を防ぐ |

## 解決方法

- `internal/agent/` に agent abstraction を新設、`internal/agent/{claude,codex}/` で adapter 実装
- `internal/sessionindex/` を agent 切替対応に拡張（読み込み元と書き込み先の指定）
- `internal/syncdb/schema/schema.sql` で `sessions.coding_agent` / `reasoning_tokens` を追加し PRIMARY KEY を複合化
- `internal/transcript/` の parser を agent ごとに分岐
- CLI に `--agent` フラグを追加（既定 `claude`）。`install` を `setup` にリネームし、`uninstall-hooks` を独立サブコマンドへ分離（旧 `install` は deprecation warning 付き alias）

## 採用しなかった代替

- **DB を agent ごとに分割**: Grafana datasource を二重に管理する必要があり、比較クエリが書きにくい
- **`coding_agent` をビューレベルだけで区別**: 既存テーブルに後追いカラムを追加しないと session 重複が解決できない
- **Codex の SessionEnd を rollout JSONL から逆算**: hook 不在による遅延・取りこぼしが大きい。Stop hook での上書きが reliable

## 参照

- 後続 [0024](0024-spec-introduce-user-id-field.md)（user_id フィールド導入）が複合 PRIMARY KEY と adapter 構造の延長線上

---
decision_type: spec
supersedes: [0014]
tags: [retro, rename, breaking, packaging]
closed_at: 2026-05-04
---

# リポジトリ名変更 — hitl-metrics → agent-telemetry

Created: 2026-05-04
Retro-converted: 2026-05-10 (from docs/history.md §8)

## 概要

ツール名・モジュールパス・バイナリ名・DB ファイル名・環境変数・Grafana datasource UID・hook 登録 command すべてを `hitl-metrics` から `agent-telemetry` に一斉に変更した。後方互換のための名残を残さず、破壊的変更を一度で済ませる方針。

## 根拠

- 「HITL（Human-In-The-Loop）」は ML / ロボティクス由来の抽象用語で、Claude Code / Codex CLI といった coding agent 領域に焦点が合っていない
- 本ツールが観測する 6 観察軸（token 効率・キャッシュ・推論・ライフサイクル・対話の摩擦・同時実行）のうち、HITL に直結するのは「対話の摩擦」のみで、残りは agent そのものの振る舞いを測る指標
- Claude Code の auto mode 普及により、HITL を前提とした "Loop" の感覚自体が薄れた
- 実態は **coding agent の telemetry / profiler** であり、Grafana エコシステムとも整合する `agent-telemetry` が新名称として自然

## 問題

- 外部公開していない個人ツールという特性を活かせば、後方互換のための名残（旧 binary alias / 旧 env var support / 旧 DB 名検出）を持たずに破壊的変更を許容できる
- 一方、自分自身が既存 `~/.claude/hitl-metrics.db` を持っているため、自動マイグレーションは必要

## 対応方針

破壊的変更を一度で済ませる。後方互換は最低限（DB 自動リネームと旧 hook command の warning 検出）に留める。

| # | 決定 | 理由 |
|---|---|---|
| D1 | メトリクスプレフィックス: `hitl_*` → `agent_*` | 個人ツールであり破壊的変更を許容できる今のうちに実施。新規メトリクスの命名規約として `agent_*` を採用 |
| D2 | CLI バイナリ名: `agent-telemetry`（フル形） | 短縮形 `at` は POSIX 標準の `at(1)` と衝突。フル形なら検索性も保たれる |
| D3 | DB ファイル名: `~/.claude/agent-telemetry.db`、自動マイグレーションを提供 | 既存ユーザー環境（自分自身）の DB を破壊しないため `scripts/migrate-db-name.sh` を同梱 |
| D4 | Go モジュールパス: `github.com/ishii1648/agent-telemetry`（import 全置換） | GitHub の自動リダイレクトに依存すると import パスと実態が乖離し続ける |

## 解決方法

- Go モジュールパス・package 内 import を全て `agent-telemetry` に置換
- バイナリ名を `agent-telemetry` に変更（`cmd/agent-telemetry/`）
- `~/.claude/hitl-metrics.db` → `~/.claude/agent-telemetry.db` の自動リネームを `sync-db` / `backfill` 実行時に実施
- 環境変数: `HITL_METRICS_DB` → `AGENT_TELEMETRY_DB`、`HITL_METRICS_AGENT` → `AGENT_TELEMETRY_AGENT`
- Grafana datasource / dashboard の UID を `hitl-metrics` → `agent-telemetry` に変更
- 旧 hook command（`hitl-metrics hook ...`）は `internal/doctor/` で warning 検出、`uninstall-hooks` で除去可能。`internal/legacy/` に旧名検出ロジックを集約
- `internal/upgrade/` で旧バイナリの残存を warning 通知

### BREAKING CHANGE — 利用者向けインパクト

| 項目 | 変更内容 |
|---|---|
| バイナリ名 | `hitl-metrics` → `agent-telemetry`。旧バイナリは PATH から削除（`agent-telemetry upgrade` が残存を warning） |
| DB / state ファイル名 | `~/.claude/hitl-metrics.db` / `*-state.json` → `agent-telemetry sync-db` / `backfill` 実行時に自動リネーム |
| hook 登録 | `settings.json` / `hooks.json` の command 文字列を書き換え（`agent-telemetry doctor` が旧 command を warning） |
| Grafana | dashboard / datasource の `uid` を `hitl-metrics` → `agent-telemetry` に変更 |
| 環境変数 | `HITL_METRICS_DB` → `AGENT_TELEMETRY_DB`、`HITL_METRICS_AGENT` → `AGENT_TELEMETRY_AGENT` |

詳細手順は `docs/setup.md` の「hitl-metrics（旧名）からの移行」を参照。

## 採用しなかった代替

- **旧バイナリ alias を残す**: 個人ツールであり、破壊的変更を一度で済ませた方が長期保守コストが低い
- **旧 env var を内部で読み続ける**: 同上、shim を残すと混乱の温床になる
- **段階的リネーム（モジュールパスだけ先に）**: 中間状態が長く続くと、レビュー・テスト・docs の整合性維持コストが高い

---
decision_type: design
tags: [retro, backfill, hooks, batch]
closed_at: 2026-04-30
---

# backfill 方式の変遷 — 初期 → cron バッチ → Stop hook + cursor

Created: 2026-04-30
Retro-converted: 2026-05-10 (from docs/history.md §7; date approximate, around ADR-019 adoption)

## 概要

PR URL の backfill は 3 段階の進化を経た。初期（[ADR-005](../../docs/archive/adr/005-session-index-pr-url-backfill-on-stop.md)）の Stop hook fire-and-forget → 中期（[ADR-006](../../docs/archive/adr/006-session-index-pr-url-backfill-cron-batch.md)）の launchd / cron バッチ → 現在（[ADR-019](../../docs/archive/adr/019-backfill-stop-hook-migration.md)）の Stop hook + cursor + Go CLI 集約。

## 根拠

- 初期方式（Stop hook fire-and-forget で `gh pr view`）は過去分が拾えず、同一 session で複数回叩く重複 API 呼び出しの問題があった
- cron バッチ方式は Claude Code の外側で動く唯一の手作業（`launchctl load` 等）が UX を損ねていた
- Go CLI への集約と cursor 設計の確立で、Stop hook 内で増分処理が可能になった

## 問題

| 段階 | 方式 | 廃止理由 |
|---|---|---|
| 初期 (ADR-005) | Stop hook で fire-and-forget で `gh pr view` | 過去分が拾えない、重複 API 呼び出し |
| 中期 (ADR-006) | launchd / cron 定期バッチ | Claude Code 外の唯一の手作業で UX が悪化 |
| 現在 (ADR-019) | Stop hook + cursor + Go CLI 集約 | — |

## 対応方針

backfill ロジックを Go CLI (`internal/backfill/`) に集約し、cursor で増分処理する。Stop hook から呼び出すことで Claude Code 外の手作業を排除。

## 解決方法

- `internal/backfill/` に backfill ロジックを集約
- `internal/backfill/state.go` で cursor を JSON 永続化し、増分処理を実現
- `internal/hook/stop.go` から backfill を呼び出す経路を確立
- launchd / cron による外部バッチを廃止

## 採用しなかった代替

- **PostToolUse hook でその場 backfill**: 1 セッション中に複数回 `gh` を叩く重複が激しい
- **session 終了時に gh API を完全同期で叩く**: hook の hot path が長くなる。fire-and-forget 廃止の主因と矛盾

## 後続の発展

- [0022](0022-design-pr-resolve-early-binding.md) で Stop hook での PR pin が導入され、backfill の責務は「PR 未作成セッションのリトライ専用」に絞られた

## 参照

- [ADR-005](../../docs/archive/adr/005-session-index-pr-url-backfill-on-stop.md) → [ADR-006](../../docs/archive/adr/006-session-index-pr-url-backfill-cron-batch.md) → [ADR-019](../../docs/archive/adr/019-backfill-stop-hook-migration.md)
- [ADR-010](../../docs/archive/adr/010-session-index-backfill-parallel-execution.md)（並列実行化、cron バッチ時代）

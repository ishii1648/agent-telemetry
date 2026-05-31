---
decision_type: design
tags: [hooks, stop-hook, claude-code, non-blocking]
closed_at: 2026-06-01
---

# Stop hook を Claude Code の async 登録で非ブロッキング化する

Created: 2026-06-01

## 概要

Claude Code の `Stop` hook を `"async": true` で登録し、hook プロセス（`agent-telemetry hook stop`）の exit をユーザの応答サイクルが待たないようにする。設定は `internal/setup` の登録例・`docs/spec.md`・`docs/design.md`・`site/content/` に反映する。

## 根拠

`Stop` hook の重い処理（`gh` 呼び出し・PR pin・`sync-db`）は既に `backfill --detach`（setsid 配下の worker）へ退避済みで、同期パスはローカル書き込みのみだった（[issues/closed/0039-bug-stop-hook-backfill-rate-limit.md](0039-bug-stop-hook-backfill-rate-limit.md) / [issues/closed/0020-design-backfill-evolution-to-stop-hook.md](0020-design-backfill-evolution-to-stop-hook.md)）。しかしこれは「worker の重い処理」を非同期化しただけで、Claude Code が `Stop` hook プロセスそのものの終了を待つ挙動は消せていなかった。

## 問題

`Stop` は応答ターンごとに発火する。そのたびに Claude Code が hook プロセスの exit を同期的に待つため、Go バイナリのコールドスタート分（数十 ms）が毎ターンの次入力をブロックし、体感を損ねていた。detach worker では解消できない front-door の待ちが残存していた。

## 対応方針

Claude Code v2.1.0+ の Command hook フィールド `"async": true`（[公式 hooks reference](https://code.claude.com/docs/en/hooks)）で `Stop` hook を登録する。Claude Code は hook をバックグラウンド起動して exit を待たないため、応答サイクルの待ちが消える。detach worker とは相補的（worker は重い処理を、async は hook プロセス待ちを隠す）。

Completed: 2026-06-01

## 解決方法

- `internal/setup/setup.go` の Claude Stop 登録例に `"async": true` を付与し、補足文を追加。`internal/setup/setup_test.go` で登録例に `"async": true` が含まれることを必須化（回帰防止）
- `docs/spec.md` / `docs/design.md` / `site/content/setup/local/index.md` / `site/content/explain/hooks/index.md` を同期更新。explain/hooks に残っていた detach 前提の「ブロッキング」表記も worker 退避＋async に修正
- コードロジック（`internal/hook/RunStop`）は無変更。`async` は登録側の話

## 採用しなかった代替

- **`asyncRewake`**: exit 2 で Claude を再起床させる＝非ブロッキングの逆。telemetry は best-effort なので silent でよく不採用
- **SessionEnd の async 化**: 発火が 1 回で体感が小さく、終了フェーズで background hook が完了前に kill される挙動が公式ドキュメント未定義。`ended_at` は JSONL（source of truth）に書かれ次回 sync で追従するためデータ損失はないが、即時性のメリットが薄いため今回は同期維持
- **Codex への適用**: `async` は Claude Code 固有フィールド。Codex の `Stop` は de-facto SessionEnd で `ended_at` を同期更新する別系統のため適用しない

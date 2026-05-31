---
decision_type: process
tags: [retro, hooks, setup, doctor, dotfiles-integration]
closed_at: 2026-05-10
---

# `install` で hook を自動登録しない / `doctor` で hook を自動修復しない

Created: 2026-05-10
Retro-converted: 2026-05-10 (from docs/history.md「残っている有効な決定の要点」)

## 概要

`agent-telemetry install`（旧）/ `setup`（新）は hook を `~/.claude/settings.json` に自動書き込みしない。`agent-telemetry doctor` も検出した不整合を自動修復しない。どちらも警告と推奨コマンドの提示にとどめる。

## 根拠

- 利用者（自分）は dotfiles で `~/.claude/settings.json` を一元管理しており、CLI が settings.json を勝手に書き換えると dotfiles の管理状態と乖離する
- 自動書き込みは「ツールの便利さ」と「dotfiles の宣言的管理」のトレードオフだが、本ツールの想定ユーザは後者を優先する（個人ツール）
- 自動修復が破壊的に働くケース（hook command を意図的に変更している）を考慮すると、警告に留める方が安全

## 問題

- 自動書き込みなしだと初見の利用者が hook 登録に手間取る
- doctor の検出だけでは、修復方法を知らない利用者が放置するリスク

## 対応方針

- `install` / `setup` は hook 登録を行わず、必要な command 文字列を出力して利用者が手動で settings.json に書き込む方針
- `doctor` は不整合を検出し、修復に必要なコマンドを提示するが自動実行はしない
- 旧 hook command（`hitl-metrics hook ...`）が登録されている場合のみ `uninstall-hooks` で除去可能（明示的に呼ばれた場合のみ書き込む）

## 解決方法

- `internal/install/` / `internal/setup/` は settings.json への書き込み機能を持たない（diff 出力にとどめる）
- `internal/doctor/` は警告レベルの分類（error / warning）と修復推奨コマンドの提示のみ
- 例外として `agent-telemetry uninstall-hooks` は明示的に呼ばれた場合のみ settings.json から旧エントリを削除する破壊操作を行う

## 採用しなかった代替

- **`install` で自動書き込み + バックアップ**: dotfiles 管理の宣言的フローと相性が悪い。バックアップファイルが散らかる
- **`doctor` で自動修復**: 利用者の意図的な変更を上書きするリスク
- **両方を opt-in フラグ（`--write` / `--fix`）で提供**: 個人ツールでフラグ追加のコストに見合わない。手動で settings.json を編集するワークフローで十分

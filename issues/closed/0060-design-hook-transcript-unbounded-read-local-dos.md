---
decision_type: design
tags: [security, hook, transcript, dos]
closed_at: 2026-06-02
---

# hook stdin / transcript parse の非有界読み込みによる local DoS

Created: 2026-06-02

## 概要

セキュリティレビュー §3「Hook・session-index・transcript」で Medium と較正された残課題。`internal/hook` の hook stdin を `io.ReadAll` で非有界に読み込み、`internal/transcript` は transcript の解凍・大ファイル parse を行う。悪意あるエージェントセッションが巨大 hook JSON や巨大 transcript を与えると持続的 CPU/memory 使用を招きうる。

## 根拠

JSONL/transcript には scanner の line limit があり不正行も保存する設計だが、hook stdin の `io.ReadAll` 自体には上限が無く、transcript の解凍後サイズも青天井になりうる。攻撃にはユーザのエージェント環境やファイルの制御が必要なため local/medium だが、ローカルメトリクスのデータ汚染や OOM の余地が残る。

## 問題

- `internal/hook/input.go` 系の hook stdin が `io.ReadAll` で非有界
- `internal/transcript` の解凍・大ファイル parse に明示的なサイズ上限が無い
- ローカルメトリクスのデータ汚染（poisoning）も同経路で起こりうる

## 対応方針

- hook stdin に `io.LimitReader` 相当の上限を入れる方針を design に記録する（server の 50MB cap と整合させる）
- transcript の解凍後サイズ上限を設け、超過時は invalid 行扱い/スキップで graceful degrade する
- 単一ユーザ前提の受容範囲を明記し、過剰防御にならない閾値を決める

Completed: 2026-06-02

## 解決方法

2 つの非有界読み込み経路に解凍後/読み込みサイズの cap を入れ、超過時は無効入力扱い（hook）/ graceful degrade（transcript）で抑える。閾値は単一ユーザの現実値を十分上回る値にし、過剰防御を避けた。方針は `docs/design.md`（`### .jsonl.zst 透過デコード > 解凍後サイズの上限` と `### hook stdin の読み込み上限`）に記録。

- **hook stdin**: `hook.ReadInput` を `io.LimitReader` で `MaxInputBytes`（50 MB、`serverpipe.MaxPayloadBytes` と整合）に cap。超過はエラーで弾く。空 stdin は従来どおり非エラー（legacy Claude Stop hook 互換）。
- **transcript**: `openTranscript` の返す reader を **解凍後** `MaxDecodedBytes`（256 MB）に cap（`.jsonl` 直読み・`.jsonl.zst` 解凍後の両方）。zstd zip-bomb と巨大 plain `.jsonl` の両方を遮断。超過時は scanner がそこで止まり partial stats を返す（最後の半端行は JSON parse 失敗で skip）。
- いずれも閾値は package var にしてテストから縮小可能にし、上限超過時の挙動を `internal/hook` / `internal/transcript` のテストで固定した。

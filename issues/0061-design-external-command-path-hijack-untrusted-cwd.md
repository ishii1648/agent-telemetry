---
decision_type: design
tags: [security, hook, backfill, exec]
---

# 外部コマンド（agent-telemetry/git/gh）の PATH hijack・untrusted cwd 実行

Created: 2026-06-02

## 概要

セキュリティレビュー §3「Hook…」「Backfill / GitHub 連携」で Medium と較正された残課題。コードは `git`/`gh` をシェルを介さず argv で起動し攻撃者データを展開しないが、`internal/hook/sessionend.go` が `agent-telemetry` を、`internal/backfill` が `gh` を、いずれも PATH 解決かつ攻撃者制御下になりうる cwd で起動する。

## 根拠

シェル injection は回避済みだが、PATH に悪意あるバイナリが置かれた環境や、攻撃者が用意したリポジトリ cwd で `git`/`gh`/`agent-telemetry` を実行すると、意図しないバイナリ実行や PR 誤帰属の余地がある。攻撃にはローカル環境制御が必要なため medium。

## 問題

- `internal/hook/sessionend.go` の `agent-telemetry` 自己呼び出しが PATH 解決依存
- `internal/backfill` が untrusted cwd で `gh` を起動する境界
- `git` も同様に cwd 依存で起動される

## 対応方針

- 自己呼び出し（`agent-telemetry`）は可能なら `os.Executable()` の絶対パス解決に寄せる方針を design に記録する
- backfill の `gh`/`git` 起動時の cwd の扱い（信頼境界）を明文化し、必要なら明示的な作業ディレクトリ指定に統一する
- 既存の no-shell / 8s timeout / global lock といった緩和は維持する

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

## 解決方法

design.md に「外部コマンドの PATH / cwd 信頼境界（[0061]）」セクションを追加し、方針を記録したうえでコードを以下に統一した:

- **自己呼び出しの絶対パス解決**: `internal/hook/sessionend.go` の `sync-db` 再実行を `exec.Command("agent-telemetry", ...)`（PATH 解決）から `os.Executable()` 解決に変更。Stop hook の worker spawn（`internal/hook/stop.go` の `os.Executable()` 前例）と挙動を揃えた。解決不能時のみ bare PATH lookup にフォールバック。
- **cwd 非依存コマンドは信頼境界の dir 固定**: `gh pr view <full-url>`（`internal/backfill`）を、攻撃者が用意しうるセッション cwd ではなく常に信頼できる作業ディレクトリ（HOME、不可なら TempDir）で実行するよう `trustedWorkdir()` ヘルパに統一。full URL がリポジトリを解決するため cwd の git remote に非依存で、リポジトリ cwd 固有の git/gh 設定・hook を拾わせない。
- **cwd 依存コマンドは信頼境界を明文化**: `gh pr list --head` は cwd の git remote からリポジトリを解決するためセッション cwd で実行せざるを得ない。cwd は自分の `session-index.jsonl` 由来で telemetry の信頼ドメイン内であることをコメントで明記し、既存の no-shell argv / 8s timeout / global single-flight lock を緩和として維持。

`go test ./...` green。

Completed: 2026-06-02

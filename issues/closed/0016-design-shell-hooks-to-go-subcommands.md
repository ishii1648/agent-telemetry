---
decision_type: design
tags: [retro, hooks, packaging, distribution]
closed_at: 2026-04-15
---

# Shell hooks → Go サブコマンド統一

Created: 2026-04-15
Retro-converted: 2026-05-10 (from docs/history.md §3; date approximate, around ADR-021 adoption)

## 概要

`session-index.sh` / `permission-log.sh` / `pretooluse-track.sh` / `stop.sh` / `todo-cleanup-check.sh` の 5 本の Shell スクリプトを、`agent-telemetry hook <event>` という単一 Go サブコマンドに統合した。

## 根拠

- 配布が「Go バイナリ + Shell スクリプト embed + 展開」の二重構造で、setup / upgrade / uninstall のすべてに余計な分岐が発生していた
- tool annotation など複数 hook で共通するロジックが、Shell スクリプト間で重複していた（DRY 違反）
- Shell の依存（`jq` / `gh` / `bash 4+`）がプラットフォームによって安定せず、特に macOS の bash 3.2 で連想配列が使えない問題に毎回遭遇していた

## 問題

- Shell の正規表現・JSON 加工・stdin 読み取りは Go と比べてエラーハンドリングが脆く、テスタビリティも低い
- `hooks/*.sh` を embed して setup 時に展開する経路が、同期ズレ（バイナリ更新後にスクリプトだけ古い）を起こす可能性があった

## 対応方針

各 hook を `agent-telemetry hook <event>` の Go サブコマンドとして再実装。Claude Code / Codex CLI からの呼び出しは `agent-telemetry hook session-start` 等の形に統一する。

## 解決方法

- `internal/hook/` 配下に hook 種別ごとの実装を集約（`sessionstart.go` / `stop.go` / `posttooluse.go` / `permissionrequest.go` / `pretooluse.go` / `sessionend.go`）
- tool annotation の共通ロジックは `internal/hook/annotate.go` に切り出し
- `cmd/agent-telemetry/main.go` で `hook <event>` ディスパッチ
- 旧 Shell スクリプトは削除。setup 時の embed / 展開も廃止
- `agent-telemetry doctor` / `uninstall-hooks` は旧 Shell command 文字列も検出して warning する（互換性のため）

## 採用しなかった代替

- **Shell のまま依存を整える**: bash 4+ を要求すると macOS 標準 shell から外れる。`brew install bash` を強制するのは UX 後退
- **Python に統一**: distribution が Python ランタイムに依存し、goreleaser での single-binary 配布と整合しない

## 参照

- [ADR-021](../../docs/archive/adr/021-migrate-shell-hooks-to-go-subcommands.md)

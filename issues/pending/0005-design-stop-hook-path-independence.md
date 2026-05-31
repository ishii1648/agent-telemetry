---
decision_type: design
tags: [hooks, path-resolution, packaging]
---

# Stop hook の agent-telemetry PATH 依存をなくす

Created: 2026-05-08
Model: Opus 4.7

## 概要

Stop hook (`agent-telemetry hook stop`) は settings.json / config.toml / hooks.json に書かれた `agent-telemetry` を PATH から解決して実行している。バイナリが PATH に無い環境では hook が動かず、しかもサイレント失敗する場合がある。PATH 依存をなくす方針を決める。

## 根拠

agent-telemetry は `go install` または GoReleaser の tarball で `$GOPATH/bin` 等に置かれることを想定しているが、PATH が通っていない非対話環境（ssh の非対話 shell、cron、IDE が spawn したプロセス、launchd 経由の起動等）から起動された Claude / Codex セッションでは hook が動かない。失敗してもログが出ない場合があり、デバッグも難しい。

## 問題

- どの解決策（候補 A/B/C）にもトレードオフがある
- 失敗時ログの設計（PATH 不在 vs 内部エラーの切り分け）も方針に含める必要がある

## 対応方針

候補:

- 候補 A: `backfill` / `sync-db` を `internal/` 関数として直接呼ぶ（同一プロセス、PATH 非依存）
- 候補 B: `setup` 時に hook コマンドの絶対パスを案内する（`settings.json` / `config.toml` 側で絶対パスを書く）
- 候補 C: hook 内で binary を `os.Executable()` で解決し PATH にフォールバックしない

各候補について、失敗時ログ（PATH 不在の検出方法、stderr / debug log への流し方）を含めて評価する。

## Pending 2026-05-08

候補ごとの影響範囲（hook プロセスのライフサイクル、Codex hooks.json 形式の制約、テスト容易性）が整理できていない。方針確定後に受け入れ条件を整えて open に昇格させる。

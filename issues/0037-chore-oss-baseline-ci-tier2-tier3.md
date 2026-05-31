---
decision_type: process
tags: [ci, oss-hygiene, supply-chain]
---

# OSS baseline CI の Tier 2 / Tier 3 を段階導入する

Created: 2026-05-11

## 概要

`feat/go-vet-oss-ci` で投入した Tier 1 OSS CI（`go vet` / `go build` / `gofmt` / `go mod tidy` drift / `govulncheck` / `actionlint` / `dependabot`）と、後続の SHA-pin 徹底（PR #57）でカバーしていない、OSS としての CI hygiene を引き続き整備する。

調査メモ: `.outputs/claude/oss-baseline-ci.md` (Tier 2 / Tier 3 セクション)。

## 根拠

Tier 1 だけでは「OSS Go プロジェクトとしての最低限」は満たすが、配布物の安全性 / supply chain / メンテナビリティの観点では不足がある:

- バイナリを GitHub Releases で配布している（goreleaser）→ 配布物に脆弱な依存や非互換ライセンスが混入していないか検査が必要
- `Dockerfile.server` の image を ghcr.io に publish → SAST と OSS hygiene 採点で外部からの可視性を確保したい
- hook は darwin で動く前提 / Go バージョンは 1 つ固定 → 一部の壊れ方を CI で拾えていない

これらを 1 PR で全部入れるのは scope 過大なので、**項目ごとに小さい PR で段階導入する**。本 issue はその受け入れ条件の集約 hub として機能させる。

## 対応方針

各項目を独立した PR で入れる。1 PR = 1 項目 = 1 受け入れ条件。

### Tier 2 — まじめな OSS なら入れる

- [ ] **CodeQL** (`github/codeql-action`) を導入。public repo は無料枠で動く。Go の SAST を PR ごとに走らせる
- [ ] **OpenSSF Scorecard** (`ossf/scorecard-action`) を導入。週次 cron + main push で repo の hygiene スコアを上げる。README に badge を貼る
- [ ] **`go-licenses` チェック** を Go workflow に追加。配布バイナリに紛れ込む依存ライセンスの汚染（GPL 等）を CI で検出する
- [ ] **goreleaser dry-run on PR**: `.goreleaser.yaml` 変更時に `goreleaser check` + `goreleaser release --snapshot` を走らせる。release 当日に config の syntax error で死ぬのを防ぐ

### Tier 3 — 余裕があれば

- [ ] **OS matrix** (`ubuntu` + `macos`): hook は darwin で動く前提なので最低 1 ジョブを macOS 上で走らせる。test と vet を対象
- [ ] **Go version matrix**: `go.mod` の directive + `stable` の 2 軸。現状 `1.26.3` 固定で、Go の patch release で挙動差を踏みかねない
- [ ] **Coverage upload** (Codecov 等): バッジ目当て。priority 低
- [ ] **PR title / commit message lint**: Contextual Commits 規約は AGENTS.md にあるが enforcement は無し。bot で format check
- [ ] **`markdownlint` / `shellcheck`**: `docs/`, `site/content/` の Markdown と `scripts/intent-lookup` 等の shell script を lint
- [ ] **gitleaks 等 secret scan**: GitHub の push protection で代替できているが明示的にも

## scope-out

- **`zizmor` / `pinact` による Action SHA-pin の audit 自動化**: Tier 2 #10 として挙がっていたが PR #57 で hand-pin を済ませたので、自動 audit 導入は本 issue の対象外（必要になったら別 issue で）。

## close 条件

すべての受け入れ条件にチェックが入った時点で close する。Tier 3 の一部を「不要」と判断した場合は、**チェックではなく取り消し線で「不採用」とその理由**を残してから close する。

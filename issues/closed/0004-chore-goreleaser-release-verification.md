---
decision_type: process
tags: [release, ci, goreleaser]
---

# GoReleaser tag push 動作確認 + リリース

Created: 2026-05-08
Completed: 2026-05-10
Model: Opus 4.7

## 概要

リポジトリリネーム（hitl-metrics → agent-telemetry）の最終フェーズとして、tag push 経由で GoReleaser が `agent-telemetry_<os>_<arch>.tar.gz` 形式のバイナリを生成することを確認する。

## 根拠

リネームのフェーズ 3〜5 は完了済みだが、最終フェーズ（タグ push 動作確認）が未実施。GoReleaser 設定が agent-telemetry 名で正しく動作するかは tag push しないと分からない。リネーム決定の背景・決定事項（D1〜D4）と BREAKING CHANGE 一覧は [0021-spec-rename-hitl-metrics-to-agent-telemetry.md](0021-spec-rename-hitl-metrics-to-agent-telemetry.md) を参照。

## 対応方針

- 任意の patch tag を push（例: `v0.x.y`）
- GoReleaser の GitHub Actions ワークフローが完走することを確認
- 生成された artifact 名が `agent-telemetry_<os>_<arch>.tar.gz` であることを確認
- リリースノートが正しく生成されることを確認

## 受け入れ条件

- [x] tag push で GoReleaser が完走
- [x] artifact 名が `agent-telemetry` プレフィックスで生成される
- [x] GitHub Releases ページに正しいリリースが表示される

## 解決方法

`v0.0.5` を `main` (ce7fde5) に対して push し、`.github/workflows/release.yml` 経由で GoReleaser を起動した。

- workflow run: 25623849035 / 1m26s で success
- 生成 artifact (`https://github.com/ishii1648/agent-telemetry/releases/tag/v0.0.5`):
  - `agent-telemetry_darwin_amd64.tar.gz`
  - `agent-telemetry_darwin_arm64.tar.gz`
  - `agent-telemetry_linux_amd64.tar.gz`
  - `agent-telemetry_linux_arm64.tar.gz`
  - `checksums.txt`
- `.goreleaser.yaml` は `binary: agent-telemetry` + `name_template: "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"` で、`ProjectName` がリポジトリ名（agent-telemetry）にデフォルト解決されることを確認。設定変更は不要だった。

これでリネーム（hitl-metrics → agent-telemetry）の全フェーズが完了。次回以降のタグ push も同じ artifact 名で出力される。

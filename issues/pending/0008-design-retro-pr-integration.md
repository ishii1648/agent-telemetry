---
decision_type: design
tags: [retro-pr, integration, automation]
---

# retro-pr との連携

Created: 2026-05-08
Model: Opus 4.7

## 概要

PR の下位・上位 10% ずつを自動で retro-pr に流して、結果を PR と関連付けて表示する。

## 根拠

agent-telemetry は外れ値 PR を可視化するところまでが責務だが、その先の「外れ値 PR の transcript を分析して原因を特定する」作業は手動で `examples/skills/analyze-pr` を呼ぶ必要がある。retro-pr が定型的な振り返りを生成するなら、上位/下位 10% に自動適用するのは自然な拡張。

## 問題

- 連携方式（retro-pr CLI を直接呼ぶ / GitHub Actions / Web Routine）未確定
- 結果の表示先（PR コメント / Grafana annotation / ローカル md）未確定
- 自動化対象（merge 直後 / 週次バッチ / 手動 trigger）未確定

## 対応方針

- retro-pr の I/O 仕様を確認
- 連携方式を 1 つ選定
- 結果保存先を決定
- 自動化トリガーを決定

## Pending 2026-05-08

retro-pr 自体が外部ツールであり、その仕様変化や運用パターンに依存する。まず agent-telemetry の `examples/` 配下にプロトタイプを作って手応えを確認してから本実装方針を決めるのが順当。受け入れ条件が未確定。

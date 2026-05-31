---
decision_type: design
tags: [hooks, observability, context, posttooluse]
---

# Bash コマンドのコンテキスト消費監視

Created: 2026-05-08
Model: Opus 4.7

## 概要

`PostToolUse` hook で Bash コマンドの stdout サイズを記録し、redirect-to-tools をすり抜けた正当な Bash コマンドのうち、出力が大きいものを定期集計で可視化する。「常連犯」コマンドを特定し、対策要否を判断する。

## 根拠

agent が大きな出力を返す Bash コマンド（`cat large.log`、`git log` のフル出力等）を実行すると context が肥大化し、token 消費が膨らむ。redirect-to-tools 機構で Read / Grep に誘導しているが、すり抜けるコマンドを「常連犯」として可視化する仕組みが無い。

## 問題

- 記録先（既存 `transcript_stats` か新テーブルか）未確定
- 閾値（何バイト以上を監視対象にするか）未確定
- 集計方法（コマンド単位 / コマンド + 引数 / hash 化）未確定

## 対応方針

- `PostToolUse` hook で `tool_input.command` と `tool_response` の bytes を記録
- 一定閾値以上のコマンドのみ記録（noise 削減）
- Grafana で「PR 内 stdout bytes 上位コマンド」を表示

## Pending 2026-05-08

受け入れ条件（記録先・閾値・集計方法）が未確定。まずは小さな実験（手動でログを取って閾値感を掴む）が必要で、いきなり実装に進める段階にない。

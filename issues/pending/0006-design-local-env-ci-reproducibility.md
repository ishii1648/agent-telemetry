---
decision_type: process
tags: [testing, ci, sqlite, reproducibility]
---

# ローカル検証環境と CI の再現性

Created: 2026-05-08
Model: Opus 4.7

## 概要

SQLite テストの不安定要因と、`go test -race` がローカルで実行不能なケースを整理し、安定化条件と代替手順の方針を決める。

## 根拠

macOS arm64 で `modernc.org/sqlite` を使う際、テストが flaky になる事象が観測されている。`go test -race` も環境によってはローカル実行不能で、CI に委ねざるを得ない。再現性の仕様が docs に書かれていないと、新しい環境でテスト実行に困る。

## 問題

- SQLite テストの不安定要因がまだ特定できていない（`modernc.org/sqlite` の race / file lock / temp dir 競合等）
- `go test -race` がローカルで動かないケース（OS / arch / Go version の組み合わせ）が網羅できていない
- 制約整理の記録先が `docs/setup.md` か別 docs か未定

## 対応方針

- 不安定要因を 1〜2 ケースに絞り込んで再現条件を特定
- ローカル実行可能/不可のマトリクスを作成
- 代替手順（CI に委ねる / Docker で回す等）の方針を決める
- 制約整理の記録先を決定（`docs/setup.md` 末尾 or `docs/development.md` 新設）

## Pending 2026-05-08

完了条件（どこまで網羅すれば十分か）が決まっていない。不安定要因の調査自体に時間を要するため、明確な受け入れ条件を立てるには予備調査が必要。

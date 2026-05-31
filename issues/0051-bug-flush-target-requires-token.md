---
decision_type: implementation
affected_paths:
  - internal/serverclient/config.go
  - deploy/oss-observability/config.toml.example
tags: [flush, export-target, oss, credential]
---

# token 無しの export target が flush から黙殺される

Created: 2026-05-31

## 概要

`agent-telemetry flush` が、token を設定していない `[[export]]` target を送信対象から除外する。OSS observability レシピ（issue 0050）は「ローカル Collector は認証不要・token 不要」を前提にしているため、`config.toml.example` どおりに設定しても **logs / metrics が一切送信されない**（warning も出ず exit 0）。

## 根拠

`internal/serverclient/config.go` の `Configured()`:

```go
func (t ExportTarget) Configured() bool {
	return t.Endpoint != "" && t.Token != ""
}
```

endpoint と token の **両方**が非空でないと「未設定」扱いになり、flush ループ（`flush.go:201` の `if !t.Configured()`）でスキップされる。一方 0050 の `deploy/oss-observability/config.toml.example` は token 行を持たず、コメントで「token は不要（ローカル Collector は認証なし）」と明記している。この two-source が矛盾している。

`docs/spec.md` の export target 表でも `token` は「（必須）」とされており、spec と OSS レシピの意図がずれている。

## 問題

- 0050 のレシピどおりに `endpoint = "http://localhost:4318"` / `signals = ["logs","metrics"]`（token なし）を設定しても flush が何も送らない。E2E 検証時は `token = "local"` のダミーを足して回避する必要があった。
- 「target が 1 つも無い / 全 target の endpoint・token が空」のときだけ warning を出す設計のため、token だけ空の target は **黙ってスキップ**され、ユーザは送信されていないことに気づきにくい。

## 対応方針

- credential 不要の OSS Collector / ローカル endpoint 向けに、token 空でも `Configured() == true` を許容する経路を用意する（例: `auth_header` / `token` がどちらも空なら認証なしで送る、あるいは `signals` が設定され endpoint があれば configured とみなす）。
- spec（`docs/spec.md` の export target 表で `token` を「必須」としている記述）と OSS レシピ（`config.toml.example`）の整合を取る。token は「認証が必要な backend でのみ必須」に改める。
- token 空 target を黙ってスキップする場合でも、誤設定と区別できるよう stderr に明示する。

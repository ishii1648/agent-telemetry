---
decision_type: implementation
affected_paths:
  - internal/upgrade/
tags: [upgrade, github, rate-limit]
---

# upgrade コマンドの GitHub API 認証

Created: 2026-05-28

## 概要

`agent-telemetry upgrade` が GitHub Releases の latest API を未認証で呼び出すため、未認証 API rate limit に達した環境では 403 で upgrade が失敗する。

## 根拠

upgrade は配布済みバイナリの自己更新経路なので、ローカル環境で頻繁に rate limit へ依存すると復旧手段としての信頼性が落ちる。ユーザ環境には `GITHUB_TOKEN` または GitHub CLI の認証情報が存在することが多く、これを使えば GitHub の認証済み rate limit を利用できる。

## 問題

- GitHub Releases latest API への HTTP request に `Authorization` header が付かない
- `GITHUB_TOKEN` が設定されていても upgrade コマンドから利用されない
- `gh auth token` で取得できる既存ログイン情報も利用されない

## 対応方針

`GITHUB_TOKEN` を最優先し、未設定の場合は `gh auth token` を参照する。どちらかで token が取得できた場合だけ GitHub API request に `Authorization: Bearer <token>` を付与する。token が取得できない環境では従来通り未認証で試行し、GitHub CLI が未インストールでも upgrade 自体を即失敗させない。

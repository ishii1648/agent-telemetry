---
decision_type: implementation
affected_paths:
  - deploy/otel-collector/collector-config.yaml
  - deploy/otel-collector/docker-compose.yaml
tags: [otel, collector, recipe, ottl, datadog]
closed_at: 2026-05-30
---

# collector レシピの OTTL 文が context prefix 無しで deprecation 警告 / `:latest` pin

Created: 2026-05-30

## 概要

0040 ファミリーの e2e 検証（項目2）で collector レシピを
`otel/opentelemetry-collector-contrib:latest`（実測 v0.153.0）で実走させたところ、
**動作はする**（raw events fanout・attribute 整形すべて PASS）が、起動時に OTTL の
deprecation 情報ログが出る:

```
ottl ... one or more paths were modified to include their context prefix,
please rewrite them accordingly
  original: set(attributes["env"], resource.attributes["deployment.environment"]) ...
  modified: set(log.attributes["env"], resource.attributes["deployment.environment"]) ...
```

`deploy/otel-collector/collector-config.yaml` の `transform/agent_telemetry` は
`attributes[...]` という旧記法で書かれており、新しい contrib は `log.attributes[...]`
への自動 rewrite で互換を保っているが、**将来の contrib で旧記法が外れると壊れる**。

加えて `docker-compose.yaml` / README が collector image を `:latest` で pin して
おり、再現性（検証・運用で版が動く）の観点で弱い。

## 影響

- 現状は動作する（low severity）。ただし contrib のメジャー更新で
  attribute 整形 pipeline が起動失敗し得る（recipe の将来互換）。
- `:latest` pin は検証・運用の再現性リスク。

## 対応方針（案）

- `transform/agent_telemetry` の `log_statements` を `log.attributes[...]` /
  `resource.attributes[...]` の context-prefixed 記法に書き換える。
- `docker-compose.yaml` / README の image を特定バージョン（例 `:0.153.0`）に pin。
- 関連: [0044] attribute 意味分類（collector processor サンプル同梱）。

Completed: 2026-05-30

## 解決方法

`transform/agent_telemetry` の `log_statements`（`context: log`）内の
`attributes[...]` を全 statement で context-prefixed 記法 `log.attributes[...]`
へ書き換えた（`resource.attributes[...]` は既に prefixed なので据え置き）。
`set` / `delete_key` / `IsMatch` の引数も漏れなく変換。意味は不変。

`docker-compose.yaml` / README の collector image を `:latest` →
`otel/opentelemetry-collector-contrib:0.153.0` に pin。

### 検証

`0.153.0` イメージで実走確認:

- `validate --config=...` が exit 0。
- collector を起動して `Everything is ready` まで到達し、起動ログに
  `context prefix` / `please rewrite` / `modified to include` の OTTL
  deprecation 警告が **出ないこと** を確認（旧記法では出ていた）。

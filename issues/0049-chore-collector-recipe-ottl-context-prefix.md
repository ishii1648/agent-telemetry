---
decision_type: implementation
affected_paths:
  - deploy/otel-collector/collector-config.yaml
  - deploy/otel-collector/docker-compose.yaml
tags: [otel, collector, recipe, ottl, datadog]
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

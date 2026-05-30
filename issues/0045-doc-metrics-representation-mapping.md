---
decision_type: spec
affected_paths:
  - docs/metrics.md
depends_on: [0040, 0043, 0044]
tags: [otel, export, metrics, docs]
---

# `docs/metrics.md` に backend 上の representation 対応を追記

Created: 2026-05-30

## 概要

[0040] の child issue (4)。`docs/metrics.md` の各メトリクスについて、外部 backend 上での representation を「`pr_metrics` 系は client 集約の gauge をそのまま使う / event-level 系は raw events から backend formula で出す」のどちらかで明示する。

## 根拠

[0040] で 2 representation（raw events Logs / pre-aggregated gauge）に分かれた帰結。metrics カタログを読む利用者が「どの metric を backend のどの object（log facet/measure か gauge series か）から作るか」を即座に判断できる状態を目指す。

## 対応方針

[0043] / [0044] の実装が固まったあとに着手する（任意 issue 扱い、優先度低）:

1. `docs/metrics.md` の各 metric 行に representation 列を追加（gauge / event-level）
2. event-level 系は backend formula の例（Datadog count / sum query 例）を参考程度に添える

## 触らない

- spec 本体（attribute 分類）は [0044]
- 責務分担の design.md 追記は [0046]

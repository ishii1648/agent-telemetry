---
decision_type: doc
depends_on: [0040, 0042, 0043, 0046, 0047, 0057, 0058, 0059]
tags: [datadog, otel, export, docs, setup-guide]
---

# Datadog metrics export を運用可能にする（外向け docs 整備と前提鮮度）

Created: 2026-06-23

## 概要

[0040] ファミリー（[0042] target 配列 / [0043] `pr_metrics` gauge / [0044] attribute 分類 /
[0046] 責務分担 / [0047] e2e 検証）でクライアント実装と attribute 整形レシピは完成しており、[0057] / [0058] /
[0059] で server hardening も整った。一方で、ユーザが Datadog を実際に立ち上げて運用に乗せるための
**外向け Hugo docs（`site/content/setup/datadog/`）が存在しない**。本 issue は実装変更を伴わず、
docs 整備と関連 README の鮮度更新だけで Datadog 経路を運用可能にする。

`setup/datadog/` は `setup/local`（SQLite 単独）/ `setup/server`（中央 server）と並ぶ第 3 の経路として
独立配置する（将来 New Relic などを増やしても並列に並べられる）。

## 根拠

- 実装は揃っているが、**ユーザがどこから入って何を設定すれば良いか** が site docs から辿れない。
  `site/content/setup/` 直下は `_index.md` / `local` / `server` のみで、Datadog の章が無い。
- `deploy/datadog/README.md` の direct 経路節に「現行の手組み JSON poster + 固定 Bearer では
  Datadog OTLP intake を満たせない。client 側拡張は [0042] の範囲」と書かれているが、
  [0042] / [0043] / [0047] で既に解消済み（`encoding = "protobuf"` + `auth_header = "dd-api-key"`
  が `internal/serverclient` に実装され、[0047] で mock receiver / httpbin による検証も PASS）。
  この古い前提が残ると direct を採用するユーザが「まだ動かない」と誤読する。
- Datadog 公式 docs の追加調査で発見した制約も docs に反映する必要がある:
  - direct OTLP **logs intake は protobuf 必須**（JSON 不可）— 我々の spec はすでに反映済みだが setup guide でも明示する
  - direct OTLP **metrics intake は delta 必須だが gauge は対象外**（我々は gauge のみ送るので無影響、
    spec 既述。setup guide でも redundant に書いて読者の不安を除く）
  - direct intake の **payload 上限は uncompressed 500KB / compressed 5MB**。
    我々の `MaxBatchBytes = 50MB` と乖離があり、大規模 PR 数で 413 が出る可能性があるため
    setup guide に運用注意として書く（コード変更は本 issue のスコープ外）
  - optional `dd-otel-metric-config: {"resource_attributes_as_tags": true}` header で
    `service.name` / `service.version` resource attribute が tag になる — 推奨設定として案内

## 問題

外向け docs の不足と前提の経年劣化、それぞれを 1 箇所ずつ確認する。

1. `site/content/setup/datadog/index.md` が無い:
   - direct vs collector の選び方
   - direct セットアップ（`config.toml` 例 / site → endpoint 早見表 / protobuf + dd-api-key 設定 / payload 上限 caveat）
   - collector セットアップ（`deploy/otel-collector/` への誘導 / client 側設定 / `DD_API_KEY` 環境変数）
   - Datadog 側 facet/measure と Logs Pipeline への誘導（`deploy/datadog/facets-measures.md` / `logs-pipeline.md`）
   - smoke test 手順（`flush --dry-run` → 実送信 → Datadog Logs / Metrics Explorer 確認）
   - cardinality 制御の注意（`pr_url` 単調増加 / 群 3 drop の意味）
   - cron / launchd 定期 flush（`setup/server` の 7 節へリンク）
2. `deploy/datadog/README.md` の direct 経路節が古い:
   - 「OTLP SDK / protobuf exporter への切替が必要。client 側拡張は [0042] の範囲」を
     「現在は `agent-telemetry flush` 本体が `encoding = "protobuf"` + `auth_header = "dd-api-key"`
     を標準でサポートしており、direct も追加実装なしで動く。設定例は
     [setup/datadog](https://ishii1648.github.io/agent-telemetry/setup/datadog/) を参照」に更新する
3. `site/content/setup/server/index.md` の OSS 検証用節から `setup/datadog/` への動線が無い:
   - 既存節は触らず、関連リンク行だけ加えるかどうかは PR 中で判断（最小変更を優先）

スコープ外（本 issue では行わない）:

- agent-telemetry-server から Datadog への fanout（[0046] 責務分担で否定済み、collector の責務）
- OTel Collector の per-client identity enforcement（[0058] で別 issue 切り出し済み）
- Datadog 以外の backend のリファレンス実装（[0040] 同様、本 issue でも保留）
- direct intake の 500KB/5MB 上限に合わせた `MaxBatchBytes` の per-target 上書き
  （現状は 1 PR ≒ 数百バイトなので個人 / 小チーム規模では収まる。必要になったら別 issue）

## 対応方針

- `site/content/setup/datadog/index.md` を新規作成（`weight: 30`、`local: 10` / `server: 20` の次に並ぶ）
- `deploy/datadog/README.md` の direct 節の前提を更新
- 必要なら `site/content/setup/server/index.md` の OSS 節から `setup/datadog/` への 1 行リンク追加
  （PR 中で過剰になりそうなら省略）
- 検証は手元で `hugo server` を起動して `setup/datadog/` ページが描画されることを確認するに留め、
  scripted な smoke test は git-track しない（[0047] の検証成果は `.outputs/claude/otlp-verify/`
  に保存済みで、本 issue で再現可能 script を git に入れる必要は無い）

PR 完了の合否条件:

- `site/content/setup/datadog/index.md` が build できる（lychee で死リンクなし）
- `deploy/datadog/README.md` の direct 節が現状実装と整合
- pr-issue-link チェックを通すため本 issue ファイルへの link を PR description に貼る

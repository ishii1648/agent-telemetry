# Datadog facet / measure 設定（direct / collector 共通）

raw events（OTLP Logs）の attribute を Datadog で **検索 facet** および **集計 measure** に昇格する設定。これは [`docs/spec.md`「OTLP export の attribute 意味分類」](../../docs/spec.md#otlp-export-の-attribute-意味分類)の「facet / measure 化」層に対応する。

> **このファイルは recipe を問わず必要。** facet / measure 化は Datadog 側の **index 設定**でしか実現できず、**OTel Collector processor では代替できない**。したがって collector レシピ（[`../otel-collector/`](../otel-collector/)）を採っても、attribute を一級の facet / measure にするにはこの手順が別途必要になる。direct レシピでも Logs Pipeline remapper（[`logs-pipeline.md`](./logs-pipeline.md)）は attribute 整形までで、facet / measure 化はこちらで行う。
>
> 理由（[0040] 本文 B「配布形式」）: attribute 整形（rename / drop）は log の前段処理だが、facet / measure は **Datadog の index に対する設定**であり、log を加工する Collector / Pipeline のレイヤとは別。

## facet 化（検索・group-by 用）

群 3（高 cardinality 識別子）と、tag 化しないが検索したい属性を **facet** にする。`Logs > Configuration > Facets`（または log エクスプローラの属性メニュー `Create facet`）で作成する。

| attribute | 種別 | 用途 |
|---|---|---|
| `session_id` | facet (string) | セッション単位のドリルダウン（tag には昇格しない） |
| `parent_session_id` | facet (string) | subagent ツリーの追跡 |
| `branch` | facet (string) | ブランチでの絞り込み |
| `pr_title` | facet (string) | PR タイトル検索 |

> 群 1 の低 cardinality 属性（`coding_agent` / `repo` / `user_id` 等）は tag に昇格済み（[`logs-pipeline.md`](./logs-pipeline.md) / collector processor）なので、tag facet として既に group-by 可能。改めて log facet を作る必要は無い。

## measure 化（集計用）

群 4（数値 measure）を **measure（数値 facet）** にする。measure 化しないと数値属性が集計対象にならず埋もれる。各属性を facet 作成時に **Measure** + 単位を指定する。

| attribute | 単位 | 例 |
|---|---|---|
| `input_tokens` | （無単位 / count） | token 集計 |
| `output_tokens` | （無単位 / count） | token 集計 |
| `cache_write_tokens` | （無単位 / count） | token 集計 |
| `cache_read_tokens` | （無単位 / count） | token 集計 |
| `reasoning_tokens` | （無単位 / count） | token 集計 |
| `tool_use_total` | （無単位 / count） | ツール使用回数 |
| `mid_session_msgs` | （無単位 / count） | mid-session メッセージ数 |
| `ask_user_question` | （無単位 / count） | AskUserQuestion 回数 |
| `review_comments` | （無単位 / count） | レビューコメント数 |
| `changes_requested` | （無単位 / count） | changes requested 回数 |

> **note**: ここで measure 化するのは **event-level 分析（raw events）** の経路。PR 単位の効率指標（`total_tokens` / `tokens_per_session` 等）は cross-event join が要るため、client がローカル VIEW で集約した **`pr_metrics` gauge（OTLP Metrics）** が担う（[0043]）。gauge は最初から metric なので facet / measure 化は不要。

## イベント種別ごとの count

「イベント種別ごとの件数」は attribute tag ではなく **LogRecord の event name を facet 化**して出す（`event_name` は attribute ではなく LogRecord の field）。Datadog 上では log の `@event_name`（または event name の標準属性）を facet にして count する。

## 作成方法

### 1. UI（推奨・確実）

`Logs > Configuration > Facets > New Facet`、または log エクスプローラで対象 log の属性を開き `Create facet` / `Create measure`。string は facet、数値は measure（単位指定）。

### 2. API（参考）

facet / measure は Logs の index 設定 API で作成できる。`DD-API-KEY` / `DD-APPLICATION-KEY` を付けて以下のような payload を送る（site に合わせて host を変える。エンドポイント仕様は Datadog の最新 API ドキュメントで確認すること）。

```jsonc
// measure の作成例（数値属性を集計対象に昇格）
{
  "type": "measure",
  "path": "input_tokens",   // log attribute path
  "name": "input_tokens",
  "source": "log"
}
```

```jsonc
// facet の作成例（高 cardinality 識別子を検索 facet に）
{
  "type": "facet",
  "path": "session_id",
  "name": "session_id",
  "source": "log"
}
```

> **Collector では不可（再掲）**: 上記は Datadog の index に対する操作で、log を中継する Collector / Pipeline では行えない。collector レシピでもこの設定は Datadog 側で別途実施する。

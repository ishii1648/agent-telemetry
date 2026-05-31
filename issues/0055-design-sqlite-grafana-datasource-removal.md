---
decision_type: design
affected_paths:
  - grafana/
  - docker-compose.yaml
  - Makefile
  - docs/design.md
  - deploy/oss-observability/
tags: [sqlite, storage, grafana, otel, external-backend, architecture, viz-unification]
related: [0032, 0040, 0043, 0046, 0050, 0053, 0054]
---

# SQLite を Grafana datasource として参照する経路（③④）を撤去し、可視化を otel+grafana に一本化する

Created: 2026-05-31

> 本 issue は SQLite の責務分解（dispatch セッションのドラフト分析）を引き継ぎ、**最終決定で結論を上書き**したもの。元ドラフトは「③ だけ段階的縮小・④ 維持」を推奨したが、ユーザ判断で **③④ 両方を撤去**する方針に確定した（理由は「## 決定」参照）。

## 決定

SQLite は **4 つの責務**を担っている。このうち **③④（Grafana datasource としての参照）を撤去**し、**①②（client 側 SoR ＋ 集約エンジン）は維持**する。可視化は OTLP → Mimir/Loki → Grafana（PromQL/LogQL）に一本化する。

| # | 責務 | コード | 方針 |
|---|---|---|---|
| ① | client 側 events **SoR**（hook 書込先・蓄積・dedup・cursor）| `internal/syncdb/`, `internal/backfill/` | **維持** |
| ② | client 側 **集約エンジン**（`pr_metrics` 等 VIEW で cross-event join → gauge 化）| `internal/syncdb/schema/`, `internal/serverclient/metrics.go` | **維持** |
| ③ | **server/team の Grafana datasource**（`frser-sqlite-datasource`）| `internal/serverpipe/`, `grafana/` | **撤去** |
| ④ | **個人ローカルの Grafana datasource**（`make grafana-up` で `.db` 直読み）| `grafana/`, `docker-compose.yaml` | **撤去** |

### なぜ ④ も撤去するか（元ドラフトからの上書き理由）

元ドラフトは ④ を「"単一 binary・外部依存ゼロ・.db 1個" の価値命題そのもの」として維持を推奨し、撤去は「個人が自分の metrics を見るためだけに Collector+Mimir+Loki+Grafana 常駐を強いられる運用退行」と批判した。**この批判は妥当**であり、撤去すると個人ユースで運用が重くなる点は決定として受容する。

それでも ③④ 一括撤去を選ぶ理由:

- **可視化レイヤのメンテ容易性**: SQL 方言（`strftime`/`julianday`/weekday バケット）の dashboard と PromQL/LogQL の dashboard を **二系統で保守する税**が消える。metric を 1 つ足すたびに 2 方言で dashboard を直す負担がなくなる。
- **アーキの一本化**: 可視化経路が「`.db` 直読み（④）/ server SQLite sidecar（③）/ otel+grafana」の 3 系統から **otel+grafana 1 系統**に収斂する。[0032] の Grafana+SQLite 物理結合（sidecar 強制）も解消。
- **依存削減**: `frser-sqlite-datasource` プラグイン依存・`make grafana-screenshot` の SQLite fixture E2E が不要になる。

**トレードオフの明示**: これは「軽い基盤上の一本化」ではなく「重い otel 基盤への片寄せ」。compute（①②）の SQLite は残るので SQL 複雑性は消えず、集約は export レイヤへ移動する（[0053]）。個人ユースの運用は重くなる。それでも **viz 二系統保守の解消**と**経路一本化**を優先する、という判断。

## 維持する根拠（①② はなぜ残すか）

- **外部 backend は SQLite の下流（sink）であって代替ではない**: flush(Logs) は `events` テーブルの未送信行を、gauge(Metrics) は `pr_metrics` VIEW のローカル評価を source にする（`internal/serverclient/{flush,metrics}.go`、[0043]）。source を sink で置き換えることはできない。
- **cross-event join のロックインは backend へ寄せても解けない**（`docs/design.md` §458）。`pr_metrics` の `session_id` cross-event join + latest-wins + sum は、record 間 join をしない log-metric backend では再現不能。join できる主体（ローカル VIEW を持つ client）が事前集約して gauge を送る [0043] の構造を維持する。

## 対応方針（teardown）

| 対象 | 作業 |
|---|---|
| ③ server datasource | `grafana/dashboards/agent-telemetry.json`（SQLite版）・`grafana/provisioning/datasources/*`（frser-sqlite）撤去。可視化は `deploy/oss-observability/` の otel dashboard（Tier 1）に集約 |
| ④ 個人ローカル | `docker-compose.yaml`（`.db` mount）・`Makefile` の `grafana-up` / `grafana-up-e2e` / `grafana-screenshot` 撤去 |
| docs | `docs/design.md` §381「SQLite + Grafana の選定」/§454/§578 を「viz は otel+grafana 一本・SQLite は client SoR/集約に固定」へ書換。`CLAUDE.md`「ダッシュボード変更時の必須作業」更新。`README.md` のスクショ差し替え |
| fidelity | Tier 1 のみ即表示済み。Tier 2/3 復元は [0053]、Tier 4（並列度）abandon は [0054] |

## 採用しなかった代替

- **③ だけ縮小・④ 維持（元ドラフト推奨）**: 二系統保守の税が残る。viz 一本化のメンテ利得を取りに行く判断で却下（運用退行は受容）
- **このタイミングで ①②も含む全 SQLite 削除**: client SoR は外部 backend で代替不可（上記根拠）。集約を Go で再実装する代償が見合わない。却下
- **server 側集約に寄せて client gauge をやめる**: [0040]/§458 で却下済み（SQLite ingest を必須化し Datadog-only 個人と矛盾）

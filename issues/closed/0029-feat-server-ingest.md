---
decision_type: implementation
tags: [server, ingest, http, k8s]
closed_at: 2026-05-10
---

# サーバ側 ingest 実装 — agent-telemetry-server で集計値を受信し SQLite に upsert

Created: 2026-05-10

## 概要

`cmd/agent-telemetry-server/` を新設し、`POST /v1/metrics` で受信した `sessions` 行 + `transcript_stats` 行を SQLite に upsert する HTTP サーバを実装する。サーバは集計を行わず、受信値をそのまま `INSERT OR REPLACE` するだけの「dumb ingest layer」として動作する。仕様の外部契約は `docs/spec.md ## サーバ送信`、設計判断は `docs/design.md ## サーバ側集約パイプライン` を参照する。

## 根拠

[0009](0009-feat-server-side-metrics-pipeline.md) で確定した方針として、サーバ側はクライアントと **DB スキーマだけ** を共通化し、集計（transcript パース等）はクライアント側で完結させる。これにより (1) 送信サイズが極小（月数 MB）、(2) サーバ実装が単純（`internal/syncdb/` 全体を持たず schema DDL のみ）、(3) transcript の保管不要（プライバシー観点とストレージ運用の議論がゼロ）という利点を取る。

## 対応方針

- `cmd/agent-telemetry-server/main.go` 新規
  - `--data-dir`（既定 `/var/lib/agent-telemetry`）、`--listen`（既定 `:8443`）
  - `AGENT_TELEMETRY_SERVER_TOKEN` 環境変数の必須チェック（未設定で起動時エラー終了）
  - 起動時に `<data_dir>/agent-telemetry.db` を作成、`internal/syncdb/schema.sql` を実行（`schema_meta` ハッシュ比較で DDL 再構築する仕組みはクライアントと同じ）
- `internal/serverpipe/` 新規パッケージ
  - `POST /v1/metrics` ハンドラ（Bearer 検証 → payload デコード → `schema_hash` 検証 → SQLite に `INSERT OR REPLACE`）
  - `schema_hash` 不一致なら `schema_mismatch: true` を返して受信拒否（DB は変更しない）
  - `(session_id, coding_agent)` PK での upsert 時に既存行があれば `<data_dir>/collisions.log` に記録（last-write-wins）
- `internal/syncdb/schema.sql` をサーバ binary に埋め込み、起動時 DDL 再構築をクライアントと共通化する。集計ロジック（`internal/transcript/Parse()` 等）はサーバ側に取り込まない
- `Dockerfile.server` 新規作成（`agent-telemetry-server` binary を含む image。k8s で pull する正本、ローカル動作確認時は `docker run` でも使える）
- `deploy/k8s/` 配下に Kustomize ベースの manifest を新規作成
  - `base/`: `agent-telemetry-server` Deployment + Service（port 8443）、Grafana Deployment + Service、共有 PVC（SQLite）、Secret（`AGENT_TELEMETRY_SERVER_TOKEN` のサンプル）
  - `base/grafana-configmap.yaml`: `grafana/provisioning/datasources/agent-telemetry.yaml` と `grafana/dashboards/agent-telemetry.json` を ConfigMap として供給。Grafana pod に `/etc/grafana/provisioning/...` で mount する
  - `overlays/local/`: kind / minikube 用（NodePort、PVC を hostPath、ローカル開発時の動作確認に使う）
  - `overlays/production/`: 実環境用（Ingress、StorageClass、resources 制限）
- `docker-compose.server.yml` は **作らない**。本番が k8s なら docker-compose を二重メンテナンスする利点はなく、ローカル動作確認は `kind` / `minikube` で同じ manifest を回す
- `contrib/systemd/agent-telemetry-server.service` を新規作成（VPS / bare metal で systemd 起動するパス）
- goreleaser 設定に `agent-telemetry-server` ビルドラインを追加

## 受け入れ条件

- [ ] `agent-telemetry-server` が `--listen :8443` で起動し、`AGENT_TELEMETRY_SERVER_TOKEN` 必須チェックが効く
- [ ] `POST /v1/metrics` に有効な payload を送ると、`<data_dir>/agent-telemetry.db` の `sessions` / `transcript_stats` テーブルに行が upsert される
- [ ] payload の `schema_hash` が DB の `schema_meta` と一致しない場合、`schema_mismatch: true` を返し DB を変更しない
- [ ] 受信後の DB を Grafana datasource uid `agent-telemetry` で参照すると、既存ダッシュボード JSON がそのまま動く
- [ ] 同一 `(session_id, coding_agent)` の再 push で `INSERT OR REPLACE` が効き、衝突は `collisions.log` に記録される
- [ ] `kubectl apply -k deploy/k8s/overlays/local/` で kind / minikube 上に server + Grafana + PVC + Secret が立ち上がる
- [ ] Grafana Deployment が ConfigMap mount 経由で既存の `grafana/dashboards/agent-telemetry.json` と `grafana/provisioning/datasources/*.yaml` を参照し、ローカル `make grafana-up` と同じダッシュボードが描画される
- [ ] 不正な Bearer token でのリクエストは 401 を返す
- [ ] `go test ./...` が通る

依存: [0028](0028-feat-server-push-client.md)（クライアント側送信、E2E 検証に必要）

Completed: 2026-05-10

## 解決方法

`agent-telemetry-server` 側を、受信値をそのまま `INSERT OR REPLACE` する dumb ingest layer として実装した。集計ロジックはサーバ側に取り込まず、`internal/syncdb/schema.sql` だけを共通化するために `internal/syncdb/schema/` 新規サブパッケージへ schema embed と hash を切り出し、`syncdb` パッケージと `serverpipe` の双方が依存する形にした。これにより server binary は transcript パーサや sessionindex を引き込まずに済む。

### 主な変更点

- `cmd/agent-telemetry-server/main.go` 新規。`--data-dir` / `--listen` フラグ、`AGENT_TELEMETRY_SERVER_TOKEN` 必須チェック、`/healthz` と graceful shutdown を実装
- `internal/serverpipe/` 新規。`POST /v1/metrics` ハンドラ（Bearer 認証 + gzip optional + 50 MB 上限 + `schema_hash` 検証 + `INSERT OR REPLACE`）と `OpenDB` / `EnsureSchema` を提供。9 ケースのユニットテストを追加（happy path / schema_mismatch / 401 / 405 / upsert / collisions / gzip / bad json / pr_metrics VIEW 連動）
- `internal/syncdb/schema/` 新規サブパッケージ。schema.sql / schema_hash.go / genhash を移動し、`schema.SQL` / `schema.Hash` として export
- `internal/syncdb/syncdb.go` を新サブパッケージ参照に更新。挙動は不変
- `Dockerfile.server` 新規（multi-stage build → distroless/static、CGO_ENABLED=0、nonroot で 8443 公開）
- `.goreleaser.yaml` に `agent-telemetry-server` build / archive を追加（linux/amd64+arm64）
- `.gitignore` に `agent-telemetry-server` バイナリを追加

### スコープ縮小: `deploy/k8s/` と `contrib/systemd/` を本リポジトリに含めない方針へ転換

当初は `deploy/k8s/` 配下に Kustomize manifest（base + overlays/local + overlays/production）を提供し、`contrib/systemd/agent-telemetry-server.service` で VPS / bare-metal の systemd 起動パスも canonical として用意する受け入れ条件だったが、実装後の議論で **どちらも削除** した。理由:

- デプロイ方式（Helm / Argo CD / Flux / 素の kubectl）と cluster topology（StorageClass / IngressClass / cert-manager の有無）は運用者ごとに異なり、ひとつの canonical manifest を置くと fork or copy が必要になる
- 同じロジックが service manager にも当てはまる（systemd / OpenRC / rc.d / supervisord / nohup / docker run / k8s …）。systemd を canonical として置くと、それ以外の運用者は無視するか fork する
- `letsencrypt-prod` cert-manager / `nginx` ingressClass / `standard` StorageClass、`MemoryDenyWriteExecute=yes` / `ProtectSystem=strict` のような defaults はクラスタ・ホスト前提を強く埋め込んでおり、メンテナンスと陳腐化のコストが本リポのスコープに見合わない
- `REPLACE_ME` token / `local-dev-token` リテラル / サンプル Secret といった「うっかり deploy されると事故るもの」をリポに置くこと自体がアンチパターン
- agent-telemetry のスコープは hook + CLI + dashboard JSON + ingest server。インフラ・サービス管理配布物は別レイヤーの責務

代替: 公式提供物は image (`ghcr.io/ishii1648/agent-telemetry-server`) と Go binary (goreleaser) のみとし、デプロイ・サービス管理は運用者の責務とする。docs (0030 の作業) で reference YAML スニペットを示す方針に切り替える。`docs/spec.md` / `docs/design.md` の k8s manifest / systemd unit 記述は merge 後に main で同期更新する（実装ブランチでは触れない CLAUDE.md ルール）。

### 確認した受け入れ条件

- `--listen :8443` で起動・`AGENT_TELEMETRY_SERVER_TOKEN` 未設定時にエラー終了する（`run()` で early return）
- `POST /v1/metrics` で `sessions` / `transcript_stats` を upsert する（`TestServeIngest_HappyPath`）
- `schema_hash` 不一致時に DB を変更せず `schema_mismatch: true` を返す（`TestServeIngest_SchemaMismatch`）
- 既存ダッシュボード JSON の `pr_metrics` VIEW がそのまま動く（`TestServeIngest_PRMetricsViewExists`）
- 同一 `(session_id, coding_agent)` での再 push が `collisions.log` に記録される（`TestServeIngest_CollisionLogged`）
- 不正 Bearer は 401（`TestServeIngest_Unauthorized`）
- `go test ./...` 全 pass、`go vet ./...` clean、`go build ./...` 成功

### 受け入れ条件のうち取り下げ

- ~~`kubectl apply -k deploy/k8s/overlays/local/` で kind / minikube 上に server + Grafana + PVC + Secret が立ち上がる~~ — 上記スコープ縮小により取り下げ
- ~~Grafana Deployment が ConfigMap mount 経由で既存のダッシュボード JSON を参照する~~ — manifest を持たない方針のため取り下げ。docker-compose 経由のローカル可視化は従来通り動く

---
decision_type: design
tags: [server, transport, multi-user]
closed_at: 2026-05-10
---

# 収集した metrics をサーバ側に転送・加工・表示できるようにする

Created: 2026-05-08

## 概要

現状、Claude Code / Codex CLI の hook で収集した metrics は各クライアントの `~/.claude/agent-telemetry.db`（SQLite）に閉じており、可視化も手元の Grafana コンテナ（`make grafana-up`）でしか行えない。複数マシン・複数ユーザーのデータをまとめて分析できるよう、共通サーバへ転送し、サーバ側で加工・表示できる経路を用意したい。

## 根拠

- ローカル SQLite + ローカル Grafana という構成は個人用途には足りるが、組織横断の分析・継続的なダッシュボードホスティングができない
- 別マシン（自宅 / 業務 / CI）で同一ユーザーが Claude を使った場合、現在は DB ファイルを物理的に集約しないと統合できない
- `docs/metrics.md` の観察軸（PR 単位のトークン効率など）はチーム全体での比較が本来価値を持つ。手元 DB に閉じている限り、その比較が実現できない
- `sync-db` が「セッション JSON → SQLite」までは集約してくれているので、その先の「SQLite → サーバ」の経路を増やす余地は仕様上残っている

## 問題

転送・加工・表示を成立させるには、少なくとも以下の論点を仕様として確定する必要がある。現時点ではいずれも未確定。

- **転送方式**: push 型（クライアントがサーバへ送る） / pull 型（サーバが各クライアントから取りに行く）のどちらを基本にするか
- **転送タイミング**: Stop hook 直後に逐次送るのか、`sync-db` 完了後にバッチで送るのか、ユーザー明示の `agent-telemetry push` で送るのか
- **プロトコル / データモデル**: 既存の OTLP（OpenTelemetry）/ Prometheus remote write / 独自 HTTP JSON のいずれに乗せるか。`docs/metrics.md` の OpenMetrics カタログとの対応関係をどう保つか
- **サーバ側の責務**: 単なる蓄積層（オブジェクトストレージ + クエリエンジン）にするのか、加工パイプライン（集計・PR 単位のロールアップ）まで含めるのか
- **既存ローカル Grafana との関係**: ローカル可視化を残しつつサーバ可視化を足すのか、サーバを正としてローカルは廃止するのか
- **認証・権限**: 誰がどのデータを書ける／読めるか。0004（複数人のデータ識別）と密結合
- **オフライン耐性**: ネットワーク断絶時のローカル蓄積とリトライ戦略

## 対応方針

設計判断が複数論点に渡るため、まず方向性を `docs/design.md` に書き起こし、合意後に実装タスクへ分解する。たたき台:

- push 型 + バッチ送信を第一候補にする。Stop hook の latency に転送 I/O を載せたくないこと、CI からの送信を考えるとクライアントから能動的に送る方が単純なため
- プロトコルは OTLP/HTTP を第一候補にする。`docs/metrics.md` の OpenMetrics カタログを将来 Prometheus 互換で配るとしても、収集経路は OTLP の方がメタデータ（resource attributes）を載せやすい
- サーバ側は最初は薄く保つ（ingestion + 蓄積のみ）。加工は Grafana / クエリエンジン側に寄せる
- 認証は 0010 の方針確定を待ってから決める（user 識別子の表現が決まらないと AuthN/AuthZ の設計が割れる）

検討の結果、たたき台を採らない判断もあり得る。`docs/spec.md` の hook / CLI 仕様は転送経路の有無で接点が増える可能性が高いので、決まった時点で同期する。

依存: 0010（複数人のデータ識別）— サーバへ集約する前提で必須。両方が揃わないと意味のある可視化にならない。

Completed: 2026-05-10

## 解決方法

設計セッションで方針を確定し、`docs/spec.md` / `docs/design.md` に書き起こした。実装は子 issue 0028 / 0029 / 0030 に分解した。

当初は raw JSONL 転送（`session-index.jsonl` 差分行 + transcript JSONL 全文）+ サーバ側 `internal/syncdb/` 再実行案で詰めたが、議論の中で **集計値転送**（`sessions` 行 + `transcript_stats` 行のみを送る）への切替を決定した。

### 確定した方針

- **送信するもの**: クライアント `~/.claude/agent-telemetry.db` の `sessions` / `transcript_stats` から差分行を抽出した **集計値のみ**。`session-index.jsonl` の生行や transcript JSONL は送らない
- **クライアント側で sync-db を完結**: 集計（transcript パース・PR 集計）はクライアントで行う。サーバは「dumb ingest layer」として受信値を `INSERT OR REPLACE` するだけ
- **プロトコル**: 独自 HTTP JSON `POST /v1/metrics`、Bearer 認証、HTTP gzip は optional、1 リクエスト 50 MB 上限（保険）
- **スキーマ整合性**: payload に `schema_hash` を含め、サーバの `schema_meta` と不一致なら受信拒否（`schema_mismatch: true` を返す）
- **送信タイミング**: 独立コマンド `agent-telemetry push --since-last`。Stop hook hot path には載せない（ユーザは cron / launchd / systemd timer で起動）
- **差分検知**: `state.json` の `pushed_session_versions: {session_id: sha256(sessions row + transcript_stats row)}`。backfill による後追い更新（`is_merged` 等）で sessions 行 hash が変わると再送信される
- **進行中セッションは除外**: `ended_at` または `end_reason` が空のセッションは送信対象外
- **認証**: 単一 API key（`AGENT_TELEMETRY_SERVER_TOKEN`）。`user_id` は `sessions` 行に含まれ、認証境界とは責務分離
- **サーバ側**: `cmd/agent-telemetry-server/` で別 binary を提供。クライアントと **schema DDL のみ共通化**（`internal/syncdb/schema.sql`）。集計ロジックはサーバ側に持たない。Grafana ダッシュボード JSON / datasource provisioning はそのまま再利用
- **Grafana 設定資産はローカル / k8s で共通参照**: `grafana/dashboards/agent-telemetry.json` と `grafana/provisioning/datasources/*.yaml` をローカル `docker-compose.yaml` の volume mount と k8s ConfigMap mount の両方から参照する 1 セットだけ持つ。配布手段（compose / k8s）自体は揃えず、それぞれの環境ネイティブな形を取る
- **配布形態**: Go binary + Docker image + k8s manifest の 3 形態
  - Go binary: VPS / bare metal で systemd 起動（`contrib/systemd/`）
  - Docker image: GitHub Container Registry `ghcr.io/ishii1648/agent-telemetry-server` を CI で自動更新（[0031](0031-feat-server-image-ghcr-publish.md)）
  - k8s manifest: `deploy/k8s/` に Kustomize ベースで Deployment / Service / ConfigMap / PVC / Secret を提供。本番デプロイの正本
- **新メトリクス追加の遡及反映**: サーバを先にデプロイ → 全クライアント binary 更新 → 各クライアントで `sync-db --recheck && push --full` を実行する運用（クライアント手元の transcript が SoR として残るため成立する）

### 採用しなかった代替

- **raw JSONL 転送 + サーバ側 `internal/syncdb/` 再実行**: 議論の最初の方針候補。サーバ側で過去 transcript から新メトリクスを再集計できる利点を持つが、(1) 送信サイズが 1 セッション数 MB〜数十 MB に膨らむ、(2) サーバが transcript を保管することになりプライバシー観点とストレージ運用の議論が必須になる、(3) サーバ側で transcript パース処理のメンテナンスが発生する、の 3 点を避けて軽量な集計値転送に切り替えた。指標追加の遡及反映は「クライアント binary 配布 + 遡及 sync-db」で代替可能であり、頻度の低い操作のためコストは見合う
- **OTLP / Prometheus remote write**: 後追い更新（`is_merged` 等）の表現が面倒、ローカル + OTel collector の二重スタックの維持コストに見合わない
- **Stop hook 同期 push（タイムアウト 3s）**: latency 侵食、failure mode の hook 出力混入
- **fire-and-forget 子プロセス push**: 失敗が静かに死ぬためデバッグ困難
- **`send_transcripts` フラグ + サニタイズフック**: 集計値だけ送る方針では transcript 自体がサーバに渡らないため、フラグ自体不要
- **サーバ用 Grafana スタックを別 docker-compose ファイルで複製**: 既存 `docker-compose.yaml` が `AGENT_TELEMETRY_DB` env で DB パスを差し替え可能な作りなので、ローカル可視化用途ではそのまま流用できる。複製するとダッシュボード JSON / datasource provisioning の二重メンテナンスを生むだけで利点がない
- **`docker-compose.server.yml` を overlay として提供**: いったん採用したが撤回。サーバの本番形態が k8s pod なので、docker-compose を二重メンテナンスする利点がない。ローカル動作確認は `kind` / `minikube` で k8s manifest を回せばよく、本番と動作確認が同じ仕組みになるメリットの方が大きい

### プライバシー観点

集計値のみ送る方針なので、transcript（会話本体）はサーバに一切渡らない。プライバシー観点の議論は不要になった（`tool_result` も user メッセージもサーバに到達しない）。

### 主な変更点

- `docs/spec.md` に「サーバ送信」節、`agent-telemetry push` コマンド、`AGENT_TELEMETRY_SERVER_TOKEN` 環境変数を追加
- `docs/design.md` に「サーバ側集約パイプライン」節を追加、「既知の制約」にサーバ送信由来の制約を追加（スキーマ整合性が新たに登場）
- 子 issue 4 件を新規発番:
  - `0028-feat-server-push-client.md`（クライアント push 実装、集計値抽出 + 送信）
  - `0029-feat-server-ingest.md`（サーバ ingest 実装、dumb upsert API、k8s manifest）
  - `0030-doc-server-grafana-setup.md`（運用ドキュメント + Grafana 設定資産共通参照）
  - `0031-feat-server-image-ghcr-publish.md`（サーバ image を ghcr.io に CI で自動 push）

### 残課題

- 0028 / 0029 / 0030 / 0031 で実装する。0028 と 0029 は並列着手可能。0031 は 0029 で `Dockerfile.server` ができてから着手する
- 配布形態は VPS（Go binary）/ k8s（image + manifest）の 2 ルート。ユーザは環境次第で選択

依存: 0010（user 識別子）— 完了済み。

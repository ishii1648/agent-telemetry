---
decision_type: process
tags: [server, docs, grafana, k8s, operations]
closed_at: 2026-05-10
---

# サーバ運用ドキュメント — image 配布 + 参考デプロイ手順 + Grafana 設定資産の共通化

Created: 2026-05-10

## 概要

サーバ送信機能を利用者が立ち上げるためのドキュメントを `docs/setup.md` / `docs/usage.md` に追加する。agent-telemetry が公式に提供するのは **image** (`ghcr.io/ishii1648/agent-telemetry-server`) と **Go binary** (goreleaser) のみ。本番デプロイ手段（Helm / Argo CD / Flux / 素の kubectl）と cluster topology（StorageClass / IngressClass / cert-manager の有無）は運用者の責務とし、docs では **参考用 YAML スニペット** を annotated markdown 形式で示す（ファイル化はしない）。Grafana の **設定資産**（dashboard JSON、datasource provisioning yaml）はローカル `docker-compose.yaml` と k8s ConfigMap の **両方から同じファイルを参照可能** な形を保ち、二重メンテナンスを避ける。

## 根拠

当初は `deploy/k8s/` に Kustomize manifest（base + overlays/local + overlays/production）を canonical として提供する案だったが、0029 完了後の議論で取り下げた。理由:

- デプロイ方式と cluster topology は運用者ごとに異なり、ひとつの manifest を canonical として置くと fork or copy が必要になる
- `letsencrypt-prod` cert-manager / `nginx` ingressClass / `standard` StorageClass などの defaults は陳腐化コストが本リポのスコープに見合わない
- `REPLACE_ME` token / サンプル Secret といった「うっかり deploy されると事故るもの」をリポに置くこと自体がアンチパターン
- agent-telemetry のスコープは hook + CLI + dashboard JSON + ingest server。インフラ配布物は別レイヤーの責務

代わりに「image を提供 + docs で参考 YAML を示す」という最小スコープにする。

## 対応方針

- `docs/setup.md` に「サーバ送信を有効化する」節を追加
  - **image の入手**: `docker pull ghcr.io/ishii1648/agent-telemetry-server:latest` の案内（tag pin の例も）
  - **k8s 参考デプロイ**: docs 内の annotated YAML スニペットとして以下を示す（ファイル化はしない、運用者が cluster に合わせて改変する前提）
    - 最小構成: `Deployment` (replicas=1, RollingUpdate→Recreate) + `Service` (ClusterIP) + `PersistentVolumeClaim` (operator が StorageClass を埋める前提) + `Secret` (token)
    - Grafana 同居版: 上記 + Grafana `Deployment` + ConfigMap mount で `grafana/provisioning/datasources/agent-telemetry-docker.yaml` と `grafana/dashboards/agent-telemetry.json` を参照
    - Ingress / cert-manager / StorageClass などのクラスタ前提に依存する箇所は `# REPLACE_ME` コメントで明示
  - `AGENT_TELEMETRY_SERVER_TOKEN` の生成と Secret 経由の供給
  - `~/.claude/agent-telemetry.toml` の `[server]` セクション設定例
  - `agent-telemetry push --since-last` を cron / launchd plist / systemd timer で定期起動するサンプル
  - 新メトリクス追加時の遡及反映手順: サーバ image 更新 → Deployment rolling update → 全クライアント binary 更新 → `agent-telemetry sync-db --recheck && push --full`（`schema_mismatch` エラーのトラブルシュート例も含める）
- `docs/usage.md` に「サーバ DB を Grafana で見る」を追加
  - k8s 経由: Grafana Service にアクセスする手順（Port-forward を最小例として、NodePort / Ingress は応用例）
  - ローカル開発時: `AGENT_TELEMETRY_DB=<server_data>/agent-telemetry.db make grafana-up` で同じダッシュボードがそのまま動くことを明記（個人利用 + ローカル DB 確認の用途）
  - `agent-telemetry push --dry-run` での運用確認方法
- `README.md` の機能一覧と図にサーバ送信経路を追加（送るのは集計値のみ、transcript はクライアント手元に残る、Grafana 設定資産はローカル/k8s で共通参照）

## 受け入れ条件

- [x] `docs/setup.md` の k8s 参考スニペット通りに自分で `kubectl apply -f -` できる粒度で書かれている（最小構成 + Grafana 同居版の 2 パターン）
- [x] スニペット内で operator が埋める箇所（StorageClass / IngressClass / token / cert-manager 設定）が `# REPLACE_ME` で明示されている
- [x] Grafana を docs 通りに mount すれば `grafana/dashboards/agent-telemetry.json` がそのまま描画される（ローカル `make grafana-up` と同一の見た目）
- [x] `docs/usage.md` のローカル開発時手順で、サーバ DB ファイルを `AGENT_TELEMETRY_DB` で指して `make grafana-up` するとダッシュボードがそのまま動く
- [x] `make grafana-screenshot` の E2E 経路は変更せず動き続ける（fixture 使用なのでサーバ/ローカル区別不要）
- [x] `README.md` の図と機能一覧が更新されている

依存: [0028](0028-feat-server-push-client.md) と [0029](0029-feat-server-ingest.md)（実装が無いと文書化が成立しない）、[0031](0031-feat-server-image-ghcr-publish.md)（image が ghcr に上がっていないとデプロイ手順が成立しない）

Completed: 2026-05-10

## 解決方法

サーバ送信のセットアップ手順を `docs/setup-server.md` に **独立 doc** として切り出した（`docs/setup.md` 上の長大な opt-in 章として書く案からは PR レビュー中に転換）。image 入手 → token 生成 → k8s 参考 YAML（最小構成 / Grafana 同居版）→ クライアント `[server]` 設定 → push 定期起動 → 新メトリクス追加時の遡及反映、までを 1 本の流れで書いた。`docs/usage.md` には「サーバ DB を Grafana で見る」を追加し、k8s Port-forward と「サーバ DB スナップショットをローカルで `AGENT_TELEMETRY_DB=… make grafana-up`」の 2 経路を載せた。`README.md` のアーキテクチャ図と機能一覧にも「サーバ集約層（オプトイン）」を 4 番目のレイヤーとして追記した。

### Grafana 同居版の構成判断 — sidecar pattern + PVC 二重 mount

設計上のキモは「ローカル `docker-compose.yaml` と k8s ConfigMap の両方から `grafana/provisioning/datasources/agent-telemetry-docker.yaml` を **そのまま** 流用する」点。datasource yaml の `path: /var/lib/grafana/agent-telemetry.db` を改変せずに k8s でも成立させるため、最終的に以下の構成にした:

- `agent-telemetry-server` と Grafana を **同 pod の 2 container** として配置
- 同じ PVC `agent-telemetry-data` を server は `/var/lib/agent-telemetry`、Grafana は `/var/lib/grafana` に mount
- 結果として server が `/var/lib/agent-telemetry/agent-telemetry.db` に書いた SQLite が、Grafana 側からは `/var/lib/grafana/agent-telemetry.db` として見える

これにより datasource yaml は無改変、`accessModes: ReadWriteOnce` のままで動く（同 pod 内の container 間共有なので RWX 不要）。Grafana 自身の state（`grafana.db`、plugins）は同 PVC root に並ぶが副作用なし。

ConfigMap は `kubectl create configmap … --from-file=… --dry-run=client -o yaml | kubectl apply -f -` のワンライナーで生成する形を示し、`grafana/dashboards/agent-telemetry.json` と `grafana/provisioning/{datasources,dashboards}/agent-telemetry-docker.yaml` を直接 source とする。これでダッシュボード変更（`make grafana-screenshot` で必須の更新作業）が ConfigMap 再生成だけで k8s 側にも反映される。

### 採用しなかった代替

- **Grafana を別 Deployment で立てて RWX PVC で共有**: RWX 対応 StorageClass（EFS / Filestore / Azure Files）を前提にすると、cluster topology 仮定が増える。sidecar pattern なら標準 RWO で動く
- **subPath で `agent-telemetry.db` 単一ファイルだけ mount**: WAL モードで `-wal` / `-shm` が同ディレクトリに必要なため壊れる。ディレクトリ全体を mount する方針が安全
- **datasource yaml を k8s 用に別ファイル化**: 「同じファイルを参照」という設計判断（`docs/design.md ## サーバ側集約パイプライン`）に反する。二重メンテになるので却下
- **`docker-compose.yaml` 側のパスを変えて両環境を揃える**: 受け入れ条件「`make grafana-screenshot` の E2E 経路は変更せず動き続ける」を満たすには既存 mount path に手を入れない方が安全

### スコープから外した — クライアント push の systemd timer

issue 本文では `agent-telemetry push --since-last` の定期起動サンプルとして cron / launchd plist / systemd timer の 3 つが挙げられていたが、systemd timer は最終的にドロップした。理由: クライアントは dev 機（macOS / remote dev box）が主な想定で、Linux dev 機の `systemctl --user` 運用はまれ。Linux ケースは cron が同等にカバーするので、3 つあると冗長と判断した（user 確認の上）。

### setup.md 内 opt-in 章 → 独立 doc への切り出し

当初は `docs/setup.md` 5 章として書いたが、PR review で「opt-in かつ約 400 行のボリュームがあるので別 md への分離を検討」と指摘されたため、`docs/setup-server.md` へ独立 doc として切り出した。理由: (1) サーバ送信は opt-in で、ローカル単独利用のユーザにとっては setup.md 内で読み飛ばすコストが高い、(2) k8s YAML スニペットが 2 種類あり setup.md の他章（hook 登録 / Grafana 設定）と粒度が大きく違う、(3) サーバ運用は別の運用者ロール（インフラ担当）に渡すケースが多く、そのとき URL 1 本で渡せた方がよい。`docs/setup.md` 5 章は完全に削除し、移行章を 5 に戻した（中途半端な stub は残さない）。README.md の docs 一覧表と `docs/usage.md` の「サーバ DB を Grafana で見る」節からは `setup-server.md` へリンクを張り直した。

### 受け入れ条件の確認

- 最小構成 + Grafana 同居版の 2 パターンの YAML を `docs/setup.md` 5.3 / 5.4 に annotated で追加
- StorageClass / IngressClass / token / cert-manager 設定箇所に `# REPLACE_ME` を付与
- Grafana 同居版は `agent-telemetry-docker.yaml` ConfigMap mount + 同 PVC 二重 mount により dashboard JSON を無改変で描画
- `docs/usage.md` の「サーバ DB を Grafana で見る」で `kubectl cp` → `AGENT_TELEMETRY_DB=… make grafana-up` の経路を案内
- `grafana/`, `docker-compose.yaml`, `e2e/` には触っていないため `make grafana-screenshot` は不変
- `README.md` のアーキテクチャ図に push 経路を追加し、機能一覧に「4. サーバ集約層（オプトイン）」を追記
- `docs/usage.md` の CLI コマンド一覧にも `agent-telemetry push` を追加（exit code 注記つき）

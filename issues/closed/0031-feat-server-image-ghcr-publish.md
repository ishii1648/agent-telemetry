---
decision_type: implementation
tags: [server, ci, container-registry, ghcr, k8s]
closed_at: 2026-05-10
---

# サーバ image を GitHub Container Registry に CI で自動 push

Created: 2026-05-10

## 概要

`agent-telemetry-server` の Docker image を GitHub Actions で自動ビルドし、`ghcr.io/ishii1648/agent-telemetry-server` に push する CI を整備する。k8s pod が pull する正本イメージを再現性のある形で配布する。

## 根拠

[0009](0009-feat-server-side-metrics-pipeline.md) で確定した方針として、サーバの本番形態は k8s pod。[0029](0029-feat-server-ingest.md) で `Dockerfile.server` を新設し `deploy/k8s/` の Deployment が `ghcr.io/ishii1648/agent-telemetry-server` を pull する形に決めたが、image を ghcr.io に上げる仕組みは未整備。手動 push は再現性とセキュリティの観点で持続しないため、CI で自動化する。

## 対応方針

- `.github/workflows/docker-image.yml` を新規作成
  - トリガ: main push（`latest` tag を上書き）/ tag push (`v*`)（`vX.Y.Z` tag を新規発行）
  - permissions: `contents: read` + `packages: write`
  - login: `docker/login-action@v3` で `${{ secrets.GITHUB_TOKEN }}` を使い ghcr.io にログイン
  - build: `docker/build-push-action@v6` で `Dockerfile.server` をビルド & push
  - multi-arch: `linux/amd64` + `linux/arm64` を `docker/setup-buildx-action` + `docker/setup-qemu-action` で生成
  - tag 戦略: `docker/metadata-action@v5` で main は `latest`、tag は `vX.Y.Z` + `vX.Y` + `vX` を自動生成
  - SBOM と provenance attestation: GitHub の `actions/attest-build-provenance` を有効化（追加コスト最小、サプライチェーン可視化）
- `Dockerfile.server` を新規作成（[0029](0029-feat-server-ingest.md) で本体実装）
  - multi-stage build（builder stage で `CGO_ENABLED=0 go build`、final stage は distroless / static）
  - non-root user で起動
  - `--listen :8443` がデフォルトで露出するよう EXPOSE 8443
- ghcr.io 上のリポジトリは **public visibility** に設定（誰でも pull 可能、k8s manifest をそのまま提供できる）
- 失敗時の rollback は不要（`latest` 上書き失敗時は前バージョンが残る、tag は immutable なので衝突は CI 失敗で検知）

## 受け入れ条件

- [ ] main branch への push で `ghcr.io/ishii1648/agent-telemetry-server:latest` が更新される
- [ ] tag push (`v*`) で `ghcr.io/ishii1648/agent-telemetry-server:vX.Y.Z` / `:vX.Y` / `:vX` が公開される
- [ ] image は `linux/amd64` と `linux/arm64` の両方をサポート（`docker manifest inspect` で確認）
- [ ] ghcr.io 上のリポジトリが public visibility で、未認証でも `docker pull ghcr.io/ishii1648/agent-telemetry-server:latest` が成功する
- [ ] `kubectl run --image=ghcr.io/ishii1648/agent-telemetry-server:latest --env="AGENT_TELEMETRY_SERVER_TOKEN=test" -- --listen :8443` でコンテナが起動し HTTP listen する
- [ ] SBOM / provenance attestation が GitHub PR / release で確認できる
- [ ] CI ジョブの実行時間が 5 分以内に収まる（buildx cache を使う）

依存: [0029](0029-feat-server-ingest.md)（`Dockerfile.server` がないとビルドできない）

Completed: 2026-05-10

## 解決方法

`.github/workflows/docker-image.yml` を新設し、main push と `v*` tag push で `ghcr.io/ishii1648/agent-telemetry-server` を自動更新する CI を整備した。PR では同じ Dockerfile / serverpipe / schema sub-package 変更を multi-arch build で verify するが registry には push しない（fork PR が誤って ghcr namespace に書き込めない安全側の設計）。`Dockerfile.server` 自体は 0029 で本体実装済みなので追加変更なし。

### 主な変更点

- `.github/workflows/docker-image.yml` 新規
  - トリガ: `pull_request`（同じ paths filter で build verify のみ） + `push` to `main`（`Dockerfile.server` / server cmd / serverpipe / schema sub-package / go.{mod,sum} / 自身のいずれかが変更された場合のみ） + `v*` tag push + `workflow_dispatch`
  - `permissions`: `contents: read` / `packages: write` / `id-token: write` / `attestations: write`。後ろ 3 つは push event でのみ実際に行使される（login / push / attest step は `if: github.event_name != 'pull_request'` で gate）。GitHub Actions の `permissions:` は expression を受け付けないため static 宣言にしてあるが、fork PR は GitHub 側で GITHUB_TOKEN が read-only に自動降格されるため安全
  - `docker/setup-qemu-action@v3` で arm64 emulation を仕込み、`docker/setup-buildx-action@v3` で buildx を起動
  - `docker/login-action@v3` で `${{ secrets.GITHUB_TOKEN }}` を使い ghcr.io にログイン（PR では skip）
  - `docker/metadata-action@v5` の tag 戦略:
    - PR / 非 default branch: tag なし（build verify のみ）
    - main: `:latest` + `:sha-<short>`
    - tag (`vX.Y.Z`): `:vX.Y.Z` + `:vX.Y` + `:vX` + `:latest`
  - `docker/build-push-action@v6` で `linux/amd64` + `linux/arm64` を build。`push` / `provenance` / `sbom` は `${{ github.event_name != 'pull_request' }}` で条件付き。`type=gha` (`mode=max`) で GitHub Actions Cache に layer をキャッシュ
  - `actions/attest-build-provenance@v2` で push 後の image digest に attestation を anchor し ghcr.io 側にも push（PR では skip。`gh attestation verify` / `cosign verify-attestation` で検証可能）

### 確認した受け入れ条件

- workflow YAML を `actionlint` で lint clean（exit 0）
- pull_request（build verify only） / main push / `v*` tag push の 3 トリガが含まれ、`paths` filter で本 workflow 自身も含めて関連変更時にのみ起動
- multi-arch (`linux/amd64,linux/arm64`) を `setup-qemu-action` + `build-push-action.platforms` で指定
- SBOM / provenance attestation（`provenance` + `sbom` + `actions/attest-build-provenance`、push event のみ）
- buildx GitHub Actions Cache (`type=gha`) で再ビルドが 5 分以内に収まる想定（実測は CI 上で検証）
- 環境準備が必要な実 push / public visibility 設定 / `kubectl run` での実行確認は CI 起動後に確認する手作業として残す（GitHub package settings から visibility を public にする必要がある）

### 関連スコープ変更

- 0029 で当初予定していた `deploy/k8s/` Kustomize manifest は close 後の議論でリポジトリから削除した（運用者の責務というスコープ判断）。本 issue はそれと独立に「公式 image を ghcr に上げる」という責務だけを完了させており、image さえあれば運用者が自分の cluster に合わせた manifest を書ける形になる

### 残課題

- ghcr.io のパッケージ visibility は GitHub の Web UI で初回 push 後に手動で **Public** に切り替える必要がある（`docker pull` を未認証で通すため）。これは API で自動化することもできるが、初回 1 回限りの操作なので手作業で許容
- `kubectl run` での実機起動確認は 0030（運用ドキュメント）の作業に含める想定

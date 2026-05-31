---
decision_type: implementation
tags: [server, ci, ghcr, release]
closed_at: 2026-05-11
---

# release publish で server image を semver tag に再付与する

Created: 2026-05-11

## 概要

[0031](0031-feat-server-image-ghcr-publish.md) で確定した「main push と `v*` tag push で `ghcr.io/.../agent-telemetry-server` を publish」のうち、tag push トリガを廃止し、新 workflow `.github/workflows/release-tag-image.yml` で **GitHub Release publish** をトリガに **main で既に push 済みの `:sha-<short>` image を `:vX.Y.Z` / `:vX.Y` / `:vX` / `:latest` に再 tag（promote）** する方式へ切り替える。

main push → `:latest` / `:sha-<short>` 経路はそのまま維持する（外向きには `:latest` 追従と tag pin の semantic は変わらない）。

## 根拠

- **digest 一致による「main で検証されたイメージ ≡ release イメージ」の保証**: tag push で再 build すると依存解決のタイミング差で digest が変わりうる。再 tag なら同一 digest が `:sha-<short>` と `:vX.Y.Z` の両方から参照されるので、staging で `:sha-<short>` を pin して通った検証結果が release 後にそのまま保証される。
- **provenance / SBOM attestation の継承**: attestation は image digest に紐づくため、再 tag では既存 attestation がそのまま有効。再 build だと attestation を打ち直す必要がある。
- **release 公開の意図性**: `on: release: types: [published]` を trigger にすると、git tag だけ打って release notes を書かない fallthrough を防げる（`git tag v0.0.7 && git push --tags` だけでは何も起きなくなる）。
- **CI ビルド時間の節約**: tag push 時の multi-arch build（~10 分）が release 経路ではゼロになる。
- 0031 の方針は **そのままでも壊れていない**（v0.0.1〜v0.0.6 はこの形で運用済み）。今回の変更は外向き semantic を保ったまま digest 一貫性と意図性を強化する純粋な改善で、0031 を `supersedes` するほどの転換ではない（main push 経路は不変）。

## 対応方針

- `.github/workflows/release-tag-image.yml` を新設
  - trigger: `on: release: types: [published]`
  - `github.event.release.target_commitish` を short SHA に解決し `ghcr.io/.../agent-telemetry-server:sha-<short>` を source として参照する
  - `docker buildx imagetools create -t <new-tag> <source>` で multi-arch manifest list ごと再 tag（再 build なし）
  - 付与する tag: `:vX.Y.Z` / `:vX.Y` / `:vX` / `:latest`（`docker/metadata-action` の semver pattern を流用）
  - target_commitish が main 以外を指す release は明示的に fail
  - 元 `:sha-<short>` image が ghcr に存在しない場合も明示的に fail（GHCR retention 切れ等の sanity check）
  - permissions は 0031 と同じ最小セット（`contents: read` / `packages: write` / `id-token: write` / `attestations: write`）。新たに provenance attestation を打ち直す必要はないが、login と再 tag のために `packages: write` は必要
- 既存 `.github/workflows/docker-image.yml` を修正
  - `on.push.tags: ["v*"]` を削除（二重 publish を防ぐ）
  - main push の `:latest` / `:sha-<short>` 経路と PR の build verify 経路は変更しない
  - `docker/metadata-action` の semver pattern も削除する（tag trigger がなくなり呼ばれない）

## 受け入れ条件

- [ ] `.github/workflows/release-tag-image.yml` が `on: release: types: [published]` で起動する
- [ ] release publish 後、`ghcr.io/.../agent-telemetry-server:vX.Y.Z` / `:vX.Y` / `:vX` / `:latest` が **元 `:sha-<short>` image と同一 digest** で pull できる（`docker buildx imagetools inspect` で digest 比較）
- [ ] `.github/workflows/docker-image.yml` から `tags: ['v*']` トリガと semver tag pattern が削除され、main push と PR build verify は維持される
- [ ] target_commitish が main 以外を指す release で workflow が fail する
- [ ] 元 `:sha-<short>` が GHCR に存在しない場合に workflow が明示的に fail する
- [ ] 既存 attestation（provenance / SBOM）が re-tag 後の `:vX.Y.Z` でも `gh attestation verify` で検証できる（digest 不変なので継承される想定）
- [ ] workflow YAML が `actionlint` で lint clean
- [ ] 第三者 actions は commit SHA で pin する（0031 と同じ運用ルール）

依存: [0031](0031-feat-server-image-ghcr-publish.md)（`:sha-<short>` を main push で publish する経路がないと再 tag の source がない）

Completed: 2026-05-11

## 解決方法

`.github/workflows/release-tag-image.yml` を新設し、GitHub Release publish (`on: release: types: [published]`) を trigger に main で push 済みの `:sha-<short>` image を `:vX.Y.Z` / `:vX.Y` / `:vX` / `:latest` に **再 tag（promote）** する経路を追加した。`docker buildx imagetools create` で multi-arch manifest list ごと付け替えるため再 build は走らず、release 後の tag は元 `:sha-<short>` と byte-identical な digest を指す。provenance / SBOM attestation は digest に紐づくのでそのまま継承される。

合わせて `.github/workflows/docker-image.yml` から `push.tags: ['v*']` トリガと `docker/metadata-action` の semver pattern を削除し、main push と PR build verify の 2 経路に絞った（二重 publish 防止）。0031 で確定した「main push で `:latest` / `:sha-<short>` を publish」の挙動は不変。

### 主な変更点

- `.github/workflows/release-tag-image.yml` 新規
  - trigger: `on: release: types: [published]` のみ。`git tag` 単独 push では起動しない（release notes が書かれていることを暗黙に require する）
  - `actions/checkout` を `fetch-depth: 0` で実行し、`git rev-list -n 1 refs/tags/<tag>` で release tag の commit を解決
  - **target_commitish は使わない** — tag の真の commit のみ信頼する。release UI で branch を `main` に指定したまま実体は別 branch というケースを排除するため
  - `git merge-base --is-ancestor <tag_sha> origin/main` で main reachability を gate（feature branch の release を拒否）
  - `docker buildx imagetools inspect` で source image の存在を sanity check（GHCR retention 切れ等で `:sha-<short>` が消えていれば明示的に fail）
  - `docker/metadata-action@v5` の semver pattern を流用して destination tag を生成。`:latest` は `enable=${{ !github.event.release.prerelease }}` で pre-release を除外（`v1.0.0-rc.1` 等で `:latest` を巻き戻さない）
  - `docker buildx imagetools create -t <new>... <source>` で manifest list を再 tag、各 promoted tag の digest が source と一致することを再 inspect で検証
  - permissions は `contents: read` + `packages: write` の最小セット（attestation を再発行しないため `id-token` / `attestations` は不要）
  - 第三者 actions は 0031 と同じ commit SHA で pin

- `.github/workflows/docker-image.yml` 修正
  - `on.push.tags: ["v*"]` を削除
  - `docker/metadata-action` の `type=semver,pattern={{version|major.minor|major}}` 3 行を削除
  - ヘッダコメントと「Tag strategy」コメントを更新し、semver tag は release-tag-image.yml が担当する旨を明示
  - main push の `:latest` / `:sha-<short>` 経路と PR build verify は無変更

### 確認した受け入れ条件

- `actionlint` で両 workflow が clean（exit 0）
- `make intent-lint` clean
- target_commitish 経由ではなく tag commit で main reachability を gate（branch 経由の release を拒否）
- 元 `:sha-<short>` が GHCR にない場合に `imagetools inspect` 段階で `::error::` を出して fail
- `imagetools create` 後に各 promoted tag の digest を再 inspect して source と一致することを確認するステップを内蔵
- pre-release では `:latest` が更新されない（`type=raw,value=latest,enable=${{ !github.event.release.prerelease }}`）
- 第三者 actions は commit SHA で pin（0031 と同じ運用ルール）

### 残課題 / 後続確認

- 実 release publish で `gh attestation verify` が re-tag 後の `:vX.Y.Z` でも通ることの実機確認は本 PR merge 後の初回 release 時に行う（attestation は digest に紐づくため理論上は通るはず）
- `release.yml`（goreleaser）→ GitHub Release publish → `release-tag-image.yml` の連鎖が想定どおり起動することを次回 release で確認する

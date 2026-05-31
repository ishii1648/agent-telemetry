---
decision_type: implementation
tags: [server, ci, ghcr, release]
closed_at: 2026-05-11
---

# release-tag-image を docker-image の main build 完了まで自動 wait させる

Created: 2026-05-11

## 概要

[0033](0033-feat-release-image-retag.md) で導入した `.github/workflows/release-tag-image.yml` は、release publish の時点で **main 最新 commit の `:sha-<short>` image が GHCR に存在する** ことを前提に source image の sanity check (`docker buildx imagetools inspect`) を行う。実装上は正しいが、release を切るタイミングを **人間が手動で順序制御する** 必要が残っており、運用上の欠陥になっている。

## 問題

`docker-image.yml`（main push → multi-arch build → ghcr push）と `release.yml`（goreleaser → GitHub Release publish）の所要時間に大きなギャップがある:

- main merge → `docker-image.yml` の multi-arch build 完了 → ghcr に `:sha-<short>` push: **約 5–10 分**
- `git push --tags` → `release.yml`（goreleaser）→ GitHub Release publish: **数秒**

main merge 直後に release を切ると `release-tag-image.yml` のほうが先に走り、source image (`:sha-<short>`) がまだ無いので sanity check が `::error::Source image ... not found` で fail する。実例:

https://github.com/ishii1648/agent-telemetry/actions/runs/25633511235/job/75241251777

現状は「main の `docker-image.yml` 完了を見届けてから release を切る」という暗黙の運用ルールで回避しているが、これは CI 側で吸収すべき責務。

## 対応方針

### 候補

| # | 方針 | 採否 | 主な trade-off |
|---|---|---|---|
| 1 | `release-tag-image.yml` で source image inspection を retry/wait（timeout 付き polling） | **採用** | 単純・1 ファイル変更で済む。digest 一貫性（[0033](0033-feat-release-image-retag.md)）も保たれる。timeout を切れば feature branch の release のように永遠に来ない場合も拾える |
| 2 | `release-tag-image.yml` の trigger を `workflow_run` に変える | 不採用 | `Docker image` workflow 完了 + 対応する release tag の存在を突き合わせる必要があり、event 二系統（`release: published` と `workflow_run`）+ どちらが先に終わったか分岐が必要で、複雑度が大きい割に得るものは「polling を短縮できる」程度 |
| 3 | `release.yml` 内で main build 完了を待つ | 不採用 | release publish 自体が遅延する。CI の責任分割としても goreleaser の前段 gate に詰め込むのは筋が悪い |
| 4 | `release.yml` に image build/push を統合（goreleaser hook で同時 build） | 不採用 | [0033](0033-feat-release-image-retag.md) で確定した「main で検証された image ≡ release image（digest 一致）」が壊れる。再 build により digest が変わり、provenance / SBOM attestation も打ち直しになる |
| 5 | `repository_dispatch` で `docker-image.yml` 完了を `release-tag-image.yml` に通知 | 不採用 | release tag 待ち keyed lock のようなものを Artifact 経由で受け渡す必要があり、状態管理が複雑。case 2 と同種だが密結合度がさらに高い |

### 採用案（案 1）の詳細

`.github/workflows/release-tag-image.yml` の **既存 main reachability gate の後**（= cheap な check を先に通してから）に、source image (`:sha-<short>`) の出現を polling する step を挿入する:

- 既存の `Resolve release commit` step（`git merge-base --is-ancestor` で feature branch の release を拒否）は **そのまま維持**。これが最初の gate なので、main 以外を指す release で無駄に polling することはない
- 既存の `Verify source image` step を **polling loop に置き換え**: `docker buildx imagetools inspect` を一定間隔で再試行し、source image が見えたら抜ける
- timeout: `docker-image.yml` の job timeout（20 分）+ release publish 後の追加 buffer を見て **25 分** を上限にする
- polling interval: **30 秒**。GHCR への過負荷を避けつつ、main build 完了から ~30 秒以内に検知できる
- timeout に達した場合: 明示的な `::error::` を出して fail（「`docker-image.yml` が main で完了したか確認せよ」というメッセージ）。reachability check は通っているので、image が来ない原因は (a) docker-image.yml の build 失敗 or (b) `paths` filter で skip された のいずれか。どちらも human intervention が必要なので fail させて気付かせる
- workflow の `timeout-minutes` も 10 → 30 に引き上げる（polling 25 分 + 既存 step 数分の余裕）

これにより、release を切るタイミングは人間が制御不要になる:

```
t=0      : git merge → docker-image.yml 起動（~10 分）
t=数秒～  : git push --tags → release.yml → release publish → release-tag-image.yml 起動
t=数秒～  : release-tag-image.yml の reachability gate 通過
t=数秒～  : source image polling 開始
t=10 分付近: docker-image.yml が :sha-<short> を push
t=10 分付近: polling が source image を検知 → re-tag 実行
```

## 受け入れ条件

- [ ] `release-tag-image.yml` が `:sha-<short>` を最大 25 分待ってから fail する（タイムアウト & polling interval は workflow 内で読み取れる定数として明示）
- [ ] 既存の main reachability gate（`git merge-base --is-ancestor`）は polling より **前** に評価される（feature branch の release は polling せず即 fail）
- [ ] timeout で fail した場合、原因切り分けを促す `::error::` メッセージが出る（`docker-image.yml` の状態を確認するヒント）
- [ ] polling 中に image が見えた時点で即 re-tag が走る（追加の wait なし）
- [ ] [0033](0033-feat-release-image-retag.md) の不変条件（main で push 済み `:sha-<short>` と release tag の digest 一致）は変更しない
- [ ] `actionlint` で workflow が clean
- [ ] `make intent-lint` clean
- [ ] 第三者 actions は commit SHA で pin（[0033](0033-feat-release-image-retag.md) と同じ運用ルール）

依存: [0033](0033-feat-release-image-retag.md)（本 issue は 0033 で導入した workflow の運用上の欠陥を埋める fix）

Completed: 2026-05-11

## 解決方法

`.github/workflows/release-tag-image.yml` の `Verify source image` step を **polling loop に置き換え**、`:sha-<short>` の出現を最大 25 分間 30 秒間隔で待つよう変更した。これにより `docker-image.yml`（main の multi-arch build）が完了する前に release を切っても自動で待機するようになり、人間が「main build 完了 → release」の順序を意識する必要がなくなった。

### 主な変更点

- `.github/workflows/release-tag-image.yml`
  - `Verify source image` → `Wait for source image` に rename。step 内で `WAIT_TIMEOUT_SECONDS=1500`（25 分）と `POLL_INTERVAL_SECONDS=30` を env に明示し、`deadline` を時刻ベースで管理する while loop で `docker buildx imagetools inspect` を再試行する形に変更
  - 既存の main reachability gate（`Resolve release commit` step）は **polling より前** にあるので変更不要。feature branch から切られた release は依然として polling に入る前に fail する
  - timeout 時の `::error::` メッセージを「source image が見えなかった原因は (a) docker-image.yml の build 失敗 or (b) `paths` filter で skip」だと示唆する内容に更新
  - job の `timeout-minutes` を 10 → 30 に引き上げ（polling 25 分 + その他 step 分の余裕）。コメントで根拠を明示
  - source image を待つだけで、見つかった後の `docker buildx imagetools create` による re-tag / digest 一致 verify は 0033 の実装そのまま（digest 一貫性は不変）

### 確認した受け入れ条件

- `actionlint` で workflow clean（exit 0）
- `make intent-lint` clean
- main reachability gate が polling より前にあることを workflow 上の step 順で確認
- timeout 時の `::error::` メッセージに `docker-image.yml` の確認を促す文言が入っている
- polling は `inspect` 成功直後に loop を抜けて re-tag step に進む（追加の `sleep` なし）
- 0033 で確定した digest 一貫性ロジック（`imagetools create` 後の `inspect` で source digest と照合する step）は無変更

### 残課題 / 後続確認

- 次回の release publish で polling 動作を実機検証する。merge → 即 release を試して、polling が正しく `:sha-<short>` を捕まえることと、timeout に達しないこと（通常運用なら ~10 分以内に source が出る想定）を確認する

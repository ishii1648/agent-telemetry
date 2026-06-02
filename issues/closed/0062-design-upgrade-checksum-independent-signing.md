---
decision_type: design
tags: [security, upgrade, supply-chain, release]
---

# upgrade の tarball/checksum が同一 release 由来で独立署名されていない

Created: 2026-06-02

## 概要

セキュリティレビュー §3「供給網とローカル可視化」/ §4 Critical 級の残課題。`internal/upgrade/upgrade.go` はダウンロードした tarball を `checksums.txt` で検証するが、成果物も checksum も同一 GitHub release 由来で、独立署名されていない。

## 根拠

§4 で「`upgrade`/release 経路の侵害でユーザが攻撃者バイナリを実行」は Critical に較正されている。release が侵害されると tarball と checksum を同時に差し替えられ、checksum 検証だけでは攻撃者バイナリの実行を防げない。signing による独立した信頼の根を持たせるべきか方針を決める必要がある。

## 問題

- tarball と `checksums.txt` が同一 release artifact 由来で、改竄時に両方差し替え可能
- checksum 検証が integrity（破損検知）にはなるが authenticity（出所証明）にならない

## 対応方針

- cosign / minisign 等による checksum or artifact の独立署名を release pipeline（goreleaser）に導入する方針を design に記録する
- `internal/upgrade/upgrade.go` 側で署名検証を行う設計を検討する（公開鍵の配布方法含む）
- 受容するならその理由（GitHub release の信頼を前提とする旨）を明記し Critical 較正を下げる根拠とする

Completed: 2026-06-02

## 解決方法

独立署名は導入せず、**`upgrade` サブコマンドそのものを廃止して self-update 経路（任意コード実行に直結する attack surface）を除去する**方針を採用した。`docs/design.md`「配布補助 > `upgrade` サブコマンドの廃止（self-update 経路の除去）（[0062]）」に記録。

> 当初は cosign keyless で `checksums.txt` を独立署名する方針で着手したが（goreleaser `signs` + release.yml の cosign step まで実装）、レビュー方針の見直しで「self-update を残して署名で守る」より「self-update を削って attack surface ごと無くす」方が単純かつ安全と判断し、署名導入はすべて revert した。

- **コード削除**: `internal/upgrade/`（`upgrade.go` / `upgrade_test.go`）を削除し、`cmd/agent-telemetry/main.go` から `upgrade` case・`runUpgrade`・import・usage 行を除去。
- **署名導入の revert**: `.goreleaser.yaml` の `signs` ブロックと `.github/workflows/release.yml` の `id-token: write` / `sigstore/cosign-installer` step を撤去（self-update を守るための信頼の根が不要になったため）。
- **更新手段**: self-update は持たず、更新は再インストール（GitHub Releases tarball / `go build` / `make install`）で行う。site の install 手順は元々 `upgrade` を案内していないため変更なし。
- **較正**: self-update 経路が消えることで「`upgrade`/release 経路の侵害で攻撃者バイナリを実行」という Critical シナリオの agent-telemetry 起因の経路は消滅。残るのは手動 DL tarball の一般的供給網リスクのみで、self-update を持たない一般 CLI と同等＝本ツール固有の Critical ではない。手動 DL の authenticity 強化（署名）は将来の任意改善として余地を残すがスコープ外。
- **却下した代替**: (a) cosign keyless 署名＋client 検証の実装（`sigstore-go`・鍵/identity 管理・air-gapped bypass まで保守コスト大、かつ同一 pipeline 署名は build 時 CI runner 侵害に無効で完全防御にならず、self-update 保持の便益がコストに見合わない）、(b) 署名なしのまま `upgrade` 据え置き（rename して即実行する経路が残り Critical を下げられない）。

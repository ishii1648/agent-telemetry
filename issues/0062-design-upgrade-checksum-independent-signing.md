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

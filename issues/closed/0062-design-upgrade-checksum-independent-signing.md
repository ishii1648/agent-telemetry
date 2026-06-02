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

受容はせず、**cosign keyless で `checksums.txt` を独立署名する**方針を採用し `docs/design.md`「配布補助 > `upgrade` の成果物署名（cosign keyless）と独立した信頼の根（[0062]）」に記録した。

- **方針記録（design）**: 信頼の根を Sigstore（Fulcio/Rekor）に置き、署名者 identity を GitHub Actions の OIDC（release workflow ref）とする。release asset を後から差し替えただけの攻撃者は正規 identity で再署名できない。却下した代替（minisign / cosign 固定鍵ペア＝長期秘密鍵の配布・ローテ負荷と鍵自体が高価値ターゲット化／署名なしでの受容＝任意コード実行直結のため非受容）も明記。
- **goreleaser 署名ステップ追加**: `.goreleaser.yaml` に `signs`（`cosign sign-blob` を `artifacts: checksum` に適用、`checksums.txt.sig` + `checksums.txt.pem` を release asset 化）を追加。`.github/workflows/release.yml` に `id-token: write` 権限と `sigstore/cosign-installer` step を追加。
- **client 側検証は後続 PR**: `internal/upgrade/upgrade.go` での `sigstore-go` による SAN/issuer 検証（＋ air-gapped 向け bypass）は実装規模が大きいため分離。署名のみでは client が検証しない限りユーザは保護されないため、**Critical 較正は本 PR では据え置き**、client 検証強制が入った段階で High に下げる。
- **較正の下限**: 同一 pipeline 内署名は公開後の asset 改竄・repo write 侵害には有効だが build 時 CI runner 侵害には無効。これが較正を下げられる下限を決めることを残留リスクとして明記した。

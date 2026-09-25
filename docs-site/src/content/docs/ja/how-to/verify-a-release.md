---
title: ダウンロードしたものを検証する
description: >-
  実行する前に、リリースの署名、SLSA プロベナンス、SBOM、OpenVEX の表明を検証する。
  インストーラーをそのままシェルにパイプしないこと。
---

control plane はセキュリティ製品なので、リリースに対して最初にすべきことは、それが
**プロジェクトが公開したものであると証明すること** です。Olivares AI のリリースは、
暗号的に検証するために必要なすべてを同梱しています: チェックサムに対する署名、SLSA
プロベナンスの表明、SBOM (SPDX + CycloneDX)、そして OpenVEX の表明 — すべて
**タグではなくダイジェストで** 参照されます。

:::danger[`curl | bash` は決して行わない]
インストーラーをシェルにパイプしないでください。成果物をダウンロードし、**検証し**、
その後でのみ実行してください。以下の手順がその方法です。
:::

## リリースに同梱されるもの

| 成果物 | 内容 |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | すべての成果物の SHA-256。cosign の署名と証明書付き |
| `*_<os>_<arch>.tar.gz` | リリースアーカイブ |
| `*.sbom.sigstore.json` | 署名済み in-toto 表明としての SBOM (SPDX) |
| `*.vex.sigstore.json` | 署名済み in-toto 表明としての OpenVEX |
| `*.intoto.jsonl` | SLSA Build L3 プロベナンス |
| コンテナイメージ | GHCR と Docker Hub に公開され、digest で検証・固定 |
| Helm チャートのソース | `deploy/helm/olivares` からインストール。OCI 公開は未検証です（`publication-unverified`：このリポジトリから公開されたことはありません） |

## ワンコマンドの経路

リポジトリには `scripts/verify-release.sh` が同梱されており、チェーン全体を実行します:
`checksums.txt` に対する署名を検証し、すべての成果物の SHA-256 を再計算し、その後
SBOM、OpenVEX、SLSA の表明を検証します。

```bash
# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.9.0

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
scripts/verify-release.sh --key /path/to/your-cosign.pub
```

`--key` は、リリースワークフローのアイデンティティではなく公開鍵に対して署名を検証し、透明性ログを
無視します。これで証明されるのは、ファイルが対応する秘密鍵で署名されたことであり、プロジェクトが
公開したことではありません。その公開鍵は、検証するファイルとは別の経路で鍵の所有者から入手して
ください。

`--offline` が cosign 呼び出しから取り除くのは Rekor の照会だけで、検証がネットワーク不要になる
わけではありません。keyless 検証には引き続き Sigstore の信頼ルート材料が必要で、キャッシュ済みで
なければ cosign が取得します。また、スクリプトには `--trusted-root` オプションがありません。
SBOM と OpenVEX のバンドルの検証には `--key` を使っても信頼ルートが必要で、SLSA ステップは
オフラインオプションなしで `slsa-verifier` を実行します。

## 何をチェックするか、ステップごと

チェックを自分で実行したい場合、スクリプトが行うのは以下のとおりです:

1. **チェックサムに対する署名** — keyless で、プロジェクトの GitHub Actions アイデンティティ
   と OIDC issuer に対して検証されます:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **成果物の完全性** — ダウンロードしたすべての成果物が `checksums.txt` と一致しなければ
   なりません:

   ```bash
   sha256sum --check checksums.txt
   ```

3. **SBOM (SPDX) の表明:**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX の表明** (プロジェクトの到達可能性ベースの脆弱性ステートメント):

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **SLSA プロベナンス:**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## コンテナイメージを検証する

公開されたイメージについては、ダイジェストを解決し、GitHub Actions アイデンティティに
対して検証します (この経路は keyless でネットワークを必要とします):

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
slsa-verifier verify-image "$REF" \
  --source-uri github.com/olivaresai/olivares --source-tag <version>
```

イメージは常に **ダイジェストで** (`@sha256:…`) デプロイし、可変タグでは決してデプロイ
しないでください。

## エアギャップ環境では

**エアギャップバンドル** は運用者が自分で管理する cosign 鍵を使って作成し、対応する
`cosign.pub` がバンドルに含まれます。バンドルのスクリプトは Rekor を使わずに、Helm チャートと
保存されたイメージをその鍵で検証します。イメージが検証を通るのは、その鍵で作成された署名を持つ
場合だけです。検証対象のバンドル自身から読み取った鍵は、そのバンドルを認証しません。バンドルを
信頼する前に、鍵の所有者から別の経路で受け取った写しと比較してください。
[エアギャップ環境でインストールする](/how-to/air-gap-install/) を参照してください。

:::note[表明の利用可能性に関する正直な注記]
検証は、あるリリースが実際に公開した表明の分だけ完全になります。検証器は実行する各
ステップを報告します。リリースが成果物を省略している場合 (たとえば SBOM を添付しなかった
ビルド)、対応するステップにはチェックするものがありません。リリースワークフローは、標準の
ビルドに対して上記の SBOM、OpenVEX、SLSA の成果物を添付します。
:::

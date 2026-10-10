---
title: ダウンロードしたものを検証する
description: >-
  実行する前に、リリースの署名、SLSA プロベナンス、SBOM、OpenVEX の表明を検証する。
  インストーラーをそのままシェルにパイプしないこと。
---

> デプロイ用パッケージは Business チャネルで提供されます。ここでは公開状況は未検証です。ローカルチャートを使う前に、チャネルの手順でパッケージと発行者を検証してください。マニフェストの例では Business が提供する `business-install.yaml` を使います。エアギャップ環境へのインストールには Enterprise が必要です。


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

次のリリースは <!-- release -->`0.1`<!-- /release --> で、GitHub ではまだ公開されていません。以下のコマンドは予定されている成果物を示します。公開まではソースからビルドし、公開後も使用前に各成果物を検証してください。観測した公開状況は <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release --> に記録されています。

control plane はセキュリティ製品なので、リリースに対して最初にすべきことは、それが
**プロジェクトが公開したものであると証明すること** です。Olivares AI のリリースは、
暗号的に検証するために必要なすべてを同梱しています: チェックサムに対する署名、SLSA
プロベナンスの表明、そしてコンテナイメージに対して表明された SBOM (SPDX) と OpenVEX
ドキュメントが 1 つずつ — すべて **タグではなくダイジェストで** 参照されます。

:::danger[`curl | bash` は決して行わない]
インストーラーをシェルにパイプしないでください。成果物をダウンロードし、**検証し**、
その後でのみ実行してください。以下の手順がその方法です。
:::

## リリースに同梱されるもの

| 成果物 | 内容 |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | アーカイブ、パッケージ、インストーラー、`release-commit.txt`、`release-build-context.json` の SHA-256。cosign の署名と証明書付き |
| `*_<os>_<arch>.tar.gz` | リリースアーカイブ |
| `olivares.spdx.sbom.json` | リリースの SBOM (SPDX)。コンテナイメージから生成され、ダイジェストでイメージに表明 |
| `olivares.vex.openvex.json` | リリースの OpenVEX。ダイジェストでコンテナイメージに表明 |
| `*.intoto.jsonl` | SLSA Build L3 プロベナンス |
| コンテナイメージ | GHCR と Docker Hub に公開され、digest で検証・固定 |
| Helm チャートのソース | `./business-chart` からインストール。OCI 公開は未検証です（`publication-unverified`：このリポジトリから公開されたことはありません） |

26.10.1<!-- release-fixed --> までのリリースは、代わりにアーカイブごとの SBOM と OpenVEX のバンドル
(`*.sbom.sigstore.json`、`*.vex.sigstore.json`) を含みます。それ以降のリリースには含まれません。

## ワンコマンドの経路

リポジトリには `scripts/verify-release.sh` が同梱されており、チェーン全体を実行します:
`checksums.txt` に対する署名を検証し、すべての成果物の SHA-256 を再計算し、その後
26.10.1<!-- release-fixed --> までのリリースのアーカイブごとの SBOM と OpenVEX のバンドル (それ以降のリリースでは
この 2 ステップはスキップと報告されます) と SLSA プロベナンスを検証します。

<!-- release -->
```bash
# The verifier is in a source checkout of the release tag, not a release asset; running it
# trusts the checkout. Without one, INSTALL.md shows the cosign + sha256sum commands.
# Run it from the directory that holds the downloaded files.

# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
/path/to/olivares/scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
/path/to/olivares/scripts/verify-release.sh --source-tag 0.1

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
/path/to/olivares/scripts/verify-release.sh --key /path/to/your-cosign.pub
```
<!-- /release -->

`--key` は、リリースワークフローのアイデンティティではなく公開鍵に対して署名を検証し、透明性ログを
無視します。これで証明されるのは、ファイルが対応する秘密鍵で署名されたことであり、プロジェクトが
公開したことではありません。その公開鍵は、検証するファイルとは別の経路で鍵の所有者から入手して
ください。

`--offline` が cosign 呼び出しから取り除くのは Rekor の照会だけで、検証がネットワーク不要になる
わけではありません。keyless 検証には引き続き Sigstore の信頼ルート材料が必要で、キャッシュ済みで
なければ cosign が取得します。また、スクリプトには `--trusted-root` オプションがありません。
アーカイブごとの SBOM と OpenVEX のバンドル (26.10.1<!-- release-fixed --> までのリリース) の検証には `--key` を使っても信頼ルートが必要で、SLSA ステップは
オフラインオプションなしで `slsa-verifier` を実行します。

## 何をチェックするか、ステップごと

チェックを自分で実行したい場合、スクリプトが行うのは以下のとおりです:

1. **チェックサムに対する署名** — keyless で、プロジェクトの GitHub Actions アイデンティティ
   と OIDC issuer に対して検証されます:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **成果物の完全性** — ダウンロードしたすべての成果物が `checksums.txt` と一致しなければ
   なりません:

   ```bash
   # checksums.txt lists all release artifacts; skip files you did not download.
   # Before use, ensure each downloaded artifact is listed and reports OK.
   # GNU sha256sum fails if no listed file is present or a checksum mismatches.
   sha256sum --check --ignore-missing checksums.txt
   ```

3. **SBOM (SPDX) の表明** — アーカイブごとのバンドル。26.10.1<!-- release-fixed --> までのリリースのみ。それ以降の
   リリースはコンテナイメージに表明された SBOM を 1 つ含みます。下の「コンテナイメージを検証する」を参照してください。

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX の表明** (プロジェクトの到達可能性ベースの脆弱性ステートメント) — アーカイブごとの
   バンドル。26.10.1<!-- release-fixed --> までのリリースのみ。それ以降のリリースはコンテナイメージに表明された OpenVEX を
   1 つ含みます。下の「コンテナイメージを検証する」を参照してください。

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
対して検証します (この経路は keyless でネットワークを必要とします)。`spdxjson` と `openvex` の
表明は、ダイジェストでイメージに表明されたリリースの SBOM (`olivares.spdx.sbom.json`) と
OpenVEX (`olivares.vex.openvex.json`) です:

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
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

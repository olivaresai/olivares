---
title: 验证你下载的内容
description: >-
  在运行之前，先验证一个发布的签名、SLSA 来源证明、SBOM 和 OpenVEX 证明 — 在线（无密钥）或完全离线（基于密钥）。绝不要把安装脚本直接管道送入
  shell。
---

控制平面（control plane）是一款安全产品，因此你拿到一个发布后首先应做的，就是 **证明它确实是项目所发布的那一个**。Olivares AI 的发布会随附你做加密验证所需的一切：对校验和的签名、一份 SLSA 来源证明、一份 SBOM（SPDX + CycloneDX），以及一份 OpenVEX 证明 — 全部 **按 digest 引用，绝不按标签**。

:::danger[绝不要 `curl | bash`]
不要把安装脚本管道送入 shell。先下载产物，**验证它们**，然后才运行。下面的步骤就是做法。
:::

## 一个发布随附哪些内容

| 产物 | 它是什么 |
|---|---|
| `checksums.txt`（+ `.sig`、`.pem`） | 每个产物的 SHA-256，附带 cosign 签名和证书 |
| `*_<os>_<arch>.tar.gz` | 发布归档文件 |
| `*.sbom.sigstore.json` | 作为已签名 in-toto 证明的 SBOM（SPDX） |
| `*.vex.sigstore.json` | 作为已签名 in-toto 证明的 OpenVEX |
| `*.intoto.jsonl` | SLSA Build L3 来源证明 |
| 容器镜像 + Helm chart | 发布时发布到 registry，按 digest 固定 |

## 单命令路径

仓库提供了 `scripts/verify-release.sh`，它会运行完整链条：验证对 `checksums.txt` 的签名，重新计算每个产物的 SHA-256，然后验证 SBOM、OpenVEX 和 SLSA 证明。

```bash
# Default: keyless (Sigstore). Needs network access to the transparency log (Rekor).
scripts/verify-release.sh

# Key-based (air-gap friendly): verify against the project's public key.
scripts/verify-release.sh --key cosign.pub

# Fully offline: no Rekor / no transparency-log network at all.
scripts/verify-release.sh --key cosign.pub --offline

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.8.0
```

使用 `--offline`（或在提供了密钥时）脚本会向每次 cosign 调用添加
`--insecure-ignore-tlog`，因此不会使用任何 Sigstore/Rekor 网络 — 这就是面向断网环境的路径。

## 它逐步检查什么

如果你更愿意自己运行这些检查，脚本所做的就是：

1. **对校验和的签名** — 无密钥，对照项目的 GitHub
   Actions 身份和 OIDC issuer 进行验证：

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **产物完整性** — 每个下载的产物都必须与 `checksums.txt` 匹配：

   ```bash
   sha256sum --check checksums.txt
   ```

3. **SBOM（SPDX）证明：**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX 证明**（项目基于可达性的漏洞声明）：

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **SLSA 来源证明：**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## 验证容器镜像

对于已发布的镜像，解析其 digest 并对照 GitHub Actions 身份进行验证（此路径无密钥且需要网络）：

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

始终 **按 digest** 部署镜像（`@sha256:…`），绝不按可变标签。

## 在气隙环境中

如果你完全无法访问网络，请使用 **气隙 bundle**，它携带一个公钥并完全离线（无 Rekor）地验证一切。参阅
[在气隙环境中安装](/how-to/air-gap-install/)。

:::note[关于证明可用性的诚实说明]
验证的完整程度只取决于某个发布实际发布了哪些证明。验证器会报告它运行的每个步骤；如果某个发布省略了某个产物（例如某次构建未附带 SBOM），相应步骤就无内容可检查。发布工作流会为标准构建附带上面列出的 SBOM、OpenVEX 和 SLSA 产物。
:::

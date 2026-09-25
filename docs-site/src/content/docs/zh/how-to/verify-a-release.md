---
title: 验证你下载的内容
description: >-
  在运行之前，先验证一个发布的签名、SLSA 来源证明、SBOM 和 OpenVEX 证明。绝不要把安装脚本直接管道送入
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
| 容器镜像 | 发布到 GHCR 和 Docker Hub，按 digest 验证并固定 |
| Helm chart 源码 | 从 `deploy/helm/olivares` 安装；其 OCI 发布未经验证（`publication-unverified`：从未从本仓库发布） |

## 单命令路径

仓库提供了 `scripts/verify-release.sh`，它会运行完整链条：验证对 `checksums.txt` 的签名，重新计算每个产物的 SHA-256，然后验证 SBOM、OpenVEX 和 SLSA 证明。

```bash
# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.9.0

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
scripts/verify-release.sh --key /path/to/your-cosign.pub
```

`--key` 使用公钥而不是发布工作流的身份来验证签名，并忽略透明日志。它证明文件是用对应的私钥签名的，但不能证明这些文件由项目发布。请通过与待验证文件分离的渠道，从密钥所有者处获取该公钥。

`--offline` 只从 cosign 调用中去掉 Rekor 查询，并不会让验证无需网络。无密钥验证仍然需要 Sigstore 信任根材料，尚未缓存时由 cosign 获取；而且脚本没有 `--trusted-root` 选项。即使使用 `--key`，验证 SBOM 和 OpenVEX bundle 也需要信任根；SLSA 步骤运行 `slsa-verifier` 时不带任何离线选项。

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

**气隙 bundle** 由运维人员使用自己控制的 cosign 密钥构建，bundle 中包含对应的 `cosign.pub`。bundle 的脚本在不使用 Rekor 的情况下，用该密钥验证 Helm chart 和已保存的镜像；只有带有该密钥所做签名的镜像才能通过。从被验证的 bundle 本身读取的密钥不能认证该 bundle：在信任 bundle 之前，请将它与密钥所有者通过独立渠道交给你的副本进行比较。参阅
[在气隙环境中安装](/how-to/air-gap-install/)。

:::note[关于证明可用性的诚实说明]
验证的完整程度只取决于某个发布实际发布了哪些证明。验证器会报告它运行的每个步骤；如果某个发布省略了某个产物（例如某次构建未附带 SBOM），相应步骤就无内容可检查。发布工作流会为标准构建附带上面列出的 SBOM、OpenVEX 和 SLSA 产物。
:::

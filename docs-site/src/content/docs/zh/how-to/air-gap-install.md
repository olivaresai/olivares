---
title: 在隔离网络环境中安装
description: >-
  把一个已签名的发布捆绑包带过隔离边界，完全离线地验证每个镜像和 Helm chart，
  按摘要将它们镜像到私有 registry 并安装——断网一侧不发起任何外呼。
---

Olivares AI 是**自托管优先且隔离网络就绪的**。本指南将一个已签名的发布物带过
一道隔离边界，且**在断网一侧没有任何网络**：你针对一个公钥离线验证每个镜像和
Helm chart，**按摘要**将它们镜像到你的私有 registry，然后安装。产品在**启动时
不会发起任何强制性外呼**，因此隔离边界内不会有任何流量到达互联网。唯一会访问厂商端点的命令是
`olivares upgrade`，`--endpoint` 或 `--bundle` 可将其指向你自己的镜像。

整个流程分为两侧：

1. **在线，一次性** —— 由维护者构建一个自包含的捆绑包。
2. **在隔离边界内** —— 你离线验证它并把它镜像到你的 registry。

本页记录如何**使用**该捆绑包和随附的脚本；它不会重建发布流水线。

## 1. 构建捆绑包（在线，一次性）

在一台联网机器上，`scripts/airgap-bundle.sh` 会拉取每个**按摘要固定**的镜像，
打包并签名 Helm chart，收集 SBOM/OpenVEX/溯源，然后输出一个带有 `VERIFY.md`
的单一 tarball：

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

镜像通过其官方坐标（`docker.io/olivaresai/olivares`）从 Docker Hub 拉取；相同的内容也位于
`ghcr.io/olivaresai/olivares`，按摘要完全一致，如果你倾向于从那里镜像也可以。Docker Hub 对**匿名**
拉取限速，而 ghcr.io 对公共镜像不限速，这在未认证的构建主机上很有用。

:::caution[SBOM/VEX/溯源是被提供的，而非生成的]
打包器**尽力地从环境变量**（`OLIVARES_SBOM_FILES`、`OLIVARES_VEX_FILES`、
`OLIVARES_PROV_FILES`）把 SBOM、OpenVEX 和溯源复制进捆绑包。如果这些未设置，
捆绑包中的 `sbom/`、`vex/` 和 `prov/` 目录将为空——请设置它们，以便你的断网
站点收到这些证明。
:::

### 捆绑包包含的内容

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

捆绑包还携带 `airgap-mirror.sh` 和 `verify-release.sh` 的副本，因此断网一侧
无需从网络获取任何东西。

## 2. 在隔离边界内验证并镜像

在断网一侧你只需要 `cosign`、`crane`、`helm` 和 `tar`——以及一个可达的
**私有 registry**。无需互联网。

### 离线验证每个镜像（无透明日志）

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` 会跳过 Sigstore 的在线透明日志；信任来自捆绑包内的
`cosign.pub`。（这与无密钥的 `--offline` 标志*不同*——在密钥模式下，离线信任
根就是该公钥。）

### 离线验证 Helm chart

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### 按摘要镜像到你的私有 registry

`scripts/airgap-mirror.sh` 会离线验证每个镜像，将其载入你的 registry，并
**按摘要重新固定**以确认摘要在镜像过程中得以保留（它使用 `crane` 和
`cosign load`——**而非** `oras`）：

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### 按摘要安装，绝不按标签

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

始终从 `digests.txt` 中的**摘要**安装，绝不要用可变标签——摘要是不可变的，
也正是你验证过的内容。

## 隔离边界内不会向外发起任何调用

> 引擎在**启动时不会发起任何强制性外呼**（默认绑定环回地址），因此隔离边界内
> 不会有任何流量到达互联网。唯一会访问厂商端点的命令是 `olivares upgrade`，
> `--endpoint` 或 `--bundle` 可将其指向你自己的镜像。

许可证以**离线**方式验证（一个 Ed25519 签名，没有许可证服务器），并且一旦捆绑
包跨过隔离边界，上述任何验证或安装步骤都不会触及互联网。不存在需要关闭的
默认遥测外呼。

联系我们发生在**在线**一侧，这是设计使然：构建捆绑包会下载发布物，而在商业环境
中，订阅就是获取附加组件及其更新和补丁的凭据。这正是 SUSE/Novell 模式——隔离
网络环境由一个仍然携带同一授权的本地镜像来提供服务。参见
[自托管](/zh/how-to/self-hosting/)。

:::note[容器与二进制的监听默认值]
直接运行时，二进制默认绑定**环回地址**。发布**容器镜像**的默认命令会在容器
内部绑定 `0.0.0.0`，以便你可以用你的 ingress/service 置于其前——这是一个容器
内绑定，而非外呼。请为你的部署显式设置监听地址。
:::

## FIPS / STIG 变体

存在加固的构建变体（一个链接 CMVP 验证过的 Go 加密模块的 FIPS 模式构建，以及
一个面向 STIG 的镜像）。这些是 **v1 之后**的内容，并附带各自的诚实账本——
尤其是，**不声明任何 FedRAMP/DoD ATO**，且只应将经过具体验证的模块版本表述为
已验证。请将它们视为可用但尚非 v1 的内容，而非一项已认证的产品。

## 另见

- [验证你下载的内容](/zh/how-to/verify-a-release/) —— 非隔离网络的验证链
  （签名、SBOM、OpenVEX、SLSA）。
- [自托管控制平面](/zh/how-to/self-hosting/) —— 单二进制、Compose 与
  Kubernetes 路径及其安全默认值。

<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — 企业 AI 的唯一可信事实源" width="720"></a>

**语言:** [English](./README.md) · [Español](./README.es.md) · **简体中文** · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**在你自己的基础设施上，运行并治理你已经在用的 AI。**

[它是什么](#它是什么) · [它做什么](#它做什么) · [安装](#安装) · [快速上手](#快速上手) · [控制台](#控制台一览) · [版本](#版本与定价) · [文档](#文档) · [安全](#安全) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta。** 26.10.0 提供签名归档、原生软件包和容器镜像。[诚实与限制](docs-site/src/content/docs/start/honesty-and-limits.md)列出了目前可运行、按需运行和仍处于设计阶段的内容。

## 它是什么

Olivares AI 是面向 AI 智能体的自托管控制平面：一个内置控制台的 Go 二进制文件。它为智能体提供上下文、资源访问和受管会话，为你提供权限、策略、预算和证据。没有强制遥测，并支持离线（air-gapped）安装。

Claude Code 通过其 `PreToolUse`/`PostToolUse` 钩子、托管设置以及控制台启动和停止接入。官方 Codex 和 Grok CLI 作为受管会话运行。gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes 以及 Ollama 等自托管端点属于连接器；每个连接器都说明它能强制执行什么、只能观察什么。

## 它做什么

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="读写访问图的动态示意：左侧是 agent、会话和身份，右侧是它们到达的资源，读取为蓝色，写入为橙色，一次从未被允许的已观察写入被标为漂移发现。">
<br><sub><b>访问图</b> — 每个 agent 在整个 estate 中读写什么，允许对照观察。</sub>
</div>

- **看见。** 连接器观察到的智能体、会话、模型、MCP 服务器、工具和身份的清单；带有“允许与实际观察”**漂移**视图的读写**访问地图**；实时会话、编排图、健康状况和 SLA。无法分类的访问显示为 `unknown`。
- **推进工作。** 带负责人、依赖关系、验收标准和决策的工作项；隔离租约，确保两个智能体不会同时持有同一项工作；从控制台启动、接入、中断和停止 Claude Code、Codex 和 Grok 会话；通过 A2A 委派给经过授权的对等方。
- **治理并强制执行。** Cedar 授权引擎和**四个 deny-closed 执行点**：Claude Code 钩子、内联 `/v1/messages` 推理代理、MCP `tools/call` 关卡和 A2A 委派关卡。未经授权的操作会在执行前被阻止、挂起等待两人审批，或被改写。预算可拒绝或限制支出，紧急访问（break-glass）需要两人，**终止开关**在故障时关闭。
- **受治理地提供数据。** SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL 以及限定在根目录内的文件系统为受治理的检索提供数据；访问权限在检索时检查。
- **证明。** 哈希链式、Ed25519 签名的审计账本；映射到 **26 个框架目录**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR 等）的密封证据，属于自评估的控制族，而非认证；推送到 SIEM 和 ITSM（CEF、LEEF、syslog、OTLP、OCSF）；WebAuthn/FIDO2、PIV/CAC、SSO、SCIM、BYOK/CMEK 以及经过验证的删除权，按部署配置。

**31 个模块**、一个控制台、**159 个集成**，由 [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) 从代码中统计。细分见 [`connectors/README.md`](connectors/README.md)，各模块及其成熟度见[模块目录](docs-site/src/content/docs/reference/modules/overview.md)。

## 安装

选择一种方法。完成后，`olivares quickstart` 会输出控制台 URL 和一次性设置令牌。发行版使用 cosign 签名，并附带 SLSA 来源证明和 SBOM；下面每种方法都会在安装前验证，`scripts/verify-release.sh` 可检查手动下载（cosign 和 SHA-256，[方法](INSTALL.md#verifying-a-release)）。引擎以 HTTPS 启动，没有默认凭据，并使用一次性设置令牌。

**1 · 一条命令，Linux 和 macOS。** 安装程序会检测操作系统和架构，验证签名校验和与归档的 SHA-256，只安装二进制文件，从不运行 `sudo`。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

添加 `--user` 以使用用户服务（systemd 用户单元或 LaunchAgent），或在有特权的 shell 中使用 `--system --start` 运行脚本以安装系统服务。手动方式和各操作系统矩阵：[`INSTALL.md`](INSTALL.md)。

**2 · Docker。** 多架构、distroless、非 root。端口发布在主机的所有接口上；如需仅本地访问，在 `-p` 映射前加上 `127.0.0.1:`。

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` 是同一镜像；生产环境请按摘要固定版本。FIPS 和 STIG 变体：[`INSTALL.md`](INSTALL.md#docker)。

**3 · Docker Compose。** 加固的堆栈：单节点 SQLite，可选 Postgres 和备份。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes。** 使用本仓库中的 Helm chart，或不使用 Helm 的扁平清单。该 chart 尚未发布到 OCI（`publication-unverified`）。

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux 软件包。** [发布页面](https://github.com/olivaresai/olivares/releases/tag/26.10.0)上的 `.deb`、`.rpm` 和 `.apk`：二进制文件、示例 env 文件、无登录的 `olivares` 用户和加固的服务单元。安装时不会启动服务。

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew。** 适用于 macOS 和 Linux，按签名校验和检查。

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · 从源码构建。** Go 1.26+、[Task](https://taskfile.dev) 和 pnpm。

```sh
task build && ./bin/olivares quickstart
```

**离线环境：** 打包签名的镜像、chart 和验证材料，并使用 `scripts/verify-release.sh --key … --offline` 离线验证（[指南](docs-site/src/content/docs/how-to/air-gap-install.md)）。**Windows** 尚无原生构建：请使用 Linux 容器或 WSL2（[计划](INSTALL.md#windows)）。升级与回滚：[指南](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)。

## 快速上手

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

演示密码是公开的：不要将演示用于真实数据。[完整快速上手](docs-site/src/content/docs/start/quickstart.md)会接入真实的 pgAudit 数据源。

## 控制台一览

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="访问图：每个 agent 在整个 estate 中读写什么，左侧是来源，右侧是资源。"></picture><br><sub><b>访问图</b> — 左侧来源，右侧资源，按颜色区分读写。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小权限漂移：叠加在访问图上的意外访问和未使用授权。"></picture><br><sub><b>最小权限漂移</b> — 已观察但未允许，以及无人使用的授权。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="从控制台创建、附加并治理的 Claude Code 会话。"></picture><br><sub><b>会话</b> — 从控制台创建、附加并治理会话，无需 SSH。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="工作：跨会话的持久工作项与决策待办。"></picture><br><sub><b>工作</b> — 跨会话的持久待办：工作项、所有权、验收、决策。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="安全与取证：护栏发现、异常队列和可检测篡改的取证。"></picture><br><sub><b>安全与取证</b> — 护栏发现、异常、可检测篡改的取证。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps：模型支出、令牌用量、预算和 run-rate 预测。"></picture><br><sub><b>FinOps</b> — 按模型和 agent 的支出、拒绝或限制的预算、run-rate。</sub> |

这些画面来自运行中二进制文件上的演示环境。全部画面：[控制台参考](docs-site/src/content/docs/reference/console.md)。

## 版本与定价

Community 是 AGPL-3.0 下的完整产品，用户数不限，并包含全部四个 deny-closed 执行点。Business 和 Enterprise 增加仅通过 `-tags enterprise` 构建的商业代码；Community 中的任何功能都不会被移除或限制。

| 版本 | 价格 | 包含内容 |
|---|---|---|
| **Community** | 免费，AGPL-3.0 | 完整的自托管产品。用户数不限，一个活动身份提供方。 |
| **Business** | 每月 129 美元或每年 1,290 美元 | 商业许可、签名发布渠道、工作时间内的邮件支持，以及 **Regulated Operations**、**AI Runtime Security**、**Compliance Packs** 和 **Identity & Scale**。用户数不限；一个法律实体；最多两个生产部署，每个附带一个预发布（staging）部署；最多五个活动身份提供方。 |
| **Enterprise** | 合同 | 更多法律实体、部署和身份提供方，离线镜像，定制 LTS 和支持条款，按年度订单签署。 |

购买条款：[olivares.ai/pricing](https://olivares.ai/pricing)。哪些开源、哪些商业：[`LICENSING.md`](LICENSING.md)。

## 架构

一个静态 Go 二进制文件内置控制台，提供四种接口：REST API、稳定核心的 gRPC 镜像、`olivares` CLI 和 Terraform provider。采集器运行在你的基础设施内。存储使用带行级安全的 SQLite 或 Postgres，由存储 API 执行一次，再由 Postgres 执行一次。包括工作平面在内的详情：[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## 文档

[docs.olivares.ai](https://docs.olivares.ai) — 经过测试的安装教程（单节点、Docker Compose、Kubernetes/Helm、air-gapped）、带真实控制台截图的连接器指南、手册（deny-closed 策略、预算、审批、kill-switch 演练、SIEM 推送）、API 参考和术语表。从[什么是 Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md)开始。站点上：[产品](https://olivares.ai/product) · [方案](https://olivares.ai/solutions) · [工作原理](https://olivares.ai/how-it-works) · [架构](https://olivares.ai/architecture) · [安全](https://olivares.ai/security) · [信任](https://olivares.ai/trust) · [对比](https://olivares.ai/compare) · [演示](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [状态](https://olivares.ai/status) · [路线图](https://olivares.ai/roadmap) · [品牌](https://olivares.ai/brand) · [新闻](https://olivares.ai/press)。发布：[GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

## 安全

请通过 [`SECURITY.md`](SECURITY.md) 私下报告漏洞，不要提交公开 issue。访问地图只存储边，不存储内容，打开它会被审计。许可证验证离线进行；AGPL 核心不发起任何许可证调用。安全公告：[`docs/security-advisories.md`](docs/security-advisories.md)；供应链证据：[`docs/openssf-badge.md`](docs/openssf-badge.md)。

## 社区

[`CONTRIBUTING.md`](CONTRIBUTING.md)（设置、DCO/CLA、SPDX、连接器边界） · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog，CalVer `YY.M.PATCH`）。

## 许可证

`core/`、`modules/` 和 `web/` 采用 **AGPL-3.0-only**；`sdk/`、`connectors/` 和 `clients/` 采用 **Apache-2.0**，连接器从不导入引擎。商业代码仅通过 `-tags enterprise` 构建，不在本仓库中。商业许可：`enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md)。贡献需要 DCO 签署（`git commit -s`）和 [CLA](CLA.md)。

> **不提供担保。** 本软件**按原样**提供，**不提供任何形式的担保**，**不对数据丢失、业务中断或利润损失承担责任**。适用 AGPL-3.0-only 第 15–16 条、Apache-2.0 第 7–8 条以及本项目的补充条款 — [`DISCLAIMER.md`](DISCLAIMER.md)。

## 支持本项目

可以通过 GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) 或 [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — 或在 Ko-fi 上一次性赞助本项目。赞助不是支持合同（[`SUPPORT.md`](SUPPORT.md)）；希望署名的赞助者列在 [`SUPPORTERS.md`](SUPPORTERS.md) 中。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>企业 AI 的唯一可信事实源。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>

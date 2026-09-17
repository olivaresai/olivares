<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — 企业 AI 的唯一可信事实源" width="720"></a>

**语言:** [English](./README.md) · [Español](./README.es.md) · **简体中文** · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**运行并治理你已经在用的 AI——在你自己的基础设施上，只有一个可信事实源。**

[它是什么](#它是什么) · [它做什么](#它做什么) · [安装](#安装) · [快速上手](#快速上手) · [控制台](#控制台一览) · [版本](#版本与定价) · [文档](#文档) · [安全](#安全) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.0](https://img.shields.io/badge/release-v26.9.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**，处于活跃开发中。**v26.9.0** 随附签名归档、原生软件包和容器镜像。哪些能力今天可运行、哪些按需提供、哪些仍处于设计阶段，见[诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)。

## 它是什么

你今天的 AI estate 是编程 agent、MCP 服务器、模型端点、服务账户和计划任务，分散在从未成为同一系统的机器上。没有人能从同一个地方说出：什么在运行、谁启动了它、它触及了什么、花了多少、谁同意了。

Olivares AI 是**一个自托管 Go 二进制文件，控制台包含在内**，把这座 estate 收拢在一起：它给 AI 工作所需（上下文、资源访问、受管会话），并给你权限、策略、预算和证据来运营它。自托管，无强制遥测，支持 air-gapped 安装。

Claude Code 在最深一层集成（`PreToolUse`/`PostToolUse` 钩子、受管设置、从控制台启动和停止）；官方 Codex 和 Grok CLI 是一等会话驱动；gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes 以及 Ollama 等自托管端点是连接器，各自声明能强制什么、只能观察什么。AGPL 构建就是完整产品，从不从内部做功能封顶；任何套餐都不按用户计数。

## 它做什么

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="读写访问图的动态示意：左侧是 agent、会话和身份，右侧是它们到达的资源，读取为蓝色，写入为橙色，一次从未被允许的已观察写入被标为漂移发现。">
<br><sub><b>访问图</b> — 每个 agent 在整个 estate 中读写什么，允许对照观察。</sub>
</div>

- **看见它。** 清点每个已发现的 agent、会话、模型、MCP 服务器、工具和身份；一张读写**访问图**，带 Permitted-vs-Observed **漂移**视图；实时会话、编排图、健康状况和 SLA。它看不到的内容会标记为 `unknown`，绝不猜测。
- **运行工作。** 带有所有权、依赖关系、验收标准和决策的持久工作项；围栏租约确保两个 agent 无法同时持有同一项工作；从控制台启动、附加、中断和停止 Claude Code、Codex 和 Grok 会话；通过 A2A 向获授权对等方委派。
- **治理并执行它。** 一个 Cedar 授权引擎，以及**四个 deny-closed 执行点**——Claude Code 钩子、内联 `/v1/messages` 推理代理、MCP `tools/call` 关卡和 A2A 委派关卡——使未授权操作在运行前被阻断、挂起等待双人审批，或被改写。预算可以拒绝或限制支出，破玻璃机制实行双人控制，还有一个失败即关闭的 estate **kill-switch**。
- **受治理地供给它。** 内容源（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL，以及限定根目录的文件系统）进入受治理检索，检索时以 deny-closed 强制许可级别。
- **证明它。** 一份哈希链、Ed25519 签名的审计台账；映射到 **26 个框架目录**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR……）的封存证据——自行评估的控制族，并非认证；SIEM/ITSM 推送（CEF/LEEF/syslog/OTLP/OCSF）；WebAuthn/FIDO2、PIV/CAC、SSO、SCIM、BYOK/CMEK 以及经验证的被遗忘权，均按部署配置。

**30 个模块**、一个控制台、**158 项集成**——计数从代码推导，并由 [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) 在每次推送时强制校验；明细见 [`connectors/README.md`](connectors/README.md)，每个模块及其成熟度见[模块目录](docs-site/src/content/docs/reference/modules/overview.md)。

## 安装

选一种方法：一条命令完成安装，然后 `olivares quickstart` 打印控制台 URL 和一次性设置令牌。每个发布版本均经 cosign 签名，带 SLSA 来源和 SBOM；下列每条路径都会在安装前校验，`scripts/verify-release.sh` 检查手动下载（cosign + SHA-256，[方法](INSTALL.md#verifying-a-release)）。引擎**默认即安全**：绑定 loopback、首次启动即 HTTPS、没有默认凭据、首次启动时打印一次性设置令牌。

**1 · 一条命令，Linux 和 macOS** — 经验证的安装器：检测操作系统和架构，校验已签名校验和与归档 SHA-256，只安装二进制文件，从不运行 `sudo`。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

用户服务（systemd 用户单元或 LaunchAgent）加 `--user`；系统服务则从特权 shell 运行已验证脚本并加上 `--system --start`。更想先下载、校验再亲手运行？手动二进制路径和按操作系统的矩阵：[`INSTALL.md`](INSTALL.md)。

**2 · Docker** — 多架构、distroless、非 root；主机端口映射使其仅限 loopback。

```sh
docker run -d --name olivares -p 127.0.0.1:8443:8443 -p 127.0.0.1:8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` 是 digest 相同的镜像；生产环境请按 digest 固定。FIPS 和 STIG 镜像变体：[`INSTALL.md`](INSTALL.md#docker)。

**3 · Docker Compose** — 加固堆栈，SQLite 单节点，可选 Postgres 和备份。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — 树中的 Helm chart，或一份不含 Helm 的扁平清单；chart 尚未发布到 OCI 仓库。

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux 软件包** — 来自[发布页面](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)的 `.deb`、`.rpm`、`.apk`：二进制、示例 env 文件、无登录的 `olivares` 用户和加固单元；服务不会替你启动。

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS 和 Linux，对照已签名校验和检查。

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · 从源码构建** — Go 1.26+、[Task](https://taskfile.dev)、pnpm。

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**：打包已签名镜像、chart 和校验材料，并用 `scripts/verify-release.sh --key … --offline` 离线校验（[指南](docs-site/src/content/docs/how-to/air-gap-install.md)）。**Windows** 尚未构建：运行 Linux 容器或 WSL2（[计划](INSTALL.md#windows)）。升级与回滚：[指南](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)。

## 快速上手

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

演示种子仅供学习（源码树中的公开密码）：切勿指向真实数据。CI 用 `task smoke:quickstart` 走同一路径，并断言访问图与漂移计数（20 个节点 / 13 条边，8 次意外访问和 2 项未使用授权）。[完整快速上手](docs-site/src/content/docs/start/quickstart.md) 会接入真实的 pgAudit 连接器。

## 控制台一览

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="访问图：每个 agent 在整个 estate 中读写什么，左侧是来源，右侧是资源。"></picture><br><sub><b>访问图</b> — 左侧来源，右侧资源，按颜色区分读写。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小权限漂移：叠加在访问图上的意外访问和未使用授权。"></picture><br><sub><b>最小权限漂移</b> — 已观察但未允许，以及无人使用的授权。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="从控制台创建、附加并治理的 Claude Code 会话。"></picture><br><sub><b>会话</b> — 从控制台创建、附加并治理会话，无需 SSH。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="工作：跨会话的持久工作项与决策待办。"></picture><br><sub><b>工作</b> — 跨会话的持久待办：工作项、所有权、验收、决策。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="安全与取证：护栏发现、异常队列和可检测篡改的取证。"></picture><br><sub><b>安全与取证</b> — 护栏发现、异常、可检测篡改的取证。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps：模型支出、令牌用量、预算和 run-rate 预测。"></picture><br><sub><b>FinOps</b> — 按模型和 agent 的支出、拒绝或限制的预算、run-rate。</sub> |

每张静帧都是正在运行的二进制所提供的已播种演示 estate 的截图。完整屏幕图：[控制台参考](docs-site/src/content/docs/reference/console.md)。

## 版本与定价

AGPL 构建就是整个平台，从不从内部做功能封顶。商业 add-on 是叠加上去的附加代码，从来不是被拿掉的功能；订阅是下载已签名模块包的凭证。自托管引擎中的用户账户不受限，全部**四个 deny-closed 执行点**均开放。

| 版本 | 面向谁 | 增加什么 |
|---|---|---|
| **Community** | 任何人。免费，AGPL-3.0，用户数不限。 | 完整产品，自托管。核心没有许可证闸门。 |
| **Business** | 正在采用它的组织。按部署计价，从不按席位。 | 服务和可选包，不是核心功能：商业许可证、受维护的签名发布通道、工作时间邮件支持，以及四个可选 add-on：**Regulated Operations**、**Compliance Packs**、**AI Runtime Security** 和 **Identity & Scale**（含官方工具的会话驾驶舱）。四个合在一起即 **Business Max**。 |
| **Cloud** | 希望同一平面由他人代为运行的团队，预付费，共享基础设施。 | 带已公布上限的托管控制平面。没有 Cloud 试用；免费选项仍是自托管 Community。 |
| **Enterprise** | 受监管、多实体、大规模 estate。 | 一份合同，通过邮件约定，并在年度订单上签署。 |

价格、add-on 矩阵和购买条款：[olivares.ai/pricing](https://olivares.ai/pricing)。开放/商业/规划矩阵：[`LICENSING.md`](LICENSING.md)。

## 架构

一个静态 Go 二进制嵌入控制台，并暴露四个界面：REST API（主）、稳定核心的聚焦 gRPC 镜像、`olivares` CLI 和 Terraform provider。采集器在你的基础设施内运行；存储为 SQLite 或带行级安全的 Postgres，先在存储 API 强制一次，再由 Postgres 强制一次。全貌（含工作平面）：[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## 文档

[docs.olivares.ai](https://docs.olivares.ai) — 经过测试的安装教程（单节点、Docker Compose、Kubernetes/Helm、air-gapped）、带真实控制台截图的连接器指南、手册（deny-closed 策略、预算、审批、kill-switch 演练、SIEM 推送）、API 参考和术语表。从[什么是 Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md)开始。站点上：[产品](https://olivares.ai/product) · [方案](https://olivares.ai/solutions) · [工作原理](https://olivares.ai/how-it-works) · [架构](https://olivares.ai/architecture) · [安全](https://olivares.ai/security) · [信任](https://olivares.ai/trust) · [对比](https://olivares.ai/compare) · [演示](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [状态](https://olivares.ai/status) · [路线图](https://olivares.ai/roadmap) · [品牌](https://olivares.ai/brand) · [新闻](https://olivares.ai/press)。发布：[GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

## 安全

通过 [`SECURITY.md`](SECURITY.md) 私下报告漏洞，切勿作为公开 issue。引擎以读为先、数据最小化：访问图存储边而非载荷，打开它是一次被记录的操作。校验许可证从不呼叫我们；AGPL 核心不做任何许可证调用。公告流程：[`docs/security-advisories.md`](docs/security-advisories.md)；供应链证据：[`docs/openssf-badge.md`](docs/openssf-badge.md)。

## 社区

[`CONTRIBUTING.md`](CONTRIBUTING.md)（设置、DCO/CLA、SPDX、连接器边界） · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog，CalVer `vYY.M.PATCH`）。

## 许可证

`core/`、`modules/` 和 `web/` 为 **AGPL-3.0-only**；`sdk/`、`connectors/` 和 `clients/` 为 **Apache-2.0**，连接器从不导入引擎。商业 add-on 独立、可选且闭源——仅以 `-tags enterprise` 构建，从不出现在本仓库；商业许可：`enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md)。贡献需要 DCO 签署（`git commit -s`）和 [CLA](CLA.md)。

> **无担保，无责任。** 软件按**现状**提供，**不提供任何形式的担保**，**不对数据丢失、业务中断或利润损失承担责任**。在控制平面上这不是套话：一次错误配置可能阻断正当工作，或放行你本想拦住的东西。适用 AGPL-3.0-only §§15–16、Apache-2.0 §§7–8 以及本项目的补充条款 — [`DISCLAIMER.md`](DISCLAIMER.md)。

## 支持本项目

核心免费，并将保持免费；让每次发布都经过签名、校验并保持最新，是持续的工作。通过 GitHub Sponsors 赞助 — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) 或 [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — 或在 Ko-fi 一次性支持。赞助不是支持合同（[`SUPPORT.md`](SUPPORT.md)）；希望具名的赞助者列于 [`SUPPORTERS.md`](SUPPORTERS.md)。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>企业 AI 的唯一可信事实源。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>

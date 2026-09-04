<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — 企业 AI 的唯一可信事实源" width="720"></a>

**语言:** [English](./README.md) · [Español](./README.es.md) · **简体中文** · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**集成、管理并保护你所运行的 AI——由一个自托管二进制文件统一完成。**

[安装](#install) · [快速上手](#quickstart) · [示例](examples/) · [文档](#documentation) · [安全](#security) · [贡献](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**，处于活跃开发中。首个带标签的发布版本 **v26.8.0** 随附签名归档、原生软件包和容器镜像。在 1.0 之前，API 和模块界面仍可能变化；哪些能力今天可运行、哪些按需提供，以及哪些仍处于设计阶段，均在[诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)中说明，并按模块列于[模块目录](docs-site/src/content/docs/reference/modules/overview.md)。

## 什么是 Olivares AI

你现在运行的是一套 estate——编程 agent、MCP 服务器、模型端点、服务账户、计划任务——分散在从未构成同一个系统的机器上。Olivares AI 是将其整合在一起的单一自托管 Go 二进制文件，内含控制台：它为 AI 提供工作所需的一切（上下文、资源访问、受管会话），并为你提供权限、策略、预算和审计证据，让你知道正在运行什么、谁启动了它、它触及了什么、成本是多少，以及谁同意了这一切。

**按设计支持多提供方。** Claude Code 处于最深层次——`PreToolUse`/`PostToolUse` 钩子、受管理设置、控制台启动和停止、按主体的模型访问——Codex 和 Grok Build 作为一流命令界面并列其旁，gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw 和 Hermes 则各自通过自己的连接器接入；每个连接器都说明它能强制执行什么、只能观察什么。Ollama 和其他自托管端点通过本地连接器进行清点，而该连接器按设计为只读。

**谁在运行它。** 每种规模使用同一个构建：家庭服务器（一个二进制文件、SQLite、绑定回环地址）；自由职业者（每位客户一个租户，预算会在账单到来前拒绝支出）；工程团队（共享工作项、SSO，以及无需手工拼凑的审计轨迹）；受监管企业（具备行级安全的 Postgres、HA、离线安装和 WORM 归档）。开放构建就是整个平台，商业附加组件是叠加在其上的增量代码，绝不是从开放产品中移除的功能；SSO、HA、WORM，以及真正会拒绝的预算，都是你需要配置的能力，而不是首次启动时的默认项。

产品没有强制遥测，控制平面默认也不产生出站流量；只有你明确配置为跨越边界的内容才会跨越你的边界——例如对你的模型 API 的调用、你接入的 SIEM/webhook 输出，以及你配置的嵌入提供方。采集器从你已在运行的系统读取，因此采集器故障绝不会处于生产环境的数据路径中。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="同一个二进制文件适用于各种规模，从家庭服务器到受监管企业；它在哪里运行、会触及什么。">
</picture>
<sub>从家庭实验室到受监管企业，都是同一个开放构建。</sub>
</div>

## 它能做什么

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="读写访问图：每个 agent 在整个 estate 中读写什么；左侧是来源，右侧是资源。">
</picture>
<sub><b>读写访问图</b> — 每个 agent 在整个 estate 中读写什么，以颜色区分读写。</sub>
</div>

- **看见它。** 清点每个已发现的 agent、会话、模型、MCP 服务器、工具和身份；用一张**读写访问图**展示每个对象实际触及的内容，并提供 Permitted-vs-Observed **漂移**视图；实时会话、编排图、健康状况和 SLA。它看不到的内容会标记为 `unknown`，绝不猜测。
- **运行工作。** 带有所有权、依赖关系、验收标准和决策的持久工作项；围栏租约确保两个 agent——或两个人——无法同时持有同一个工作项；从控制台启动、附加和停止会话；通过 A2A 向获授权对等方委派。影子模式与最终权威尚未构建，并明确列为缺失：[工作平面](docs-site/src/content/docs/explanation/work-plane.md)。
- **治理并执行它。** 一个 Cedar 授权引擎，以及**四个 deny-closed 执行点**——Claude Code 钩子、内联 `/v1/messages` 推理代理、MCP `tools/call` 关卡和 A2A 委派关卡——使未授权操作在运行前被阻断、挂起等待双人审批，或在钩子中被改写；只有当测试实际走过该执行点的未配置路径，并断言结果为拒绝时，该执行点才计入数量。预算可以拒绝或限制支出，破玻璃机制实行双人控制，还有一个失败即关闭的 estate **终止开关**。
- **受治理地供给它。** 内容源（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL，以及一个限定根目录的文件系统源）将内容送入受治理检索：开箱即用的零出站词法检索；当你配置嵌入提供方后启用模型支撑的语义检索；在检索时以 deny-closed 方式强制执行许可级别。
- **证明它。** 一份哈希链、Ed25519 签名的审计台账；映射到 **26 个框架目录**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR……）的封存证据——这些是自行评估的控制族，并非认证；SIEM/ITSM 推送（CEF/LEEF/syslog/OTLP/OCSF）。按具体部署配置：人类和非人类身份（WebAuthn/FIDO2、PIV/CAC、单 IdP SSO、SCIM 对账、agent 身份联邦）、内联护栏、DLP、BYOK/CMEK 加密，以及通过经验证的密钥销毁实现的被遗忘权。

**30 个模块**、一个控制台、**158 项集成**——计数从代码推导，并由 [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) 在每次推送时强制校验。一项集成是一个包含 Go 代码的连接器目录，其中十二个是共享库包：[`connectors/README.md`](connectors/README.md) 给出了明细。每个模块及其成熟度见[模块目录](docs-site/src/content/docs/reference/modules/overview.md)；按保真度层级列出的已接入连接器见[连接器参考](docs-site/src/content/docs/reference/connectors.md)。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="agent 如何协作：由工作项、围栏租约和限定范围的消息构成单一持久工作平面；委派经过执行关卡；影子模式与最终权威以虚线绘出，因为它们尚未构建。">
</picture>
<sub>agent 共用同一个持久工作平面。尚未构建的内容以缺席方式绘出。</sub>
</div>

## 控制台一览

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="从控制台创建、附加和治理 Claude Code 会话。"></picture><br><sub><b>Claude Code</b> — 从控制台创建、附加和治理会话，无需 SSH。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="工作：跨会话持久保存的工作项与决策积压。"></picture><br><sub><b>工作</b> — 持久的跨会话积压：工作项、所有权、验收、决策。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="编排与 A2A：根据观测信号得出的 agent 间委派图。"></picture><br><sub><b>编排与 A2A</b> — 谁向谁委派，由观测信号得出。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="清单：在你的 estate 中发现的每个 agent、会话、MCP 服务器、模型和身份。"></picture><br><sub><b>清单</b> — 在你的 estate 中发现的每个 agent、会话、MCP 服务器、模型和身份。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小权限漂移：叠加在读写访问图上的意外访问和未使用授予。"></picture><br><sub><b>最小权限漂移</b> — 已观测但未许可的访问，以及无人使用的授予。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="安全与取证：护栏发现项、异常队列和防篡改取证。"></picture><br><sub><b>安全与取证</b> — 护栏发现项、异常和防篡改取证。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="终止开关：实行双人控制恢复的 estate 紧急停止。"></picture><br><sub><b>终止开关</b> — 一键中止所有受治理的作动面；恢复需要两个账户。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="会话录制查看器：同一条时间线上的 agent 活动和治理证据，链已验证。"></picture><br><sub><b>会话录制</b> — 同一条时间线上的 agent 活动和治理证据，链已验证。</sub> |

每张静态图都是由运行中的二进制文件所提供的预置演示 estate 的截图（`bash scripts/docs-captures.sh` 可重新生成原始截图集）。完整的界面地图见[控制台参考](docs-site/src/content/docs/reference/console.md)。

<a name="install"></a>
## 安装

每个发布版本都在一条经 cosign 签名的信任链下交付，并按构件类型验证：一份经 cosign 签名的校验和清单涵盖其中列出的归档、软件包和逐归档 SBOM；每份归档都配有一个 SPDX SBOM sidecar 及其 in-toto 证明；容器镜像带有 cosign 签名及其自身的 SBOM 证明；整套构件还有 OpenVEX 声明和 SLSA 构建溯源。对于安全产品而言，供应链是信任模型的一部分：运行之前先[验证它](docs/RELEASE-VERIFICATION.md)。

**HTTPS 便捷路径。** 脚本正文经 HTTPS 传输，管道不会预先验证它；开始运行后，它会检测你的操作系统和架构，要求 `cosign`，验证已签名的校验和清单及归档的 SHA-256，只安装二进制文件，并且绝不调用 `sudo`。通过管道交给 shell 时，请固定版本：

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**高保证路径。** 先下载、验证，再执行：归档、软件包和校验和清单都在[发布页面](https://github.com/olivaresai/olivares/releases/tag/v26.8.0)，[`scripts/verify-release.sh`](scripts/verify-release.sh) 会验证存在的内容并说明跳过了什么——默认采用无密钥模式；在断网主机上则使用 `--key … --offline`。[安装器信任合同](docs/RELEASE-INSTALLER.md)说明了两条路径；带签名且有版本的安装器及其可选启用的服务适配器，要到该安装器落地后切出的首个发布版本才开始提供，而 v26.8.0 早于该安装器。

| 路径 | 获得内容 |
|---|---|
| **Linux 软件包** — `.deb`、`.rpm`、`.apk` | 二进制文件、一个加固的 systemd 单元、一个示例 env 文件和一个不可登录的 `olivares` 服务用户；服务不会替你启动 |
| **容器** — `docker.io/olivaresai/olivares:26.8.0` | distroless、非 root，标签不带 `v` 前缀；`ghcr.io/olivaresai/olivares` 是 digest 相同的镜像。默认镜像支持多架构（amd64/arm64）；`-fips` 和 `-stig` 变体仅支持 amd64 |
| **Homebrew** — `brew install olivaresai/tap/olivares` | macOS 和 Linux 上的发布版二进制文件，会对照已签名的校验和进行核验，并已清除 Gatekeeper 隔离；darwin 构建尚未经 Apple 公证 |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) 或 [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | 源码树中的 Helm chart 源码以及一份无需 Helm 的扁平清单；该 chart **尚未发布到 OCI 注册表** |
| **从源码构建** — `task build`（Go 1.26+、[Task](https://taskfile.dev)、pnpm） | `./bin/olivares quickstart`，同样默认即安全的首次运行 |

引擎**默认即安全**：它绑定回环地址，首次启动时使用自签名证书提供 HTTPS，不附带默认凭据，并打印单次使用的设置令牌；在容器或 pod 中，进程监听自身的网络，而主机映射或 Service 使其保持私有。**Windows** 尚未构建——请运行 Linux 容器或 WSL2（[计划](INSTALL.md#windows)）。各操作系统的完整矩阵和生产环境设置见 [`INSTALL.md`](INSTALL.md)；部署指南（Compose、Kubernetes、离线）及[升级](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)见 [`docs-site/`](docs-site/)。

<a name="quickstart"></a>
## 快速上手

探索一个合成 estate，或正式启动它。两者运行的都是同一个二进制文件。

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

演示种子数据仅供学习（密码位于公开源码树中）：切勿将其指向真实数据。CI 使用 `task smoke:quickstart` 走过同一路径，并断言读写访问图和漂移计数（20 个节点 / 13 条边，含 8 处意外访问和 2 项未使用授予），因而本页面无法悄然偏离代码。[完整快速上手](docs-site/src/content/docs/start/quickstart.md)会接入一个真实的 pgAudit 连接器，并链接生产环境安装路径。

## 版本

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="按组成划分的版本：AGPL 核心是整个平台，附加组件是在其上增加的代码，Cloud Standard 是托管服务。">
</picture>
<sub>按组成划分的版本。打包与定价请咨询。</sub>
</div>

AGPL 构建是整个平台，绝不会从内部设置功能上限；商业附加组件是增量代码，绝不是从开放产品中移除的功能。订阅是用于下载签名模块包的凭据——这是一种发行式模式，而不是用于解锁已在你磁盘上的代码的密钥。自托管引擎的用户账户不受限制，并且**四个 deny-closed 执行点**全部开放。开放、商业和计划能力的逐区域矩阵见 [`LICENSING.md`](LICENSING.md) 和[开放核心与许可](docs-site/src/content/docs/explanation/open-core-and-licensing.md)。

## 架构

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="架构：agent 界面、审计源、MCP 与 A2A 对等方及内容源汇入一个自托管二进制文件，由它提供控制台、REST API、gRPC、CLI 和 Terraform provider；云控制平面（已构建，尚未部署）与许可门户（已部署，销售关闭）绘为独立平面。">
</picture>
</div>

一个静态 Go 二进制文件内嵌控制台，并通过四个界面提供有明确覆盖范围的能力：REST API（主要界面）、稳定核心的聚焦 gRPC 镜像、`olivares` CLI 和 Terraform provider。采集器以三种模式运行在你的基础设施内；存储为 SQLite 或带行级安全的 Postgres，相关约束先由存储 API 强制执行，再由 Postgres 强制执行。包括工作平面逐项细节在内的说明见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

<a name="documentation"></a>
## 文档

[docs.olivares.ai](https://docs.olivares.ai)——经测试的安装教程（单节点、Docker Compose、Kubernetes/Helm、离线）、带真实控制台截图的连接器指南、一本 cookbook（deny-closed 策略、预算、审批、终止开关演练、SIEM 推送）、API 参考和术语表。从[什么是 Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md)和[诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)开始。

<a name="security"></a>
## 安全

请通过 [`SECURITY.md`](SECURITY.md) 私密报告漏洞，绝不要提交公开 issue。引擎读优先且坚持最小数据：读写访问图只存边，不存载荷；打开它也会被记录。公告流程见 [`docs/security-advisories.md`](docs/security-advisories.md)；供应链证据映射见 [`docs/openssf-badge.md`](docs/openssf-badge.md)。

## 社区

[`CONTRIBUTING.md`](CONTRIBUTING.md)（环境搭建、DCO/CLA、SPDX、连接器边界）· [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md)（Contributor Covenant 2.1）· [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog 1.1、CalVer `vYY.M.PATCH`）。

## 许可

`core/`、`modules/` 和 `web/` 采用 **AGPL-3.0-only**；`sdk/`、`connectors/` 和 `clients/` 采用 **Apache-2.0**，且连接器绝不导入引擎。商业附加组件是独立、可选且闭源的——仅通过 `-tags enterprise` 构建，绝不出现在本仓库或开放二进制文件中；商业授权请联系 `enterprise@olivares.ai`——[`LICENSING.md`](LICENSING.md)。贡献需要 DCO 签署（`git commit -s`）和 [CLA](CLA.md)。

> **无担保、无责任。** 本软件按**现状**提供，**不附带任何形式的担保**，并且**不对数据丢失、业务中断或利润损失承担任何责任**。在控制平面上，这并非形式条款：错误配置可能阻断正当工作，也可能恰好放行你本想阻止的内容。AGPL-3.0-only §§15–16、Apache-2.0 §§7–8 以及本项目的补充条款均适用——[`DISCLAIMER.md`](DISCLAIMER.md)。

## 支持这个项目

核心免费，并将保持免费；让每个发布版本保持签名、经过验证且最新是一项持续工作。如果 Olivares AI 对你有用，可以通过 GitHub Sponsors 赞助——[github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) 或 [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares)——也可以在 Ko-fi 上一次性赞助。赞助不是支持合同，也不购买优先级（[`SUPPORT.md`](SUPPORT.md)）；要求署名的赞助者会列入 [`SUPPORTERS.md`](SUPPORTERS.md)。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>企业 AI 的唯一可信事实源。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>

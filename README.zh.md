<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**语言:** [English](./README.md) · [Español](./README.es.md) · **简体中文** · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**运行团队已经在用的 AI，像管理其他基础设施一样掌握它。**

[它做什么](#它做什么) · [安装](#安装) · [控制台](#控制台一览) · [版本](#版本与定价) · [文档](#文档) · [社区](#社区) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

下一个版本是 <!-- release -->`0.1`<!-- /release -->，其 GitHub 发行尚未发布。以下命令描述计划中的产物。发布前请从源码构建，发布后也应在使用前验证每个产物。观测到的发布状态记录在 <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->。 Kubernetes OCI: `publication-unverified`.

开发者用 Claude Code 和 Codex 工作。agent 调用 MCP 服务器、模型和内部 API，定时任务自行运行。每个组件都有自己的日志和权限，所以连简单的问题也难以快速回答：哪个 agent 改了这个文件，谁批准的，这个月 AI 花了多少钱？

Olivares AI 把答案汇集在一处。它连接你已经在用的 agent 和工具，展示每个工具的行为，在操作执行前应用你的规则，并为一切保留签名记录。它是一个运行在你自己服务器上的程序，完整产品免费且开源。

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>访问图</b> — 每个 agent 读取和写入了什么，以及那次无人授权的写入。</sub>
</div>

## 它做什么

- **知道什么在运行。** 在一份清单中查看所有 agent、会话、模型、MCP 服务器和工具。读写访问图展示每个对象读取和写入的内容，并标出没有任何规则允许的访问。
- **在操作造成损害之前拦住它。** Olivares AI 有**四个 deny-closed 执行点**，在每次操作执行前进行检查：Claude Code 内部、模型代理、每次 MCP 工具调用，以及 agent 之间。有风险的操作等待第二个人确认，被禁止的操作不会执行，一个开关就能同时停止所有 agent。检查无法作出判断时，操作也不会执行。
- **控制 AI 支出。** 按团队、agent 或模型设置预算，在账单到来之前发出警告、减缓或停止支出。
- **安全地让 agent 使用公司知识。** 连接 SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3 和 PostgreSQL。每个 agent 只能看到使用它的人有权看到的内容。
- **跨会话继续工作。** 会话结束后，任务、负责人和决策仍然保留。无需 SSH，就能从浏览器启动、加入和停止 Claude Code、Codex 和 Grok 会话。
- **需要时提供证据。** 每项决策都写入签名日志，事后的任何篡改都可被检测到。Business Compliance Packs 将证据对应到 26 个框架目录，并为安全团队和审计人员生成报告。Community 保留已存证据的读取和 JSON/CSV 导出功能。

支持你已有的工具：Claude Code、Codex、Grok、Cursor、gemini-cli、opencode、OpenHands，以及通过 Ollama 运行的本地模型。**32 个模块**和 **136 项集成**：[所有模块](docs-site/src/content/docs/reference/modules/overview.md) · [所有连接器](connectors/README.md)。

Community 保留本地可观测性、已保存的设置和备份导出。SIEM/ITSM 推送、外部遥测传输和安全态势导出包含在 Business 基础版中。

## 安装

**Docker Compose.** The container qualification job exercises this installation path.
Set the release image explicitly so a cached `:latest` image cannot select an
older release.

<!-- release -->
```sh
set -e
cd /path/to/your/project   # the host folder your sessions will work on
export OLIVARES_PROJECT_DIR="$PWD"
git clone --depth 1 https://github.com/olivaresai/olivares.git "$HOME/olivares"
export OLIVARES_IMAGE=docker.io/olivaresai/olivares:0.1
# On Linux hosts whose AppArmor policy mediates user namespace creation:
if [ -r /sys/kernel/security/apparmor/features/namespaces/mask ] &&
   grep -qw userns_create /sys/kernel/security/apparmor/features/namespaces/mask; then
  sudo install -m 0644 "$HOME/olivares/deploy/apparmor/olivares-sessions.conf" /etc/apparmor.d/olivares-sessions
  sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions
  export OLIVARES_APPARMOR_PROFILE=olivares-sessions
fi
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" up --wait --wait-timeout 120
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" exec olivares \
  olivares first-boot --data-dir /var/lib/olivares --new-token
```
<!-- /release -->

Open the console address printed by `first-boot --new-token` and use the replacement
one-time setup token it prints to create the first administrator. This invalidates
the previous setup token and is available only before the first administrator exists.
The stack uses SQLite and a persistent data volume.
The AppArmor step needs an AppArmor 4 parser and runs on the Docker daemon host.
See [session confinement on AppArmor hosts](deploy/compose/README.md#session-confinement-on-apparmor-hosts).
It publishes ports on every host interface by default; set `OLIVARES_BIND=127.0.0.1`
to restrict access to this host.

Sessions in the container work on one host folder, mounted at `/project`: the absolute
path in `OLIVARES_PROJECT_DIR`, set before `up`. Without it, `/project` is an empty Docker
volume, never the directory you run Compose from. A session there can change everything in
that folder: never set the variable to your home directory. In the console, choose **Change folder**
on the New session form and enter `/project`. On a Linux host the container user
(UID 65532) needs write access; see
[work on a host project folder](deploy/compose/README.md#work-on-a-host-project-folder).

Gate coverage is not a passing release result: see the `qualify-compose-ready` job in
[container qualification](.github/workflows/compose-ready.yml). The release must also
pass its first-hour journey before it is qualified.

Other installation methods are **not qualified** by the first-hour gate. Their commands
and limits are in [INSTALL.md](INSTALL.md#installation-qualification), including the
shell installer, standalone Docker, Kubernetes, native packages, Homebrew, source builds
and offline installs. [Verify release artifacts](INSTALL.md#verifying-a-release) before
running them; see [upgrading and uninstalling](INSTALL.md#upgrading--uninstalling) for an
existing installation.

- Helm Chart、Kubernetes Operator 和 Terraform Provider 由 Business 发行版提供。 [Editions](https://olivares.ai/pricing).
Install the chart from source; see [Kubernetes installation](INSTALL.md#kubernetes).

## 控制台一览

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>访问图</b> — 谁读取和写入了什么。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — 无人授权的访问，以及无人使用的权限。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>会话</b> — 从浏览器启动、加入和停止 agent 会话。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>工作</b> — 会话结束后仍保留的任务、负责人和决策。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>安全</b> — 被拦截的操作、异常和可检测篡改的记录。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>支出</b> — 按模型和 agent 查看费用、预算和预测。</sub> |

全部页面见[控制台参考](docs-site/src/content/docs/reference/console.md)。

## 版本与定价

Community 是完整产品，免费且开源。Business 增加企业在生产环境中运行所需的能力。Enterprise 面向基础设施规模更大或受监管的企业集团。

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **价格** | 免费，AGPL-3.0 | 每月 129 美元或每年 1,290 美元 | 年度合同 |
| **包含内容** | 完整产品：不限用户数量，包含全部四个 deny-closed 执行点 | Community 的全部内容，加上 Regulated Operations、AI Runtime Security、Compliance Packs、Identity & Scale、商业许可证、签名更新和邮件支持 | Business 的全部内容，加上更多公司、部署和身份提供商、离线镜像，以及与你约定的支持条款 |
| **使用范围** | 一个活跃身份提供商 | 一家公司，同一时间一个活跃实例 | 按合同约定 |

**Regulated Operations** 增加法规要求的最短保留期限、归档上的法律保全核对，以及 Azure 和 GCS 上的 WORM 归档。**AI Runtime Security** 对 agent 发送、接收和执行的内容进行更深入的检查。**Compliance Packs** 为你的审计人员起草 DORA 信息登记册和 ISO/IEC 42001 材料包。**Identity & Scale** 同时连接多个身份提供商，并支持更大规模的部署。

[olivares.ai/pricing](https://olivares.ai/pricing) · [各版本包含的内容](docs/editions.md) · [开源与商业范围](LICENSING.md)

## 架构

一个内置控制台的 Go 二进制程序，提供 REST API、gRPC API、`olivares` 命令行和 Terraform provider。采集器在你的网络内运行，数据保留在你服务器上的 SQLite 或 PostgreSQL 中。[整体架构](ARCHITECTURE.md)。

## 文档

[docs.olivares.ai](https://docs.olivares.ai) 提供安装指南、每个连接器的指南、常见策略的配置示例和 API 参考。先阅读[什么是 Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md)。当前可用功能与后续计划见[诚实与限制](docs-site/src/content/docs/start/honesty-and-limits.md)。发布版本：[GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

网站：[产品](https://olivares.ai/product) · [方案](https://olivares.ai/solutions) · [工作原理](https://olivares.ai/how-it-works) · [架构](https://olivares.ai/architecture) · [安全](https://olivares.ai/security) · [信任](https://olivares.ai/trust) · [对比](https://olivares.ai/compare) · [演示](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [状态](https://olivares.ai/status) · [路线图](https://olivares.ai/roadmap) · [品牌](https://olivares.ai/brand) · [新闻](https://olivares.ai/press)。

## 安全

发现漏洞？请按 [`SECURITY.md`](SECURITY.md) 私下报告。Olivares AI 记录哪个 agent 访问了哪个资源，不记录内容；查看这些记录的行为本身也会被记录。许可证离线验证；开源核心从不联系我们。

## 社区

欢迎贡献。[`CONTRIBUTING.md`](CONTRIBUTING.md) 介绍环境设置、签署确认和连接器如何接入。[行为准则](CODE_OF_CONDUCT.md) · [支持](SUPPORT.md) · [治理](GOVERNANCE.md) · [更新日志](CHANGELOG.md)。

## 支持本项目

Olivares AI 公开开发。如果它对你有帮助，可以在 GitHub Sponsors 上赞助 [olivaresai](https://github.com/sponsors/olivaresai) 或 [fran-olivares](https://github.com/sponsors/fran-olivares)，也可以在 Ko-fi 请我们喝杯咖啡。愿意公开署名的赞助者会列在 [`SUPPORTERS.md`](SUPPORTERS.md) 中。赞助不是支持合同（[`SUPPORT.md`](SUPPORT.md)）。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## 许可证

引擎、模块和控制台采用 **AGPL-3.0-only**；SDK、连接器和客户端采用 **Apache-2.0**。商业代码单独构建，不包含在本仓库中；商业许可请联系 `enterprise@olivares.ai`。贡献需要 DCO 签署（`git commit -s`）和 [CLA](CLA.md)。

> **按原样**提供，不提供任何形式的担保，也不对数据丢失、业务中断或利润损失承担责任。参见 [`DISCLAIMER.md`](DISCLAIMER.md)。

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>企业 AI 的唯一可信事实源。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>

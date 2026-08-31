<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**语言:** [English](./README.md) · [Español](./README.es.md) · **简体中文** · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**为你实际运行的 AI 而设的控制平面。** 将它集成并投入工作，把它连接到你的系统，并治理它的每一个部分——一个自托管二进制文件，适用于从家庭服务器到受监管企业的各种规模。

[安装](#install) ·
[快速上手](#quickstart) ·
[示例](examples/) ·
[架构](#architecture) ·
[文档](#documentation) ·
[安全](SECURITY.md) ·
[贡献](CONTRIBUTING.md) ·
[olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

<!-- OpenSSF Best Practices Badge (self-certification).
     Registration at https://www.bestpractices.dev is pending (a maintainer action); the
     evidence map is in docs/openssf-badge.md. Once a project ID is assigned, uncomment:
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
-->

</div>

> 状态：**beta**，处于活跃开发中。引擎可端到端运行——一个内嵌控制台的单一静态二进制文件，从 AI 实际运行的系统中摄取真实信号。在 1.0 之前，API、schema 和模块界面仍可能变化，部分执行接缝（已声明的 deny-closed 集成点）在完成配置前保持关闭（参见 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)）。发布版本从本仓库切出；下方的[安装路径](#install)将随首个打标签的发布版本一并提供。

> 供应链：发布版本在 GitHub Actions 上构建，并按构件类型附带签名的信任链——归档附带 SPDX SBOM 与 in-toto 证明，容器镜像经 cosign 签名并附带镜像 SBOM 证明，每个构件（包括软件包与 chart）都被 cosign 签名的校验和清单覆盖，另有覆盖整组发布的 OpenVEX 文档和 SLSA 构建溯源。可用 [`scripts/verify-release.sh`](scripts/verify-release.sh) 校验任何发布版本；各构件类型的确切信任链、离线路径与 Helm chart 记录于 [`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md) 和 [`deploy/`](deploy/)。

## 什么是 Olivares AI

AI 早已不只是一个聊天窗口。你现在实际运行的是一片小型资产域：终端中的编程 agent、MCP 服务器、模型端点、服务账户和计划任务，分散在从未被设计为一个系统的机器上。没有任何东西将它们凝聚在一起，于是回答这些普通问题会变得代价高昂：正在运行什么、谁启动了它、它访问了什么、花了多少钱，以及谁同意了这一切。

**Olivares AI 是把它们凝聚在一起的平面。** 它有两个部分，并在同一个二进制文件中交付：

- **运行并连接它。** 用于实际工作的持久平面。带所有权、依赖关系、验收标准和决策的工作项；使所有权成为过期持有者无法继续使用的权威的租约；从控制台启动、附加和停止会话，并向正在运行的会话提供输入；经 A2A 向远程对等方委派；作为工具界面的 MCP；以及为检索提供内容的受治理内容源。这是下方[工作平面](#the-work-plane)所描述的一半，其中每个部分的状态都明确说明。
- **查看并治理它。** 所有已发现内容的清单、每个 agent 和身份实际访问内容的读/写访问映射、Cedar 策略、deny-closed 强制执行、可拒绝支出的预算，以及事后证明这一切的哈希链签名账本。

两部分都不是另一部分的装饰。没有工作平面的治理只是没有任何可供行动之物的仪表盘；没有治理的工作平面则是事后无法交代清楚的工作。

**按设计支持多提供方。** Claude Code 以最深层次集成——`PreToolUse`/`PostToolUse` 钩子、受管理设置、控制台启动和停止、按主体的模型访问——Codex 和 Grok Build 也作为一流命令界面并列，gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw 和 Hermes 则各自作为连接器接入。每一个都说明它能强制执行什么、只能观察什么；没有一个是产品的重心。Ollama 和其他自托管端点通过本地连接器完成清点和归因；该连接器按设计是只读的。策略和预算规则在推理穿过受治理代理之处绑定，而那是它们唯一能够绑定的地方。

**谁在运行它。** 开放构建在所有这些规模下都是整个平台——商业附加组件是在其之上的增量代码，绝非不同的产品：

| 你是 | 这意味着什么 |
|---|---|
| **运行家庭服务器或 homelab 网络** | 一个二进制文件、SQLite、一个 Docker 卷、绑定回环地址、无外部服务——随附的 Compose 拓扑以非 root 和只读方式在 1 CPU 与 1 GiB 内运行（[`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml)） |
| **自由职业者或独立顾问** | 每位客户一个租户——每次模块操作都绑定到其中一个——可在账单到来前拒绝或限制支出的预算，以及可交付的姿态导出 |
| **专业人士或高级用户** | 与企业运行的同一套引擎，没有任何保留：开放构建就是整个平台，因此你在自己的设备上学到的，正是你在工作中操作的 |
| **工程团队或小型企业** | 共享的工作项和租约，让两个 agent——或两个人——不能同时持有同一个工作项；SSO、角色，以及无需手工拼凑的审计轨迹 |
| **受监管企业** | 具备行级安全的 Postgres、单写入器和备用节点的 HA、离线安装、映射到 **26 个框架目录**的证据，以及在不可变底层存储上的 WORM 归档 |

每一行都是同一份构建。其中若干能力——SSO、HA、WORM 归档、真正会拒绝的预算——是你需要**配置**的，而非首次启动时就默认具备；下方矩阵和 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md) 会逐项说明分别属于哪种情况。

它以**单一自托管 Go 二进制文件**运行，控制台内嵌其中——可部署于 Linux、Docker、Kubernetes、本地或完全离线环境。没有强制遥测，默认也没有控制平面出站流量：跨越你边界的，只有你配置为跨越它的东西——对你的模型 API 的调用、你接入的 SIEM/webhook 输出，以及（若你配置了）外部嵌入提供方。采集器从你已经在运行的系统读取（pgAudit、CloudTrail、eBPF、MCP、你的 IdP），因此失效的采集器绝不会处于生产数据路径之中。

覆盖范围和归因带有明确分级（`firm`/`approximate`/`unknown`、`clean`/`lossy`/`opaque`），执行在已接线之处为 deny-closed、在未接线之处为声明的接缝，文档明确说明今天哪些可运行、哪些处于设计阶段。产品不会编造它无法证明的确定性——参见 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)。

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840"
       alt="访问映射: 展示每个智能体在你的资产域中读取和写入了哪些内容——左侧为来源，右侧为其触及的资源，读/写（R/RW）以颜色区分。">
</picture>

<sub><b>访问映射</b> — 展示每个智能体在你的资产域中读取和写入了哪些内容——左侧为来源，右侧为其触及的资源，读/写（R/RW）以颜色区分。</sub>

</div>

**用两条命令亲眼看看**（Go 1.26+、[Task](https://taskfile.dev)、pnpm——[前置条件](#quickstart-prerequisites)）：

```sh
task build
./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 \
  --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps
```

CI 走的也是同一条路径：`task smoke:quickstart` 会针对真实二进制文件启动这个演示资产域，并对其访问映射与漂移计数做断言。有关安装路径及其默认运行设置，请参阅[安装](#install)与[快速上手](#quickstart)。

<a name="the-work-plane"></a>
<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840"
       alt="同一个二进制文件适用于各种规模：家庭服务器或家庭实验室、为每位客户设一个租户的自由职业者、工程团队或小型企业，以及受监管的企业。它可运行于 Linux、Docker、Kubernetes、Helm 与气隙环境，并在发布时提供托管云，且可触达模型提供方、云与目录、受治理的内容源和输出连接器——访问地图是其中的一项能力，而不是中心。">
</picture>

<sub>从家庭实验室到受监管企业，都是同一个构建。</sub>
</div>

## 工作平面

承载工作的平面是 Olivares AI 中 agent 和人共同使用的部分，也是最常被描述得仿佛处处都已完成的部分。它并非如此，因此这里列出每个部分实际依托的基础，以及它今天达到的范围。

| 部分 | 状态 | 所在位置 |
|---|---|---|
| **工作项**——简报、溯源、依赖关系、验收标准、决策、所有者和事件历史；持久化，并由 REST、CLI 和进程内调用方共享同一份命令文档 | **已上线，公开 API** | [`modules/sessions/work_model.go`](modules/sessions/work_model.go)，路由位于 [`modules/sessions/work_api.go`](modules/sessions/work_api.go) |
| **租约**——作为受围栏保护、会过期的权威的所有权：获取、续期、释放、接管、撤销；过期持有者不能继续行动，并发获取恰好产生一个获胜者 | **已上线，公开 API** | [`modules/sessions/work_lease.go`](modules/sessions/work_lease.go) |
| **消息、确认和交接**——绑定工作项的持久对话，支持重放并拒绝过期 epoch | **在编排工作流之后已上线；通用公开收件箱是有意不接线的** | [`modules/sessions/communication_model.go`](modules/sessions/communication_model.go)；禁止接线公开平面的启动测试位于 [`cmd/olivares/communicationauthorityboot_test.go`](cmd/olivares/communicationauthorityboot_test.go) |
| **为工作启动**——预留、获取租约、*然后*生成会话，持久化工作/epoch/围栏/执行，以便重试是安全的 | **通过编排已上线** | [`modules/sessions/runtime_work_launch.go`](modules/sessions/runtime_work_launch.go) |
| **通过 A2A 远程执行**——在获授权对等方上计划、测试、启动、观察和取消工作，并保存持久回执 | **已上线，但仅在配置目标时**；没有获授权目标时，该接缝完全不会挂载 | [`cmd/olivares/wire.go`](cmd/olivares/wire.go)，[`cmd/olivares/orchremote.go`](cmd/olivares/orchremote.go) |
| **影子模式和最终权威**——在该平面成为权威之前，与既有系统及比较器进行双重报告 | **尚未构建** | 仅有设计 |

请把该表理解为“彼此对话的 agent”的诚实版本：工作项和租约是今天就能驱动的普通 API 界面；agent 间对话真实且持久，但范围限定于编排工作流，并不存在供任意 agent 使用的通用消息总线；远程委派可用且会拒绝未知对等方。不存在的东西不会在界面里列为即将推出——它在这里被列为缺失。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840"
       alt="智能体如何协同工作：各类代理界面汇入一个持久的工作平面，包含工作项、同一时刻仅有一个持有者生效的围栏租约、面向工作的启动，以及限定在工作区范围内的消息与确认。委派通过其执行闸门抵达获授权的对端。该平面输出编排图、事件总线、带偏移的访问地图，以及可送达你的 SIEM 的签名账本。影子模式与最终权限以虚线框绘出，因为它们尚未构建。">
</picture>

<sub>智能体共用同一个持久工作平面。尚未构建的内容以缺席方式绘出。</sub>
</div>

## 它覆盖什么

一个二进制文件、**30 个模块**、一个控制台——横跨你 AI 的整个足迹，而非单一功能。每项能力都带有明确的成熟度状态——已上线、按需、仅观测，或已声明的 deny-closed 接缝——并在 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md) 中逐项说明。

- **运行工作。** 如[工作平面](#the-work-plane)所述的持久工作项、租约、经编排的启动和 A2A 委派；控制台的 Work 视图是同一存储的操作界面，Orchestration 视图从观测信号绘制委派拓扑。
- **看见它。** 对每个**已发现**的 agent、会话、模型、MCP 服务器、工具和身份进行清点——覆盖范围随你所接入的系统而定，带有明确指示标记，并把它看不到的内容标为 `unknown` 而不是猜测；提供一张展示各方实际触及范围的读/写**访问映射**，并配有 Permitted-vs-Observed **漂移**视图；实时会话、编排图、健康状况与 SLA。
- **治理并执行它。** 一个 Cedar 授权引擎（RBAC + deny-overlay + 正向的范围化授予）以及**四个 deny-closed 执行点**——Claude Code 的 `PreToolUse`/`PostToolUse` 钩子、一个内联 `/v1/messages` 推理代理、一个 MCP `tools/call` 关卡和一个 A2A 委派关卡——使未授权行为不会执行：它们会被拦截、送交双人审批，或在运行前被改写。这个形容词是测量出来的，不是断言出来的：只有当测试走过某点*未配置*的路径——没有接线的关卡、空策略文档、不会作答的策略存储——并断言其拒绝时，该点才会被计入。接缝到证明的清单见 [`scripts/enforcement-seams.tsv`](scripts/enforcement-seams.tsv)；移除一份证明，计数就会下降，构建随之失败。策略更深入会话本身：钩子中按路径、按子树的 allow/ask/deny 规则，按界面、按组的上下文窗口预算，以及可细化到会话、agent、用户、组或角色的来源范围界定。此外还有范围化管理和自定义角色、带双人控制的 break-glass，以及一个失败即关闭的资产域**kill-switch**。
- **Claude 与 agent 生态。** 在钩子中治理 Claude Code；从控制台启动、附加、治理并停止 Claude Code 会话及其工作区；下发企业级 managed-settings；治理每个主体可在何种界面使用何种模型；MCP（OAuth 把关的资源服务器、姿态、注册表、`.mcpb`）；获授权对等方之间的 A2A v1；以及面向团队实际运行的 agent 的界面——gemini-cli、Cursor、Codex CLI、opencode、goose、cline、OpenHands、OpenClaw 和 Hermes（在各界面暴露强制能力之处实施强制，在未暴露之处做只读姿态观测；每个连接器都注明属于哪一种）——外加带审批 deep-link 的 Teams 通知。
- **受治理地供给它。** 同一枚硬币的上下文一面：内容源（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL，外加一个限定根目录的文件系统源，用于本地/NFS/SMB 挂载）供给一条自带可用默认配置的受治理 RAG 流水线——开箱即用的零出站词法检索、当你配置嵌入提供方后启用的模型支撑语义检索（Voyage、OpenAI-compatible 或自托管；`embed_policy=model_backed` 会失败即关闭，而不是悄然降级）、按来源的溯源、在检索时刻以 deny-closed 方式执行的许可级别与范围界定——外加带版本化契约与质量关卡的数据产品目录。参见 [Governed data for Claude](docs-site/src/content/docs/how-to/governed-data-for-claude.md)。
- **身份与访问。** 人类身份（WebAuthn/FIDO2、PIV/CAC、AAL 升级验证）与**非人类身份**生命周期；agent 身份联邦（Entra Agent ID、AWS AgentCore、Google、SPIFFE/SPIRE）；从 AD/LDAP/Okta/Entra/Vault/Infisical 通过 SCIM 进行名册对账。
- **保护数据。** 内联护栏（PII、prompt-injection、jailbreak）、DLP 出口防护、跨三种 KMS 后端（AWS KMS、Google Cloud KMS、Azure Key Vault）的 BYOK/CMEK 信封加密、特权会话录制、带经验证密钥销毁的被遗忘权、保留与 legal-hold、驻留地证明，以及 TLS 1.3 混合后量子密钥建立（对端支持时使用 X25519MLKEM768；签名目前仍为经典算法）。
- **证明它。** 一份哈希链、Ed25519 签名的审计账本；封存的、仅追加的合规证据，映射到 **26 个框架目录**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR……）；SIEM/ITSM 推送（CEF/LEEF/syslog/OTLP/OCSF）。
- **把它运营好。** 可拒绝或限制支出的 FinOps 预算；带阻断式 CI 关卡的经校准 LLM-judge 评测（按需运行——没有 judge 凭据时，运行会报告 `SKIPPED`，绝不静默通过）；OS 级隔离的红队沙箱（gVisor/Firecracker；未配置沙箱时，运行会报告 `DEGRADED`，绝不编造通过）；一个带公开状态页的连接器健康仪表盘；由控制台管理的备份与恢复。

覆盖你已在运行的云、目录、密钥库、模型提供方、agent 界面、SIEM 和流水线的 **158 项集成**——该计数从代码推导，并由 [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) 在每次推送时强制校验。单位是包含 Go 代码的连接器目录：树中的 159 个目录中，158 个符合条件，并且该校验会在每次推送时以这种方式推导该数字。其中有 12 个是共享契约/库包，而不是能力——它们被计入，且 [`connectors/README.md`](connectors/README.md) 载有每个目录分别是什么的完整明细。每项能力及其成熟度的完整映射见 [`docs-site/`](docs-site/)，并由其自己的测试套件把关。

<a name="whats-open-whats-enterprise-whats-planned"></a>
## 哪些是开放版、哪些是企业版、哪些是计划项

此表将每个能力区域映射到其交付位置——开放（AGPL）构建，或某个单独的可选商业附加组件；每项能力的成熟度在 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md) 中如实说明。保留接缝的完整清单就声明在公开源码树本身之中（[`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)）：开放二进制所保留的能力会应答 `501` 或者空操作，其注释也如是说明——没有任何东西被隐藏，也没有任何开放的东西被移除。

| 区域 | 开放版（AGPL） | 商业附加组件 | 计划项 |
|---|---|---|---|
| 工作与编排 | 持久工作项（简报、依赖关系、验收、决策、事件）、可接管和撤销的围栏租约、针对工作项经编排启动会话，sessions API 中的输入和停止受工作围栏保护、向获授权对等方的 A2A 委派及持久回执、范围限定于工作流的消息/确认/交接、控制台 Work 和 Orchestration 视图 | — | 影子双重报告，以及使该平面成为记录系统的权威开关 |
| 可见性 | agent/会话/模型/MCP 服务器/工具/身份的清点，带 Permitted-vs-Observed 漂移的读/写访问映射，实时会话，编排图，健康状况/SLA | — | — |
| 策略与执行 | Cedar 授权引擎（RBAC + deny-overlay + 范围化授予），四个 deny-closed 执行点（Claude Code 钩子、内联 `/v1/messages` 代理、MCP `tools/call` 关卡、A2A 委派关卡），双人审批，带双人控制的 break-glass，资产域 kill-switch | 钩子加固，服务器工具出口控制，computer-use 治理关卡，MCP 工具定义钉固（定义变更时 deny-closed），带 kill-switch 升级的自动熔断器 | — |
| Claude 与 agent 生态 | 在钩子中治理 Claude Code，从控制台启动/附加/治理/停止 Claude Code 会话，企业级 managed-settings 下发，按主体/按界面的模型访问，MCP（OAuth 把关的资源服务器、姿态、注册表、`.mcpb`），A2A v1，面向 gemini-cli/Cursor/Codex CLI/opencode/goose/cline/OpenHands/OpenClaw/Hermes 的界面（界面暴露强制能力之处实施强制，未暴露之处仅做只读姿态观测），带审批 deep-link 的 Teams 通知 | MCP App render 内容检查，elicitation/sampling 中介 | — |
| 上下文与知识 | 十个实时内容源（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL）外加一个限定根目录的文件系统源（本地/NFS/SMB 挂载），受治理 RAG（默认词法检索，配置嵌入提供方后为模型支撑的语义检索——在 `embed_policy=model_backed` 下失败即关闭）并在检索时刻执行 deny-closed 许可级别，按来源的溯源，带版本化契约与质量关卡的数据产品目录 | — | — |
| 身份与访问 | 单 IdP SSO（OIDC + SAML 2.0），WebAuthn/FIDO2，PIV/CAC，AAL 升级验证，非人类身份生命周期，agent 身份联邦（Entra Agent ID、AWS AgentCore、Google、SPIFFE/SPIRE），通过 SCIM 进行名册（AD/LDAP/Okta/Entra/Vault/Infisical）对账，CAEP 事件接收器 | 多 IdP 联邦，SSO 强制，托管 SCIM，CyberArk Conjur NHI 轮换，CAEP 发送器（向 SSF 接收方发送签名 SET） | — |
| 数据安全 | 内联护栏（PII、prompt-injection、jailbreak），DLP 出口防护，跨三种 KMS 后端（AWS KMS、Google Cloud KMS、Azure Key Vault）的 BYOK/CMEK，特权会话录制，带经验证密钥销毁的被遗忘权，保留与 legal-hold，驻留地证明，TLS 1.3 混合 PQC 密钥建立（X25519MLKEM768） | 内容防火墙/DLP | — |
| 证据与合规 | 哈希链、Ed25519 签名的审计账本，封存的仅追加证据，26 个框架目录，带导出/校验的目录/S3 归档（目录仅在不可变底层存储上是 WORM；S3 使用 Object Lock），OSCAL 导出（三种开放模型），开放的 DORA ICT 风险视图，SIEM/ITSM 推送（CEF/LEEF/syslog/OTLP/OCSF） | OSCAL profile/SSP 摄取 + POA&M 构建器，监管保留下限 + 合规模式锁定（SEC 17a-4/FINRA 4511/CFTC 1.31），DORA 信息登记册 + 重大事件报告，长周期 WORM 法务保全 + 审查级证据包，Azure/GCS WORM 汇，ISO 42001 AIMS 包，合规深度包 + NIS2 分类包，企业级报表 | — |
| 运营 | 可拒绝或限制支出的 FinOps 预算，带阻断式 CI 关卡的经校准 LLM-judge 评测（按需运行：需要 judge 凭据，否则 `SKIPPED`），OS 级隔离的红队沙箱（gVisor/Firecracker；未配置时运行报告 `DEGRADED`），带公开状态页的连接器健康仪表盘，由控制台管理的备份与恢复，开放的攻击路径查询 | 编译的威胁情报目录，事件闭环 | — |
| 平台与部署 | 内嵌控制台的单一静态二进制文件，带行级安全的 SQLite 或 Postgres，Docker/Kubernetes/Helm/离线，Terraform provider，生成的客户端 SDK（Go、Java、Python、TypeScript），开放的进程内总线 + Core-NATS 桥接 | 持久化 JetStream 总线（至少一次送达 + 去重） | Windows 包（今天：Linux 容器或从源码构建），v1 后的模型微调，语音遥测探针（今天为已声明的 deny-closed 接缝） |

AGPL 构建就是整个平台，绝不会从内部设置功能上限。商业附加组件是增量新代码，而不是从开放产品中移除的功能。订阅是你用来下载签名构件的凭据——SUSE 模型——而不是解锁已在你磁盘上的代码的密钥。自托管引擎的用户账户不受限制：它的任何版本都不强制执行席位上限，且二进制文件的席位接缝是无条件的空操作。托管 Cloud 层是唯一例外——其控制平面按租户准入席位，这是该服务的属性，而不是此二进制文件的属性。见 [`LICENSING.md`](LICENSING.md) 和 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840"
       alt="每个版本包含什么：AGPL 内核即完整平台，附加组件是叠加其上的新增代码。Community 是功能完整的 AGPL 产品，用户数不限。Business 在报表、上手引导、威胁情报、PQC 态势与 NIS2 方面增加商业深度。Regulated Operations 增加保留调控器、WORM 审计归档、法律保全与擦除深度。Business Max 即 Business 加上全部四个附加组件。Cloud Standard 是托管服务，其套餐配额包含服务席位。订阅是你据以下载已签名制品的凭据。">
</picture>

<sub>按组成划分的版本。打包与定价请咨询。</sub>
</div>

## 控制台一览

<div align="center">

<img src=".github/assets/olivares-reel.gif" width="720" alt="一段短片，轮换展示 Olivares AI 控制台的真实视图：访问映射、会话、策略、FinOps 和合规。">

<sub>真实控制台的短短几秒。下方每张静态图都是由正在运行的二进制文件所服务的预置演示资产域的截图——原始截图可用 <code>bash scripts/docs-captures.sh</code> 自行重新生成（此处的精选集从其输出中挑出）。</sub>

</div>

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小权限漂移: 叠加最小权限差异：高亮显示异常访问（已观测但未许可）以及未使用的授权。"></picture><br><sub><b>最小权限漂移</b> — 叠加最小权限差异：高亮显示异常访问（已观测但未许可）以及未使用的授权。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="编排与 A2A: 智能体到智能体的拓扑——谁向谁委派、实时的委派流，以及声明的执行节奏。对通信图的读取属于特权操作并自审计。"></picture><br><sub><b>编排与 A2A</b> — 智能体到智能体的拓扑——谁向谁委派、实时的委派流，以及声明的执行节奏。对通信图的读取属于特权操作并自审计。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="清单: 在您的资产域中发现的每个智能体、会话、MCP、模型和身份。"></picture><br><sub><b>清单</b> — 在您的资产域中发现的每个智能体、会话、MCP、模型和身份。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/observability-dark.png"><img src="docs-site/public/console/observability-light.png" alt="可观测性与互操作: 基于标准的摄取健康度，以及与账本关联的追踪下钻。各项数据为引擎级（进程全局），而非按租户统计；标准固定到上游机构所声明的版本与成熟度。"></picture><br><sub><b>可观测性与互操作</b> — 基于标准的摄取健康度，以及与账本关联的追踪下钻。各项数据为引擎级（进程全局），而非按租户统计；标准固定到上游机构所声明的版本与成熟度。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/dashboards-dark.png"><img src="docs-site/public/console/dashboards-light.png" alt="高管概览: 一览成本、用量、风险与合规——深入操作视图可查看明细。"></picture><br><sub><b>高管概览</b> — 一览成本、用量、风险与合规——深入操作视图可查看明细。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/home-dark.png"><img src="docs-site/public/console/home-light.png" alt="概览: 一览您的 AI 资产域——清单、活动、风险、合规、支出与健康状况。"></picture><br><sub><b>概览</b> — 一览您的 AI 资产域——清单、活动、风险、合规、支出与健康状况。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="安全与取证: 护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。"></picture><br><sub><b>安全与取证</b> — 护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="会话录制查看器: 单个会话的代理活动和治理证据的统一时间线。"></picture><br><sub><b>会话录制查看器</b> — 单个会话的代理活动和治理证据的统一时间线。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/identity-dark.png"><img src="docs-site/public/console/identity-light.png" alt="身份与 NHI: SSO、SCIM、身份清单、NHI 生命周期、WIF 图谱与特权登录——经观测、治理与审计。"></picture><br><sub><b>身份与 NHI</b> — SSO、SCIM、身份清单、NHI 生命周期、WIF 图谱与特权登录——经观测、治理与审计。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/knowledge-dark.png"><img src="docs-site/public/console/knowledge-light.png" alt="数据、知识与上下文: 受治理的知识库、检索溯源、提示词注册表、智能体记忆与上下文策略。"></picture><br><sub><b>数据、知识与上下文</b> — 受治理的知识库、检索溯源、提示词注册表、智能体记忆与上下文策略。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-apply-refused-dark.png"><img src="docs-site/public/console/work-apply-refused-light.png" alt="计划: 正在规划变更。此步骤不写入任何内容。"></picture><br><sub><b>计划</b> — 正在规划变更。此步骤不写入任何内容。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="紧急关停开关: 资产域紧急停止：一键即可中止所有受治理的执行面。触发的代价被刻意设计得很低；恢复则需要两个不同的用户账户以及一次强制的事后复核。"></picture><br><sub><b>紧急关停开关</b> — 资产域紧急停止：一键即可中止所有受治理的执行面。触发的代价被刻意设计得很低；恢复则需要两个不同的用户账户以及一次强制的事后复核。</sub> |

<a name="install"></a>
## 安装

每个发布版本都在一条**cosign 签名的信任链**下交付——一份覆盖每个构件的 cosign 签名校验和清单，归档与静态二进制文件由它间接覆盖；每个归档附带 SBOM in-toto 证明；容器镜像由 cosign 直接签名，并附带容器镜像的 SBOM 证明，Helm chart 也由 cosign 直接签名；整组发布附带 SLSA 构建溯源。对于安全产品而言，供应链是信任模型的一部分，所以请在运行前 [校验它](docs/RELEASE-VERIFICATION.md)。完整的逐操作系统矩阵与生产环境配置见 [`INSTALL.md`](INSTALL.md)；部署教程（Compose、Kubernetes/Helm、离线）见 [`docs-site/`](docs-site/)。

引擎**默认即安全**：它绑定到回环地址，首次启动时以自签名证书提供 HTTPS，不附带任何默认凭据，并在控制台打印一个一次性的安装令牌。你运行的第一条命令就是安全的那条。

**从源码构建**（在首个打标签的发布版本之前所支持的路径）：

```sh
# Build the single binary (Go 1.26+, Task, pnpm — the web console is embedded).
task build

# Start it — one guided, secure-by-default command (TLS on, loopback-only, no
# default credentials). It prints your console URL and a one-time setup token.
./bin/olivares quickstart
```

**自首个发布版本起**，推荐路径将变为单步的可校验安装——`.deb`/`.rpm`/`.apk` 包配以加固的 systemd 单元、一个多架构 Docker 镜像、一个 Homebrew cask 和一个 Helm chart，每一种都被该发布版本的 cosign 签名校验和清单所覆盖（镜像为直接签名），每一种都可一步安装且依然默认即安全。这些尚未发布；在标签落地之前，请按上文从源码构建。**Windows** 尚未构建——请运行 Linux 容器或从源码构建（[计划见 `INSTALL.md`](INSTALL.md#windows)）。

> 想先随意看看、暂不接入真实数据源？一个合成资产域可用一条命令在回环地址上运行——参见下方的[快速上手](#quickstart)。

<a name="quickstart"></a>
## 快速上手

两种入门方式：立即探索一个合成资产域，或将引擎指向一个真实数据源。两者运行的都是同一个真实二进制文件。

### 五分钟评估

1. 使用 `task build` 构建（Go 1.26+、Task、pnpm；见[前置条件](#quickstart-prerequisites)）。
2. 使用下方步骤 2a 中的确切命令启动演示资产域。
3. 在控制台中检查访问映射及其 Permitted-vs-Observed 漂移（20 个节点 / 13 条边，含 8 处意外访问和 2 项未使用授予）、一条 Cedar 策略和一个审批流、合规证据视图（26 个框架目录）、一个 FinOps 预算。
4. 然后阅读哪些是真实可用、哪些是计划项：上方功能矩阵、[工作平面](#the-work-plane)与 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md)。

<a name="quickstart-prerequisites"></a>
从源码构建的前置条件：Go 1.26+、[Task](https://taskfile.dev)（go-task）和 pnpm（Web UI 已内嵌）。完整的开发环境搭建见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

**1. 构建：**

```sh
task build && ./bin/olivares version
```

**2a. 探索演示资产域**——通过真实引擎产生的合成观测数据，仅限回环地址（它会拒绝非回环地址），无真实数据：

```sh
./bin/olivares serve --seed-demo --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$(mktemp -d)"
```

打开 `http://127.0.0.1:8901`，用启动横幅中的演示凭据登录，并浏览控制台——清点、访问映射及其漂移、会话、编排、策略、FinOps、合规。演示种子仅供学习（密码在公开源码树中）；切勿将其指向真实数据。

**2b. 或者正式启动它**——一条受引导、默认即安全的命令：

```sh
./bin/olivares quickstart        # TLS on, loopback; prints the console URL + a one-time setup token
```

在打印出的 URL 处打开控制台，用该令牌创建你的第一个管理员——无需 curl，无需额外步骤。（`olivares serve` 是同一引擎，带显式标志，面向生产环境与容器。）然后连接一个数据源。[完整快速上手](docs-site/src/content/docs/start/quickstart.md) 会针对一份 PostgreSQL 审计日志接入一个**真实 pgAudit connector**——无演示种子——并链接生产安装路径（systemd、Docker Compose、经 [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) 的 Kubernetes、离线）。

演示资产域是确定性的。这些数字并非愿景式的——`task smoke:quickstart` 会针对真实二进制文件走这同一条路径（使用它自己的端口与数据目录），并对上文列出的访问映射和漂移计数做断言，因此本节不会悄然偏离代码。

<a name="architecture"></a>
## 架构

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840"
       alt="架构：代理界面、审计源、MCP 与 A2A 对端以及内容源通过三种方式汇入单一自托管 Go 二进制文件，该文件内嵌控制台，承载产品模块、策略与执行层，以及位于按租户限定范围的存储之上的已签名证据账本；它提供控制台、REST API、聚焦的 gRPC 子集、CLI 和 Terraform provider，云控制平面（已构建，尚未部署）与许可门户（已部署，交付功能关闭）被画为独立平面。">
</picture>
</div>

引擎是一个单一静态 Go 二进制文件（`olivares`），内嵌 Web UI，并通过四个界面暴露其能力，每个界面都有成文的覆盖范围：一个 REST API（主界面）、一个聚焦且已冻结的稳定核心 gRPC 镜像、`olivares` CLI 本身——68 个已分组的顶级命令，从 `quickstart` 和 `serve` 到 `work`、`orchestration`、`agent`、`mcp` 和 `compliance`，并有测试确保帮助组完整，使新命令不能在未分组状态下落地——以及一个面向配置即代码资源的 Terraform provider。采集器以三种模式运行于客户的基础设施内部：进程内的快速路径数据源，由引擎通过一条经认证的、按启动建立的通道（AutoMTLS）监管的进程外插件，以及可选启用的、经过验证客户端证书双向 TLS 的远程“采集器→核心”部署。核心将数据存储于 SQLite（单节点、离线）或带行级安全的 Postgres，其中每个模块操作都在存储 API 中绑定到一个租户，Postgres 再通过 FORCE 行级安全强制执行。若应用角色的权限足以静默绕过它（superuser 或 `BYPASSRLS`），则会在启动时被拒绝；越过该拒绝的唯一方式是一个明确说明其代价的 opt-in 标志。跨租户的系统读取通过一个独立、遵循最小权限的 `BYPASSRLS` 管理连接池进行，该连接池绝不用于租户范围内的工作——一扇已声明的门，而非不存在的门。

总览：[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## 开放核心，按目录划分

授权从第一个 commit 起就已定型：**开放核心**——完整的产品置于 AGPL 之下，宽松许可的 SDK 和连接器让生态无需 copyleft 摩擦即可成长，外加一小组**增量**商业附加组件——仅通过 `-tags enterprise` 构建，各自按其商业条款单独授权，且不包含在公开二进制文件中——用于保留的能力。AGPL 构建即完整的治理平台，绝不会为了向上销售而被阉割；商业附加组件*增添*从未出现在开放产品中的新代码——因此企业构建与开放构建并不完全相同，但开放发行的内容不会被拿走任何东西。每个源文件都带有一个 `SPDX-License-Identifier` 头，并在 CI 中强制执行。

| 目录 | 许可 | 内容 |
|---|---|---|
| `core/` | `AGPL-3.0-only` | 引擎：摄取、事件总线、数据模型、模块运行时、API、认证/授权、审计、多租户 |
| `modules/` | `AGPL-3.0-only` | 30 个产品模块（清点、访问映射、工作与租约、身份、FinOps、评测、护栏，……） |
| `web/` | `AGPL-3.0-only` | React UI，通过 `go:embed` 内嵌进二进制文件 |
| `sdk/` | `Apache-2.0` | 稳定的 `SourceConnector` / `OutputConnector` / `Module` 接口 + gRPC 契约 + 类型 |
| `connectors/` | `Apache-2.0` | 第一方与社区连接器（Claude、MCP、pg-audit、eBPF、云、SIEM，……） |
| `clients/` | `Apache-2.0` | 生成的客户端 SDK（Go、Java、Python、TypeScript） |
| 商业附加组件*（独立的私有仓库）* | `LicenseRef-Olivares-Commercial` | 增量、单独授权的附加组件家族，横跨执行、MCP、身份、数据安全、合规深度、运营与平台——在[上方矩阵](#whats-open-whats-enterprise-whats-planned)中逐区域列举，每一个都是 [`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go) 中已声明的接缝——仅通过 `-tags enterprise` 构建，绝不出现在本仓库或公开二进制文件中 |
| `docs/`、`docs-site/` | — | 设计文档与产品文档站点 |

连接器只能从 `sdk/` 导入，绝不能从 `core/` 导入。这使 AGPL / Apache 边界保持干净，并让第三方能在无 copyleft 义务的情况下编写连接器——由 CI 中的 [`scripts/check-boundary.sh`](scripts/check-boundary.sh) 强制执行。

## 安全与供应链

Olivares AI 运行在客户主机上并映射每个 agent 能触及之物，因此其安全标准在设计上就很高：读优先；观测平面内的最小数据（访问映射只存边，不存载荷——受治理的 Knowledge store 只保存你显式摄取的内容）；最小权限；mTLS；仅追加的哈希链审计配以签名检查点；签名的发布版本。访问映射本身就是一个特权的、受审计的界面——打开它是一次被记录的操作，读取 agent 间通信图也是如此。

报告漏洞或阅读披露政策请见 [`SECURITY.md`](SECURITY.md)（私密报告——切勿用公开 issue）。公告流程记录于 [`docs/security-advisories.md`](docs/security-advisories.md)；供应链就绪证据记录于 [`docs/openssf-badge.md`](docs/openssf-badge.md) 的 Best Practices 映射中。

<a name="documentation"></a>
## 文档

产品文档位于 [`docs-site/`](docs-site/)——一个 Diátaxis 站点，包含经测试的安装教程（单节点、Docker Compose、Kubernetes/Helm、离线）、带真实控制台截图的逐连接器指南、一本 cookbook（deny-closed 策略、预算、审批、kill-switch 演练、SIEM 推送）、API 参考和一份术语表。从 [什么是 Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) 和 [诚实与边界](docs-site/src/content/docs/start/honesty-and-limits.md) 开始——后者明确说明今天哪些可运行、哪些处于设计阶段，以及产品刻意不做哪些事。

## 社区与治理

采用者所期待的社区健康与治理文件均已齐备并保持最新：

- **决策如何形成：** [`GOVERNANCE.md`](GOVERNANCE.md)（maintainer 主导 / 开放核心，对项目所处阶段保持诚实）以及 [`.github/CODEOWNERS`](.github/CODEOWNERS)（评审路由映射到许可前沿）。
- **贡献：** [`CONTRIBUTING.md`](CONTRIBUTING.md)（环境搭建、DCO/CLA、SPDX、连接器边界）——每项变更都通过 [pull-request 模板](.github/PULL_REQUEST_TEMPLATE.md) 提交。
- **行为准则：** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md)（Contributor Covenant 2.1）。
- **获取帮助：** [`SUPPORT.md`](SUPPORT.md)——以及哪些地方**不应**报告安全问题。
- **变更：** [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog 1.1 + CalVer `vYY.M.PATCH`；beta）。

## 许可

产品（`core/`、`modules/`、`web/`）采用 **GNU Affero General Public License, version 3**（`AGPL-3.0-only`）授权。连接器 SDK、连接器和客户端 SDK（`sdk/`、`connectors/`、`clients/`）采用 **Apache-2.0** 授权。具体文件所适用的许可证由其 SPDX 头标明；对于发布版本，则由其 SBOM 标明。

> **无担保、无责任——部署前请先阅读。** 本自由软件按**现状**提供，**不提供任何形式的担保**，且**对数据丢失、损坏、业务中断或利润损失不承担责任**。在控制平面上这并非形式条款：一次错误配置既可能阻断正当工作、中断生产，也可能恰好放行你本想拦截的内容。适用 AGPL-3.0-only §§15–16 与 Apache-2.0 §§7–8，以及本项目依据 AGPL §7(a) 的自有补充条款——完整文本（含高风险用途、合规结果与第三方组件）见 [`DISCLAIMER.md`](DISCLAIMER.md)。

对于无法在 AGPL 条款下运营的组织，可获得提供 AGPL 私有例外的**商业许可**。增量的 `enterprise/` 能力——即[上方矩阵](#whats-open-whats-enterprise-whats-planned)中逐区域列举的附加组件家族，每一个都是公开源码树中已声明的接缝——作为**单独的可选附加组件**、按各自的商业条款提供：闭源代码仅通过 `-tags enterprise` 构建，绝不出现在开放二进制中。打包与价格请垂询。AGPL 核心本身是完整的，绝不会从内部设置功能上限。商业授权或企业咨询请联系 `enterprise@olivares.ai`。参见 [`LICENSING.md`](LICENSING.md)。

贡献需要 DCO 签署（`git commit -s`）和一份 Contributor License Agreement；参见 [`CONTRIBUTING.md`](CONTRIBUTING.md) 和 [`CLA.md`](CLA.md)。

## 支持这个项目

Olivares AI 采用 AGPL-3.0 并自托管：核心免费，并将保持如此。如果它对你有用，并且你愿意直接支持这项工作，可以通过本仓库的 **Sponsor** 按钮赞助。

赞助**不是**支持合同，也不购买优先级：问题与缺陷报告的处理方式见 [`SUPPORT.md`](SUPPORT.md)；商业条款与 enterprise 附加组件见 [`LICENSING.md`](LICENSING.md)。

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>企业 AI 的事实基准（ground truth）。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>

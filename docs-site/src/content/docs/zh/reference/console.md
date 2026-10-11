---
title: 控制台参考——每个屏幕及其所需权限
description: >-
  Olivares AI 控制台发布的每条路由，按控制台的区域分组，并列出各自需要的
  RBAC 权限及产品内帮助链接打开的参考页面。由控制台自己的路由清单生成。
---

本页是控制台的地图。它列出**应用挂载的每条路由**——不是选集，也不是某人记得
写下的那些——以及主体进入路由所需的权限和更多信息所在的位置。

控制台只有**一个导航结构：带分区的九个区域**。每个屏幕都位于一个区域及其中的一个
分区，所有显示屏幕位置的地方都指向同一处：“所有区域”（侧边栏中，手机上为“更多”）、
区域目录页、面包屑、命令面板，以及屏幕上方的链接行——它列出同一分区的其他屏幕。
侧边栏固定一小组屏幕，首要工作在前（主页、会话、AI 工具、审批），然后是工作，固定的屏幕在其分区的其他屏幕上也保持
选中标记。导航过滤会把分组树替换成已授权匹配项的排序列表；命令面板使用同一索引、
同一排序和同一权限投影。区域目录是链接页——列出你的权限允许的条目——不是能力、
可用性或就绪状态的实时读数。

新安装只列出首要工作：主页、会话、AI 工具及提供方、审批、配置向导和设置。其他屏幕在新安装中
属于预览：其地址、API 和 CLI 照常可用，但导航、命令面板和快捷键不会列出它们。升级前已存在的
安装会继续列出它原来列出的所有屏幕；管理员已选择模块的安装（自下次启动起）和使用演示数据的
安装也是如此。

本页是**生成的**。名册来自 `web/src/features/route-census.json`，这是一份只追加的
清单，`registry.route-conservation.test.ts` 会将它与构建后的路由器固定比对，因此
任何屏幕的新增、移动或丢失都会引起本页变化。每个屏幕的名称和单行描述都是
**控制台自己的字符串**，来自侧边栏使用的同一翻译目录，所以你在这里读到的就是
在产品中看到的内容。下面的表格按区域分组这些行：主页（概览）在前，登录、设置和账户在后。九个区域
目录页与功能注册表之外挂载的路由列在同一张表中。

:::note[权限由引擎强制实施，而不是由此表实施]
`需要`列说明控制台在提供路由前，根据引擎返回的有效权限检查哪些权限。
引擎独立授权 API 请求，包括来自控制台之外的请求。条目可见并不表示模块已完成配置或
已准备好运行。请参阅[角色与权限](/zh/reference/modules/vi-governance/)。
:::

## 如何阅读本页

- **屏幕**——侧边栏、区域目录和命令面板使用的名称。
- **路径**——相对于部署控制台 origin 的 URL。它是已发布契约：书签、runbook
  深层链接和文档交叉引用都使用这段字符串。
- **需要**——RBAC 权限。`任何已登录用户`表示路由向所有已认证主体开放；
  **无需登录**表示它在建立任何会话前即可提供。
- **参考**——控制台为该屏幕提供的帮助链接所打开的页面。

下面的标题就是各个区域，顺序与“所有区域”一致：基础设施、AI、数据与上下文、
工作与通信、自动化、安全与身份、部署、可观测性与证据，然后是系统与设置。

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

控制台发布 **83 条路由**。以下表格列出了每一条路由、所需权限，以及产品内帮助链接
打开的参考页面。

### 主页

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 主页 | `/` | 基础设施总览和健康情况 | 任何已登录用户 | [文档主页](/zh/) |

### 基础设施

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 环境 | `/estate` | 查看已配置的资源及其关系，包括工作项的依赖关系。 | `sessions:run:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 清单 | `/inventory` | 发现并编目连接器观察到的智能体、MCP 服务器与模型。 | `inventory:catalog:read` | [reference/modules/i-inventory](/zh/reference/modules/i-inventory/) |
| 工作区 | `/workspace` | 限定在一个工作区内的 Agent、会话、资源和活动 | `tenant:read` | [reference/modules/xx-multi-tenancy](/zh/reference/modules/xx-multi-tenancy/) |

### AI

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 智能体工具 | `/agent-tools` | 检测、安装和更新此主机上的智能体工具，并跟踪每次安装；仅限部署管理员 | `system:admin` | [how-to/add-a-provider](/zh/how-to/add-a-provider/) |
| 会话 | `/agentops` | 启动会话并跟踪每个会话的工作，包括 Olivares 发现的会话 | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/zh/how-to/run-claude-code-with-olivares/) |
| MCP 服务器 | `/mcp-servers` | 将远程 MCP 服务器连接到此组织，测试它们，并选择会话可以使用哪些工具 | `tenant:admin` | [how-to/connectors/mcp-governance](/zh/how-to/connectors/mcp-governance/) |
| 模型运维 | `/model-operations` | 自有模型、准入和部署 | `models:registry:read` | [reference/modules/xxiii-model-operations](/zh/reference/modules/xxiii-model-operations/) |
| 模型 | `/models` | 模型、路由和提供商密钥 | `models:catalog:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |
| 平台 | `/platforms` | 部署表面、合规矩阵和各平台模型生命周期 | `models:platforms:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |
| 提供商账户 | `/provider-accounts` | 列出已命名的提供商账户，并将现有提供商配置文件采纳为账户 | `sessions:account:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 来源绑定 | `/provider-bindings` | 将已配置的来源，以本节点应用的修订版本，专用于提供商配置文件 | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 提供商配置文件 | `/provider-profiles` | 登记并管理会话启动所依据的提供商主目录，并按需读取其配置 | `sessions:profile:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 提供方 | `/providers` | 注册会话启动时使用的 API 密钥与端点；可测试、更换和吊销 | `sessions:provider:read` | [how-to/add-a-provider](/zh/how-to/add-a-provider/) |
| 速率限制 | `/rate-limits` | Anthropic 速率限制清单（只读） | `models:ratelimits:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |
| 沙箱 | `/sandbox` | 隔离的 Agent 测试与重放 | `sandbox:run:read` | [reference/modules/xvii-sandbox](/zh/reference/modules/xvii-sandbox/) |
| 会话 | `/sessions` | 启动会话并跟踪每个会话的工作，包括 Olivares 发现的会话 | `sessions:live:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 语音 | `/voice` | 语音和实时会话 | `voice:session:read` | [reference/modules/xvi-voice](/zh/reference/modules/xvi-voice/) |
| 工作区模板 | `/workspace-templates` | 可复用的会话配置快照：hook、设置、连接器和策略。 | `sessions:template:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |

### 数据与上下文

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| Agent 制品 | `/agent-artifacts` | 技能、MCP 扩展和指令文件——注册表、状态和供应链 BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/zh/reference/modules/xxiii-model-operations/) |
| MCP 与技能 | `/capabilities` | 治理 MCP 服务器、技能和工具 | `capabilities:catalog:read` | [reference/modules/v-capabilities](/zh/reference/modules/v-capabilities/) |
| 目录 | `/catalog` | 策展并获批准的 Agent 和能力 | `catalog:entry:read` | [reference/modules/xiv-catalog](/zh/reference/modules/xiv-catalog/) |
| 知识 | `/knowledge` | 知识库、RAG 和数据沿袭 | `knowledge:kb:read` | [reference/modules/viii-knowledge](/zh/reference/modules/viii-knowledge/) |
| 技能目录 | `/skills` | 浏览技能包并分配给部门、agent 组和 agent | `skills:catalog:read` | [reference/console](/zh/reference/console/) |

### 工作与通信

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 通信 | `/communications` | 所选工作区的频道、直接通知与个人收件箱 | `sessions:channel:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 频道管理 | `/communications/administration` | 管理频道：配置与授权历史，每次操作都在频道当前 ETag 下 | `sessions:channel:admin` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 交接 | `/communications/handoffs` | 发给你的工作责任交接提议：阅读上下文后接受或拒绝 | `sessions:delivery:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 通信收件箱 | `/communications/inbox` | 你的精确收件箱：仅发给你的投递，每次重新读取并显式确认 | `sessions:delivery:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 新建频道 | `/communications/new` | 以显式初始授权创建频道 | `sessions:channel:write` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 协议绑定 | `/communications/protocol-bindings` | 组合并协调受治理的 A2A 和 MCP 绑定 | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 工作 | `/work` | 共享工作：工作项、依赖关系、验收和决定 | `sessions:work:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |

### 自动化

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 告警 | `/alerting` | 将发现项路由到目的地并检查投递 | `notify:route:read` | [reference/modules/xv-notify](/zh/reference/modules/xv-notify/) |
| 自动化 | `/automations` | 所有三条自动化轨道及其触发器目录 | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/zh/reference/modules/iv-orchestration/) |
| Webhook 与事件 | `/eventing` | 出站 webhook 订阅、投递日志和死信队列。 | `eventing:subscription:read` | [reference/modules/eventing](/zh/reference/modules/eventing/) |
| 编排 | `/orchestration` | Agent 间协调与计划 | `orchestration:graph:read` | [reference/modules/iv-orchestration](/zh/reference/modules/iv-orchestration/) |

### 安全与身份

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 访问图 | `/access-map` | 每个 Agent 读取和写入的内容（R/RW） | `accessmap:graph:read` | [reference/modules/iii-access-map](/zh/reference/modules/iii-access-map/) |
| AgentCore 导出 | `/agentcore-export` | 规划、审阅并应用将本租户治理规则投射为 AWS AgentCore Cedar 策略；规划不写入任何内容 | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| Claude Code 治理 | `/claude-policy` | 托管策略、hook、MCP、沙箱和策略即代码 | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/zh/how-to/connectors/claude-code-hooks-pep/) |
| 身份与 NHI | `/identity` | SSO、SCIM、NHI 名册和 WIF 图 | `governance:identity:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| 推理代理 | `/inference-proxy` | 代理门禁、出站 DLP 规则和设备批准 | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/zh/reference/modules/inferenceproxy/) |
| 紧急开关 | `/killswitch` | 紧急停止、双人控制恢复和 guardian 遏制 | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/zh/how-to/cookbook/kill-switch-drill/) |
| 权限 | `/permissions` | 身份、角色和批准 | `governance:identity:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| 红队测试 | `/red-team` | 对 Agent 进行对抗性测试 | `redteam:target:read` | [reference/modules/xviii-redteam](/zh/reference/modules/xviii-redteam/) |
| 数据驻留 | `/residency` | 将每个组织固定到某个区域，或保持不固定 | `system:admin` | [reference/modules/xiii-compliance](/zh/reference/modules/xiii-compliance/) |
| 例行策略 | `/routine-policies` | Claude Code 例行任务的周期下限、并发上限、批准要求和 cron allowlist。 | `governance:routine:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| 安全 | `/security` | 护栏、取证和异常 | `security:finding:read` | [reference/modules/ix-security](/zh/reference/modules/ix-security/) |

### 部署

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 部署 | `/deploy` | 配置 Agent 并将其接入基础设施 | `deploy:deployment:read` | [reference/modules/vii-deploy](/zh/reference/modules/vii-deploy/) |
| Git 发布 | `/git-publication` | 通过已批准的 Git 目标推送提交、创建拉取请求并合并 | `gitpublish:target:read` | [reference/modules/gitpublish](/zh/reference/modules/gitpublish/) |

### 可观测性与证据

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| Claude Code 采用情况 | `/adoption` | 生产力、接受率和模型组合 | `adoption:metrics:read` | [reference/modules/claudeadoption](/zh/reference/modules/claudeadoption/) |
| 供应链 | `/attestation` | 发布证明——SLSA、SBOM、VEX 和 Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/zh/how-to/verify-a-release/) |
| 审计账本 | `/audit` | 可检测篡改的证据账本 | `audit:read` | [reference/modules/ix-security](/zh/reference/modules/ix-security/) |
| 合规 | `/compliance` | 框架、控制和证据 | `compliance:framework:read` | [reference/modules/xiii-compliance](/zh/reference/modules/xiii-compliance/) |
| 仪表板 | `/dashboards` | 管理层 KPI 和报告 | 任何已登录用户 | [reference/modules/xxi-executive-dashboards](/zh/reference/modules/xxi-executive-dashboards/) |
| 评估 | `/evals` | 质量、评估和回归 | `evals:run:read` | [reference/modules/xii-evals](/zh/reference/modules/xii-evals/) |
| 成本与 FinOps | `/finops` | Token 成本、预算和支出 | `finops:spend:read` | [reference/modules/xi-finops](/zh/reference/modules/xi-finops/) |
| 健康与 SLA | `/health` | Agent 和 MCP 的运行时间及 SLA | `health:status:read` | [reference/modules/xxii-health](/zh/reference/modules/xxii-health/) |
| 可观测性 | `/observability` | 按标准查看摄取健康状况和追踪下钻 | `health:status:read` | [reference/modules/observability](/zh/reference/modules/observability/) |
| 状态导出 | `/posture-export` | 为控制塔导出事实状态 | `posture:export:read` | [reference/modules/posture-export](/zh/reference/modules/posture-export/) |
| 录制 | `/recordings` | 特权会话录制和重放 | `recording:session:admin` | [reference/modules/recording](/zh/reference/modules/recording/) |
| 报告 | `/reporting` | 生成和下载治理报告 | `reporting:report:read` | [reference/modules/reporting](/zh/reference/modules/reporting/) |
| 会话查看器 | `/session-viewer/$id`（仅深层链接） | 一个已录制会话的完整时间线；从“录制”中的行进入，而不是从侧边栏进入。 | `recording:session:admin` | [reference/modules/recording](/zh/reference/modules/recording/) |
| 团队成本 | `/team-costs` | 按团队归属的支出，可展开到每个项目和模型的明细。 | `finops:spend:read` | [reference/modules/xi-finops](/zh/reference/modules/xi-finops/) |

### 系统与设置

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| API Playground | `/api-playground` | 交互式探索和测试控制平面 API | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/zh/reference/modules/xix-api-manage-as-code/) |
| 备份 | `/backups` | 触发、计划、下载和恢复备份，并在破坏性路径上进行第二次确认。 | `system:admin` | [how-to/backup-and-restore](/zh/how-to/backup-and-restore/) |
| 管理 | `/console` | 用户、SSO/IdP、工作区、代理组、角色、密钥、连接器、API 密钥以及此安装的许可证 | `tenant:admin` | [reference/modules/xx-multi-tenancy](/zh/reference/modules/xx-multi-tenancy/) |
| 源差异 | `/console/sources/diff` | 逐个文件比较已连接 Git 仓库的基准修订与 head 修订 | `system:admin` | [reference/console](/zh/reference/console/) |
| 日志 | `/logs` | 实时引擎日志流，可按级别和模块过滤，并支持搜索和暂停。 | `system:admin` | [how-to/troubleshooting](/zh/how-to/troubleshooting/) |
| 设置向导 | `/onboarding` | 分步部署配置 | `system:admin` | [start/quickstart](/zh/start/quickstart/) |
| 租户 | `/tenants` | 撤销或恢复租户服务 | `system:admin` | [how-to/troubleshooting](/zh/how-to/troubleshooting/) |

### 登录、设置与账户

这些路由挂载在功能注册表之外。标记为**无需登录**的路由在会话建立前提供——它们是
唯一如此工作的控制台路由。

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 接受邀请 | `/accept-invite` | 电子邮件邀请链接的落点：受邀者设置密码并加入工作区，无需预先建立会话。 | **无需登录** | — |
| AI | `/areas/ai` | AI区域目录：会话观测与操作、提供商配置文件与环境、模型、专项执行和提供商参考。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 自动化 | `/areas/automation` | 自动化区域目录：流程与编排、事件与通知。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 数据与上下文 | `/areas/data-context` | 数据与上下文区域目录：能力、知识与工件。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 部署 | `/areas/deployment` | 部署区域目录：部署的准备与控制。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 基础设施 | `/areas/infrastructure` | 基础设施区域目录：环境清单与工作区。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 可观测性与证据 | `/areas/observation` | 可观测性与证据区域目录：状态与活动、成本与采用、审计、评估与证据。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 安全与身份 | `/areas/security-identity` | 安全与身份区域目录：身份与访问、策略与治理边界、防护与响应。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 系统与设置 | `/areas/system` | 系统与设置区域目录：管理、安装与维护、开发者工具和个人偏好。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 工作与通信 | `/areas/work-communications` | 工作与通信区域目录：持久保存的跨会话工作清单与受治理的通信。列出您的权限允许访问的条目。 | 任何已登录用户 | — |
| 登录 | `/login` | 已配置账户使用凭据和令牌登录的页面。 | **无需登录** | — |
| 设置 | `/settings` | 工作区和账户设置 | 任何已登录用户 | — |
| 首次运行设置 | `/setup` | 将全新部署变为可用部署的一次性页面：使用设置令牌并创建第一个所有者账户。 | **无需登录** | — |
| 公共状态 | `/status-page` | 面向未登录用户的组件健康状态，在页面打开时自动刷新。 | **无需登录** | — |

<!-- END GENERATED olivares-console-routes -->

## 本页未告诉你的内容

这是地图，不是手册。它说明有哪些屏幕、位于何处以及谁可以打开；不会引导你完成
任务。请从[按角色选择路径](/zh/start/paths-by-role/)或
[操作指南](/zh/how-to/self-hosting/)开始。

后端在操作员完成配置前会拒绝关闭的屏幕，与其他屏幕一样出现在这里——路由存在，
权限也真实有效。哪些模块启用、哪些受门控，记录在[模块概览](/zh/reference/modules/overview/)；
[诚实性与限制](/zh/start/honesty-and-limits/)页面说明了通用规则。区域目录列表不是能力或
就绪状态的实时读数。

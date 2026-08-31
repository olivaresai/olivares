---
title: 控制台参考——每个屏幕及其所需权限
description: >-
  Olivares AI 控制台发布的每条路由，按五个中心分组，并列出各自需要的 RBAC
  权限及产品内帮助链接打开的参考页面。由控制台自己的路由清单生成。
---

本页是控制台的地图。它列出**应用挂载的每条路由**——不是选集，也不是某人记得
写下的那些——以及主体进入路由所需的权限和更多信息所在的位置。

本页是**生成的**。名册来自 `web/src/features/route-census.json`，这是一份只追加的
清单，`registry.route-conservation.test.ts` 会将它与构建后的路由器固定比对，因此
任何屏幕的新增、移动或丢失都会引起本页变化。每个屏幕的名称和单行描述都是
**控制台自己的字符串**，来自侧边栏使用的同一翻译目录，所以你在这里读到的就是
在产品中看到的内容。

:::note[权限由引擎强制实施，而不是由此表实施]
`需要`列是控制台在提供路由前检查的权限，它映射引擎的 RBAC。引擎仍是权威：直接
访问你没有权限的屏幕时，API 会拒绝请求，而不只是从侧边栏隐藏它。请参阅
[角色与权限](/zh/reference/modules/vi-governance/)。
:::

## 如何阅读本页

- **屏幕**——侧边栏和命令面板使用的名称。
- **路径**——相对于部署控制台 origin 的 URL。它是已发布契约：书签、runbook
  深层链接和文档交叉引用都使用这段字符串。
- **需要**——RBAC 权限。`任何已登录用户`表示路由向所有已认证主体开放；
  **无需登录**表示它在建立任何会话前即可提供。
- **参考**——控制台为该屏幕提供的帮助链接所打开的页面。

下面五个标题是控制台的中心，顺序与侧边栏渲染顺序相同。

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

控制台发布 **59 条路由**。以下表格列出了每一条路由、所需权限，以及产品内帮助链接
打开的参考页面。

### 运维

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 概览 | `/` | 基础设施总览和健康情况 | 任何已登录用户 | [文档主页](/zh/) |
| Claude Code | `/agentops` | 创建、接入并治理 Claude Code 会话——无需 SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/zh/how-to/run-claude-code-with-olivares/) |
| 备份 | `/backups` | 触发、计划、下载和恢复备份，并在破坏性路径上进行第二次确认。 | `system:admin` | [how-to/backup-and-restore](/zh/how-to/backup-and-restore/) |
| 健康与 SLA | `/health` | Agent 和 MCP 的运行时间及 SLA | `health:status:read` | [reference/modules/xxii-health](/zh/reference/modules/xxii-health/) |
| 紧急开关 | `/killswitch` | 紧急停止、双人控制恢复和 guardian 遏制 | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/zh/how-to/cookbook/kill-switch-drill/) |
| 日志 | `/logs` | 实时引擎日志流，可按级别和模块过滤，并支持搜索和暂停。 | `system:admin` | [how-to/troubleshooting](/zh/how-to/troubleshooting/) |
| 可观测性 | `/observability` | 按标准查看摄取健康状况和追踪下钻 | `health:status:read` | [reference/modules/observability](/zh/reference/modules/observability/) |
| 沙箱 | `/sandbox` | 隔离的 Agent 测试与重放 | `sandbox:run:read` | [reference/modules/xvii-sandbox](/zh/reference/modules/xvii-sandbox/) |
| 会话 | `/sessions` | 实时 Agent 操作和时间线 | `sessions:live:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 租户 | `/tenants` | 撤销或恢复租户服务 | `system:admin` | [how-to/troubleshooting](/zh/how-to/troubleshooting/) |
| 语音 | `/voice` | 语音和实时会话 | `voice:session:read` | [reference/modules/xvi-voice](/zh/reference/modules/xvi-voice/) |
| 工作 | `/work` | 跨会话持久 backlog：项目、依赖、验收和决定 | `sessions:work:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 工作区 | `/workspace` | 限定在一个工作区内的 Agent、会话、资源和活动 | `tenant:read` | [reference/modules/xx-multi-tenancy](/zh/reference/modules/xx-multi-tenancy/) |
| 工作区模板 | `/workspace-templates` | 可复用的会话配置快照：hook、设置、连接器和策略。 | `sessions:template:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |

### 自动化

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 告警 | `/alerting` | 将发现项路由到目的地并检查投递 | `notify:route:read` | [reference/modules/xv-notify](/zh/reference/modules/xv-notify/) |
| 自动化 | `/automations` | 所有三条自动化轨道及其触发器目录 | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/zh/reference/modules/iv-orchestration/) |
| Webhook 与事件 | `/eventing` | 出站 webhook 订阅、投递日志和死信队列。 | `eventing:subscription:read` | [reference/modules/eventing](/zh/reference/modules/eventing/) |
| 编排 | `/orchestration` | Agent 间协调与计划 | `orchestration:graph:read` | [reference/modules/iv-orchestration](/zh/reference/modules/iv-orchestration/) |

### 连接

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| API Playground | `/api-playground` | 交互式探索和测试控制平面 API | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/zh/reference/modules/xix-api-manage-as-code/) |
| MCP 与技能 | `/capabilities` | 治理 MCP 服务器、技能和工具 | `capabilities:catalog:read` | [reference/modules/v-capabilities](/zh/reference/modules/v-capabilities/) |
| 目录 | `/catalog` | 策展并获批准的 Agent 和能力 | `catalog:entry:read` | [reference/modules/xiv-catalog](/zh/reference/modules/xiv-catalog/) |
| 协议绑定 | `/communications/protocol-bindings` | 组合并协调受治理的 A2A 和 MCP 绑定 | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/zh/reference/modules/ii-sessions/) |
| 部署 | `/deploy` | 配置 Agent 并将其接入基础设施 | `deploy:deployment:read` | [reference/modules/vii-deploy](/zh/reference/modules/vii-deploy/) |
| 清单 | `/inventory` | 发现并编目每个 Agent、MCP 和模型 | `inventory:catalog:read` | [reference/modules/i-inventory](/zh/reference/modules/i-inventory/) |
| 知识 | `/knowledge` | 知识库、RAG 和数据沿袭 | `knowledge:kb:read` | [reference/modules/viii-knowledge](/zh/reference/modules/viii-knowledge/) |
| 模型运维 | `/model-operations` | 自有模型、准入和部署 | `models:registry:read` | [reference/modules/xxiii-model-operations](/zh/reference/modules/xxiii-model-operations/) |
| 模型 | `/models` | 模型、路由和提供商密钥 | `models:catalog:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |
| 设置向导 | `/onboarding` | 分步部署配置 | `system:admin` | [start/quickstart](/zh/start/quickstart/) |
| 平台 | `/platforms` | 部署表面、合规矩阵和各平台模型生命周期 | `models:platforms:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |

### 治理

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 访问图 | `/access-map` | 每个 Agent 读取和写入的内容（R/RW） | `accessmap:graph:read` | [reference/modules/iii-access-map](/zh/reference/modules/iii-access-map/) |
| AgentCore 导出 | `/agentcore-export` | 规划并应用到 AWS AgentCore 的 Cedar 策略导出，在执行前审查将发生的变化。 | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| Claude Code 治理 | `/claude-policy` | 托管策略、hook、MCP、沙箱和策略即代码 | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/zh/how-to/connectors/claude-code-hooks-pep/) |
| 控制台 | `/console` | 引导用户、连接 SSO/IdP，并塑造工作区和 Agent 组。 | `tenant:admin` | [reference/modules/xx-multi-tenancy](/zh/reference/modules/xx-multi-tenancy/) |
| 身份与 NHI | `/identity` | SSO、SCIM、NHI 名册和 WIF 图 | `governance:identity:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| 推理代理 | `/inference-proxy` | 代理门禁、出站 DLP 规则和设备批准 | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/zh/reference/modules/inferenceproxy/) |
| 权限 | `/permissions` | 身份、角色和批准 | `governance:identity:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |
| 速率限制 | `/rate-limits` | Anthropic 速率限制清单（只读） | `models:ratelimits:read` | [reference/modules/x-models](/zh/reference/modules/x-models/) |
| 数据驻留 | `/residency` | 将每个组织固定到某个区域，或保持不固定 | `system:admin` | [reference/modules/xiii-compliance](/zh/reference/modules/xiii-compliance/) |
| 例行策略 | `/routine-policies` | Claude Code 例行任务的周期下限、并发上限、批准要求和 cron allowlist。 | `governance:routine:read` | [reference/modules/vi-governance](/zh/reference/modules/vi-governance/) |

### 证明

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| Claude Code 采用情况 | `/adoption` | 生产力、接受率和模型组合 | `adoption:metrics:read` | [reference/modules/claudeadoption](/zh/reference/modules/claudeadoption/) |
| Agent 制品 | `/agent-artifacts` | 技能、MCP 扩展和指令文件——注册表、状态和供应链 BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/zh/reference/modules/xxiii-model-operations/) |
| 供应链 | `/attestation` | 发布证明——SLSA、SBOM、VEX 和 Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/zh/how-to/verify-a-release/) |
| 审计账本 | `/audit` | 可检测篡改的证据账本 | `audit:read` | [reference/modules/ix-security](/zh/reference/modules/ix-security/) |
| 合规 | `/compliance` | 框架、控制和证据 | `compliance:framework:read` | [reference/modules/xiii-compliance](/zh/reference/modules/xiii-compliance/) |
| 仪表板 | `/dashboards` | 管理层 KPI 和报告 | 任何已登录用户 | [reference/modules/xxi-executive-dashboards](/zh/reference/modules/xxi-executive-dashboards/) |
| 评估 | `/evals` | 质量、评估和回归 | `evals:run:read` | [reference/modules/xii-evals](/zh/reference/modules/xii-evals/) |
| 成本与 FinOps | `/finops` | Token 成本、预算和支出 | `finops:spend:read` | [reference/modules/xi-finops](/zh/reference/modules/xi-finops/) |
| 状态导出 | `/posture-export` | 为控制塔导出事实状态 | `posture:export:read` | [reference/modules/posture-export](/zh/reference/modules/posture-export/) |
| 录制 | `/recordings` | 特权会话录制和重放 | `recording:session:admin` | [reference/modules/recording](/zh/reference/modules/recording/) |
| 红队测试 | `/red-team` | 对 Agent 进行对抗性测试 | `redteam:target:read` | [reference/modules/xviii-redteam](/zh/reference/modules/xviii-redteam/) |
| 报告 | `/reporting` | 生成和下载治理报告 | `reporting:report:read` | [reference/modules/reporting](/zh/reference/modules/reporting/) |
| 安全 | `/security` | 护栏、取证和异常 | `security:finding:read` | [reference/modules/ix-security](/zh/reference/modules/ix-security/) |
| 会话查看器 | `/session-viewer/$id`（仅深层链接） | 一个已录制会话的完整时间线；从“录制”中的行进入，而不是从侧边栏进入。 | `recording:session:admin` | [reference/modules/recording](/zh/reference/modules/recording/) |
| 团队成本 | `/team-costs` | 按团队归属的支出，可展开到每个项目和模型的明细。 | `finops:spend:read` | [reference/modules/xi-finops](/zh/reference/modules/xi-finops/) |

### 登录、设置与账户

这些路由挂载在功能注册表之外。标记为**无需登录**的路由在会话建立前提供——它们是
唯一如此工作的控制台路由。

| 屏幕 | 路径 | 用途 | 需要 | 参考 |
|---|---|---|---|---|
| 接受邀请 | `/accept-invite` | 电子邮件邀请链接的落点：受邀者设置密码并加入工作区，无需预先建立会话。 | **无需登录** | — |
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
[诚实性与限制](/zh/start/honesty-and-limits/)页面说明了通用规则。

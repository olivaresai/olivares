---
title: 模块目录
description: >-
  Olivares AI 的 30 个模块 —— 按九大能力领域组织，并附上每个模块诚实的成熟度。
  Olivares AI 唯一可信事实源：Claude Code 处于最深层次，Codex 与 Grok Build 并列其旁，用于在企业中集成、管理和保护 AI；
  本文是逐模块的参考。
---

Olivares AI 以单一可信事实源构建（Claude Code 处于最深层次，Codex 与 Grok Build 并列其旁），用于在企业中集成、管理和保护 AI。
它是一个**模块化平台** —— 一个引擎、一个控制台、**30 个模块**接入到单一二进制文件中
—— 它观察 agent 在何处运行，治理它们被允许做什么，并（在不断扩大的子集上）对你的真实
基础设施执行操作。每个模块都会：(a) 从核心消费规范化的事件/数据，(b) 在共享数据模型中
声明其实体，以及 (c) 暴露自己的 API 端点和 UI 视图 —— 而不触碰核心或其他模块。

这 30 个模块按下文的**九大能力领域**组织。请将每个模块的状态读作**两个部分**：
*治理/观察（Govern/Observe）*（编目、观察、把关、报告）今天已构建并接入；
*执行（Actuate）*（在真实基础设施上执行操作 —— 部署、调度、发送、强制、运行）落入若干
诚实的状态 —— 在默认二进制文件中对一个子集是**实时（live）**的，对若干模块是**按需
（on-demand）**的（后端已构建并接入到一个注入点，但在运营者通过环境配置进行预置之前
保持拒止关闭或降级），在表面受门控/可选启用之处为 **PARTIAL（部分）**，其余则是一个声明
的**拒止关闭接缝（deny-closed seam）**。特别地，**部署（deploy）**会规划并治理部署，但
在执行器被预置之前**不会**将其应用到实时基础设施上：`apply`/`retire` 会返回明确的 `503`。
各模块的深度有所不同，且产品的大部分在所注明之处处于 pre-1.0 / 设计阶段（参见
[诚实与局限](/zh/start/honesty-and-limits/)）。

**访问图谱（access map）**（`iii-access-map`）—— 即每个 agent 能够触及以及实际触及哪些
内容的读取/读写图谱，配合最小权限漂移（least-privilege drift）= `Permitted ≠ Observed`
—— 是这 30 个模块中**最有用的能力之一**，而非整个产品。广度才是要点：九大领域、一个引擎、
一个控制台。

## 30 个模块，按能力领域

每一行都链接到其模块页面（`/reference/modules/<slug>/`）。**Actuate（执行）**列是执行部分
的诚实状态；`—` 表示该模块在本质上是治理/观察型的，没有执行表面。

### 观察（Observe）

| 模块 | 执行 | 用途 |
|---|---|---|
| [清点与发现](/zh/reference/modules/i-inventory/) | — | 发现并编目治理范围中的每一个 agent/会话/MCP 服务器/工具/模型/身份。 |
| [实时运行与会话](/zh/reference/modules/ii-sessions/) | — | 每个 agent 与会话的实时状态；同时托管受治理的 Claude Code 会话运行时。 |
| [访问与资源图谱（R/RW）](/zh/reference/modules/iii-access-map/) | — | 每个 agent 访问什么，以及它是读取还是写入；最小权限漂移 = `Permitted ≠ Observed`。 |
| [编排与 A2A](/zh/reference/modules/iv-orchestration/) | 按需 | 对实时委派/通信图谱进行观察与治理；调度按需接入，在预置之前拒止关闭。 |
| [MCP、技能与能力](/zh/reference/modules/v-capabilities/) | — | 可视化地治理 agent 的工具与能力。 |
| [健康、SLA 与正常运行时间](/zh/reference/modules/xxii-health/) | — | 治理范围中各 agent 与 MCP 服务器的可靠性；检查、事件、依赖图谱。 |
| [可观测性只读模型](/zh/reference/modules/observability/) | — | 引擎对自身的只读模型：固定的互操作标准、经 W3C 关联的账本/追踪视图、供应链证明。 |
| [Claude Code 采用度](/zh/reference/modules/claudeadoption/) | — | Claude Code 采用度/生产力的只读模型：会话、代码行数、commit、PR、工具接受-拒绝、按模型的 token，按团队/开发者/日；默认按团队，按开发者下钻为可选启用。仅限-Claude-API 边界；从不携带成本。 |
| [实时摄取](/zh/reference/modules/live-ingest/) | PARTIAL | 进程内产生连接器无法发出的侦测事件；受环境门控、拒止关闭、最小数据。 |

### 治理与强制（Govern & enforce）

| 模块 | 执行 | 用途 |
|---|---|---|
| [身份、权限与治理](/zh/reference/modules/vi-governance/) | — | 谁以及什么能做什么，粒度化：Cedar RBAC + 拒止覆盖层 + 作用域授权、名册对账、作用域化管理员/自定义角色、应急破窗（break-glass）、紧急停止开关（kill-switch）。 |
| [源与凭据作用域化](/zh/reference/modules/sourcescope/) | — | 将源绑定到某个工作区/agent 组；拒止关闭的作用域解析器 + 解析时的作用域化凭据。 |
| [部署与集成](/zh/reference/modules/vii-deploy/) | 按需（503） | 规划并治理向真实基础设施的部署；执行器为按需 —— 在预置之前，实时的 `apply`/`retire` 返回 `503`。 |

> **身份与访问**位于 [治理](/zh/reference/modules/vi-governance/) 内部 —— 没有单独的模块。
> NHI 生命周期、agent 身份联邦、AAL3 升级验证以及 SSO/SCIM 都是治理能力。

### Claude 与 agent 生态

| 模块 | 执行 | 用途 |
|---|---|---|
| [模型与提供方管理](/zh/reference/modules/x-models/) | 按需（503） | 跨整个模型/提供方栈进行治理：模型访问、逐表面上下文窗口、模型组门控；模型*执行*为按需 —— 在推理凭据被预置之前返回 `503`。 |
| [内联推理代理](/zh/reference/modules/inferenceproxy/) | PARTIAL | 为内联 `/v1/messages` PEP 代理提供逐租户的推理出口配置 + DLP；模块配置已实时生效，监听器为可选启用、回环默认、失败时关闭（fail-CLOSED）。 |
| [内部目录与市场](/zh/reference/modules/xiv-catalog/) | — | 经核准/签名的 agent、MCP 服务器与技能的精选市场。 |
| [语音与实时 agent](/zh/reference/modules/xvi-voice/) | 按需 | 对会话式/实时 agent 进行观察与治理（默认拒止、两阶段 human-in-the-loop）；永不打开媒体流；调度为按需。 |

### 安全与数据保护（Security & data protection）

| 模块 | 执行 | 用途 |
|---|---|---|
| [安全、护栏与审计](/zh/reference/modules/ix-security/) | 实时 | 护栏（guardrails，PII/注入/越狱）、异常、事件时间线；BYOK/DLP/RTBF/保留/WORM/驻留位于此平面。 |
| [特权会话录制](/zh/reference/modules/recording/) | 实时 | 与 PAM 对齐的特权会话录制：哈希链式（hash-chained）帧、写入时脱敏、锚定到账本。 |
| [数据、知识与上下文](/zh/reference/modules/viii-knowledge/) | 按需 | 受治理的数据平面：知识库 + RAG、受治理的检索、血缘、提示词注册表、agent 记忆；基于模型的语义嵌入为按需。 |

### 合规与证据（Compliance & evidence）

| 模块 | 执行 | 用途 |
|---|---|---|
| [合规与监管](/zh/reference/modules/xiii-compliance/) | — | 26 个框架目录 + 经密封的、源自账本的证据，并提供实时链式校验。 |
| [SIEM/ITSM 转发器](/zh/reference/modules/siemforward/) | 实时 | 将密封的账本 + 发现项推送到 SIEM 塔（OCSF 1.8/CEF/LEEF/syslog/OTLP），领导者门控的游标遍历，至少一次（at-least-once）。 |
| [态势导出](/zh/reference/modules/posture-export/) | PARTIAL | 面向控制塔的只读态势/清点拉取（中立 JSON）；**不**声称已校验的下游推送。 |
| [Reporting](/zh/reference/modules/reporting/) | — | 根据平台的合规、审计与 FinOps 数据生成专业 PDF/HTML 报告 —— 内置 5 种类型；审计人员下载文档，无需复制粘贴 JSON。 |

### FinOps

| 模块 | 执行 | 用途 |
|---|---|---|
| [成本与 AI FinOps](/zh/reference/modules/xi-finops/) | 实时 | 在上限处拒止/限流的执行型预算、按结果计成本（cost-per-outcome）、取消风险；预算与身份强绑定。 |

### 评估与安全性（Evals & safety）

| 模块 | 执行 | 用途 |
|---|---|---|
| [质量、评估与测试](/zh/reference/modules/xii-evals/) | — | 经校准的 LLM 裁判 + 一道阻塞式 CI 回归门控；离线裁判 → SKIPPED，绝不静默通过。 |
| [Agent 沙箱](/zh/reference/modules/xvii-sandbox/) | 按需 | 在投产前测试 agent 的安全环境；真正的操作系统隔离（gVisor/Firecracker）为按需。 |
| [红队与对抗测试](/zh/reference/modules/xviii-redteam/) | 按需 | 经同意门控的对抗测试集；在沙箱运行时被预置之前为 DEGRADED（降级）—— 绝不假通过。 |

### 平台与集成（Platform & integrations）

| 模块 | 执行 | 用途 |
|---|---|---|
| [输出集成与通知](/zh/reference/modules/xv-notify/) | 实时 | 面向公司已运行系统的通知路由器；调度已实时接入，目的地由运营者预置。 |
| [事件订阅](/zh/reference/modules/eventing/) | 实时 | 总线之上的外部订阅表面：类型化订阅、持久的至少一次投递、重试/退避、DLQ、游标重放。 |
| [已保存的控制台视图](/zh/reference/modules/consoleviews/) | — | 控制台视图状态（过滤器、范围）的命名、可共享快照，按租户存储在服务端：保存一次调查，与团队共享。接受上限 4096 字节、用于视图参数的 JSON 对象——请勿在其中存放敏感数据或查询结果。创建/更新仅限所有者；租户管理员/所有者和超级管理员可为清理而删除；每次变更均被审计。 |

**Actuate（执行）**列：`live`（实时）= 执行已接入并在默认二进制文件中实时生效，无需预置
（例如 FinOps 预算强制在上限处拒止、通知路由器进行调度）；`on-demand`（按需）/
`on-demand (503)`（按需（503））= 后端已构建并接入到一个注入点，但在运营者通过环境配置
**进行预置之前保持拒止关闭或降级**（部署在执行器存在之前应答 `503`；编排/语音调度在配置
之前拒止关闭；红队在沙箱运行时被预置之前以 DEGRADED 运行；模型执行与语义嵌入在凭据被预置
之前返回 `503`）；`PARTIAL`（部分）= 表面是真实的但受门控/可选启用，或不声称已校验的下游
（推理代理监听器为可选启用、回环默认；实时摄取受环境门控；态势导出是一个中立的只读投影）；
`—` = 该模块在本质上是治理/观察型的，没有执行表面。这一拆分就是诚实的契约：产品**今天广泛地
观察并治理，并在一个不断扩大、大多受预置门控的子集上执行** —— 参见
[诚实与局限](/zh/start/honesty-and-limits/)。该目录派生自组合根（`cmd/olivares/wire.go`）：
全部 30 个模块都在那里构造并通过 `rt.AddModule` 注册（已于 2026-08-01 对
main @ f632f03f 进行验证）。

## 平台与核心能力（不计入 30 个模块之内）

以下是真实的、已交付的能力，但它们是**引擎/核心/Web 能力**，而非 `modules/` 集合中的
模块 —— 因此不计入这 30 个之内：

- [自有 API + 以代码管理](/zh/reference/modules/xix-api-manage-as-code/) ——
  **引擎/核心能力。** 引擎自有的、带版本的 REST/gRPC API，外加 Terraform provider；
  通过 API 与 IaC 来管理平台本身。
- [多租户与组织管理](/zh/reference/modules/xx-multi-tenancy/) ——
  **引擎/核心能力。** 组织层级与委派式管理，配合 Postgres 行级安全（row-level-security）
  的租户隔离。
- [高管仪表盘](/zh/reference/modules/xxi-executive-dashboards/) ——
  **Web 能力。** 与技术 UI 并列的领导层控制台视图。（其报告生成后端是
  [reporting](/zh/reference/modules/reporting/) 模块，该模块计入 30 个模块。）
- [模型运维（自有模型）](/zh/reference/modules/xxiii-model-operations/) ——
  **models 模块的能力**（通过模块 X 的行计数，不是单独一行）：受治理的自有模型注册表、
  签名模型准入、数据集/微调作业的血缘记录、本地推理部署治理，以及 AIBOM/模型卡片证据。

**计划中：** 自有模型微调与本地推理的**执行**
（[xxiii-fine-tuning](/zh/reference/modules/xxiii-fine-tuning/)）—— 平台今天已治理并记录
这些工作（见上文模型运维），但自身不执行训练、不提供推理服务；执行的那一半是有文档记载的
**计划中**工作，**未交付**，也不在这 30 个之内。

## 模块如何在 API 与总线中呈现

- **REST。** [API 参考](/reference/api/) 从产品的 OpenAPI 3.1 契约渲染核心 REST 表面。
  某些模块路由可达，但**有意不**出现在该文档中；它们的字段级契约存在于产品的类型化接口里。
- **事件。** 模块对 [事件总线](/zh/reference/events/) 作出反应：访问图谱消费
  `edge.observed`，FinOps 消费 `cost.sampled`，安全模块消费 `finding.reported` 和
  `guardrail.observed`。

## 层级

这 30 个模块构建在引擎之上的若干层级上，与上文的引擎/核心及 Web 能力并列：

- **引擎（第 0 层）** —— 自有 API/以代码管理与多租户能力（核心，不计入这 30 个）。
- **核心（第 1 层）** —— 清点、会话、访问图谱、模型、健康、可观测性。
- **管理（第 2 层）** —— 能力、治理、sourcescope、部署、知识。
- **智能（第 3 层）** —— 编排、安全、录制、推理代理、finops、评估、合规、reporting、siemforward、
  态势导出、目录、notify、事件订阅、语音、沙箱、红队、实时摄取、已保存的控制台视图。
- **Web（第 4 层）** —— UI 与高管仪表盘能力。

参见 [架构概览](/zh/explanation/architecture/overview/) 以了解引擎与这些层级如何组合。

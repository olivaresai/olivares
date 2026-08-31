---
title: Olivares AI 与 LLM 网关及可观测性（LiteLLM、Langfuse）的对比
description: >-
  与流行的自托管 LLM 运维栈——一个网关（LiteLLM）加一个可观测性平台（Langfuse）——的坦诚对比。
  各自擅长什么、Olivares AI 有何不同，以及为何这是"且"，而非"或"。
sidebar:
  order: 3
---

一种常见而合理的自托管栈，是把 **LLM 网关**（例如 **LiteLLM**）与 **LLM 可观测性平台**（例如 **Langfuse**）配对。
如果你已经拥有这样一套，你或许会合理地发问：自己究竟还需不需要一个 control plane。本页将坦诚地回答这个问题——
包括那些答案为 **不需要** 的情形。

:::tip[简短版本]
LiteLLM 和 Langfuse 关注的是 **你的应用发起的模型调用**：路由它们、追踪它们、管理 prompt、按调用核算成本。
Olivares AI 关注的是 **你资产范围（estate）中的每一个智能体，以及它读取或写入的一切**——数据库、对象存储、MCP 服务器、
工具、文件——以及这是否与策略所允许的相符。不同的高度（altitude）。它们可以组合；我们 **摄入它们发出的同一份
OpenTelemetry gen-ai 信号**。
:::

## 那套栈擅长什么（就用它来做这些）

- **LiteLLM** —— 一个置于众多供应商之前的统一、OpenAI 兼容网关：路由、回退（fallback）、重试、虚拟密钥、
  按密钥的预算与速率限制，以及对经其转发的模型调用的成本核算。
- **Langfuse** —— LLM 工程与可观测性：请求/响应 **追踪（trace）**、prompt 管理与版本化、评测、数据集，
  以及一个面向开发者、用于调试链路的 UI。

如果你的问题是 *"为我的应用 LLM 调用打点、调试 prompt、并从一个端点管理模型访问"*，这套栈非常出色且可自托管。
你不需要一个 control plane 来做这件事，我们也不会假装并非如此。

## Olivares AI 在结构上有何不同

| 维度 | LLM 网关 + 可观测性 | Olivares AI |
|---|---|---|
| **关注单元** | 一次模型调用（prompt → completion） | 一个智能体及其读/写的每一项资源——数据库、对象存储、MCP、工具、文件 |
| **观察位置** | **在请求路径中**（proxy/SDK）；看到应用所发送的内容 | **带外、读优先（read-first）**；观察遥测、原生审计和一个内核兜底——永不在数据路径中 |
| **真相来源** | 应用/代理 **所报告** 的内容 | 自报告遥测 **与系统自身账本相互印证**——pgAudit（读 vs 写）、CloudTrail（对象访问）、eBPF 兜底 |
| **关键问题** | "这次 prompt 做了什么，花了多少？" | "这个智能体是否在使用 **无人授予** 的访问权？"——[Permitted-vs-Observed 偏移](/zh/explanation/#访问图谱读优先最小数据许可对比观测) |
| **强制执行** | 网关能对 **模型调用** 设闸（密钥、预算） | 对 **动作与资源访问** 的 deny-closed 闸门：审批、[Claude Code hooks PEP](/zh/how-to/connectors/claude-code-hooks-pep/)、MCP 工具设闸、kill switch |
| **审计制品** | 用于调试的追踪 / 日志 | 仅追加（append-only）、hash-chained、**Ed25519 签名** 的账本，**可离机（off-box）验证**，可导出为 **OSCAL** 证据包 |
| **部署姿态** | 可自托管 | 自托管 **或气隙（air-gapped）**；数据平面永不离开你的边界；**AGPL**，source-available |

承重的差异在于 **地面真相（ground truth）**。一条可观测性追踪告诉你应用 *声称* 做了什么。它无法告诉你某个智能体
触及了一张该追踪从未提及的表。Olivares AI 把这份协作式信号与数据平面交叉核验，于是"智能体触及了什么"成为一项
经印证的事实，而非自我报告。关于为何这是我们三条主线中的第一条，参见
[分析师词汇表](/zh/explanation/positioning/analyst-vocabulary/)。

## 这是"且"，而非"或"——我们摄入你的遥测

Olivares AI **不是** 你的网关或追踪工具的替代品，它也不想置身于它们所占据的请求路径中。它 **消费同一份信号**：
该 control plane 摄入 **OpenTelemetry GenAI** 语义约定的 span，正是这些工具发出与消费的同一份 gen-ai 遥测。
因此一种健康的安排是：

- 保留 **LiteLLM** 作为你的模型网关，**Langfuse** 用于面向开发者的追踪与 prompt 工作。
- 把 **OTel gen-ai** 流指向 Olivares AI，作为一个印证来源，让访问图、偏移检测与账本在其上构成全资产范围的治理层。

→ [摄入 OpenTelemetry GenAI](/zh/how-to/connectors/otel-genai/) ·
[面向 Claude Code 的企业级 OTel](/zh/how-to/claude-code-enterprise-otel/)

## 何时你 *不应* 选择 Olivares AI

坦诚是双向的。在以下情况下，你很可能 **不需要** 这个 control plane：

- 你唯一的目标是在一两个应用中 **追踪与调试 LLM 调用**，外加一个 prompt 演练场——单用 Langfuse 更合适。
- 你只需要一个带预算与故障转移的 **多供应商网关**——那是 LiteLLM 的本职，我们与这一模式集成，而不去重新实现它。
- 你 **没有需要治理的资产范围**：单一服务、单一模型、没有触及数据库/对象存储/MCP 的智能体，也没有审计或监管义务。

当问题变得 *横跨整个资产范围且充满对抗性* 时，Olivares AI 才显出价值：**存在哪些智能体、各自实际能触及什么、
访问在哪里偏离策略、我能否向审计方证明它、以及我能否以 deny-closed 方式阻断一个不良动作**——而这一切都无需
把这幅图景发往别人的云。

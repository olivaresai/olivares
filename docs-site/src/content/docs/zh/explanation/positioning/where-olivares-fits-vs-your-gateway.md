---
title: Olivares 相对于你的 AI 网关与 Guardrails 的位置
description: >-
  你已经在运行一个 AI 网关（LiteLLM、Portkey、Cloudflare）或超大规模云商的
  Guardrails（Bedrock、Azure）。很好 —— 留着它们。Olivares AI 不是网关，
  也不在路由或缓存上竞争。它是位于它们一侧、并补上它们留下的缺口的治理与
  证据平面。
sidebar:
  order: 7
---

如果你已经在一个 **AI 网关**或某超大规模云商的 **Guardrails** 上投入，诚实地说，
第一句话是：**留着它们，Olivares AI 不打算替换它们。** 网关的本职是处理模型调用
—— 路由它、缓存它、做负载均衡、设预算。Guardrails 的本职是该调用上的内容安全。
两者都真实存在，都擅长各自的本职，而它们都不是 Olivares 之所是。

:::tip[简短版本]
**Olivares AI 不是一个 AI 网关。** 它不路由、不缓存、不做负载均衡，也不坐在你模型
流量的热路径上，而且永远不会。它坐在你网关的**一侧与其后方**，作为*治理与证据
平面*：智能体运行时内部的进程内强制执行、一份篡改可检测的证据账本、非人类身份生命周期，
以及对**实时会话**的人在环中 / break-glass / kill-switch。你的网关治理*请求*；
Olivares 治理*智能体及其所触及的一切*，并向审计方证明它。
:::

## 网关与 Guardrails 各自擅长什么（就用它们来做这些）

这些是商品化、广为人知的能力，供应商对它们的描述也直白：

- **AI 网关**是面向模型调用的请求路径管理器。LiteLLM 是一个*"OpenAI Proxy Server
  (LLM Gateway) to call 100+ LLMs in a unified interface & track spend, set budgets
  per virtual key/user"*
  （[LiteLLM](https://docs.litellm.ai/docs/simple_proxy)）；Cloudflare AI Gateway
  让你得以*"Connect to any model, dynamically route requests, and manage usage,
  billing, and logs from one unified gateway"*
  （[Cloudflare](https://www.cloudflare.com/products/ai-gateway/)）；Portkey
  *"records real-time API requests, including cost"*
  （[Portkey](https://portkey.ai/features/ai-gateway)）。路由、回退、缓存、虚拟密钥、
  按密钥预算、请求日志 —— 这是它们的赛道。
- **超大规模云商的 Guardrails** 是内容安全过滤器。Bedrock Guardrails *"provides
  configurable safeguards to help you build safe generative AI applications"*，
  这些防护*"detect and filter undesirable content and protect sensitive information
  that might be present in user inputs or model responses"* —— 内容过滤、被拒主题、
  词过滤、PII 脱敏、上下文接地与自动化推理检查
  （[AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)）。

如果你的问题是*"给我的应用一个通往众多模型的端点，带预算、缓存与内容过滤"*，那套栈
能解决它，你也不需要一个 control plane 来做这件事。我们与这一模式集成；我们不去
重新实现它。

## 它们留下的治理缺口

网关看到一个**请求**。Guardrails 看到**内容**。两者都看不到**智能体** —— 它随
时间推移的身份、它跨你数据平面触及了什么、谁批准了一个有风险的动作，以及这一切
日后能否被证明。这正是 Olivares 所填补的缺口。

| 网关 / Guardrails 留下的缺口 | 为何重要 | Olivares AI 提供什么 |
|---|---|---|
| **智能体运行时处的强制执行** | 网关在*请求边界*强制执行；它无法阻止一次永不经过它的本地 Claude Code 工具调用 | 智能体处一个默认拒绝的[进程内 PEP](/zh/how-to/connectors/claude-code-hooks-pep/)：确凿身份闸门、策略处置、实时策略叠加，全部在工具运行之前 |
| **篡改可检测的证据** | 网关与 Guardrails 发出的是*日志* —— 可变的请求记录；审计方要的是不可变的证明 | 仅追加、hash-chained、[Ed25519 签名的账本](/zh/reference/glossary/#audit-ledger审计台账)，可离机验证，可导出为 OSCAL 证据 |
| **非人类身份生命周期** | 网关的"虚拟密钥"是一个预算桶，而非一个被配置、被归因、被轮换与被离职处理的身份 | [NHI 生命周期](/zh/reference/glossary/#identity--nhi)：陈旧 → 阻断、离职级联、轮换上的双人控制、绑定到访问图 |
| **实时会话干预** | 日志与预算都是事后的；这些被调查的工具没有一个能在会话进行中将其停止 | [HITL 审批](/zh/reference/glossary/#approval审批hitl)、[break-glass](/zh/reference/glossary/#break-glass破玻璃) 与一个 [kill switch](/zh/reference/glossary/#kill-switch终止开关)，在双人控制重新启用之前拒绝所有受治理的执行 |
| **横跨资产清单的事实依据** | 网关只看到经过它的那些调用；智能体还会直接触及数据库、对象存储、MCP、文件 | 读取优先的 [R/RW 访问图](/zh/explanation/#访问图谱读优先最小数据许可对比观测) 与已许可对已观察的漂移，并与原生审计相互印证 |
| **主权** | SaaS 网关与云端 Guardrails 在它们的云中处理那份流量 | 自托管 / 气隙；数据平面永不离开你的边界 |

这些没有一个是路由功能。这正是要点所在：缺口不是*更好的路由*，而是**请求路径
从未被设计来提供的治理**。

## 专门谈 Guardrails：内容安全是一个 hook，而非一个竞争者

Bedrock Guardrails 可以两种方式应用 —— 在一次 Bedrock 推理调用中内联，或*"directly
through the `ApplyGuardrail` API without invoking the foundation models"*，后者
*"with any foundation model whether hosted on Amazon Bedrock or self-hosted models"*
都能用（[AWS](https://aws.amazon.com/bedrock/guardrails/)）。这确实有用，而 Olivares
把内容安全当作一个**你插入的检测器**，绝非一道我们要你*替代* Guardrails 去选择的墙。
两个诚实而清晰的事实：

- 内联推理代理暴露一个**内容检查接缝** —— 一个可插拔的点，在此一个内容 / DLP 检测器
  返回一个裁决，默认拒绝的决策器据此行动。内容安全归属*那里*，在流水线之中，而非
  被重新实现为一个相互竞争的过滤器。
- Olivares 读取优先地读取你 Guardrails 的**自有决策**。AWS 连接器从他们的 CloudWatch
  / S3 日志中摄入 Bedrock guardrail 决策，作为态势与证据；它有意**不**自行调用付费的
  `ApplyGuardrail` 运行时。你的内容裁决成为篡改可检测记录的一部分。

所以内容安全与你已在运行之物相互组合。Guardrails *未*记录之处 —— 以及治理缺口
仍敞开之处 —— 是智能体生命的其余部分：Bedrock 页面没有记录智能体身份、没有会话
管理、没有人工审批，也没有成本治理（这些在那些页面上未记录，核对于 2026-06-21）。
Olivares 恰是那样的补充：它承载身份、会话控制、审批与证据；内容过滤器留在它已经
所在之处。

## 它们如何组合

一种健康的安排让每个工具各守其赛道：

- **保留你的网关**（LiteLLM / Portkey / Kong / Cloudflare）作为模型调用平面 ——
  在请求上做路由、缓存、虚拟密钥、预算。
- **保留你的 Guardrails**（Bedrock / Azure Content Safety）作为你的内容安全检测器
  —— Olivares PEP 在其内容检查接缝处运行一个可插拔检测器，并读取优先地读取你
  Guardrails 的自有决策作为证据；它不自行调用 `ApplyGuardrail`。
- **在它们一侧加入 Olivares** 作为治理与证据平面：在那些永不经过你网关的智能体上的
  进程内 PEP、横跨整个资产清单的访问图、篡改可检测的账本，以及实时的 HITL/break-glass/kill
  控制。

Olivares 确实触碰推理的唯一之处既狭窄又明确 —— 一条面向原始 SDK/`curl` 调用方的
**仅 API-key** 网关路径，其描述见
[治理以订阅认证的智能体](/zh/explanation/positioning/governing-subscription-authed-agents/)。
它的存在是为了治理你其他工具触及不到的流量，绝非在路由上与它们竞争，而且它**绝不**
承载订阅凭据。

## 何时你的网关已经足够

诚实是双向的。如果你的智能体**只**会**经由**你的网关调用模型、你的内容安全需求由
Guardrails 满足、你**没有**直接触及数据库 / 对象存储 / MCP 的自托管或驻留笔记本的
智能体，且你**没有**主权或篡改可检测证据的要求 —— 那么你的网关加上它的日志与 Guardrails
也许就是你所需要的全部，你不应为了一个 control plane 本身而去添加它。

当问题变得*横跨整个资产清单且充满对抗性*时，Olivares 才赢得它的位置：存在哪些
智能体、各自实际触及了什么、我能否**在智能体处**以默认拒绝阻断一个不良动作、谁批准
了那个有风险的、以及我能否向审计方递交**不可变的证明** —— 而这一切都无需把这幅
图景发往他人的云。关于两个相邻对比的更深入处理，参见
[与 AI control tower 的对比](/zh/explanation/positioning/vs-control-towers/)以及
[与 LLM 网关及可观测性的对比](/zh/explanation/positioning/vs-llm-observability/)。

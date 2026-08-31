---
title: Olivares AI 与 WitnessAI 的对比
description: >-
  一份诚实、有出处、与 WitnessAI 的对比 —— 在 IDE 与开发者工具内部治理 AI
  智能体这一点上，它是最贴近的正面对手。在智能体发现与 MCP 允许列表上是真正
  的对等；而对于受监管、自托管的买家，存在一处清晰、站得住脚的差异：进程内
  强制执行、一份密码学证据账本，以及一个永不离开你边界的数据平面。
sidebar:
  order: 8
---

Olivares AI 的多数"竞争者"都处于相邻赛道 —— control tower、网关、可观测性 ——
而[其他定位页面](/zh/explanation/positioning/market-context-and-sources/)解释了
为何那些是"且"而非"或"。**WitnessAI 才是真正的正面对手。** 它在开发者环境内部
治理 AI 智能体：发现编码智能体、强制执行已批准的工具列表，并对智能体的行为施加
策略。因此本页被要求达到更高的标准 —— 下文每一条关于 WitnessAI 的论断都是逐字
引自其官网（抓取于 2026-06-21），凡其官网未提之处，我们说*"未记录（not
documented）"*，绝不说*"不存在（absent）"*。

:::note[如何阅读本页]
我们在**架构与部署模型**上对比，而非在功能清单上对比，因为差异真正存在且持久之处
正在于此。在我们确实重叠的功能上，我们如实说明，并**不主张任何优越性**。差异化
是为某一类特定买家而设的：那些无法把治理数据发往他人云端的受监管或气隙组织。
:::

## 我们处于对等之处（且我们不会另作他言）

WitnessAI 在两个 Olivares 同样覆盖的领域做了实打实的工作。我们将其视为**对等**，
不主张我们更好：

- **智能体 / 影子 AI 发现。** WitnessAI 宣称*"Find and catalog thousands of AI
  applications, agents, and MCP servers"*，并针对开发者*"Discover apps like GitHub
  Copilot, Cursor, and hundreds of other AI dev tools across your network"*
  （[witness.ai](https://witness.ai/)）。Olivares 同样发现并清点智能体、模型、MCP
  服务器与工具。视角不同 —— 他们的网络，我们的读取优先的遥测加审计 —— 但*发现*这一
  成果是可比的，我们不会假装我们的清单在类别上更胜一筹。
- **MCP 允许列表 / 已批准工具治理。** WitnessAI：*"Enforce control of approved MCP
  servers and tools across every agent, IDE, and agentic app"* 以及*"Maintain an
  organization-wide approved-tool list of MCP servers and tools"*（witness.ai）。
  Olivares 同样治理 MCP 工具访问
  （[MCP 治理](/zh/how-to/connectors/mcp-governance/)）。对等。本页没有一条要点是
  "我们在 MCP 允许列表上做得比他们好"。

如果智能体发现与 MCP 允许列表就是你需求的全部，那么这在能力上势均力敌，而其他
因素（部署模型、价格、既有布局）才应作出抉择。我们宁愿这么说，也不愿过度声称。

## 用他们自己的话说，WitnessAI 是什么

WitnessAI 的模型是**网络级且以云交付**的，带有一种明确的*基于意图（intent-based）*
的控制哲学：

- **网络级、无客户端。** *"See AI activity across your entire network without
  relying on browser extensions or endpoint clients"*，以及一个*"operates at the
  network level—no new SDKs, additional clients, or added exposure"* 的平台
  （witness.ai）。
- **基于意图的策略。** *"Traditional security sees text; WitnessAI sees intent"*，
  带有*"intent-based ML engines that understand context, not just keywords"*
  （witness.ai）。这是一项真实而独特的设计选择，对于在线、内容感知的用例是一项
  优势。
- **归因到人的智能体治理。** *"every agent action maps back to a human identity"*，
  在*"a single policy engine [that] governs both human and agent workforces"* 之下
  （witness.ai）。
- **一个 SaaS 主权叙事。** 他们确实回应了数据控制 —— *"a secure, single-tenant
  environment that ensures data sovereignty"*、*"single-tenant environment with your
  own key encryption"*，以及*"regional sandboxes"*（witness.ai）。这是一个**云端、
  单租户、客户密钥**的模型。它是对数据驻留的一个真实回答 —— 而它是一个与我们*不同*的
  回答，这正是下文的关键所在。

这些是能力，已注明出处并如实陈述。对比不是"他们弱"；而是"我们建立在一个不同的
架构之上，面向一类不同的买家"。

## Olivares 在结构上有何不同

| 维度 | WitnessAI（据其官网） | Olivares AI |
|---|---|---|
| **部署** | 网络级、以云交付；单租户，带客户密钥与区域沙箱。自托管 / 本地部署 / 气隙**未记录** | 默认自托管；支持[气隙](/zh/how-to/air-gap-install/)；数据平面永不离开你的边界 |
| **许可** | 专有 SaaS；开源**未记录** | 开放核心 **AGPL**，source-available —— 可审计，你的合规路径中没有 SaaS control plane |
| **强制执行点** | 在网络级，带*"enforcement at the tool call and MCP server level"* | 在智能体运行时进程内 —— 一个默认拒绝的 [Claude Code 内部 PEP](/zh/how-to/connectors/claude-code-hooks-pep/)，外加 MCP 与执行（actuation）闸门 |
| **证据** | *"detailed logging keeps you audit-ready"* —— 密码学 / 不可变账本**未记录** | 仅追加、hash-chained、[Ed25519 签名的账本](/zh/reference/glossary/#audit-ledger审计台账)，可离机验证，OSCAL 导出 |
| **实时干预** | 人在环中审批 / break-glass**未记录** | 对实时会话的 [HITL 审批](/zh/reference/glossary/#approval审批hitl)、[break-glass](/zh/reference/glossary/#break-glass破玻璃) 与 [kill switch](/zh/reference/glossary/#kill-switch终止开关)，默认拒绝 |
| **身份模型** | *"every agent action maps back to a human identity"* —— NHI 生命周期**未记录** | 智能体作为一等的[非人类身份](/zh/reference/glossary/#identity--nhi)，带配置、陈旧阻断、轮换与离职处理 |

上文每一处*"未记录"*恰恰意味着：它未出现在我们所阅读的 WitnessAI 页面上。这**不是**
声称他们的产品缺乏该能力 —— 只是我们不会替他们断言其官网并未陈述的内容。

## 站得住脚的楔子：受监管、自托管的买家

把这张表精简下来，有一处差异是承重的。WitnessAI 的数据控制是一个带你密钥的**单租户
云**；Olivares 的则是一个运行在你自有基础设施上的**自托管 control plane**——Linux、
Docker、Kubernetes、本地部署或气隙。产品没有强制遥测，控制平面默认也不产生出站流量。
只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、你接入的
SIEM/webhook 输出，以及你配置时使用的外部嵌入提供商。对许多买家而言两种模式等价。
但对那些**在合同上或法律上被禁止使用第三方云**的买家 —— 国防、涉密、主权云、某些
受监管的金融与医疗 —— 一个 SaaS 或单租户云模型在功能对比尚未开始之前就已被取消资格，
而一个 source-available、可自托管、控制平面默认不产生出站流量的产品，是唯一能通过采购的
那一类。

这就是诚实的楔子：不是"我们把智能体治理得更好"，而是**"我们在你完全掌控的基础设施上
治理它们，带密码学证据与进程内强制执行，面向那些根本无法使用云的买家"。** 结合
进程内 PEP 与篡改可检测的账本，这是一个网络级 SaaS 无法靠加一项功能就占据的位置。

## 何时 WitnessAI 更合适

我们宁愿你选得好，而非选了我们。在以下情况下，WitnessAI 很可能更合适：

- 你想要**无需部署或运维**一个 control plane 的网络级可见性，且一个单租户 SaaS
  满足你的数据驻留标准。
- 你的优先事项是横跨一般企业 AI 流量的**在线、基于意图的内容分类**（而非 Olivares
  所聚焦的受治理编码智能体与篡改可检测证据这一具体问题）。
- 你**不要求**自托管、AGPL 源码可得、密码学证据账本，或对实时会话的 break-glass/HITL
  —— 这些是他们官网未记录、而 Olivares 围绕其构建的东西。

当资产清单是**自托管或气隙**的、当证据必须是**篡改可检测且可离机验证**的、当强制执行
必须存在于**智能体内部**且默认拒绝 —— 且这一切都不越界进入另一家公司的云时，
Olivares 才赢得这个决定。

:::caution[出处与限度]
本页每一条 WitnessAI 论断均引自其公开站点（首页、产品、开发者、合规与控制页面），
抓取于 2026-06-21；我们并未阅读他们发布的每一个页面，*"未记录"*的范围仅限于我们
所阅读的页面。营销文案不是架构文档，而产品能力会变化。如果你正同时评估两者，请
直接向各供应商核实当前状态 —— 这正是整个[定位章节](/zh/explanation/positioning/market-context-and-sources/)
所恪守的标准。
:::

## 相关阅读

- [治理以订阅认证的 Claude Code 与 Codex](/zh/explanation/positioning/governing-subscription-authed-agents/)
  —— 进程内强制执行究竟如何运作。
- [Olivares 相对于你的网关 / Guardrails 的位置](/zh/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  —— 同一套"我们不在请求路径上竞争"的纪律。
- [Olivares 与你的 IdP 如何契合](/zh/explanation/architecture/where-it-fits-with-your-idp/)
  —— NHI 模型背后的只读身份联合。

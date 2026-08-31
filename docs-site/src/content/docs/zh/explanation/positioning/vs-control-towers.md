---
title: Olivares AI 与 AI 控制塔的对比
description: >-
  Olivares AI 如何与 AI 控制塔及生态治理仪表盘（ServiceNow AI Control Tower、超大规模云厂商的智能体管理平面）相处。
  我们集成，而非竞争——我们是控制塔之下的地面真相来源。
sidebar:
  order: 4
---

**AI 控制塔（AI control tower）** 是面向 AI 治理的全组织级仪表盘与工作流层：一个集中之处，用于查看已注册的智能体、
路由审批、提交工单，并向管理层汇报态势。典型例子包括 **ServiceNow AI Control Tower** 以及超大规模云厂商的智能体管理平面
（Microsoft 的 Entra Agent ID / Agent 365 界面、AWS AgentCore 的治理功能）。

如果你已经在某个控制塔上投入，那么正确的问题不是"选控制塔还是选 Olivares？"，而是"什么在为控制塔提供真相？"
我们刻意给出的答案是：**我们集成，而非竞争。**

:::tip[简短版本]
控制塔擅长 **工作流、工单、全组织级仪表盘，以及治理其自身生态内的智能体**。它们在 **异构、自托管、多云的资产范围（estate）**
上较弱，在 **地面真相**——即智能体实际触及了什么、并与数据平面相互印证——上也较弱。Olivares AI 是 **控制塔之下的来源层**：
它产出带归属的清单、Permitted-vs-Observed 偏移以及篡改可检测的证据，并将它们 **向上推送**。
:::

## 控制塔擅长什么

- **工作流与 ITSM**：审批、变更记录、事件工单、归属责任人——这些是组织既有的流程，AI 治理理应接入其中，
  而不是另起一个平行的孤岛。
- **高管报告**：面向管理层、横跨众多 AI 计划的单一视图。
- **生态原生治理**：超大规模云厂商的控制塔能很好地治理 *其自身云内* 的智能体——它的身份、它的策略、它的运行时。

这些是实实在在的优势，我们并不去复制它们。Olivares AI 不是 ITSM 产品，也无意成为你 CISO 的报告仪表盘。

## 控制塔留下的缺口

| 缺口 | 为何重要 | Olivares AI 提供什么 |
|---|---|---|
| **异构资产范围** | 智能体跨云、本地、笔记本和 CI 运行——而非仅在某一家厂商的运行时中 | 横跨 SQL/对象/数仓存储、MCP、工具以及本地开发智能体的全资产清单与访问图（access map） |
| **地面真相** | 控制塔显示的是 *已注册* 的内容；它很少印证智能体 *实际做了什么* | 自报告遥测与 pgAudit / CloudTrail / eBPF 交叉核验——把 Permitted-vs-Observed 当作事实 |
| **对开发智能体的强制执行** | 控制塔只观察；很少有能以 deny-closed 方式阻断本地智能体动作的 | [Claude Code hooks PEP](/zh/how-to/connectors/claude-code-hooks-pep/) 与 deny-closed 的执行闸门 |
| **篡改可检测的证据** | 仪表盘是可变的；审计方需要不可变的证明 | 仅追加（append-only）、Ed25519 签名的审计账本；OSCAL 证据包；离机（off-box）验证 |
| **主权** | SaaS 控制塔在其云中处理你的治理数据 | 自托管 / 气隙（air-gapped）；数据平面永不离开你的边界 |

## 我们如何接入（双向）

Olivares AI 的设计是坐落于你的控制塔 **之下** 并为其供给数据，同时也从那些暴露名册的控制塔 **向下读取**。

- **向上推送态势与证据。** 导出供控制塔消费的清单与态势（`GET /v1/m/posture/export`），并将审计账本与发现项转发到
  你的 **SIEM/ITSM**，使其落入你已经在运行的工作流中。
  → [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/)
- **向下只读地读取身份名册。** 身份联邦（identity-federation）连接器从 **Microsoft Entra Agent ID**、
  **AWS AgentCore Identity**、**Google Agent Identity** 同步智能体名册，并以只读方式从 **Microsoft Agent 365** 与
  **ServiceNow AI Control Tower** 读取——将它们映射到 SPIFFE/WIF 名册上，使访问图能把边归属到真实、受治理的身份。参见
  [Olivares AI 如何与你的 IdP 协同](/zh/explanation/architecture/where-it-fits-with-your-idp/)。

这种关系 **在设计上是互补的**：控制塔拥有工作流与董事会视图；Olivares AI 拥有地面真相与不可变证据，
正是它们让控制塔的数字变得可信。

## 何时仅有控制塔就够了

如果你的整个智能体资产范围都生活在 **某一家** 超大规模云或 SaaS 生态之内，由该厂商的原生控制塔治理，
并且你 **没有主权要求、也没有异构/自托管的足迹**，那么你可能并不需要一个独立的 control plane——
原生控制塔加上它的审计导出就能覆盖你。当资产范围 **混杂**、当你需要 **经印证的地面真相而非一份注册表**、
或当 **供应商托管的控制平面不适合承载你的治理证据** 时，Olivares AI 才变得必要。

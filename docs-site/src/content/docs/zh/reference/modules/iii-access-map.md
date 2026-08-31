---
title: "模块 III——读/写访问映射"
description: >-
  一项关键的差异化能力：对每条发起方→资源边的读/写访问映射，附带
  Permitted-vs-Observed 差异（最小权限漂移，least-privilege drift）。边如何被构建、分类与
  信任，以及其限制。
---

模块 III 是**读/写访问映射**：哪个发起方（agent、身份、会话）触及哪个资源，被分类为读或读写，
以及揭示最小权限漂移（least-privilege drift）的 **Permitted-vs-Observed 差异**。它是本产品
最有用、最具差异化的能力之一——是 30 个模块之一，而非整个产品。本页是关于该映射是什么、以及如何
如实阅读它的参考。

## 边

该映射是一张由**边**构成的图。每条边是规范化的、最小数据的事实 `发起方 → 资源`，携带：

| 字段 | 取值 | 含义 |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | 读/写分类（当无法确定时为 `unknown`——从不猜测） |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | 产生该边的信号 |
| **confidence** | `attributed` \| `approximate` | 该访问与发起方绑定的牢固程度 |

边以 [`edge.observed`](/zh/reference/events/) 事件到达事件总线，引擎将它们合并进持久化的
`AccessEdge` 实体——该实体本身同时携带**permitted（许可）**侧与**observed（观测）**侧，
因此访问映射是**通用数据模型之上的一个视图**，而非一个独立存储。

## 边如何被构建

模块 III 横跨两条路径：

- **协作式路径**——发出 OpenTelemetry（`otel`）并暴露 MCP 服务器的 agent。结合**原生存储审计**，
  这是高保真的：Postgres pgAudit（`pg_audit`）逐字分类 READ/WRITE；AWS CloudTrail
  （`cloudtrail`）给出 S3 的 `readOnly`；数据仓库同理。
- **非协作式路径**——一个内核级的 **eBPF/Tetragon 兜底（backstop）**（`ebpf`）在系统调用级别
  记录 `MAY_READ`/`MAY_WRITE`，处于 agent 的控制之外（反规避），但对加密的主体内容是盲的。

MCP 工具注解（`readOnlyHint`/`destructiveHint`，来源 `mcp_annotation`）是一个有用的信号，
但按 MCP 规范是**不受信任的**——本产品对它们进行**佐证（corroborate）**，从不单独信任它们。

**permitted（许可）**侧（来源 `policy`）来自声明的授权；**observed（观测）**侧来自上述信号。

## Permitted vs Observed（最小权限漂移）

定义性的视图是一个发起方被*许可*触及的内容与它被*观测到*触及的内容之间的**差异**。它揭示：

- **意外访问**——某发起方使用了一个它从未被授予的资源；
- **未使用的授权**——一项从未被任何发起方行使过的权限；
- **待对账（reconciliation-pending）**——系统尚无法牢固归属的访问。

[从零到图教程](/zh/tutorials/zero-to-graph/)会在演示 estate 上得到一个已填充的漂移结果。

:::caution[诚实的限制]
- **按 agent 的身份是一项硬依赖。** 审计将活动归属到一个凭据或角色，而非本质上归属到一个 agent。
  一个带连接池的共享服务账户会将归属坍缩为 `approximate`。治理得当意味着为每个 agent 颁发身份
  （通向模块 VI 的桥梁）。
- **覆盖是分层的。** 在具备原生审计的存储上是*干净的*（SQL、对象存储、数据仓库）；在某些存储上是
  *有损的*（文档/向量）；在另一些存储上则**无法被动重建**（例如 Redis、SQLite、D1）。在覆盖有损或
  缺失之处，一条缺失的边**并不**证明该访问没有发生过。
- **`unknown` 和 `approximate` 会被展示，而非隐藏。** 本产品从不编造它并不拥有的分类或确定性。
:::

## 阅读该映射

访问映射的结果——包括 Permitted-vs-Observed 漂移——由独立 **beta**
[模块路由参考](/reference/api-beta/)中发布的模块路由提供（而非稳定核心契约）；它们的字段级形状
存在于产品的类型化 Go/TypeScript 接口中，而 web UI 在其之上渲染该图与漂移叠加层。
阅读访问图是一项**特权的、按租户限定的、受完整审计的**操作
（编辑者角色及以上，绝非最低的查看者）——参见
[安全模型](/zh/explanation/security/security-model/)与
[威胁模型](/zh/explanation/security/threat-model/)。

## 相关

- [事件总线参考](/zh/reference/events/)——`edge.observed` 事件及其载荷。
- [架构概览](/zh/explanation/architecture/overview/)——模块 III 所处的位置。
- [治理与审批](/zh/how-to/govern-and-approve/)——对漂移采取行动。

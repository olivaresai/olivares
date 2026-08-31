---
title: "模块 XI — 成本与 AI FinOps"
description: >-
  从成本流核算 AI 开销，按任意归因维度切分，预测当期，并以在上限处拒绝该项消费的方式
  强制执行预算——传输协议中不含金额、opt-in 且 fail-open。它做什么，以及它的局限。
---

模块 XI 是面向 AI 的**成本 / FinOps** 层：它核算模型与提供方 connector（连接器）所报告的内容，
让你按任意归因维度切分开销，预测当前周期，并把一个预算变成真正的强制执行——在上限处**拒绝该项消费**，
而不仅仅是发出标记。本页是关于 FinOps 如今做什么、以及其保证在何处终止的参考。

## 它是什么

FinOps **不会**重新实现提供方集成——它消费模型/提供方成本流，并**核算 connector 权威地派生或读取到的
内容**。金额始终是一个**整数微美元（micro-USD）**值（百万分之一美元），绝不是浮点数，因此总额绝不
漂移。它是一个 Intelligence（智能）层模块：它拥有摄取、预算与分析功能，并通过它自身受 RBAC 门控的
API 命名空间与 UI 视图将其暴露出来，而不触及核心或其邻居模块。

该模块**在构造上即为最小化数据**：它存储 token 计数、派生成本与归因*引用*——绝不存储 prompt、
completion 或密钥。成本属于治理数据，因此读取在 API 处按角色门控，并且**绝不向最终用户暴露任何 USD
数额**（这是连接的一种属性，而非一项 UI 设置）。

## 它的实体与契约

每个 `cost.sampled` 事件（一个 `CostSample`——见[事件总线](/zh/reference/events/)）以两种方式被记录：

- 规范化、已归一的 **CostRecord ledger（账本）**（一个核心实体，以 id 为键），**按自然键去重**——
  即该桶的*身份*（provider / model / session / instant 外加每个归因维度与来源出处），而非其*值*——
  因此一个被重新拉取的开放桶或一份延迟结算的报告会**就地 upsert**，而不会在 at-least-once（至少一次）
  流上重复计数；
- 一行非规范化的 **FinOps read-model（读模型）**，以自然归因名称为键（provider、model、agent、
  session、team、project），从而使开销能按**任意**上述维度高效聚合——包括提供方的 `service_tier`。

一个**预算**是一个 kind 为 `budget` 的核心 `Policy`：一个维度（global / model / provider / agent /
session / team / project）、一个限额、一个周期，以及告警阈值。它的 `action` 为三者之一——`alert`
（仅 showback（费用展示），永不强制执行的安全默认值）、`throttle` 或 `block`。分析提供按任意维度的
开销拆解、总额、一条按日趋势序列、当前周期的 run-rate 与趋势预测（附带一个明确的置信带）、一个
prompt-cache 效率视图，以及优化建议——每一项都立足于已记录的数据，并**对其假设保持诚实**。

## 它消费什么、产生什么

FinOps 从[事件总线](/zh/reference/events/)**消费** `cost.sampled`，并**产生**两种效果。在摄取时，当
消耗越过一个本周期尚未越过的预算阈值时，它记录该告警并**发出一个 `FindingReport`**
（`finding.reported`）——*仅为信号*；向 Slack / SIEM / PagerDuty 的投递是输出 connector 模块的职责，
而非 FinOps 的。

第二种效果是**强制执行**。一个 `action` 为 `throttle` 或 `block` 的预算，会通过一道在每个施动模块
自身术语中声明的 **`BudgetGate` 接缝**（编排的 *fire*、语音的 *open*、模型路由器的 *resolve*）在上限处
拒绝该项消费；没有任何模块导入 FinOps。该门控**与审批门控正交运行**——一个操作可以被人工批准，同时
仍被预算拒绝——并以一个**不含金额的原因**对上限有效开销作答（在只读路由上不含 USD、不含预算名称）。
一次硬 `block` 以 **HTTP 402** 拒绝，一次软 `throttle` 以 **HTTP 429** 拒绝，并且该拒绝会被写入
仅追加的 ledger 并被审计。见[治理与审批](/zh/how-to/govern-and-approve/)。

:::caution[诚实的局限]
- **强制执行是 opt-in（选择加入）的，默认不是 deny-closed。** 在没有任何强制执行预算覆盖某个请求时，
  绝不会有任何东西被拒绝——这种缺失是正常状态，而非一处安全漏洞。只有一个*确定地*处于其限额处的预算
  才会拒绝。这是刻意为之的，与审批门控 deny-closed 的姿态相反。
- **该门控 fail open（失败即放行）。** 一次 FinOps 读取错误绝不会击垮一个进行中的操作——一个已批准的
  fire/open 会继续进行，路由器会完成 resolve。持久的兜底是摄取时发出的预算上限 finding，而非这道
  飞行前（pre-flight）门控。
- **路由器只强制执行它在执行前已知的范围**（global / provider / model）；更细的范围（agent、session、
  team、project）在 fire/open 接缝与模型网关处强制执行，而非在路由解析处。
- **FinOps 进行核算；它不开账单。** 它记录 connector 所报告的内容——`billed`（已计费）对比 `estimated`
  （估算）的来源出处会被携带，而不会被对账成一张发票——并且一个字段为零/为空的样本意味着*“未报告”*，
  绝不意味着*“为零”*。
- **除拒绝外不进行任何执行。** FinOps 既不执行模型调用，也不转移资金；它观测成本流，并对其被配置为
  要门控的消费进行门控。
:::

## 相关

- [事件总线参考](/zh/reference/events/) — `cost.sampled` / `CostSample` 与 `finding.reported` 的载荷。
- [模块目录](/zh/reference/modules/overview/) — 模块 XI 的位置及其诚实的执行状态。
- [架构概览](/zh/explanation/architecture/overview/) — 引擎、各层与成本流。
- [治理与审批](/zh/how-to/govern-and-approve/) — 对一个被预算拒绝的操作实施操作。
- [诚实与局限](/zh/start/honesty-and-limits/) — 跨模块的 deny-closed-seam 策略。

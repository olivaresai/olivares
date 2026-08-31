---
title: "模块 XV —— 输出集成与通知"
description: >-
  control plane 的通知路由器：它决定哪个信号到达谁、走哪条通道、何时送达，
  并通过输出 connector —— Slack/Teams、PagerDuty/Opsgenie、签名 webhook、SIEM ——
  分发经过脱敏处理的结果。经过端到端验证的执行接缝，默认 deny-closed，并带有证据 ledger。
---

模块 XV 是 control plane 的**通知路由器**：当任何模块在事件总线上把告警转化为一条 finding 时，
本模块决定它匹配哪条租户路由、构建一条经过脱敏处理的通知、抑制重复与风暴，
并**实时分发**到公司已经在运行的通道。它负责*决定什么/谁/何时*；
输出 connector 负责投递的*如何*——它消费该传输层，而绝不重新实现它。

## 它是什么

产品中的每个模块都会以一条最小数据的 finding 形式在总线上报告告警
（[`finding.reported`](/zh/reference/events/)），并带有命名空间化的 `Kind` —— 可靠性
（`health_subject_down`）、开销（`finops_budget`）、安全（`security_guardrail`）、
评估回归（`eval_regression`）、驻留地（`compliance_residency_violation`）、
编排节奏、语音等等。模块 XV **只**订阅这唯一一条产品级告警通道，并按 `Kind`、严重程度、
源模块和主体进行路由。它有意**不**订阅诸如 `cost.sampled` 或 `edge.observed` 这样的原始遥测：
一条开销*告警*是以 `finops_budget` finding 的形式抵达的，而不是一条成本样本。
这就是把整个产品的 finding 转化为可执行通知的接缝。

## 契约与实体

本模块在共享数据模型中声明了两个租户级实体：

| 实体 | 模式 | 它持有什么 |
|---|---|---|
| **route** | 可变、受审计 | 一条路由规则：对事件类型、finding-kind 通配符（如 `health_*`）、最低严重程度、源模块和主体类型的谓词 → 一个具名**目的地**，带有按路由的去重与节流窗口以及优先级。**不持有任何目的地凭据**——只持有一个非机密的目的地名称。 |
| **delivery** | 仅追加 | 每次投递*尝试*的证据 ledger：路由、目的地、finding kind、严重程度、主体引用、简短标题、一个关联哈希，以及一个结果类别（`delivered`、`failed`、`no_dispatcher`、`unknown_destination`）。 |

每收到一条 finding，本模块就按优先级顺序评估租户已启用的路由；
每个留空的谓词维度都表示*任意*，通配符匹配支持精确或 `prefix*` 形式。
匹配发生在一个读视图内，**网络投递严格地在任何存储事务之外运行**，
结果随后被写入仅追加 ledger。创建、更改或删除一条路由，以及发送一条测试通知，都是
**特权、自审计**的操作，归属于真实主体。route 与 delivery 路由发布在独立的 **beta**
[模块路由参考](/reference/api-beta/)中，而非稳定核心契约中；它们的字段级形状存在于
产品的类型化接口中。

## 它消费什么、产出什么

- **消费** [`finding.reported`](/zh/reference/events/) —— 那条唯一的产品级告警通道。
  它是一个路由器，而非探针或计量器：它从不轮询基础设施，也从不进行测量。
- **产出**通过一个分发接缝发出的出站通知，由输出 connector 支撑（Slack/Teams、
  PagerDuty/Opsgenie、签名 webhook，以及一个覆盖 Splunk/Elastic 的 SIEM 目的地，
  经由 CEF/LEEF/syslog/OTLP）。一条通知只携带 finding 已经安全的展示字段——标题、kind、
  严重程度、主体引用和一个关联哈希——**绝不**携带载荷、prompt、机密或 PII。
  **最小数据是传输层的一项属性**，而非事后过滤。目的地机密只存在于运营方所置备的
  connector 配置中，在此仅以一个非机密名称引用。

:::caution[诚实的限制]
- **默认二进制随附一个 deny-closed 的分发器。** 在运营方置备目的地之前，分发器已装配但为空：
  一次未匹配的投递在 ledger 中记录为 `no_dispatcher`，一个配置错误或未知 kind 的目的地解析为
  `unknown_destination`。它**绝不伪造成功**——一次未投递始终是可见的。
- **出站 webhook 是一个目的地 connector，不是 OpenAPI webhook。** 它是 control plane
  向其推送的输出通道，而不是你针对产品 API 注册的回调。
- **去重与节流抑制的是*发送*，而非一个结果。** 一条被去重或被节流的通知有意**不**写入
  delivery ledger（因此它从不被虚增）。相比之下，每一次实际的投递*尝试*都会被记录——
  无论是 `delivered`、`failed`、`no_dispatcher` 还是 `unknown_destination`——
  因此一次未投递始终可见，绝不会被静默丢弃。
- **connector 的原始错误从不被持久化或记录到日志**——只记录一个非敏感的结果类别——
  因为一个传输错误可能在其 URL 中携带目的地机密。
:::

## 相关内容

- [模块目录](/zh/reference/modules/overview/) —— 模块 XV 所处的位置以及 Govern/Actuate 划分。
- [推送到你的 SIEM](/zh/how-to/cookbook/push-to-siem/) —— S2S 推送驱动
  （`modules/siemforward`），它把 finding 和封存的审计 ledger 重塑为某座塔的原生方言
  （OCSF/CEF/LEEF/syslog/OTLP），并搭乘 eventing 平台的持久化投递——
  作为上述目的地的推送补充。
- [事件总线参考](/zh/reference/events/) —— `finding.reported` 事件及其 `FindingReport` 载荷。
- [访问与资源 map](/zh/reference/modules/iii-access-map/) —— 一份同属 Core/Intelligence 的参考。
- [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/) —— 配置一个 SIEM 目的地。
- [治理与审批](/zh/how-to/govern-and-approve/) —— 对本模块路由的 finding 采取行动。
- [诚实与限制](/zh/start/honesty-and-limits/) —— 贯穿产品的默认 deny-closed 立场。

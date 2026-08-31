---
title: "模块 X — 模型与提供方管理"
description: >-
  覆盖整个 AI 模型栈的治理层——Claude、OpenAI、Gemini 与本地推理。一个版本化的参考
  目录、能力矩阵与路由策略，可解析出 主选 + 回退链；它进行路由，但尚未执行模型调用。
---

模块 X 治理**整个 AI 模型与提供方栈**——Claude、OpenAI、Gemini 与本地推理，而不仅仅是某一家厂商。
它是一个 **Core（核心）层**模块，位于模型/提供方 connector（连接器）*之上*：它不会重新实现任何
提供方集成，也不会重新实现推理网关。它所拥有的是**治理层**——一个版本化的目录、一份跨厂商的能力
矩阵，以及具名的路由策略。

## 它是什么

该模块把 inventory（清点，模块 I）所发现的裸 `Provider`/`Model` 实体，变成一个受治理的目录。
分为两半：

- **一份声明的参考目录**——一张在仓库中版本化、可由操作员覆盖的模型族表，附带它们所声明的 API 特性
  能力与**列表价（list-price）默认值**。价格会被打上其声明日期的戳记（`pricing_as_of`），并明确标注
  为*需对照各提供方定价页核实的默认值*，绝不是被捏造的遥测数据。一个没有匹配条目的模型族会保持
  **未定价（unpriced）**，而不是被赋予一个杜撰的价格。
- **对实时 estate 的丰富**——该模块监听 [`cost.sampled`](/zh/reference/events/) 流，并用模型族、上下文
  窗口、模态、每 token 定价与能力集来丰富所发现的 `Model`/`Provider` 实体（inventory 将这些定价
  字段交由它处理）。

能力词汇表是一份**跨厂商矩阵**——完整的 Claude 栈（prompt caching、batch、Files、citations、
extended thinking、computer use、memory 工具、context management、vision/PDF、structured outputs），
外加其他每家厂商实际暴露的对应能力——因此 UI 渲染单一矩阵，而一项路由策略可以*跨*厂商要求某项能力。
Claude 模型族按族编目（`claude-opus`、`claude-sonnet`、`claude-haiku`、`claude-fable`、`claude-mythos`），已弃用/遗留版本保留在更长的
前缀下，从而使当前 id 解析到当前的价格档位。

## 它的契约与实体

路由是其执行面，且它是**仅路由（routing-only）**的：

- **路由策略**持久化在核心 `Policy` 实体上（`Kind="routing"`）：具名的选择 / 回退 / 版本锁定策略
  （最便宜优先、最低延迟、按能力排序，或一个锁定的模型）。`POST …/routing-policies/{id}/resolve`
  会针对受治理的 estate 解析一项策略，并返回一条 **主选 + 回退链**，附带做出该选择的原因。这是
  **只读的**：它计算出一个选择，随后由 connector/网关执行——该模块**不执行任何推理**。
- **API-key / 工作区治理**是**仅最小化数据的元数据**——哪个智能体或团队使用哪个凭据，以一个掩码提示
  携带，绝不含密钥的实际值。
- 一份只读的 **Anthropic 速率限制清单**（网关或代理必须保持同步的上限）作为一个可查询的清单被提供；
  它绝不是该模块会变更的控制项，并且当只读的 Admin connector 未被预置时，它会降级为一个诚实的
  *附原因的不可用（unavailable-with-reason）*响应。

目录与特性读取不敏感，门控在 viewer（查看者）档位；路由与密钥治理变更是一项 editor（编辑者）档位、
被审计的更改；而受治理执行路径是一项 admin（管理员）档位的操作，与读取档位的 resolve 截然不同。
这些路由发布于独立的 **beta** [模块路由参考](/reference/api-beta/)中，而非稳定核心契约；
它们的字段级形态存在于产品的有类型接口中。

## 它消费什么、产生什么

该模块从[事件总线](/zh/reference/events/)**消费** `cost.sampled`，以真实的每 token 定价与用量来丰富
目录；它不会引入新的观测类型。在受治理执行路径上，一次成功的调用会向 FinOps **产生**一份已脱敏的
`CostSample`——模型输出会发给调用方，但在此处不会被持久化到任何地方。金额从不出现在这个面上：
不返回任何 USD 数额，只返回 token 计数与所服务的目标。

:::caution[诚实的局限]
- **仅路由式执行。** 该模块**解析**出一条路由（主选 + 回退链），但**不执行模型调用**。受治理执行路径
  是一道 **deny-closed 接缝**：在没有预置 executor 的情况下，它返回一个明确的 `503`——control plane
  （控制平面）可以*选择*一个模型，但不会向某个提供方*消费*。当一个 executor 接入后，一个处于其上限的
  FinOps 预算会在任何提供方调用*之前*拒绝该项消费。
- **所声明的定价是一个默认值，而非保证。** 列表价是经操作员核实、打上日期戳记的默认值；真实用量的
  权威成本始终是由 connector 派生的 `CostSample`，而绝不是那个便于参考的每 token 数字。未匹配的
  模型族会显示为未定价——绝不会带着一个杜撰的价格。
- **刚刚发布的模型会被列出但加上标记。** 一个其能力尚未对照 model card 核实的预览模型，会被编目并将
  其能力集标记为 *待确认（to-confirm）* 且保持未定价，而不是去捏造这些数据。
- **密钥清单是元数据，绝不是密钥本身。** 该模块持久化治理关系与一个掩码提示；凭据值绝不离开提供方的
  Admin API，也绝不被存储。某些提供方根本不暴露密钥清单——这是一项有记录的局限，而非一处疏漏。
:::

## 相关

- [模块目录](/zh/reference/modules/overview/) — 模块 X 的位置及其执行状态。
- [访问与资源地图](/zh/reference/modules/iii-access-map/) — R/RW map（读/读写访问地图）与最小权限漂移。
- [事件总线参考](/zh/reference/events/) — 本模块所消费的 `cost.sampled` 事件。
- [架构概览](/zh/explanation/architecture/overview/) — 引擎、各层与 connector。
- [治理与审批](/zh/how-to/govern-and-approve/) — 对路由与治理实施操作。
- [诚实与局限](/zh/start/honesty-and-limits/) — 广泛观测 / 在一个子集上执行的契约。

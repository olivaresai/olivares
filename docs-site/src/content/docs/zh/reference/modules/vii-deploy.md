---
title: "模块 VII — 部署与集成"
description: >-
  这是唯一会对你的基础设施实施操作的模块：它规划并治理智能体与 MCP 服务器及其与
  estate 之间连接的声明式生命周期。变更经过 human-in-the-loop 门控、先 dry-run
  后 apply、且可回滚——而在 executor 被预置之前，实时 apply 始终保持
  deny-closed（503）。
---

模块 VII 是**唯一**会变更客户基础设施的模块——产品的其余每个部分都是 read-first（先读取）的。
它以**声明式、版本化、可回滚**的操作来预置、更新和退役智能体与 MCP 服务器，并声明智能体到达
企业资源时所使用的连接方式与所引用的身份。正因为它会实施操作，其安全标准在整个产品中最高，
而实时执行被保持在一道 deny-closed 接缝之后，直到操作员显式将其预置为止。

## 先规划与治理，然后（也许）apply

生命周期为 `plan → apply → verify → retire`，将**期望**状态与**实际**状态进行协调。真正关键的
区分是**声明 ≠ 变更**：

- **声明**期望状态——创建、更新、回滚一份定义（也可通过 manage-as-code 的
  `olivares_deployment` 资源进行）——仅限于 control plane（控制平面），**绝不触及基础设施**。
- **`plan`** 是纯粹的 dry-run 差异计算；**`verify`** 检查漂移并刷新快照。两者都不会变更。
- **`apply` 与 `retire`** 是仅有的会变更的操作。它们是**两阶段**且 **deny-by-default（默认拒绝）**的：
  第一阶段计算差异并*请求*一项绑定到 plan hash 的人工审批，期间不改变任何东西；第二阶段仅在审批为
  `approved` **且** plan hash 仍然匹配时才继续——任何其他状态（pending、expired、rejected、无门控、
  陈旧的 plan）都会被拒绝并记录。重新指定会改变 hash 并使审批失效（anti-TOCTOU，防时序攻击）。

会变更的 apply/retire **默认不是实时的**。执行接缝（[`Executor`](/zh/reference/modules/overview/)）
是 deny-closed 的：在没有预置 executor 的情况下，apply/retire/plan/verify **会以 `503` fail closed
（失败即拒绝）**——control plane 可以声明期望状态，但无法协调到实际基础设施。一个真正的引擎
（Tofu/Terraform、GitOps、Kubernetes、Docker、Nomad、Crossplane）外加一个短时、按操作、经过证明的
凭据来源，**只在操作员配置时**才会接入；在此之前，该模块绝不会静默地实施操作。

## 实体与所声明的契约

该模块声明了四个带命名空间的实体，外加作为已应用快照的核心 `Deployment`：

| 实体 | 角色 |
|---|---|
| **definition** | 期望状态——期望版本对比已应用版本、spec hash、指向核心 `Deployment` 的链接 |
| **revision** | 仅追加、不可变的 spec 历史——可回滚的来源 |
| **wiring** | 它所声明的 `agent → resource` **被许可的（permitted）**连接（即模块 III 用以对照的契约）|
| **operation** | 仅追加的变更管理 ledger（账本）——版本、plan hash、由谁审批、结果 |

期望 spec 是**有类型的，并从结构体重新序列化得来**（绝不经过操作员的 JSON 往返）：未知字段会被拒绝，
会运行一道内联凭据守卫，任何以明文携带凭据材料的 spec 都会**在声明阶段被拒绝**。凭据**仅以引用方式**
传递（`<scheme>:<locator>`，scheme 在白名单内）——这是连接的一种属性，绝不是被存储的密钥。

## 它在事件总线上产生什么（模块 III 的“被许可”一侧）

模块 VII 从不写入 access map（访问地图）；模块 III 是其边的唯一写入者。在一次已提交的 `apply` 上，
针对每条 wiring，该模块会发布一个 policy-grant 类型的
[`edge.observed`](/zh/reference/events/) 事件（`Source = policy`），其中仅携带引用与模式（mode）。
模块 III 将其协调进自身 permitted-vs-observed（被许可 vs 被观测）差异中**被许可**的一侧——因此本模块
所声明的内容，恰恰就是模块 III 用来对照其所观测内容的依据。身份按智能体通过治理进行绑定：一个稳固、
唯一的非人类身份会产生一条 `attributed`（已归因）的边；一个共享或缺失的身份则被报告为 `approximate`
（近似）——**标记出来，绝不伪造**。

:::caution[诚实的局限]
- **实时 apply 是一道 deny-closed 接缝。** 在没有预置 executor 的情况下，`apply`/`retire`
  （以及 `plan`/`verify`）会返回一个明确的 `503`。该模块如今会规划、治理、版本化并声明期望状态；
  只有当操作员接入一个 executor 之后，它才会协调到实际基础设施——绝不默认进行，绝不静默地变成 no-op。
- **审批与归因同样 fail safe。** 没有审批门控，每一次变更都会被拒绝；没有身份绑定器，一条 wiring 的
  归因会被降级，而不是被捏造。`Start()` 会针对每一道未接入的接缝告警一次，以便让一个有缺陷的部署
  可见。
- **退役一条 wiring 并不会撤回其已发布的“被许可”边。** 边模型没有撤回动词；该 wiring 会被标记为
  revoked（已撤销），由模块 III 协调这种陈旧性。是声明出来的，而不是被隐藏的。
- **后端深度各异。** 在各执行后端之间，某些观测路径比另一些更浅（例如某些运行时上仅有表层健康检查）；
  这些被注明为诚实的差距，绝不报告成被捏造的 in-sync（已同步）。
:::

## 相关

- [模块目录](/zh/reference/modules/overview/) — Govern/Observe（治理/观测）与 Actuate（执行）的区分及 `503` 接缝。
- [模块 III — access map](/zh/reference/modules/iii-access-map/) — 消费本模块所声明的“被许可”wiring。
- [事件总线参考](/zh/reference/events/) — `edge.observed` 事件及其最小化数据载荷。
- [治理与审批](/zh/how-to/govern-and-approve/) — 每一次变更背后的 HITL 审批流程。
- [诚实与局限](/zh/start/honesty-and-limits/) — 如今哪些会执行、哪些不会。
- [架构概览](/zh/explanation/architecture/overview/) — 模块 VII 在 Management（管理）层中的位置。

> 机器翻译。英文版本为权威来源。

# ADR-0013: 授权 PDP——内嵌 Cedar + OPA-over-HTTP 适配器

- **Status:** accepted（仅收紧，范围限定于本记录创建的 `auth.PolicyEvaluator`
  接缝）——**由 ADR-0019（2026-06-15）修订**：移除基础 permit——此前被该覆盖层
  静默抵消的运维方 permit 规则现在会真正授权，同时 Cedar 本身已在另一个接缝中转为
  正向、带作用域的授权引擎。仅含 forbid 的策略不受影响；下文“背景”和“决策驱动因素”
  中“绝不放宽”的措辞若按上述广义理解，已被取代——见文末修订说明。
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares
- **References:** NHI/MCP-auth contract；由 ADR-0019（Cedar 带作用域授权）修订

## 背景与待解决的问题

在 RBAC 之上，平台还需要一个策略决策点（PDP, policy decision point）来支持基于属性的授权（ABAC）。各组织需求各异：有的想要一个自包含的引擎，有的已有现成的 OPA 资产（estate）。PDP 绝不能*放宽*访问权限——只能收紧。

## 决策驱动因素

- 自包含运行（单一二进制、隔离网络），无需外部策略服务。
- 在运维方已有 OPA 部署时能够契合。
- 一条仅收紧（restrict-only）的不变式：策略可以拒绝，但绝不能在 RBAC 之外授予权限。

## 考虑过的选项

- **两者兼备：** 内嵌 Cedar（主引擎，纯 Go）**以及**一个 OPA-over-HTTP 适配器，置于同一接缝（seam）之后，由运维方选择。
- **仅 Cedar。**
- **仅 OPA。**

## 决策结果

选定方案：**两者兼备，置于单一 `PolicyEvaluator` 接缝之后**。**Cedar** 是内嵌的纯 Go 主 PDP；同时提供一个 **OPA-over-HTTP** 适配器；运维方通过 `OLIVARES_PDP_ENGINE = cedar | opa | none` 选择引擎。该 ABAC 接缝**只收紧**（它与 RBAC 做 AND 运算，绝不放宽）。仅收紧的不变式经过端到端测试。

### 后果

- **好处：** 默认自包含（Cedar，无 sidecar）；在需要时契合 OPA 资产；一个接缝、两个引擎。
- **坏处／权衡：** 需维护两个适配器；OPA 路径的传输强化（例如到 sidecar 的 mTLS）是一项有文档记载的扩展，尚未完成。
- **中性：** `none` 会禁用 ABAC 层，仅保留 RBAC 的默认拒绝（deny-by-default）。

## 为何否决了其他选项

- **仅 Cedar**——会把已标准化采用 OPA 的组织排除在外。
- **仅 OPA**——会强制每次安装都引入一个外部策略服务，破坏自包含／隔离网络的默认形态。

## 修订（2026-06-15，ADR-0019）

*（修订决策日期为 2026-06-15；本说明添加于 2026-08-17，当时一次决策登记册检查发现，
两份记录相隔十一天签署，却没有优先关系链接。上文内容不作改写。）*

**哪些表述不再按原文成立。** “背景”中的 *“PDP 绝不能放宽访问权限——只能收紧”*
以及决策驱动因素中的 *“策略可以拒绝，但绝不能在 RBAC 之外授予权限”*，按原文会被
理解为针对**整个授权决策**和 **Cedar** 的断言。自 **ADR-0019** 起，这两项广义理解均
不成立：Cedar 已从仅拒绝的叠加层提升为三值、作用域感知的**授权**引擎；使 Cedar
决策只能收紧的隐式基础 `permit` 也已移除，因此显式编写的 `permit` 现在会真正授权。

**实际成立的内容。** 仅收紧不变式仍然成立，但**只限于本记录创建的接缝**：
`auth.PolicyEvaluator` 仍在 RBAC 之后执行，只能进一步收紧
（`core/auth/authorizer.go:100-104`）。正向授权位于**另一个新接缝**
`auth.ScopedAuthorizer` 中，它装配在 deny-overlay **旁边**而非内部；Authorizer 按
`Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay` 组合它们
（`core/auth/authorizer.go:157-163`，代数见 `:161` 与 `:200`）。默认拒绝、forbid
覆盖 permit，以及出错时 fail-closed 均得以保留；未编写任何 grant 的部署会与本记录
下的决策完全相同。其余内容仍然成立：同一接缝后的两个引擎，以及
`OLIVARES_PDP_ENGINE = cedar | opa | none` 选择器，都是已交付行为
（`cmd/olivares/wire.go:994-1018`）。

**当前决策所在位置。** `docs/adr/0019-cedar-scoped-grants.md`（accepted，
2026-06-15，Fran Olivares）明确引用本记录。只引用此 ADR——即术语 *ABAC* 所指向的
那一份——的读者，可能误以为已交付的正向授权路径违反了已签署决策。事实并非如此；
它遵循 ADR-0019。

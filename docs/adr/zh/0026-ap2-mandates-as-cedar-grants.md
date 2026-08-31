> 机器翻译。英文版本为权威来源。

# ADR-0026: 把 AP2 payment mandate 作为 Cedar scoped grant（受治理的采购）

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## 背景与问题陈述

Agentic payment 正以协议层的形式到来。Google 的 **AP2（Agent Payments Protocol）** 是其中最受关注的
之一；其当前规范是 **v0.2.0（发布于 2026-04-28）**，并在同一天捐赠给 FIDO Alliance。AP2 让 user 把一份
签名的 **mandate** 委托给 shopping agent，agent 随后把它绑定到一笔具体的 transaction，由 **Verifier**
（merchant、credential provider、network、payment processor）校验。

有两个事实决定了本决策的形态：

1. **时效性（实测的现实胜过计划）。** 早先的规划依据的是 AP2 v0.1，描述了一个由“verifiable
   credentials”签名的 *Intent / Cart / Payment* mandate 三元组。该模型已被**取代**。v0.2 恰好定义了
   **两种** mandate 类型——**Checkout Mandate** 与 **Payment Mandate**——每一种都有 **Open** 状态
   （携带 constraint、由 user 签名）和 **Closed** 状态（绑定到 transaction；agent 针对 open mandate 的
   `cnf` claim 中的 key 生成 Key Binding JWT / Proof-of-Possession）。Mandate 是 **SD-JWT**
   （RFC 9901）；**binding hash / Key Binding JWT 必须使用非确定性方案（ES256/ECDSA），而不是确定性
   方案（Ed25519）**——规范称这是为了保护 hash binding。本 ADR 面向 **v0.2**，并固定到已发布的 `vct`
   schema 后缀（依 v0.2 规范为 `mandate.checkout.1` / `mandate.payment.1`；构建时对照规范的
   `docs/ap2/*` 验证）。

2. **Olivares 是什么——以及不是什么。** Olivares 是一个 **governance control plane**：一个 Policy
   Decision Point（PDP）和一个篡改可检测的 evidence ledger。它**不是** payment processor、PSP、
   card network、wallet 或资金托管方，本 ADR 也不会让它变成其中之一。AP2 本身仍是 **pre-1.0**，
   **采用处于早期、且很大程度上停留在愿景层面**（PayPal 自己的页面只在分类意义上提到 AP2，重点是
   OpenAI 的 ACP 与 Google 的 UCP；Mastercard 的“Agent Pay”是另一个独立项目；“60+ 家机构”这一数字是
   2025 年 9 月发布时的统计；FIDO 签署方名单约 12 家）。诚实标注禁止宣称超出可验证范围的 AP2 支持。

问题在于：**Olivares 如何用它已经拥有的 primitive 去治理一次由 AP2 中介的 agentic 采购——带着一个具体的
enterprise use case 诞生，并覆盖 AP2 有意留给上层的空白——同时不引入 authorization fall-through 或静默的
constraint 降级？**

本设计诞生时所依托的具体 use case：一个**受治理的采购 agent**——enterprise 通过一个在 AP2 open mandate
之下运行的 agent 采购，该 mandate 的 constraint 编码了采购政策（budget ceiling、允许的 supplier、
单项限额、周期性、执行窗口）；Olivares 依据该政策授权每一次具体采购，把高金额的升级给人工，并把
mandate+receipt 封存为不可抵赖的 evidence。

**前置条件（in-path gate）。** 下文的每一项保证，只在 deployment 把采购**路由经过 Olivares 这个
in-path gate** 时才成立——agent 必须先取得一份新的 Olivares 授权，才能向 settlement layer 出示 closed
mandate。作为旁路/建议性的 PDP，Olivares 与 AP2 一样，都够不到一份已经交给 merchant 的 closed mandate。
构建必须记录这一 deployment 要求。

## 决策驱动因素

- **复用现有的 authorization plane，不要 fork 它**——但只在语义确实匹配之处（见下文的 Abstain-vs-deny
  更正）。
- **在我们这一层覆盖 AP2 声明的空白**（见配套的 threat-model spec）：AP2 **没有 revocation**，把
  verifier 侧的 double-spend 拒绝定为**可选（MAY）**，**不**证明人类身份 / SCA，**对 clock trust 未作
  规定**，并把 evidence 的保留/检索与责任归属排除在 scope 之外。一个“假定所有 agent 都是潜在攻击者”的
  PDP（AP2 自己的 threat model）必须把这些变成强制项。
- **对任何无法建模的东西 fail closed。** 我们无法编码的 constraint、agent 隐瞒的 disclosure、未知的
  算法——每一种都必须拒绝该 mandate，绝不放宽它。
- **诚实的 scope 与 pre-1.0 风险。** 现在做设计，固定到 `vct`，不发布我们无法验证的宣称，让 Olivares
  严格待在 PDP/evidence 这一侧。

## 考虑过的选项

- **选项 A——把 AP2 mandate 作为 Cedar scoped grant；Olivares 充当治理性的 Verifier/PDP。**
  把 AP2 **open mandate** 建模为一条与那一份 mandate 绑定、经编写的 **Cedar grant**（ADR-0019），其
  `when` 条件就是 mandate 的 constraint；把 **closed mandate** 当作一次 **authorization request**
  （principal = `cnf` 中的 agent key；action = `purchase`/`pay`；resource = payee / checkout），并
  **对 payment action 按 deny-by-default** 求值。Olivares 作为 PDP 执行 AP2 的验证规则，把高金额者经由
  一次性 HITL 批准 gate 起来，以 fail-closed 的方式 reserve FinOps budget（ADR-0025），并把完整签名的
  mandate+receipt 封存为 evidence。
- **选项 B——与 Cedar 平行的定制 AP2 mandate engine。**
- **选项 C——仅观望。**

## 决策结果

选定方案：**选项 A**，因为 constraint 模型可以映射到 Cedar grant 条件，而周边的控制（approval、
reserve ledger、signed audit chain）也都已存在——**前提是完成下述三项语义更正**，否则这种复用并不安全。

### 让这次复用成立的三项语义更正

1. **Payment action 是 DENY-BY-DEFAULT，而不是 abstain-defers-to-RBAC。** 当没有 permit 匹配时，
   scoped-grant engine 返回的是 **`EffectAbstain`**（而不是 deny）——“没有 grant”、“grant 已过期”和
   “该 tenant 没有 scoped grant”都会 Abstain，而 Abstain 意味着*base RBAC 决策继续生效*
   （`modules/governance/grants.go:31-38`，即 RBAC 向后兼容不变式）。天真地把“没有匹配的 mandate”等同于
   “deny”是**错误的**：cnf 不匹配、mandate 过期或 grant 被撤销都会 Abstain，并可能 fall through 到一次
   **RBAC allow**。更正：`purchase`/`pay` **只**由一条匹配、有效、绑定 mandate 的 grant 授权，
   **没有 RBAC 回退**。构建必须通过以下之一强制这一点：(i) 证明 base authorizer 不向任何 role 授予
   `purchase`/`pay` permit（于是 Abstain→deny），或 (ii) 一个 payment overlay，把 payment action 上的
   Abstain 当作 deny。存在但无效的 mandate 还会额外编写一条显式的 **`forbid`**。必须有一项 conformance
   test 断言仅凭 RBAC 绝不授权一次 payment。

2. **mandate→grant translator 对任何无法建模的 constraint FAILS CLOSED。** “未知 constraint 必须失败”
   是一项**翻译时**的义务，而不是 Cedar 的 deny-by-default 所能提供的：如果 translator 静默省略了一条
   它无法编码的 constraint，它产出的 grant 就**比 user 签署的更宽**，而 Cedar 会放行，因为它从未见过
   那条 constraint。更正：依据一份由已识别的 constraint key、operator 和单位组成的 **allowlist** 进行
   翻译；一旦遇到任何无法识别的元素，就**拒绝整份 mandate，且不编写任何 grant**。

3. **完整 disclosure 是强制的；不可信的 agent 不能隐瞒 constraint。** 在 SD-JWT 中，由 *holder*
   （不可信的 agent）选择披露哪些 disclosure。它可能只出示能够通过的 disclosure，而隐瞒一条更严格的
   constraint。更正：验证 adapter 会枚举 `_sd` digest，只要任何 policy 相关 claim 的 digest **未被披露**，
   就把它当作一条无法求值的 constraint 并 **fail closed**。

### 对应关系（已应用上述更正）

| AP2 v0.2 concept | Olivares primitive (file:line) |
|---|---|
| Open mandate（constraint，由 user 签名） | 绑定到该 mandate 的 `jti`/`sd_hash` 的 Cedar scoped **grant**（`modules/governance/grants.go:67`, ADR-0019） |
| Closed mandate | Authorization **request**，**对 `purchase`/`pay` 按 deny-by-default** 求值（更正 1） |
| “Verification and Processing Rules” | Adapter 链式验证 + 完整 disclosure 检查（更正 3）+ fail-closed 翻译（更正 2）+ PDP 决策 |
| `payment.budget`（累计）/ `amount_range`（单笔） | FinOps reserve ledger（`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025），带一个**全新的 per-mandate reservation key**；对 mandate cap 与全部 Olivares scope 原子地同时 reserve（NOT `min()`） |
| `payment.agent_recurrence`（次数/速率） | **全新的**次数/速率 limiter（在 ADR-0025 之下 TOCTOU-safe）——而不是既有的基于金额的 budget |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Cedar 集合成员 `when` 条件 |
| `execution_date` {not_before,not_after} | 针对 **DDIL trusted signed dead-man clock**（`modules/governance/ddiladopt.go`）的时间条件，并同样注入 SD-JWT adapter |
| User approval；高金额 gating | **一次性 HITL** 批准消费（`modules/governance/approvals.go`） |
| Checkout/Payment Mandate + Receipt（争议 evidence） | 以 `transaction_id` 为 key 的 hash-chained **runtime audit ledger**（`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009）——存什么见决策 1 |

### 本 ADR 作出的决策

1. **Mandate 的表示——authority 与 evidence 是两个不同的存储。**
   - **Authority** 是 **Cedar grant**（被求值的那份 policy），绑定到该 open mandate 的稳定 id
     （`jti`/`sd_hash`），使得一份 closed mandate 只能针对由*它自己*的 open mandate 编写出的 grant 求值
     （防止 **mandate substitution**：持有宽松 mandate A 的 agent 无法让一份 B 的 closed mandate 针对
     grant-A 求值）。grant **绝不**是被当作自证权限的原始 blob。
   - **Evidence** 是**完整的签名工件**：open SD-JWT、closed Key Binding JWT，以及**实际出示的
     disclosure**——加密并受访问控制地保留，以便争议时可以*重放 AP2 的签名验证序列*，而这是一个 hash
     做不到的。这些 evidence 携带 PII（金额、payee），因此它是**加密的最小必要 evidence，而不是“绝不含
     PII”**——最小化数据的规则适用于 *authority/grant* 和运维日志，而不适用于封存的争议记录。

2. **签名验证——链式验证，算法固定，trust root 分离。**
   验证 SD-JWT 链，并通过 `cnf` 绑定的 Key Binding JWT（PoP）验证 open→closed 的链接，确认 closed
   mandate 原封不动地保留了 open mandate 的 claim，并对每一条 constraint 求值（更正 2 和 3）。原始规范
   未给出的两条加固规则：
   - **算法固定。** 把每个 trust-root key 绑定到它被允许的算法集合，并严格据此验证；**忽略 token 自称的
     `alg`**。拒绝 `alg:none`、HS/ES 混淆以及曲线/强度降级——AP2 的 Ed25519 禁令只是一条狭窄的规则，而
     它所处的 header 协商面由不可信的 agent 掌控。
   - **trust root 分离。** **User-Credential** root（OpenID4VP）验证的是*人类授权了* open mandate；
     **Trusted-Agent-Provider** 名单只管辖哪个 agent 身份可以**持有/绑定** `cnf` key。二者证明的是不同
     的事实，并且**各自都必须履行自己的那项义务**——绝不是可以互换的 OR（agent-provider 的 attestation
     不能替代 user 的授权签名）。所需的 root 缺失时 deny-closed。

3. **过期、一次性使用与撤销（限定在由 Olivares gate 的流程内）。** AP2 **没有 revocation**。Olivares 为
   **in-path** 的 deployment 关闭这一点：(a) 绑定 mandate 的 grant 是**一等可撤销**的——撤销它会让针对
   该 mandate 的每一次*未来的 Olivares 授权*都 deny-by-default（更正 1）；它够不到一份已经放行到
   settlement 的 closed mandate（与 AP2 相同的局限——如实说明）。(b) 一份高金额的 closed mandate 会消费
   一次**一次性 approval**，因此 approval 无法被重放。(c) `exp`/`execution_date`/recurrence 依据
   **DDIL trusted signed clock** 强制执行，而且 SD-JWT adapter 的 `now` 也取自同一个 clock，使这两层不会
   互相矛盾。

4. **重放 / double-spend——verifier 侧的去重是 MANDATORY（in-path）。** AP2 把 anti-double-spend 的 MUST
   放在 *shopping agent* 身上（而它在 AP2 自己的 threat model 里就是攻击者），却把 verifier 的检查只定为
   MAY。Olivares PDP 按 open mandate 跟踪已出示的 closed-mandate nonce / `transaction_id`，并拒绝重叠或
   重复的出示——针对那些路由经过 Olivares 的授权（即 in-path 前置条件）。

5. **Olivares 不做的事。** 不托管资金，不执行支付，不发行 card/token，不充当 PSP/network/wallet。
   Olivares 是依据政策授权这次 agentic 采购的 **PDP**，以及封存 mandate/receipt 的 **evidence plane**。
   Settlement 仍然属于 merchant/PSP/network。

### 后果

- **好处：** 在语义真正匹配之处复用 Cedar/reserve-ledger/approval/audit-chain；AP2 的空白变成被强制
  执行的保证；封存的不可抵赖 evidence；诚实且可验证的定位。
- **坏处 / 权衡：** 这次复用是**有条件的**——它需要一个 payment-action deny-by-default overlay、一个
  fail-closed translator、完整 disclosure 强制、一个 per-mandate reservation key，以及一个全新的
  recurrence limiter（都不是免费的）；AP2 仍是 pre-1.0（v0.3 会迫使重新映射，但被隔离在 adapter 之后并
  固定到 `vct`）；保留带 PII 的签名 evidence 增加了加密/保留义务。
- **中性 / 后续：** agent 之间的 mandate 委托**不在 AP2 的 scope 内** → 也不在我们的 scope 内；x402
  （crypto-rail 的 AP2 扩展）与 ACP（OpenAI/Stripe）是独立的，只做跟踪，不在此构建。

## 为何否决了其他选项

- **选项 B（定制 engine）**——否决：它为一个 pre-1.0 的协议重复了 reserve-ledger/approval/audit
  machinery；上述更正表明，只要 payment-action deny-by-default 与 fail-closed 翻译到位，这次复用就是
  可靠的。
- **选项 C（仅观望）**——否决：既定方向是现在就做设计并尽早启动 enterprise 构建，*同时不阻塞公开发布*。
  仅观望会在标准于 FIDO 收敛的过程中白白让出差异化优势（带封存 evidence 的受治理 agentic spend）。
  诚实标注方面的顾虑，是通过现在交付**设计**、把**构建**卡在经过验证的需求之后来满足的，而不是靠什么
  都不做。

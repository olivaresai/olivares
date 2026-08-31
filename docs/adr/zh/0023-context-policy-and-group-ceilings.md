> 机器翻译。英文版本为权威来源。

# ADR-0023: 在三个传输点执行 context-policy，并按 group 设置 window 与 spend ceiling

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## 背景与问题陈述

context-policy（window size 与 compaction strategy）已作为 governed data 持久化，但**从未有 consumer
应用它**——code comment 所承诺的 consumer 并不存在，因此 policy 是无效 metadata。另外，inference proxy
的 token ceiling 仅为**per-tenant / per-request**，FinOps 虽有 `team` budget dimension，却是**detective 且
fail-open**。无法表达并强制“这组 user（或 agent）最多可消耗这么多 window / spend”。

产品愿景需要两项存而不用的 policy 无法提供的能力：

1. **context-policy 在全部三个 transit point 作出 DECISION**：platform 接触 model request 的 session runtime、
   inline inference proxy、knowledge retrieval，而非仅作为 inert data。
2. **按 group 强制 ceiling**：对 `user_group` 与 `agent_group` 同时限制 **context window** 和 **spend**，
   在 policy 要求时 deny-closed，并进行**诚实降级**（绝不 silent clamp 或 silent allow）。

## 决策驱动因素

- **与 source scoping（ADR-0022）一致。** 复用同一 subject vocabulary 与 `most-specific` precedence，
  让 operator 以理解 source scoping 的方式理解 context governance；不引入第二 decision engine，attack surface 小。
- **ceiling 必须真正是 ceiling。** 可由 more-specific scope *放宽*的数值 limit 并非 ceiling；强制 ceiling
  正是目标。
- **诚实降级。** platform 无法完全核算某项数据（approximate group spend）时，必须向安全方向 fail 并说明：
  绝不错误拒绝，绝不静默允许。
- **复用现有 primitive。** 优先使用 audit ledger、现有 per-subject cost attribution 和现有 proxy deny path，
  而非新建 cross-cutting machinery。

## 决策结果

### 1. `Apply` composition——定性字段 most-specific，security floor 从严，`max_tokens` 取 MIN

`Module.Apply`（`modules/knowledge/context.go:263`）解析 request 的 effective policy：

- **Qualitative** field（`strategy`）采用 **most-specific-wins**，与 ADR-0022 一致。
- **Security floor** **从严** compose：`forbid` 绝对；`redaction_required` 采用 OR；
  `excluded_sources` 取 union。
- **`max_tokens` 通过 MIN compose**（most-restrictive；字段位于 `context.go:62,73`，bound 位于
  `context.go:124`）。这是针对数值 limit 的有意细化：可由 more-specific scope 提高的 ceiling 不是 ceiling。
  若 deployment 更偏好数值 limit 也采用 most-specific，此行为约两行即可 reversible。

### 2. Proxy 中的 agent identity——关闭可达残余（E3-lite），其余诚实推迟

session-inference WIF credential（`sk-ant-oat`）**不**经过 inline inference proxy；后者只认证 platform
自身的 `olvs` / `olvk` token。要为 *session* traffic 完全关闭 agent-identity federation，需要重新设计
inference credential（多日工作，是 ephemeral-WIF mint posture 的一部分），因此**推迟至专项工作 E3-full**。

当前关闭可达部分（**E3-lite**）：`authToken` 传播 `AgentRef` → `AgentIdentity`，models actor-scope
resolver 遵从 **authenticated principal** 而非 caller-declared value（bug fix），让代表 agent 调用的 caller
可在 proxy 中使用 `agent_group` axis。agent ref 始终取自 authenticated credential，绝不取自 request body
（`context.go:278-279`, `query.go:110-111`）。

### 3. 按 group 设置 SPEND ceiling——preventive、本质 fail-open，并有细粒度 fail-closed knob

Budget 增加 `user_group` / `agent_group` dimension，通过 `CheckBudget` **preventively** 强制；group spend
在现有 per-subject cost attribution 上通过 **member fan-out** 求和（不存在 group column；不加区别地汇总
所有 row 会导致 mis-attribution bug——`modules/finops/ingest.go:75,361`）。

posture 为 **fail-open**——这是 budget check 的性质，也符合产品 *security = deny-closed* 与
*budget = fail-open* 的区分（`modules/models/api.go:639,656`）——并为希望 hard stop 的 deployment 提供
per-budget **`fail_closed`** knob（`modules/finops/budgets.go:102,166,182`）。这一点会**诚实**说明：
preventive group spend 是*近似*而非精确 accounting。coverage 随 attribution 扩展；尚未 attribution 的 spend
只会 under-count group，这是安全方向（绝不会错误 deny）。用于 group 的 detective ingest/finding FinOps backstop
与 local degradation counter 是**已记录后续工作**，有意不 half-wire。

### 4. 超过 window 时 proxy 拒绝——413，绝不修改 client payload

request 超过 effective policy/group window 时，proxy **以 HTTP 413 拒绝**并返回 detail
（`cmd/olivares/inferenceproxy.go:449`）；它**绝不修改 client 的 opaque payload**，而是拒绝而非静默 clamp
（`inferenceproxy.go:550`）。Compaction 与显式 truncation 仅存在于 platform 自行组装 context 的位置
（retrieval 与 session runtime），绝不作用于 caller prompt。没有 silent degradation。

三个 enforcement point 均已 wiring：retrieval（`modules/knowledge/query.go:167` → `:354`）、session runtime
（`modules/sessions/runtime.go:285,623`）和上述 inference proxy。

## 在批准方向内决定并记录

- **九种 context-policy scope-kind**——`session > agent > user > user_group > role > agent_group > kb > workspace > tenant`
  ——在 write handler 验证（`modules/knowledge/context.go:102-103`），使用 nullable、expand-only `effect`
  （既有 module-column reconcile，无 numbered migration）。
- **`surface` 与 `model` 不是 scope-kind。** retrieval 没有 surface，proxy 已将 per-surface window 折入 MIN；
  添加它们只会产生未使用的一般性（YAGNI）。
- 此功能的 **“OTel metric”= auditable event + native finding**，并非 in-module meter。产品 telemetry 通过
  bus 以 finding 进入 observability；新 meter 是 scope 外的 cross-cutting architecture change。

## 考虑过的替代方案

- **`max_tokens` 采用 most-specific composition**：否决——可由 more-specific scope 提高的数值 ceiling
  不是 ceiling，会违背目标。若 deployment 不同意，仍保持轻易 reversible。
- **用于 context/group telemetry 的专用 in-module meter：** 作为 cross-cutting architecture change 否决；
  audit-events + bus-findings path 已传输信号。
- **不使用 member fan-out 而汇总 group 的全部 per-subject spend row：** 否决——会 over-count 且
  mis-attribute；在 authenticated membership 上 fan-out 才是正确、安全的 attribution。

## 后果

- context-policy 从无效 metadata 变成 retrieval、proxy 和 session runtime 上的**实时 decision**。
- 按 group 的 **window** ceiling **严格且采用 MIN compose**；按 group 的 **spend** ceiling **preventive 且
  如实为 approximate**，并有 opt-in `fail_closed`。
- **已登记债务，无 half-wired 项：** E3-full（让 session inference 通过 governed identity 重新路由）、
  FinOps detective group-spend backstop + local degradation counter，以及将 principal（`user` / `user_group`）
  传入 launch gate。均为已记录后续工作。

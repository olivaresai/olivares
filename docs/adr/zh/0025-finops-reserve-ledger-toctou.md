> 机器翻译。英文版本为权威来源。

# ADR-0025: FinOps 的 reserve→commit/release ledger 关闭 budget/spend-limit 的 TOCTOU

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## 背景与问题陈述

`finops.CheckBudget` 与 `finops.CheckSpendLimit` 是只读的 pre-flight admission check：它们汇总 cost
read-model 并回答“该 request 是否处于限定它的 enforcing budget/limit 之内？”。在这个答案与实际 spend
写回（connector 的 `CostSampled` → `onCost` ingest）之间存在一个窗口。**N 个并发 request 读到同一份
pre-spend state，全部通过，合起来突破 limit**——这是一次 check→act（TOCTOU）double-spend。此前的
fail-closed 加固关闭了 `Truncated` degradation 与 availability posture，但 race 本身仍未关闭。

正确的修复必须让“检查 ceiling，再消耗 headroom”成为**原子**操作，而且必须**在 Postgres 上跨 replica**
原子，而不只是在单个 process 内——因此 process 级 mutex 不可接受。

## 决策驱动因素

- **ceiling 必须在 admission 时消耗，而不是在 settlement 时。** 让 N 个并发 request 不可能全部通过的
  唯一办法，是每次 admission 都在下一次读取之前持久地扣掉自己的 headroom。
- **跨 store，同一份 contract。** 同一机制必须在 SQLite（embedded、单 writer）和 Postgres HA
  （多 connection、READ COMMITTED）上都成立。使用 store 自身的原子性 primitive，绝不使用 in-memory lock。
- **实际 cost 只能事后得知。** Output token（因而 cost）在调用前未知。Admission 必须 reserve 一个*估算值*，
  并在完成时对账。
- **诚实的 expiry。** 崩溃的 caller 绝不能永久占住 headroom，而回收 headroom 也绝不能重复计数。
- **不引入新的 schema engine。** 复用 module 的 `ExtensionRegistry` descriptor + 通用 repo 的
  optimistic concurrency。

## 决策结果

一个具备 reserve→commit/release 生命周期的 **dynamic reserve ledger**（`finops.budget_reservation`，
table `finops_budget_reservation`）。`ReserveBudget` / `ReserveSpendLimit` 针对限定该 request 的每一个
enforcing policy 原子地 reserve 估算值；`CommitReservation` 用实际 cost 结算；`ReleaseReservation`
在失败时归还 headroom。各处的 ceiling（`CheckBudget`、`budgetStatus`、`evaluateBudgets`）现在都是
`committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`。

这与既有的**静态** `budgetSpec.ReservedMicroUSD`（计入 limit 的 Priority-Tier capacity commitment）
**不同**。两者都会汇总进 `effective`；本 ADR 增加的是*动态的、per-request* 的那一项。

### 1. 原子性：UNIQUE index 之下按 scope 单调的 `seq`（无 process lock）

每条 reservation 都带一个按 **(policy, period_start, scope_key)** 单调的 `seq`，处于 UNIQUE index
`finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)` 之下。
Reserve = 读 `max(seq)`，读当前 spend + active reservation，若还有空间则以 `seq = max+1` 执行 `INSERT`。

- 两个并发的 reserver 会算出**相同**的下一个 `seq`；UNIQUE index 让恰好**一个** `INSERT` 提交成功，
  并把另一个映射为 `store.ErrConflict`（`mapWriteErr`）。失败方**重试整个 transaction**，重新读取此时
  已提交的状态。这在**不使用任何 process lock** 的前提下把 reserve-check-insert 串行化。
- **SQLite：** `MaxOpenConns=1` 已经让每个 transaction 在单 writer 上串行，因此 reserve 本身即为原子；
  seq index 是双保险 backstop。
- **Postgres READ COMMITTED（真正吃重的场景）：** 不同 connection 看不到彼此未提交的 row，因此正是
  seq 冲突迫使重试。**顺序不变式：** reserve **先**读 `max(seq)`，再读 reserved 总和，并用*那个* seq
  插入——因此一次成功的插入（无冲突）证明我们读到的 seq 就是真正已提交的 max，于是（严格更晚读到的）
  总和看到了此前的每一条 reservation。把这两次读取颠倒过来会重新打开 race（陈旧的总和配上新鲜的
  无冲突 seq 会 over-admit）。可用归纳法证明：第 k 次成功插入看到了此前全部 k-1 条 reservation，
  因此恰好 `floor(headroom/estimate)` 条被 admit。

Multi-policy request 会在**一个** transaction 中 reserve 全部 target（全有或全无）：后面某个 target 被拒
会回滚前面的插入；block 优先于 throttle。

### 2. reservation 的粒度——按 enforcing policy，以 scope 为 key

对 request 匹配的**每一个 enforcing policy 各一行 reservation**，以
`(policy_ref, period_start, scope_key)` 为 key：

- **Budget：** `scope_key` = budget 的 dimension key（global 用 `""`）——每个 policy 一个 scope。会在
  request 匹配的全部 17 个 non-group dimension 上 reserve（常见的 per-request 情形：
  model/provider/agent/workspace/identity/api_key/…）。
- **Per-seat spend limit：** `scope_key` = **actor**，因此源自 org/group policy 的 cap 会**独立地**为
  每个 seat reserve headroom——与 `CheckSpendLimit` 的 per-actor 语义一致。
- **Group-dimension budget（`user_group`/`agent_group`）在这里并不 reserve。** 它们的 spend 是在
  `actor`/`agent_ref` 上的 member fan-out，read-model 没有 group column；fan-out reservation 是更大的
  设计。它们仍由 `CheckBudget` 现有的 preventive path 强制执行。（未决的后续工作——见下文。）

### 3. 估算——reserve 一个估算值，在 commit 时对账

Admission 会 reserve `estimateMicroUSD`（seam 的先验估算——例如对 prompt 调用 `count_tokens`，再加上
`max_tokens` 的输出配额）。完成时 `CommitReservation(handle, actualMicroUSD)` 记下实际值并把该 row
翻转为 `committed`，从而将它移出 active 总和；真实 spend 另行经由 `onCost` 落库。如果估算**过低**，
该 request 可能让 budget 短暂超出 `actual − estimate`——有界，并在实际 spend 被记录后自行纠正。
**默认估算策略是产品决策（见下文）；机制本身与估算方式无关。**

**顺序：** 先 ingest 实际 spend，*再* commit reservation，这样 ceiling 在结算过程中绝不会短暂少计。

### 4. Expiry——一个谓词，而非递减

active-reserved 总和的过滤条件是 `state = active AND expires_at > now`。因此过期的 reservation
**在失效的那一刻起就不再计数**——没有需要递减的计数器，所以**重复计数在结构上不可能发生**。
`SweepExpiredReservations` 只是为 observability/GC 打上终态 `expired`；正确性并不依赖它运行。
TTL（`reservationTTL`，默认 **5 分钟**）是 caller 在 reserve 与 commit/release 之间死亡时的崩溃
backstop；它必须超过最慢的 governed actuation，这样仍在运行的 request 绝不会被丢弃。

### 后果

- **正面：** double-spend 在两种 engine 上都被原子地关闭；修复是增量式的（一张新的 descriptor
  table——`applyModuleTables` 会在全新 DB 和原地升级的 DB 上创建它；不触碰任何现有 migration）；
  `CheckBudget`/status/alert 现在都会反映在途的 reservation，因此 pre-flight 拒绝、hard-cap 信号与
  status DTO 三者一致。
- **代价：** 一次 reserve 是两次写（reserve + settle），而非一次只读的 check；在 hot path 上这只是几个
  额外的小 transaction，与它所守护的 inference 调用相比微不足道。
- **在完成装配之前处于潜伏状态：** 只有当 actuation seam 改为调用 `ReserveBudget`/`Commit`/`Release`
  （带估算值）而不是只读的 `CheckBudget` 时，ledger 才会真正起作用。在那之前 dynamic-reserved 为 0，
  行为不变。把 inference proxy / HITL gate 接上线并选定默认估算值，是剩余的集成工作。

## 未决问题（产品）

1. **默认估算值。** 当 seam 没有先验估算时该取什么？选项：按 model 费率计的 `count_tokens(prompt)` +
   配置的 `max_tokens` 输出配额；每个 request 一个固定下限；或 per-model 的 p95 历史 cost。低估会削弱
   这项保证，高估会过早 throttle。
2. **TTL。** 5 分钟是合适的崩溃 backstop，还是应当跟随 model 的最大 completion time / 按 surface 区分？
3. **Group-budget reservation。** `user_group`/`agent_group` budget 是否也应当 reserve
   （member fan-out），还是对 group ceiling 而言仅做 preventive 强制即可接受？
4. **重试耗尽时的 posture。** `maxReserveRetries`（64）耗尽时，reserve 会 fail **open**（依
   `CheckBudget` 的 contract）。对于硬性的 `block` budget，极端争用时是否应当改为 fail **closed**？

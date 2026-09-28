---
title: "预留、结算与核对 FinOps 准入"
description: >-
  计费操作如何预留支出并获得唯一的 handle，如何确认或释放该 handle，
  重试与延迟确认如何处理，以及如何阅读准入核对报告。
sidebar:
  order: 21
---

计费操作在运行前**预留**其预估支出，并获得**唯一的 handle**。操作运行后，调用方用
该 handle **确认** (commit) 实测成本；若操作未运行，则**释放**该保留。引擎自身的闸门
（推理代理、会话启动和计划任务）会自行完成这些步骤。本页面向调用这些路由的连接器，
以及阅读结果的运维人员。

## 预留

```bash
olivares finops admission reserve --data @reserve.json -o json
```

```json
{
  "scope": "model_gateway",
  "idempotency_key": "gateway/req-7f3a",
  "estimate_micro_usd": 2000000,
  "actor_ref": "alice",
  "dims": { "provider_ref": "anthropic", "model_ref": "claude-sonnet-4" }
}
```

预估金额会针对覆盖该请求的每个强制预算进行保留；设置了 `actor_ref` 时，还会针对该
参与者的支出限额进行保留。应答携带唯一的 handle：

```json
{
  "allowed": true,
  "handle": "0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c",
  "estimate_micro_usd": 2000000
}
```

预估为零时不保留任何金额，也不返回 handle。已超过限额的上限仍会拒绝它。

| 状态 | 含义 |
|---|---|
| 200 | 已准入。未保留任何金额时 `handle` 为空。设置 `"unreachable": "allow"` 时，无法建立的准入会在不保留的情况下准入，并带有原因 `admission could not be established; admitted without a hold (unreachable=allow)`。 |
| 402 | 阻断判定：`action=block` 的预算或支出限额没有余量（由按席位的支出限额拒绝时 `spend_limit` 为 true），预算集合过大无法评估，或租户处于尝试生命周期的激活边界之下或其状态无法读取。原因会指明是哪一种。 |
| 429 | `action=throttle` 的预算没有余量。 |
| 503 | 无法建立准入：无法读取预算存储，键在重试后仍被占用（另一个调用方进行中的声明，或早期构建发布的一对保留，其剩余保留仍在保留金额），键持有早期构建遗留的声明，或键的欠付保留列表无法解码。原因为 `budget store unreachable (deny-closed)`。 |
| 409 | 幂等键被用于不同的内容。 |
| 500 | 该键的准入行未通过完整性检查。在该行修复之前，该键在任何策略下都会被拒绝。 |
| 400 | 文档无法处理：未知的范围、缺少键、负的预估，或架构未公布的字段。 |

因上限产生的 402 或 429 是确定性的：在新周期或更高限额之前，不要重试同一操作。因激活边界产生的 402 在尝试生命周期管理该租户账本期间持续有效。402、429、503 或 500 拒绝同时也会记为审计行 `finops.admission.denied`。

无法建立准入时的默认策略是**拒绝**。在预留文档中设置 `"unreachable": "allow"`，即可在不保留的情况下准入此类请求：应答为带有上述原因的 200，引擎日志以 ERROR 级别记录租户、范围、`posture=allow outcome=admitted` 和失败类别，从不记录存储自身的消息。未知值即为拒绝。

## 重试规则

- **预留。** 在重放窗口内，使用相同幂等键和相同内容的请求会得到首次调用获得的
  handle（`"replayed": true`）。窗口为自应答起五分钟；保留被确认后，为自确认起
  五分钟。窗口之外，以及未保留任何金额的调用，请求会被重新评估。
- **确认。** 在应答不确定之后，可按需多次用相同 handle 和相同金额重复 `commit`。
  每次重复都得到相同的行。确认后使用其他金额会以 **409** 被拒绝：以首次实测成本
  为准。
- **释放。** 可随意重复。释放永远不会撤销确认。

## 确认与释放

```bash
olivares finops admission commit --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c","actual_micro_usd":1500000}'
olivares finops admission release --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c"}'
```

请在确认前导入实测成本，以免上限少计。文档只以 `handle` 指明保留，任何其他字段都会
以 400 被拒绝。空 handle 不结算任何内容。

| 状态 | 含义 |
|---|---|
| 200 | 已结算，或已按相同方式结算。 |
| 409 | 确认后的其他金额；准入仍是进行中声明的保留，因此没有调用方得到该 handle；或资金现已归属尝试生命周期的保留（`lifecycle_api_required`）。 |
| 500 | 指向该保留的准入行未通过完整性检查。未写入任何内容：保留已导入的成本，并在该行修复后重试同一调用。 |
| 400 | 不是保留标识的 handle，或负的金额。 |

`committed: true` 和 `released: true` 表示调用已被接受，而不表示某行已改变：空 handle 的确认不结算任何内容，已确认保留的释放会使其保持已确认。

## 延迟确认规则

确认记录的是操作已经运行，因此无论多晚到达都会被接受：在释放之后、保留过期之后，
或另一个调用接管该键之后。保留的各行以实测金额变为已确认，并保留其保留结束的时刻。
预算上限不会变化，因为已释放或已过期的行不保留任何金额。对已过期保留的延迟确认会在
下一次核对中降低 `expired_unsettled`。

## 早期准入构建遗留的保留

运行过早期准入构建的数据库可能包含该构建写入的行。该构建以两个保留准入的键，可用
其中任一保留结算，且两者会一起结算。该构建遗留的进行中声明，只有在运维人员声明的
停止时刻下才会被撤回：

```bash
OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT=2026-09-20T09:00:00Z
```

将其设为早期构建的每个写入方停止的时刻，格式为以 `Z` 结尾的 UTC RFC 3339 时间。
启动时读取一次。只有自该时刻起已过去五分钟，且这些写入方遗留的行中没有更晚日期的行
时，恢复才会撤回此类声明。为空（默认值）时不撤回任何声明。不是此类时刻的文本也不撤回
任何声明，并在启动时记录一条错误。报告在 `legacy_stop` 中显示状态：`absent`、
`invalid`、`future`、`contradicted`、`waiting` 或 `usable`。

## 核对

引擎为每个活跃租户每分钟运行一次恢复，每五分钟运行一次核对任务。

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`reconciliation` 只读，需要预算读取权限：不会恢复、清理或上报任何内容。
`reconcile` 是任务，需要预算写入权限：它运行恢复，清理未结算即过期的保留，并在
`drift` 为 true 时上报类型为 `finops_reservation_drift` 的发现。控制台在预算旁显示
同一份报告。

| 字段 | 含义 |
|---|---|
| `active`、`committed`、`released` | 各状态的账本行 |
| `expired_unsettled` | 无人结算的行；TTL 已归还余量 |
| `active_lapsed` | 已过期但任务尚未清理的行 |
| `idempotency_orphans` | handle 在账本中没有行的已预留准入行 |
| `owed_remaining` | 进行中的声明或已发布的行仍欠结算的保留 |
| `legacy_pending`、`legacy_owes_release` | 早期构建遗留的声明和释放 |
| `unresolved` | 结果尚未确定的恢复写入；下一轮会重新判定 |
| `undecodable` | 欠付保留列表无法解码的准入行 |
| `frontier_blocked` | 在激活边界下恢复既无法结算也无法丢弃的保留 |
| `corrupt` | 未通过完整性检查的准入行；只计数，从不写入 |
| `drift` | 当 `expired_unsettled`、`active_lapsed`、`idempotency_orphans`、`unresolved`、`undecodable` 或 `corrupt` 不为零时为 true |

`unresolved` 和 `frontier_blocked` 只由 `reconcile` 的恢复轮次计数；`reconciliation` 将它们报告为 0。

当 `owed_remaining` 到 `corrupt` 的计数全部为零时，恢复对该租户已无事可做。请将
`corrupt` 行视为该租户存储的完整性故障，并与最近的备份比较。它所指向的保留会按 TTL
过期；在该行修复之前，没有任何操作会结算它们。

## 范围

| 范围 | 调用方 |
|---|---|
| `model_gateway` | 推理代理和模型路由：调用前预留，之后确认或释放 |
| `session_launch` | 受管会话的启动和语音通道的开启 |
| `scheduled_job` | 编排触发、评估裁判和 MCP 任务 |

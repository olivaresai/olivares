---
title: "配方：预算与 FinOps guardrail"
description: >-
  为 AI 支出设定硬性的美元上限 —— 按模型、团队、workspace 或单个身份：在阈值处告警，
  然后在上限处限流或阻断。外加 cost-per-outcome，让支出有一个分母。
sidebar:
  order: 2
---

**目标：** “这个团队的 agent 在每月 $500 处停止花钱” —— 声明一次、实时强制执行，并在
逼近过程中带有告警阈值。

预算强制执行是**在默认二进制中即可生效**的几种执行（actuation）之一：一个处于上限的强制
预算会拒绝该支出，无需任何额外预配（[modules 目录](/zh/reference/modules/overview/)将其
标记为 `v1 | v1`）。

## 创建一个预算

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **金额以 micro-USD 为单位**（`limit_micro_usd: 500000000` = $500），因此契约中不存在
  浮点歧义。
- **`dimension` + `key`** 界定预算的范围。可界定范围的 dimension 包括 `global`、`model`、
  `provider`、`agent`、`session`、`team`、`project`、`workspace`、`api_key`、`actor`、
  `service_tier`、`context_window`、`inference_geo`、`gateway` 以及 `identity`。
- **`action`** 是强制执行模式：

| `action` | 到达上限时 |
|---|---|
| `alert`（默认） | 仅 showback —— 触发告警，不拒绝任何东西 |
| `throttle` | 执行 seam 放慢新增支出 |
| `block` | 执行 seam 拒绝新增支出 |

## 为单个身份设定预算

`dimension: "identity"` 以一个 firm 名册身份的 **external id** 为范围 —— 即你的
[身份来源](/zh/how-to/connectors/sso-scim-identity/)所注册的工作负载或 agent 身份：

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

该身份在成本摄取（cost-ingest）时从样本的 agent 绑定、API key 或 actor 解析得出 ——
因此预算会跨表面跟随该身份，而非跟随某一个 API key。

## 看它如何运作

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

到达上限时，一个强制预算的检查会返回 `allowed: false`，连同其动作（`throttle` 或
`block`）以及触发的预算 —— 该拒绝会道出它的理由。告警也会搭乘通知流，因此一个 Slack 或
PagerDuty [目的地](/zh/how-to/forward-audit-to-splunk/)会在 100% 拒绝之前就听到 80%
的越线。

在控制台中，**Cost & FinOps** 按 dimension 展示支出，并内联显示预算状态：

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="带支出趋势与预算态势的 Cost & FinOps 视图。" />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="带支出趋势与预算态势的 Cost & FinOps 视图。" />

## 给支出一个分母：outcome

cost-per-outcome 才是让预算成为一场业务对话的关键。上报 outcome（一张已解决的工单、一个
已合并的 PR、一个已关闭的案件），然后读取价值面板：

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

价值摘要包含 **cancellation risk** —— 没有任何 outcome 的烧钱 —— 它是成功度量的诚实
反面。

## 备注

- **刻意的 fail-open：** 如果预算检查本身出错（一次 FinOps 读取失败），推理会被放行而非
  被悄无声息地阻断 —— 一个坏掉的计量表绝不能变成一次停机。该故障会被记录并可见。
- 预留容量（`reserved_micro_usd`）计入上限，因此预算无法通过预先占用来规避。
- `cost_type` 被刻意**排除**在预算 dimension 之外 —— 估算回退（estimated-fallback）的
  条目会搭乘它们所属的那个 dimension，而不是另立一个平行池。

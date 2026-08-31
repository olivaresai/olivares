---
title: "实战手册：人在回路（human-in-the-loop）审批"
description: >-
  把破坏性动作置于受治理的审批之后：开启一个绑定到确切计划的请求，让获授权的
  人员在服务端强制的职责分离与过期约束下做出决定，并把决策记录进审计账本。
sidebar:
  order: 3
---

**目标：** "一次部署应用（或一次编排触发，或一次语音会话开启）在某个*不是*请求者
的人审批之前不会发生——而且该决策是一项被记录的事实。"

审批引擎在默认二进制中即为可用；
[治理模型](/zh/how-to/govern-and-approve/#human-in-the-loop-态势)
解释了其姿态。本手册是其运维配置。

## 1. 接入审批关卡

会变更基础设施的模块动作都要经过人在回路桥接。它通过配置启用——若不启用，这些动作将
保持默认拒绝（deny-closed）：

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

让那个*开启*审批的组件以其 **自己的、绝不属于审批者池的服务账号** 运行。职责分离在引擎侧
强制执行（开启者无法决定自己的请求，且系统令牌根本无法审批）——如果开启者的账号同时也是
审批者，你构建的就不是一项控制，而是一个活性死锁（liveness deadlock）。

## 2. 开启一个请求

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

请求以 **默认拒绝且有时限** 的方式开启，绑定到它所覆盖的确切计划。如果有一条已启用的
审批*策略*匹配 `(action, subject_kind)`，则以该策略的 `required_approvals` 为准——请求方
无法从请求侧降低门槛。

## 3. 做出决定

```bash
# The queue (filter by status / action):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# The decision (approval-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

引擎在服务端强制执行的内容——以下没有一项是客户端约定：

- **职责分离：** 决策者以稳定的用户 id 为键；请求者无法做决定，同一个人也无法决定两次
  （这是唯一索引，而非 UI 规则）。
- **过期：** 已过期的请求永远无法收到具约束力的决定，即便在清扫器把状态物化之前也是
  如此。
- **风险层级底线：** 预先归类为 CRITICAL 的动作（kill-switch 家族、凭据终结及同类）要求
  **每次决策至少有两位不同的、采用强（AAL3）认证的人类审批者**——而且该底线是结构性的：
  试图降低层级的审批策略会在决策点被重新强制回该底线。

## 4. 记录

每一次决策都会与真实行为者在同一事务中追加到审计账本——
`GET /v1/m/governance/approvals/{id}/decisions` 即不可变的轨迹，而
[拉取式导出](/zh/how-to/forward-audit-to-splunk/)会把它带到你的 SIEM。你无法做出一项审计账本
会悄然遗忘的受治理变更。

## 备注

- 若请求长时间未决，`escalate_in_seconds` 会通知职责分离（SoD）团队——对生产关键动作请
  使用它。
- 取消（`POST …/{id}/cancel`）供请求者或管理员针对待决请求使用；它同样会被记录。
- 仍在成熟中的是更丰富的评审 **控制台**；上述引擎侧的保证均为可用
  （[诚实的范围](/zh/how-to/govern-and-approve/)）。

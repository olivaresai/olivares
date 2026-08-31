---
title: "实战手册：资产盘紧急停机开关（及如何演练）"
description: >-
  一次调用即可停止资产盘中每一个受治理的执行动作——或停止单个智能体。设计上启用即快；
  重新启用需要两个人，且整起事件会留下一份证据包。在你需要它之前先演练它。
sidebar:
  order: 5
---

**目标：** 当某个智能体以机器速度出错时，*立刻* 停止它——或停止一切——只用一次经过认证的
调用，并在事后于双人控制下解除停机，且整起事件全程在案。

这种不对称正是设计本身：**启用很快**（管理员层级，无审批关卡——紧急停机绝不能在队列里
排队等待），**重新启用很慢**（两位不同的人类，且该事件会留下供事后复核的证据包）。围绕这一停机刻意
不设破玻璃（break-glass）：已停机*就是*安全状态。

## 启用

```bash
# Stop the whole estate:
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"scope_kind":"estate","reason":"runaway agent incident #1234"}'

# Or stop one agent (by UUID or external id):
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"scope_kind":"agent","scope_ref":"agent:billing-reconciler","reason":"…"}'
```

会立即且失败关闭（fail-closed）地停止的内容：受治理的 **执行（actuation）** 界面——
`claude.tool.use`、`mcp.tool.call`、`deploy.apply`、`deploy.retire`、
`orchestration.schedule.fire`、`voice.session.open`。范围内待决的执行审批会 **在同一事务中
被取消**，因此不会有任何已批准但尚未运行的动作在停机之后溜过去。

刻意*不*停止的内容：观测，以及治理本身（发现、身份生命周期、合规）——你在停机期间仍能看见
并治理。对一个已经停机的范围再次启用会返回 `409`（它对该范围是幂等的，不是一个栈）。

```bash
# Live posture — is anything stopped right now?
curl -ks "$BASE/v1/m/governance/killswitch/state" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

当某条遏制规则触发时，守护（Guardian）规则可以自动启用同一个停机（`stop_agent` /
`stop_estate` 动作）——自动路径与人工路径是同一个关卡，而一次自动停机会发出一条 CRITICAL
发现。

## 重新启用（双人控制）

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/reenable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"reason":"root cause fixed: …"}'
```

这会 **开启一个审批**，绝不直接解除停机。该动作被预先归类为 CRITICAL：**两位不同的人类
审批者**，每次决策采用强（AAL3）认证——而且这条两人底线是结构性的，即便某条审批策略试图
降低层级，也会在事务中被强制执行。请求者不能作为决策者；被拒绝或已过期的请求会开启一个
全新的法定人数（quorum）。

重新启用之后，由再一位人类（不同于启用者、请求者*以及*重新启用者）做的 **事后复核** 才能
关闭这起事件——在复核被记录之前，同一范围无法在未复核的情况下再次执行停机并重新启用：

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/review" … 
curl -ks "$BASE/v1/m/governance/killswitch/$STOP_ID/evidence"   # the evidence pack
```

证据端点返回这起事件的证据包——停机、被取消的审批、决策以及轨迹——可直接交给审计人员。

## 控制台

管理板块中的 **Kill switch** 是同一关卡的一键版本，附带实时状态与重新启用流程：

<img class="light:sl-hidden" src="/console/killswitch-dark.png" alt="Kill switch 控制台视图：资产盘状态与逐次停机的历史。" />
<img class="dark:sl-hidden" src="/console/killswitch-light.png" alt="Kill switch 控制台视图：资产盘状态与逐次停机的历史。" />

## 演练它

一个你从未拉过的紧急停机开关只是一种假设。每季度，在一个维护窗口内：

1. 对一个低风险智能体启用一次 **按智能体范围（agent-scoped）** 的停机；验证它的工具调用
   被拒绝且发现被触发。
2. 走完整个重新启用流程：两位审批者、事后复核、拉取并归档证据包。
3. 端到端计时整个循环——那个数字就是你真实的遏制时延，而这次演练会留下一条完整的账本轨迹
   作为佐证。

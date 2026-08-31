---
title: "实战手册：默认拒绝（deny-closed）策略（Cedar / OPA）"
description: >-
  接入仅做限制的策略决策点（PDP）：一份 Cedar forbid 叠加策略，或一份
  默认放行的 OPA 策略，在发布前完成校验与试运行（dry-run）——这些策略
  只能收紧访问，永远无法放宽。
sidebar:
  order: 1
---

**目标：** 在默认拒绝（deny-by-default）的 RBAC 之上叠加基于属性的限制——
例如，"无论角色如何规定，任何人都不能触碰标记为 `secret` 的资源。"

需要牢记的唯一不变量：PDP **只做限制**。决策按 RBAC ∩ 原生 ABAC ∩ 外部 PDP
组合而成——策略永远无法授予角色模型所拒绝的权限
（[该模型](/zh/how-to/govern-and-approve/#策略接缝abacpdp只能收紧)）。

## Cedar（内嵌，首选）

选择引擎并将其指向你的策略文件，然后重启：

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

一份 Cedar 策略是一层 **forbid 叠加**——基础 permit 代表"RBAC 已经做出决定"，
而你的 `forbid` 规则在其上做减法：

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

两条经对照适配器验证过的编写要点：`resource.kind` 与 `resource.sensitivity`
始终存在于决策输入中（可无条件引用）；任何其他属性都必须用 `has()` 守卫，
否则规则无法匹配。你所写的 `permit` 永远无法放宽决策。

## OPA（通过 HTTP）

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

按 **默认放行（permit-by-default）** 编写 Rego：

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = 不做限制。`false`、缺失的结果，或 **任何传输错误或非 2xx 错误都会失败关闭
（fail closed）**——请求被拒绝，绝不会悄无声息地处于不受治理的状态。

## 校验、试运行、发布

治理模块暴露了一套策略生命周期，使得有问题的策略绝不会盲目上线：

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` 列出已部署的内容；
`POST /v1/m/governance/pdp/explain` 解释某个决策。

## 验证安全特性

- 用一份 **无效的** 策略文件重启：引擎仅禁用外部 PDP 并记录日志——RBAC 与原生
  ABAC 照常治理；控制平面（control plane）不会宕机。
- PDP 施加的每一项限制都会 **被审计**——在一次被拒绝的请求之后检查审计账本
  （ledger）。

## 备注

- 策略是版本化并发布的，而不是生产中可热改的文件——把发布当作一次经过评审的变更。
- 对于需要审批放行（而非直接拒绝）的动作，参见
  [HITL 审批](/zh/how-to/cookbook/hitl-approvals/)。

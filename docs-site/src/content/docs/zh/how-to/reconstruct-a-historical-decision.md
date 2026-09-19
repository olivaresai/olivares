---
title: "重建历史授权决定"
description: >-
  重建能证明什么、不能证明什么，以及审计人员如何在周二撤销之后重放周一的 allow。
sidebar:
  order: 22
---

审计人员可以询问某项策略在过去某日**决定了什么**。控制平面从**证据账本**
作答：已记录的策略版本，以及该决定消耗的输入。它**不会**评估今天生效的策略。

## 命令

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --action SELECT
```

或指定一行：

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP：`POST /v1/m/governance/decisions/replay` 与
`GET /v1/m/governance/decisions/{id}/reconstruct`。本交付不含控制台按钮。
请使用 CLI 或 HTTP。

## 重建能证明什么

状态 `reconstructed` 表示：存在已记录的 `live_authorization`；所点名的
工件被**保留**；本二进制可以运行该评估器（Cedar）；重新评估使用**原始**
工件；没有读取实时 PDP。

`policy_version_id` 是保留工件的 id。它不是路由见证的 `PolicyVersion`，
也不是编写修订号。

## 不能证明什么

- 原执行点是否诚实。已存储的 allow 不是重复该效果的许可。
- 生产者真实性或链完整性。
- Olivares 从未决定过的外部活动。没有记录时答案是
  `COULD NOT RECONSTRUCT`。
- 本二进制无法运行的引擎（此处 OPA/Rego 仅用于编写）。

## COULD NOT RECONSTRUCT

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

`missing` 指出缺失的事实。旧行保持 **unknown**；存储不会用当前策略回填。

## 周一、周二、周三

周一记录 allow，周二撤销，周三 `--at <周一>` 仍从周一的工件回答
**allow**。

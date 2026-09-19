---
title: "阅读 FinOps 准入拒绝与对账"
description: >-
  预算拒绝在线路上的形态、存储不可达时的姿态，以及如何阅读预留与提交的偏离。
sidebar:
  order: 21
---

计费效果必须在执行前 **预留** 估计支出。控制平面随后 **提交** 实测成本，
或 **释放** 占用。

## 拒绝的形态

硬上限 (`action=block`) 返回 **HTTP 402**。软上限 (`action=throttle`)
返回 **HTTP 429**。正文给出动作、预算以及缺少的余量。该端点需要预算写入权限，因此答复的是管理员。推理代理从不转述这个原因：上限以 `budget limit reached` 拒绝，每席限额以 `spend limit reached` 拒绝，都不携带预算名称或金额。

```bash
olivares finops admission reserve --data @reserve.json -o json
```

## 存储不可达（默认拒绝）

无法读取预算存储时，准入 **拒绝**。稳定原因是
`budget store unreachable (deny-closed)`。HTTP **503**。同时写入审计行
`finops.admission.denied`。默认姿态是 **deny**。

## 如何阅读对账

```bash
olivares finops admission reconciliation -o json
```

`drift` 为真表示存在过期未结算的预留。`reconciliation` 只读取，不做任何改动；
`reconcile` 才是作业：它清扫这些预留，需要预算写权限，并发出
`finops_reservation_drift` 发现。将其视为姿态，而不是对支出的静默改写。

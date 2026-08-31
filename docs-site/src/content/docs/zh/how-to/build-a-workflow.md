---
title: "构建受治理工作流（DAG）"
description: "把现有受治理 action 组合成依赖图，在无副作用的情况下审阅执行计划，并在获得与所审阅精确图绑定的人类审批后运行。"
---

**工作流**把平台已治理的 action（触发调度、向其他模块发出 signal、发送测试通知、
等待）串成一张依赖图（DAG）。运行工作流是一次经人类审批的特权 action；每个实际
触及某项内容的 step，都会像单次调度触发一样，在同一份仅追加决策台账中留下
一行。

工作流是**组合，不是新权力**。系统刻意没有可执行命令、调用任意 URL 或携带
payload 的 step kind：图只能在现有 gate 下重新排列 estate 已公开的动词。运行
工作流既要求 admin tier，*也*要求人类审批，因此绝不能借此触及原本无法直接
触及的对象。

## 图的结构

工作流由一组 **step** 构成。每个 step 都有一个在工作流内唯一的短 `ref`、一个
`kind`、有类型的 `config`，以及它在 `depends_on` 中依赖的 ref。图必须无环；在
存储任何内容之前，服务端会同时强制检查无环性、引用是否存在，以及 fan-in/fan-out
边界。

| Kind | 作用 | 经过的 gate |
|---|---|---|
| `schedule-fire` | dispatch 一项现有的受治理调度 | kill switch、budget、dispatcher seam |
| `eventing-emit` | 发布其他模块可订阅的 `workflow.signal` event | — |
| `notify-test` | 通过 alert route 发送 synthetic test | notify actuator seam |
| `wait` | 在有界时间内暂停运行（1 秒–24 小时） | — |
| `approval-gate` | 在**图中途**开启人类审批，并暂停到作出决定 | approval gate |

`eventing-emit` 发布的是**固定** event type。step config 只提供一个 label，因此
工作流作者绝不能伪造 `edge.observed` 之类的 first-party event 并送入其他模块的
ingestion。

## 1. 声明工作流

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

编写需要 **write-tier**。图被拒绝时，返回的 `400` 会指出有问题的 step：

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

控制台把该 `step_ref` 锚定到画布上的 node。日后替换图只需一次原子的
`PUT .../steps`；图作为整体被审阅和审批，绝不逐 step 进行。

每次更改都会向 revision 台账追加完整 snapshot，任何较早的 revision 都可通过
live 动词所使用的同一验证流程恢复。

## 2. 审阅计划 — 无副作用

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

dry-run 按拓扑顺序返回 step，说明每一步将执行什么、经过哪些 gate；如果引用在保存
图后已经过期（例如某项调度上周已退役），还会给出 warning。它不写入任何内容、
不 dispatch 任何内容，也不开启审批，因此这是一次**读取**，任何可读取工作流的主体
都能执行。

它还会返回 `plan_hash`，即这张精确图的 fingerprint。下一步会用到它。

## 3. 运行 — 分两阶段，与人类所见内容绑定

运行需要 admin tier，**并且**经过 gate。第一阶段开启审批：

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

由人类通过 governance decision API 作出决定。第二阶段再把该引用传回，以消费
这项决定：

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

审批与 **plan hash 绑定**。如果在两个阶段之间编辑图，hash 就会改变，原审批不再
授权任何内容，运行会被拒绝。人类的“同意”只适用于其审阅过的图，绝不适用于事后
替换的图。随后运行使用该图的一个 **snapshot**，所以运行中途的编辑无法改变已经
执行中的内容。

全程保持 deny-by-default：如果没有接入 approval gate，运行会被拒绝，并将治理
缺口作为 finding 提出，而不是悄然放行。

## 4. 观察运行

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

每个 step 都报告自己的状态。上游失败时，下游 step 为 `skipped`；运行绝不越过
失败继续，也绝不报告并未取得的成功。`wait` 显示恢复时间；`approval-gate` 显示
正在等待的审批。启用 emergency stop 后，整个运行会带着可见的 `paused_reason`
**冻结**，在 stop 解除后恢复；stop 绝不会被悄然忽略，也绝不会仅凭自身就直接让
运行失败。

step 由后台处理循环推进，因此无需任何人保持 request 打开，等待和图中途审批也能
继续进展。

### 台账记录什么

每个执行作动的 step 都会追加一行不可变记录，并归于启动此次运行的人类。需要了解
两个特性：

- **被拒绝的**运行也会记录。拒绝本身就是证据。
- 如果 runner 已放弃等待后才收到作动结果，该结果会带真实 dispatch 引用被
  **reconcile** 到台账中。step 可能显示“结果未知”，但台账绝不声称发生了实际未
  发生的作动，也绝不隐藏实际发生的作动。

## 刻意排除的范围

- **自动 trigger。** 工作流在人类审批后运行。接入 cron 或 event 来启动运行会新增
  一条无人作动路径，因此应作为独立变更，置于现有调度 rail 之后。
- **带任意副作用的 step**（HTTP、exec）。它们会把组合 surface 变成通用执行
  engine，并破坏工作流只能重排既有受治理动词这一性质。

## 另请参阅

- [治理与审批](/zh/how-to/govern-and-approve/) — 运行及图中途 gate 所经过的审批
  engine。
- [事件参考](/zh/reference/events/) — `workflow.signal`，以及 subscriber 接收它所需
  的权限。

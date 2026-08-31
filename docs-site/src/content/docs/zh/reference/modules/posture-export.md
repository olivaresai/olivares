---
title: "向控制塔导出态势"
description: >-
  引擎真实态势（ground-truth posture）的一个只读、出站投影 —— 已发现的清点、最小
  权限漂移（least-privilege drift）与安全发现项 —— 由控制塔拉取以丰富其自身视图。
  这是一个中立 JSON 投影，而非已校验的原生推送。
---

态势导出（`modules/posture-export`）是引擎的**出站态势表面**：一个由控制塔轮询的单一
只读端点，用引擎的真实[访问图谱](/zh/reference/modules/iii-access-map/)、最小权限漂移、
已发现的清点与安全态势来丰富其自身清点。它代表平台"集成而非竞争"的一面 —— 它从不发出
身份（身份是入站的，由[治理](/zh/reference/modules/vi-governance/)拥有），只发出态势，
且不改变任何东西。

## 它暴露什么

一条路由，`GET /v1/m/posture/export`，由 `posture:export:read` 门控并固定到单一租户
作用域。响应是一份在**一笔受审计的事务**内组装的中立 JSON 文档，包含三个投影：

- **`inventory`** —— 活跃的已发现实体（kind、ref、status、signal sources、hosts、
  首次/最后可见时间、出现次数），可选地由 `?kind=` 过滤。
- **`posture_drift`** —— 已对账的最小权限漂移：观察到但未被许可的访问，外加未使用授权
  与清点授权的计数。
- **`findings`** —— 安全发现项，仅以 refs 和一个 `detail_hash` 投影，可由 `?severity=`
  下限与 `?category=` 过滤。

每一次导出都是**最小数据** —— 只有 refs、哈希和关系，绝不包含原始负载或机密 —— 并且一道
防御性的脱敏处理会清洗每一个自由格式字段。导出本身会将数据移出本机，因此它会在与读取相同的
事务中，以真实的 principal **自审计（self-audit）**到账本。

## 成熟度与界限上下文

**PARTIAL（部分）。** 导出动作已实时生效并受审计；*未*被校验的是另一端。所点名的两个塔
—— **Microsoft Agent 365** 与 **ServiceNow AI Control Tower** —— 的摄取格式没有引擎
可据以验证的一手来源 API，因此这是一个**诚实的、由塔拉取（或由运营者通过配置的 sink 路由）
的中立 JSON 投影，明确不是一个可工作的原生推送**。每个响应都在其中内联携带该来源说明。

逐请求的上限对清点、漂移和发现项加以限制；部分导出会报告其自身的截断标志，且从不被标注为
权威。

## 相关

- [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/) —— `siemforward` 平面，即
  *推送*对应物，它将密封的账本与发现项推送到一个 SIEM 塔。
- [模块 XIII —— 合规与监管](/zh/reference/modules/xiii-compliance/) —— 本态势与其共享真实
  数据来源的密封证据。
- [模块 III —— 访问与资源图谱](/zh/reference/modules/iii-access-map/) —— 本导出所投影的已
  对账漂移。
- [诚实与局限](/zh/start/honesty-and-limits/) —— 为何这是一个投影，而非已校验的推送。
- [模块目录](/zh/reference/modules/overview/) —— 态势导出在 30 个已交付模块中所处的位置。

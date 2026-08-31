---
title: "Reporting — 专业 HTML/PDF 报告"
description: >-
  根据平台的合规、审计与 FinOps 数据生成可下载的 HTML 和 PDF 报告。开放核心按需提供
  5 种内置报告；计划报告属于 enterprise add-on。
---

Reporting（`modules/reporting`）为 **LIVE**。它把平台的合规、审计与 FinOps 数据整理为
一份专业文档，让审计人员直接下载证据，无需从多个 API 复制粘贴 JSON。

## 内置报告

open-core 模块按需提供 5 种报告：

- `compliance-evidence` —— 按框架展示合规态势、控制状态与证据。
- `audit-summary` —— 审计事件汇总与 ledger 完整性验证。
- `finops-report` —— 按模型和提供方拆分的 AI 支出。
- `access-review` —— 用于定期审查的用户与访问数据。
- `executive-summary` —— 治理态势、风险、成本与采用情况的简洁总览。

`GET /v1/m/reporting/reports` 列出类型与格式。通过
`GET /v1/m/reporting/reports/{type}` 生成报告；默认返回 HTML，添加
`?format=pdf` 则下载 PDF。路由要求 `reporting:report:read` 权限。

## Open core 与 enterprise

按需 HTML 包含在 open-core 二进制文件中。存在兼容 Chromium 的可执行文件时，也可按需
生成 PDF。**Enterprise add-on：**计划报告生成由 build tag 门控，不属于 community runtime。

## 明确的边界

- PDF 生成会以 headless 模式启动 Chromium。若 `PATH` 中没有 `chromium`、
  `chromium-browser` 或 `google-chrome`/`chrome`，PDF 请求返回 `501`；HTML 仍可使用。
- compliance-evidence 需要合规数据源。数据源未接入时，文档会明确显示
  “Data source not configured”，而不会编造证据。
- 本模块根据平台已经持有的数据渲染文档，不取代 audit ledger、合规评估或 FinOps
  权威数据源。

## 相关内容

- [合规与监管](/zh/reference/modules/xiii-compliance/) —— 合规态势与证据来源。
- [成本与 AI FinOps](/zh/reference/modules/xi-finops/) —— 权威支出表面。
- [模块目录](/zh/reference/modules/overview/) —— 全部 30 个已接入模块及其诚实成熟度。

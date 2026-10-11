---
title: "Reporting — 专业 HTML/PDF 报告"
description: >-
  根据平台的合规、审计与 FinOps 数据生成可下载的 HTML 和 PDF 报告。开放核心按需提供
  5 种内置报告；计划报告属于 Business。
---

Reporting（`modules/reporting`）在启用模块时生成报告。它把平台的合规、审计与 FinOps 数据整理成单一文档，让审计人员下载证据，而不用从多个 API 复制粘贴 JSON。

**版本：** Business Compliance Packs 提供框架目录、评估、监管日历、DORA/HIPAA 视图、证据封存、OSCAL 导出以及按需 HTML/PDF 报告。Community 对这些功能返回 `501`，并保留风险、数据驻留、记录管理和已保存证据的 JSON/CSV 导出。升级会保留现有记录。

## 内置报告

Business Compliance Packs 按需提供 5 种报告：

- `compliance-evidence` —— 按框架展示合规态势、控制状态与证据。
- `audit-summary` —— 审计事件汇总与 ledger 完整性验证。
- `finops-report` —— 按模型和提供方拆分的 AI 支出。
- `access-review` —— 用于定期审查的用户与访问数据。
- `executive-summary` —— 治理态势、风险、成本与采用情况的简洁总览。

`GET /v1/m/reporting/reports` 列出类型与格式。通过
`GET /v1/m/reporting/reports/{type}` 生成报告；默认返回 HTML，添加
`?format=pdf` 则下载 PDF。路由要求 `reporting:report:read` 权限。

## 启用并下载报告

CLI 登录引擎后，管理员可以启用 reporting：

```sh
olivares modules on reporting
```

激活改变运行模块时，引擎会重启。请求报告前，请等待控制台重新连接，或已安装服务的就绪检查通过。Compose 健康检查使用 `olivares readyz`。

```sh
olivares reporting reports ls -o json
olivares reporting reports get finops-report --format html --out spend.html
```

启用 reporting 也会激活必需的 compliance 模块及其依赖。目录列出运行引擎可用的格式；请求 PDF 前请查看目录。生成过程读取所选租户的存储数据。空安装没有可报告的支出。

要指定时期，请同时传入 `--from` 和 `--to`，使用 ISO 日期或 RFC 3339 时间戳。脚本中，`--out -` 将文档写入 stdout，将下载回执写入 stderr。文档是 HTML 或 PDF，而不是 JSON。

`olivares modules off reporting` 将它移出所选模块。如果没有已启用模块或版本附加组件依赖 reporting，路由被禁用而存储数据不删除。禁用时，reporting 请求返回 `404` 和 `module_not_enabled`；重新启用恢复路由。模块选择和报告输入数据在引擎重启后保留。按需生成的文档是下载文件；如需保留特定报告，请保存该文件。

## 明确的边界

- PDF 生成会以 headless 模式启动 Chromium。若 `PATH` 中没有 `chromium`、
  `chromium-browser` 或 `google-chrome`/`chrome`，PDF 请求返回 `501`；HTML 仍可使用。
- compliance-evidence 需要合规数据源。数据源未接入时，文档会明确显示
  “Data source not configured”，而不会编造证据。
- 本模块根据平台已经持有的数据渲染文档，不取代 audit ledger、合规评估或 FinOps
  权威数据源。
- HTML 生成和目录访问要求调用者已认证，且在所选租户拥有 `reporting:report:read` 权限。认证拒绝返回 `401`；对该租户无权限的调用者收到 `403`。
- 目录可用不代表每种报告的数据覆盖度均已认证。当前 access-review 报告填充身份名称；其数据适配器不填充电子邮箱、角色、权限和最后访问字段。
- FinOps 报告分组用存储的 ID 标识模型和提供商，而不是显示名称。

## 相关内容

- [合规与监管](/zh/reference/modules/xiii-compliance/) —— 合规态势与证据来源。
- [成本与 AI FinOps](/zh/reference/modules/xi-finops/) —— 权威支出表面。
- [模块目录](/zh/reference/modules/overview/) —— 模块可用性与成熟度。

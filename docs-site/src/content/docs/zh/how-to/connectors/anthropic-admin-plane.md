---
title: "Anthropic admin 平面（用量、成本、合规）"
description: >-
  治理 Claude 组织本身：通过 Admin API 获取权威的已计费成本与用量、把 API 侧的 MCP 与
  server-tool 许可集作为已许可 edge、合规活动馈送与组织目录——每个凭据各自限定范围，
  每个盲点都被点名。
sidebar:
  order: 6
---

Claude Code 遥测告诉你在开发者机器上运行了什么。**Anthropic admin 平面**告诉你*组织*在做什么：
已计费成本、每工作区用量、组织成员与密钥、合规活动馈送。四个只读源覆盖它；本页配置其中两个核心源，
并概述它们在名册侧的同伴。

| 源（`kind`） | 它读取什么 | 凭据 |
|---|---|---|
| `claude-api` | 用量与已计费成本、模型/工作区清单、Claude Code 分析、API 侧 MCP/server-tool 治理 | Admin API 密钥（`admin_key`） |
| `claude-compliance` | 合规活动馈送（证据级事件）+ 组织目录 | 活动馈送密钥 + 一个**独立的** Compliance Access Key |
| `claude-console` | 组织 IAM 名册（成员、角色）→ SSO/SCIM 姿态发现 | console 凭据 |
| `claude-wif` | 非人类身份（服务账户 `svac_…`、联邦身份）+ 它们的**已许可**范围 edge | WIF 端点凭据 |

全部都是**只读且 deny-closed 的**：一个空凭据意味着该馈送已关闭，且产品会如实说明——绝不会编造一份
空清单。

## `claude-api`：成本、用量与 API 侧治理

```json
{
  "sources": [{
    "name": "anthropic-org",
    "kind": "claude-api",
    "tenant": "<tenant-id>",
    "config": {
      "admin_key": "<admin-api-key-reference>",
      "cost_report": "true",
      "claude_code": "true"
    }
  }]
}
```

重要的键（来自所发布的描述符；默认值在括号中）：

- **`admin_key`**（secret）——Anthropic Admin API 密钥。空 = 仅离线目录。
- **`cost_report`**（`true`）——拉取**已计费**成本报告（每日、权威），与派生的用量估算并列。产品
  把二者分开：估算会与已计费数字对账，每个会话只取一个成本来源，绝不取两个。
- **`lookback`**（`24h`）/ **`cost_lookback`**（`48h`）/
  **`bucket_width`**（`1d`；也可 `1h`、`1m`）/ **`max_pages`**——拉取窗口与分页边界。
- **`claude_code`**（`false`）——也拉取 Claude Code Analytics 馈送（按模型的每开发者估算成本）以做
  费用分摊。
- **`claude_code_shadow_auth`**（`true`）——在分析馈送开启时，标记每一位其 Claude Code 用量以
  `customer_type=api` 计费的开发者——即一个**在组织订阅之外**的个人/API 密钥，亦即身份和支出搭乘
  在一个未受治理的密钥上。仅当你的组织有意让 Claude Code 以 API 计费运行时才设为 `false`。
- **`gateway`**（`direct`）——该组织所运行的部署表面（`direct | claude-platform-aws |
  bedrock-mantle | bedrock-legacy | vertex | foundry`）。在一个没有 Admin API 的表面上
  （Bedrock/Vertex/Foundry），治理摄取会**带着一项姿态发现如实降级**，而不是假装一份空清单。
- **`mcp_toolsets`** / **`server_tool_grants`**——为 API 驱动的 Claude 代理运维方声明的许可集
  （某个代理*可以*使用哪些 MCP 工具、哪些 Anthropic server-tool 类型）。每个被许可的条目都成为模块
  III 中的一条**已许可 edge**，与已观测访问交叉比对——和别处一样的“已许可对比已观测”差异。`agent_ref`
  必须是该代理在运行时被发现的外部 id，否则该 grant 就是一个如实的无操作，而非一个错误匹配。

:::caution[分析馈送有一个被点名的边界]
Claude Code Analytics 馈送只追踪 **Claude API** 上的用量。在 Claude Platform on AWS、Bedrock、
Gemini Enterprise Agent Platform (formerly Vertex AI) 或 Microsoft Foundry 上的机群**不在其中**——那里没有发现并不是”不存在”的证据。对于那些
表面，[OTel 平面](/zh/how-to/claude-code-enterprise-otel/) 才是你拥有的观测。
:::

## `claude-compliance`：证据馈送与目录

```json
{
  "sources": [{
    "name": "anthropic-compliance",
    "kind": "claude-compliance",
    "tenant": "<tenant-id>",
    "config": {
      "api_key": "<activity-feed-key-reference>",
      "compliance_access_key": "<compliance-access-key-reference>"
    }
  }]
}
```

两个**独立的**凭据，有意为之：

- **`api_key`**——一个带 `read:compliance_activities` 的 Admin API 密钥；拉取活动馈送（证据级事件）。
- **`compliance_access_key`**——一个带 `read:compliance_org_data` / `read:compliance_user_data` 的
  单独密钥；启用组织**目录**摄取（组织、用户、角色、组——包括 Admin API 看不到的 SCIM 预配信号）。
  空 = 目录关闭，deny-closed。

删除范围（`delete:compliance_user_data`，由被遗忘权路径使用）是单独预配并经双重控制门控的——这个
只读连接器从不持有它。

## 你将在 console 中看到什么

已计费与估算支出，按遥测所携带的维度切分（team 和 project 标签成为一等的），在 **Cost & FinOps** 中；
组织成员、非人类身份及其范围在 **Identity & NHI** 中；姿态发现（影子认证、表面降级、WIF 隐患）在
**Security** 中：

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Cost & FinOps 视图：按模型与维度的支出，带预算与告警。" />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Cost & FinOps 视图：按模型与维度的支出，带预算与告警。" />

## 诚实局限

- **成本权威是已计费报告。** 用量派生的数字是估算，会被对账，绝不重复计数。
- **admin 平面看到的是 Anthropic 运营的表面。** 第三方托管的 Claude（Bedrock/Vertex/Foundry）对它
  不可见——通过 `gateway` 显式点名，由 OTel 平面覆盖。
- **`claude-console` 姿态发现包含一个盲点：** console 无法观测 SSO/SCIM 是否在上游被强制执行——该
  发现会如实说明，而不是臆测。

## 相关

- [Claude Code 企业级 OTel](/zh/how-to/claude-code-enterprise-otel/)——这些组织级馈送所补充的
  每会话平面。
- [预算与 FinOps guardrails](/zh/how-to/cookbook/budgets-and-finops-guardrails/)——把成本流转化为
  被强制执行的限额。
- [连接器与覆盖层级](/zh/reference/connectors/)——完整目录。

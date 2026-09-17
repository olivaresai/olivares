---
title: "会话驾驶舱（可用性）"
description: >-
  session-cockpit API 命名空间的可用性描述符。Community 注册该命名空间时不挂载
  处理程序，也没有交互式驾驶舱。如何对照实时会话和 AgentOps 确认这一缺失，
  以及应使用哪些开放能力。
---

Community 二进制为 `session-cockpit` API 命名空间注册一个可用性描述符。该命名空间
当前 **没有处理程序**，也 **没有交互式驾驶舱**。`/v1/m/session-cockpit` 下的请求
会因 **缺失而得到 404**。该描述符不是目录中 30 个产品模块之一。

## 当前可用性

| 表面 | Community（本制品） |
|---|---|
| API 命名空间 | `session-cockpit`（`/v1/m/session-cockpit`） |
| 描述符 | `olivares.session-cockpit` `0.1.0` — 标题 `Session cockpit (availability)` |
| 已注册路由 / 处理程序 | 无 |
| 交互式驾驶舱 | 无 |
| 已声明权限 | `session-cockpit:availability:read`（已声明，未绑定路由） |
| 生命周期 | 空（`Init` / `Start` / `Stop` 不执行任何操作） |

该命名空间上的 404 是 Community 的预期响应。它并不表示控制平面安装失败。

## 如何诊断缺失

确认已交付的会话表面仍然可用：

1. `sessions` 命名空间下的实时会话模块路由 —
   [实时运行与会话](/zh/reference/modules/ii-sessions/)。
2. 控制台 **Sessions**（`/sessions`）、**Claude Code**（`/agentops`）和
   **Work**（`/work`）— [控制台参考](/zh/reference/console/)。
3. 下一节中的官方 CLI 生命周期。

若这些表面有响应，而 `/v1/m/session-cockpit` 为 404，则描述符与本制品一致。

## 开放能力

官方 CLI 的安装、启动、观察与管理仍属于开放的 Community 产品：

- [实时运行与会话](/zh/reference/modules/ii-sessions/) — 实时智能体会话、时间线、
  提供商配置文件和 `live_ref`。
- [运行提供商会话](/zh/how-to/operate-provider-sessions/) — 在固定的官方二进制
  下启动 Claude、Codex 或 Grok。
- [在 Olivares 中运行 Claude Code](/zh/how-to/run-claude-code-with-olivares/) —
  官方 `claude` 会话的 AgentOps 共存部署。
- Connector 与 PEP-hook 指南（观察/治理，不是会话启动）：
  [Claude Code](/zh/how-to/integrations/claude-code/)、
  [Codex](/zh/how-to/integrations/codex/)、
  [Grok Build](/zh/how-to/integrations/grok/)。
- [特权会话记录](/zh/reference/modules/recording/)
- [身份、权限与治理](/zh/reference/modules/vi-governance/)

## 相关

- [模块目录](/zh/reference/modules/overview/)
- [诚实与局限](/zh/start/honesty-and-limits/)

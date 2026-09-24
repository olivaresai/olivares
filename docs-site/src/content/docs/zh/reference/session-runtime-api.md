---
title: 会话运行时 API（官方 CLI）
description: >-
  Community HTTP 面：列出、连接、输入并停止自有的 Claude Code、Codex 与 Grok CLI 进程。
  权限、PTY、恢复与重连。
---

控制平面 **启动供应商 CLI**。它不替换 Claude Code、Codex 或 Grok Build。会话与终端
是本产品的一个模块，不是产品本身。

本页记录 `/v1/m/sessions/runs` 下的 Community operate 路由。它们已在
[beta OpenAPI 文档](/reference/api-beta/) 中。尚未发布的 v26.10 将增加驱动契约、本地 PTY
运行器，以及作为测试的旅程 J01–J08。

## 版本边界

| 版本 | 做什么 | 不做什么 |
|---|---|---|
| **Community（本页）** | 自有本地子进程：启动、stdin/stdout/stderr、带游标的 attach、恢复确切对话、重连活流、以观察到的退出状态停止。会话行与证据留在模块 II。 | Identity & Scale 多窗格引擎、mTLS 监听器、商业输入会话、xterm UI 块 |
| **Identity & Scale 叠加** | 商业 session-cockpit 引擎（监听器、窗格、账本）。有附加组件时路由在 `/v1/m/session-cockpit/` 下。 | 不替换 `/v1/m/sessions/runs` |

Community 构建以 **缺席**（404）回答叠加命名空间。它不挂载 501 存根。

Claude Code hook 路径仍是 `olivares claude-hook`（PreToolUse PEP）。那是观察与
执行，不是替代 CLI。

## 权限

模块路由上的既有执行点：

| 权限 | 路由 |
|---|---|
| `sessions:run:read` | `GET /runs`、`GET /runs/{ref}`、`GET /runs/{ref}/events`、`GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/stop`、`POST /runs/{ref}/resume` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`、`DELETE /runs/{ref}` |

viewer 可以列出。创建、输入和停止需要 write。路由上的授权器是执行点；缺少权限为 403。

## 控制台读取的路由

基路径：`/v1/m/sessions`。认证。发送 `X-Olivares-Tenant`。

| 方法 | 路径 | 结果 |
|---|---|---|
| `GET` | `/runs` | 托管运行分页 |
| `GET` | `/runs/{ref}` | 一次运行。`state` 为派生。`exit_code` 为观察值。没有捏造的成功字段 |
| `GET` | `/runs/{ref}/events` | 生命周期证据行 |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE：`output` 帧；环在游标以下驱逐时为 `lag`；`end` 或非活动 `notice` |
| `POST` | `/runs/{ref}/input` | stdin。stream-json 用 `line`/`message`。Codex/Grok 用 `text`。202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | 对进程组 SIGTERM 再 SIGKILL。行上记录观察到的退出状态 |
| `POST` | `/runs/{ref}/resume` | 新的进程世代。确切已存储对话 |
| `POST` | `/runs/{ref}/interrupt` | 取消活动回合。进程保留 |

断开 attach 后的重连是对 **同一** 活动进程的 `GET …/attach?from={last+1}`。
进程丢失后，attach 说明会话不活动。Resume 开始新世代。Reconnect 不捏造替代进程
（SDD R04）。

## 传输（Community）

每个官方 CLI 都在其 operate 形式所需的传输上启动，由
`cliruntime.LaunchTransport` 声明是哪一种。今天三种形式都是 stdio 协议，所以
组合根接线 `sessions.NewProcRunner()`：stdin、stdout 与 stderr 都是管道，并保持
为不同的流。

**Claude Code 拒绝 stdin 上的终端。** 它的 `--print` stream-json 形式回答
`Error: Input must be provided either through stdin or as a prompt argument
when using --print`，不输出任何协议帧便以 1 退出。需要终端的是交互形式；
`sessions.NewPTYRunner()` 在 Linux 上为此保留。container 与 sandbox 隔离在两者
上都仍被拒绝。

驱动契约在 `modules/sessions/cliruntime`。种类：`claude`、`codex`、`grok`。
符合性始终针对进程内假实现和本地 PTY 对端运行。当 PATH 上有 `claude` /
`codex` / `grok` 时，单独测试拥有真实二进制、停止它并记录退出。它不发送模型回合。

## 相关

- [操作提供方会话](/how-to/operate-provider-sessions/)
- [模块 II — 实时运行](/reference/modules/ii-sessions/)
- [连接 Claude Code](/how-to/connect-claude-code/)
- [Beta OpenAPI](/reference/api-beta/)

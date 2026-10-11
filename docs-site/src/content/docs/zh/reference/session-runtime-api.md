---
title: 会话运行时 API（官方 CLI）
description: >-
  Community HTTP 面：列出、连接、输入并停止自有的 Claude Code、Codex 与 Grok CLI 进程。
  权限、PTY、恢复与重连。
---

控制平面 **启动供应商 CLI**。它不替换 Claude Code、Codex 或 Grok Build。会话与终端
是本产品的一个模块，不是产品本身。

本页记录 `/v1/m/sessions/runs` 下的 Community operate 路由。它们已在
[beta OpenAPI 文档](/reference/api-beta/) 中。26.10 增加了驱动契约、本地 PTY
运行器，以及作为测试的旅程 J01–J08。

## 版本边界

| 版本 | 做什么 | 不做什么 |
|---|---|---|
| **Community（本页）** | 自有本地子进程：启动、stdin/stdout/stderr、带游标的 attach、恢复确切对话、重连活流、以观察到的退出状态停止。会话行与证据留在模块 II。 | Identity & Scale 多窗格引擎、mTLS 监听器、cockpit 窗格输入、xterm UI 块 |
| **Identity & Scale 叠加** | 商业 session-cockpit 引擎（监听器、窗格、账本）。有模块时路由在 `/v1/m/session-cockpit/` 下。 | 不替换 `/v1/m/sessions/runs` |

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
| `GET` | `/runs/{ref}/diff` | 工作树会话的分支相对起点的差异：`branch`、`base`、`head` 和变更的 `files`（`path`、`status`）。无工作树时为 404 |
| `GET` | `/runs/{ref}/diff/file?path=` | 某路径在 `base` 与 `head` 的文本（`original`、`modified`，各自上限为工作区 `max_read_bytes` 与 64 KiB 中的较小值） |

断开 attach 后的重连是对 **同一** 活动进程的 `GET …/attach?from={last+1}`。
进程丢失后，attach 说明会话不活动。Resume 开始新世代。Reconnect 不捏造替代进程
（SDD R04）。

### 从提交或分支启动工作树

`POST /runs` 的 `worktree` 选项创建专用工作树和分支。与 `worktree` 一起，可传入可选的 `worktree_from`：完整提交 ID（40 或 64 位小写十六进制），或工作区仓库的本地分支。新工作树和其自己的新分支从那里开始，而不是从工作区当前提交开始；指定分支和工作区检出不会移动。这是接收方打开交接工作的方法，交接内容可选地携带 `branch` 和 `sha`。Git 将值解析为完整提交 ID，仅使用该 ID。仓库未持有的提交、不存在的分支、修订表达式、范围、选项，以及不带 `worktree` 的 `worktree_from`，都会在创建任何内容之前返回 422。省略该值时，启动仍从工作区当前提交开始。

`GET /runs/{ref}/diff` 列出会话工作树分支自离开工作区当前提交后变更的路径（`base` 为合并基点，`head` 为分支顶端，均是完整提交 ID；最多 200 个路径，超过时由 `truncated` 指示）。`GET /runs/{ref}/diff/file?path=` 返回路径在 `base` 和 `head` 处的文本，文件在那里不存在时为空。它通过 git plumbing 命令读取已提交对象，因此不包含未提交编辑，并需要与读取运行相同的权限。它遵守工作区自身的文件规则：允许子路径之外的路径不列出且返回 404；工作区 DLP 策略拒绝时返回 403（读取与工作区文件读取一样被审计）；超过 16 MiB 的文件返回 413。没有工作树的会话和其他租户的运行返回 404；分支已消失或与工作区没有共同历史时返回 409。仓库中未被任何分支、标签或远程分支持有的提交 ID 也会使 `worktree_from` 返回 422。

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

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
| `sessions:run:read` | `GET /runs`、`GET /runs/{ref}`、`GET /runs/{ref}/events`、`GET /runs/{ref}/attach`、`GET /runs/{ref}/handoff`、`GET /runs/{ref}/activity`、`GET /runs/{ref}/activity/agents/{agent}`、`GET /runs/{ref}/messages` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/stop`、`POST /runs/{ref}/resume`、`POST /runs/{ref}/move`、`POST /runs/{ref}/handoff` |
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
| `POST` | `/runs/{ref}/move` | Body `{provider_profile_ref}`。将已停止或失败的 Claude Code 会话重新绑定到同一工具的另一个配置文件，并携带其对话。不启动任何内容。200 运行 |
| `GET` | `/runs/{ref}/handoff` | 交接将从其开始的摘要：`summary`、`source_driver`、`turns`、`transcript_bytes`。只读，记录在审计账本中。运行中的会话按其当前状态读取；尚未启动的会话回答 409 |
| `POST` | `/runs/{ref}/handoff` | Body `{provider_profile_ref, summary?, name?}`。在该配置文件中启动一个与本会话关联的新会话。201 运行加 `summary_state`（当其不是 `delivered` 时还有 `first_turn`）；启动在等待批准时为 202 |
| `GET` | `/runs/{ref}/activity` | Claude Code 或 Codex 会话的工具正在做什么，每次请求时从其自身的原生记录读取：`agents`、`tasks`（命令与后台工作）、`files`（路径、`added`、`removed`、`created`）、`todos`、`skills`、`mcp_servers`、`model`、`context_tokens`、工具报告时才有值（否则为 null）的 `context_window` 和 `context_percent`（Claude Code 为百分比，未知时为 null）、`compactions`，以及 `transcript`（`read`；工具写入之前为 `pending`；本应存在却缺失时为 `missing`）。代理和任务的状态为 `running`、`completed`、`failed`、`stopped` 或 `scheduled`。Codex 使用其 rollout 文件，不会启动另一个 app-server；上下文 token 描述的是最近一次请求，而不是累计用量。只读；对每个读取者和会话每 15 分钟在审计账本中记录一次。不受支持的工具回答 422；被赋予保险库机密或仓库凭证的会话回答 409；使用过包注册表令牌的会话在其已停止时、或在紧跟一次使用过令牌的启动之后的 resume 之后运行时，同样回答 409 |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | 一个 Claude Code 子代理的对话记录，以纯文本轮次呈现（`role`、`text`）。只读，记录在审计账本中。未知代理为 404。Codex 在根 Activity 回答中报告子级状态；不提供子级对话记录读取 |
| `GET` | `/runs/{ref}/messages` | Claude Code 或 Codex 会话的消息，每次请求时从工具自身的对话记录读取，分页返回：`items`（`id`、`role`（`user` 或 `assistant`）、`at`、`first_line`）、`total`、`next`、`transcript`（`read`、`pending`、`missing`）。默认只含人发送的内容；`role=all` 会加入工具的回答。`q` 保留任意位置包含该短语的消息（不区分大小写，最多 256 字节）；`at`（RFC 3339）保留该时间或之后的消息，因此第一项就是锚点；`order` 为 `asc`（默认）或 `desc`，`limit` 最大 200，`cursor` 是一页接续所用的 id（`next`）；items 始终按最旧在前排列。`id` 读取一条完整消息（`text`，以及 `later_same`：之后有多少条同一发送者的消息重复其第一行；此时不应用任何过滤器，`total` 是会话的消息总数）。消息的 `id` 是它在会话消息中的位置，不会随对话记录增长而改变。只读；对每个读取者、会话和 15 分钟窗口在审计账本中记录一次。不受支持的工具回答 422；被赋予保险库机密或仓库凭证的会话回答 409；未知 `id` 回答 404；无效参数回答 400 |
| `GET` | `/runs/{ref}/diff` | 工作树会话的分支相对起点的差异：`branch`、`base`、`head` 和变更的 `files`（`path`、`status`）。无工作树时为 404 |
| `GET` | `/runs/{ref}/diff/file?path=` | 某路径在 `base` 与 `head` 的文本（`original`、`modified`，各自上限为工作区 `max_read_bytes` 与 64 KiB 中的较小值） |

对于 plan 参数未保留在 rollout 中的 Codex Code Mode 调用，Activity 还会读取实时桥接已收到的最新 `turn/plan/updated` 通知。浏览器刷新使用同一个服务端会话；刷新不会启动 app-server。实时句柄消失之后，只能读取 Codex 自己保留的数据。

对于 Claude Code，窗口来自其原生 result 帧的 `modelUsage[model].contextWindow`，在停止和重连后仍保留。当报告匹配且无歧义时，`context_percent` 为 `100 × context_tokens / context_window`；否则两个字段都为 `null`，CLI 和控制台显示 token 数并附上窗口未知的提示。新的工具启动会清除之前的窗口报告。

工作流任务可包含 `workflow`：`run_id`、`name`、`duration_ms`、`agents_total`、`agents_done`、`agents_failed` 和 `phases`。每个阶段有 `index`、`title`、可选的 `detail` 和 `state`，以及相同的代理计数。计数描述的是到目前为止观察到的代理。在实时期间，阶段状态描述观察到的运行中或失败的代理。代理完成并不能证明脚本已离开该阶段，因此不会把阶段标记为已完成，也不会指明当前的脚本步骤。已完成的阶段状态由最终工作流摘要提供。

实时工作流快照来自 Claude Code 的 `system/task_progress` 输出，与 attach 流共用。工具已完成的 `workflows/<runId>.json` 在对话记录的主目录、链接和大小规则下填充最终摘要。摘要读取受限：每个文件 8 MiB，每个请求 64 MiB；更大的原生日志或结果会使摘要不可读，同时保留可用的实时进度。不可读的摘要以及受限实时缓冲区中的缺口都会计入 `unreadable`。

断开 attach 后的重连是对 **同一** 活动进程的 `GET …/attach?from={last+1}`。
进程丢失后，attach 说明会话不活动。Resume 开始新世代。Reconnect 不捏造替代进程
（SDD R04）。

### 移动会话与交接

会话可以通过两种方式在另一个账户中继续，控制台以及 `olivares session move` /
`olivares session handoff` 两种都提供。`move` 会把 Claude Code 会话的已存储对话带到同一工具的另一个配置文件，
因此只有 Claude Code 会话可以被 `move`。`handoff` 则根据 Claude Code、Codex、Gemini CLI 或 OpenCode 会话的对话启动一个新会话。

当工具以达到限额结束上一回合时，运行带有 `limit`：
`{kind, resets_at, account_ref}`。`kind` 为 `usage`（账户的套餐额度已用完）
或 `rate`（提供商正在限制请求速率）。`resets_at` 为 RFC 3339，仅当工具报告
限额何时解除时才有，否则不存在；`account_ref` 是该回合所用的配置文件。
Claude Code 在其自身结果中给出该事实（`api_error: usage_limit_reached` 或
HTTP 429），并在其 `rate_limit_event` 中给出重置时间；Codex 在类型为
`usageLimitExceeded` 或 `rateLimitExceeded` 的失败回合中给出该事实，并在其
`account/rateLimits/updated` 窗口中给出重置时间；当两个窗口都已用完时，记录较晚的
重置时间，距现在超过一年的重置时间则略去。该事实首次被记录时，账本新增一个
`limit_reached` 事件；再次遇到相同事实不会新增。
下一个成功的回合会清除 `limit`，移动到另一个账户同样会清除。其他任何类型的
结果都不会设置它。

`POST /runs/{ref}/move` 保持同一个会话。它要求会话为 `stopped` 或 `failed`，
且其对话位于其账户主目录中，并要求目标配置文件属于同一工具，且在其下的启动
会被接受（active、本节点的环境、获授权的来源）。引擎只复制那一个对话——对话记录
及其子代理和工具结果文件——到目标主目录，绝不复制登录、设置或其他对话，并在
一个事务中重新绑定运行行、实时行和账本。运行保留其 `run_ref` 和提供商对话 id；
账本新增一个 `moved` 事件，其详情按引用命名两个配置文件并统计携带的文件数，
失败的会话保留其 `reason`。源主目录只被读取。拒绝时不写入任何内容，并移除
移动所产生的内容：

- 409：运行中的会话；被赋予保险库机密、仓库凭证或包注册表令牌的会话（它打印
  的内容无法证明不含这些）；其主目录不属于其配置文件的行；在目标下对话 id
  已绑定到另一个会话；目标持有该对话的不同版本；目标属于另一工具；或文件在
  携带过程中发生了变化（重试）；
- 422：对话中的链接、文件的第二个名称或特殊文件，以及另一工具的会话。

此前一次移动在目标上留下的同一对话的副本会被较新的副本替换。不会启动任何内容：
下一次 `POST /runs/{ref}/resume` 就是普通的 resume，因此目标配置文件的策略、
预算和批准适用于它。尚无对话的会话移动时没有可携带的内容，并在目标上重新开始。

`GET /runs/{ref}/handoff` 和 `POST /runs/{ref}/handoff` 用于在不存在原生 resume 的
另一个工具中继续。两者都会读取 Claude Code、Codex、Gemini CLI 或 OpenCode 会话的已存储对话（Grok 会话回答 422）。
会话可以是 `stopped`、`failed`、`running` 或 `idle`；尚未启动的会话回答 409。预览按运行中会话的当前状态读取。
POST 会先像 `POST /runs/{ref}/stop` 那样停止运行中的会话，并在其工具退出后读取其对话；不需要停止的拒绝
（目标配置文件、保险库机密、仓库凭证）都发生在停止之前，停止之后的拒绝会说明该会话已被停止且可以恢复。
Codex 从其 rollout 读取，Gemini CLI 从其聊天日志读取，OpenCode 则通过其自带的 `opencode export` 读取，
该命令在会话的配置文件下只运行一次且离线。
预览由已存储的对话生成，不使用模型，因此人可以审阅和编辑，对它的每次读取都作为
`sessions.run.handoff_preview` 记录在审计账本中。POST 在所选配置文件中启动一个新会话，
位于同一工作区并使用同一模板，其第一轮是经过审阅的 `summary`（缺省时自动生成）
以及对话记录的路径，该记录写在目标配置文件的配置主目录下，使用其专属名称。
对话记录完整携带每条消息，每条最多 256 KiB，工具调用和结果被缩短，并说明有多少
记录无法读取。新运行报告 `handoff_of`，原会话的账本新增一个 `handed_off`
事件。回答带有 `summary_state`：

| `summary_state` | 含义 |
|---|---|
| `delivered` | 第一轮已到达新会话。 |
| `not_delivered` | 未送达。`first_turn` 持有要发送的文本。 |
| `unknown` | 已尝试，但结果未得到确认；再次发送 `first_turn` 之前请先核实。 |
| `waits_for_approval` | 启动被扣留，等待人工批准（202）；不会自动发送第一轮。 |

`first_turn` 仅在状态不是 `delivered` 时出现，因为它指明了附件的路径。当源对话的
会话被赋予保险库机密、仓库凭证或包注册表令牌时，该对话会被以 409 拒绝，因为交接会把它
发送给另一个提供商；超过 256 MiB 或包含一条大于 32 MiB 的记录时，以 422 拒绝。

### 批准文件夹的设置命令

设置命令属于已登记的文件夹，保存在引擎存储中。仓库文件和环境变量无法更改它。
目前，这些操作和会话启动期间都不会执行该命令。

- `PUT /workspaces/{ref}/setup-command` 接受 `{"command":"npm ci"}`。确切的
  UTF-8 文本会被原样保留，包括空白；最多 8192 字节，不得含 NUL。
  空字符串会清除该命令。需要工作区管理权限。
- `GET /workspaces/{ref}` 返回 `setup_command`、`setup_command_sha256`，并在
  批准之后返回 `setup_approval`，含 `sha256`、`actor` 和 `at`。
  只有当该批准覆盖当前文本时，`setup_approved` 才为 true。
  编辑之后，`setup_approval` 仍作为证据保留，所以应检测 `setup_approved`，
  绝不要检测 `setup_approval` 是否存在。
- `POST /workspaces/{ref}/setup-command/approve` 接受 `{"sha256":"<reviewed hash>"}`。
  它需要工作区管理权限，并拒绝代理身份。该哈希必须与当前存储的文本匹配；
  并发编辑返回 409。批准与其 `sessions.workspace.approve-setup` 审计条目在同一事务中提交。

当命令没有匹配的批准时，工作树启动返回 409，且发生在创建工作树之前。其提示语句会指明
该命令和批准步骤。哪怕只更改一个字符（包括空白），都需要重新批准。最后一次批准
作为证据保留；其哈希必须等于 `setup_command_sha256` 才会生效，所以把命令设回此前
已批准过的文本，会再次应用那次批准。任何能读取该文件夹的人都能读取该命令，
所以不要把机密放进去。
带有 `"no_setup":true` 的 `POST /runs` 只对那一次启动跳过设置。对带工作树的会话进行
交接，会启动新的工作树，需要同样的批准；它没有跳过选项。其他启动和 resume
保持其现有行为。

使用 CLI 时，用 `olivares agent workspace setup <ref> --command 'npm ci'` 设置，
用 `olivares agent workspace setup <ref>` 读取，并用
`olivares agent workspace approve-setup <ref> --sha256 <hash>` 批准所显示的哈希。
`olivares session start --worktree --no-setup` 跳过一次设置。

### 从提交或分支启动工作树

`POST /runs` 的 `worktree` 选项创建专用工作树和分支。与 `worktree` 一起，可传入可选的 `worktree_from`：完整提交 ID（40 或 64 位小写十六进制），或工作区仓库的本地分支。新工作树和其自己的新分支从那里开始，而不是从工作区当前提交开始；指定分支和工作区检出不会移动。这是接收方打开交接工作的方法，交接内容可选地携带 `branch` 和 `sha`。Git 将值解析为完整提交 ID，仅使用该 ID。仓库未持有的提交、不存在的分支、修订表达式、范围、选项，以及不带 `worktree` 的 `worktree_from`，都会在创建任何内容之前返回 422。省略该值时，启动仍从工作区当前提交开始。

`GET /runs/{ref}/diff` 列出会话工作树分支自离开工作区当前提交后变更的路径（`base` 为合并基点，`head` 为分支顶端，均是完整提交 ID；最多 200 个路径，超过时由 `truncated` 指示）。`GET /runs/{ref}/diff/file?path=` 返回路径在 `base` 和 `head` 处的文本，文件在那里不存在时为空。它通过 git plumbing 命令读取已提交对象，因此不包含未提交编辑，并需要与读取运行相同的权限。它遵守工作区自身的文件规则：允许子路径之外的路径不列出且返回 404；工作区 DLP 策略拒绝时返回 403（读取与工作区文件读取一样被审计）；超过 16 MiB 的文件返回 413。没有工作树的会话和其他租户的运行返回 404；分支已消失或与工作区没有共同历史时返回 409。仓库中未被任何分支、标签或远程分支持有的提交 ID 也会使 `worktree_from` 返回 422。

### 仓库携带的代理配置

克隆的仓库可能携带会运行程序或重定向流量的代理配置。会话在某个文件夹中启动时，
引擎把它当作不受信任的输入。它读取下列文件，绝不执行它们，并且只收集所列出的键。
空值（`{}`、`[]`、`""`、`false`、`null`）不配置任何内容，也不会被标记。

| 工具 | 文件 | 被标记的内容 |
| --- | --- | --- |
| Claude Code | `.claude` 文件夹中的 `settings.json` 和 `settings.local.json` | `hooks`、`apiKeyHelper`、`statusLine`、`mcpServers`、`enableAllProjectMcpServers`、`enabledMcpjsonServers`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`fileSuggestion`、`enabledPlugins`、`extraKnownMarketplaces`，以及名称列在最后一行中的 `env` 条目 |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`、`notify`、`mcp_servers`、`model_providers`、`otel`、任何顶层 `*_base_url`，以及名称列在最后一行中的 `shell_environment_policy.set` 条目；`profiles.<name>` 下的相同键 |
| Gemini CLI | `.gemini/settings.json` | `hooks`、`mcpServers`、`toolDiscoveryCommand`、`toolCallCommand`、`proxy`、`tools.discoveryCommand`、`tools.callCommand`、`mcp.serverCommand`、`telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`、`.env` | 名称列在最后一行中的条目 |
| 环境变量名称 | | `NODE_OPTIONS`、`NODE_EXTRA_CA_CERTS`、`NODE_TLS_REJECT_UNAUTHORIZED`、`LD_PRELOAD`、`LD_LIBRARY_PATH`、`LD_AUDIT`、`DYLD_INSERT_LIBRARIES`、`BASH_ENV`、`PATH`、`PYTHONPATH`、`GIT_SSH_COMMAND`、`GIT_EXTERNAL_DIFF`、`SSL_CERT_FILE`、`CLAUDE_CODE_SHELL_PREFIX`、`ANTHROPIC_CUSTOM_HEADERS`、`AWS_ENDPOINT_URL*`，以及任何以 `_BASE_URL`、`_API_BASE`、`_ENDPOINT` 或 `_PROXY` 结尾的名称（`NO_PROXY` 只会收窄代理，不被标记） |

存在但无法按其格式读取的文件（不是有效的 JSON 或 TOML、超过 1 MiB、嵌套深度超过
64 层、不是常规文件）算作被标记，因为无法证明它是安全的。在 TOML 中，带点的键或
表头的每一段都算一层，与解析后的文档一致，并与其外围的括号叠加：一个有 30 段的表头
加上 35 层嵌套的内联表是 65 层。文件一旦有 `[[array]]` 表头，表头的各段就计两次，
因为每一段都可能指一个数组及其元素。超大的文件在每次检查时都会被完整哈希。
没有任何被标记内容的文件夹照常启动。

- `GET /workspaces/{ref}/project-config` 按文件列出被标记的键及其值（URL 中的密码
  会被掩码），以及恰好是该被标记部分的 `sha256`，涵盖全部三种工具的文件，外加
  `approved` 和最近一次 `approval`。它读取的是文件夹本身；从另一个提交启动的
  工作树可能带有不同的已跟踪文件，其拒绝提示会指明它自己的哈希。
- `POST /workspaces/{ref}/project-config/approve` 接受 `{"sha256":"<reviewed hash>"}`，
  需要工作区管理权限，并拒绝代理身份。批准会存储操作者、时间和哈希，并在同一事务中
  写入一条 `sessions.workspace.approve-project-config` 审计条目。

除非已存储的批准等于当前哈希，否则读取被标记文件的工具的启动或 resume 返回 409。
更改被标记值的一个字符，或增加、移除被标记的键，都需要重新批准；更改未被标记的键则不需要。
没有跳过该检查的选项。

携带引擎 hook 的 Claude Code 会话以 `--setting-sources ""` 启动；在 Claude Code
2.1.299 上，该会话不加载项目或本地设置文件，也不加载项目 `.mcp.json`，因此这样的启动
不会被拒绝。不带引擎 hook 的 Claude Code 启动（远程控制，或没有 hook 端点的引擎）
会读取这些文件，因而要接受检查。Codex 只对在其账户主目录中受信任的项目遵从
`.codex/config.toml`，并忽略不受信任项目的该文件；引擎无法询问该工具，所以它检查该文件，
并由人批准一次。按引擎启动方式（`gemini --acp`）启动的 Gemini CLI 0.64.0 会忽略其账户主目录
不信任的文件夹的 `.gemini/settings.json` 和 `.gemini/.env`，而在受信任的文件夹中会运行
`tools.discoveryCommand`、`hooks` 和 `mcpServers` 命令，并从 `.gemini/.env` 获取
基础 URL；出于同样的原因，引擎检查这些文件。它还会加载父文件夹的文件，该检查不读取这些文件。

使用 CLI 时，用 `olivares agent workspace project-config <ref>` 读取，并用
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` 批准。

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

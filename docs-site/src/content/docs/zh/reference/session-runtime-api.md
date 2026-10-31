---
title: 会话运行时 API（官方 CLI）
description: >-
  Community HTTP 面：列出、连接、输入并停止自有的 Claude Code、Codex 与 Grok CLI 进程。
  权限、stdio I/O、恢复与重连。
---

控制平面 **启动供应商 CLI**。它不替换 Claude Code、Codex 或 Grok Build。会话与终端
是本产品的一个模块，不是产品本身。

本页记录 `/v1/m/sessions/runs` 下的 Community operate 路由。它们已在
[beta OpenAPI 文档](/reference/api-beta/) 中。26.10 增加了驱动契约、各启动形式所需的
传输方式，以及作为测试的旅程 J01–J08。

## 版本边界

| 版本 | 做什么 | 不做什么 |
|---|---|---|
| **Community（本页）** | 自有本地子进程：启动、stdin/stdout/stderr、带游标的 attach、恢复确切对话、重连活流、以观察到的退出状态停止。会话行与证据留在模块 II。 | Identity & Scale 多窗格引擎、mTLS 监听器、cockpit 窗格输入、xterm UI 块 |
| **Identity & Scale 叠加** | 商业 session-cockpit 引擎（监听器、窗格、账本）。有模块时路由位于 `/v1/m/session-cockpit/` 下。 | 不替换 `/v1/m/sessions/runs` |

Community 构建以 **缺席**（404）回答叠加命名空间。它不挂载 501 存根。

Claude Code hook 路径仍为 `olivares claude-hook`（PreToolUse PEP）。那是观察与
执行，不是替代 CLI。

## 权限

模块路由上现有的执行点：

| 权限 | 路由 |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `POST /runs/{ref}/pin`, `DELETE /runs/{ref}/pin` |
| `sessions:output:read` | `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/changes`, `GET /runs/{ref}/changes/file`, `GET /runs/{ref}/git`, `GET /runs/{ref}/diff`, `GET /runs/{ref}/diff/file`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages`, `GET /runs/{ref}/attachments/{id}` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/resume`、`POST /runs/{ref}/move`、`POST /runs/{ref}/handoff`, `PUT /runs/{ref}/name`, `POST /runs/{ref}/archive`, `DELETE /runs/{ref}/archive` |
| `sessions:stop:write` | `POST /runs/{ref}/stop` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`、`DELETE /runs/{ref}` |

viewer 可以列出会话并为自己固定会话。创建和输入需要 `sessions:run:write`，停止需要 `sessions:stop:write`。输出、会话记录以及文件或 Git 内容现在需要 `sessions:output:read`，editor 及以上角色默认拥有此权限。viewer 保留元数据和个人标记，但未经明确授权将失去对这些内容的访问权限。缺少权限时返回 403。

## 控制台读取的路由

基路径：`/v1/m/sessions`。需认证。发送 `X-Olivares-Tenant`。

| 方法 | 路径 | 结果 |
|---|---|---|
| `GET` | `/runs` | 托管运行分页。可选 `state`、`live_ref`、`claude_session_id`（旧版） |
| `GET` | `/runs/{ref}` | 一次运行。`state` 为派生值。`exit_code` 为观察值。没有捏造的成功字段。会话归档期间包含 `archived_at`（RFC 3339，UTC） |
| `GET` | `/runs/{ref}/events` | 生命周期证据行 |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE：`output` 帧；环形缓冲区因游标以下内容被驱逐时返回 `lag`；`end` 或非活动的 `notice`。对于在启动时被赋予保险库机密、仓库凭证、托管凭证或包注册表令牌的运行，其后恢复的 Grok、OpenCode 或 Gemini CLI 会话不会流式返回其工具在 resume 时重放的对话，并会通过一个 `olivares_notice` 帧（`history_withheld`）说明此情况 |
| `POST` | `/runs/{ref}/input` | stdin。stream-json 使用 `line`/`message`。Codex/Grok 使用 `text`。返回 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | 对进程组先发 SIGTERM，再发 SIGKILL。行上记录观察到的退出状态 |
| `POST` | `/runs/{ref}/resume` | 新的进程世代。确切的已存储对话。不会静默回退为新对话 |
| `POST` | `/runs/{ref}/interrupt` | 取消活动回合。进程保留 |
| `POST`, `DELETE` | `/runs/{ref}/pin` | 为调用者固定会话，或取消固定。该固定属于调用者本人，并跟随其到另一台浏览器或机器；它从不改变他人的列表。两次调用都是幂等的，均返回 200 `{run_ref, pinned, pinned_at}`。每人最多保留 20 个固定：固定第 21 个时，最旧的一个被移除。释放或删除会话会清除其上的所有固定。`GET /runs` 和 `GET /runs/{ref}` 携带调用者自己的 `pinned` 与 `pinned_at`；`GET /runs` 还会返回调用者落在本页之外的已固定会话（比 `limit` 多至多 20 行；`pagination=cursor` 不增加任何行，因为它本身一次就能遍历每个会话）。固定属于创建它的凭证：某人的控制台与 `olivares login` 会话共享固定，API 令牌则保留各自的固定。已释放的会话回答 409 |
| `POST`, `DELETE` | `/runs/{ref}/snooze` | 为调用者将会话延后到某一时刻，或结束延后。`POST` 接受 `{until}`，即一个未来的 RFC 3339 时间（否则为 400）；再次 `POST` 会移动该时间。延后属于调用者本人，与固定一样：控制台会在 `until` 之前将该会话移出此人的列表，其他人的列表不变。两者均返回 200 `{run_ref, snoozed_until?, done}`。`GET /runs` 和 `GET /runs/{ref}` 仅在延后仍有效期间携带调用者的 `snoozed_until`。释放或删除会话会清除其上的所有延后。已释放的会话回答 409 |
| `POST`, `DELETE` | `/runs/{ref}/done` | 为调用者将会话标记为已完成，或清除该标记。控制台会把已完成的会话归入此人列表中的「更早」。两者均幂等，并返回 200 `{run_ref, snoozed_until?, done}`。`GET /runs` 和 `GET /runs/{ref}` 携带调用者的 `done`。释放或删除会话会清除其上的所有标记。已释放的会话回答 409 |
| `GET`、`POST`、`PUT` | `/runs/{ref}/queue` | 工具工作期间为会话排好的消息。`GET` 返回 `{run_ref, items, hold?, can_steer}`：按发送顺序排列的消息（`id`、`text`、`queued_by`、`queued_at`）、第一条尚未发出的原因（`hold`：`turn`、`approval`、`limit` 或 `stopped`；可立即发出时不出现），以及工具是否接受向进行中的轮次插入消息（`can_steer`）。`POST` 接受 `{text}`，返回 201 和该消息；会话不忙时立即发送。`PUT` 接受 `{ids}`，即队列中每个 id 按新顺序各出现一次（否则 400），返回队列。队列保存在服务器上，因此刷新页面后仍在，并显示在每台设备上。轮次结束时，第一条消息通过会话的输入只发送一次；已停止、被中断或达到额度的会话，以及等待批准的会话，会保留队列。写入权限和版本规则与 `/input` 相同；由工作项启动的会话和已释放的会话返回 409。释放会话会丢弃其队列，`POST /runs/{ref}/cleanup` 的返回以 `discarded_queue` 列出被丢弃的消息 |
| `PATCH`、`DELETE` | `/runs/{ref}/queue/{id}` | 修改一条排队消息的文本（`{text}`，返回 200 和该消息），或将其移出队列而不发送（返回 200 和队列）。不在队列中的 id 返回 404 |
| `POST` | `/runs/{ref}/queue/{id}/steer` | 立即通过会话的输入发送一条排队消息：工具接受时发入进行中的轮次（`can_steer`），否则作为下一条消息。消息先离开队列，因此只发送一次；在写入任何内容之前被拒绝时，它会回到原位。返回 202 `{accepted}`。不接受向进行中轮次插入消息的工具，在其轮次进行期间返回 409 |
| `PUT` | `/runs/{ref}/name` | 请求体 `{name}`。重命名会话。名称会去除首尾空白；空名称会清除名称，客户端改用各自的命名。超过 200 个字符、包含控制字符或不可见字符（格式字符、行或段落分隔符：零宽空格、方向覆盖字符）或缺少 `name` 时回答 400。返回 200 及运行对象。名称属于会话，因此每个读取者都能看到。在审计账本中记录为 `sessions.run.renamed`，记录的是运行引用而不是名称；与会话现有名称相同的名称不改变任何内容，也不记录任何内容 |
| `POST`, `DELETE` | `/runs/{ref}/archive` | 归档会话或取消归档。无请求体。返回 200 及运行对象，归档期间包含 `archived_at`。归档既不停止也不移除会话。该标记属于会话而非调用者：会话对所有人都处于归档状态。`GET /runs` 不因此缩小范围；由各客户端在其列表中省略已归档的会话。两个调用都是幂等的：归档已归档的会话会保留其 `archived_at`，且不记录任何内容。在审计账本中记录为 `sessions.run.archived` 和 `sessions.run.unarchived` |
| `POST` | `/runs/{ref}/move` | 请求体 `{provider_profile_ref}`。将已停止或失败的 Claude Code 会话重新绑定到同一工具的另一个配置文件，并携带其对话。不启动任何内容。返回 200 及运行对象 |
| `GET` | `/runs/{ref}/handoff` | 交接将从其开始的摘要：`summary`、`source_driver`、`turns`、`transcript_bytes`。只读，记录在审计账本中。运行中的会话按其当前状态读取；尚未启动的会话回答 409 |
| `POST` | `/runs/{ref}/handoff` | 请求体 `{provider_profile_ref, summary?, name?}`。在该配置文件中启动一个与本会话相关联的新会话。返回 201 及运行对象，外加 `summary_state`（当其不为 `delivered` 时还包括 `first_turn`）；启动等待批准时返回 202 |
| `GET` | `/runs/{ref}/activity` | Claude Code 或 Codex 会话的工具正在做什么，每次请求时从其自身的原生记录中读取：`agents`、`tasks`（命令与后台工作）、`files`（路径、`added`、`removed`、`created`）、`todos`、`skills`、`mcp_servers`、`model`、`context_tokens`、工具报告时才有的可空 `context_window`，以及 `context_percent`（Claude Code 的百分比，未知时为 null）、`compactions`，以及 `transcript`（`read`；工具写入之前为 `pending`；本应存在却缺失时为 `missing`）。智能体与任务的状态为 `running`、`completed`、`failed`、`stopped` 或 `scheduled`。Codex 使用其 rollout 文件，而不会启动另一个 app-server；context tokens 描述的是最近一次请求，而非累计用量。只读；对每个读取者与会话，每 15 分钟在审计账本中记录一次。不受支持的工具回答 422；被赋予保险库机密、仓库凭证或托管凭证的会话回答 409；使用过包注册表令牌的会话在停止时，或在某次使用了此类令牌的启动之后的 resume 之后运行时，同样回答 409 |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | 一个 Claude Code 子智能体的对话记录，以纯文本轮次形式呈现（`role`、`text`）。只读，记录在审计账本中。未知智能体回答 404。Codex 在根 Activity 回答中报告子级状态；不提供子级对话记录的读取 |
| `GET` | `/runs/{ref}/messages` | Claude Code 或 Codex 会话的消息，每次请求时从工具自身的转录记录中读取，分页返回：`items`（`id`、`role`（`user` 或 `assistant`）、`at`、`first_line`）、`total`、`next`、`transcript`（`read`、`pending`、`missing`）。默认仅包括人员发送的内容；`role=all` 会加入工具的回答。`q` 保留任一位置包含该短语的消息（不区分大小写，最多 256 字节）；`at`（RFC 3339）保留该时间或之后的消息，因此第一项即为锚点；`order` 为 `asc`（默认）或 `desc`，`limit` 最多 200，`cursor` 为分页续接所用的 id（即 `next`）；items 始终按从旧到新排列。`id` 读取一条完整消息（`text`，以及 `later_same`，即之后有多少条同一发送者的消息与其首行重复；此时不应用任何过滤器，`total` 为会话的消息总数）。`text=1` 为页面的每个条目加上 `text`（与按 `id` 读取一样，每条消息上限 64 KiB），且不含 `later_same`；在该上限处被截断的文本以 ` [...]` 结尾，其条目带有 `clipped: true`（按 `id` 读取时同样如此）；这样的页面最多包含 50 条消息；要复制整段对话，请沿 `next` 继续读取。消息的 `id` 是其在会话消息中的位置，不会随转录记录增长而改变。只读；对每个读取者、会话与 15 分钟窗口，在审计账本中记录一次，记为 `sessions.run.messages`；带 `text=1` 的读取记为 `sessions.run.messages.text`。不受支持的工具回答 422；被赋予保险库机密、仓库凭证或托管凭证的会话回答 409；未知的 `id` 回答 404；无效参数回答 400 |
| `DELETE` | `/runs/{ref}` | 需要 `sessions:run:admin`。删除已释放会话的记录。未释放的会话回答 409 `session must be cleaned before delete (state=…)`。在删除所在的事务中，于审计账本记录为 `sessions.run.deleted` |
| `GET` | `/runs/{ref}/diff` | 工作树会话的分支相对于其起点的差异：`branch`、`base`、`head`，以及变更的 `files`（`path`、`status`）。没有工作树时回答 404 |
| `GET` | `/runs/{ref}/diff/file?path=` | 某路径在 `base` 与 `head` 处的文本（`original`、`modified`，各自最多为工作区 `max_read_bytes` 与 64 KiB 中的较小值） |

对于 plan 参数未保留在 rollout 中的 Codex Code Mode 调用，Activity 还会读取实时桥接已收到的最新
`turn/plan/updated` 通知。浏览器刷新使用同一个服务端会话；刷新不会启动 app-server。实时句柄消失之后，
只能读取 Codex 自身保留的数据。

对于 Claude Code，窗口取自其原生 result 帧中的 `modelUsage[model].contextWindow`，并在停止与重连后保留。
当报告匹配且无歧义时，`context_percent` 为 `100 × context_tokens / context_window`；否则两个字段均为
`null`，CLI 与控制台显示 token 数并附带窗口未知的提示。新的工具启动会清除先前的窗口报告。

工作流任务可包含 `workflow`：`run_id`、`name`、`duration_ms`、`agents_total`、`agents_done`、`agents_failed`
与 `phases`。每个阶段有 `index`、`title`、可选的 `detail` 与 `state`，以及相同的智能体计数。计数描述的是迄今观察到的智能体。
运行期间，阶段状态描述观察到的运行中或失败的智能体。智能体完成并不能证明脚本已离开该阶段，因此不会将阶段标记为已完成，
也不会识别当前的脚本步骤。已完成的阶段状态由最终工作流摘要提供。

实时工作流快照来自 Claude Code 的 `system/task_progress` 输出，与 attach 流共享。该工具已完成的
`workflows/<runId>.json` 在对话记录主目录、链接与大小规则之下填充最终摘要。摘要读取受限：每个文件 8 MiB，
每个请求 64 MiB；更大的原生日志或结果会使摘要不可读，同时保留可用的实时进度。不可读的摘要以及有界实时缓冲区中的缺口，
计入 `unreadable`。

断开 attach 后的重连，是对**同一**活动进程的 `GET …/attach?from={last+1}`。进程丢失后，attach 会告知会话不活动。
Resume 启动新的世代。Reconnect 不会凭空产生替代进程（SDD R04）。

### 移动会话与交接

会话可以通过两种方式在另一个账户中继续，控制台以及 `olivares session move` / `olivares session handoff`
两种方式都提供。「移动」会把 Claude Code 会话的已存储对话带到同一工具的另一个配置文件，因此只有 Claude Code
会话可以被移动。「交接」则基于 Claude Code、Codex、Gemini CLI 或 OpenCode 会话的对话，启动一个新会话。

当工具以限额结束最后一个回合时，运行会携带 `limit`：`{kind, resets_at, account_ref}`。`kind` 为 `usage`
（账户的套餐额度已用尽）或 `rate`（提供商正在限制请求速率）。`resets_at` 为 RFC 3339 格式，仅当工具报告限额何时解除时存在，
否则缺席；`account_ref` 是该回合所使用的配置文件。Claude Code 在其自身结果中提供该事实（`api_error: usage_limit_reached` 或
HTTP 429），并在其 `rate_limit_event` 中提供重置时间；Codex 在类型为 `usageLimitExceeded` 或 `rateLimitExceeded` 的失败回合中提供该事实，
并在其 `account/rateLimits/updated` 窗口中提供重置时间；当两个窗口都已用尽时，记录较晚的重置时间，距今超过一年的重置时间则略去。
该事实首次被记录时，账本新增一条 `limit_reached` 事件；再次遇到同一事实则不新增。下一个成功的回合会清除 `limit`，
移动到另一个账户同样会清除。其他任何类型的结果都不会设置它。

`POST /runs/{ref}/move` 保持同一个会话。它要求会话处于 `stopped` 或 `failed` 状态，且其对话位于其账户主目录中；
并要求目标配置文件属于同一工具，且在其下的启动会被接受（active、本节点的环境、已授权的来源）。引擎只复制那一个对话——
转录记录及其子智能体与工具结果文件——到目标主目录，绝不复制登录信息、设置或其他对话，并在一个事务中重新绑定运行行、实时行与账本。
运行保留其 `run_ref` 与供应商对话 id；账本新增一条 `moved` 事件，其详情以引用方式命名两个配置文件并统计所携带的文件数；
失败的会话保留其 `reason`。源主目录只被读取。被拒绝时不写入任何内容，并清除移动所产生的内容：

- 409：运行中的会话；被赋予保险库机密、仓库凭证、托管凭证或包注册表令牌的会话（其打印内容无法证明不含这些）；
  其主目录不属于其配置文件的行；在目标下对话 id 已绑定到另一会话；目标持有该对话的不同版本；目标属于另一工具；
  或文件在携带过程中发生了变化（请重试）；
- 422：对话中的链接、文件的第二个名称或特殊文件，以及另一工具的会话。

此前一次移动在目标上留下的同一对话副本，会被较新的副本替换。不会启动任何内容：下一次 `POST /runs/{ref}/resume`
才是普通的 resume，因此目标配置文件的策略、预算与审批同样适用于它。尚无对话的会话移动时没有可携带的内容，并会在目标上重新开始。

`GET /runs/{ref}/handoff` 与 `POST /runs/{ref}/handoff` 用于在另一个不存在原生 resume 的工具中继续。两者都读取
Claude Code、Codex、Gemini CLI 或 OpenCode 会话的已存储对话（Grok 会话回答 422）。会话可以是 `stopped`、`failed`、
`running` 或 `idle`；尚未启动的会话回答 409。预览按运行中会话的当前状态读取。POST 会先像 `POST /runs/{ref}/stop`
那样停止运行中的会话，并在其工具退出后读取其对话；所有不需要停止的拒绝（目标配置文件、保险库机密、仓库凭证）都发生在停止之前，
而停止之后的拒绝会说明该会话已被停止且可以恢复。Codex 从其 rollout 读取，Gemini CLI 从其聊天日志读取，OpenCode 则通过其自带的
`opencode export` 读取，该命令在会话的配置文件下只运行一次且离线进行。预览由已存储的对话生成，不使用模型，因此人员可以审阅与编辑；
每次读取预览都会作为 `sessions.run.handoff_preview` 记录在审计账本中。POST 在所选配置文件中启动一个新会话，位于同一工作区并使用同一模板，
其第一轮是经过审阅的 `summary`（缺省时自动生成）以及转录记录的路径；该转录记录写在目标配置文件的配置主目录下，使用其专属名称。
转录记录会完整携带每条消息（每条最多 256 KiB），工具调用与结果会被缩短，并说明有多少条记录无法读取。新运行报告 `handoff_of`，
原会话的账本新增一条 `handed_off` 事件。回答中带有 `summary_state`：

| `summary_state` | 含义 |
|---|---|
| `delivered` | 第一轮已到达新会话。 |
| `not_delivered` | 未送达。`first_turn` 持有要发送的文本。 |
| `unknown` | 已尝试，但结果未得到确认；再次发送 `first_turn` 之前请先核实。 |
| `waits_for_approval` | 启动被扣留，等待人工批准（202）；不会自动发送第一轮。 |

`first_turn` 仅在状态不是 `delivered` 时出现，因为它指明了附件的路径。当源对话所属会话被赋予保险库机密、仓库凭证、
托管凭证或包注册表令牌时，该对话会以 409 被拒绝，因为交接会把它发送给另一个供应商；超过 256 MiB 或包含单条大于 32 MiB
的记录时，则以 422 拒绝。

### 工作树选项

`POST /runs` 接受可选的布尔值 `worktree`。当其为 true，且 `workspace_ref` 指向一个已登记、并且是某个 git 仓库顶层文件夹的工作区时，
会话会在该仓库的一个新 git 工作树与新分支中工作（`workspace_path` 为该工作树，运行报告 `worktree_branch`）。若缺省、为 null 或为 false，
则保持当前行为。对于不是 git 工作树、不是仓库顶层文件夹、没有提交、为只读或含只读文件夹、其 git 配置指定了过滤器或包含某个文件的工作区，
对于非原生隔离，以及没有工作区的启动，引擎回答 422；节点没有工作树目录时回答 503。`POST /runs/{ref}/resume` 会返回同一工作树。

`POST /runs/{ref}/cleanup` 接受可选请求体 `{"discard_worktree": true}`；无请求体时，它仍是一直以来的调用方式，
且服务端仍会接受任何请求体：只有明确的 `true` 才会确认。当会话的分支已合并到工作区当前分支、工作树位于该分支上且没有未提交文件时，
有工作树的会话会被释放；否则该调用会以 409 被拒绝（存在未合并的工作、分离的 HEAD，或节点无法访问的工作树），且运行保持已停止状态。
此时可以带 `discard_worktree` 重复调用，以无论如何删除工作树与分支；对于节点无法访问的工作树，则释放会话并使工作树留在原处。
账本的 `cleaned` 事件会记录工作树的处理结果，并附带分支顶端。

### 批准文件夹的设置命令

设置命令属于已登记的文件夹，保存在引擎存储中。仓库文件与环境变量无法更改它。
保存和批准命令不会执行它；工作树启动会执行它，具体如下。

- `PUT /workspaces/{ref}/setup-command` 接受 `{"command":"npm ci"}`。准确的 UTF-8 文本会被原样保留，包括空白；
  最多 8192 字节，且不得含 NUL。空字符串会清除该命令。需要工作区管理权限。
- `GET /workspaces/{ref}` 返回 `setup_command`、`setup_command_sha256`，批准之后还返回 `setup_approval`，其中含 `sha256`、`actor` 与 `at`。
  只有当该批准涵盖当前文本时，`setup_approved` 才为 true。编辑之后，`setup_approval` 作为证据保留，因此应检测 `setup_approved`，
  绝不要检测 `setup_approval` 是否存在。
- `POST /workspaces/{ref}/setup-command/approve` 接受 `{"sha256":"<reviewed hash>"}`。它需要工作区管理权限，并拒绝智能体身份。
  该哈希必须与当前存储的文本匹配；并发编辑返回 409。批准及其 `sessions.workspace.approve-setup` 审计条目在同一事务中提交。

当命令没有匹配的批准时，工作树启动会在创建工作树之前返回 409。其说明语句会指明该命令与批准步骤。只要更改一个字符（包括空白），
就需要重新批准。最后一次批准作为证据保留；其哈希必须等于 `setup_command_sha256` 才会生效，因此把命令改回此前已批准的文本，
会再次应用该批准。任何能读取该文件夹的人都能读取此命令，所以不要把机密放进去。带有 `"no_setup":true` 的 `POST /runs`
只对该次启动跳过设置。对带工作树会话的交接会启动新工作树，并需要同样的批准；它没有跳过选项。不使用工作树的启动保持其现有行为。

新工作树创建后、会话首轮之前，已批准的命令在工作树文件夹中以 `/bin/sh -c` 执行一次。
它在文件夹隔离（工作树及其 Git 元数据）和会话网络策略下运行。相同的允许列表也适用于下载：
绑定提供商密钥的会话只能访问其提供商和明确允许的主机，因此运行安装程序前请允许包注册表。
它不会收到会话的环境变量、凭据、提供商账户主目录或凭据文件；其 `PATH` 与会话相同，
因此能找到会话配置中的程序。非零退出状态或达到时间限制而停止都会阻止启动，运行账本会记录包含命令最后输出的
`worktree_setup` 事件。请求最多等待命令 20 秒。在此时间内结束的命令照常响应：失败时响应为 422
并携带最后输出。此时仍在运行的命令归会话所有：响应为 201，`state` 为 `pending`，命令在会话的
文件夹隔离下继续运行，停止请求会结束它。其时间限制为 10 分钟。会话的第一次输入最多等待命令完成 30 秒，
之后会以 409（`session is not running`，`state=pending`）被拒绝，由调用方重新发送。控制台会保留第一条消息直到会话就绪再发送，
因此安装程序运行期间发送的消息会在其结束后送达，绝不会进入尚未准备好的文件夹。失败、达到限制或被停止的命令会使运行变为 `failed`（停止请求则为
`stopped`），并保留相同的事件和最后输出；任何输入都不会到达工具。resume 会等待其命令结束，
并保持 20 秒限制。在 Linux 上，命令结束后，它留下的任何进程都会停止。在其他平台上，为避免留下分离的进程，启动会被拒绝。命令写入的文件会与原有文件一样接受检查：能运行程序或重定向
流量的项目智能体配置会阻止启动。

命令在工作树会话首次启动时执行。等待批准的启动会在批准允许启动时执行命令。设置失败后，
resume 会先再次执行命令；如果命令未获批准或未能成功完成，会话不会启动。设置成功后，
即使后续启动步骤失败，resume 也不会再次执行命令。如果无法确认进程清理完成，resume 将被拒绝；
请停止剩余进程并创建新的工作树。`no_setup` 会跳过该次启动及后续 resume 的设置。
对于记录设置状态之前的旧版本工作树运行，如果从未启动，则会改为执行一次已批准的设置。

使用 CLI 时，用 `olivares agent workspace setup <ref> --command 'npm ci'` 设置，用 `olivares agent workspace setup <ref>` 读取，
并用 `olivares agent workspace approve-setup <ref> --sha256 <hash>` 批准所显示的哈希。`olivares session start --worktree --no-setup`
仅跳过一次设置。

### 从提交或分支启动工作树

对于 `worktree`，`POST /runs` 还接受可选的 `worktree_from`：一个完整提交 id（40 或 64 位小写十六进制数字），
或工作区仓库的一个本地分支。新工作树及其自身的新分支从该处开始，而不是从工作区的当前提交开始；指定的分支与工作区检出都不会移动。
接收方借此打开交接所指定的工作，交接内容可以携带可选的 `branch` 与 `sha`。Git 会把该值解析为完整提交 id，且仅使用该 id。
对于仓库中不存在的提交、不存在的分支、修订表达式、范围、选项，以及没有 `worktree` 的 `worktree_from`，引擎会在创建任何内容之前回答 422。
省略时，启动仍从工作区的当前提交开始。

`GET /runs/{ref}/diff` 列出会话工作树分支自离开工作区当前提交以来变更的路径（`base` 为其合并基点，`head` 为分支顶端，
二者均为完整提交 id；最多 200 条路径，`truncated` 指示是否还有更多路径），而 `GET /runs/{ref}/diff/file?path=` 返回某路径在
`base` 与 `head` 处的文本，若文件在该处不存在则为空。它使用 git 底层（plumbing）命令读取已提交的对象，因此不包含未提交的编辑，
并且需要与读取运行相同的权限。它遵循工作区自身的文件规则：允许子路径之外的路径不会被列出，并回答 404；工作区 DLP 策略拒绝时回答 403
（读取会与工作区文件读取一样被审计）；超过 16 MiB 的文件回答 413。没有工作树的会话以及其他租户的运行回答 404；分支已不存在，
或与工作区没有共同历史时，回答 409。对于仓库中任何分支、标签或远程分支都未持有的提交 id，`worktree_from` 同样回答 422。

对该 diff 某一行的评论会作为下一条消息回到智能体，与其他输入一样经由 `POST /runs/{ref}/input`。加上 `review_comment`（`path`、从 1 开始的 `line`，以及 `side`：`original` 表示 `base` 处的文件，`modified` 表示 `head` 处的文件）来指明位置。格式错误的位置，或与 `attachments` 一同发送的位置，回答 400 且不发送任何内容。输入被接受后，审计账本会记录 `sessions.run.review_comment`，含运行、路径、行和侧；评论本身是工具收到的消息，不会复制进账本。`olivares session diff <session>` 以 unified diff 形式输出同一份 diff。

### 仓库携带的智能体配置

克隆的仓库可能携带会运行程序或重定向流量的智能体配置。会话在某个文件夹中启动时，引擎将其视为不受信任的输入。
它读取下列文件，绝不执行它们，并且只收集所列出的键。空值（`{}`、`[]`、`""`、`false`、`null`）不配置任何内容，也不会被标记。

| 工具 | 文件 | 被标记的内容 |
| --- | --- | --- |
| Claude Code | `.claude` 文件夹中的 `settings.json` 和 `settings.local.json` | `hooks`、`apiKeyHelper`、`statusLine`、`mcpServers`、`enableAllProjectMcpServers`、`enabledMcpjsonServers`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`fileSuggestion`、`enabledPlugins`、`extraKnownMarketplaces`，以及名称列在最后一行中的 `env` 条目 |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`、`notify`、`mcp_servers`、`model_providers`、`otel`、任何顶层 `*_base_url`，以及名称列在最后一行中的 `shell_environment_policy.set` 条目；`profiles.<name>` 下的相同键 |
| Gemini CLI | `.gemini/settings.json` | `hooks`、`mcpServers`、`toolDiscoveryCommand`、`toolCallCommand`、`proxy`、`tools.discoveryCommand`、`tools.callCommand`、`mcp.serverCommand`、`telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`、`.env` | 名称列在最后一行中的条目 |
| OpenCode | `opencode.json`、`opencode.jsonc` | 除 `$schema`、`theme`、`keybinds`、`logLevel`、`model`、`small_model`、`username`、`share`、`autoupdate`、`snapshot`、`layout` 之外的每个顶层键；文本中任何位置的每个 `{file:…}` 和 `{env:…}` 标记，其中 `{file:}` 绑定到它所指文件的 sha256 |
| OpenCode | `.opencode/` | 整个文件夹：每个文件按其 sha256（文件夹内的链接按其目标），其自身的 `.gitignore` 除外；其中的 `opencode.json` 和 `opencode.jsonc` 按上一行处理，`{file:}` 从 `.opencode/` 读取 |
| 环境变量名称 | | `NODE_OPTIONS`、`NODE_EXTRA_CA_CERTS`、`NODE_TLS_REJECT_UNAUTHORIZED`、`LD_PRELOAD`、`LD_LIBRARY_PATH`、`LD_AUDIT`、`DYLD_INSERT_LIBRARIES`、`BASH_ENV`、`PATH`、`PYTHONPATH`、`GIT_SSH_COMMAND`、`GIT_EXTERNAL_DIFF`、`SSL_CERT_FILE`、`CLAUDE_CODE_SHELL_PREFIX`、`ANTHROPIC_CUSTOM_HEADERS`、`AWS_ENDPOINT_URL*`，以及任何以 `_BASE_URL`、`_API_BASE`、`_ENDPOINT` 或 `_PROXY` 结尾的名称（`NO_PROXY` 只会收窄代理，不被标记） |

批准 `.opencode/` 前，请审阅 `package.json`，然后在该文件夹运行 `OpenCode=1.19.4; npm install --ignore-scripts --save-exact @opencode-ai/plugin@"$OpenCode"`。该包版本对应固定的 OpenCode 1.19.4，安装脚本保持禁用。检查要求存在 `node_modules/`，且 `package-lock.json` 的根条目包含 `@opencode-ai/plugin` 和所有声明的依赖。否则会拒绝会话并提示先安装依赖。已安装的目录树属于批准哈希的一部分；更改后需要重新批准。

存在但无法按其格式读取的文件（不是有效的 JSON 或 TOML、超过 1 MiB、嵌套深度超过 64 层、不是常规文件）计为被标记，
因为无法证明其安全。在 TOML 中，点号键或表头的每一段都计为一层，与解析后的文档一致，并计入包围它的方括号：
一个 30 段的表头加上 35 层嵌套的内联表，共 65 层。文件一旦含有 `[[array]]` 表头，表头各段即计为两次，
因为每一段都可能指代一个数组及其元素。超大文件在每次检查时都会被完整哈希，但 OpenCode 文件在用于解析的受限读取后即被拒绝。没有任何内容被标记的文件夹照常启动。

- `GET /workspaces/{ref}/project-config` 按文件列出被标记的键及其值（URL 中的密码会被掩码），以及恰好覆盖该被标记部分的 `sha256`，
  涵盖上述所有工具的全部文件，并附带 `approved` 与最近一次 `approval`。它读取的是文件夹本身；从另一提交启动的工作树可能带有不同的已跟踪文件，
  其拒绝提示会说明它自己的哈希。
- `POST /workspaces/{ref}/project-config/approve` 接受 `{"sha256":"<reviewed hash>"}`，需要工作区管理权限，并拒绝智能体身份。
  批准会存储操作者、时间与哈希，并在同一事务中写入一条 `sessions.workspace.approve-project-config` 审计条目。

对于读取被标记文件的工具，除非已存储的批准等于当前哈希，否则其启动或 resume 返回 409。更改被标记值的一个字符，
或增加、移除被标记的键，都需要重新批准；更改未被标记的键则不需要。没有跳过该检查的选项。

有些配置无法用哈希绑定，因此任何批准都不能让会话使用它。其条目带有 `refused` 原因；在该配置被更改或移除之前，无论批准了什么，该工具的每次启动或 resume 都返回 409：带有 `{env:…}` 标记的 OpenCode 文件；不是有效 JSONC 的 OpenCode 文件；所指文件位于文件夹之外、位于 `~` 下、为绝对路径或路径前后带空格，或者不是不超过 1 MiB 的普通文件的 `{file:…}`；合计所指文件超过 8192 个或 64 MiB 的 `{file:…}` 标记；展开后的替换内容超过 64 MiB；非空 `plugin` 或 `instructions` 键；超过 1 MiB 或不是普通文件的 OpenCode 文件；作为指向文件夹之外链接的 `opencode.json(c)` 或 `.opencode`；不是文件夹的 `.opencode`；以及超过 8192 个条目、64 MiB 或 1024 个文件夹，或带有指向外部的链接、链接循环、非普通文件条目或检查无法读取的文件夹或文件的 `.opencode/` 文件夹。

携带引擎 hook 的 Claude Code 会话以 `--setting-sources ""` 启动；在 Claude Code 2.1.299 上，该会话不加载项目或本地设置文件，
也不加载项目 `.mcp.json`，因此此类启动不会被拒绝。不带引擎 hook 的 Claude Code 启动（远程控制，或没有 hook 端点的引擎）
会读取这些文件，因而会接受检查。Codex 只对在其账户主目录中受信任的项目遵循 `.codex/config.toml`，并忽略不受信任项目的该文件；
引擎无法询问该工具，所以它检查该文件，并由人员批准一次。以引擎的启动方式（`gemini --acp`）启动的 Gemini CLI 0.64.0，会忽略其账户主目录
不信任的文件夹中的 `.gemini/settings.json` 与 `.gemini/.env`；而在受信任的文件夹中，它会运行 `tools.discoveryCommand`、`hooks` 与
`mcpServers` 命令，并从 `.gemini/.env` 读取基础 URL。出于同样的原因，引擎检查这些文件。它还会加载父文件夹的文件，而本检查不读取这些文件。

使用 CLI 时，用 `olivares agent workspace project-config <ref>` 读取，并用
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` 批准。

## 传输（Community）

每个官方 CLI 都以其 operate 形式所需的传输方式启动，`cliruntime.LaunchTransport` 声明具体是哪一种。目前三种形式都是 stdio 协议，
因此组合根接入 `sessions.NewProcRunner()`：stdin、stdout 与 stderr 都是管道，并保持为各自独立的流。

**Claude Code 拒绝 stdin 上的终端。** 其 `--print` stream-json 形式会回答
`Error: Input must be provided either through stdin or as a prompt argument when using --print`，
并且在没有任何协议帧的情况下以 1 退出。需要终端的是交互式形式；`sessions.NewPTYRunner()` 为此在 Linux 上保留，
其 stdin/stdout 连接到原始的本地 PTY，stderr 为管道。两者都不运行容器或沙箱隔离。宿主机上若有 podman 或 Docker，
则会为 stdio 形式的 `isolation=container` 添加一个容器运行器；sandbox 仍被拒绝（参见 [Topology 4](/how-to/run-claude-code-with-olivares/#topology-4--olivares-native-claude-in-a-per-session-container)）。

驱动契约位于 `modules/sessions/cliruntime`。种类：`claude`、`codex`、`grok`。一致性测试始终针对进程内假实现运行，
并在两种传输上针对本地对端运行。当 `claude` / `codex` / `grok` 位于 PATH 上时，一个单独的测试集会针对真实二进制驱动整个契约——
启动、attach、输入、输出、重连、停止、恢复——且不发送任何模型回合：每个握手都是协议握手，并且每次启动都会获得全新的 HOME 与配置主目录。

## 相关

- [操作提供商会话](/how-to/operate-provider-sessions/)
- [模块 II — 实时运行](/reference/modules/ii-sessions/)
- [连接 Claude Code](/how-to/connect-claude-code/)
- [Beta OpenAPI](/reference/api-beta/)

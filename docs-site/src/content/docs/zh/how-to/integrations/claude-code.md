---
title: 集成 Claude Code
description: >-
  将 Claude Code 纳入治理 control plane：连接器、managed settings、受治理的 PEP hook，
  以及运行后控制台中显示的内容。
---

此集成将 Claude Code 纳入治理 control plane，而不会把 Olivares AI 变成强制 proxy。
`claude` 连接器接收 OTLP 遥测和 hook 事件，关联 session，并记录 R/RW 访问、成本和发现项。
需要预防性控制时，受管的 `olivares claude-hook` hook 会在每次使用工具前查询本地 Olivares
PEP。这两个平面彼此独立：接收遥测并不代表 policy 正在被强制执行。

## 添加 Claude Code

### 前置条件

- 一个包含 first-party `claude` 连接器的 Olivares AI 二进制文件。
- 用来归属观测结果的企业 tenant UUID。
- 已在待治理端点安装 Claude Code。本地接收端不需要 Anthropic API key。
- Claude Code 到 Olivares 接收端之间的本地连接。默认地址为：OTLP/gRPC 使用
  `127.0.0.1:4317`，OTLP/HTTP 和协作式 hook 使用 `127.0.0.1:4318`。
- Olivares 服务可执行的临时路径。`claude` 作为隔离 plugin 运行；如果系统将 `/tmp` 以
  `noexec` 挂载，请在 service unit 中把 `TMPDIR` 设置为 Olivares 服务账户所拥有的专用目录。

不要把 OTLP 接收端或协作式 endpoint 暴露到 loopback 之外。它们不认证发送方，因此任何能够
访问它们的主机都可能伪造遥测。受治理的 PEP 是另一处独立 surface：它使用自己的本地 socket，
认证每个请求，并记录每次决策。

1. 打开 **Control console**（`/console`），选择 **Connectors** 标签页。连接器 roster 是全局的：
   需要 superadmin 账户，保存、测试和重新加载需要 AAL3 elevation。
2. 添加一个类型为 `claude` 的 source，指定稳定的运维名称（如 `claude-code-prod`）、对应的
   tenant、`live` mode、interval `0` 和启用状态。interval 为 0 是正确配置：该连接器维护接收端，
   而不是按批次轮询。
3. 保存 source，然后选择 **Reload**。该行会确认名称、类型、mode 和状态。由于 `claude` 是
   out-of-process connector，控制台 test action 不可用；保存时会做 validation，而完整的 open test
   使用会启动 plugin 的 `olivares sources test`。

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="配置谁可以进入以及他们可以管理什么：为用户办理入驻、连接 SSO，并构建工作区与智能体组。">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="配置谁可以进入以及他们可以管理什么：为用户办理入驻、连接 SSO，并构建工作区与智能体组。">

## 配置 Claude Code

同时分发两项配置：观测 source 和受管的 agent policy。

### 1. 接收端与数据最小化

安全的初始配置就是默认配置：

| Source 设置 | 初始值 | 效果 |
|---|---:|---|
| `enable_grpc` | `true` | 在 `grpc_addr`（`127.0.0.1:4317`）提供 OTLP/gRPC。 |
| `enable_http` | `true` | 在 `http_addr`（`127.0.0.1:4318`）提供 OTLP/HTTP 和协作式 hook。 |
| `hook_path` | `/hooks` | HTTP 接收端内的协作式 hook 路径。 |
| `content_capture` | 空 | 保留结构，但不保留 prompt、工具 body 或 API body。extended reasoning 始终被 redact。 |
| `enforcement` | 空 | 观测 hook；此 source 不返回预防性决策。 |
| `allow_public_bind` | `false` | 拒绝在 loopback 之外 bind。 |

如果一台主机运行多个 OTLP 接收端，请为每个接收端分配不同的 loopback 地址，并在 agent 配置中
使用相同的值。Claude、Codex 和 Grok 在某些 mode 下默认使用 `4318`，无法同时 bind 同一个 socket。

### 2. Managed settings 与受治理的 PEP

使用 Olivares 二进制文件生成 Claude Code 的系统级文件：

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

生成器安装 `allowManagedHooksOnly: true`、一个运行 `olivares claude-hook` 的 `PreToolUse`
hook，以及 `PostToolUse` redact hook。它还通过 `grpc` protocol 启用 OTLP，因此上述 endpoint
使用接收端 `4317`，而不是 HTTP 接收端 `4318`。该文件属于受管的系统层，不应放在 session 的
`HOME` 中。

Olivares 启动时，如果 `OLIVARES_HOOK_PEP_CONFIG` 指定了一个文件，PEP server 就会启用。
以下是适用于一个 tenant 的有效 policy 示例：

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Shell commands require human confirmation"
          }
        ]
      }
    }
  ]
}
```

由 Olivares 启动的 session 会收到 `OLIVARES_HOOK_PEP_URL`、`OLIVARES_HOOK_PEP_TOKEN`、
`OLIVARES_HOOK_PEP_TENANT` 和 agent attribution 的短期值。对于独立启动的 session，operator
必须通过 secrets channel 提供这些值；不要把它们写入 `managed-settings.json`。如果 endpoint
缺失或不可用，`olivares claude-hook` 返回 `deny`。

初次非阻断式 rollout 可使用 `observe` mode，并把 `observe_until` 设置为未来的 RFC3339 时间。
这种放行是临时的：timestamp 缺失、无效或过期时都会解析为 `enforce`。在观测业务规则期间，
包括 identity、tenant、kill switch、firewall 和 fail-closed error 在内的平台 invariant 仍然强制执行。

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="需要升级认证 — AAL3（硬件、抗钓鱼）">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="需要升级认证 — AAL3（硬件、抗钓鱼）">

## CLI 用法

以下输出片段于 2026 年 8 月 30 日使用从此 worktree 构建的二进制文件测得。省略了 engine 的
一般启动消息。

### 注册 source

使用 SQLite 时，在通过 CLI 修改 roster 前应停止 engine，因为它采用 single-writer profile。
使用 PostgreSQL 时，该操作可以与 engine 并行运行。SQLite 的在线变更请使用控制台。

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

`--actor` 和 `--reason` 是必填项，因为此变更会改变数据 provenance，并记录在 audit ledger 中。

### 验证并打开连接器

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` 不打开 socket。`test` 调用 `Open` 和 `Close`，但不调用 `Gather`，不会把 source 接入
engine，也不能证明 Claude Code 正在发送遥测。如果 plugin 已设置可执行位却仍以
`permission denied` 失败，请检查 process 的 `TMPDIR` 是否位于 `noexec` volume 上。

### 确认 hook 的 fail-closed 行为

故意不配置 endpoint 时，client 会按 Claude Code 预期的格式返回拒绝：

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

此 probe 检查的是本地 client，而不是远程 policy decision。在扩大 production rollout 前，还应测试
一条允许规则、一条拒绝规则，以及带 firm identity 的 `ask` 请求。

## Control console

添加 source 不会创建历史数据。重新加载 roster 并收到第一个 event 后，operator 可以使用以下 view：

| 位置 | 显示内容 | 如何解读状态 |
|---|---|---|
| **Control console > Connectors**（`/console`） | 名称、`claude` 类型、mode、非 secret 配置、roster 状态，以及保存/重新加载 action。 | “已保存”证明持久化，但不能证明已收到 event。 |
| **Health > Connectors**（`/health`） | 连接器 health、运维消息、趋势，以及最近已知的 poll 或 activity。 | 接收端已打开且健康时，agent 仍可能保持静默。 |
| **Observability > Ingestion**（`/observability`） | 按 source 分类的 record、`edge`、`cost` 和 `finding` 类型、signal，以及首个/末个 event。 | 这些是 process 自启动以来的全局 counter；重启时归零，且不按 tenant 区分。 |
| **Sessions**（`/sessions`） | session、状态、action、model、token、cost、最近 activity，以及 `enforced` 或 `observed` posture。 | posture 汇总 event evidence，不会根据连接器注册状态推断。 |
| **Access map**（`/access-map`） | 从已观测的 tool、MCP 和 resource 归属的 R/RW edge。 | 已观测 edge 描述 activity，不等于事先 authorization。 |
| **Cost & FinOps**（`/finops`） | 从已接收遥测导出的 cost 和 token sample。 | 覆盖范围仅限 fleet 导出的内容；从未发出 OTLP 的 call 无法重建。 |
| **Security**（`/security`） | 遥测 gap、sandbox/MCP posture 和其他已发出的 finding。 | 没有 finding 并不能让未观测的 surface 变成合规。 |
| **Claude Policy**（`/claude-policy`） | 受管 Claude Code surface 的 authoring、分发、version 和 check-in 状态。 | 分发与 drift verification 是两个独立事实，分别显示。 |

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="智能体实时运行情况——每个会话当前正在执行的操作、其 token、成本与节奏，通过实时流持续更新。">
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="智能体实时运行情况——每个会话当前正在执行的操作、其 token、成本与节奏，通过实时流持续更新。">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。">

## 生产环境使用

- **分阶段 rollout：** 先以带过期时间的 observed mode 使用结构化 content 和规则。检查 false
  positive，随后逐个 tenant 提升为 `enforce`。
- **Fleet 管理：** 通过 RPM、immutable image、Ansible、Salt 或等效的企业配置管理器分发
  `/etc/claude-code/managed-settings.json`。使用第二个 `managed-settings` source 检查实际文件，
  以发现缺失或 drift。
- **职责分离：** platform team 维护接收端和可用性；security team 对规则做 version 管理；tenant
  owner 审核 `ask` 请求与 finding。每次 privileged change 都保持可归属。
- **数据最小化：** 除非存在已批准且定义了 residency 与 retention 的 forensic need，否则保持
  `content_capture` 为空。结构化数据通常足以进行 adoption 和 cost analysis。
- **加固主机：** 将接收端限制在 loopback，为 plugin 提供最小的可执行临时目录，并把 policy 设为
  read-only。不要为了启动连接器而在全局放宽 `noexec`。

## 强制执行与仅观测的内容

| Surface | 实际行为 |
|---|---|
| `claude` 连接器的 OTLP 遥测与协作式 hook | **仅观测。** 发送方主动配合；loopback 接收端不认证，且本地 process 可以省略或伪造 signal。 |
| source 上空的 `enforcement` 设置 | **仅观测。** 这是默认值，不会阻止工具。 |
| `olivares claude-hook` + PEP + managed settings | 对 Claude Code 能够 veto 的 event **强制执行** `allow`、`ask` 或 `deny`，并记录决策。endpoint failure 会以 deny-closed 拒绝。 |
| 受管层中的 `allowManagedHooksOnly` | 针对可能与 PEP 竞争的 user 或 project hook，**加固安装**。 |
| `PostToolUse` | **在 action 后观测并 redact。** 无法撤销工具已经产生的效果。 |
| Claude Code process 与 hook 之外的 action | **不在此配线的覆盖范围内。** 请以操作系统控制、原生审计和网络 policy 作为补充防线。 |

运维验证需要四项彼此独立的检查：已持久化的 roster、已打开的连接器、**Ingestion** 中可见的
event，以及确实被 PEP 阻止的工具。任何一项都不能替代另外三项。

---
title: 集成 Codex
description: >-
  将 Codex 纳入治理 control plane：连接器、managed config、受治理的 hook，
  以及运行后控制台中显示的内容。
---

Olivares AI 通过三个互补平面集成 Codex。在 read-only mode 下，`codex` source 使用企业
automation credential 读取 Analytics、Compliance、Audit Logs 和已结算成本。
`codex-managed-config` 连接器清点并检查已部署的系统 policy。最后，`olivares codex-hook`
把 session 和工具决策发送到本地 PEP。仅凭通过个人 ChatGPT subscription 认证的 session，
并不能获得企业 API 的访问权。

## 添加 Codex

### 前置条件

- 一个 Olivares AI enterprise tenant，以及可为 roster 操作执行 AAL3 elevation 的 superadmin 账户。
- 对于企业 ingestion，需要具有所需 read scope 的 platform API key 或 workspace access token，
  以及 `workspace_id`。通过 ChatGPT 登录 Codex CLI 不会提供连接器 credential。
- 对主机系统层的管理访问权限，用于分发 `/etc/codex/requirements.toml`、
  `/etc/codex/managed_config.toml` 和 trusted hook。
- 一个 Codex PEP 专用的 loopback socket。默认值为 `127.0.0.1:8448`；不要与 Claude 或 Grok
  共用，因为各 agent 期望不同的 response format。

1. 打开 **Control console**（`/console`），选择 **Connectors**。
2. 添加一个类型为 `codex` 的 source，指定稳定名称、tenant 和 batch interval。`300` 秒是 pilot
   的合理起点；请按 API budget 与 freshness objective 调整频率。
3. 对企业 source，在 secret `api_key` field 中输入 credential，选择 `auth_mode`（`api_key` 或
   `access_token`），并提供 `workspace_id`。控制台会 seal 该值，且绝不将其返回。保存、测试并
   重新加载 source。

也可以不带 credential 添加 `codex`，用于本地 catalog inventory。该 mode 不查询 Analytics、
Compliance、Audit Logs 或 Costs，且 `Gather` 不发出 remote observation。

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="配置谁可以进入以及他们可以管理什么：为用户办理入驻、连接 SSO，并构建工作区与智能体组。">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="配置谁可以进入以及他们可以管理什么：为用户办理入驻、连接 SSO，并构建工作区与智能体组。">

## 配置 Codex

### 1. Read-only 企业 source

以下设置定义 coverage：

| 设置 | 默认值 | 用途 |
|---|---:|---|
| `api_key` | 空 | automation credential 的引用。空值只启用 offline catalog。 |
| `auth_mode` | `api_key` | 把 credential 标识为 `api_key` 或 `access_token`；两者都作为 Bearer token 发送。 |
| `workspace_id` | 空 | workspace scope 的 Analytics 和 Compliance 所必需。 |
| `analytics` | `true` | Codex 使用情况和 adoption；生成结构化 sample 和 finding。 |
| `compliance` | `true` | 把 Codex Compliance log 作为 activity evidence。 |
| `audit` | `true` | 把 organization Audit Logs 作为 evidence。 |
| `costs` | `false` | 每日已结算成本。请与 `project_id` 一起启用，以免把无关支出归到 Codex。 |
| `attribute_email` | `false` | 保留 `user_id` 作为稳定 actor，避免把 email 用作 attribution PII。 |
| `compliance_prompt_scan` | `false` | 启用时临时扫描 risk pattern，只保留结构化 finding。 |
| `otlp_http` | `false` | 实验性 log 接收端；因会打开端口而禁用。目前只统计并排空 event，不会将其转换为 session。 |

初次集成时保持 `otlp_http` 禁用。受治理的 hook 提供完整的 session plane；在此版本中启用
OTLP 并不能代替该安装。

通过 CLI 操作时，把 credential 存放在 shell history 之外，并按名称引用：

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

如果启用 `costs=true`，还要添加 `project_id=<project-id>`。缺少此限制时，Costs API 的范围是
整个组织，可能混入与 Codex 无关的支出。

### 2. 系统要求与受管值

Olivares 将两层保持分离：

- `requirements.toml` 包含用户无法放宽的限制：approval policy、sandbox mode、web search、
  remote control、hook trust、禁止读取的内容和允许的 MCP server。
- `managed_config.toml` 包含受管的初始值。这些是默认值；必须保持不可变的限制应放在
  `requirements.toml` 中。

以下 policy document 有效：它默认拒绝 network access、web search、remote control 和 MCP，
同时把 write 限制在 workspace 内。

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

分发前先验证 policy，然后用同一条 command 生成两个 artifact：

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

如果 policy 含有未知 enum、没有 identity 的 MCP server 或无效 TOML，rendering 会在写入前失败。
若要稍后检查 live state 和 drift，请额外注册一个 `codex-managed-config` 类型的 source；它读取两个
系统文件，但不修改它们。

### 3. Session hook 与 PEP

Codex 从 `$CODEX_HOME/hooks.json` 读取经过实测的 hook。`command` 必须是 string，而不是 array：
array 可能成功 parse，但 hook 从不运行。实测版本也不读取 `config.toml` 中的 inline `[hooks]` table。

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

Olivares 启动时，如果 `OLIVARES_CODEX_HOOK_PEP_CONFIG` 指向有效 JSON，server 就会 mount：

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

每个 instance 治理一个 tenant，决策来自 Olivares 中已配置的 PDP。client 使用
`OLIVARES_CODEX_HOOK_URL`、`OLIVARES_CODEX_HOOK_TOKEN`、`OLIVARES_CODEX_HOOK_TENANT`、
`OLIVARES_CODEX_HOOK_AGENT`、`OLIVARES_CODEX_HOOK_ORG` 和 `OLIVARES_CODEX_HOOK_ACCOUNT`。
通过 process 和 secrets manager 提供这些值；不要嵌入 `hooks.json`。

在把 hook 作为 fleet control 之前，必须设置 `allow_managed_hooks_only=true`。如果没有强制执行
trust，Codex 可以省略 hook 而不产生 event 或 warning；静默的安装不能作为 enforcement evidence。

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="需要升级认证 — AAL3（硬件、抗钓鱼）">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="需要升级认证 — AAL3（硬件、抗钓鱼）">

## CLI 用法

输出示例于 2026 年 8 月 30 日测得。为只保留 command response，省略了一般 startup log。

### 可复现的 offline 注册

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

使用 SQLite 时，请在 engine 停止后 offline 执行 roster mutation；使用 PostgreSQL 时，可以与 engine
并行运行。SQLite 的在线变更建议通过控制台完成。

### 连接测试及其限制

2026 年 8 月 30 日在截图主机上完成的可复现测量得到以下结果：

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

process 以 code `0` 退出。该主机上有通过 ChatGPT 认证的 Codex CLI session，但 `codex-demo`
没有 `api_key`：此结果只证明 offline catalog 和 `Open` 接受了配置。它不能证明 OpenAI
authentication，不调用 `Gather`，也不读取任何一行 Analytics 或 Compliance。即使存在 credential，
`sources test` 也不会发送 upstream request，因为 `Open` 只构造 client。首次数据测试应是一次真实的
engine poll，随后看到 observation。

### 验证受管 policy

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### 测试 hook 的本地拒绝

故意不提供 endpoint 时：

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

拒绝包含在 Codex 解释的 JSON 中，因此 process 以 code `0` 退出。此 probe 验证 fail-closed client；
还必须在 hook 被标记为 trusted 的主机上，测试 Codex 是否接受 `PreToolUse` event。

## Control console

| 位置 | 显示内容 | 显示条件 |
|---|---|---|
| **Control console > Connectors**（`/console`） | source、mode、频率、非 secret 配置，以及 Test/Save/Reload action。 | 已持久化的 source 立即显示，但数据不会。 |
| **Health > Connectors**（`/health`） | 连接器状态、消息、趋势和最近 activity。 | roster 重新加载后。 |
| **Observability > Ingestion**（`/observability`） | `olivares.codex` 的 counter、observation type，以及首次/末次接收。 | `Gather` 发出数据后。这些 process 全局 counter 从 boot 开始，并在重启时归零。 |
| **Cost & FinOps**（`/finops`） | 估算的 Analytics usage，以及启用时的日结成本。 | 需要有效 credential、`workspace_id` 和已授权 API；`costs` 要求明确 opt-in。 |
| **Security**（`/security`） | adoption finding、不可用的企业 surface，以及对 Compliance data 的 opt-in 结构化分析。 | collection 后；企业 surface 的 403/404 response 会成为 posture evidence，而不是成功。 |
| **Sessions**（`/sessions`） | 包含 action、model、identity、cost 和 posture 的 session 与 timeline。 | 来自受治理的 hook。仅有 batch source 不会创建 live session。 |
| **Audit**（`/audit`） | 已导入的 activity evidence 和锚定在 ledger 中的 PEP 决策。 | 收到可归属的 log 或 decision 后。 |

不要把 offline catalog 当作 models panel 含有 remote inventory 的证据。连接器向 runtime 提供 catalog，
但此 tree 中没有 module consumer 把它发布到该屏幕。

<img class="light:sl-hidden" src="/console/health-dark.png" alt="覆盖整个资产域的存活性、可靠性与依赖关系——基于观测到的活动与陈旧性扫描推导得出，从不主动探测基础设施。">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="覆盖整个资产域的存活性、可靠性与依赖关系——基于观测到的活动与陈旧性扫描推导得出，从不主动探测基础设施。">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="覆盖整个资产域的 token 成本——趋势、分摊计费、对账、预算与预测。各项数字与 FinOps 账本所报告的完全一致。">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="覆盖整个资产域的 token 成本——趋势、分摊计费、对账、预算与预测。各项数字与 FinOps 账本所报告的完全一致。">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="护栏发现项、强制执行态势、异常队列以及防篡改的事件取证。该平面默认仅作探测——它进行记录，除非已启用并受治理的强制执行，否则不会自行阻断。">

## 生产环境使用

- **无 credential pilot：** 使用 `codex-demo` 验证 packaging 和 roster，但要标注为 offline catalog。
  不要将其用作企业连接指标。
- **治理 ingestion：** 使用 read-only automation identity 和最小 API 集。除非有已批准的 chargeback
  需求，否则保持 `attribute_email=false`。
- **Endpoint 控制：** 从 version 管理的 policy 生成 TOML 文件，通过 fleet 配置系统分发，并用
  `codex-managed-config` 轮询其状态，以区分 intent、deployment 和 drift。
- **Session 控制：** 先在 canary group 上安装 hook。扩大范围前，确认 `PreToolUse` 能阻止无害的
  action。没有产生 event 的 hook 不能计为已治理。
- **准确的 FinOps：** 只有在 `project_id` 把数据限制为 Codex 支出时才启用 `costs`。使用 Analytics
  衡量 adoption，使用 Costs API 获取结算金额；不要把两者相加，仿佛它们是两份账单。

## 强制执行与仅观测的内容

| Surface | 实际行为 |
|---|---|
| `codex` source 与企业 API | **仅观测、read-only。** 不更改 OpenAI 配置，也不拦截 inference。 |
| 没有 `api_key` 的 mode | **Offline catalog。** 不能证明 ChatGPT subscription、remote API 或 workspace。 |
| `requirements.toml` | **强制执行用户无法放宽的系统限制**，包括只信任受管 hook。 |
| `managed_config.toml` | **设置受管默认值。** 不会代替 `requirements.toml` 中的限制。 |
| `codex-managed-config` | **观测并比较 drift。** 绝不修正主机上的文件。 |
| `PreToolUse` 或 `PermissionRequest` 上的 `olivares codex-hook` | **可以阻止 action。** Codex 不接受 `permissionDecision=allow`；Olivares 以不干预表示 allow，并把 `ask` 请求转换为拒绝。 |
| `PostToolUse` 和 lifecycle event | **能力不对等的 evidence。** 后续 block 无法撤销已执行的工具，且 `SessionEnd` 没有 veto output。 |
| Codex OTLP 接收端 | **此版本中仅部分接收。** 统计并排空 event，但尚未转换为 session 或 finding。 |

完成条件是累积的：source 必须重新加载，第一个 `Gather` 必须返回企业数据，system policy 必须验证，
trusted hook 必须被观测，而且 `PreToolUse` 必须经实证被 veto。`ANSWERED` 只覆盖 `Open` 的第一部分。

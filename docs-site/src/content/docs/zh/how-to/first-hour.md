---
title: "使用 Olivares AI 的第一个小时"
description: >-
  安装控制平面、打开控制台、连接一个编码智能体、运行一次允许一项操作并拒绝另一项
  操作的受治理会话，并阅读证据。三种形态：本地（SQLite）、团队（Postgres 与
  Docker）、混合。
---

本页是**本树**上的第一个小时。它不是模型图。本地形态中的每条命令，都是
`task smoke:first-hour` 对 `./bin/olivares` 实际运行的命令。若某形态需要
Docker 或 Postgres，页面会写明。

产品会打印下一步。`olivares quickstart` 在设置令牌之后会指出通行密钥登记、
`olivares agent tool detect`、清单、hook PEP 和 `olivares doctor`。
`olivares doctor` 报告 `first-hour-coding-agent`、`first-hour-hook-pep` 和
`first-hour-next-step`。这些检查是可选的。它们不会让健康安装失败。

## 这一小时是什么

安装 → 控制台 → 连接 **一个** 编码智能体 → 在清单中看到它 → 运行 **允许一项、
拒绝另一项** 的受治理会话 → 阅读证据。

本页使用本树已经交付的 **Claude Code hook PEP**（`olivares claude-hook`）。
把官方 CLI 作为会话进程启动是 Community 运行时的接缝。这一小时不复制该驱动。
它不发送模型回合。

`--seed-demo` 不是这一小时。

## 形态 1 — 本地（SQLite，本机）

本容器 **没有 Docker**。PostgreSQL **未运行**。本地形态使用内嵌 SQLite 与
回环 HTTP。冒烟测试重放的就是这一形态。

### 1. 构建并启动

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

冒烟测试使用 `serve --insecure`，这样 `curl` 不必信任自签名证书；它还显式传入
`--listen 127.0.0.1:8443`——这个回环绑定属于冒烟测试，而不是产品的默认值。
交互操作员运行 `olivares quickstart`（启用 TLS、没有默认凭据、设置令牌只能使用
一次）。它的默认监听地址是 `:8443`——**所有网络接口**，因为这是一台服务器
（`cmd/olivares/binddefaults.go`）；传入 `--listen 127.0.0.1:8443` 可将其限制在
本机。2026-09-17 在本机测得：`quickstart --quiet` 在 **3 秒**内打印令牌。

原生快速启动根据设置状态显示以下提示中的一条。
仅在新签发令牌时显示令牌：

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

对于默认的通配绑定，横幅会打印 `https://localhost:8443`，并列出这台
主机应答的其他所有地址——它们用于从另一台机器访问控制台。如果横幅已经滚出屏幕，
`olivares first-boot` 会再次打印控制台地址和首次设置的状态。登记通行密钥前，请
打开 `https://localhost:PORT`：浏览器会拒绝将 IP 地址用作 WebAuthn 依赖方，产品
已在地址提示中说明这一点（`cmd/olivares/consoleaddr.go`）。

### 2. 创建管理员和租户

一条命令即可对正在运行的引擎完成初始化，这也是启动面板打印的那条命令。令牌从标准
输入读取，因此它永远不会出现在进程表中：

```bash
# 粘贴引擎启动时打印的 olst_… 令牌
./bin/olivares auth bootstrap --server http://127.0.0.1:8443 \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` 会登录并保存会话，因此下一条命令已经通过身份验证。2026-09-18 在
干净的数据目录上测得：263 毫秒。

如果你直接针对这些端点编写脚本，通过 API 也能完成同样的事：

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. 连接一个编码智能体并在清单中看到它

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` 不要求额外的管理认证。源、连接器、工作区和密钥的额外
认证由 `admin_step_up` 决定，默认值为 `none`。如何要求 AAL3，请参阅下方
的策略设置。

### 4. 受治理会话：允许 Read，拒绝 Bash

停止步骤 1 的引擎。在同一工作目录中保留步骤 1 和 2 的 `DATA` 与 `TENANT`，
然后执行以下命令。这会写入默认拒绝的策略，并挂载 PEP 后重启引擎：

```bash
# TENANT is the tenant_id returned in step 2.
: "${TENANT:?Set TENANT to the tenant_id from step 2}"
cat > ./hook-pep.json <<JSON
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "$TENANT",
      "require_firm_identity": false,
      "policy": {
        "version": "first-hour/v1",
        "default": "deny",
        "rules": [
          { "tool": "Read", "decision": "allow", "reason": "reads are permitted in the first hour" },
          { "tool": "Bash", "decision": "deny", "reason": "shell execution is blocked in the first hour" }
        ]
      }
    }
  ]
}
JSON
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.json \
  ./bin/olivares serve --insecure --data-dir "$DATA" \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444
```

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. 阅读证据

每个受治理决策向租户账本追加 `hook.tool.allow` 或 `hook.tool.deny`（`modules/sessions/hookpep/claudehookpep.go`）。冒烟测试断言两种记录均存在。

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## 形态 2 — 团队（Postgres 与 Docker)

本容器 **没有 Docker**。PostgreSQL **未在此运行**。不要把下面的命令当作在
本机测过。

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

没有 `olivares_admin` 池时，`POST /v1/setup` 返回
`501 cross_tenant_admin_pool_not_configured`。见
[使用 Docker 部署](/how-to/docker-deployment/)。

## 形态 3 — 混合（本地控制平面，工作站上的智能体）

控制平面保持本地 SQLite。在已有 `claude` 的工作站上运行智能体。官方 CLI
的实时会话（PTY）属于 Community 运行时，不是本页。

## 管理操作的额外认证策略

`admin_step_up` 默认为 `none`：已登录的管理员使用当前会话的认证强度
执行操作。因此，创建源、连接器、工作区和密钥默认不要求 AAL3。认证、权限、
租户隔离和审计仍然适用；API 令牌不能满足此认证要求。

要为这些操作要求新鲜的 AAL3，请在 `https://localhost:PORT` 注册 passkey，
并在该控制台地址完成新的 passkey 额外认证。在 **Settings → Sign-in →
Extra check for administrative actions** 中选择 **Passkey**。对应的 API 是
`PUT /v1/auth/step-up-policy`，请求体为 `{"admin_step_up":"passkey"}`。
管理员证明所选认证方式可用之前，引擎会拒绝提高策略要求。`POST /v1/agents`
不受此额外认证检查约束。


## 3. 从控制台启动 Claude Code 会话

没有推理凭证源时，stream-json 启动是 deny-closed：

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

对于未指定提供方的 `managed_injection` Claude 配置档案，
`OLIVARES_SESSION_RUNTIME_WIF` 或 `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
提供主机的推理凭据。绑定提供方的配置档案使用该提供方的凭据；若无法读取，
则拒绝启动，不回退到主机凭据。`provider_account_home` 配置档案使用获授权的
工具登录，不需要这两个变量。参见
[添加提供方](/zh/how-to/add-a-provider/)。

## OpenCode 状态检查

如果设置期间 OpenCode 的登录状态检查超时或失败，引擎会报告无法读取状态。请重试检查；检查失败不代表工具已退出登录。同一组织和账户的并发检查共享一个原生命令。成功结果最多复用 30 秒，并在登录文件或已安装可执行文件发生变化时刷新。

## 相关页面

- [诚实与限度](/start/honesty-and-limits/)
- [自托管 Olivares AI](/how-to/self-hosting/)
- [运营提供商会话](/how-to/operate-provider-sessions/)
- [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/)
- [使用 Docker 部署](/how-to/docker-deployment/)

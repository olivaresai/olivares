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
把官方 CLI 作为会话进程启动是 D04 接缝（PR #2547）。这一小时不复制该驱动。
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

冒烟测试使用 `serve --insecure`，这样 `curl` 不必信任自签名证书。交互操作员
运行 `olivares quickstart`。2026-09-17 在本机测得：`quickstart --quiet` 在
**3 秒**内打印令牌。

欢迎面板打印：

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

登记通行密钥前，请打开 `https://localhost:PORT`。

### 2. 创建管理员和租户

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

`POST /v1/agents` **不**要求 AAL3。创建源、连接器、工作区和密钥 **要求**。

### 4. 受治理会话：允许 Read，拒绝 Bash

用 `OLIVARES_HOOK_PEP_CONFIG` 和 deny-closed 策略重启。然后：

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
的实时会话（PTY）属于 D04，不是本页。

## AAL3 屏障（仍然成立）

创建源、连接器、工作区和密钥在达到 AAL3 之前会被 **拒绝**。标准安装上
PIV/CAC 返回 **501**。打开 `https://localhost:PORT`，然后到
**Identity → Privileged login**。

## 3. 从控制台启动 Claude Code 会话

没有推理凭证源时，stream-json 启动是 deny-closed：

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

（`cmd/olivares/sessionruntime.go`）。设置
`OLIVARES_SESSION_RUNTIME_WIF` 或 `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
中的 **一个**。自 v26.10 起这不再是唯一路径：在控制台注册凭据并把它绑定到配置
档案。见 [添加提供方并启动代理](/zh/how-to/add-a-provider/) 和
[运营提供商会话](/how-to/operate-provider-sessions/)。

## 相关页面

- [诚实与限度](/start/honesty-and-limits/)
- [自托管 Olivares AI](/how-to/self-hosting/)
- [运营提供商会话](/how-to/operate-provider-sessions/)
- [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/)
- [使用 Docker 部署](/how-to/docker-deployment/)

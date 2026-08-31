---
title: 快速上手
description: >-
  约五分钟内从零开始，得到一张已填充的读/写访问图，以及一个真实的
  Permitted-vs-Observed 漂移结果——先在内置的演示 estate 上跑，再用一个真实的
  pgAudit connector 验证它并非演示。
---

这是体验 Olivares AI *用途*的快速通道：一张你 estate 的**读/写访问图**，以及在它之上的
**Permitted-vs-Observed 漂移**——即某个 agent *被授予*的访问与它*被观测到*实际使用的访问之间的差距。

你会两次到达这个结果，总共约五分钟：

1. **一分钟内，在内置的演示 estate 上**——即时回答“它到底长什么样”的入门路径（合成的观测数据，流经真实的引擎）。
2. **然后在一个真实 connector 上**——同样的图与漂移，这次逐字解析自一份 PostgreSQL **pgAudit**
   日志，以证明这个核心能力跑在真实数据上，而非演示。

下面的每条命令都被 `scripts/quickstart-smoke.sh` 原样执行
（[可复现性](#5-自己复现)）——因此本页面不会悄悄地与二进制文件产生偏差。

这是一条学习路径，而非生产部署。真正的安装（没有默认凭据、一次性的安装 token、TLS）请前往
[自托管](/zh/how-to/self-hosting/)。如需有引导的 UI 演练，参见
[zero-to-graph 教程](/zh/tutorials/zero-to-graph/)。

:::caution[演示模式仅供学习]
`--seed-demo` 会配置一个使用**公开的、源码树中可见密码**的演示管理员以及合成数据，并且它
**拒绝在非回环地址上启动**。切勿将其用于真实安装——真正的首次运行路径是下面的第 3 步，以及
[自托管](/zh/how-to/self-hosting/)。
:::

## 1. 构建单一二进制文件

从仓库的 checkout 开始（需要 Go 1.26+、[Task](https://taskfile.dev) 与
pnpm——`task build` 会在编译前打包 web UI；存储是纯 Go 的 SQLite，
因此无需 C 工具链）：

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` 在 `./bin/olivares` 处产出一个自包含的产物——引擎、嵌入的 web UI 以及第一方
connector 插件。**容器和 Kubernetes 安装都包装的是同一个二进制文件**：一个已发布镜像加上一份 Compose 文件
（[自托管](/zh/how-to/self-hosting/)），或一份扁平 manifest，你用 `kubectl apply -f
deploy/manifests/install.yaml` 应用即可（无需 Helm）。下面你看到的核心能力在这三种方式上完全一致——
仅演示种子数据不同（仅回环，绝不出现在真实安装中）。

## 2. 启动演示 estate（仅回环）

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` 在回环上提供明文 HTTP（用于本地演示没问题；其他情况下**默认开启 TLS**）。你会看到针对那些
开箱即 deny-closed 的接缝（seam）打出的诚实 `WARN` 行（没有评判器、没有 embedder、没有审批门、没有真实数据源），
随后是带凭据的 **DEMO MODE** 横幅：

```text
demo@olivares.local / olivares-demo-estate
```

这个合成 estate 流经的是**真实**的事件总线，与实时的 pgAudit 或 OpenTelemetry collector 完全一样——
只是观测数据是种子数据。

## 3. 到达访问图及其漂移（核心能力）

让服务器继续运行；在第二个终端中登录、解析演示租户，并获取图与它的漂移：

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

演示 estate 恰好返回 **20 个节点和 13 条边**，漂移则呈现 **8 个意外访问**和 **2 个未使用的授权**。每条边都
携带产品的诚实度维度，因此你无需猜测即可读懂每条结论：

- **`mode`** —— `read` / `write` / `readwrite` / `unknown`：R/W 分类，逐字取自信号，绝不推断。
- **`attribution_tier`** —— `firm` / `approximate` / `unknown`：访问被绑定到某个*特定* agent
  或工作负载身份的牢固程度。在演示中，**6 条边为 firm，7 条为 approximate**——例如某 agent 读取了它
  从未被授予的资源（`appdb.public.secrets`，*firm*），相对地是某个共享池身份写入日志
  （`appdb.public.logs`，诚实地标为 *approximate*）。
- **`coverage_tier`** —— `clean` / `lossy` / `opaque` / `mixed`：该*资源*信号的保真度，与归因正交。

:::tip[一项关键的差异化能力]
**Permitted 与 Observed 之间的差异**就是*least-privilege drift*（最小权限漂移）——你希望在审计员或攻击者
发现之前先发现的东西。种子数据证明它是真实的，而非“一切皆漂移”：那 3 条既被授予**又**被观测到的边会调和并从
漂移结果中剔除；只有真正的缺口保留下来（8 个意外访问 + 2 个声明了却从未行使的授权）。而且产品绝不会捏造一个
它无法证明的标签——仅仅是 `approximate` 的归因会如实说明，而非凭空捏造一个 `firm` 的 agent。
:::

同一张图会在 `http://127.0.0.1:8901` 的嵌入式 web UI 中渲染（用演示凭据登录，并切换到
**Demo Estate** 组织）。

进入下一步前先停止演示服务器（`Ctrl-C`）。

## 4. 在真实 connector 上验证它（而非演示）

这个核心能力不是种子数据的魔法：它跑在你的数据源所观测到的任何东西上。这里你将接入
**真实的 pgAudit connector**——与生产安装走的是同一条代码路径——对接一份 PostgreSQL 审计日志，
且**没有演示种子**。

首先，一份小的 `pgAudit` csvlog（三条真实审计行：一个应用的两次读取和一次写入）。在生产中 pgAudit
会将这些写入 Postgres 日志；这里用一个文件来替代那段日志尾部：

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

现在做一次**真实的首次运行**：在没有默认凭据的情况下启动一次，领取一次性的安装 token，
并创建一个租户来挂接 connector。

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Connector 从一份运算符配置文件接入，按值传入，引擎从不持久化。将 pgAudit 指向你租户的日志，并带着该配置
**重启**：

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

启动日志会打印 `ingest: wired source … kind=pgaudit`。在第二个终端中再次登录并读取图——这次的边是
**真正被解析出来的**，而非种子数据：

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

你会得到 **3 条边**——`salesdb.public.customers`（read）、`…orders`（write）、
`…secrets`（read）——每条都带有 `signal_source: pg_audit` 与 `coverage_tier: clean`
（pgAudit 逐字报告 R/W），且漂移会将全部 **3 条标记为意外访问**
（尚未接入任何授权，因此每个被观测到的访问都是漂移）。

:::note[默认即诚实：在接入身份前都是 approximate]
这些真实的边落地为 `attribution_tier: approximate`，而非 `firm`——pgAudit 信号命名的是一个数据库角色/应用，
而不是一个*受治理的 agent*。这就是诚实的默认值：产品不会声称它牢固地把某个访问归因到一个它无法证明的 agent。
你需要接入一个身份源（LDAP/IdP/SPIFFE），把凭据绑定到某个 agent 或工作负载身份，才能换来 `firm`——参见
[接入数据源](/zh/how-to/connect-a-source/)。演示 estate 之所以显示 `firm` 边，正是因为它预先绑定了它的 agent。
:::

:::note[端点的形态]
Permitted-vs-Observed 结果由 `/v1/m/accessmap/drift` 提供（没有 `/diff`）。
`/v1/m/accessmap/*` 路由不属于包含 53 条路径的稳定核心契约；它们以独立的
**beta** 文档发布——见[模块路由参考](/reference/api-beta/)。[API 参考](/reference/api/)
记录稳定核心表面。
:::

## 5. 自己复现

上述一切都端到端地针对真实二进制做了断言：

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

它会启动演示 estate **以及**真实的 pgAudit 路径，运行本页面上确切的命令，并核对数字
（20 个节点 / 13 条边，8 个意外 + 2 个未使用，3 条真实 pgAudit 边）。如果从安装→价值的路径或漂移结果
不再成立，烟雾测试就会失败——这就是让本页面保持诚实的契约。它在挂钟时间上几秒钟即可完成；上面由人工走完的
路径就是文档所述的**五分钟**。

## 下一步

- **真刀真枪地运行它：** getting-started 系列教程端到端地演练每种安装场景——
  [单节点（systemd）](/zh/tutorials/getting-started/single-node/)、
  [Docker Compose](/zh/tutorials/getting-started/docker-compose/)、
  [Kubernetes/Helm](/zh/tutorials/getting-started/kubernetes/) 与
  [air-gapped（隔离网络）](/zh/tutorials/getting-started/air-gapped/)；
  [自托管](/zh/how-to/self-hosting/)是横跨这些场景的决策页面。
- **给它喂真实信号：** [接入数据源](/zh/how-to/connect-a-source/)与
  [connector 目录](/zh/reference/connectors/)——每个数据源观测什么、它诚实的覆盖度等级，以及如何接入身份
  从而让归因变为 `firm`。
- **加固它：** [安全加固](/zh/how-to/security-hardening/)——安全默认值、human-in-the-loop（人工介入）审批，
  以及在运行之前验证一个发布版本。
- **了解边界：** [诚实度与限制](/zh/start/honesty-and-limits/)——今天有哪些已经运行、哪些处于设计阶段，
  以及产品刻意不做哪些事。

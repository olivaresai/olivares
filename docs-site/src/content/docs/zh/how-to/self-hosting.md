---
title: 自托管 Olivares AI
description: >-
  自行运行 Olivares AI — 单一二进制文件、Docker Compose 或 Kubernetes — 采用安全的默认配置：无默认凭据、一次性安装令牌、默认启用
  TLS、没有强制遥测，且控制平面默认不产生出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界，例如对你的模型 API 的调用和你接入的 SIEM/webhook 输出。
---

Olivares AI 是 **自托管优先（self-host-first）** 的。整个产品就是一个内嵌了 Web UI 的静态二进制文件，因此最简单的部署方式就是一个文件；Compose 与
Kubernetes 路径则用于多节点和生产环境。每条路径都共享相同的安全默认配置 — 无默认凭据、一次性安装令牌、默认启用 TLS —，没有强制遥测，控制平面默认也不产生出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、你接入的 SIEM/webhook 输出，以及你配置时使用的外部嵌入提供商。

本指南是部署的 **决策页面** — 一览各个选项及其安全默认配置。各场景的逐步安装说明，请参阅从头到尾走完每条路径的入门教程：
[单节点（systemd）](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[气隙环境（air-gapped）](/tutorials/getting-started/air-gapped/)。若想先对产物做加密验证，请参阅
[验证你下载的内容](/how-to/verify-a-release/)；对于离线站点，请参阅
[在气隙环境中安装](/how-to/air-gap-install/)。

## 安全默认配置（所有路径）

| 默认配置 | 行为 |
|---|---|
| **凭据** | 无。首次启动会打印一个 **一次性、单次使用的安装令牌**（`olst_…`）；你用它创建第一个管理员。 |
| **TLS** | 默认启用。`--insecure`（明文）仅用于本地开发。 |
| **绑定** | 默认绑定**所有网络接口**（`:8443`、`:8444`）——这是一台服务器。传入 `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444` 可将其限制在本机。 |
| **许可证** | 在开放（AGPL）二进制文件中，许可证采用 **离线**校验（Ed25519），且仅用于证明——它绝不对开放产品设门槛或使其降级，这一点不会改变。商业附加组件是按已付费期限享有的权利，以**订阅方式访问企业版仓库**来交付（SUSE/Novell 模式）：获取这些组件并接收其更新（包括安全更新）均须具备该权利。服务于气隙环境的方式与 SUSE 相同：使用仍受该权利约束的本地镜像。 |
| **遥测回传（Telemetry-home）** | 关闭。引擎在启动时不发起任何强制的出站调用。 |

## 选项 1 — 单一二进制文件

构建这个唯一的静态产物（纯 Go 的 SQLite 存储，因此无需 C 工具链）并运行它：

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

首次启动时，引擎会打印安装横幅：

```text
=== FIRST-BOOT SETUP ===
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

创建第一个管理员，然后登录：

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

数据目录保存着 SQLite 数据库、审计签名密钥和 TLS 材料 — 请备份并妥善保护它。

### 自定义数据目录（`layout: custom`）

默认原生布局是 `/var/lib/olivares`。签名的服务适配器（`install.sh --data-dir`、
`scripts/install-service.sh`）按 **形状** 接受 **自定义** 数据目录，而不是按
允许列表。所有权清单记录 `"layout": "custom"`（`CHANGELOG.md` `[26.9.0]`
Added；`INSTALL.md`）。

SDD 04 §6：每个可配置字段声明 owner、schema、accepted sources 和 validator。
此处适配器拥有专用目录；操作者拥有父目录。以下是适配器自己的拒绝字符串
（`scripts/install-service.sh`）：

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state…; choose a real directory` |
| Path under `/tmp` or `/var/tmp` on systemd older than 235 | `data directory $data_dir is under /tmp or /var/tmp and this host runs systemd $running: creating a BindPaths= destination inside the private /tmp needs systemd 235 or later…` |
| A BindPaths= path contains `:` | `$2 $1 contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it` |

`install-agentops.sh` 对 `OLIVARES_DATA_DIR` 使用同一条两级规则：
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`。它尊重 `OLIVARES_DATA_DIR` 和显式选择的
`OLIVARES_WORKSPACE_DIR`。

`/home`、`/root` 或 `/run/user` 下的路径以 `ProtectHome=tmpfs` 和仅针对该目录的
`BindPaths=` 呈现。`/tmp` 或 `/var/tmp` 下的路径保留 `PrivateTmp=true`，并只为
该目录获得 `BindPaths=`（`scripts/install-service.sh` 中的 `sandbox_access`）。

仅当其索引路径上的 unit 用该目录执行引擎，或 preserve 已在服务配置旁留下卸载
证人时，`olivares uninstall` 才接受该自定义目录。用 `olivares doctor` 诊断记录的
AgentOps 布局 — 见
[故障排查](/how-to/troubleshooting/#agentops-layout-check)。

软件包安装的默认仍是 `/var/lib/olivares`。见
[从软件包安装](/how-to/install-from-packages/)。macOS 见
[使用 Homebrew 安装](/how-to/install-from-homebrew/)。

## 选项 2 — Docker Compose（单节点，SQLite）

仓库提供了一套 Compose 栈：

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

若要使用多租户的 Postgres 后端，请设置好密码并叠加 Postgres override：

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[容器默认在容器内部绑定]
容器的默认命令在 *容器内部* 绑定 `0.0.0.0`，以便你用自己的入口网关（ingress）置于其前；Compose 栈则将主机端口映射到 `127.0.0.1`。
没有裸 `docker run` 的方案 — 请使用 Compose（或 Helm chart），以便数据卷、端口和首次启动流程都被正确连接。
:::

## 选项 3 — Kubernetes（Helm）

`deploy/helm/olivares` 中的 Helm chart 将控制平面（control plane）部署为一个 **核心 StatefulSet**
（单写入者；其数据目录保存审计签名密钥和 TLS 材料），并且，对于分布式拓扑，还部署一个
**采集器 DaemonSet（collectors DaemonSet）**，它通过 **gRPC + mTLS** 将观测数据推送给核心。
引擎 v26.9.0 release 不会将 chart 发布到 OCI registry；独立的 `chart-v*` tag 尚未运行该 workflow。
请从 checkout 安装经过审查的源 chart，并按 digest 固定已发布的容器镜像。

```bash
helm install olivares \
  deploy/helm/olivares \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> 未来发布 chart 时，`release-chart.yml` 会用 cosign 签署 OCI manifest，且不产生 GPG
> `.prov` layer。届时须按 digest 单独验证；当前源安装不会被描述成已签名的 OCI 下载。
> 参见 `deploy/helm/README.md`。

chart 会从 Docker Hub（`docker.io/olivaresai/olivares`）拉取容器镜像；同一镜像也位于
`ghcr.io/olivaresai/olivares`，按 digest 完全相同；如果 Docker Hub 的**匿名**拉取限速造成困扰，
可将 `image.repository` 指向那里（ghcr.io 对公共镜像不限速）。在独立发布之前，chart 来自
`deploy/helm/olivares`。

始终 **按 digest** 部署，绝不使用可变的标签。对于完全断网的集群，请先镜像该 bundle — 参阅 [气隙安装](/how-to/air-gap-install/)。

## 选择拓扑

| 拓扑 | 适用场景 | 存储 | 事件总线 |
|---|---|---|---|
| **单一二进制文件** | 单节点、实验室、小型 estate、气隙 | SQLite（内嵌） | 进程内 |
| **分布式** | 多主机、扩容、多租户 | Postgres + RLS | 进程内 + **NATS 桥接**（`OLIVARES_BUS_CONFIG`；跨节点投递诚实地为至多一次） |
| **气隙** | 不允许出站流量 | SQLite 或 Postgres | 进程内（边界内可选 NATS 桥接） |

**数据平面（采集器）始终运行在你自己的基础设施上** — 控制平面是唯一由你选择托管位置的部分。
[架构概述](/explanation/architecture/overview/)阐述了其中的权衡取舍。

## 连接真实数据源

全新安装的 estate 是空的。接入真实数据源（Postgres pgAudit、CloudTrail、来自 agent 的 OpenTelemetry、eBPF），使访问图（access map）填充起来 — 参阅
[连接数据源](/how-to/connect-a-source/)和
[连接 Claude Code](/how-to/connect-claude-code/)。关于配置面，请参阅 [配置参考](/reference/configuration/)。

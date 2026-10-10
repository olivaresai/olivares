---
title: 自托管 Olivares AI
description: >-
  自行运行 Olivares AI — 单一二进制文件、Docker Compose 或 Kubernetes — 采用安全的默认配置：无默认凭据、一次性安装令牌、默认启用
  TLS、没有强制遥测，且控制平面默认不产生出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界，例如对你的模型 API 的调用和你接入的 SIEM/webhook 输出。
---

> 部署包通过 Business 渠道提供；此处未验证其发布状态。使用本地 chart 前，请按渠道说明验证包及其发布者。清单示例使用 Business 提供的 `business-install.yaml`。隔离环境安装需要 Enterprise。


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

下一个版本是 <!-- release -->`0.1`<!-- /release -->，其 GitHub 发行尚未发布。以下命令描述计划中的产物。发布前请从源码构建，发布后也应在使用前验证每个产物。观测到的发布状态记录在 <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->。

Olivares AI 是 **自托管优先（self-host-first）** 的。整个产品就是一个内嵌了 Web UI 的静态二进制文件，因此最简单的部署方式就是一个文件；Compose 与
Kubernetes 路径则用于多节点和生产环境。每条路径都共享相同的安全默认配置 — 无默认凭据、一次性安装令牌、默认启用 TLS —，没有强制遥测，控制平面默认也不产生出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、你接入的 SIEM/webhook 输出，以及你配置时使用的外部嵌入提供商。

本指南是部署的 **决策页面** — 一览各个选项及其安全默认配置。各场景的逐步安装说明，请参阅从头到尾走完每条路径的入门教程：
[单节点（systemd）](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[气隙环境（air-gapped）](/tutorials/getting-started/air-gapped/)。若想先对产物做加密验证，请参阅
[验证你下载的内容](/how-to/verify-a-release/)；对于离线站点，请参阅
[在气隙环境中安装](/how-to/air-gap-install/)。

**NATS 事件传递：** Core NATS 桥接和 NATS JetStream 需要 Business Identity & Scale。Community 在进程内传递事件。

## 安全默认配置（所有路径）

| 默认配置 | 行为 |
|---|---|
| **凭据** | 无。首次启动会打印一个 **一次性、单次使用的安装令牌**（`olst_…`）；你用它创建第一个管理员。 |
| **TLS** | 默认启用。`--insecure`（明文）仅用于本地开发。 |
| **绑定** | 默认绑定**所有网络接口**（`:8443`、`:8444`）——这是一台服务器。传入 `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444` 可将其限制在本机。 |
| **许可证** | 在开放（AGPL）二进制文件中，许可证采用 **离线**校验（Ed25519），且仅用于证明——它绝不对开放产品设门槛或使其降级，这一点不会改变。Business 在一个订阅中包含 Regulated Operations、AI Runtime Security、Compliance Packs 和 Identity & Scale。客户可以启用或禁用每个产品系列。Business 和 Enterprise 以二进制文件交付；付费版本的源代码保持私有。 |
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
curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/auth/login \
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
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): choose a real directory` |

`install-agentops.sh` 对 `OLIVARES_DATA_DIR` 使用同一条两级规则：
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`。它尊重 `OLIVARES_DATA_DIR` 和显式选择的
`OLIVARES_WORKSPACE_DIR`。

该单元把目录渲染为 `ReadWritePaths=<data-dir>`。它设置 `ProtectHome=false` 和
`PrivateTmp=false`，因此 `/home`、`/root`、`/run/user`、`/tmp` 或 `/var/tmp` 下的目录
无需额外挂载。安装程序会提示 `/tmp` 和 `/var/tmp` 可能在启动时或按定时器被清空。

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

部署包通过 Business 渠道提供；此处未验证其发布状态。使用本地 chart 前，请按渠道说明验证包及其发布者。清单示例使用 Business 提供的 `business-install.yaml`。隔离环境安装需要 Enterprise。

```bash
# Set these inputs from the authenticated Business channel after verification.
helm upgrade --install olivares "$BUSINESS_CHART_PACKAGE" \
  --set image.repository="$BUSINESS_IMAGE_REPOSITORY" \
  --set image.digest="$BUSINESS_IMAGE_DIGEST"
```

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

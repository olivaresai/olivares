---
title: "在 Olivares 下运行 Claude Code（联合部署）"
description: "在一台 Linux 主机上联合部署 Olivares 控制平面与 Claude Code 运行时，默认安全，使引擎能够启动、治理并拆除共享同一工作区的 Claude Code 会话——共四种拓扑。"
---

这是 Anthropic 优先这一叙事中的 **Operate（操作）** 那一半：不仅*观察*和*治理* Claude Code，
还要**指挥**它。控制平面启动一个真实的 `claude` 进程，将其 I/O 桥接为受治理的流，将每一次生命周期
转换锚定在审计账本中，并将其拆除——在一个共享工作区之上，从 API/CLI（以及后续的门户）进行，
**无需 SSH**。本页在一台 Linux 主机上以四种拓扑联合部署这两半，默认安全。

关于*协作式观察*路径（OTLP 遥测 → access map），见
[连接 Claude Code](/how-to/connect-claude-code/)；关于*治理*路径（将 PreToolUse 钩子用作 PEP），
见 [govern-claude-code 示例](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code)。
本页讲的是**联合部署**：让两个运行时一起跑起来。

:::note[治理究竟如何抵达会话]
一个会话之所以受治理，是因为**引擎掌握着 `claude` 的 stdin/stdout**——即 `stream-json` 无头传输。
引擎将 `claude` 作为子进程派生（原生 procRunner），并桥接每一帧 NDJSON。这只有在引擎与 `claude`
共享同一执行上下文（同一主机，或同一容器）时才成立。推荐的拓扑正是出于这个原因将二者放在一起；
混合拓扑及其诚实的约束见下文。
:::

## 开始前的两条原则

1. **可选启用（Opt-in）。** Olivares 基础镜像是 distroless 的，且**不携带 `claude`**。
   Operate-Claude-Code 层是一个*独立的*工件——一个组合镜像
   （`Dockerfile.agentops`）或一个原生安装附加项。如果你不运行受治理的 Claude Code，就永远不会拉取它，
   它额外的攻击面也永远不会触及你的控制平面。
2. **官方来源，绝不二次分发。** Anthropic 的条款不允许二次分发 `claude` 二进制文件，因此我们
   **从 Anthropic 官方的、经 GPG 签名的来源**在构建/首次运行时安装它（签名的 apt/dnf/apk 仓库），
   做了固定（pin）并禁用了自动更新器。我们不附带任何第三方二进制文件。你也可以
   **自带（BYO）** `claude` 并让引擎指向它。

## 四种拓扑一览

| # | Olivares | Claude Code | 引擎如何指挥它 | 状态 |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **同一容器**（组合镜像），procRunner 子进程 | **推荐**（与 2 相同的受治理路径） |
| 2 | 原生 | 原生 | 同一主机（systemd），procRunner 子进程 | **推荐**，已做端到端冒烟测试 |
| 3 | Docker | 原生（主机） | 跨命名空间——按现状无法治理 | 改为同址部署（见下文） |
| 4 | 原生 | Docker（每会话） | 通过 Docker API 的每会话容器 | 后续工作（已记录） |

两种**同址（co-located）**拓扑（1、2）是安全的默认选项。拓扑 2（原生）由
[`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh)
做了端到端测试；拓扑 1 复用**同一条**受治理的 procRunner 路径（组合镜像的构建/运行尚未接入自动化测试）。
拓扑 3 和 4 要求治理者与被治理者位于*不同*的容器中；跨该边界桥接 stdio 需要 Docker-API 访问权限
（这是引擎默认有意**不**获取的特权）。它们诚实的路径在
[混合拓扑](#混合拓扑3-和-4) 中阐明。

---

## 拓扑 1 — 两者都在 Docker 中（推荐）

一个经过加固的容器同时运行引擎**和** `claude`；一个工作区卷作为共享的工作目录。仅回环、非 root、
只读根文件系统——与基础 compose 完全相同的姿态，外加被指挥的运行时。

### 构建组合镜像

`claude` 在构建时从 Anthropic 的**签名 apt 仓库**安装，签名密钥指纹被固定
（`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`）且禁用自动更新。先按摘要固定引擎基础镜像并验证它：

```sh
# verify the engine image you build FROM (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --build-arg CLAUDE_CHANNEL=stable \
  -t olivares-agentops:26.8.0 .
```

也可改用 `--build-arg CLAUDE_INSTALL=byo` 自带 `claude`（镜像不携带 `claude`；在运行时挂载你自己的，
并设置 `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`）。

### 启动它

```sh
export OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.agentops.yml up -d
```

该 override 只改动 Operate 所需的部分：组合镜像、四个可写卷
（引擎数据、**工作区**、claude 的 `~/.claude` 主目录、短期推理令牌），以及会话运行时环境变量。
其余一切——绑定到 `127.0.0.1` 的端口、uid 65532、`read_only` 根、`cap_drop: ALL`、
`no-new-privileges`——都从基础配置继承。

:::caution[首个受治理会话需要一份推理凭据]
凭据来源是**默认关闭（deny-closed）**的：一次 `stream-json` 启动会从
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`（`/run/olivares/session-token`，位于 `olivares-runtime` 卷上）
读取一个*短期* bearer 令牌并将其丢弃——存储下来的只有一个非敏感的 `credential_id`。
让你的 WIF/SPIFFE/OIDC 刷新器指向该卷。在令牌就位之前，`stream-json` 启动会以**关闭**态失败——
引擎仍在运行，并在其他方面可被治理；接入认证是你刻意为之的一步。（实时的进程内令牌交换另行接入。）
:::

---

## 拓扑 2 — 两者都原生（无 Docker）

引擎与 `claude` 都在主机上；systemd 运行引擎，由引擎指挥 `claude`。工作区位于
`/var/lib/olivares/workspaces`。

### 一条命令

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

它会自动检测原生拓扑，安装**经过验证的**引擎二进制文件（受 cosign 关卡控制的 `install.sh`），
从签名的 apt/dnf/apk 仓库安装 `claude`（带密钥指纹验证——或用 `OLIVARES_CLAUDE_INSTALL=byo` 跳过），
创建无登录权限的 `olivares` 服务用户和工作区目录，并放入经过加固的 systemd override + 环境变量示例。
它**不会**自动启动治理平面——是否运行由你明确决定。

### 安装程序接入了什么（以及为什么）

- `packaging/systemd/olivares.service.d/agentops.conf` —— 一个 drop-in，为被指挥的 `claude`
  提供一个可写的 `HOME` 用于 `~/.claude`（保留在 `/var/lib/olivares` 之下，因此 `ProtectHome=true`
  仍保护真实用户），确保工作区目录存在，并仅放开**一项**沙箱属性：`MemoryDenyWriteExecute`
  （`claude` 运行时会做 JIT 编译，需要 W→X 内存）。基础单元中的所有其他加固指令依然有效。
- `/etc/olivares/agentops.env` —— 会话运行时配置（令牌文件、TTL、可选的网关基础 URL、
  可选的自带 `claude` 路径）。

随后，刻意地：

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # loopback-only by default
```

:::note[为什么没有单独的 `claude` 服务]
一个长时间运行的 `claude` 守护进程会让它的 stdin/stdout 脱离引擎的掌控——而受治理的传输*正是* stdio。
因此引擎亲自启动并掌握 `claude` 进程；那个"运行时单元"就是引擎自身的服务，由 drop-in 为 Operate
角色而配置。
:::

---

## 启动首个受治理会话

在任一同址拓扑中步骤相同。对 CLI 进行认证，注册共享工作区，启动：

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

每一次转换（`created → launched → … → stopped`）都被**锚定在签名的审计账本中**
（`olivares agent session events <run-ref>`）；工作区文件 API
（`olivares agent workspace files|get|put|…`）受 jail 限制并被审计。这一切的可复现性契约是
[`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh)，
它对一个隔离的伪造 `claude` 拉起原生联合部署，并断言会话端到端可被治理。

:::note[本次发布仅 `--isolation native` 可用]
`--isolation container` 与 `--isolation sandbox` 是**前向兼容的接缝值，尚未接入**
（每会话容器 Runner 是 [拓扑 4](#拓扑-4--olivares-原生claude-在每会话容器中) 中记录的后续工作）。
原生 runner 会**拒绝**容器/沙箱启动（给出明确的错误），而不是悄悄地在缺少你所要求的隔离的情况下运行
`claude`。请使用 `native`——在组合镜像 / systemd 联合部署之下，那就是引擎自身经过加固的容器/主机边界。
:::

:::caution[`bypassPermissions` 应处于治理之后]
以宽松的 `--permission-mode`（`bypassPermissions`、`dontAsk`）无头运行 `claude`，正是你需要治理平面的时刻。
引擎的白名单环境绝不会向智能体泄露 `OLIVARES_*`/`ANTHROPIC_*` 密钥，而 PreToolUse PEP / 预算 / kill-switch
决定会话实际可以做什么。
:::

---

## 混合拓扑（3 和 4）

这两种拓扑将治理者与被治理者跨容器边界拆分开。要清醒地认识其代价。

### 拓扑 3 — Olivares 在 Docker 中，Claude 在主机上

**不存在干净的受治理路径**：容器化的引擎无法掌握主机命名空间中某个进程的 stdio，而受治理的传输是 stdio。
要触及主机上的 `claude` 就得共享主机 PID 命名空间并把挂载映射进引擎容器——这是一次范围很大、刻意为之的
去隔离，违背了把引擎封装起来的初衷。**请改为同址部署**：把两者都放进组合镜像（那*就是*拓扑 1），
或都原生运行（拓扑 2）。这是一个真实的限制，是被明说出来而非被掩盖的。

### 拓扑 4 — Olivares 原生，Claude 在每会话容器中

这是**每会话全新容器隔离**的天然归宿：每个会话都获得一个全新的、经过加固的 `claude` 容器
（工作区 bind 挂载、只读根、非 root、cap-drop），由引擎通过 Docker API 创建并拆除，stdio 经由
Docker attach/hijack 桥接。数据模型接缝已经**建模**了它（`--isolation container` 是一个有效值，
而它将要消费的执行器挂载原语已经交付）——但其背后的 runner 尚未接入，因此原生 runner 今天会拒绝该值
（见上文的提示）。

**这是一项已记录的后续工作，并未在本次发布中交付。** 驱动同级容器意味着授予引擎 Docker-API 访问权限
（理想情况下通过一个最小权限的 socket 代理）——本次发布有意回避的一个信任面，转而采用无 socket 的组合镜像。
选择这一拓扑就是选择更强的治理者/被治理者隔离，*代价是*那项 Docker-API 授权；它将在现有的
`isolation=container` 接缝之后到来。在那之前，安全的默认选项是同址部署。

---

## 安全姿态（所有拓扑）

- **默认仅回环。** 主机端口仅在 `127.0.0.1` 上发布。在容器中，引擎在容器*内部*监听 `0.0.0.0`，
  因此**主机端口映射才是暴露边界**——在没有你自己的 TLS 终结认证代理的情况下，绝不要把它发布到非回环的
  主机地址上。原生/systemd 的默认绑定是回环。要刻意地暴露。
- **非 root，最小权限。** uid/gid 65532、只读根文件系统、`cap_drop: ALL`、`no-new-privileges`（Docker）/
  完整的 `Protect*`/`Restrict*` 集合，减去那一项已记录的 W^X 放宽（systemd）。
- **最小数据、白名单环境。** 子进程 `claude` 仅继承一个明确的白名单（PATH、HOME、locale……）外加内存中的
  推理令牌——**没有** `OLIVARES_*` 签名密钥，**没有**可能遮蔽所铸造凭据的环境态 `ANTHROPIC_*`/`CLAUDE_CODE_*`。
- **经过验证的供应链。** 引擎经 cosign 签名（验证它 / 按摘要固定）；`claude` 从 Anthropic 的签名仓库安装，
  密钥指纹被固定。安装程序**拒绝运行未经验证的引擎**，除非你显式选择退出。
- **锚定的审计。** 每一次生命周期转换和每一次工作区变更都通过 `PayloadHash` 被封存在哈希链式（hash-chained）、
  签名的账本中——文件的字节与帧的内容从不被持久化。

## 另见

- [连接 Claude Code](/how-to/connect-claude-code/) — 协作式观察路径。
- [安全与加固](/how-to/security-hardening/) — 引擎的基线姿态。
- [验证一次发布](/how-to/verify-a-release/) — cosign / SBOM / SLSA 验证。
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — 安装矩阵，包括本次联合部署。

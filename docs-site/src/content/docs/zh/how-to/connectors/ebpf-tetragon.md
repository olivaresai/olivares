---
title: "eBPF / Tetragon（内核兜底）"
description: >-
  接通访问图中非合作的那一半：Tetragon 在 agent 控制之外捕获内核文件与网络事件，
  连接器将其 JSON 导出转化为诚实近似的访问边 —— 外加一个可选启用的反规避检测器。
sidebar:
  order: 3
---

`ebpf` source 是 R/RW 图中**反规避的那一半**。合作路径看到的是 agent 所*上报*的，
而它看到的是内核*实际所做*的 —— 文件读/写以及出站连接 —— 即便某个 agent
关闭了自身的遥测也无妨，因为它运行在 **agent 的控制之外**。

两项设计决策定义了它，而二者皆是安全态势本身：

- **它自身不加载 eBPF 程序。** 由 [Tetragon](https://tetragon.io)
  完成内核捕获，作为一个独立的、加固过的服务部署，持有 `CAP_BPF` + `CAP_PERFMON`。
  连接器是 Tetragon JSON 事件导出（一个共享文件/FIFO，权限 `0600`，或 stdin）的
  **零能力（zero-capability）、只读消费者**。
- **它对 TLS 主体与载荷是盲的。** 它观测访问关系 —— 从不观测内容。

该仓库在 `connectors/ebpf/deploy/` 下附带参考部署：
一个加固过的 Tetragon DaemonSet、两条 TracingPolicy（文件访问、网络），
以及面向单主机的 Compose 变体。

## 它发出什么

| 字段 | 取值 |
|---|---|
| 信号来源 | `ebpf` |
| 模式 | 文件 `read` / `write`，网络连接边 |
| 发起方 | 一个**运行时身份**（进程/容器）—— 种类为 `identity`，从不是已解析的 agent |
| 置信度 | **始终为 `approximate`** —— 见下文 |
| 覆盖层级 | 内核兜底 |

这个 `approximate` 是精确的，而非谦辞：那次*访问*是内核地面真值（系统调用确实发生了）；
内核给不出的是那个 *agent* —— 它知道进程和 cgroup，却不知道那是哪个受治理的 agent。
当某个身份源把运行时身份绑定到 agent 时，访问图模块会提升归属等级。

## 1. 部署 Tetragon（传感器）

在 Kubernetes 上，应用附带的 DaemonSet 与 TracingPolicy：

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon 将其 JSON 导出写入共享卷
（`/var/run/olivares/tetragon.log`）；连接器从另一侧读取它。
在单主机上，`connectors/ebpf/deploy/docker-compose.yaml` 是去掉 Kubernetes
的相同拆分方式。完整架构与加固说明见 `connectors/ebpf/deploy/README.md`。

## 2. 声明 source

```json
{
  "sources": [{
    "name": "node-kernel-backstop",
    "kind": "ebpf",
    "tenant": "<tenant-id>",
    "config": {
      "events_path": "/var/run/olivares/tetragon.log",
      "detect_evasion": "true"
    }
  }]
}
```

| 键 | 默认值 | 含义 |
|---|---|---|
| `events_path` | `-`（stdin） | Tetragon JSON 事件流 —— 文件、FIFO 或 stdin |
| `follow` | `true` | 随流增长持续读取 |
| `detect_evasion` | `false` | 可选启用：标记某个已知 agent 进程，其合作遥测陷入静默，而内核仍看到它在行动 |
| `evasion_window` | `5m` | 在标记某个缺失的合作连接之前的宽限期 |
| `agent_signatures` | `claude,claude-code` | 被检测器归类为合作 agent 的可执行文件名 |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | 检测器据以关联其连接的合作遥测端点 |

连接器消费 Tetragon 的 `ProcessKprobe` 事件（文件操作与网络连接）和
`ProcessExit`（检测器状态）；`ProcessExec` 用于归属上下文，从不作为一条边发出。

## 3. 你将在控制台中看到什么

内核边以归属到运行时身份的方式加入访问图，始终标记为 `approximate`。
检测器的输出作为发现项落在 **Security** 中 ——
一个停止发出遥测、而内核仍看到其活动的会话，正是本 source 存在的原因：

<img class="light:sl-hidden" src="/console/security-dark.png" alt="Security 视图列出来自 estate 各侦测型 source 的发现项。" />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Security 视图列出来自 estate 各侦测型 source 的发现项。" />

## 诚实的局限

- **它端到端的归属深度仍在验证之中。** 合作路径与存储原生审计是已验证的高保真信号；
  请把内核兜底当作抬高下限者，而非已完工的主信号源
  （[诚实与局限](/zh/start/honesty-and-limits/)）。
- **Tetragon 的范围即其 TracingPolicy。** 附带的策略覆盖文件访问与网络连接；
  它们不追踪的内容在导出中并不存在。
- **进程 ≠ agent。** 没有身份绑定，每条内核边都保持 `approximate` ——
  这是设计使然，而非意外。

## 相关

- [连接 Claude Code](/zh/how-to/connect-claude-code/) —— 本 source 所兜底的合作那一半。
- [SSO/SCIM 与身份源](/zh/how-to/connectors/sso-scim-identity/) —— 归属如何被提升。
- [安全加固](/zh/how-to/security-hardening/) —— 兜底在防御态势中的位置。

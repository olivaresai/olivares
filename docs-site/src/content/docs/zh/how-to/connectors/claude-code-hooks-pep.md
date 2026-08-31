---
title: "Claude Code hooks 与强制执行（PEP）"
description: >-
  Claude Code 连接器的治理部分：hooks 默认仅被观测，以及一个可选启用的策略执行点，
  它以 deny 或 ask 应答 PreToolUse / PermissionRequest hooks —— 每一次拦截都被记录为一条发现项。
sidebar:
  order: 5
---

[连接 Claude Code](/zh/how-to/connect-claude-code/) 接通了*观测*那一半 ——
OTLP 遥测进、访问边出。本页讲的是**治理那一半**：Claude Code 的 **hooks**
将工具决策上报给连接器，而一个可选启用的**策略执行点（PEP）**把这条通道变成一道关卡 ——
连接器以 `deny` 或 `ask` 的 `permissionDecision` 应答匹配的 `PreToolUse` /
`PermissionRequest` hook，并将每一次拦截记录为一条发现项。

默认行为刻意是**读优先**的：在未配置任何强制执行策略时，hooks 仅*被观测，从不被拦截*。
强制执行是一种具名的、显式的可选启用，而无效的策略会**在启动时失败** ——
连接器不会在静默中处于未治理状态运行。

## hook 通道如何工作

连接器的 OTLP/HTTP 接收端（默认环回 `127.0.0.1:4318`）同时在 `hook_path`
（默认 **`/hooks`**）上提供 hook 端点。在开发者机器上，Claude Code 的 hook
配置会将其 hook 事件 POST 到该环回端点 —— 确切的 hook 设置语法属于 Claude Code
自己的文档；本产品所拥有的是接收端以及下文的策略。

针对同一次工具调用的 hook 事件与 OTLP 遥测会被**关联**起来
（`correlation_window`，默认 5s，会让一侧等待另一侧），
这样一个被拦截的动作与其遥测会作为一段连贯的记录落地，而非两条互不相关的记录。
一个持续触发 hook 但在 `silence_threshold`（默认 2m）之外保持 OTLP 静默的会话，
会被标记为遥测缺口 —— 即反规避信号。

## 开启强制执行

在 source 的配置（`OLIVARES_SOURCES_CONFIG`）中添加一条 `enforcement` 策略：

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

规则按工具名和/或资源种类与访问模式进行匹配；决策为 `deny` 或 `ask`
（升级到会话中的人工处理）。匹配的 `PreToolUse` / `PermissionRequest` hook
会拿到这个决策作为 Claude Code 的 `permissionDecision`；其余一切以被观测状态放行。
每一次拦截都被记录为一条**发现项**，因此强制执行的轨迹是可查询的，而非口耳相传。

:::note[紧急停止凌驾于一切之上]
如果整个 estate（或特定 agent）处于[紧急停止](/zh/how-to/cookbook/kill-switch-drill/)之下，
无论本策略如何，`claude.tool.use` 都会在治理层被终止 ——
停止关卡会在任何按工具的规则之前被检查，并且失败时闭合（fail closed）。
:::

## 整队态势：托管设置，被观测

在 hook 处的强制执行只是其中一层。整队级别的那一层是 Claude Code 的**托管设置**文件，
`managed-settings` source 以只读方式观测它：

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| 键 | 默认值 | 含义 |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json`（Linux） | 主机上活跃的托管设置文件（macOS：`/Library/Application Support/ClaudeCode/…`） |
| `scope` | 操作系统主机名 | 归属范围（主机 id / 发行版名称） |
| `expected_policy` | — | 可选的编写意图；设置后，连接器会报告**漂移**（许可策略 vs 观测到的配置）。留空 = 仅观测 |

`claude` source 上相关的可选启用观测器：`managed_mcp_path`（建模托管 MCP
允许列表的求值顺序，并标记仅按名称的允许条目）和 `sandbox_path`
（针对沙箱锁定设置的态势发现项）—— 两者皆为只读，在指向某个文件之前都处于关闭状态。

## 你将在控制台中看到什么

**Claude Code governance** 是编写与真值闭环的界面：你所意图的策略、各主机实际承载的配置，
以及二者之间的漂移。拦截项与遥测缺口发现项落在 **Security** 中；会话本身仍在
**Sessions** 中可见：

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="Claude Code 治理视图 —— 策略编写与整队态势集于一处。" />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="Claude Code 治理视图 —— 策略编写与整队态势集于一处。" />

## 诚实的局限

- **PEP 只能拦截 hooks 所上报的内容。** 一台未配置 hooks 的主机不会被拦截 ——
  将整队与[托管设置观测器](#整队态势托管设置被观测)配对，使缺失可见；
  并与[内核兜底](/zh/how-to/connectors/ebpf-tetragon/)配对，使其不至失明。
- **`ask` 把决定权交给会话中的人** —— 它是摩擦，不是锁。`deny` 才是锁。
- **子进程不在此处的范围内**（hooks 只为 Claude Code 自身的工具调用触发）；
  关于遥测环境变量能触及和不能触及什么，参见[企业 OTel 页面](/zh/how-to/claude-code-enterprise-otel/)。

## 相关

- [连接 Claude Code](/zh/how-to/connect-claude-code/) —— 观测的那一半。
- [面向 Claude Code 的企业 OTel](/zh/how-to/claude-code-enterprise-otel/) ——
  整队遥测、标签、追踪。
- [治理与审批](/zh/how-to/govern-and-approve/) —— PEP 所接入的授权模型。

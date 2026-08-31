---
title: "为 Claude Code 配置企业级 OpenTelemetry"
description: >-
  面向 Claude Code 机群的推荐企业级遥测姿态：通过受管设置（managed-settings）的 env
  开启经认可的 OTel 导出，借助 OTEL_RESOURCE_ATTRIBUTES 设置成为 FinOps 维度的运维标签，
  用于子代理层级的 tracing beta，以及隐私开关——并逐一说明它们各自带来的责任。
---

Claude Code 的 OpenTelemetry 导出是受治理机群的**经认可观测路径**：它不受套餐门槛限制、
携带按会话归因的遥测，并且受管设置层可以为每一位开发者将其开启——而无需代理（proxy）任何流量。
本页是在 [连接 Claude Code](/zh/how-to/connect-claude-code/) 之上的*企业级*配置：机群范围内该设置什么、
每个开关为你换来了什么，以及它带来何种责任。下文中的键名与语义已于 2026-06-10 对照 Claude Code
自身的文档（客户端 2.1.17x）进行核验；在编码新键之前请到那里重新核对——它们演进得很快。

:::note[受管 env 仅治理 Claude Code]
受管的 `env` 块配置的是 **Claude Code 进程**。OTEL_* 变量**不会**传播到子进程（Bash 命令、hook、
MCP 服务器）；在 tracing 处于活动状态时，只有 `TRACEPARENT` 会被 shell 子进程继承。请单独规划
子进程可观测性（内核/eBPF 兜底）。
:::

## 你能得到什么

| 开关 | 它换来什么 | 它带来的责任 |
|---|---|---|
| 受管遥测 `env` | 每个会话都将 OTLP 导出到你的采集器——这种观测能在开发者自己的配置之外存续 | 无——默认即结构化遥测 |
| `OTEL_RESOURCE_ATTRIBUTES` | 在**每个指标数据点和事件记录**上附加组织自定义标签（团队、项目、成本中心）；control plane 将它们路由进 FinOps 支出维度 | 保持标签值非敏感；连接器会对其做白名单过滤并清洗 |
| Tracing beta | `claude_code.llm_request` / `claude_code.tool` span 携带 `agent_id` / `parent_agent_id`——access map 中的**每实例子代理层级** | Beta 表面：升级时需核验 |
| `OTEL_LOG_TOOL_DETAILS=1` | 工具事件上的 `tool_parameters`——包括在拒绝某次工具决策时**哪条命令被拒绝** | 工具输入会离开主机：这是一项你必须承担的驻留/脱敏责任 |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint`（cli / sdk-ts / claude-vscode …）——是哪个表面启动了每个会话 | 无（低基数标签） |

## 第 1 步——从受管层开启导出

在你的受管设置策略中编写遥测 `env`（`managed-settings` 连接器的 `TelemetryEnv` 辅助函数渲染的
正是这种姿态）：启用遥测、把 OTLP 导出器指向 control-plane 采集器，并同时导出指标和日志。完整的
变量参考请交给 Claude Code 自身的监控文档处理——不要从这里手抄数值。

:::caution[切勿内联采集器凭据]
受管设置文件在每台主机上都是明文。编写层正因如此会拒绝带值的 `OTEL_EXPORTER_OTLP_HEADERS`——
请用 mTLS 或密钥管理器引用来认证采集器，绝不要用内联令牌。
:::

内容捕获（提示词、工具体）在你显式选择加入前一直**关闭**——且无论客户端发出什么，control-plane
连接器都独立地仅保留结构化数据。

## 第 2 步——为机群打标签以支持 FinOps

在同一份受管 env 中设置 `OTEL_RESOURCE_ATTRIBUTES`，使用严格的 W3C Baggage 格式（对值做百分号编码；
不含空格或引号）：

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

自客户端 2.1.161 起，这些值会随**每个指标数据点和事件记录**一起传递，而不只是 OTLP 资源块——
且自定义键永远不会覆盖标准属性。在 control plane 上，把你认可的键列入 claude 连接器的
`resource_labels` 白名单；连接器会清洗这些值，并把它们作为标签附加到会话的身份边以及每个成本样本上。
FinOps 将 `team` 和 `project` 提升为一等的支出维度，因此“按团队切分 Claude Code 支出”可端到端工作。
不在白名单上的键会被丢弃——默认最小化数据。

## 第 3 步——子代理层级（tracing beta）

在受管 env 中启用增强遥测 beta 并加上 traces 导出器即可获得 span。子代理身份属性
（`agent_id`、`parent_agent_id`）是**仅 span 的**——它们不出现在任何指标和任何日志事件上——
且存在于 `claude_code.llm_request`（自 2.1.139 起）和 `claude_code.tool`（自 2.1.145 起）span 上。
连接器将它们映射进 access map，形式为：

- `session → identity.subagent`——实际发生动作的子代理**实例**，以及
- `parent agent → identity.subagent`——**谁孵化了它**（对于由主会话直接孵化的代理则缺失）。

这正是使同一类型的两个并发子代理可区分的关键——`Agent` 工具的 `subagent_type` 本身只是一个
类型标签，而非实例。

## 第 4 步——可选的精度开关

- `OTEL_LOG_TOOL_DETAILS=1` 为工具事件添加 `tool_parameters`——在被拒绝的工具决策上也是如此
  （自 2.1.157 起），因此拒绝类发现可以指明被拦截的那条经过净化的命令。连接器在摄取时把输入
  归约为经脱敏的资源引用，并且从不以原始形式存储；但这些值的确**会**离开开发者主机，因此启用它
  是一项深思熟虑的驻留决策。
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` 为所有指标和事件添加 `app.entrypoint`（默认关闭）。
  连接器将其记录为会话拓扑——一个嵌入 SDK 的机群与交互式 CLI 使用具有不同的风险姿态。

## 这条路径的诚实局限

- **未认证的回环摄取。** 协作式接收端默认绑定回环并必须保持如此；任何可达者都能伪造遥测
  （参见 [连接 Claude Code](/zh/how-to/connect-claude-code/)）。
- **子进程未被覆盖。** OTEL_* 不会到达 Bash/hook/MCP 子进程；在 tracing 下只有 `TRACEPARENT`
  会被继承。
- **admin-plane 馈送看不到第三方提供方。** Claude Code Analytics API 只追踪 Claude API 上的用量——
  Claude Platform on AWS、Microsoft Foundry、Amazon Bedrock 和 Gemini Enterprise Agent Platform (formerly Vertex AI) 都不在其中。对于运行在
  这些表面上的机群，**这条 OTel 路径是你拥有的唯一观测**，而 admin 馈送上的影子认证检测器无法
  为它们清账。
- **此处的成本数字是估算。** 每请求的成本遥测会与权威成本报告对账；每个会话只取一个成本来源，
  绝不取两个。

## 后续步骤

- [连接 Claude Code](/zh/how-to/connect-claude-code/)——本页所依托的基础连接。
- [治理与审批](/zh/how-to/govern-and-approve/)——执行的另一半（受管设置、hook、PEP）。
- [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/)——把这份遥测产生的发现送往你的 SIEM。

---
title: "OpenTelemetry GenAI（任意已插桩的 agent）"
description: >-
  通过厂商中立的 gen_ai.* 摄取 profile，从任意经过 OTel 插桩的 agent —— LangChain、
  LangGraph、CrewAI、AutoGen、Google ADK 及同类 —— 馈送 access map 与 FinOps：
  按需启用，固定到 semconv v1.41.1，并归一化真实机群中并存的三种 GenAI 方言。
sidebar:
  order: 4
---

Claude Code 是规范化的协作式来源，但它并不是你运行的唯一一个协作式 agent。接收
Claude Code 遥测（`kind: claude`）的同一个 connector，还承载一个**按需启用、厂商中立的
OpenTelemetry GenAI profile**：把任意经过 OTel 插桩的 agent 或框架指向同一个
OTLP 接收端，它的 `gen_ai.*` 遥测就会馈送 access map 与成本流水线 —— LangChain、
LangGraph、CrewAI、AutoGen、Google ADK，以及任何在 span 或 log 事件上发出 GenAI
语义约定（semantic conventions）的产物。

## 为什么默认按需启用

OpenTelemetry GenAI 约定在上游仍是 **Development 状态**（pre-stable，预稳定），而在
2026 年的机群中确实有三种方言并存。因此该 profile 默认关闭，并采用与 OTel SDK 完全
一致的门控方式 —— 通过 opt-in token：

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` 与 `OTEL_SEMCONV_STABILITY_OPT_IN` 对应：一个必须包含
`gen_ai_latest_experimental` 的逗号分隔列表。当该 profile **关闭**时，一条
`gen_ai.*` 记录仍会馈送会话存活看门狗（session-liveness watchdog），但**不会被映射**
—— 这是诚实地标明缺失，而非悄无声息地摄取。

## 归一化器接受什么

该 profile 固定到 **semconv v1.41.1**，并归一化真实 estate 中并存的三种 GenAI 方言，
为每个归一化后的事件打上该方言的 semconv 固定版本，使来源溯源（provenance）得以保留：

| 方言 | 形态 |
|---|---|
| Legacy OpenLLMetry | 带索引的 `gen_ai.prompt.{i}.*` 属性 |
| v1.36 及更早 | 已弃用的逐条消息事件 |
| v1.37+ | `messages` generation |

在消息形态之上，它还映射 **`mcp.*` 约定（v1.39）** 以及 **`invoke_agent`
client/internal 拆分加上 `invoke_workflow`（v1.41）** —— 这样由框架编排的 agent 与
workflow 调用会落地为结构化的拓扑，而非噪声。基于 span 的发出方式（LangGraph、
LangChain、CrewAI、AutoGen 和 Google ADK 的插桩方式）与基于 log 的发出方式都会被摄取。

成本样本（cost samples）按 W3C span id 去重，因此遥测同时通过 span 与 log 两条路径
到达的 agent 绝不会被重复计费。

## 把一个 agent 接入它

接收端是 connector 自己的 OTLP 端点（默认 gRPC `127.0.0.1:4317`，
HTTP `127.0.0.1:4318`）。在 agent 一侧，使用标准的 OTel SDK 配置 —— 将 exporter
端点指向该 loopback 接收端，并在你的插桩对其设有门控时启用 GenAI opt-in：

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[与 Claude Code 相同的 loopback 规则]
该协作式摄取是**未经身份验证的**，并默认绑定到 loopback。任何能触达该 socket 的实体
都能伪造遥测 —— 务必让它保持在 loopback 上（`allow_public_bind` 存在，并被刻意标记为
DANGEROUS）。处于异机的 agent 是内核 backstop 的职责，而非一个公开的 OTLP 端口的。
:::

## 你将在控制台看到什么

已插桩的会话会作为实时活动出现在 **Sessions** 中，归属到发出遥测的 agent；它们的模型
调用会馈送 **Cost & FinOps**；MCP 与工具 span 会像任何协作式来源一样为 **Access map**
贡献边（edge）：

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="Sessions 视图，展示来自协作式遥测的实时 agent 会话活动。" />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="Sessions 视图，展示来自协作式遥测的实时 agent 会话活动。" />

## 诚实的局限

- **预稳定约定，固定版本摄取。** 该 profile 固定到 v1.41.1；当上游演进时，固定版本
  通过一次刻意的更新来推进，而非通过悄无声息的漂移。发出第四种方言的插桩不会被臆测。
- **协作就是协作。** 不发出遥测的 agent 对这条路径是不可见的 —— 那正是
  [eBPF/Tetragon](/zh/how-to/connectors/ebpf-tetragon/) 与存储原生审计的用武之地。
- **框架的 span-kind 怪癖是真实存在的。** 某些框架发出的 span，其 kind 不符合 v1.41
  的 client/internal 规则；归一化器映射它能证明的部分，并让其余部分保持未映射，而不是
  错误归属。

## 相关

- [接入 Claude Code](/zh/how-to/connect-claude-code/) —— 同一个接收端，Claude 专属表面。
- [面向 Claude Code 的企业级 OTel](/zh/how-to/claude-code-enterprise-otel/) ——
  机群范围的遥测态势。
- [事件参考](/zh/reference/events/) —— 它产出的归一化观测。

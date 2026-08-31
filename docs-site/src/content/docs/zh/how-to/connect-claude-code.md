---
title: "连接 Claude Code（协作路径）"
description: "把 Claude Code 的 OpenTelemetry 导出器指向引擎并将其连接为一个源，使其工具遥测——以及不可信的 MCP 内省——馈入 R/RW access map。"
---

Claude Code 是 Olivares AI 的**标准协作式源**。它发出关于自己所运行工具的 OpenTelemetry（OTLP）遥测，
而它所对话的 MCP 服务器暴露关于某个工具是读还是写的内省提示（`readOnlyHint` / `destructiveHint`）。
二者共同以高保真、按代理归因的 edge 馈入**模块 III——R/RW access map**，即“已许可对比已观测”图景的
协作那一半。

本页连接那条路径：把 Claude Code 的 OTLP 导出器指向引擎的接收端，然后声明该源，使其遥测成为 access
edge。关于通用的源连接机制以及它如何契合，参见 [连接一个源](/zh/how-to/connect-a-source/) 与
[架构概览](/zh/explanation/architecture/overview/)。关于它所产生的归一化事件的形态，参见
[事件参考](/zh/reference/events/)。

:::note[协作的，而非权威的]
协作路径是**高保真但分信任层级的**。OTLP 工具遥测被归因到一个具体的代理会话；MCP 注解是一个有用的
R/RW *信号*，但按 MCP 规范是**不可信的**，需被佐证，绝不单独信任（参见
[诚实与局限](/zh/start/honesty-and-limits/)）。对于代理协作之外的活动——或为了抓住一个停止发出遥测
的代理——请把这条路径与一个非协作兜底（内核/eBPF）以及存储原生审计（pgAudit、CloudTrail）搭配使用。
本页只涉及协作式源。
:::

## 你从这个源能得到什么

连接之后，Claude Code 的遥测会被归一化进引擎的数据模型，并馈给模块 III：

| 输出 | 来源 | 备注 |
|---|---|---|
| **Access edge** `agent session → resource (read/write)` | 信号源 `otel` | 置信度 `attributed`——发起方是一个具体会话，而非共享的服务账户 |
| **MCP 服务器 edge** `session → MCP server` | 信号源 `otel` | 模式 `unknown`（一次连接本身并非一次访问；这属于拓扑/清单） |
| **来自 MCP 内省的 R/RW 提示** | 信号源 `mcp_annotation` | **不可信**——一个佐证信号，绝不单独成为一条 edge |
| **Cost sample**（每请求模型用量） | api-request 遥测 | 馈给 FinOps，而非 access map |
| **Finding**（反规避） | 遥测缺口 / 被拒绝的工具 | 一个仍处于活动状态却停止发出遥测的会话会被标记 |

该连接器是 **read-first 且最小化数据的**：它记录*关系*（哪个会话触及了哪个资源，读还是写），绝不
记录载荷。一个原始工具输入或 shell 命令——它可能携带密钥或 PII——在成为一条观测结果之前就被归约为
一个脱敏的资源引用。该姿态即默认；保留任何内容都是一项显式的、按类别限定范围的选择加入。

## 连接是如何工作的

它有两半，而它们在 Claude Code 所运行主机上的一个回环套接字处汇合。

1. **引擎把一个 OTLP 接收端作为 core 摄取暴露出来。** 协作连接器运行一个 OTLP 接收端（gRPC 和
   HTTP），用于 Claude Code 自身的 OpenTelemetry 输出，外加一个用于其工具 hook 的端点。它**默认
   绑定到回环**——协作式摄取是未认证的，因此它绝不能在主机之外可达。请让它保持在回环上；主机外的
   兜底是内核采集器，而不是一个公开的 OTLP 端口。
2. **你把 Claude Code 的 OTLP 导出器指向那个接收端**，并**声明该源**，使引擎知道为你的租户运行它。

```
  Claude Code (agent host)                 Olivares AI engine
  ┌──────────────────────────┐             ┌─────────────────────────────┐
  │ OTLP exporter            │── loopback ─▶│ cooperative OTLP receiver   │
  │ (OTEL_* env on the CLI)  │   (4317/4318)│ → normalize → access edges  │
  │ MCP servers (R/RW hints) │             │ → module III (R/RW map)     │
  └──────────────────────────┘             └─────────────────────────────┘
```

:::caution[接收端默认未认证且仅限回环]
由于协作式摄取在不认证发送方的情况下接受遥测，任何能够到达该套接字的人都能伪造 edge。接收端正因
如此默认采用回环绑定。把它绑定到一个非回环地址是一项危险的、显式的选择加入；不要把它暴露在共享网络
上。主机外的代理应改用非协作兜底来观测。
:::

## 第 1 步——把 Claude Code 指向接收端

Claude Code 通过它自己的 OpenTelemetry 环境变量来配置。在代理主机上，启用其 OTLP 导出并将其指向
引擎的回环接收端。引擎的接收端遵循标准 OpenTelemetry 端口（gRPC 和 HTTP）；把 Claude Code 导出器的
端点设置为相应的回环地址和协议。

:::note[确切的 OTEL 变量名属于 Claude Code，而非本产品]
导出器用 Claude Code / OpenTelemetry 自己的设置来配置（启用遥测、选择 OTLP 协议、设置端点）。这些
名称由 Claude Code 和 OTel SDK 定义——请查阅 Claude Code 的遥测文档了解当前的变量名，而不要在此复制
一份清单。本产品所拥有的是它们所指向的**接收端**以及下文的**源声明**。
:::

默认情况下，连接器只保留**结构化**遥测——会话和身份属性、工具名、R/RW 模式、计时——而绝不保留提示词
文本、工具体或原始 API 体，即使 Claude Code 被配置为发出它们。除非你有一个具体的、经审计的理由去
保留某个内容类别，否则请保持这种方式。

## 第 2 步——声明该源

真实（非演示）源是从一个由 `OLIVARES_SOURCES_CONFIG` 环境变量命名的、单一的、运维方拥有的配置文件
连接的，引擎在**它启动之前**读取该文件。密钥按值存放于那份运维文件中，绝不存放于存储中。每个条目命名
该源、它的 `kind`、它所属的租户，以及一个每源的 `config` 块：

```json
{
  "sources": [
    {
      "name": "claude",
      "kind": "claude",
      "tenant": "<tenant-ref>",
      "config": {
        "grpc_addr": "127.0.0.1:4317"
      }
    }
  ]
}
```

- **`name`** 是你为这个源实例所起的标签。
- **`kind`** 选择协作式 Claude Code 连接器。
- **`tenant`** 把它产生的每条 edge 限定到一个租户的范围（模块 III 的读取是按租户限定范围且需特权的）。
- **`config`** 持有连接器自身的设置——例如 OTLP 接收端所绑定的回环地址。该连接器自行绑定其接收端，
  而不是借用代理的，因此禁用某个 Claude Code OTEL 变量无法静默地把采集器关闭。

:::caution[对照所发布的描述符确认连接器的配置键]
该连接器发布它自己的配置 schema（其描述符列出每个键、类型、默认值和描述）。上面的 `config` 块展示
的是有代表性的接收端地址键；**不要**从本页**臆造额外的键**。请阅读连接器所报告的描述符——或
[配置参考](/zh/reference/configuration/)——以获取权威的、带版本的清单（接收端地址、hook 路径、
关联/静默窗口、内容捕获白名单，以及选择加入的治理字段）。一次一个值，对照你的构建实际发布的内容
加以核验。
:::

一个**未配置或为空的源会如实告警**而非失败：一个未知的、未嵌入的、或加载失败的 `kind` 会在启动时
被报告，绝不会被静默降级为无操作。编辑该文件后，请重启引擎，以便组合根（composition root）重新读取它。

## 第 3 步——验证 edge 正在到达

在 Claude Code 正在导出且源已声明的情况下，运行一个会触及某个资源（读取一个文件、运行一条命令、调用
一个 MCP 工具）的 Claude Code 会话，然后查看 access map。查看 access 图是一项**需特权的、按租户限定
范围的、被审计的动作**（editor 角色及以上——绝非最低的 viewer），因此请使用一个具有正确角色的令牌：

- access 图在模块路由 `/v1/m/accessmap/graph` 提供。
- “已许可对比已观测”的结果——最小权限**漂移（drift）**——在 `/v1/m/accessmap/drift`。

这些模块路由可达，但被刻意**排除**在所提供的 OpenAPI 文档之外；它们的契约存在于产品的类型化 Go/TS
接口中。关于从一个全新引擎到一张已填充图的端到端演练，请遵循
[从零到图教程](/zh/tutorials/zero-to-graph/)。

你应当看到信号源为 `otel` 的 edge，归因到该 Claude Code 会话。如果 MCP 内省贡献了一个 R/RW 提示，
它会作为一个单独的 `mcp_annotation` 信号到达，该信号佐证——但本身并不确立——该 edge 的模式。

## 这条路径的诚实局限

- **MCP 注解是不可信的。** `readOnlyHint` / `destructiveHint` 是一个服务器关于自身所声明的咨询性
  提示；MCP 规范说客户端必须把它们当作不可信。产品把它们作为一个佐证信号呈现，并如实展示置信度——
  它绝不会仅凭一个提示就把一条 edge 升级为“只读”。
- **归因取决于每代理身份。** edge 被归因到一个会话身份。一池共享同一个服务账户的代理会使归因塌缩；
  解决这一点是治理层面的关切（签发并强制执行每代理身份），而非这个连接器能够凭空制造的。
- **它是协作式的。** 它看到代理报告了什么。一个从不发出遥测的代理，或一项发生在代理路径之外的活动，
  从构造上对这个源就是不可见的——这恰恰是为什么非协作的内核兜底和存储原生审计与它并存。
- **设计阶段的深度。** 平台的大部分尚处于 1.0 之前。请把此处的能力当作经核验的协作式摄取路径；当
  某个下游模块或字段尚未构建时，产品会如实说明，而不是暗示已有覆盖。

## 后续步骤

- [连接一个源](/zh/how-to/connect-a-source/)——通用的源连接模型（协作的与非协作的）。
- [治理与审批](/zh/how-to/govern-and-approve/)——把已观测的漂移转化为一个最小权限决策。
- [事件参考](/zh/reference/events/)——这个源所发出的归一化观测结果。
- [架构概览](/zh/explanation/architecture/overview/)——协作路径在平台中所处的位置。

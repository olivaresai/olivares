---
title: gRPC 参考——服务、方法和消息类型
description: >-
  Olivares AI 引擎和插件宿主注册的每个 RPC，包括流式形态、请求和响应消息，
  以及传输所用的完整方法字符串。由服务器自己的注册表生成。
---

Olivares AI 在两个位置使用 gRPC，方向彼此相反：

- **引擎的控制平面 API**（`olivares.api.v1.ControlPlane`）——REST 表面的一个小型
  镜像，供偏好类型化 stub 的调用方使用。[API 参考](/reference/api/)中的 REST
  契约仍是二者中更广泛的契约。
- **插件传输契约**（`olivares.sdk.v1.*`）——每个进程外连接器和模块使用的版本化
  契约。当你使用 Go 以外的语言[构建连接器](/zh/how-to/build-a-connector/)时，
  要实现的就是这个契约。

本页**由服务器交给 gRPC 的注册表生成**，而不是从 `.proto` 文件生成。这个区别正是
重点：只编辑 `.proto` 而不重新生成，会描述一个二进制文件并未提供的服务；本页背后的
检查会报告这一分歧，而不是发布两者中看起来更漂亮的版本。本页列出的方法就是客户端
能够调用的方法。

:::note[稳定性]
插件契约 `olivares.sdk.v1` 已版本化，并由 buf 的破坏性变更检测器保护：不兼容变更
需要新的主版本 package。我们承诺的内容和期限请参阅
[API 稳定性](/zh/reference/api-stability/)。
:::

## 传输与认证

以下服务除 `GetServerInfo` 外的每个方法都要求主体已经认证并获得授权。两个例外是
有意设计的，在此明确列出，避免让你自行发现：`GetServerInfo` 匿名响应；标准
`grpc.health.v1.Health` 服务（`Check`、`List`、`Watch`）在同一 listener 上提供，
不要求主体，因为探针或 service mesh 必须能够到达每个 pod 上的它，就像 kubelet
到达 `/livez` 一样。没有 bearer token 会使请求保持匿名，而提供了无效 token 则会
被拒绝。控制平面服务通过引擎的 gRPC listener 访问；插件服务通过 go-plugin broker
调用（宿主内连接器），或由远程采集器通过双向 TLS 的 gRPC 调用。请使用
[配置参考](/zh/reference/configuration/)中的 `OLIVARES_*` 变量配置 listener。

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

引擎和插件宿主在 **7 个服务**中注册 **28 个 RPC**。下表读取自服务器交给 gRPC 的
生成注册表，因此这里列出的方法就是客户端能够调用的方法。

### `olivares.api.v1.ControlPlane`

定义于 `apiv1/api.proto`；5 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | 在清单中注册新 Agent，并返回存储的记录，包括 API 其余部分使用的标识符。 |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | 按标识符返回一个 Agent，字段与 REST 清单端点提供的相同。 |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | 报告版本、版本类型和就绪情况。它是此服务上唯一不需要认证主体的方法。 |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | 逐页列出调用主体可见的 Agent。 |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | 重新验证某一范围内的审计链，并报告哈希是否仍然相连，包括检查点状态。 |

### `olivares.sdk.v1.ContentSourceService`

定义于 `olivaresv1/v1.proto`；7 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | 结束由 `Open` 打开的会话，并释放连接器为该会话持有的所有内容。 |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | 流式传输游标之后的变化。仅在连接器声明 `content.delta` 能力时调用。 |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | 返回连接器描述符：身份、配置字段和声明的能力。 |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | 返回宿主从 `List` 流中选出的引用所对应的一份文档正文和元数据。 |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | 返回治理一份文档的权限引用。空结果表示应用知识库默认值。 |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | 每次一页流式传输文档引用，并受宿主传入的上限约束，使语料库无法在一次调用中全部载入宿主内存。 |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | 在进行任何内容调用前，使用宿主提供的配置启动会话。 |

### `olivares.sdk.v1.HostService`

定义于 `olivaresv1/v1.proto`；3 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | 通过引擎写入一条结构化日志记录，使进程外模块与进程内模块写入同一位置。 |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | 代表进程外模块在引擎总线上发布一个事件。 |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | 将总线事件流式传给模块，并按模块请求的事件类型过滤。空过滤器表示所有类型。 |

### `olivares.sdk.v1.IngestService`

定义于 `olivaresv1/v1.proto`；1 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | 接收采集守护进程推送的观测流，将每一项提升到事件总线，并在流结束时返回摘要。 |

### `olivares.sdk.v1.ModuleService`

定义于 `olivaresv1/v1.proto`；4 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | 返回模块描述符：模块身份及其接受的配置。 |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | 将配置交给模块，让它在任何内容启动前做好准备。 |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | 在 `Init` 成功后启动模块工作。 |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | 停止模块，并允许它释放持有的内容。 |

### `olivares.sdk.v1.OutputService`

定义于 `olivaresv1/v1.proto`；4 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | 结束由 `Open` 打开的会话，并释放连接器为该会话持有的所有内容。 |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | 返回连接器描述符：身份、配置字段和声明的能力。 |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | 将一条通知投递到目的地，并报告目的地的处理结果；该结果决定宿主是否重试。 |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | 在任何投递前，使用宿主提供的配置启动会话。 |

### `olivares.sdk.v1.SourceService`

定义于 `olivaresv1/v1.proto`；4 个 RPC。

| 方法 | 完整方法 | 类型 | 请求 | 响应 | 用途 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | 结束由 `Open` 打开的会话，并释放连接器为该会话持有的所有内容。 |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | 返回连接器描述符：身份、配置字段和声明的能力。 |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | 将观测流式传给宿主，由宿主把每一项提升到事件总线。批处理运行完成或宿主取消时，流会结束。 |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | 在采集任何观测前，使用宿主提供的配置启动会话。 |

<!-- END GENERATED olivares-grpc-reference -->

## 消息形态

表格给出了每个请求和响应消息的名称；它们的字段声明在各服务旁列出的 `.proto`
文件中。这些文件随仓库发布，也是生成 stub 的来源。阅读前有两条惯例值得了解：

- **词汇字段是字符串，不是封闭 enum**——访问模式、信号来源、置信度、严重性和
  事件类型。第三方连接器无需等待 SDK 发布，即可引入自己的信号来源。
- **Payload 形态是封闭的。** `Observation` 或 `Event` 的 payload 是已知消息类型的
  `oneof`，另加一个供模块定义事件 payload 使用的 JSON fallback。无法识别的 payload
  是契约错误；不会被静默丢弃。

## 生成客户端

`.proto` 文件就是契约。插件契约请让所用语言的 protobuf 工具链指向
`sdk/plugin/proto/olivaresv1/v1.proto`；控制平面镜像则指向
`core/api/proto/apiv1/api.proto`。Go 和 TypeScript 的现成客户端在
[使用客户端 SDK](/zh/how-to/use-the-client-sdks/)中说明。

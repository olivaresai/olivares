---
title: "参考"
description: "面向信息的参考：REST API、事件总线、模块目录、CLI 与配置——精确且详尽，绝无推断。"
---

参考是**面向信息**的。它的职责是精确而完整，而非教学或说服：它陈述接口是什么、它们的输入与输出
是什么、默认值是什么——到此为止。文字刻意干涩。如果你想通过动手来学习本系统，请从
[教程](/zh/tutorials/zero-to-graph/)开始；如果你想完成某项具体任务，请使用
[how-to 指南](/zh/how-to/connect-a-source/)；如果你想理解本系统为何如此构建，请阅读
[explanation](/zh/explanation/architecture/overview/)。本节适用于当你针对本产品进行构建并需要确切
契约之时。

下文绝大部分内容是**直接从产品自身的源码工件**生成或手工推导而来的，因此该参考不会悄然偏离引擎
实际所提供的内容。凡某项能力处于设计阶段或属 v1 之后，相关页面都会明白说出；总体契约见
[Honesty & limits](/zh/start/honesty-and-limits/)。

## 参考领域

| 领域 | 它记录什么 | 真相之源 |
|---|---|---|
| **[REST API](/reference/api/)** | control-plane HTTP API：auth、setup、tenancy、agent、R/RW access map、token 与 audit ledger。 | 本产品的 **OpenAPI 3.1** 契约（53 条核心路径），在构建时从真实文件渲染——不是副本。 |
| **[模块路由（beta）](/reference/api-beta/)** | 产品的模块路由（`/v1/m/<ns>/…`）——finops、compliance、governance、sessions、models、knowledge 等——作为独立的 **beta** OpenAPI 文档。 | 同一份 OpenAPI 3.1 契约，在构建时从模块实际注册的路由反射得出。 |
| **[稳定性策略](/zh/reference/api-stability/)** | 版本管理、稳定性层级、弃用/停用信令，以及 API、provider 与客户端 SDK 的最短支持窗口。 | 代码内的弃用表及其会使构建失败的窗口测试。 |
| **[gRPC](/zh/reference/grpc/)** | 引擎的 gRPC 镜像，以及每个进程外连接器与模块用于通信的版本化插件线协议。 | 服务器交给 gRPC 的 `grpc.ServiceDesc` 注册表。 |
| **[事件总线](/zh/reference/events/)** | 内部事件总线：事件信封、第一方事件类型，以及连接器提升其上的观测 payload。 | 一份从 Go SDK 手工推导的 **AsyncAPI 3.0** 契约。 |
| **[控制台界面](/zh/reference/console/)** | 控制台发布的每条路由、其所需的 RBAC 权限，以及其产品内帮助链接打开的参考页。 | 控制台路由清查，钉住到已构建的路由器。 |
| **[模块目录](/zh/reference/modules/overview/)** | 30 个产品模块——每个是什么、其状态，以及它在核心 API 之外暴露哪些路由（如有）。 | 产品能力目录与类型化的模块接口。 |
| **[CLI](/zh/reference/cli/)** | `olivares` 二进制及其子命令——`serve`、`collector`、`audit`、`license`、`openapi`、`version`——及其 flag。 | 已编译的命令定义。 |
| **[配置](/zh/reference/configuration/)** | 环境变量与运行时选项：数据目录、source 配置、授权引擎与台账签名。 | 引擎的配置加载器。 |

## REST API

[REST API 参考](/reference/api/)在构建时从本产品的 **OpenAPI 3.1** 契约渲染——与引擎在其自身
`/openapi.json` 端点所提供的是同一份文档。没有任何内容由人工誊写，因此渲染出的参考即契约。它涵盖
无凭据的首次引导流程（`POST /v1/setup` 带一次性 setup token，再 `POST /v1/auth/login`）、身份与
tenancy、agent、读/写 access map（`GET /v1/access-edges`；其已对账的最小权限 *drift* 由
access-map 模块而非核心面提供）、token 管理与 audit ledger。

该契约描述 **53 条核心路径**。这是刻意为之：它是 control plane 稳定、带版本的面，而非引擎能应答的
每一条路由。“stable” 所承诺的内容——版本管理、弃用信令与最短支持窗口——即
[API 稳定性策略](/zh/reference/api-stability/)。

:::note[模块路由是独立的 beta 契约]
模块路由——例如 access-map 模块的 `/v1/m/accessmap/graph`、
`/v1/m/accessmap/neighbors` 与 `/v1/m/accessmap/drift`——**不**属于包含 53 条路径的
稳定核心文档。它们作为独立的 **beta** OpenAPI 文档发布在
[模块路由参考](/reference/api-beta/)（服务于 `/openapi.beta.json`，并从模块实际
注册的路由反射得出），从而让稳定面保持可识别，同时完整产品面仍可编程。
Beta 表示这些形状可在通知后发生变更（支持窗口短于 stable）；字段级细节仍存在于
类型化的 Go 与 TypeScript 接口中。最小权限 access-map 结果即 `drift` 路由；
没有单独的 `diff` 端点。
:::

### gRPC 镜像（`olivares.api.v1`）

control plane 还暴露一个 **gRPC** 面——带版本 proto 包 `olivares.api.v1` 中的 `ControlPlane`
服务。它是上述 REST 契约一个子集的**聚焦、冻结镜像**（server info、agent list/get/create、audit
verify），用于偏好类型化二进制契约之处（例如 collector）。它镜像 REST 契约而非扩展之；OpenAPI
文档仍是完整 API 的规范面。

## 事件总线

[事件总线参考](/zh/reference/events/)是一份 **AsyncAPI 3.0** 契约。该总线**默认在进程内**——连接器
将规整后的观测作为类型化事件提升其上，模块与输出连接器**按事件类型**订阅并作出反应，彼此之间不
直接调用。基于 NATS 的分布式绑定为可选，非必需。

该契约是**从 Go SDK 手工推导**而非生成的：权威定义是事件信封、第一方事件类型，以及观测 payload
（agent→resource 访问观测、成本采样与 finding 报告）。凡总线尚未形式化某项内容，该参考如实说出
而非发明。

## 模块目录

[模块目录](/zh/reference/modules/overview/)列举位于核心引擎之上的 **30 个模块**，横跨九个能力领域。
其中最有用的之一是带 **Permitted-vs-Observed** 差异的 **R/RW access map**：它从日志、OTEL 以及
（作为非协作后备的）eBPF 读取，而非位于数据路径中，并且只存储*哪个 agent 能读或写哪个 resource*
这一关系——绝不存储 payload、secret 或 PII。

该目录对状态与覆盖是诚实的。每个模块携带其自身的成熟度——多数实时且端到端集成，部分为局部或
选择性启用。被动观测按存储类型**分层**——SQL、对象与数仓存储为 clean；文档与向量存储为 lossy；
内存或嵌入式存储在无协作时不可行——且目录标记某模块处于设计阶段之处。自有 model 注册与微调是一项
**计划中的能力**，而非已发布的 30 个模块之一。

## CLI

[CLI 参考](/zh/reference/cli/)记录单一的 `olivares` 二进制及其子命令。你用来操作 control plane 的那个
是 `serve`，它启动 HTTP（REST + 内嵌 web UI）与 gRPC 监听器；**TLS 默认开启**。其他子命令涵盖
collector、audit ledger（`verify`、`checkpoint`、`export`）、license 工具，以及发出 OpenAPI 文档。

:::caution[先构建，再运行]
没有 `task run` 或裸 `docker run` 捷径。你要么直接构建并调用该二进制——`task setup`、`task build`，
然后 `./bin/olivares serve`——要么用所提供的 Compose 文件将其拉起，并从日志中读取一次性 setup
token。CLI 页面列出已验证的 `serve` flag 及其默认值。
:::

## 配置

[配置参考](/zh/reference/configuration/)列出塑造一次部署的环境变量与运行时选项。承重的那些是数据目录
（`OLIVARES_DATA_DIR`）、引擎启动前从 `OLIVARES_SOURCES_CONFIG` 读取的真实（非演示）source 配置，
以及授权引擎选择器 `OLIVARES_PDP_ENGINE`（`cedar`、`opa` 或 `none`）。

两条设计规则贯穿整个配置面。一个**未配置的 source 会诚实告警**而非使引擎失败。并且授权 seam
**只会限制，绝不放宽**：RBAC 是 deny-by-default，查看 access graph 是一项特权动作，且每次此类
读取都被审计。

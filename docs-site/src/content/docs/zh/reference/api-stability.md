---
title: API 稳定性、版本管理、弃用与停用
description: >-
  REST API、gRPC 镜像、实时摄取（live-ingest）传输协议契约、Terraform provider
  以及各客户端 SDK 的版本管理方案、稳定性层级、弃用信号（RFC 9745 /
  RFC 8594 头部）与最短支持窗口。
---

本页是面向一切对 control plane 编程的对象的**稳定性契约**。它阐明哪些内容是稳定的、
破坏性变更如何被发出信号，以及一个已弃用的接口面会继续工作多久。其执行落在
代码库中，而非散文里：下文的弃用表、响应头部、OpenAPI 标记与窗口检查，
全部由单一的代码内声明（`core/api/stability.go`）驱动，而一个调度时间早于
策略所允许范围的停用计划会**使构建失败**。

:::note[1.0 之前的状态]
Olivares AI 处于 1.0 之前（参见 [诚实与局限](/zh/start/honesty-and-limits/)）。
本页所述的信号机制已经上线；而**最短支持窗口自 1.0/GA 发布起方才生效**。
在此之前，已发布的接口面在实践中保持稳定，但下文的正式窗口是你自 GA 起
可以据以要求我们的承诺。
:::

## 覆盖的接口面与层级

| 接口面 | 版本依据 | 当前层级 |
|---|---|---|
| REST 核心契约 —— [所提供的 OpenAPI 文档](/reference/api/)中的路径 | URL 主版本（`/v1/…`） | **stable** |
| gRPC 镜像 —— proto 包 `olivares.api.v1` 中的 `ControlPlane` | proto 包主版本 | **stable**（冻结镜像） |
| 实时摄取 / 连接器传输协议 —— proto 包 `olivares.sdk.v1` | proto 包主版本 + 插件 `ProtocolVersion` | **stable**（冻结） |
| 连接器 SDK（Go）—— 模块 `sdk`、`sdk/plugin`（作者接口面） | 模块 semver —— 自首个公开发布起的标签 `sdk/v*`、`sdk/plugin/v*` | **stable v1**（Go 契约；见上方协议行） |
| [事件总线契约](/zh/reference/events/)（AsyncAPI 3.0）—— 其事件类型也正是 eventing 平台投递给[外部 webhook 订阅](/zh/reference/events/#外部订阅eventing-平台)的内容；订阅管理路由是模块路由（`/v1/m/eventing/`，不在契约内），但每个**事件类型**都携带其在代码内目录中的稳定性层级 | `info.version`（`1.0.0-preview`） | **beta**（文档）；事件类型按类型分层 |
| Terraform provider | 自有 semver（`terraform-provider-v*` 标签） | **stable**，MAJOR 跟随 API v1 |
| 客户端 SDK（Go / Java / Python / TypeScript） | 自有 semver；自 GA 起 MAJOR 跟随 API 主版本 | **beta**（1.0 之前的包） |
| 任何未列出者 —— `/v1/m/<ns>/` 模块路由、SCIM、联邦、内部接口 | — | **out of contract（契约之外）** |

**层级。** *stable* 接口面在其主版本内不会发生不兼容变更；
移除或更改它需要走下文的弃用流程。*beta* 接口面仍可能改变形态，
但获得相同的信号机制与一个更短的窗口。*out-of-contract（契约之外）* 接口面
（尤其是那些被刻意排除在 OpenAPI 文档之外的模块路由 —— 参见
[参考概览](/zh/reference/)）不携带任何兼容性承诺；其契约存在于随产品一同交付的
类型化接口中。

OpenAPI 文档中的每个操作都携带一个机器可读的
`x-stability` 标记，而文档本身在 `info.x-stability-policy` 中链接到本页。

## 什么算作破坏性变更

对于 stable 接口面，以下各项都属于破坏性变更，并受下文流程的把关：

- 移除或重命名路径、方法、请求字段、响应字段或错误 `code`；
- 更改某字段的类型或含义，或将一个可选的请求字段改为必填；
- 收紧认证/授权，以致此前有效的调用失败；
- 对于 gRPC/protobuf：任何被 `buf breaking`（FILE 规则集）拒绝的内容。

以下**不**属于破坏性变更：新增端点、新增可选请求参数、
新增响应字段、为新的失败模式新增错误码，以及新增响应头部。
客户端必须容忍未知的 JSON 字段。

## 版本管理

- **REST** 在 URL 中进行版本管理：整个 stable 契约位于
  `/v1/` 之下。不兼容的变更在 `/v2/` 下交付，而 `/v1/` 进入
  弃用 —— 绝不就地破坏。
- **gRPC** 按 proto 包进行版本管理：`olivares.api.v1` /
  `olivares.sdk.v1`。不兼容的变更需要一个新的包主版本
  （`…v2`）；两份契约都由 `buf breaking` 针对 `main` 加以守护
  （`task proto:breaking`）。
- **Terraform provider** 独立发布
  （`terraform-provider-v*` 标签）；其 MAJOR 跟随它所对接的 API 主版本。
- **客户端 SDK** 内嵌 `API_VERSION`（其生成所依据的契约主版本）
  与 `SPEC_HASH`（确切的 OpenAPI 快照）—— 在 Go 中为 `APIVersion` 与
  `SpecHash`；自 GA 起其 MAJOR 跟随 API 主版本。
- **连接器 SDK**（第三方连接器据以构建的 Go 契约）
  按每模块的 semver 标签（`sdk/vX.Y.Z`、
  `sdk/plugin/vX.Y.Z`）进行版本管理，并由作用于其传输协议的同一道 `buf breaking`
  防线把关。作者所实现的接口在一个主版本内绝不新增方法；新能力
  以新的可选接口形式到来。完整策略随模块一同交付
  （`sdk/VERSIONING.md`）；编写生命周期见
  [构建并发布一个连接器](/zh/how-to/build-a-connector/)。

## 弃用流程与信号

一次弃用 = 代码内表中一条声明的条目，外加一份迁移
指南；其余一切都由它机械地推导而来。

1. **公告。** 该条目带着其公告日期与迁移
   指南 URL 落地。自那一刻起，已弃用路由的每个响应都携带
   [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745) 头部以及指向该指南的链接，
   并且该 OpenAPI 操作获得 `deprecated: true`、
   `x-deprecated-at` 与 `x-migration-guide`：

   ```http
   Deprecation: @1780272000
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="deprecation"
   ```

2. **安排停用。** 当退役日期确定后，响应会
   加上 [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594) 头部（且规范获得
   `x-sunset-at`）：

   ```http
   Sunset: Thu, 01 Jun 2028 00:00:00 GMT
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="sunset"
   ```

3. **移除** —— 最早在停用日期，通常随下一个
   API 主版本进行。

**最短支持窗口**（弃用公告 → 停用）：

| 层级 | 最短窗口 |
|---|---|
| stable | **24 个月** |
| beta | **12 个月** |

这些窗口由针对声明表的测试强制执行：一个其停用
违反所属层级窗口的条目，或者一个指向不存在路由的条目，将无法构建。

对于 **gRPC**，弃用以 protobuf 的 `deprecated` 选项表达
（它会在生成代码中显现）外加相同的窗口；传输协议契约
在其余方面均被冻结，而 `buf breaking` 会直接拒绝不兼容的修改。

## 客户端看到什么

- **Terraform provider** —— 当 control-plane 响应携带弃用信号时，
  对每个唯一的方法与请求路径在每次运行中发出一次 `tflog` WARN
  （方法、端点、日期、指南）（一个被弃用的参数化路由会对它所触及的每个资源
  各告警一次），并发送一个带版本的 `User-Agent`，使服务端可归因
  已弃用客户端的使用情况。
- **Go SDK** —— 对每个端点显现一次 `DeprecationNotice`（默认为一条
  `slog` 警告；可用 `WithDeprecationHandler` 覆盖）。已弃用的
  操作携带 Go `// Deprecated:` 标记，因此编辑器与 `staticcheck`
  会在开发期将其标出。
- **Python SDK** —— 对每个端点一次 `DeprecationWarning`（或你的
  `on_deprecation` 回调）；已弃用的操作在 docstring 中被标注。
- **TypeScript SDK** —— 对每个端点一次 `console.warn`（或你的
  `onDeprecation` 回调）；已弃用的操作携带 `@deprecated` JSDoc。

## 相关

- [REST API 参考](/reference/api/) —— stable 契约本身
- [使用客户端 SDK](/zh/how-to/use-the-client-sdks/)
- [构建并发布一个连接器](/zh/how-to/build-a-connector/) —— 连接器 SDK
  契约与生命周期
- [以代码方式管理（Terraform）](/zh/how-to/manage-as-code/)
- [模块 XIX —— 自有 API + 以代码方式管理](/zh/reference/modules/xix-api-manage-as-code/)
- [事件总线（AsyncAPI 3.0）](/zh/reference/events/)
- [诚实与局限](/zh/start/honesty-and-limits/)

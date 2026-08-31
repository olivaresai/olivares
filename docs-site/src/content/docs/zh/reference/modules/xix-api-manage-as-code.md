---
title: "模块 XIX — 自有 API 与 manage-as-code 面"
description: >-
  引擎的基础面：通过一套 REST/gRPC API 实现每一项控制平面（control plane）动作，外加一个
  Terraform provider，使控制平面本身得以被声明并纳入版本控制。API 契约是什么、provider
  管理什么，以及各自诚实的边界。
---

模块 XIX 不是一个被栓接到引擎上的功能——它**就是**引擎的面。其他每个模块都通过同一套第一方 API 触及外部世界，
而 Web UI 是覆盖在那个完全相同契约之上的一个表现层，而非一套平行的契约。本页是这个面当前所暴露内容的参考，
以及如何将控制平面作为代码来管理，及其真实边界。

## API 契约

引擎在 `/v1` 之下讲一套 REST API（chi 路由器，经加固的 `http.Server`），以及它的一个**聚焦的、冻结的 gRPC 镜像**
（`olivares.api.v1`：服务器信息、智能体读取/创建、审计验证，外加标准的健康服务）。gRPC 是一个刻意的子集，
而非完全对等——新端点先落地于 REST。两种接口都运行**同一条** `authenticate → resolve-tenant → authorize` 链路，
并以完全相同的方式映射错误，因此无论在哪种接口上，一个 not-found 都与一个跨租户资源无法区分。

REST 面作为一份 **OpenAPI 3.1 契约**发布，直接从产品撰写的 schema 渲染于
[API 参考](/reference/api/)。该文档是稳定核心表面的契约权威记录；模块路由则作为独立的
**beta** 文档发布——见[模块路由参考](/reference/api-beta/)（亦见下方诚实的边界）。
相同的功能也可从终端驱动——见 [CLI 参考](/zh/reference/cli/)——因为 CLI 就是引擎，而非它的一层包装。

## 认证与协议

认证采用**不透明的服务端 bearer token**，而非 JWT。
token 带有用途前缀（会话型 vs. API key）；
服务器只持久化一个公开选择器和密钥的 SHA-256，并以常数时间比较该密钥。对 manage-as-code 工作流而言要紧的后果是：
token 是**可即时吊销的**，**不携带任何声明（claims）或机密**，并且不增加任何加密解析攻击面。
一个 API token 被绑定到一个 `(tenant, role)`，或者是一个未绑定的系统级凭据；
一个其租户 header 与所绑定 token 不一致的请求会被拒绝，绝不静默放宽。

## Manage-as-code：Terraform provider

`terraform-provider-olivares` provider 是一个**独立的 Go 模块**，且是一个纯 REST 客户端
——它从不导入引擎核心或连接器（connector）SDK，从而将庞大的 provider 依赖树排除在核心的供应链之外。
它配置有一个端点、一个敏感的 API token 和一个可选的租户，管理一组刻意保持精简的、声明式的对象：

| 类别 | 名称 | 管理什么 |
|---|---|---|
| resource | `olivares_agent` | 一个智能体的目录定义（完整 CRUD + 导入） |
| resource | `olivares_policy` | 一份治理策略声明 |
| resource | `olivares_agent_identity_binding` | 一个智能体到非人身份（NHI）的绑定 |
| resource | `olivares_deployment` | 一个部署**定义**（期望状态，声明式） |
| data source | `olivares_policies` / `olivares_identities` | 受治理名册的只读视图 |
| data source | `olivares_access_edges` | R/RW 访问映射及其 permitted-vs-observed 漂移 |
| data source | `olivares_deployment` / `olivares_server_info` | 一个部署定义；引擎元数据 |

这些是该 provider 所提供的**唯一**资源与数据源。声明一个 `olivares_deployment` 会在控制平面中记录期望状态
——它**不**触及基础设施；apply 路径属于 [模块 VII](/zh/reference/modules/vii-deploy/)，是一个 deny-closed 接缝。

:::caution[诚实的边界]
- **`olivares_deployment` 进行声明；它不进行部署。** 该资源通过模块 VII 的路由写入一个部署*定义*。
  针对真实基础设施的实时 `apply`/`retire` 是一个**返回 `503` 的 deny-closed 接缝**，
  直到一位操作员供给一个执行器为止——在 HCL 中声明一个部署绝不会改变你的资产清单（estate）。
- **稳定 OpenAPI 并非全部协议表面。** 稳定核心表面位于已发布契约
  （`/openapi.json`）中；模块路由（例如访问映射与漂移读取，以及 provider 所用的
  治理与部署路由）作为独立的 **beta** 文档发布（`/openapi.beta.json`，即
  [模块路由参考](/reference/api-beta/)）。它们字段级的形态存在于产品的类型化接口中，
  而非稳定 schema 里。
- **gRPC 是一个冻结的子集，而非完整 API。** 它为第一方自动化镜像了少数几项读取/创建与审计操作；
  不要因为某个端点在 REST 上存在，就假定它在 gRPC 上也存在。
- **provider 的面是刻意保持小的。** 四个资源和五个数据源——而非将整个 API 作为 IaC。
  这一集合之外的任何东西，今天都通过 REST/CLI 管理，而非在 HCL 中声明。
- **许可证是证明，绝非功能门。** 该产品在其许可证下是完整的；离线许可证检查只记录持有者和状态，
  从不禁用、降级或阻断任何 API 请求或启动。
:::

## 默认安全

服务引擎是默认安全（secure-by-default）的：TLS 是开启的（若未提供，首次启动时会生成一份自签证书），
绑定默认为 localhost，且本地监听**并不**豁免授权。一份全新安装没有凭据——它向 stdout 铸造一个一次性的设置 token，
并拒绝每一个受保护端点，直到第一位管理员被创建。审计是仅追加、哈希链式的，并带有 Ed25519 签名的检查点，
使得在某个检查点之前重写历史在密码学上是可被检测的。

## 事件平台（模块 XIX 的出站一半）

自事件平台（`modules/eventing`）发布以来，模块 XIX 的面也包含了**租户自助式事件订阅**：
对总线事件目录（`edge.observed`、`cost.sampled`、`finding.reported`、`audit.recorded`、…）的类型化订阅，
具有**持久的至少一次（at-least-once）投递**——带退避的重试、一个死信队列，以及从游标回放——
投递到一个 HMAC 签名的 webhook 或一个 [SIEM sink](/zh/how-to/cookbook/push-to-siem/)。
通知模块（[XV](/zh/reference/modules/xv-notify/)）仍是面向操作员供给目的地的告警*路由器*；
事件平台则是面向集成者的平台。一个配套的只读**姿态导出（posture export）**（`modules/posture-export`）
让一座控制塔得以轮询产品的 ground-truth 姿态——访问图、漂移、清单、发现——仅以 ref/哈希/关系的形式呈现，
导出本身也经审计。

## 相关

- [API 参考](/reference/api/) — 为核心面渲染的 OpenAPI 3.1 契约。
- [API 稳定性策略](/zh/reference/api-stability/) — 本面的版本管理、弃用/日落（sunset）信号传达与支持窗口。
- [使用客户端 SDK](/zh/how-to/use-the-client-sdks/) — 第一方 Go/Python/TypeScript 客户端。
- [CLI 参考](/zh/reference/cli/) — 来自 `olivares` 二进制的相同功能。
- [将控制平面作为代码管理](/zh/how-to/manage-as-code/) — Terraform provider 指南。
- [模块 VII — 部署](/zh/reference/modules/vii-deploy/) — `olivares_deployment` 驱动之处（那个 `503` 接缝）。
- [模块目录](/zh/reference/modules/overview/) — Govern/Observe 与 Actuate 的划分。
- [诚实与边界](/zh/start/honesty-and-limits/) — 今天驱动什么、不驱动什么。
- [架构概览](/zh/explanation/architecture/overview/) — 这个面所处的引擎层。

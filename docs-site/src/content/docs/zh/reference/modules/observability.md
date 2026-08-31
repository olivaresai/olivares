---
title: "可观测性 —— 引擎对自身的只读模型"
description: >-
  一个建立在已有事物之上的纯只读模型：引擎固定并对外提供哪些互操作标准、经
  W3C 关联的审计账本（audit ledger）对某条追踪的记述，以及关于运行中二进制文件
  供应链可被证明为真的内容。它不拥有任何实体，也不持久化任何数据。
---

可观测性（`modules/observability`）是 30 个模块之一 —— 与
[实时摄取](/zh/reference/modules/live-ingest/) 一样，它承担的是架构性角色，而非填补某个
能力槽位。它是引擎对**自身的只读模型**：`/v1/m/observability/` 下的三个只读
表面，回答管理控制台 System（系统）区域所渲染的问题，且不拥有任何一个存储实体。

## 三个表面

| 路由 | 回答的问题 |
|---|---|
| `GET /ingestion-health` | **按互操作标准**列出流入和流出引擎的内容 —— 引擎所固定的标准（OTel GenAI semconv、OCSF、ASIM、统一的 SIEM 格式、账本推送、Prometheus 文本格式、W3C Trace Context），每项都附带其已校验的版本 |
| `GET /traces`、`GET /traces/{id}` | **经 W3C 关联的审计账本**对某条追踪的记述 —— 一条分布式追踪在审计侧的视图，通过 Trace Context 关联 |
| `GET /attestation` | **关于运行中二进制文件供应链可被证明为真的内容** —— [发布校验链](/zh/how-to/verify-a-release/) 所馈送的证明（attestation）表面 |

这三者都是带有模块作用域权限的读取操作；这里不会对任何东西进行变更。

## 它为何会是一个模块

管理控制台需要一个权威的答案来回答"这个引擎究竟支持什么，以及处于哪个固定的
版本？"—— 而诚实的提供方式是来自引擎本身，而非可能漂移的文档。
ingestion-health 表是从连接器（connector）和导出器编译所依据的同一组固定版本
生成的，因此当某个固定版本变动时，该表面也随之变动。

## 明确陈述的界限上下文

- **它不拥有任何存储实体，也不持久化任何数据** —— 一个建立在已有底层之上的纯只读
  模型（固定版本、账本、证明证据）。
- 它**不是** [模块 XXII（健康/SLA）](/zh/reference/modules/xxii-health/)，后者的边界
  限定于*治理范围（estate）*中各 agent 与 MCP 服务器的可靠性。本模块关注的是*引擎*。
- 它**不是**指标端点：运营时序数据存在于
  [`/metrics`](/zh/how-to/monitor-with-prometheus/)；本模块提供的是结构化答案，而非时序。

## 相关

- [使用 Prometheus 监控](/zh/how-to/monitor-with-prometheus/) —— 运营指标与 SLO。
- [事件参考](/zh/reference/events/) —— ingestion 表所报告的总线词汇表。
- [校验一次发布](/zh/how-to/verify-a-release/) —— 证明表面所反映的供应链证据。

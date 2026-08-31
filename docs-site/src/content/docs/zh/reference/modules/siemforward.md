---
title: "SIEM/ITSM 转发器"
description: >-
  将已封存、哈希链式的审计账本（audit ledger）与治理发现，以你的 SIEM 与 ITSM
  系统的原生格式（OCSF 1.8、CEF、LEEF、syslog 或 OTLP）通过持久化的事件平台进行投递，
  采用领导者门控的游标遍历与至少一次（at-least-once）交付。它只负责渲染与转发，绝不重新推导完整性。
---

SIEM/ITSM 转发器（`modules/siemforward`）把引擎已经封存的证据，送入你的 SOC
正在运行的系统中。它处于 **LIVE** 状态。它不拥有任何新证据：它遍历
篡改可检测审计账本（audit ledger）
与治理发现流，把每一条记录重新塑形为目标系统的原生格式，再交给
[事件平台](/zh/reference/modules/eventing/) 进行持久化交付。完整性字段原样传递——绝不在传输途中重新推导。

## 它转发什么，以及如何转发

两个部分协同工作。一个 **`SinkRenderer`**（它实现了 `eventing.SinkRenderer`）
将一条捕获的事件重新塑形为目标系统的线格式：

- `audit.recorded` —— 一条已封存的账本记录，经由 `core/audit` 渲染。
- `finding.reported` —— 一条治理发现（最小化数据：哈希加上经脱敏的摘录）。
- 总线上的任何其它内容 —— 一个格式中立的信封，通用采集器可自行解析。

支持的格式：**OCSF 1.8**、**CEF**、**LEEF**、**syslog**、**OTLP**，以及一个结构化的
JSON 直通（passthrough）。渲染器是 **默认拒绝（deny-closed）** 的：未知的 sink 类型或无法渲染的格式会返回错误，
引擎随即重试，再将该交付转入死信队列——绝不会发出未经认证或格式错误的内容。

一个 **领导者门控的转发泵（forward pump）** 驱动其余流程。每一遍读取一个按租户划分的游标，
从下一个序列号开始以有界批次遍历账本，并将每条记录入队。游标只会越过成功入队的记录，
因此崩溃或重启后会从停止处恢复——这是从账本（权威来源）出发的 **至少一次（at-least-once）** 交付。
被重新遍历的记录在下游去重。

## 目的地

账本去往何处，由按租户划分的事件 **sink 订阅** 决定，而非本模块上的自助式 API——它不挂载任何路由。
目的地由 **运维方预置（operator-provisioned）**：Splunk HEC、Microsoft Sentinel（Logs Ingestion / DCR）、
Datadog Logs、New Relic，或通用的 HTTPS 采集器。引擎打开已封存的凭据并掌管传输；
渲染器不持有任何状态、不持有任何凭据，因此单个实例即可服务每一个租户与 sink。

## 有界上下文，直说

- 它 **转发**，不存储。没有 sink 订阅的租户即为空操作：什么都不会入队，游标仍会前进，什么都不会丢失。
- 转发从游标遍历中运行，**在账本封存事务之外**——网络写入绝不会处于封存路径中。
- 这是一次 **推送到你的系统（push to your tower）**，区别于只读的
  [态势导出（posture export）](/zh/reference/modules/posture-export/) 拉取。系统侧的摄取超出范围；
  我们渲染为已发布的格式并交付。

## 相关内容

- [事件平台（Eventing）](/zh/reference/modules/eventing/) —— 本模块渲染所写入的持久化订阅面（重试/退避、DLQ、游标重放）。
- [合规（Compliance）](/zh/reference/modules/xiii-compliance/) —— 已封存、由账本推导的证据包，此数据流对其形成补充。
- [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/) —— 当你无法预置原生 sink 时的文件尾随（file-tail）路径。
- [诚实与限制](/zh/start/honesty-and-limits/) —— “至少一次”与“运维方预置”对本面意味着什么。

---
title: Olivares AI 是什么？
description: >-
  集成、管理并保护你所运行的 AI，从一台机器到整套基础设施 —— 唯一可信事实源：
  Claude Code 处于最深层次，Codex 与 Grok Build 并列其旁。单一的自托管二进制文件，
  为你的 AI 提供上下文、资源访问与受管会话，并为你提供运行它所需的权限、策略、预算与
  审计证据，使其遍及你的基础设施。产品没有强制遥测，控制平面默认也不产生出站流量；
  只有你明确配置为跨越边界的内容才会跨越你的边界，例如对你的模型 API 的调用和你接入的
  SIEM/webhook 输出。
---

Olivares AI **集成、管理并保护你所运行的 AI** —— 无论是在一台机器上，还是遍及整套
基础设施；唯一可信事实源：Claude Code 处于最深层次，Codex 与 Grok Build 并列其旁，
与它们互补而非竞争。随着你让越来越多的模型、agent、MCP 服务器和工具在真实、异构的
基础设施上运转，两件事会同时变难：让 AI 真正发挥作用，以及让它处于可控之下。
无论是一台自托管机器，还是一整套受监管的基础设施，情况都是如此；两者只有规模之别，
并无性质之分。

Olivares AI 两者兼顾。一方面，它给你的 AI 提供工作所需的东西——上下文、对正确资源的访问、受管会话。
另一方面，它给你提供运行这一切所需的**细粒度权限、策略、预算与审计证据**：哪个模型和 agent 能触及什么、
它们接触的数据、它们被允许执行什么、它们花费多少，以及你可以交给监管机构的证明。

一切都作为一个**单一的自托管二进制文件**运行在你自己的主机上。产品没有强制遥测，控制平面默认也不产生
出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、你接入的 SIEM/webhook
输出，以及你配置时使用的外部嵌入提供商。这是架构和你的配置所具有的属性；它是一项说明，**并非保证**。

## 一项能力：读/写访问图

在这些能力之中有**R/RW 访问图**。对于每个发起方（一个 agent、一个非人类身份、一个会话），它都会向它所触及的
每个资源建立一条边，分类为 **read**、**write**、**read-write** 或 **unknown**，并标注：

- **信号来自何处**（`SignalSource`）——来自协作式 agent 的 OpenTelemetry、Postgres 的 pgAudit
  READ/WRITE 分类、一条 AWS CloudTrail 记录、内核级的 eBPF/Tetragon 兜底、一条 MCP 注释
  （被视为**不可信**并需佐证，绝不单独信任）、一条声明的策略授权，或一条 agent 间（A2A）信号；以及
- **该归因可信到何种程度**（`Confidence`）——当它牢固地绑定到某个按 agent 区分的身份时为 `attributed`，
  当它是推断而来时（一个共享服务账户，或一个有损存储）为 `approximate`。

其核心是这个差异：**Permitted vs Observed**。Permitted 边来自声明的授权；observed 边来自真实的遥测与审计。
将二者比较会呈现*意外访问*（某 agent 读取了一张它从未被授予的表）、*未使用的授权*（某权限没有任何 agent
行使过）以及*待调和*的边（系统尚无法牢固归因的访问）。

产品对**保真度是诚实的**。覆盖度是**分级的**：在具备原生审计的存储上为 clean（SQL、对象存储、数据仓库），
在某些存储上为 lossy（文档/向量），在另一些存储上则无法被动重建（例如 Redis、SQLite、D1）。在无法确定
读写性质之处，mode 为 `unknown`——产品绝不捏造分类。

## 一个平台，而非单一功能

访问图只是众多能力之一。该产品是一个**模块化平台**（精神上类似 Grafana 或 Backstage）：一个引擎加上模块再加上
connector，其设计使得任何模块都能在无需重构其余部分的情况下接入。它内置 **30 个模块**——清单与实时会话、
R/RW 图、agent 编排（A2A，开发中）、MCP 与技能管理、身份与非人类身份、部署、知识与上下文、安全与 guardrail（护栏）、
模型与提供方管理、成本/FinOps、evals 与测试沙箱、red-teaming、合规与证据、内部目录、输出集成与 SIEM 推送、
voice/realtime，以及健康/SLA——再加上不计入这 30 个模块的平台能力（它自己的 API 与 manage-as-code、多租户、高管仪表盘）——涵盖 **158 项集成**（该数字由 `scripts/check-public-counts.sh` 从代码中测得）。
少数能力在 provisioned 之前是 pre-v1 或 deny-closed 的接缝；文档会明确说明是哪些。

完整列表参见[模块目录](/zh/reference/modules/overview/)，引擎与模块如何组合在一起参见
[架构概览](/zh/explanation/architecture/overview/)。

## 它如何观测：read-first、minimal-data

Olivares AI 是 **read-first（读优先）**的：引擎通过日志、OpenTelemetry 与 eBPF 进行观测；它**不**位于
agent 的数据路径上，因此 collector 故障绝不会破坏你的生产流量。而且它在设计上是 **minimal-data（最小数据）**的：
访问图存储的是**关系**——发起方 → 资源、读/写、信号源、置信度、时间戳——**绝不存储有效载荷、SQL 主体、密钥或 PII**。
未被存储之物无法泄露。

这也是它可自托管且对 air-gap（隔离网络）友好的原因：产品没有强制遥测，控制平面默认也不产生出站流量。
只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、你接入的 SIEM/webhook 输出，
以及你配置时使用的外部嵌入提供商。Olivares AI 不在该列表中：供应商从不位于数据路径之上。只有在你
主动向我们索取时才会联系我们——`olivares upgrade`，或按订阅下载商业附加组件及其更新——而绝不会作为
运行的副作用发生。 而且 `olivares upgrade --endpoint` 可以把这唯一的外呼也指向你自己的镜像。这对数据驻留、GDPR
与隔离网络环境而言是一个有力的理由。

## 接下来去哪

- **试用它：** [zero-to-graph 教程](/zh/tutorials/zero-to-graph/)会启动这个单一二进制文件，并到达一张已填充的
  Permitted-vs-Observed 图。
- **理解它：** [架构概览](/zh/explanation/architecture/overview/)与
  [安全与威胁模型](/zh/explanation/security/threat-model/)。
- **运营它：** [自托管](/zh/how-to/self-hosting/)与
  [隔离网络安装](/zh/how-to/air-gap-install/)。

:::note[状态]
Olivares AI 处于 **pre-1.0**。这个单一二进制文件今天即可构建、启动并到达一张已填充的访问图
（这一点由测试套件端到端地验证），但若干能力处于设计阶段或属于 post-v1。文档会明确区分现在已运行的与计划中的——
参见[诚实度与限制](/zh/start/honesty-and-limits/)。
:::

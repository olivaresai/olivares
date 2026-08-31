---
title: "Live-ingest——进程内观察生产者"
description: >-
  30 个模块之一："实时旁路（live-tap）"生产者，发布进程外连接器无法发射的侦测事件。
  默认拒绝（deny-closed）且最小数据：它不搬运任何原始内容，且它所拥有的每一个观察侧都是
  如实留空而非伪装。部分实现——它需主动启用且受环境变量门控。
---

Live-ingest（`modules/liveingest`）是 30 个已接入模块之一——它是一个**进程内生产者**，而非一个
能力槽位。它不属于历史编号图 I–XXIII。它存在只有一个架构上的理由：一个进程外的
`SourceConnector` 只能通过其 gRPC 契约
流式传输封存的观察总和（边 / 成本 / 发现），而该契约没有事件 RPC、也没有文本字段——因此它
**无法发布侦测事件**。只有进程内模块才持有总线发布能力，因此 live-ingest 是为那些已经在消费
这些事件的模块发射它们的"实时旁路"那一半。

## 它是什么

控制平面的 Claude 遥测连接器作为嵌入式插件运行于进程外；其 `Gather` 流只携带冻结的
`Observation` `oneof`。该传输契约被有意冻结（经破坏性变更检查；参见
[API 稳定性策略](/zh/reference/api-stability/)）且不携带任何摘录或文本界面。Live-ingest 是补足
连接器在结构上无法提供的两个事件的进程内生产者：供 [模块 IX](/zh/reference/modules/ix-security/)
使用的 `guardrail.observed`，以及供模块 XVI 使用的 `voice.telemetry.observed`。它不拥有实体、
也不拥有 REST 界面；它是一个面向 [事件总线](/zh/reference/events/) 的发布者。

## 它产出什么——`guardrail.observed`

这是为那条已在消费 [`guardrail.observed`](/zh/reference/events/) 的安全检测器链补足的缺失生产者。
它**默认拒绝且需主动启用**：

- **默认（检查关闭）。** 本模块不订阅任何内容、不发布任何内容，并将其留空的那一半可见地记录
  下来——绝非静默的无操作（no-op）。
- **在运维方主动启用之后。** 它订阅 `edge.observed`，并针对资源为已解析工具引用的边，派生出
  一份**有界的、已脱敏的** `tool_args` 摘录，并将其作为仅携带非敏感引用字段的 `ObservedText`
  发布。该摘录是连接器已在源头脱敏的资源*标识符*（一条净化后的路径、一个去除查询串与凭据的
  主机+路径、一个丢弃了参数的 Bash 程序名、一个 MCP 工具引用）。Live-ingest 对其加以约束，
  安全链再次对其钳制——三重防御。参数的**内容在连接器处被丢弃，绝不抵达总线。**

随后检测器链会基于真实流量自动为每项检测发射一个发现。

## 它产出什么——`voice.telemetry.observed`

一个仅面向已列入允许列表的语音/实时回合元数据的、已接入的进程内生产者——绝非音频、也绝非
转录文本。其载荷是一个类型化的值，在构造上无法携带音频、转录或 PII，且消费方会拒绝任何带有
允许列表之外键、或缺失会话/智能体引用的样本。由于本次构建中没有语音实时后端，**没有任何东西
调用它**：观察那一半如实休眠，并且在某个后端为其供给数据之前不伪造任何遥测。

:::caution[诚实的边界]
- **默认拒绝。** 除非运维方显式主动启用，否则 `guardrail.observed` 不发布任何内容；留空的那一半
  会被记录，而非隐藏。
- **检测覆盖很窄，并如此声明。** 由于进程内只能获得已脱敏的参数*引用*，该界面上现实可行的检测
  是嵌入在引用中的 PII 或密钥，以及异常/敏感资源模式。**提示注入与越狱遥不可及**——它们需要
  参数*内容*，而连接器将其丢弃。`input` / `output` / `tool_result` 界面需要一个进程内内容源，
  而本次构建在进程外传输与冻结的传输契约下并不具备它。
- **语音遥测处于休眠。** 本次构建中不存在实时后端，因此那一半不产出任何内容，而非凭空发明样本。
- **它绝不搬运原始内容，也绝不扩大连接器的捕获范围。** 最小数据是传输本身的属性，而非叠加在
  其上的一项设置。
:::

## 相关内容

- [事件总线参考](/zh/reference/events/)——`guardrail.observed` / `ObservedText` 载荷（一份位于
  JSON 回退上的脱敏摘录，而非封存的总和）以及 `edge.observed`。
- [模块 IX——安全、护栏与审计](/zh/reference/modules/ix-security/)——消费本模块所发布的
  `guardrail.observed` 馈送的检测器链。
- [模块 XVI——语音与实时智能体](/zh/reference/modules/xvi-voice/)——（休眠中的）
  `voice.telemetry.observed` 那一半的消费者。
- [模块 II——实时运行与会话](/zh/reference/modules/ii-sessions/)——它直接从自身已消费的信号中派生
  出自己的 `goal` / `agent_ref` / `summary`，而非经由 live-ingest 事件。
- [模块目录](/zh/reference/modules/overview/)——30 个模块，以及本进程内生产者所支撑的诚实的
  治理/观察 vs 执行（Govern/Observe-vs-Actuate）拆分。
- [架构概览](/zh/explanation/architecture/overview/)——进程内模块与进程外连接器所处的位置。
- [诚实与边界](/zh/start/honesty-and-limits/)——为何留空的那些侧是声明出来的，而非伪装的。

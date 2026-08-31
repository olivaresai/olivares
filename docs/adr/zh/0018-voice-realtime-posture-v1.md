> 机器翻译。英文版本为权威来源。

# ADR-0018: 实时语音后端——v1 中以休眠态记录，集成留待 v1 之后

- **Status:** accepted
- **Date:** 2026-06-12
- **Deciders:** Fran Olivares
- **References:** `modules/liveingest/voice.go:28`
  (`PublishVoiceTelemetry`), `modules/voice`（模块 XVI）

## 背景与问题陈述

语音遥测探针已端到端构建并通过验证：`liveingest.PublishVoiceTelemetry`
将一个经过白名单校验的 `voice.Telemetry` 以 `voice.telemetry.observed` 形式发布，模块 XVI 通过一个严格重新校验的消费者将其折叠进会话元数据。在任何生产路径中都没有调用该生产者——构建中并不存在实时语音后端——因此观测端（observe）那一半是空的。
它是一条纯粹的接缝（seam）。问题在于：现在就集成一个后端（例如 LiveKit），还是先声明这一态势？

## 决策结果

**v1 以休眠态交付该探针，并明确说明这一点。** 这种诚实的态势已经在代码中得到强制：
生产者拒绝可丢弃的样本，且不臆造任何内容；liveingest 的 `Start` 会记录日志 "voice
telemetry probe wired but dormant — no realtime voice backend in this build emits turn metadata"；
观测端那一半保持明显为空，而不是虚假地显示为满（绝不出现无声的缺口——
同样地，也绝不臆造出虚假的充实）。集成一个具体的实时后端（LiveKit 或
等价物）是一项 **v1 之后的会话工作，当且仅当存在需求时**。

扩展（scale-out）工作在推进过程中让这条接缝在多节点下也保持诚实：未来的调度器若在任意节点上向该探针馈送数据，现在都能通过 NATS 桥接到达 leader 的 voice 模块（组合根注册了 `voice.Telemetry` 载荷解码器），因此这条休眠接缝不会在 HA 下无声地退化为仅限单节点的接缝。

### 影响

- **好处：** 没有投机性依赖；该接缝已经过测试（生产者 + 消费者 + NATS 桥接
  解码器），因此未来的集成只是组件装配工作，而非设计工作。
- **坏处 / 权衡：** 语音观测面板在 v1 中保持为空——在 UI 契约中
  记录为一条已声明的接缝，这正是其真实状态。
- **中性：** 该决策由需求门控，而非由架构门控。

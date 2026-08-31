---
title: 术语表
description: >-
  本产品的词汇，精确地：access map 及其诚实维度、观测类型、治理原语，以及运维术语
  —— 每个都按引擎实际使用它的方式来定义。
---

术语按引擎使用它们的方式定义——其中若干刻意比业界用法更窄，而这种窄正是要点所在。

### Access map（R/RW map，读写访问图）

模块 III 的图，包含**发起方**（agent、identity、session）与它们所触及的 **resource**，每条 edge
都按 [mode](#mode) 分类，并标注其[signal source](#signal-source信号源)、[归因](#attribution归因置信度)
与[覆盖层级](#coverage-tier覆盖层级)。一项关键的差异化能力——30 个模块之一，而非整个产品。见
[什么是 Olivares AI？](/zh/start/what-is-olivares-ai/)。

### 作动状态：`v1` / `on-demand` / `seam`

每个模块*行动*那半的三种诚实状态。**`v1`**——在默认二进制中实时可用，无需预配。**`on-demand`**
——已构建并装配，但在运营方预配之前为 deny-closed 或降级（deploy apply/retire、orchestration
fire、voice dispatch）。**`seam`**——一个无后端的已声明接口。[模块目录](/zh/reference/modules/overview/)
标记每个模块；CI 中的一个回归守卫使该表保持诚实。

### Agent

一个 AI 系统（一个编码 agent、一个服务 agent、一个被编排的工作流步骤），作为一等实体受治理，
与它所运行所凭的 [identity](#identity--nhi)（凭据）相区别。将 agent 绑定到 identity 正是锐化
[归因](#attribution归因置信度)的途径。

### Agent sprawl（agent 蔓延）

分析师术语，指 AI agent、copilot 与 MCP server 在一个组织内扩散的速度快于任何人能维护清单——
带未知访问的未知 agent。这正是 [access map](#access-maprrw-map读写访问图) 与发现旨在使其可见的问题。见
[分析师词汇](/zh/explanation/positioning/analyst-vocabulary/)。

### AI TRiSM

*AI Trust, Risk and Security Management*（AI 信任、风险与安全管理）——一个**由 Gartner 提出并拥有**
的、用于治理 AI 信任、风险与安全的框架。我们将自身能力映射到它的**主题**（治理、运行时检查、
运行时执行、信息治理）；我们**不**复制 Gartner 的确切模型、不声称符合性、也不暗示背书——该分类法
是 Gartner 的专有研究。见 [分析师词汇](/zh/explanation/positioning/analyst-vocabulary/)。

### Approval（审批，HITL）

执行某项受控动作的受治理请求，以 **deny-closed 且限时**开启，绑定到确切的计划，由有权限的人类在
服务端强制执行职责分离与过期后裁决，并记录到 [ledger](#audit-ledger审计台账)。见
[配方](/zh/how-to/cookbook/hitl-approvals/)。

### Attribution（归因，置信度）

一次已观测访问与某个*特定*发起方绑定得有多牢：**`attributed`**（轨迹中存在每个 agent 的身份）
或 **`approximate`**（推断——一个共享 service account、一个有损存储、一个尚未绑定到 agent 的内核
进程）。图显示该级别，而非编造确定性；console 同时把 attributed edge 渲染为*确凿*。提升归因是一个
身份问题：[SSO/SCIM & 身份 source](/zh/how-to/connectors/sso-scim-identity/)。

### Audit ledger（审计台账）

每项治理决策与每次特权读取的仅追加、hash-chained 记录，由 Ed25519 签名保护——每条记录携带
`seq`、`prev_hash`、`hash`、`sig`，因此改写历史在密码学上可被检测。它绝不包含 PII。以 pull 导出、
push sink 与离线验证（`olivares audit verify`）形式暴露。

### Break-glass（破玻璃）

针对*特定*受控动作的受治理、可审计的紧急提权——刻意**不**对一切可用：重新启用一个
[kill switch](#kill-switch终止开关) 或终结一个 identity 的生命周期，绝不能被破玻璃绕过。

### Checkpoint（检查点）

按间隔（默认 1h）写入的、覆盖某 tenant 台账链的已签名锚点。检查点与公钥的一份**离箱**副本，正是
在 host 被攻陷后使验证抗攻击的依据。

### Collector（采集器）

仅推送的边缘进程（`olivares collector`），在被观测系统附近运行 [source](#source)，并将观测经 gRPC
（可选 mTLS）推送到核心。Collector **没有入站监听器**。

### Cooperative path（协作路径）

依赖 agent 报告的观测——OTLP 遥测、hooks。存在时保真度最高，结构上可规避，这正是
[内核后备](#kernel-backstop内核后备)与存储原生审计并存其旁的原因。

### Coverage tier（覆盖层级）

一个*resource* 信号的保真度，与归因正交：**clean**（原生审计逐字分类 R/W——pgAudit、CloudTrail）、
**lossy**（edge 落地但不精确）、**opaque / impossible passively**（无可用被动审计面——本产品如实
说出而非猜测）；**mixed** 标记一条由不止一个层级构建的 edge。

### Demo estate（演示 estate）

合成 estate `serve --seed-demo` 通过**真实**事件总线加载（仅 loopback、公开源码树密码、拒绝
非 loopback 绑定）。一个学习工具，绝非安装路径。

### Destination（输出连接器，目的地）

连接器目录的投递那半：Slack、Teams、PagerDuty、webhook、Splunk HEC、ServiceNow、Jira、email
及同类——它们投递 finding 与通知，且因为什么都不观测而没有覆盖层级。

### DR bundle / KEK

加密的、**台账连续性安全**的备份，由 `olivares dr backup` 产生；封存于一个 key-encryption key
（口令派生或 KMS 提供）之下，该 key 必须与 bundle 分开传输。见
[备份与恢复](/zh/how-to/backup-and-restore/)。

### Drift（least-privilege drift，最小权限漂移）

[Permitted 与 Observed](#permitted-vs-observed) 之间的差异：已授予与已行使访问之间的鸿沟。三类——
**unexpected access**（已观测，从未授予）、**unused grant**（已授予，从未观测）、
**reconciliation pending**（已观测，身份链接未解析）。[分诊配方](/zh/how-to/cookbook/drift-triage/)。

### Edge / cost / finding

一个 source 所能发出的观测类型的**封闭集合**：一个访问关系、一项用量成本事实、或一项侦测 finding。
设计上封闭——一个连接器不能发明新类型，这正是使 minimal-data 契约可强制执行的所在。

### Estate

你在一次部署中所治理的一切：agent、identity、MCP server、model、resource 及它们的关系，
横跨你所有的组织。

### Finding

一项 guardrail / 态势 / red-team / forensic 观测，携带任何敏感明细的哈希而非明细本身。路由到通知轨道
并送往 [SIEM sink](/zh/how-to/cookbook/push-to-siem/)。

### Guardian agent（守护 agent）

**Gartner** 的术语，指监视或干预*其他* AI agent 的 AI。Olivares AI 交付该类别的**治理结果**——
观测、对 permitted-vs-observed 作差、deny-closed 门控、不可变记录——但作为一个**位于数据路径之外的
读优先 control plane**，而非一个内联站岗的 LLM。见
[分析师词汇](/zh/explanation/positioning/analyst-vocabulary/)；与产品内的
[guardian loop](#guardian-loop守护回路) 相对照。

### Guardian loop（守护回路）

一项治理规则，监视 finding 并自动启动遏制——包括 [kill switch](#kill-switch终止开关)——其自动路径走与人类
停止完全相同的那道门控。

### Identity / NHI

一个持有凭据的 principal：人类，或**非人类身份**（service account、workload identity、API key、
agent identity）。名册来自[身份 source](/zh/how-to/connectors/sso-scim-identity/)；将它们绑定到 agent
是从观测到治理的桥梁。

### Kernel backstop（内核后备）

非协作的观测路径：Tetragon 在 agent 控制之外捕获内核文件/网络事件；`ebpf` source 消费其导出。
在某个 identity 把进程绑定到一个 agent 之前，始终为 [`approximate`](#attribution归因置信度)。见
[eBPF/Tetragon](/zh/how-to/connectors/ebpf-tetragon/)。

### Kill switch（终止开关）

estate（或每个 agent）的紧急停止：一次 admin 层级的调用即终止每项受治理作动，fail-closed；重新
启用需要两个不同的人类外加一次事后复核，且其周围没有任何 break-glass。
[演练配方](/zh/how-to/cookbook/kill-switch-drill/)。

### MCP annotation（MCP 标注）

一个 server 自我声明的 `readOnlyHint` / `destructiveHint`——**按 MCP 规范不可信**，仅作为声明
能力提示摄取（`approximate`，既非已观测也非 permitted），需佐证且绝不单独信任。见
[MCP 治理](/zh/how-to/connectors/mcp-governance/)。

### Minimal data（最小数据）

传输协议层属性，即观测携带标识符与分类，绝不携带 payload、SQL 主体、prompt、secret 或 PII。这是连接器
词汇表的一项属性，而非一项设置。

### Mode

一条 edge 的读/写分类：`read`、`write`、`readwrite` 或 `unknown`——逐字取自信号且**绝不推断**；
`unknown` 是一个诚实答案，而非一个缺失的答案。

### Observed / Permitted

见 [Permitted vs Observed](#permitted-vs-observed)。

### Opaque tokens（不透明令牌）

本产品的凭据：随机、可吊销、服务端验证的 token（`olvs_…` session、`olvk_…` API key、`olst_…`
一次性 setup token）——刻意不是 JWT，因此持有签名 key 绝不能铸造访问。

### Organization（tenant，组织）

隔离边界。每次模块读写都按 tenant 限定范围；在 Postgres 上，row-level security 作为后备
（引擎拒绝以一个可能绕过 RLS 的角色运行）。

### Permitted vs Observed

access map 作差的两半：**permitted** edge 来自已声明的 grant 与策略；**observed** edge 来自遥测
与原生审计。这一差异即 [drift](#driftleast-privilege-drift最小权限漂移)。

### Sealed admission（封存准入）

针对进程外连接器插件的 deny-closed 信任门控：固定 digest + Sigstore 证明，针对运营方固定的信任锚
验证，无任何逃逸口。见 [构建一个连接器](/zh/how-to/build-a-connector/)。

### Setup token（设置令牌）

首次引导时打印到 stdout 的单次使用 `olst_…` token——整个 bootstrap 凭据故事；没有任何默认凭据。
仅存储其哈希。

### Signal source（信号源）

哪个观测者产生了一条 edge：`pg_audit`、`cloudtrail`、`otel`、`ebpf`、`mcp_annotation`、一项已声明
策略 grant、一个 A2A 信号。来源绝不被折叠：一次 pgAudit READ 与一个 MCP 提示不是同一种证据。

### Sink

一个 eventing 订阅，以其方言（Splunk HEC、Sentinel DCR、Datadog、New Relic，或一个通用的 HMAC
签名 webhook）向某个 SIEM 投递事件，采用 OCSF/CEF/LEEF/syslog/OTLP/JSON。见
[push to SIEM](/zh/how-to/cookbook/push-to-siem/)。

### SLI / SLO

已发布的服务水平：经 `/readyz` 的可用性、请求成功率、API 与摄取延迟 p99——单节点与 HA 层级分开且
诚实陈述。见 [监控](/zh/how-to/monitor-with-prometheus/)。

### Source

一个观测连接器：它以 config `Open`、把观测 `Gather` 进引擎的 sink，并 `Close`。引擎拥有调度、
minimal-data 词汇、Apache-2.0，绝不导入核心。见 [connect a source](/zh/how-to/connect-a-source/)。

### Stop gate（停止门控）

每项受治理作动针对 [kill switch](#kill-switch终止开关) 状态所做的执行检查——在任何其他门控之前检查，
fail **closed**（与预算检查相反，后者 fail open：一个坏掉的计量器不应导致宕机，但一个坏掉的停止
检查必须如此）。

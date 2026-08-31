> 机器翻译。英文版本为权威来源。

# ADR-0021: 作为闭源企业附加组件的持久 JetStream 事件总线后端（at-least-once + 总线边界去重）

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## 背景与问题陈述

ADR-0017 将分布式总线实现为进程内本地 fan-out + **Core-NATS、at-most-once** bridge，并且
因为 2026-06-12 的 subscriber 统计发现大多数 subscriber 无法安全处理重复事件，明确**拒绝在
v1 中采用 JetStream**（选项 C）；at-least-once 会将重复事件送入不能正确处理它们的 handler。
JetStream 被保留为“at-least-once 升级路径，**以完成 subscriber 幂等性检查为前提**”。

治理 control plane 不能悄无声息地丢失触发 DECISION 的事件。在开放 bridge 下，HA node 之间
丢失 finding.reported / cost.sampled（server restart、reconnect-buffer overflow、slow-consumer
drop）意味着悄然漏过 enforcement signal。企业级 scale/reliability 层（lever #4）需要为
enforcement-event class 消除此问题，但不能依赖 ADR-0017 所设想的逐 subscriber 幂等性检查。
重新统计确认 subscriber 依然只是“**足够**幂等”：例如 `modules/security` 通过**有界的
best-effort scan** 去重 finding，而非硬性保证（`observed.go`, `anomaly.go`）。

## 决策驱动因素

- **在 BUS 层解决非幂等问题，而不是信任 handler。** ADR-0017 以每个 subscriber 都幂等作为
  JetStream 的前提。这是横跨约 17 个 handler 的脆弱分布式不变式，任何未来修改都可能再次
  破坏，而且从未完成。在总线边界提供单一、归总线所有的去重才是持久解决方案；subscriber
  无需永远保持正确即可获得 durability。
- **无 rug-pull，无 hot-path regression。** ADR-0017 的承重约束不变：社区二进制文件中的本地
  进程内 hot path 与开放 Core-NATS bridge 必须逐字节不变。升级必须是 ADDITIVE。
- **变现时机（ADR-0020）。** Durability/HA 是 enterprise-tier lever。在私有仓库拆分使 tag 成为
  真正边界后，它以 `enterprise` build tag 后的闭源代码交付。

## 考虑过的选项

- **A. 对所有 type 使用 JetStream 替换 bridge。** 否决：这会让可容忍丢失的高流量 observation
  （edge/metric）经过 RAFT storage，并改变开放 bridge 的行为（rug-pull）。
- **B. 仅对 ENFORCEMENT class 使用持久 JetStream，其余部分嵌入开放 bridge（选定）。**
- **C. 在 store 中设置持久的逐 subscriber 去重 table。** Fase 1 否决：enterprise-only table 会
  破坏 open≡enterprise schema-parity gate，而开放 table 比保障所需的改动更重。去重 state 改存
  JetStream KV（无 store，无 schema 变化）。

## 决策结果

选择 **B**：闭源附加组件 `enterprise/durablebus`（`//go:build enterprise`，
`LicenseRef-Olivares-Commercial`）**嵌入**开放 `*natsbus.Bus`，并为 **enforcement set**
（`finding.reported`, `cost.sampled`, `guardrail.observed`, `approval.requested`,
`policy.changed`——operator 可覆盖）增加 JetStream path。机制如下：

- **同级 subject namespace。** 持久事件发布到 `<durable_prefix>.<type>`（JetStream stream、
  RAFT、replicas ≥ 3），与 Core bridge 的 `<subject_prefix>.>` 互不相交，因此每个 type 恰好
  由一种 transport 交付，而非两种。嵌入的 bridge 被告知从 Core bridging 中排除 durable set
  （`natsbus.Options.BridgeExclude`，在开放二进制文件中 inert）。非 enforcement type 保留开放
  bridge 的 at-most-once reach（无 regression）。
- **Publish 确认 PubAck**（`Nats-Msg-Id = event.ID`）：持久事件要么持久存储，要么暴露失败，
  绝不会静默丢弃；stream 的 duplicate window 将 retry / failover 的双重 publish 合并为一个已存副本。
- **Leader-gated durable consumer**（ack-explicit），通过 `Active()` watcher 在 promotion 时绑定、
  demotion 时停止（elector 不提供 OnDemote）；server-side position 可经受 failover。Enforcement
  在整个 cluster 仅运行一次。
- **在 inject boundary 按 event.ID 去重**，分两层：in-memory time window（快速、same-node）和
  **JetStream KV** bucket（RAFT-replicated、TTL-bounded、经受 crash/restart，并跨 node 去重）。
  READ-before-inject 抑制重复，RECORD-after-inject 确保 crash 会重新 inject 而不是丢失。

**诚实语义：at-least-once，绝不声称 exactly-once。** 正常和中等降级运行中不会发生 LOSS：
record-after-inject、已确认 publish 持久化、consumer 从已 ack position 继续。唯一剩余的 loss path
受 retention 限制：stream 最多保留 message `MaxAge`（默认 72h，`LimitsPolicy`）；若超过
`MaxAge` 都没有 leader drain，stored event 将被丢弃，即 total-quorum-loss / 多日 leaderless 或
partitioned outage。该窗口通过 `olivares_durablebus_stream_pending` SLI 可观测（接近 `MaxAge`
的 backlog 可 alert），所以绝非 silent drop；operator 可提高 `MaxAge` 或恢复 leader 以保持为零。
DUPLICATE 只可能发生在两个有界窗口：≤2s leadership overlap，以及 inject 后写入 dedup record 前的
hard crash；二者都由 downstream 吸收（eventing capture 的 `(tenant_id, event_id)` index 与 security
bounded-scan dedup）。开放 bridge 保持 at-most-once 且不变。

### 后果

- **好处：** enforcement event 以 at-least-once 经受跨 node 交付，并有一项归总线所有的 dedup
  guarantee；community 二进制 byte-identical（附加组件不存在，唯一开放 seam `BridgeExclude`
  inert）；store schema 不变（dedup 位于 JetStream KV），schema parity 不受影响；fail-boot-closed
  （声明的 durable backend 无法建立时中止 boot；无许可证的 enterprise binary 会**明显地**降级到
  开放 Core-NATS bridge，绝不会悄然降到 single-node）。
- **坏处 / 权衡：** durable delivery 在 publish 时增加 JetStream round-trip（PubAck），inject 时增加
  KV read；对于中等流量 enforcement class 可接受，operator 也可缩小 durable set。durable event 仅通过
  consumer 在 leader 上到达 subscriber，因此 node 自己的 durable publish 不会本地 fan-out（符合
  “enforcement 仅在 leader 上”）。总线 license gate 位于 boot-time，启用 durability 的许可证安装需要
  restart，与 hot-applied 的 add-on entitlement 不同。
- **中性：** lever 的 Fase 2+（DR ladder、multi-region、per-tenant silo/CMEK）是已记录的 roadmap
  （`enterprise/durablebus/doc.go`），尚未构建。

## 为何否决了其他选项

A 对开放 bridge 进行 rug-pull 并加重 hot path；C 用 core schema 变化替代小型 KV，破坏 parity gate。
B 将变化限制在闭源附加代码内，并在总线边界解决 ADR-0017 的 duplicate-safety 问题，而非依赖从未完成的
逐 subscriber 检查。

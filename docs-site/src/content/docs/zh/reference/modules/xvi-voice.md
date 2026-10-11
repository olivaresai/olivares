---
title: "模块 XVI —— 语音与实时 agent"
description: >-
  面向对话式/实时 agent 的观察与治理平面。它在默认 DENY 的策略下治理谁可以
  开启一个语音会话、使用哪个模型和提供方——并跟踪会话元数据，同时硬性禁止
  任何音频或文字记录内容。
---

模块 XVI 治理**对话式与实时 agent**。它是一个**观察与治理**平面：它**不**重新实现语音 SDK
（Realtime API、WebRTC、ASR 或 TTS），也从不自己开启媒体流。它决定*谁*可以开启一个语音会话、
使用*哪个*模型和提供方、在*哪条*策略下，并跟踪该会话的元数据——绝不跟踪其内容。

## 它是什么

开启一个语音接口被视为一个**特权操作**，而非一项自由操作。策略是**默认 DENY**：
一个没有任何允许策略的会话会被拒绝。一次开启是**两阶段**的，并通过
[审批门](/zh/how-to/govern-and-approve/)进行 **human-in-the-loop** 门控；它绑定到一个 `plan_hash`，
因此一次审批不能被静默升级为一个更强的模型（anti-TOCTOU），归属于**真实主体**审计
（绝非 `system`），并以**仅追加**方式留存证据。模块本身从不调用提供方——
执行经由一个单独的分发接缝离开。

另一半是**观察**：本模块只跟踪会话元数据——派生状态（live/idle/ended，在读取时由活动新近度计算，
不存储生命周期列）、轮次计数、时长、延迟（来自真实样本的诚实平均值与最大值），以及 BCP-47 语言。
据此它提出治理 **finding**：当遥测命名了一个没有任何策略允许的 agent/模型/提供方时提出一条策略违规，
当延迟越过某条策略 SLA 时提出一条延迟降级 finding，以及当尝试开启而没有配置任何门时提出一条
未治理开启 finding——该缺口被暴露，且该开启仍被拒绝。

## 契约与实体

本模块在共享数据模型中声明了三个实体：

| 实体 | 可变性 | 用途 |
|---|---|---|
| **session** | 可变（upsert） | 会话元数据；**零内容** |
| **policy** | 可变 | 治理声明——谁可以用哪个模型/提供方开启（默认 DENY） |
| **decision** | **仅追加** | 开启/关闭决策的不可变 ledger |

一条策略按 agent、允许的模型和允许的提供方匹配（各项可为具体值或通配符），并带有可选的
会话分钟数与延迟 SLA 边界。**没有匹配策略即表示 DENY。** decision ledger 记录每个 `open_request`、
`open` 和 `close`，连同其策略裁决、门状态和结果状态。读取访问需要 viewer 角色及以上；
声明一条策略和开启一个会话是管理性的、租户级的、受审计的操作。这些模块路由发布在
独立的 **beta** [模块路由参考](/reference/api-beta/)中，而非稳定核心契约中——
它们的字段级形状存在于产品的类型化接口中。
此处**没有**金额；FinOps（模块 XI）拥有成本。

## 它消费什么、产出什么

本模块拥有一个 deny-closed 的摄取接缝——它自己的 `voice.telemetry.observed` 事件——
一个**进程内**探针将通过它馈送会话元数据。该传输层**在构造上即为最小数据**：遥测解析器携带一份
允许列表，并在看到一个被禁止的键时**拒绝整个事件**，因此任何音频、文字记录文本、ASR/TTS 文本、
prompt/响应内容或说话者 PII 都绝不会被持久化。唯一保留的文字记录信号是一个*外部*文字记录**定位符**的
单向哈希——证明文字记录存在，而绝非文字记录本身。治理 finding 以
[`finding.reported`](/zh/reference/events/) 的形式在提交之后带着哈希化的细节发出。

## 执行状态

受治理的开启以 **live** 方式分发。运营者预配语音 dispatcher 后，获批开启会铸造一个**服务端临时凭据**，并返回它和连接坐标。语音与轮次检测由运营者的会话配置提供；已配置模型覆盖请求模型。未配置模型时，dispatcher 使用租户策略允许的请求模型。提供商主密钥绝不离开服务器。未预配时，分发接缝采用 **deny-closed**：获批开启被如实记录为“已声明，未开启”，而不伪造成功。

## 配置并测试受治理的开启

用 `olivares modules on voice` 启用现有模块。运行模块集合改变时，引擎保存选择并重启一次；`olivares modules ls` 显示是否运行。模块规范要求 voice 依赖 FinOps 和 governance。关闭 voice 会保留策略、会话元数据和决策账本。

dispatcher 在引擎主机上通过 `OLIVARES_VOICE_DISPATCH_CONFIG` 预配，其值为运营者拥有的 JSON 文件的绝对路径。仅允许引擎账户读取该文件；提供商主密钥属于该文件，不得放入 CLI 参数、策略行或客户端连接包。OpenAI adapter 的格式如下：

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

为引擎服务设置环境变量并重启。提供了不可读或无效的 JSON 文件会使启动失败。没有 dispatcher 配置时，保持“已声明，未开启”行为。运营者文件选择 provider adapter 和会话设置；租户 voice 策略另行授权请求的 agent、模型和提供商。两者使用相同的模型与提供商引用。

用 `olivares login` 登录后，声明策略并请求批准：

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

首次请求返回 `op_status: requested`、`approval_ref` 和 CLI 退出码 7，不打开媒体连接，也不铸造提供商凭据。请所需的独立批准者通过 governance 批准页面，或 `olivares governance approvals approve <approval-ref>` 批准该引用。默认本地批准桥接的新请求中，请求者不能批准自己的请求，包括使用同一账户的其他凭据。用 `--approval-ref <approval-ref>` 重复相同的开启请求。策略拒绝或批准仍待处理时返回 403 和 CLI 退出码 3；adapter 失败返回 502。预算和 estate-stop 检查仍适用。

成功的已配置请求返回 `op_status: dispatched`。其 `dispatch_ref` 是 JSON 字符串，包含短期 `credential`、`connect` 坐标、`transport`、模型和到期时间。将该响应视为凭据，不要贴入报告或日志。对于 OpenAI，客户端使用短期凭据，在返回的 `connect` URL 交换 SDP offer，随后拥有 WebRTC 媒体连接。仅铸造凭据不能证明媒体已连接。决策账本保留的是含凭据连接包的 SHA-256 指纹，而非连接凭据。旧存储包也在读取时指纹化；现有仅追加行不重写。普通 provider handle 保留原值。

检查保留的元数据和决策：

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

引擎重启前后使用相同数据目录。策略和仅追加决策在重启后，以及关闭再开启 voice 后，仍可用；提供商凭据仍须单独预配。上述 JSON 命令使用与 API 相同的租户限定 `/v1/m/voice` 路由。

:::caution[诚实的限制]
- **批准归因具有作用域。** 默认本地桥接为新的人类发起开启保留已认证请求者。现有批准保留存储的归因。明确配置的服务令牌批准桥接将请求归因于其服务凭据；不提供相同的发起人分离保证。
- **观测需要已配置的生产者。** 可选 OpenAI Realtime SIP call plane 使用 `OLIVARES_VOICE_CALL_CONFIG` 进行 webhook 验证、租户和项目归因，并结合 dispatcher 配置中的提供商凭据。没有这些配置或进程内遥测生产者时，观测部分保持为空。铸造 WebRTC 凭据不会填充轮次计数或延迟。gRPC 控制平面没有事件 RPC，进程外插件不能通过它发布该模块事件。
- **客户端拥有已铸造会话的媒体。** 本模块不实现 WebRTC 客户端，也不关闭客户端音频连接。可选 SIP call controller 是独立路径。使用合成音频的本地协议测试不能证明厂商语音、计费、SIP 观测或媒体拆除。
- **控制台范围独立。** voice 视图编辑策略，并显示会话、决策和元数据流。dispatcher 预配与客户端媒体连接在视图之外；API 或 CLI 验证不证明浏览器动作。
- **永不含内容。** 这是传输层的一项硬性属性，而非一个设置：schema 没有内容列，解析器拒绝未知键。
  延迟以来自真实样本的诚实平均值/最大值展示——绝非编造的 p50/p95。
- **没有“停滞”finding。** 一个语音会话结束属于正常的静默（如同一个已完成的 agent）。
  在没有诚实基线的情况下，一条停滞 finding 会是误报，因此它被有意省略。
- **Pre-1.0。** 与平台的许多部分一样，本模块在深度上处于设计阶段——参见
  [诚实与限制](/zh/start/honesty-and-limits/)。
:::

## 相关内容

- [模块目录](/zh/reference/modules/overview/) —— 模块 XVI 所处的位置及其执行状态。
- [事件总线参考](/zh/reference/events/) —— `finding.reported` 携带语音 finding。
- [模块 IV —— 编排](/zh/reference/modules/iv-orchestration/) —— 同类分发接缝（实弹）。
- [模块 X —— 模型与提供方路由](/zh/reference/modules/x-models/) —— 一条策略可能允许哪些模型。
- [治理与审批](/zh/how-to/govern-and-approve/) —— 实践中的两阶段开启门。
- [诚实与限制](/zh/start/honesty-and-limits/) —— 观察/治理/执行的划分。

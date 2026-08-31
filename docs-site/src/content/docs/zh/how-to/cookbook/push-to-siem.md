---
title: "操作手册：将发现项与审计账本推送到 SIEM"
description: >-
  创建推送接收端（sink）——Splunk HEC、Microsoft Sentinel、Datadog 或 New Relic，
  或一个通用的 HMAC 签名 webhook——并将其订阅到发现项和封存的审计账本，
  以 OCSF、CEF 或你的系统所使用的格式进行至少一次（at-least-once）投递。
sidebar:
  order: 6
---

**目标：** 让你的 SIEM 以推送方式接收 control plane 的发现项*以及*其篡改可检测审计账本，
无需用 forwarder 去 tail 文件。

这是事件平台（eventing platform）上的 S2S（服务到服务）推送路径。
[拉取导出与文件 tail 这两种方式](/zh/how-to/forward-audit-to-splunk/)仍然
完全受支持——对于 WORM 归档和离线再验证，pull 仍是正确的形态；
而对于实时 SIEM 摄取，push 才是正确的形态。

## 1. 创建接收端订阅

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

- **`sink_kind`** 选择目标系统的方言：`splunk_hec`、`sentinel_dcr`、
  `datadog`、`newrelic`——或者完全省略它以使用**通用 webhook**
  （一个接收 JSON 事件的 HTTPS 端点，由引擎的 HMAC 签名进行认证；
  用 `…/{id}/rotate-secret` 轮换密钥）。
- **`sink_format`**：`ocsf`（SIEM 接收端的默认值——具备 AI 感知能力的
  schema）、`cef`、`leef`、`syslog`、`otlp`、`otlp_envelope` 或 `json`。

  :::caution[`sink_format` 需要 `sink_kind`]
  只有设置了 sink 类型时格式才会生效。**省略 `sink_kind` 并不是"选择 HTTPS"** —— 它选中的是
  通用 webhook，发送的是 Olivares 事件 JSON，而且根本不会校验 `sink_format`。要把 SIEM 方言
  投递到自己的端点，请显式设置 `sink_kind: "https"`：

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  对 `otlp`（及其完全等价的别名 `otlp_envelope`），端点必须是采集器的 `/v1/logs`
  精确路径——请求体会原样 POST 到该 URL。
  :::
- **`sink_cred`**（HEC token / DCR bearer / API key）只接受一次，
  **静态封存、绝不返回或记入日志**。厂商类型在创建时需要它；
  通用 webhook 则无需任何凭据。
- **`event_types`** 是你的流选择：`finding.reported` 对应发现项通道，
  `audit.recorded` 对应账本（见下文），或两者皆选。

在信任投递之前先做测试：

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. 对账本推送的诚实描述

订阅 **`audit.recorded`** 会开启账本泵（ledger pump）：forwarder 会从
每个租户专属的游标开始遍历该租户封存的审计账本，并将每条记录入队到
持久投递引擎——**至少一次（at-least-once）**、有序、可恢复。每条记录都携带
其链完整性字段（原样携带），因此 SIEM 侧的副本所支持的与 pull 导出完全相同，
也仅止于此：链的**衔接**（n+1 的 `prev_hash` 等于 n 的 `hash`）以及对 `hash` 的
检查点签名都可以离线校验。而且现在可以从**一行**导出内容**重新推导**出某条记录的
`hash`——链哈希的每一项输入都在传输记录中，包括规范的 `occurred_at` 文本与元数据
承诺（commitment）。目前已对 `syslog` 和三种 OTLP 写法证明了逐字节精确重新推导，
适用范围是该账本所发出的值字母表（UUID、`kind:id` actor、点分 verb、固定布局的
时间戳与十六进制摘要）：syslog 会把 CR 和 LF 替换为空格，OTLP 会替换无效 UTF-8，
因此两者都不是无条件成立。`ocsf`（接收端默认值）、`cef` 与 `leef` 携带相同字段，
但由于其转义与字段映射会损失自由文本值，目前尚无法逐字节重建；若要重新推导，
请选择一种已证明的格式。该承诺按记录加盲，因此在补全原像的同时不泄露其背后的任何
元数据。三种主张仍然彼此独立——重新推导哈希既不等于验证**真实性**（那需要一把
外部受信任的密钥），也不等于验证**完整性**（那需要相邻记录与检查点）。审计*归档*
仍然是更强的工件：它连同盲化值一起携带元数据本身，因此还能回答某个承诺覆盖的是
**哪些**元数据。

有三个值得了解的特性：

- **没有订阅，就没有工作。** 在没有 `audit.recorded` 订阅者时，泵不会写入
  任何内容——在你主动请求之前，这条路径没有任何开销。
- **至少一次意味着重投递时可能出现重复**；按每个租户的记录序列号进行去重。
- **泵在 HA 下受 leader 选举门控**——恰好只有一个节点进行转发。

## 3. ITSM：将发现项变为工单

同一套订阅机制通过通知通道驱动 ITSM 目标——由发现项生成 ServiceNow 事件
和 Jira 问题，并将严重性映射到优先级。请将其配置为通知**目标
（destinations）**（`servicenow` / `jira` 输出 connector），而非
SIEM 接收端；[Splunk 页面的目标表](/zh/how-to/forward-audit-to-splunk/)
展示了该模式。

## 端到端验证

1. `…/test` 返回已投递。
2. 触发某个可观测的事件（一个[预算告警](/zh/how-to/cookbook/budgets-and-finops-guardrails/)
   阈值、一次被拒绝的工具调用），并观察发现项到达。
3. 对于账本：将 SIEM 侧的 `seq` 高水位标记与
   `GET /v1/audit/export?from=<seq>` 进行对比——两条流必须一致。

## 备注

- 端点必须为 **HTTPS**；引擎拒绝明文接收端。
- 态势快照（合规 / NHI / 发现项汇总）有自己的导出模块，
  搭乘相同的通道——参见
  [合规模块](/zh/reference/modules/xiii-compliance/)。
- 完整的决策表——何时 pull、何时 tail、何时 push——在
  [Splunk 转发页面](/zh/how-to/forward-audit-to-splunk/)上。

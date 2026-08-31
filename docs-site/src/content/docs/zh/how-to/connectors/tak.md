---
title: "TAK Server posture 与受治理 Cursor-on-Target ingest"
description: >-
  治理 TAK deployment：离线读取 CoreConfig.xml 中的 TAK Server posture（可选
  live version probe），并通过 UDP/TCP ingest Cursor-on-Target event，作为最小
  数据的受治理 signal；坐标和 detail 绝不离开 connector，每条 edge 都如实标为
  approximate。
sidebar:
  order: 9
---

`tak` source 把一个 **TAK**（Team Awareness Kit）deployment 作为另一项 surface
加以治理。它执行两项相互独立的工作，可以只启用其中任一项：

- **TAK Server posture** — 把 server config（input 及其 protocol/port、
  TLS/keystore setting、certificate-signing backend）报告为最小数据 finding。
  **有根有据的** source 是 server 自己的 `CoreConfig.xml`，从 disk **离线**读取；
  网络上只读取可选的 live **version probe**。它不读取 TAK federation。
- **受治理 CoT ingest** — 在 connector 自己的 **UDP** 和 **TCP** listener 上接收
  **Cursor-on-Target** event，并把每个 event 转为一条受治理 access edge。

connector **读取优先**：它绝不写入 TAK Server，绝不加入 federation，也绝不重新
发出 payload。若 credential 和 listener 均未配置，它会如实地**不做任何事**：
不输出内容，而不是为从未接触的 deployment 虚构 posture。

## 它输出什么

| 字段 | 值 |
|---|---|
| Signal source | `cot` |
| Mode | `write` — CoT emitter 向 feed *贡献* situational-awareness state |
| 发起方 | emitter 的 `uid`，**默认 hash**（`cot_uid_mode`） |
| Confidence | 始终为 **`approximate`** — base CoT 未经认证（见下文） |
| Findings | drop-track cancellation、无界误差 event，以及聚合的 listener 拒绝（rate-limit / oversize / malformed / conn-limit） |

## 1. Posture：先离线读取 server

有根有据的 posture source 是 server 自己的 config file。通过 package 安装时，它
位于 `/opt/tak/CoreConfig.xml`。把 connector 指向该文件，它便能在**不接触网络**
的情况下读取已配置 input、TLS/keystore setting 和 certificate-signing backend。
`<federation>` element 被刻意排除在模型之外，因此不会产生 federation posture。

live **version probe** 是可选项，只添加当前运行版本。由于 TAK Server 使用 **mTLS**
认证运营者，此 probe 以 deny-closed 方式工作：如果开启 `posture` 并设置
`server_url`，却**省略** client certificate，connector 会**拒绝启动**，而不是
匿名 probe 并报告未经认证的 posture。`server_url` 必须使用 `https`。

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingest：通过 UDP 和 TCP 接收 CoT

启用 listener 后，connector 会接收 CoT：每个 **UDP** datagram 一条 message，每个
**TCP** connection 一条 message（“open-squirt-close”）。把 TAK feed 或 CoT client
指向 connector 的 listen address；connector 是 consumer，不会主动连接 server
来 pull 数据。

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### 配置 key（来自 connector 随附的 descriptor）

| Key | Type | Default | Secret | 含义 |
|---|---|---|:--:|---|
| `core_config_path` | string | — | no | `CoreConfig.xml` 的 path（package 安装：`/opt/tak/CoreConfig.xml`）— 有根有据的离线 posture source |
| `server_url` | string | — | no | TAK Server base URL（例如 `https://takserver.example.mil:8443`）。可选：只启用 live version probe |
| `version_path` | string | `/Marti/api/version` | no | `server_url` 上的 Marti version endpoint。可配置，因为 tak.gov 的 API reference 需要 account |
| `client_cert` | string | — | **yes** | TAK Server mTLS 的 PEM client certificate，以引用提供 |
| `client_key` | string | — | **yes** | client certificate 的 PEM private key，以引用提供 |
| `ca_cert` | string | — | no | TAK Server certificate 的 PEM CA bundle。留空时使用 host trust store |
| `posture` | bool | `true` | no | 发出 TAK Server posture finding |
| `request_timeout` | duration | `15s` | no | 对 TAK Server API 每次 request 的 timeout |
| `feed_ref` | string | `tak` | no | 此 CoT feed 的稳定引用，即 sourcescope binding 以 `source_type=data` 限定 scope 的 `source_ref` |
| `cot_udp_listen` | string | — | no | CoT 的 UDP listen address（例如 `127.0.0.1:6969`）。留空时禁用 UDP ingest |
| `cot_tcp_listen` | string | — | no | CoT open-squirt-close 的 TCP listen address（例如 `127.0.0.1:8087`）。留空时禁用 TCP ingest |
| `cot_multicast_group` | string | — | no | 在 UDP listener 上加入的可选 multicast group（TAK 的 SA 默认值为 `239.2.3.1`） |
| `cot_max_event_bytes` | int | `65536` | no | 一项 CoT event 的最大 byte 数 |
| `cot_max_detail_bytes` | int | `32768` | no | 一项 CoT event 中 opaque `<detail>` 区间的最大 byte 数 |
| `cot_rate_limit_eps` | int | `500` | no | 所有 listener 合计每秒最多接受的 CoT event 数；超出部分会 drop 并计数 |
| `cot_max_tcp_conns` | int | `128` | no | 同时存在的 TCP CoT connection 上限 |
| `cot_uid_mode` | string | `hash` | no | `uid` 离开 connector 的方式：`hash`（默认、单向）或 `raw`。uid 标识 device，而 device 标识其携带者 |

## Port（TAK Server Configuration Guide v5.2）

以下信息用于说明集成对象。connector 自己的 listener 会 bind 到你配置的任意
`host:port`；示例复用这些编号仅为便于理解。

| Port / group | 惯例 |
|---|---|
| **8089** | TLS CoT streaming input — 经认证的 client↔server channel |
| **6969** + multicast **239.2.3.1** | Situational-awareness（SA）multicast group |
| **8087** | 惯用 input port；guide 的 canonical example 将其 bind 为 **UDP**。protocol 可配置，8087 **并非**天然就是 TCP |
| **8088** | `stcp` — 未加密 TCP input，**仅供测试** |
| **8443** | 管理 web UI |
| **8446** | Certificate enrollment |

## Privacy：坐标与 detail 绝不离开 connector

CoT 是位置报告 protocol，也是本产品 ingest 的 signal 中 PII 密度最高的一种，因此
严格强制数据最小化：

- `<point>` 的 `lat` / `lon` / `hae` **绝不离开 connector**。坐标就是人的位置；
  产品只记录收到了一项 event、来自哪个 emitter、属于哪个 CoT type，绝不记录
  任何人在哪里。
- opaque `<detail>` 区间绝不离开 connector；只保留其**大小**和 **SHA-256 digest**，
  从而无需存储 payload 也能关联相同 payload。
- emitter `uid` **默认 hash**（`cot_uid_mode=hash`，domain-separated 且单向）。
  `raw` 必须由运营者明确 opt-in。

## Confidence：CoT uid 不是经认证的 identity

base CoT **不带任何认证**；任何能访问 listener 的 host 都可以声称任意 `uid`。
TAK Server 的 TLS 保护 client↔**server** channel（port 8089），但不能说明此
connector 在自己明文 UDP/TCP listener 上收到的 event。因此，来自 base CoT
listener 的**每一条** edge 都按设计评为 **`approximate`**；没有任何 code path
返回 `attributed`。

:::caution[`uid` 是声明，不是证明]
应把 CoT `uid` 理解为*“一个声称使用此 id 的 emitter 向 feed 发布了内容”*，而不是
经认证的 identity。只有 listener terminate mTLS 并把 uid 绑定到 peer certificate
时，它才会成为经认证的 identity。
:::

## Scope：使用 sourcescope binding 治理 feed

feed 是一等受治理 source。**sourcescope** binding 以 `source_type=data` 和
`source_ref=<feed_ref>` 限定谁能使用它，支持任意主体轴：**session / agent /
user / user_group / role**。effect 为 `allow`（默认）或 `forbid`；**`forbid`
是绝对的**（`forbid` 覆盖 `allow`）。

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

设置 `"effect": "forbid"`（例如搭配 `"scope_tree": "user_group"`），可以移除
整个 group 的访问权，即使已有 `allow`。

## License 与 clean-room provenance

CoT 传输格式是只依据**公开发布的 MITRE specification** 编写的 **clean-room**
implementation；没有读取、复制 TAK 或 ATAK source code，也没有从中派生：

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, 2005 年 8 月 —
  DTIC **ADA637348**，MITRE **Case #06-0249**。
- `Event-PUBLIC.xsd`，CoT base-event schema（Version 2.0）— MITRE
  **Case #11-3895**。
- *TAK Server Configuration Guide* **v5.2** — 用于 port/protocol 惯例。

ATAK-CIV 和 TAK Server 使用 **GPLv3**，connector（Apache-2.0）不得使用它们，
license boundary check 会强制这一边界。两者都有美国联邦 **“Distribution A”**
标记；这是**政府发布声明，不是 software license**，code tree 仍为 GPLv3。使
clean-room implementation 合法的是 MITRE 公开发布的 schema 与 guide。

## 如实声明的限制

- **没有 mesh/radio bearer** — 仅 UDP 和 TCP；没有 serial、TAK mesh 或 radio。
- **没有 ATAK/WinTAK plugin** — connector 不实现任何面向终端用户的 TAK client。
- **没有 TAK federation** — 只*观测*已配置 federation，绝不参与 federation。
- **没有 Link-16 / MIL-STD** 或需认证的 tactical protocol，且**没有 Iron Bank /
  DoD accreditation**；这些是独立、可选的客户路径。
- **不建模 CoT `<detail>` 子 schema** — 只解析 base event；detail 是 opaque、有大小
  上限并取 digest 的 byte。
- **UDP loss 无法计数** — backpressure 会减慢 listener；对于 UDP，**kernel**会在
  本 process 看到 datagram 前将其 drop，无法统计这些丢失。只有 connector 实际拒绝
  的 event 才会聚合到 rejection finding 中。

## 相关页面

- [连接 source](/zh/how-to/connect-a-source/) — connector model 与如实的 tier taxonomy。
- [治理与审批](/zh/how-to/govern-and-approve/) — sourcescope binding 所接入的
  authorization model。
- [Connector 与 coverage tier](/zh/reference/connectors/) — 完整 catalog。

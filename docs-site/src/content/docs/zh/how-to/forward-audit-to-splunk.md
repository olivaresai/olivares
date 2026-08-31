---
title: 转发到 Splunk（投放一个 Universal Forwarder + tail）
description: >-
  在没有原生 Splunk-to-Splunk 发射器的情况下，通过用 Universal Forwarder tail 一个文件，
  把 control plane 的治理发现项及其篡改可检测审计账本送入 Splunk。诚实地说明每条流各是什么。
---

你**今天**就可以把 Olivares AI 的数据送入 Splunk，无需等待原生 connector：
将数据写入一个文件，并让一个 **Splunk Universal Forwarder（UF）**指向它。
UF 负责到你 indexer 的 Splunk-to-Splunk（S2S）跳转。

:::caution[没有原生的 Splunk S2S 发射器]
Olivares AI **没有**实现 Splunk 专有的 S2S forwarder 协议。原生 S2S 发射器是
v1 之后的事项。受支持的方式包括**文件 tail 转发**（一个 UF tail 由 Olivares
写入的文件）、**拉取导出**（用于 WORM 归档和离线再验证），以及
**经由 Splunk HEC 的 HTTP 推送**——自 SIEM 互操作工作以来，这还包括通过事件
接收端推送**账本本身**（[推送到你的 SIEM](/zh/how-to/cookbook/push-to-siem/)）。
本页记录文件与 pull 路径；推送由操作手册介绍。
:::

存在**两条不同的流**，二者并不是同一回事。请有意识地选择：

| 流 | 它是什么 | 送入 Splunk 的方式 |
|---|---|---|
| **治理 / 发现项** | 模块 IX 路由的通知流（健康、支出、安全、合规发现项） | `filelog` 输出 connector 将其追加到文件；或 `splunkhec` 推送它；或一个订阅了 `finding.reported` 的[事件接收端](/zh/how-to/cookbook/push-to-siem/) |
| **篡改可检测审计账本** | 仅追加、hash 链式、已签名的审计轨迹 | **拉取**导出 `GET /v1/audit/export`（本页）；或**推送**泵——一个订阅了 `audit.recorded` 的事件接收端，以至少一次投递。没有原生的*文件*接收端；用下文的定时导出物化一个文件 |

## 流 A——发现项，经由 `filelog` connector

`filelog` 输出 connector 将通知/发现项流**每行一条记录**追加到文件
（或 `stdout`/`stderr`），UF 可以 tail 它。配置一个 `filelog` 类型的
通知目标，使用以下字段：

| 字段 | 含义 |
|---|---|
| `path` | 追加目标：一个文件路径，或 `stdout`/`stderr`/`-` |
| `format` | 逐行格式：`json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim`（默认 `json`） |
| `hostname` | syslog 的 `HOSTNAME` 字段（用于 `syslog` 格式） |
| `fsync` | 将每条记录刷到磁盘（为 WORM 副本提供持久性；较慢） |

对于 Splunk，`format: json`（丰富字段）或 `format: cef`/`syslog`
（Splunk 原生解析的行格式）都可用。文件以仅追加方式打开，因此当置于 WORM
存储上时，同一文件还兼作一份不可变的外部副本。

:::note[`filelog` 承载发现项，不承载已签名账本]
`filelog` connector 转发**发现项**流——它从不接触篡改可检测审计账本。
要转发可验证的账本，请使用流 B。
:::

### 一站式替代方案：Splunk HEC

如果你更愿意通过 HTTP 推送而非 tail 文件，`splunkhec` connector 会把同样的
发现项流 POST 到 Splunk 的 HTTP Event Collector（`/services/collector`），
带一个 `Authorization: Splunk <token>` 头——这是一条一站式的 HTTP 路径，
但仍非 S2S，且仍是发现项流而非账本。

## 流 B——篡改可检测账本，经由拉取导出

审计账本以**经认证的拉取导出**形式暴露，而不是引擎自行写出的文件。
每条记录都携带链完整性字段（`seq`、`prev_hash`、`hash`、`sig`），
因此你的 SIEM 可以**离线再验证 hash 链**；PII 绝不导出。

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

受支持的 `format` 值为 `cef`、`leef`、`syslog`、`otlp`、`otlp_envelope`、
`otlp_log_record` 和 `ocsf`。`otlp` 是每条记录一个完整、可直接 POST 的 OTLP/HTTP
导出请求，`otlp_envelope` 是它的精确别名，`otlp_log_record` 则是裸 LogRecord 投影（每行一个 LogRecord）。行格式
（`cef`/`leef`/`syslog`）以 `text/plain` 流式传输；`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` 以 NDJSON
（`application/x-ndjson`）流式传输，每行一个 JSON 对象。

:::note[`ocsf` 即 OCSF v1.8.0 API Activity]
本页早先的版本曾指出引擎的错误文本在所通告的列表中遗漏了 `ocsf`——
该缺口已在上游修复；摘要与错误请求消息现在都由引擎的格式注册表生成，因此始终列出全部受支持的格式。
:::

### 用游标进行增量 tail

该导出通过 `?from=` 按序列号对无间隙的链分页。为了让一个文件持续被追加以供
UF tail，运行一个小型定时作业，从它上次看到的序列号恢复：

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

每次导出都以一个完成终止符结束——文本格式为一行
`# olivares-audit-export-complete count=N last_seq=M` 注释，
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` 则为一行 `{"export_complete":true,...}` JSON。
**它的缺失意味着该流被截断了**——若缺失，请勿推进游标。

## 让 Universal Forwarder 指向该文件

无论你选了哪条流，都在主机上安装一个 Splunk UF 并添加一个
`monitor://` 输入。Olivares AI 不附带任何 `inputs.conf`——这是你需要添加的
配置段：

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

UF 通过 S2S 转发到你的 indexer；Olivares AI 自身从不讲 S2S。

## 支持与不支持内容的小结

- **支持：** 文件 tail 转发（UF tail 一个文件）——两条流皆可。
- **支持：** Splunk HEC 推送——发现项流（`splunkhec` 目标），
  **以及**经由一个事件**接收端**推送账本与发现项
  （`sink_kind: splunk_hec`，事件 `audit.recorded` / `finding.reported`，
  至少一次）——参见[推送到你的 SIEM](/zh/how-to/cookbook/push-to-siem/)。
- **支持：** 离线账本再验证——拉取导出与推送泵都原样携带 hash 链字段，
  因此 SIEM 可以再验证完整性。
- **不支持：** 原生 Splunk S2S 发射器——未实现（v1 之后）。
- **不支持：** 自动账本*文件*接收端——要将账本送入本地文件，
  请用上文的定时拉取导出将其物化（推送泵面向 HTTP 接收端，而非文件）。

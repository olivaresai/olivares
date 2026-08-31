---
title: "故障排查（症状 → 诊断 → 修复）"
description: >-
  面向运维人员的故障模式指南，提炼自产品自身的 runbook：启动与首次运行问题、就绪性失败、摄取背压、
  ledger 验证失败，以及引擎有意打印的告警。
---

每个条目都遵循相同的结构：你看到的症状、如何确认它是什么，以及修复方法。这里引用的日志行是引擎实际输出的字符串，因此你可以直接用它们做 grep。在存在更深入 runbook 的地方，条目会链接到相关页面，而非重新推导一遍。

## 首次启动与安装

### 我错过了安装令牌

重启 **不会** 重新打印它（只有令牌的哈希被存储，位于数据目录下的 `setup.token`）。在尚无任何用户存在时，恢复是安全的：停止引擎，删除 `setup.token`，再启动它 — 会铸造并打印一个新令牌。这 *仅* 在没有用户的安装上有效，因此并不构成接管路径。该令牌 **仅输出到 stdout**（systemd 下的 journal，Docker/Kubernetes 上的容器日志）— 绝不写入日志文件。

### `=== FIRST-BOOT SETUP ===` 从未出现

该数据目录中已经存在用户 — 你并不处于首次启动状态。要么用现有管理员登录，要么，若想真正全新开始，使用一个全新的 `--data-dir`。

### 引擎在首次启动时就密钥发出告警

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

两者都是有意为之，而第一个是日后会咬人的那个：**没有强制托管（escrow）** — 现在就把 `audit-signing.key` 拷贝到机器外，并把公钥（`GET /v1/audit/pubkey`）固定在机器外，否则未来一旦主机被攻破，你将无法证明自己的 ledger
（[备份与恢复](/zh/how-to/backup-and-restore/#决定一切的那两把密钥)）。

TLS 那一行会打印 **两个** 摘要，而且它们不可互换：`cert_fingerprint_sha256` 是证书摘要，也就是浏览器显示的那个；`pin_sha256` 是叶证书的 SPKI 摘要，而 `--pin-sha256` 只比较后者。请原样复制该值：

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

改为固定证书指纹并不会以「标志值非法」的方式失败——它是一个格式正确的 32 字节摘要，因此连接会被尝试并以 `TLS SPKI pin mismatch` 拒绝，而该错误会给出你本应使用的值。使用 `curl --pinnedpubkey sha256//…` 时，请补上结尾的 `=` 填充：引擎有意打印不带填充的 base64，这样该值在日志中不会被加引号、可以安全复制粘贴，但 curl 要求带填充的形式。

## 数据源与访问图

### 访问图是空的

首先检查是否接入了任何东西。引擎在启动时会明确说明：

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

缺失、不可读或无效的 sources 文件会 **告警并继续**（启动绝不会因此崩溃）— 因此一个看起来健康、访问图却为空的引擎，通常意味着配置从未加载。修复文件/路径并重启；成功的样子是每个数据源都有一行 `ingest: wired source … kind=…`。构造失败的数据源会记录 `ingest: failed to register in-process source; not wired` 并附上原因 — 它会被报告，绝不会被静默丢弃。

### pgAudit 已接入但没有边到达

三个原因几乎覆盖了所有情况，且都是设计使然
（[pgAudit 指南](/zh/how-to/connectors/pgaudit/)）：

1. **服务器没有以 UTC 记录日志。** 带有非 UTC 时区缩写的记录会被 **跳过**，而非加上错误的时间戳 — 设置
   `log_timezone = 'UTC'`。
2. **csvlog 是批量的，而非 tail。** `follow` 只对 `jsonlog` 生效；csvlog 数据源在每次扫描时摄取，而非持续摄取。
3. **被审计的类别关闭了** — 检查 `pgaudit.log` 是否包含
   `read, write`。

### 所有内容都显示为 drift（偏移）

全新安装上这是预期的：在没有声明任何授权（grant）时，每一次被观测到的访问都诚实地是「未预期的」。这是起始状态，而非缺陷 —
[对其分诊](/zh/how-to/cookbook/drift-triage/)：声明你打算授予的 grant。

## 可用性

### `/readyz` 返回 503

阅读响应体 — 它区分两种情况：

- `{"status":"unavailable","store":"down"}` — 存储不可达。在 SQLite 上：磁盘满、PVC 问题、文件权限。在 Postgres 上：可达性与凭据。**存活检查（liveness）有意保持通过**（进程仍存活），因此存储中断时不会有任何东西反复重启循环；若它持续卡住，修复存储后手动重启 pod/服务。
- `{"status":"standby","leader":false,…}` — 一个诚实作答的 HA 备机。这不是错误：Service 会路由到 leader；备机按设计将流量排空。若 **所有** 副本都报告 standby，则 leader 选举卡住了 — 检查 Postgres advisory-lock 的连通性。

### pod 挂了却没有任何东西接管

在 **默认的单副本** 拓扑下没有自动故障转移 — 恢复靠的是 StatefulSet 重新调度加上 RWO 卷重新挂载（注意 Multi-Attach 错误；该卷把恢复钉死在它所在的 AZ）。自动故障转移是
[HA 拓扑](/zh/tutorials/getting-started/kubernetes/#3-active-passive-高可用)
（Postgres + 副本 + 共享签名密钥）的特性。绝不要在禁用持久化的情况下运行生产环境：`emptyDir` 会在每次重新调度时丢失签名密钥。

## 性能

### 摄取延迟 p99 在上升（背压）

总线 **阻塞而非丢弃** — 上升的
`olivares_ingest_duration_seconds` p99 是设计好的信号，表示某个订阅者饱和了，而非数据丢失。直接点名罪魁祸首：

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

按订阅者划分的标签指向那个慢模块；
`olivares_eventbus_publish_blocked_total` 统计背压事件数。常见的根因是 **存储写入吞吐量**（SQLite 单写入者上限）— 这是容量层面的修复（迁移到 Postgres，或减少写放大），而非一个调优旋钮。慢的输出 connector（webhook、SIEM）绝不能作为同步订阅者。

启用分布式总线（`OLIVARES_BUS_CONFIG`）后，请记住跨节点桥接是 **至多一次（at-most-once）**：饱和的桥接会填满
`olivares_eventbus_bridge_pending_messages`，随后 **丢弃远程事件**，
计入 `olivares_eventbus_bridge_dropped_total` — 对任何增长发出告警，并在 `olivares_eventbus_bridge_connected == 0` 时呼叫值班。

### 登录失败并提示 "locked out"

`olivares_auth_login_attempts_total{outcome="locked_out"}` 上升意味着在反复失败后，按账号/按 IP 的限流生效了。它会自行清除；应调查失败的来源，而不是提高限额。

## 证据

### ledger 验证失败

首先，要清楚你运行了什么：默认的 `audit verify` **即使链校验失败也会以 0 退出**（结果在 JSON 报告里）— 自动化必须使用
`--strict` 或解析报告：

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

固定 **机器外** 公钥：没有固定值时，验证器会信任从（可能已被攻破的）主机读取的密钥 — 作为提示性检查尚可，但不能作为篡改可检测的证据。然后按 `reason` 字段分类：

| Reason | 类别 | 处置 |
|---|---|---|
| `hash-mismatch`、`prev-mismatch`、`head-mismatch`、`tail-truncated` | 篡改或截断 | 当作 SEV1：保全该机器，对照机器外检查点进行核对 |
| `checkpoint-sig-invalid`、`checkpoint-link-mismatch`、`event-sig-invalid` | 篡改或密钥错误 | SEV1，除非你能证明这是密钥保管的混淆 |
| `seq-gap` | 删除 **或** 恢复不一致 | 在喊篡改之前，先对照机器外检查点 |
| `event-sig-missing` | 可能是签名启用前的遗留记录 | 用 `--from` 把它界定在启用边界处；边界之前缺失是预期的 |

一份恢复出来的备份，若能通过朴素遍历却与你固定的机器外检查点不一致，就属于恢复异常的情况 — 正是为此才需要这个固定值。

### `olivares_audit_checkpoint_age_seconds` 持续增长

检查点已停止写入（默认节奏 1h；`OlivaresAuditCheckpointStale` 在 2h 时触发）。检查引擎日志中的检查点错误以及存储的可写性 — 在它增长期间，你的篡改检测锚点正在老化。

## 通知与 sink

### 某个目的地始终收不到任何东西

带有未知 kind 的目的地会被 **跳过并记录日志**
（`notify: destination has unknown connector kind; skipped` — 检查 `kind` 的拼写）。对于事件 sink，`POST …/subscriptions/{id}/test`
会发送一次你可以观察的投递，且端点必须为 HTTPS
（[推送到 SIEM](/zh/how-to/cookbook/push-to-siem/)）。

---

如果某个症状不在此处，且引擎自身的消息也无法解释它，那就是文档缺陷 — 请连同那行日志一起报告。

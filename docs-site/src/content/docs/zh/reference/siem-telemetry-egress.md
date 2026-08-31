---
title: SIEM 与遥测出口
description: >-
  控制平面输出的全部线格式 —— CEF、LEEF 2.0、RFC 5424 syslog、OTLP 日志、OCSF
  1.8.0、SARIF 2.1.0 ——，规则所依据的严重性映射、各传输方式对应的接收端上限，以及
  两处「投影并非完整信封」的说明。
---

本页是**出口契约**：什么内容离开控制平面、以何种方言、经由何种传输，以及接收端会如何
处理它。它写给那些必须一次就让 ArcSight 规则、QRadar DSM、Sentinel DCR 或 code
scanning 上传跑通的人。

页面中的每一项都对照厂商自己的规范核对过，并标注核对日期。凡是厂商**没有**规定的地方，
本页直说而不猜测——这些空白标注为*厂商未定义*，编码器在每种情况下都取保守的一侧。

## 两条数据流

记录有两个彼此独立的来源，二者共用同一个编码器，方言因此不会漂移：

| 数据流 | 内容 | 拉取 | 推送 |
|---|---|---|---|
| **审计账本** | 仅追加、哈希链式的账本及其完整性字段（序号、前序哈希、哈希、签名） | `GET /v1/audit/export?format=…`（NDJSON，一行一条记录） | 账本转发器，经由任意输出连接器 |
| **通知与发现项** | 治理发现项、策略判定、健康与生命周期事件 | — | 任意输出连接器 |

账本的完整性字段在所有格式中**原样**传递，因此 SOC 可以直接从自己 SIEM 中的副本重新
验证整条哈希链，而不必回到产品。

## 格式

| 格式 | 标准 | 固定版本 | 可选位置 |
|---|---|---|---|
| CEF | ArcSight Common Event Format | V27（2024 年 7 月） | 账本导出、连接器 |
| LEEF | IBM QRadar Log Event Extended Format | 2.0 | 账本导出、连接器 |
| syslog | RFC 5424（+ RFC 5426 UDP、RFC 6587 TCP 分帧、RFC 5425 TLS） | — | 账本导出、连接器 |
| OTLP 请求 (`otlp`) | OTLP/HTTP JSON 导出请求 (`ExportLogsServiceRequest`) | 见下文*投影* | 账本导出、连接器 |
| OTLP 请求 (`otlp_envelope`) | `otlp` 的逐字节精确别名 | 见下文*投影* | 账本导出、连接器 |
| OTLP LogRecord (`otlp_log_record`) | OpenTelemetry logs，一行一个 LogRecord | 见下文*投影* | 账本导出 |
| OCSF | Open Cybersecurity Schema Framework，`ai_operation` 配置档 | 1.8.0 | 账本导出、连接器 |
| ASIM | Microsoft Sentinel Advanced SIEM Information Model | — | 连接器 |
| ECS | Elastic Common Schema | 9.4.0 | Elastic 连接器 |
| UDM | Google SecOps Unified Data Model | — | Chronicle 连接器 |
| SARIF | OASIS Static Analysis Results Interchange Format | 2.1.0 Errata 01 | 发现项导出 |

每个可选位置各自接受这些令牌的一个有序子集，全部派生自同一个目录，因此各列表不会
再次漂移：

| 可选位置 | 接受的令牌 | 默认值 |
|---|---|---|
| 账本导出（`GET /v1/audit/export?format=…`） | `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` | `cef` |
| 事件推送 sink（push 订阅的 `sink_format`） | `ocsf\|cef\|leef\|syslog\|otlp\|otlp_envelope\|json` | `ocsf` |
| 通知连接器（`filelog`、`splunkhec`、`s3archive`、`siem`） | `json\|cef\|leef\|syslog\|otlp\|otlp_envelope\|ocsf\|asim` | `json` |
| syslog 连接器 | `syslog\|cef\|leef` | `syslog` |

账本导出没有原始 JSON 直通——它的 JSON 形态正是上面的 OTLP 形态。`json` 在不同位置意味着
两种不同的投递：事件推送 sink 发布捕获到的原始事件信封（结构化直通，不做方言转换），而
通知连接器只渲染一个最小的通知投影——可展示的字段，而非原始载荷。`asim` 被包括
`s3archive` 在内的全部四个通知连接器接受。超出所在位置列表的格式会被拒绝：编写或配置时
打错的令牌会得到一个点名该位置所接受令牌的错误，而损坏的存储值则在编码时被拒绝（只点名
损坏的拼写，不列出清单）；不会静默回退到 JSON。

## 严重性：唯一事实来源

任何按严重性过滤的规则都依赖这张表。映射只有一处、只有一份，因此同一事件的 CEF 数值、
syslog 优先级与 OTLP 严重性不可能互相矛盾。

| 产品严重性 | CEF (0-10) | syslog (0-7) | OTLP | ECS | UDM |
|---|---|---|---|---|---|
| info | 1 | 6 (info) | INFO | 1 | INFORMATIONAL |
| low | 3 | 5 (notice) | INFO2 | 3 | LOW |
| medium | 5 | 4 (warning) | WARN | 5 | MEDIUM |
| high | 7 | 3 (error) | ERROR | 7 | HIGH |
| critical | 10 | 2 (critical) | FATAL | 10 | CRITICAL |
| 未判定 | 0 (Unknown) | 6 (info) | UNSPECIFIED | 0 | *(省略)* |

有两条性质由测试锁定，因为二者都很容易在无意间丢失：

- **五个已判定的严重性绝不共用同一数值。** 诸如 `local0.notice` 的采集器选择器、ArcSight
  规则或 Sentinel DCR 都按输出的数值过滤，而 RFC 5424 帧中没有其他严重性信号——两个严重性
  共用一个优先级，就会静默且不可逆地毁掉一处区分。
- **未判定的严重性不会被编造。** CEF V27 把 `0` 从 *Low* 改称为 *Unknown*，未判定严重性的
  事件得到的正是它。（LEEF 是唯一例外：其范围为 1-10，没有表示「未知」的取值，因此适用
  下限。见下文。）

:::note[syslog 一列为何如此]
CEF 与 RFC 5424 都没有定义从 CEF 严重性到 syslog 优先级的映射——已于 2026-07-24 对照两份
规范核对。因此 syslog 一列属于**产品策略**，其选取标准是让每个严重性保持可区分，并让
「critical」落在 RFC 5424 自己称为 *critical* 的优先级上。现存唯一的厂商映射（ArcSight
连接器的一个可配置项）同样把最高一档放在 `crit`。若贵方已按另一种分档标准化，请在采集器
侧做映射——这里的数值不会在没有 changelog `Changed` 条目的情况下变动。
:::

## CEF 细节

- **报头长度**限制在 V27 上限：device vendor 63、device product 63、device version 31、
  event class id 1023、name 512。
- 规范给出了这些数字，却从未说明它们计的是**字符还是传输中的八位字节**，也未定义超长字段的
  处理方式（*厂商未定义*，2026-07-24 核对）。因此两种解读都被满足：取值同时按解码后的
  字符数**和**传输中的 UTF-8 八位字节数受限。于是非 ASCII 的设备名或事件名能容纳的字符数少于
  数字的字面暗示——这是保守的方向。
- 截断只作用于**报头**。承载可审计内容的扩展部分绝不截断。
- 取时间值的扩展键（`rt`、`start`、`end`）按 CEF 词典的要求，使用十进制**纪元毫秒**。

## LEEF 细节

- `sev` 是 LEEF 2.0 规定的 **1-10** 整数。严重性从未被判定的事件输出为 `sev=1`：LEEF 没有
  「未知」取值，而 `sev=0` 超出范围。
- `devTime` 是 **13 位纪元值**，QRadar 无需 `devTimeFormat` 即可接受。对没有记录时间的
  事件则**省略**——绝不编造——此时 QRadar 按其文档回退到接收时间。
- `sev`、`devTime` 与 `devTimeFormat` **归编码器所有**。若事件带有同名字段（不论大小写），
  将改键为 `olvSev` / `olvDevTime` / `olvDevTimeFormat` 后输出：取值照样送达，但既不能
  覆盖归一化后的严重性，也不能改写事件时间。IBM 文档指出，被识别的 `devTime` 优先于
  syslog 时间戳——正因如此，这一点不能靠运气。

:::caution[IBM 未定义]
对于 `sev=0`、无法解析的 `devTime`，以及属性键是否区分大小写，IBM 均未记载 QRadar 的行为
（2026-07-24 核对）。上述各项均为保守解读。若贵方掌握相反的接收端实测证据，值得提一个
issue。
:::

## syslog 传输与接收端上限

syslog 连接器承载原生 RFC 5424 记录，或把 CEF / LEEF 记录作为合规 RFC 5424 帧的 MSG ——
这正是 ArcSight 与 QRadar 经 syslog 接收这些格式的方式。

- **默认使用 6514 端口上的 TLS（RFC 5425）**，并按 RFC 要求采用八位字节计数分帧。明文
  TCP 或 UDP 属于运营方的显式选择退出；不存在把 TLS 目标降级为明文的代码路径。
- **接收端载荷预算**（`max_payload_bytes`，默认 `0` = 关闭）。会拆分超长记录的接收端，
  会把一条可审计事件变成两半无法解析的碎片。当你声明所运营目标的预算后，超出预算的记录
  会**让投递失败**——重试后进入 DLQ，你能看见——而不是被送出去等着被拆开。记录本身绝不
  截断。

该设置的参考值，以及各来源的实际措辞（2026-07-24 核对）：

| 接收端 | 字节 | 来源怎么说 |
|---|---|---|
| 任意 RFC 5424 接收端 | 480 | 接收端**必须**支持的最小值（§6.1） |
| 任意 RFC 5424 接收端 | 2048 | 实现**应当**支持的大小 |
| ArcSight syslog 守护进程 | 1024 | 其指南称更长的消息**「可能会被拆分」**——这是部署提醒而非接收端规则，且不适用于文件或管道路径 |
| QRadar TCP | 4096 | **默认**最大载荷；可调高（IBM 记载 8192，上限 32000） |

上述来源都没有说明计数是否包含 syslog 报头，因此预算按**完整记录**的 UTF-8 八位字节计。

## OCSF

事件以带 `ai_operation` 配置档的 OCSF **1.8.0** 输出，使用注册了该配置档的三个类：
API Activity（6003，默认）、Process Activity（1007）与 Datastore Activity（6005）。输出在
测试套件中对照官方 1.8.0 类模式校验，而这些模式禁止未知字段——因此配置档之外的属性或不
完整的配置档对象会让构建失败，而不是送到你面前。

:::caution[AWS Security Lake 仅接受 OCSF ≤ 1.3]
Security Lake 自定义源的上限是 **Parquet 格式的 OCSF 1.3**，因此 1.8.0 的 `ai_operation`
事件无法原样落地（2026-07-24 核对）。在 1.3 降级输出器出现之前，请自行做转换后再路由到
Security Lake，或改用其他目的地。这是已声明的缺口，不是疏忽。
:::

## 不是信封的投影

两条诚实的限制，在把采集器指过来之前值得先了解：

- **`otlp` 在每个可选位置上都是可直接投递的请求；`otlp_log_record` 才是裸的投影。**
  自格式目录重映射起，凡是接受 `otlp` 令牌的地方——账本导出、输出连接器、事件推送——
  每一个**事件行**都是一个完整的 OTLP/HTTP JSON 导出请求（`ExportLogsServiceRequest`），
  带有采集器所需的资源标识与检测作用域。`otlp_envelope` 在每个可选位置上都是 `otlp`
  的逐字节精确别名，保留它是因为信封最早随这个拼写发布——二者永不相异。一行一个
  LogRecord 的投影——一行一个 JSON 对象，面向文件与 NDJSON 消费——依然存在，如今顶着
  它诚实的名字 `otlp_log_record`，且仅限账本拉取导出：孤立的 LogRecord 行不是可 POST
  到 `/v1/logs` 的请求体，因此推送侧有意不提供它。三点容易踩坑的说明：拉取文件的
  **最后一行**是 Olivares 的 `{"export_complete":true,…}` 标记，它*不是*请求——逐行
  POST 的循环必须跳过它，并且要按**结构**过滤
  （例如 `jq -c 'select(has("resourceLogs"))'`），绝不要按子串过滤：某个 actor 或 target
  恰好包含 `export_complete` 的正当事件会被 `grep -v` 丢掉，那是删除证据而非跳过标记；
  推送目标必须精确指向采集器的 `/v1/logs`，因为端点会被原样使用；通用 HTTPS sink 会把
  任何 2xx 视为投递成功而不读取采集器的部分成功响应——专用的 **OTLP 日志连接器**会
  读取。`otlp_log_record` 在正常时间域内保留重映射前 `otlp` 令牌产出的确切字节——
  零时刻，以及从纪元到 `2262-04-11T23:47:16.854775807Z` 的任意时刻。在该域之外并不
  保证逐字节兼容；字节确有差异之处即是修正：纪元之前的日期此前会在 OTLP 声明为无符号
  的字段中产出负值；介于有符号与无符号上限之间的日期现在携带其真实的无符号值；而晚于
  `2554-07-21T23:34:33.709551615Z` 的日期现在编码为未知（`0`），而不是一个溢出值——
  其中包括读起来像 1970 年初的小正值。在个别溢出为零的输入上，新旧字节恰好一致。
  两条升级说明照直讲明：拉取得到的*文件*仍是 NDJSON（一行一个请求，外加完成标记），
  并非单个请求；而重映射之前把格式恰好写作 `otlp` 保存下来的事件推送订阅，如今在
  原先投递裸行的地方投递信封——引擎会为每个这样的订阅记录一条结构化警告，重映射之前
  记录的审计元数据则按令牌的旧含义解读。
- **OWASP Agentic AI Security 追踪扩展**位于 OCSF 的 `unmapped` 容器中，这正是其规范
  （v0.1 公开预览）所规定的位置。它不是一等的 OCSF 属性集，模式校验只覆盖其位置。

## 以 SARIF 输出发现项

治理发现项可导出为 **SARIF 2.1.0 Errata 01**，供 code scanning 消费方使用：

- `GET /v1/m/security/findings/export?format=sarif` —— 与发现项列表相同的过滤条件，带
  结果上限，并在触及上限时给出诚实的截断响应头。
- `olivares findings export` —— 同样的导出，来自 CLI，以 `0600` 权限原子写入。

该 run 声明其结果位置所解析依据的 URI 基准，为每个发现项携带稳定的
`partialFingerprints.primaryLocationLineHash` 以便消费方去重而非重复告警，并拒绝输出
rule id 为空或 level 超出枚举的结果——这两点会导致消费方拒收整个文件，而在上传时才发现
比在这里发现更糟。

若发现项的主体不是版本库中的文件，则会得到一个合成的位置 URI。run 依然有效且可摄取，但
GitHub 只为与检出内容中文件匹配的 URI 渲染告警——因此想要在 GitHub 上锚定的检测器应显式
设置构件 URI。

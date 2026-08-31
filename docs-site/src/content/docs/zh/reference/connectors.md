---
title: 连接器目录与覆盖层级
description: >-
  control plane 当前可接入的第一方连接器，按照每个连接器所能诚实支持的覆盖层级分组
  —— clean、lossy、impossible-passively、cooperative 以及 approximate-by-attribution
  —— 外加输出目的地。
---

本页是第一方连接器的**目录**，并为每个连接器列出它所能诚实支持的**覆盖层级**。它是
[connect a source](/zh/how-to/connect-a-source/) 的配套页面，后者解释连接器*模型*
（observe-only、minimal-data、三种观测类型）——请先阅读那篇。本页回答下一个问题：
*存在哪些 source，每一个的信号有多好？*

覆盖层级**取决于一个系统的审计面能够诚实告诉你什么**，绝不取决于我们希望它能告诉我们什么。
贯穿全部文档所使用的层级如下：

- **Cooperative（协作式）**——报告自身行为的 agent 或平台（OpenTelemetry、厂商 admin API）。
  *存在时*保真度最高；依赖 source 的协作。
- **Clean（干净）**——**原生**区分读与写的存储，逐字取自其自身的审计轨迹（SQL audit、
  对象存储 / 数仓的数据访问日志）。
- **Lossy（有损）**——其审计无法干净区分读写、或无法区分调用方与调用方的存储（文档存储、
  lineage）。edge 仍会落地，但往往是 `approximate`。
- **Impossible passively（被动不可行）**——没有可用被动审计面的系统（内存缓存、嵌入式单文件
  数据库）。不存在诚实的读优先信号；本产品也不假装如此。
- **Approximate-by-attribution（按归因近似）**——访问真实存在，但归因落到某个角色、进程或共享
  凭据，而非一个已解析的 agent，因此该 edge 为 `approximate`。
- **Untrusted hint（不可信提示）**——一项声明的能力（一个 MCP 工具标注），需经佐证，绝不单独信任。

:::caution[本目录反映的内容：接入当前构建的连接器]
本表列出**当前已注册到默认二进制连接器集合**中的连接器——即你可以在 `OLIVARES_SOURCES_CONFIG`
中命名、并让引擎接入的 kind。本产品处于 1.0 之前阶段。规范的 R/RW access map 连接器
——**pgAudit**、**S3/CloudTrail**、**eBPF/Tetragon** 后备、**runtime** 清点与 **MCP**
自省——以及**知识文档 source**，现已接入且可在标准 `serve` 中配置；其中部分带有
**部署要求**（一个 Tetragon 传感器、host 访问），见下文
[部署要求](#部署要求与诚实归因)。覆盖**层级是诚实划分的**：
一个连接器出现在此处并不等于声称对每个 agent 的归因是确凿的，而后者仍是那个硬性依赖
（一个共享账户会把即便是 clean 层级的存储也坍缩为 `approximate`）。
:::

## Cooperative —— Claude 与厂商遥测

存在时保真度最高的 source。Claude Code 运行时 source 作为内嵌插件**在进程外**运行
（普通的 dev 构建会省略它，引导日志会诚实地告警，而不是表现得健康）。

| Kind | 观测内容 | 备注 |
|---|---|---|
| `claude` | Claude Code OTLP 工具遥测 + MCP 自省 → edges / cost / findings | 进程外插件；存在每个 agent 身份时为 `attributed`，否则为 `approximate` |
| `claude-api` | Claude Admin-API 成本采样 + 治理态势 findings | 进程内；离线（无 admin key）时为空操作 |
| `claude-compliance` | Claude Compliance 活动流证据 → findings | 构造上仅 GET；离线时为空操作 |
| `claude-config` | 静态 Claude config 树（subagents / Skills / plugins）→ **声明能力** edges | 仅元数据——一个能力面，而非已观测的访问 |
| `claude-console` | Claude 组织 IAM → SSO/SCIM 态势 findings（身份名册 + source） | |
| `claude-wif` | Anthropic 非人类身份 / 工作负载身份名册 + 允许范围 edges | 建模运营方声明的 federation；标记静态 key 隐患 |
| `claude-managed-agents` | Claude managed-agents 清单 + thread event（webhook receiver + GET poller） | 流式 source（`poll_seconds: 0`）；离线时为空操作 |
| `claude-projects` | Claude Organization Projects 清单（membership / API key）+ 运维方声明的 project policy | Admin API 只读；离线时为空操作 |
| `claude-apps-gateway` | Claude apps-gateway 态势、声明的 model grant 与审计事件 ingest → topology + finding | 读取既有 `gateway.yaml` 与可选 JSONL 审计导出 |
| `claude-batch` | Anthropic Message Batches + Files API 清单、batch policy 执行、upload retention expiry | 绝不读取 payload 或文件内容；无 admin key 时给出诚实的 offline finding |
| `claude-routines` | Claude Code Routines（scheduled trigger）清单 → edge + cadence/review finding | 仅 GET；prompt 内容只做哈希；流式（`poll_seconds: 0`） |
| `cowork` | Claude Cowork OTLP/HTTP 日志 receiver → activity evidence | 进程外插件（隔离 OTel-proto 依赖） |
| `cowork-analytics` | Claude Cowork engagement analytics | 进程内（仅 modelprovider client） |
| `codex` | OpenAI Codex 成本样本、usage/auth/admin-audit 证据、adoption finding | Admin API 只读；销售门控表面降级为态势 finding |
| `cursor` | Cursor Admin API 计费成本、team audit log、member inventory、budget posture | 套餐门控的 `403`/`404` 降级为 finding，绝不导致失败 |

### 厂商无关的 GenAI 框架 profile（`gen_ai.*`）—— 选择性启用

本目录承诺的 agent 框架——**LangGraph / LangChain、CrewAI、AutoGen / Microsoft Agent
Framework、Google ADK**（以及 OpenAI SDK、LlamaIndex、Pydantic-AI、Strands……）——**不**
发出 Claude 的 `claude_code.*` schema。它们汇聚到
[OpenTelemetry **GenAI** 语义约定](https://github.com/open-telemetry/semantic-conventions-genai)
（`gen_ai.*`）。同一个 `claude` source 也会摄取该 profile，因此一支经 OTel 插桩的舰队可以通过
单次摄取喂给 **access map** 与 **FinOps**，而无需为每个框架定制连接器——这是杠杆率最高的集成。

**该 profile 为选择性启用，并被诚实标记为实验性。**整个 `gen_ai` 领域处于 OpenTelemetry
**Development** 状态（非 Stable，2026 年 6 月），因此它只有在你按规范自身的门控镜像启用时才激活。
将连接器的 `semconv_opt_in` 设为一个包含 token `gen_ai_latest_experimental` 的逗号分隔列表
（镜像 `OTEL_SEMCONV_STABILITY_OPT_IN`）。默认关闭时，`gen_ai.*` 信号仍会喂给静默看门狗，但
不映射任何 edge/cost——我们绝不声称约定本身并不具备的稳定性。

由于这些约定正处于变动期，本摄取是**双名的**（它同时读取当前 key *与*仍在野外发出的已弃用前身）
且**多信号的**（它映射 trace **span**、`gen_ai.client.inference.operation.details` 日志
**事件**，并识别客户端 **metrics**）：

| 它读取什么 | 当前 key | 同时接受（已弃用，仍由其发出） |
|---|---|---|
| Provider | `gen_ai.provider.name` | `gen_ai.system`（v1.36.0 及更早的默认值；**Google ADK**，例如 `gcp.gemini`） |
| 输入 token | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens`（**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI） |
| 输出 token | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens`（同上） |

| gen_ai 属性 | 映射到 | 置信度 |
|---|---|---|
| `gen_ai.usage.*`（tokens） | `CostSample`（来源为 **estimated**——token 而非已计费成本） | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | 成本 provider + model（优先 response） | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | agent→tool **访问 edge**（mode 为 `unknown`） | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | conversation→agent **归因 edge** + session ref | `attributed` |

#### 支持的方言矩阵（多代规整器）

GenAI 约定经历了**三代共存**于真实 2026 年舰队中的演变。本摄取从各代特有的标记中**逐信号**
检测代际，并用对应的 semconv 锁定值给规整后的事件打戳（`genai.semconv` 态势 finding 记录每次
运行的活动集合；每次运行会有一条 info 级 `drift` finding 标记所见的每个**已弃用**方言，让你
知道哪些舰队需要升级插桩）。**任何一代都绝不读取消息内容**——内容 key 仅作为方言标记
（minimal-data 态势）。

| 检测到的方言 | 打上的锁定值 | 独占标记（已验证） | 发出方（已验证 2026 年 6 月） |
|---|---|---|---|
| 旧版 **OpenLLMetry/Traceloop**（semconv 之前） | `openllmetry` | 索引化 `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*`、`gen_ai.usage.prompt_tokens`/`completion_tokens`、`llm.usage.total_tokens`、`llm.request.type`、`llm.vendor`、`traceloop.span.kind` | 经 Traceloop 插桩、锁定 **< openllmetry v0.55.0**（发布于 2026-03-29）的 LangChain / LangGraph / CrewAI。首字母大写的 provider（`OpenAI`、`Langchain`）会被转为小写，使 FinOps 不会因大小写而拆分 |
| **v1.36 及更早事件**（规范自身的命名） | `1.36.0` | `gen_ai.system`；五个 per-message 日志事件 `gen_ai.{system,user,assistant,tool}.message`、`gen_ai.choice`（**按名**识别——其唯一属性是可选的） | Google ADK LLM span（`gcp.vertex.agent`）、AutoGen（`autogen`）、Microsoft Agent Framework——它们仍全部发出 `gen_ai.system` |
| **v1.37+ messages**（当前） | `1.41.1` | `gen_ai.provider.name`、`gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`、`gen_ai.client.inference.operation.details` 事件、`gen_ai.workflow.name` | OTel 官方插桩；openllmetry **≥ v0.55.0** |

一个仅携带跨代名称完全相同的 key 的信号（例如一个 ADK `invoke_agent` span：operation + agent +
conversation，完全没有 provider key）会在当前锁定值下规整——所应用的映射逐字节相同，且生产者的
真实版本无法从传输协议得知。

#### MCP 约定（`mcp.*`，semconv v1.39 —— Development）

上游恰好存在四个 `mcp.*` 属性（`mcp.method.name`、`mcp.protocol.version`、`mcp.resource.uri`、
`mcp.session.id`）；tool 搭载于 `gen_ai.tool.name`，prompt 搭载于 `gen_ai.prompt.name`。本摄取
通过复用 Claude 路径所发出的相同 resource kind，将这些 trace 与产品自身的 MCP 治理事实连接起来：

| MCP 信号 | 映射到 |
|---|---|
| 任意带 `server.address` 的客户端侧 `mcp.*` span | session→`mcp.server` edge（与 `claude_code.mcp_server_connection` 的 edge 连接） |
| `tools/call` + `gen_ai.tool.name` | `mcp.tool` 访问 edge（端点已知时为 `server.address/tool`）——与 Claude 的 `mcp__server__tool` 调用同一 kind |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | **read 模式** `mcp.resource` edge（URI 已脱敏：凭据/查询被剥离） |
| `prompts/get` + `gen_ai.prompt.name` | **read 模式** `mcp.prompt` edge（prompt 面） |
| SERVER 类 span / `mcp.client|server.*.duration` metrics | 仅存活性（干净降级——server 视角不归因任何 agent 身份） |

#### Agent span（`invoke_agent` client/internal 拆分 + `invoke_workflow`，semconv v1.41 —— Development）

v1.41.0 将 `invoke_agent` 拆分为 **CLIENT** 变体（远端 agent 服务）与 **INTERNAL** 变体（进程内）。
真实框架今天违反该 kind（AutoGen 与 Microsoft Agent Framework 对进程内 agent 硬编码 CLIENT；
Google ADK 使用 INTERNAL），因此本摄取仅当一个 span 为 CLIENT **且**携带 `server.address` 时才
将一次调用分类为**远端**——这会产生一条 conversation→`genai.agent.remote` 委派 edge。其余一切
均保持为由 conversation→`genai.agent` 归因 edge 所覆盖的进程内调用：干净降级，绝不编造
“remote”。`invoke_workflow`（v1.41 新增；CrewAI 式 crew）映射一条 conversation→`genai.workflow`
edge。Agent span 在上游仍为 **Development**（实验性）——不声称任何稳定性。

**稳定 vs 实验，诚实地说：****机制**（选择性启用门控、方言检测 + 双名读取、span/event/metric
映射、已封存的 `CostSample`/`EdgeObservation` 形状）在本产品中是稳定的。它所映射的**词汇表**
（`gen_ai.*`/`mcp.*` key、operation 枚举）在上游为 **Development**，可能再次重命名；这正是本摄取
规整每一代而非锁定某一代的原因。v1.41.1 是 gen-ai 约定最后一个*带版本号*的发布（它们迁移到了
`open-telemetry/semantic-conventions-genai`，截至 2026 年 6 月该仓库无任何发布）。注意：

- **成本按 W3C span id 去重。**当一次 operation 在其 span *与*其 `operation.details` 事件上
  （它们共享一个 span id）同时报告用量时，只计一次成本，而非两次。
- **Metrics 喂存活性，绝不喂成本。**`gen_ai.client.token.usage` 是聚合值；span/event 才是每次
  operation 的权威用量，因此对 metric 也计费会重复计数。v1.39 的 `mcp.*` 时长直方图以相同方式识别。
- **Provider 可能为 `unknown`。**若某个 span 携带 model 但无 provider/system，成本归因到
  `unknown`，而非从 model id 猜测。
- **仅有总量的 token 计数不拆分。**没有 prompt/completion 拆分的旧版 `llm.usage.total_tokens`
  绝不被猜测为输入/输出（不编造成本）。
- **OpenInference（Arize/Phoenix）是另一套约定**，本 profile *不*摄取它——此处读取的 `llm.*` key
  （`llm.request.type`、`llm.usage.total_tokens`、`llm.vendor`）是 **OpenLLMetry 旧版标记**，
  而非 OpenInference 的 `llm.*` 命名空间。

## Cooperative——本地 agent-surface 配置

这些 source 读取本地 agent 声明的配置，并发出 **permitted** edge 与 posture finding。
它们不是实时执行 trace；框架若有原生 OTEL，实时使用仍经由上文的 `gen_ai.*` ingest。

| Kind | 观测内容 | 诚实覆盖 |
|---|---|---|
| `opencode` | 本地 `opencode.json` / `opencode.jsonc` JSONC 层 → permission、managed/admin-override posture，MCP/tool/custom-agent permitted edge，credential-in-config/share/autoupdate/OTEL finding，以及 authoring fragment | 仅配置声明。managed 层可在本地检测，但并非 immutable lock：runtime `OPENCODE_PERMISSION`、test-dir 重定向和远程组织配置均不在此 reader 范围内。原生 OTEL 启用后可通过 out-of-band `OTEL_*` exporter 提供实时 `gen_ai.*` 使用 |
| `gemini-cli` | Gemini CLI `settings.json` 层（system/user/workspace）→ permitted MCP/tool edge、enforcement-gap posture、effective-config inventory | 仅配置声明；实时使用经由 `gen_ai.*` ingest（CLI 原生发出）。不是 Gemini API（后者为 hosted-provider 表面） |
| `openhands` | OpenHands `config.toml` + env → sandbox/model-pinning/credential/telemetry posture、permitted MCP/action edge | 仅配置声明；实时使用经原生 OTEL `gen_ai.*` |
| `goose` | Goose（Block）`profiles.yaml` + env → admin-settings/model-pinning/extension/tool-approval posture、permitted extension edge | 仅配置声明 |
| `cline` | Cline / Kilo Code VSCode `settings.json` namespace → auto-approve/MCP-allowlist/credential/model-pinning posture | 仅配置声明；upstream 无原生 OTEL |
| `grok` | Grok Build（xAI）终端 coding agent，通过其本地配置读取：hook wiring、具备已记录 veto 的 event 与可声明 governance posture | **不是 xAI API connector**（`xai` 读取 catalog 与 cost，模型中包括 `grok-build-0.1`）。本项读取 AGENT，二者不重叠。观测部分走 Grok Build 已发出的 OTLP ingest。只有 `PreToolUse`（唯一有已记录 veto 的 event）可声明 `PostureEnforced`；其余均为 `observed` |
| `openclaw` | OpenClaw `openclaw.json`（JSON5 discovery、受限 `$include`）→ 每 agent 的 gateway/channel/tool/sandbox/skill/model posture，声明的 channel/skill/model edge | 仅配置声明；upstream 未验证 inline PEP hook |
| `hermes` | Hermes Agent `config.yaml` + profile tree + managed scope → terminal/channel/skill/security/model/MCP posture、声明的 edge | 仅配置声明；upstream 未验证 inline PEP hook 或原生 OTEL |
| `google-adk` | 导出的 Google ADK 2.0 Session JSON → agent/app 清单、sub-agent、tool function-call、transfer、approved-tool drift、Vertex reasoningEngine correlation | 只读导出；绝不读取消息内容。不同于 `google-agent` 平台表面 |
| `agents-md` | 遍历 repo 中的 agent instruction 文件（AGENTS.md 与每 agent memory/instruction 文件）→ SHA-256 baseline drift + instruction-injection / hidden-Unicode / secret scan | minimal-data：净化后的 path + hashed detail，绝不读取内容 |
| `mcpb` | 已安装 / 已分发的 `.mcpb` desktop extension → manifest posture scan、enterprise-allowlist drift、PKCS#7 signature verification | extension 表面上的 PERMITTED-vs-OBSERVED |
| `codex-managed-config` | OpenAI Codex managed-config 文件 → enforcement posture + 与 authored baseline 的 drift | 仅观测：无法阻止 developer 绕过 managed 层（Codex 的 `managed-settings` 镜像） |

## Clean —— 原生存储审计（逐字读/写）

这些 source 读取存储的**自有**审计轨迹，并逐字采用其读/写分类——绝不从查询文本推断。`pgaudit`
与 `s3cloudtrail` 是 [access map](/zh/reference/modules/iii-access-map/) 所围绕构建的规范 R/RW
source（它们带连字符的 `pg-audit` / `s3-cloudtrail` 别名也可解析）。

| Kind | 观测内容 |
|---|---|
| `pgaudit` | PostgreSQL **pgAudit** 轨迹（csvlog/jsonlog）→ R/RW 表访问，`READ`/`WRITE` 逐字取自 pgAudit 的 CLASS |
| `s3cloudtrail` | AWS **CloudTrail** S3 事件 → 对象 R/RW，读/写取自 CloudTrail 的 `readOnly` 标志（同时浮现 Claude-on-Bedrock 模型调用） |
| `snowflake-audit` | Snowflake 原生访问历史 |
| `databricks-uc` | Databricks Unity Catalog 审计 |
| `bigquery-audit` | BigQuery 数据访问审计 |
| `redshift-audit` | Amazon Redshift 审计 |
| `mssql-audit` | SQL Server 审计 |
| `oracle-audit` | Oracle 统一审计 |
| `gcs-audit` | Google Cloud Storage 数据访问审计 |
| `azure-blob-audit` | Azure Blob Storage 审计 |

## 云管理平面 —— org/tenant 清点 + 控制平面活动

**管理**平面的三云对等——区别于上文存储审计连接器所覆盖的每个资源的**数据**平面。每一个都是
某云 org/tenant 控制平面的实时、**只读** API 客户端：它发现资源的**拓扑**（清点 edge，
`mode=unknown`，attributed）并读取该云原生的**审计流**以获取控制平面**活动**
（`identity→…api` edges，已分类读/写）。它们补全了 AWS 已用 `s3cloudtrail`（数据平面）加上
账户级 IAM/CloudTrail `aws` 连接器所锚定的矩阵。两者均**在进程内**运行且**离线安全**
（无凭据 ⇒ Gather 为空操作）；两者都只观测控制平面——绝不观测任何 payload、secret、key 或
资源属性。

| Kind | 观测内容 | 诚实覆盖 |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM**（org→folder→project→service-account 拓扑）+ **Cloud Audit Logs**（Admin Activity + Data Access）→ `identity→gcp.api` | 有日志处为 **Clean**：按日志类型定义 Admin Activity 为写，Data Access 由标准方法动词判定读/写。Data Access 日志被禁用处（GCP 默认关闭）或方法动词非标准处为 **Lossy**（`unknown`，绝不猜测）。声明的共享 principal 为 `approximate`；`principalEmail` 与 SPIFFE/SA 名册收敛 |
| `azure-activity` | Azure **Resource Graph**（tenant→subscription→resource 拓扑）+ **Azure Monitor Activity Log**（控制平面操作）→ `identity→azure.api` | 控制平面写/删为 **Clean**（逐字取自 RBAC action）。通用 `action` 后缀为 **Lossy**（`unknown`——可能读或写）。数据平面**读不在** Activity Log 中（由 `azure-blob-audit` / `azurekeyvault` 数据平面覆盖）。共享调用方为 `approximate`；调用方 `objectId`/`appId` 与 Entra 名册收敛 |
| `cloudflare` | 经 REST API v4 获取 Cloudflare edge estate——**Worker、R2 bucket、Logpush job**→ topology edge | 仅清单（本 connector 无 audit feed）；受限只读 token。不同于 `cloudflare-ai-gateway` / MCP portal AI 表面 |

GCP **Data Access** 的选择性启用与 Azure **读不入日志**的缺口是本平面诚实的**不透明** edge：
在那些日志关闭处，缺少一条活动 edge 并不能证明没有访问。完整的每云层级表见随附源码树中的
`docs/contracts/S165-connectors-cloud-management.md`。

## 托管模型提供方——目录、态势与计量

这些 source 治理托管模型提供方账户和目录。它们**不**代理 inference；当提供方没有可用
usage API 时，费用由 connector 在 inference path 周围的 Meter 估算，而不是从汇总 billing feed 拉取。

| Kind | 观测内容 | 诚实覆盖 |
|---|---|---|
| `openai` | OpenAI platform usage 与 cost（org API），以及 model 与 API-key catalog | 只读 org/admin key；无 data-plane payload。不同于 `azure-openai`，后者访问真实 Azure 表面而非 OpenAI-org path |
| `gemini` | Gemini（Google）hosted model catalog 与运维方接入的 usage export | hosted-provider 表面。不同于读取本地 CLI settings 的 `gemini-cli` 和覆盖 enterprise Vertex 表面的 `vertex`。Google 在此路径没有 aggregate usage API，因此 usage 仅为运维方所接入内容 |
| `deepseek` | DeepSeek hosted catalog、account balance availability、PRC sovereignty posture | 无 aggregate usage API；cost 从声明定价在 inference 周围计量 |
| `mistral` | Mistral catalog 与 governance posture | 无公开 usage/billing/spending-cap API；cost 从 list pricing 在 inference 周围计量 |
| `xai` | xAI/Grok live catalog、billing endpoint、key/ACL inventory、credit 与 spending-limit posture | cost 使用只读 management billing endpoint；management 与 inference credential 分离 |
| `glm` | Zhipu GLM / Z.ai 声明 catalog、USD list-pricing Meter、entitlement probe、sovereignty posture | 仅 catalog + Meter：GLM 没有已验证的 usage、billing、balance、admin、key 或 organization API。PRC nexus / Entity List 警示同时适用于 `z.ai` 与 `bigmodel.cn` 表面 |
| `vertex` | Google Vertex AI catalog、per-model token usage（Cloud Monitoring）、opt-in billed cost（billing export）、opt-in Model Armor safety posture | AI Studio path 不覆盖的 enterprise Google 表面；GCP 无实时 cost API |
| `azure-openai` | Azure OpenAI / AI Foundry deployment + model（ARM）、Azure Monitor token usage 与 cost 表面 | 只读 management-plane client；无 data-plane payload |
| `openrouter` | OpenRouter live catalog（USD/MTok pricing）、account usage/limit posture、approved-model policy drift | billed cost 经导出的 `MeterCall`；离线时为空操作 |
| `cohere` | Cohere live model catalog（cursor-paginated Models API） | 无公开 usage/billing/org API（仅 dashboard）——诚实 coverage caveat；cost 从 list pricing 在 inference 周围计量 |
| `fal` | fal.ai API-key lifecycle inventory + rotation posture；cost 在 queue API 周围计量 | 无公开 usage/audit API——按 key lifecycle 治理；深层表面受销售门控并标为 UNVERIFIED |

## 自托管 inference——本地 catalog 与 usage

自托管 inference 始终在范围内，因此它是一等 source，而非 gateway 的附带项。
此层观测本地 runtime 实际正在服务的内容。

| Kind | 观测内容 | 诚实覆盖 |
|---|---|---|
| `local` | Ollama model catalog（`/api/tags`）、**Ollama residency（`/api/ps`）**——当前已加载 model、GPU/CPU 分布与 unload deadline——以及经 OpenAI-compatible 表面的 vLLM token usage | residency 作为 posture 上报，其 severity 即 PLACEMENT：完全位于 VRAM 的 model 为 informational；位于 CPU 或在 CPU/GPU 间 SPLIT 的 model 会被标记，因为 operator 会承受 latency 却未获告知。Ollama 无 aggregate token metric，因此不贡献 metering。本 source 仍不提供 local inference 的 per-call identity 或 policy；治理它们需要 gateway 或 OTel path。localhost 上的 Ollama 无需 credential，因此空 config 是可工作的只读默认值；禁用 server 需显式空 URL，两者均空则为空操作 |

## 内核后备 —— eBPF / Tetragon（信号干净，归因近似）

护城河中**非协作**的那一半：协作路径看到 agent *报告*的内容，而此处看到内核*实际做了*什么
——文件读/写与出站连接——即便一个 agent 关闭了自身遥测。**访问**是内核的地面真相（关于
*发生了什么*的 clean 层级信号）；**归因**则刻意地诚实面对其极限——内核归因到一个运行时身份
（process/cgroup/container），绝不归因到已解析的 agent，因此每条 eBPF edge 都是 `approximate`。
它绝不解密或检查 payload（它对 TLS 主体视而不见）。

| Kind | 观测内容 | 诚实极限 |
|---|---|---|
| `ebpf` | Tetragon 内核事件 → 文件 R/RW（`MAY_*` 掩码）与网络 edge；当某 agent 在内核层行动却无协作遥测时，可选的反规避 finding | agent 匿名 → 始终 `approximate`；一个流式后备，而非每个 agent 的台账 |

它**不**自行加载 eBPF 程序：内核捕获由 [Tetragon](https://tetragon.io/)
（一个独立、加固的 DaemonSet）完成。见
[部署要求](#部署要求与诚实归因)。

## Lossy —— edge 落地，但常为近似

| Kind | 观测内容 | 为何有损 |
|---|---|---|
| `mongo-audit` | MongoDB 审计 | 文档存储；调用方区分弱 |
| `openlineage` | OpenLineage run 事件 → 数据集 lineage | lineage 不是每次调用的审计 |
| `delta-sharing` | Delta Sharing 接收方活动 | 共享接收方归因 |

## 按归因近似 & permitted 侧 source

这些发出 **permitted** 侧（声明的 grant），或发出归因到角色 / 进程 / 共享凭据而非已解析 agent
的访问。

| Kind | 观测内容 | 层级 |
|---|---|---|
| `iceberg-catalog` | Iceberg REST catalog → permitted grant + 下发凭据身份 | permitted |
| `inference-gateway` | K8s Gateway API Inference-Extension 路由 → permitted 推理路由 | permitted |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | 云 KMS 审计 → key 访问 edge（绝不含 key 材料） | approximate |
| `external-secrets` / `sops` / `kmip` | 密钥管理清单 / KMIP locate → 预配/托管 edge | approximate（存在性，非使用） |
| `istio-telemetry` | Istio Telemetry CRD → L7 mesh edge | approximate（解析的 CRD，非实时流） |
| `egress-proxy` | Egress-proxy 裁决日志 → L7 出站 edge | approximate |
| `kong-audit` | Kong 审计日志 → 配置变更 finding | approximate |
| `ai-gateway` | Envoy AI Gateway 用量记录 → **成本**采样（FinOps） | 成本流 |
| `github` | 将 GitHub repository 作为 agent data source → observed R/RW access edge（webhook-first、API poll reconciliation）+ permitted ACL edge | observed + permitted；流式（`poll_seconds: 0`） |
| `gitlab` | GitLab repository → observed R/RW access edge + permitted ACL edge | observed + permitted；流式（`poll_seconds: 0`） |

## 态势观测者 —— findings，而非访问 edge

读优先的观测者，将态势（sync/health/drift、认证异常）浮现为 finding；它们绝不改动 estate。

| Kind | 观测内容 |
|---|---|
| `runtime` | AI 工作负载运行于何处（Linux procfs、Docker daemon、Kubernetes API）→ 容纳 edge + 健康 finding（需要 host 访问——见 [部署要求](#部署要求与诚实归因)） |
| `argocd` / `flux` / `crossplane` | GitOps / 控制平面 CRD → sync、health、drift、composition 态势 |
| `kerberos` | KDC 认证遥测 → Kerberoasting finding |
| `aaa` | RADIUS / TACACS+ AAA 观测 |
| `ssf` | Shared-Signals / CAEP 接收器（agent kill-switch） |
| `edugain` / `openidfed` | Federation 聚合 / OpenID-Federation 信任链 → federation 态势 |
| `managed-settings` | Claude `managed-settings` 策略 → permitted edge + drift finding |
| `envoy-ai-gateway` | Envoy AI Gateway **声明配置**导出 → gateway posture + gateway-vs-Olivares policy drift（`ai-gateway` usage stream 的 config sibling） |
| `kong-agent-gateway` | Kong agent-gateway 声明配置导出 → posture + policy drift |
| `litellm` | LiteLLM proxy 声明配置导出 → posture + policy drift |
| `bedrock-kb` | Amazon Bedrock Knowledge Bases retrieval health/config（Agent Runtime Retrieve health-check）→ per-KB posture finding + KB→data-source edge。绝不 `RetrieveAndGenerate`（无 billable inference），绝不读取完整 document content |
| `tak` | TAK Server `CoreConfig.xml` posture（+ 可选 mTLS probe）和受治理、minimal-data 的 Cursor-on-Target ingest（position digest、uid hashed） |
| `a2a` | Agent2Agent（A2A）v1.0 peer → Agent Card discovery + JWS/JCS signature verification（peer trust level），以及 observed task/message interaction 作为 agent↔agent edge。仅观测——绝不 dispatch task；发出 signed card 是另一项能力 |

## Untrusted hint —— MCP 自省

`mcp` source 对 MCP server（stdio + Streamable HTTP）进行自省，并发出携带 server *声明的*
R/RW 提示的**能力 edge**，外加协议修订、特性面与注册来源的 finding。按 MCP 规范，一个工具标注
是**不可信**声明——一项能力*主张*，需对照已观测 source 加以佐证，**绝不单独信任**。（协作式
`claude` source 也会将 MCP 自省作为其 OTLP 路径的一部分；`mcp` 是你指向某个 server 列表或某个
`.mcp.json` 的独立自省器。）

| Kind | 观测内容 | 层级 |
|---|---|---|
| `mcp` | MCP server 的 tools/resources/prompts → 声明能力 edge + 态势 finding | untrusted hint |

## 进程外 broker 与 mesh 观测者

这些携带庞大的网络协议依赖树，因此每一个都**在进程外**运行（依赖绝不链接进核心）。
一个连接器可触达多个目标。

| Kind | 观测内容 |
|---|---|
| `kafka` | Kafka / Event Hubs / Redpanda / MSK topic 活动 |
| `amqp` | AMQP broker（RabbitMQ、Azure Service Bus） |
| `nats` / `mqtt` / `cloudqueue` | NATS、MQTT、云队列活动 |
| `debezium` | Debezium 变更数据捕获流 |
| `envoy` | Envoy ALS / ext_authz / ext_proc 观测服务 |
| `hubble` | Cilium Hubble 流数据 |

## 身份名册 provider

这些填充非人类身份**名册**，以锐化归因（把 `approximate` edge 变成 `attributed`）。每个带 grant
面的 source 也会从 `Gather` 发出其**permitted-access**（`SignalPolicy`）edge——即
permitted-vs-observed 差异中的 PERMITTED 侧：

| Kind | 名册 | Permitted edge |
|---|---|---|
| `vault` | entity、group、policy | ACL policy path grant（`vault.path`），按每个绑定 entity 展开 |
| `ldap` | user、service/computer 账户、group | 特权组成员 → 目录 grant（`ldap.directory`） |
| `idp`（Okta / Entra） | user、app/service principal、group | app 分配 / scope grant（`okta.app` / `entra.app`） |
| `infisical` | machine identity、org member、project | project grant（`infisical.project`） |
| `keycloak` | realm、client、role、group、user | 仅名册（空操作 `Gather`） |
| `pingone` / `forgerock` | 通过同一个 multi-provider reader 获取 PingOne / ForgeRock directory roster（kind 会预设对应 `provider`；`ping` 是 `pingone` alias） | 仅名册（空操作 `Gather`） |
| `spiffe` | SPIRE 注册条目 | 仅名册（空操作 `Gather`） |

在 `identity` 条目上接入 `as_source: true` 可在每次引导时执行一次性的 permitted-grant 扫描，
或用一个带 `poll_seconds` 的单独 `sources` 条目进行周期性重扫——同一 kind 绝不可两者并用
（`okta`/`entra` 共享同一个 `idp` 连接器，因此每个进程只能注册一个 idp 家族实例作为 source）。
组/角色成员关系仅随类型化的名册快照传递，绝不作为 edge。

### Agent 身份 federation

超大规模厂商的 **agent registry** 针对本平面的 SPIFFE/WIF 名册进行只读 federation。它们每个
agent 的行（`agent_identity` / `workload_identity` kind）是专用、非共享的身份，因此 access map
将它们视为**确凿**的每个 agent 归因；来自同一 source 的辅助行（blueprint principal、凭据
provider、由 service-account 支撑的 agent）保持近似。Federation 绝不写入 registry；向 control
tower 的*导出*是一项独立的、后续的能力。

| Kind | Federate | Gather |
|---|---|---|
| `entra-agent` | 经 Graph v1.0 的 Microsoft Entra Agent ID（agent identity、agent user、blueprint、blueprint principal、owner/sponsor、快照内 orphan 计算、opt-in soft-deleted） | `nhi_longlived_credential` drift finding、CA/risky-agent/governance/sponsorless posture finding，以及 opt-in beta `auditLogs/signIns` observed agent access edge——添加带 `poll_seconds` 的 `sources` 条目 |
| `agentcore` | AWS Bedrock AgentCore Identity（workload identity、token-vault 凭据 provider）+ 作为集合的 AgentCore Policy 引擎/Cedar policy | `nhi_longlived_credential` drift finding（静态 API-key provider）——添加一个带 `poll_seconds` 的 `sources` 条目 |
| `google-agent` | Google Agent Identity（Agent Runtime reasoning engine；基于 SPIFFE 的 agent identity）加 Agent Registry / Agent Gateway posture。row 使用**完整 SPIFFE ID** 作 ref，与 `spiffe` roster 收敛；Gather 检测 unattributed registry agent、可读 registry 之外的 shadow reasoning engine、risky MCP tool annotation 与 gateway registry posture | registry/gateway posture finding 与 shadow-agent detection——添加带 `poll_seconds` 的 `sources` 条目 |
| `agent365` | 经 Graph v1.0 的 Microsoft Agent 365 registry（package-level inventory，含*没有* Entra identity 的 agent），支持 app-permission client credential 或 delegated token、opt-in package detail | registry-hygiene finding（已部署但 blocked 的 package；向所有 user 部署的 external/shared package）——添加带 `poll_seconds` 的 `sources` 条目 |
| `foundry-agents` | 经 ARM + Foundry Agent Service v1 获取 Microsoft Foundry project、agent application/deployment 与当前 Agent Service agent；将 app identity link 与 `entra-agent` 关联 | ARM-derived application posture finding（缺少 Entra agent identity；已启用 app 的 deployment failed）——添加带 `poll_seconds` 的 `sources` 条目 |
| `ai-control-tower` | ServiceNow AI Control Tower 数字资产清点（Table API，只读） | 空操作（仅名册） |
| `oasf` | AGNTCY/OASF agent 描述符 + Agent Badge 验证——在身份规范符合 VCDM 2.0 之前为 **EXPERIMENTAL** | badge finding——添加一个带 `poll_seconds` 的 `sources` 条目 |
| `onepassword` | 1Password 账户作为 `secret_store` 托管者 | item 使用密钥访问 edge——添加一个带 `poll_seconds` 的 `sources` 条目 |

对于带可重轮询 Gather 的七个 kind（`entra-agent`、`agent365`、`agentcore`、
`foundry-agents`、`google-agent`、`oasf`、`onepassword`），将
**名册**那半作为一个*不带* `as_source` 的 `identity` 条目接入，将 **edge/finding** 那半作为一个
带 `poll_seconds` 的单独 `sources` 条目接入——不要两者皆用 `as_source: true`，那只会每次引导
扫描一次（且同一 kind 的重复注册会被拒绝）。

registry 声明的 **owner/sponsor** 会在名册同步期间落到 NHI 生命周期记录上（与
`PUT /nhi/{ref}/ownership` 同语义），而 registry 断言的**孤儿**（一个 blueprint 已不存在的 Entra
agent）落到同一记录的 `registry_orphaned` 标志上——生命周期扫描将其 OR 进 `orphaned` 并发出
`nhi_orphaned` finding，因此孤儿检测无需任何额外配置即可监视 federation 的 agent。`vault-audit`
*source*（位于 `sources` 之下，而非 `identity`）跟踪 Vault 文件审计设备，并为相同的
`entity:<name>` ref 发出 `vault` permitted grant 的 OBSERVED 对应物。

## 知识文档 source（非 access-map 覆盖）

这些喂给**知识**模块（模块 VIII），**而非** access map：它们摄取*文档内容*以供受治理检索，
发出**无** R/RW edge，且在总线上产生**无**观测。模块在收到摄取请求时*拉取*它们（List → Fetch）
（`POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"}`），因此它们接入该模块——在
`OLIVARES_SOURCES_CONFIG` 中将它们命名于 `documents` 之下，而非 `sources`。每一个都是只读且
minimal-data：它携带 source 的 ACL 与来源（绝不含个人邮箱；模块在持久化前会脱敏正文）。

| Kind | 摄取内容 |
|---|---|
| `gdrive` | Google Drive 文档（Docs/Sheets/Slides/文件） |
| `confluence` | Atlassian Confluence 空间与页面 |
| `notion` | Notion 工作区、数据库与页面 |
| `sharepoint` | Microsoft SharePoint / OneDrive 站点与文档 |
| `s3content` | 对象存储内容（S3 / R2 / GCS 对象） |
| `sap_odata` | SAP OData service entity，作为 governed document |
| `salesforce` | Salesforce object/record，作为 governed document |
| `snowflake` | Snowflake table/row，作为 governed document（不同于 `snowflake-audit` R/RW observer） |
| `azure_ai_search` | Azure AI Search index document |
| `postgres` | PostgreSQL row，作为 governed document——构造上只读、声明 per-row ACL、per-column classification（不同于 `pgaudit` R/RW observer；不是 NL-to-SQL）。见[将 Postgres 用作受治理上下文源](/zh/how-to/govern-postgres-content/)。 |
| `filesystem` | 文件服务器内容（local / NFS / SMB）——构造上将读取限制在 root，POSIX owner/group/ACL 映射为 Document ACL，使用 xattr classification（不同于 `filelog` log sink）。见[治理文件服务器](/zh/how-to/govern-your-file-server/)。 |

```jsonc
// OLIVARES_SOURCES_CONFIG —— 文档 source 位于 "documents" 之下，绝不在 "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## 输出目的地（非覆盖）

输出连接器**投递** finding 与通知；它们什么都不观测，也没有覆盖层级。它们与 source 分开配置。

进程内 destination kind：`slack`、`teams`、`pagerduty`、`opsgenie`、`webhook`、
`siem`、`splunkhec`、`syslog`、`servicenow`、`jira`、`email`、`twilio`、
`chronicle`、`datadog`、`elastic`、`snmp`、`filelog`、`otlplog`（OTLP/HTTP log）
与 `s3archive`（S3 Object Lock WORM sink——每条通知生成一个 immutable、lock-verified object）。

三种 broker egress kind 作为内嵌插件**在进程外**运行（其网络协议 dependency tree
绝不链接进 engine，与 plugin source 完全相同）：`kafka`、`amqp` 和 `cloudqueue`——
kind name 与其 source twin 相同；作为 destination 时，各自将通知以 CloudEvent 投递至配置的
broker/queue。未执行 `task build:connectors` 的普通 dev build 会在启动时诚实告警并跳过该
destination，而不会假装它存在。

:::note[出站 webhook 是目的地，而非 API webhook]
`webhook` 是 control plane 推送目标的输出通道，而不是你针对本产品 REST API 注册的回调
——OpenAPI 文档未定义任何 `webhooks`。见 [Honesty & limits](/zh/start/honesty-and-limits/)。
:::

## 部署要求与诚实归因

R/RW 差异连接器已接入默认二进制，但其中两个带有其余连接器没有的**部署要求**——连接器代码与
host 无关，它所消费的*数据*则不然：

- **`ebpf`** 消费 [Tetragon](https://tetragon.io/) 的内核事件导出。**该连接器不需要任何内核能力**
  ——它读取一个由 Tetragon 拥有的 `0600` 文件/FIFO/`stdin`（`events_path`，默认 `-`）。Tetragon
  本身是一个**独立、加固的 DaemonSet**，持有最小的 `CAP_BPF` + `CAP_PERFMON`，以非 root 运行，
  带 seccomp/AppArmor 且无入站监听器。因此部署是：以特权运行 Tetragon（其捆绑的文件访问 +
  TCP-connect TracingPolicy），然后将 `ebpf` 指向其导出。Tetragon 最低版本：v1.0。
- **`runtime`** 读取 host 的 procfs（`proc_root`，默认 `/proc`）、Docker daemon socket
  （`docker_socket`，**默认关闭**——对 `docker.sock` 的读访问等同于 root；请刻意选择性启用，
  理想做法是经一个 GET 白名单的 socket 代理）和/或 Kubernetes API（默认为集群内 ServiceAccount）。
  只挂载你启用的内容。
- **`gcp-audit`** 以一个 GCP service account（key JSON 或 WIF/ADC 签发的 `access_token`）认证，
  且仅需**只读管理**角色：`roles/resourcemanager.organizationViewer` +
  `roles/iam.serviceAccountViewer` + `roles/logging.viewer`——读取 **Data Access** 条目额外需要
  `roles/logging.privateLogViewer`。设定 `organization_id`（org 遍历 + org 范围审计）和/或
  `projects`。Data Access 审计日志在 **GCP 中默认关闭**：按 IAM/data-access 配置启用，否则活动流
  会诚实地少报。
- **`azure-activity`** 以一个 Entra service principal（client-credentials）或一个托管身份的
  `access_token` 认证，且仅需 tenant root（或每个 subscription）上的 **Reader** 角色——该单一角色
  即可覆盖 Resource Graph、subscription 列举与 Activity Log。`subscriptions` 未设置时会自动列举
  subscription。

两者仍**在进程内**运行（传输 A）；若你倾向于把它们隔离在 host 附近的进程外**collector** 部署中，
则存在 `cmd/{pg-audit,s3-cloudtrail,ebpf-source}` go-plugin 二进制。

每个 source 都是**选择性启用、deny-closed** 的：缺失的 `log_path`/`path`/`events_path` 在启动时
是一个配置错误（该 source 未接入），绝不是静默的空操作。演示 estate（[quickstart](/zh/start/quickstart/)）
通过真实总线播下等价的合成观测，让你在接入一个实时 source 之前即可端到端看到 clean 层级的信号。

:::caution[贯穿每个层级的诚实极限]
- **缺少一条 edge 并不能证明没有访问**——在覆盖为有损、不可行、或 source 未接入处。access map
  对自身的触达范围是诚实的。
- **每个 agent 的身份是那个硬性依赖。**一个连接池背后的共享 service account 会把即便是 clean
  层级存储上的归因坍缩为 `approximate`——见 [govern and approve](/zh/how-to/govern-and-approve/)。
- **MCP 工具标注按 MCP 规范是不可信的**：一项声明的能力提示，需对照已观测 source 佐证，绝不单独信任。
:::

## 相关

- [Connect a source](/zh/how-to/connect-a-source/) —— 连接器模型及如何接入一个。
- [Connect Claude Code](/zh/how-to/connect-claude-code/) —— 端到端的协作路径。
- [Module III — the access map](/zh/reference/modules/iii-access-map/) —— edge 最终变成什么。
- [Honesty & limits](/zh/start/honesty-and-limits/) —— 贯穿全产品的诚实契约。

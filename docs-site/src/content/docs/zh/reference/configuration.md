---
title: "配置参考"
description: "Olivares AI control plane 经过验证的配置接口面：serve 标志、环境变量、存储选择，以及开箱即来的安全默认值。"
---

本页记录 control plane 引擎 —— 那个名为 `olivares` 的单一 Go 二进制文件 —— 的配置接口面。它涵盖 `serve` 子命令所接受的标志、引擎在启动时读取的环境变量、存储与策略决策点（policy decision point）如何被选定，以及在完全没有任何配置时即生效的安全默认值。

此处列出的一切都取自引擎自身的命令定义与组合根（composition root）。凡是无法在源代码中确认的设置，均不列出。关于这些默认值背后的概念性安全姿态，参见 [安全模型](/zh/explanation/security/security-model/)；关于可运行的端到端路径，参见 [自托管](/zh/how-to/self-hosting/)。

:::note[配置哲学]
引擎由标志和环境变量配置，而非由一个庞杂的配置文件。它读取的每个变量都列在下文，并从源码本身生成。配置真实数据源的密钥保留在由环境变量引用的、运营方持有的文件中 —— 绝不进入存储。默认值的选择以失败时关闭（fail closed）为准：loopback 绑定、TLS 开启、不存在默认凭据。
:::

## `serve` 子命令

`olivares serve` 在一个进程中运行 REST/Web HTTP 服务器与 gRPC 服务器，Web UI 从与 API 相同的 origin 提供服务。下表中的标志是该命令经过验证的配置输入。

| 标志 | 默认值 | 用途 |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | HTTP 监听地址（REST API + 内嵌 Web UI）。 |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC 监听地址（control-plane / 采集器摄取 API）。 |
| `--data-dir` | `$OLIVARES_DATA_DIR`、已存在的 `./olivares-data` 安装，否则 `$XDG_DATA_HOME/olivares` 或 `~/.local/share/olivares` | 数据目录：审计签名密钥、TLS 材料，以及（对 SQLite 而言）存储文件。 |
| `--engine` | `sqlite` | 存储引擎：`sqlite` 或 `postgres`。 |
| `--dsn` | 空（数据目录中的 SQLite 文件） | 存储连接字符串。 |
| `--checkpoint-interval` | `1h` | 多久在每条租户链上写入一次签名审计检查点。`0` 禁用。 |
| `--insecure` | 关 | 以明文提供 HTTP/gRPC 服务。危险；仅限 localhost 开发。 |
| `--seed-demo` | 关 | 为演示/E2E 加载一份合成的样本 estate。在非 loopback 绑定上拒绝启动。 |

TLS 默认开启。在未提供 `--tls-cert`/`--tls-key` 时，引擎会在任何监听器接受连接之前，预先一次性地在数据目录中确保一份自签名证书存在，从而让 HTTP 与 gRPC 服务器都使用同一份证书，二者都不会回退到明文。当它生成一份自签名证书时，会记录 `cert_fingerprint_sha256`（证书摘要，浏览器显示的那个）和 `pin_sha256`（叶证书 SPKI 摘要）。`--pin-sha256` 接受后者，base64 或十六进制均可；证书指纹是另一个摘要——两种写法都是 32 字节，因此解析本身能通过，随后在握手阶段以 `TLS SPKI pin mismatch` 失败，该错误会给出应当使用的值。

:::caution[`--insecure` 按设计仅限 loopback]
`--insecure` 以明文提供 HTTP 与 gRPC 服务，这会把 bearer 令牌暴露在网络中。gRPC 路径**失败时关闭（fail closed）**：在 `--insecure` 之外，服务器会拒绝构造一个明文监听器，而不是悄然降级。仅在本地开发期间针对 `127.0.0.1` 使用 `--insecure`，绝不在已发布的地址上使用。
:::

:::danger[`--seed-demo` 是合成的且自我保护]
`--seed-demo` 配置一个拥有**公开的、源代码树中密码**的演示管理员以及虚构的 estate 数据。它仅供演示与 E2E 使用。引擎拒绝在一个非 loopback 监听器上启动它：如果 `--listen` 或 `--grpc-listen` 中任一不是 loopback 地址，该命令将以一个错误退出。请使用一个用完即弃的数据目录；绝不让它指向真实数据。
:::

完整的标志清单 —— 包括用于分布式部署的仅 Postgres 标志与双向 TLS（mutual-TLS）标志 —— 见 [CLI 参考](/zh/reference/cli/)。本页记录常见的配置接口面；某些高级标志治理 [架构概览](/zh/explanation/architecture/overview/) 中所述的多节点拓扑。

## 环境变量

以下三个分组是运营方最先遇到的配置，并逐项说明其行为。随后是从引擎自身源码生成的完整清单，因此不会落后于二进制。

### 数据目录

| 变量 | 作用 |
| --- | --- |
| `OLIVARES_DATA_DIR` | 在未给出 `--data-dir` 时的默认数据目录。两者都没有时，引擎会使用已存在的 `./olivares-data` 安装，否则使用 `$XDG_DATA_HOME/olivares` 或 `~/.local/share/olivares` —— 绝不会使用当前工作目录，因为那会把私钥留在那里。 |

数据目录存放审计签名密钥、TLS 证书与密钥，以及 —— 对 SQLite 引擎而言 —— 存储文件。请在重启之间将其持久化。

### 配置真实数据源

| 变量 | 作用 |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | 指向一个 JSON 文件的路径，该文件在引擎启动前配置真实的观测源与身份名册（roster）提供方。 |

`OLIVARES_SOURCES_CONFIG` 是非演示信号源与身份名册提供方据以解析的唯一输入。它是运营方持有密钥的配置，并被刻意保留在存储之外。引擎在启动期间读取它，并在运行时启动**之前**注册每一个源。

其处理方式是诚实的，而非快速失败（fail-fast）：

- 一个**缺失的**变量产生一份空配置，引擎会告警：没有配置任何真实数据源。
- 一个**不可读或 JSON 无效**的文件会告警并产生一份空配置 —— 它绝不中止启动。
- 一个已配置但**为空**的源列表会告警：没有连接器将进行摄取，因此该 estate 在没有任何实时流量的情况下运行，而不是悄然显得健康。
- 一个空的**身份**列表会告警：名册保持为空，名册同步是一次空操作（no-op）。

这是有意为之：一个未配置的源会浮现一条告警，而不是使 control plane 崩溃或假装在工作。要真正填充访问图谱（access map），请至少配置一个源 —— 参见 [连接一个数据源](/zh/how-to/connect-a-source/)，以及对于通过 OpenTelemetry 与 MCP 的协作式 Claude Code 路径，参见 [连接 Claude Code](/zh/how-to/connect-claude-code/)。

### 授权决策点（PDP）

授权策略决策点（policy decision point）在组合根处按环境选定。原生的基于属性的访问控制（ABAC）引擎与基于角色的访问控制（RBAC）始终治理；外部 PDP 在被选定时，是一个额外的**仅限收紧（restrict-only）**层，永远无法放宽访问。

| 变量 | 作用 |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | 选择外部 PDP：`cedar`、`opa` 或 `none`（空或 `none` 表示仅原生 ABAC）。 |
| `OLIVARES_PDP_CEDAR_FILE` | 仅 Cedar 引擎：指向运营方 Cedar 策略文件的路径。 |
| `OLIVARES_PDP_OPA_URL` | 仅 OPA 引擎：Open Policy Agent 端点的基 URL。 |
| `OLIVARES_PDP_OPA_PATH` | 仅 OPA 引擎：在该端点下查询的决策路径。 |
| `OLIVARES_PDP_OPA_TOKEN` | 仅 OPA 引擎：用于 OPA 端点的 bearer 令牌。 |

一道接缝（seam）背后坐着两个适配器：一个**内嵌 Cedar** 求值器（主要的纯 Go 路径）与一个 **OPA-over-HTTP** 适配器。运营方选择其中一个引擎；两者都只能收紧、绝不能放宽内置 RBAC 已经做出的决策。

:::note[一个糟糕的策略绝不会让该平面失去治理]
如果 `OLIVARES_PDP_ENGINE` 选定了一个引擎但其配置无效 —— 一个不可读的 Cedar 文件、一个格式错误的 OPA 目标 —— 引擎会**仅禁用该外部 PDP**，保持原生 ABAC 引擎与 RBAC 继续执行，并大声地记录日志。一个损坏的策略文件绝不会悄然让请求失去治理，也绝不会使 control plane 崩溃。
:::

关于 deny-by-default（默认拒绝）模型、查看访问图谱（access graph）的特权性质，以及每一次授权读取如何被审计，参见 [安全模型](/zh/explanation/security/security-model/)。

<!-- BEGIN GENERATED olivares-env-reference — regenerate with `bash scripts/check-config-env-docs.sh --write`; do not edit by hand -->

### 完整变量参考

下表由产品自身源码生成：266 个变量与 17 个运行时构造的 family，覆盖 engine、CLI、Kubernetes operator、Terraform provider 与 connector。每次变更都会从这些源码重新生成并校验，因此不会落后于二进制文件。

**必需**表示读取该变量的功能没有它就无法启动；大多数变量是可选的，即使一个也未设置，引擎仍会运行。

| 变量 | 必需 | 默认值 | 配置内容 |
| --- | --- | --- | --- |
| `OLIVARES_ACTOR` | No | — | Default `--actor` for the decision-bearing eventing verbs, so a scripted change still records who made it. |
| `OLIVARES_ADMIN_DSN` | No | — | Privileged connection string the Kubernetes operator uses for schema migration, separate from the least-privilege runtime role. |
| `OLIVARES_AGENTCORE_EXPORT_CONFIG` | No | — | Path to the JSON configuration of the AgentCore usage export. |
| `OLIVARES_AGENT_GATEWAY_CONFIG` | No | — | Path to the JSON configuration of the MCP agent gateway. |
| `OLIVARES_ALLOW_CLEARTEXT` | No | — | Dangerous opt-in: lets a request carrying a credential reach a NON-loopback host over plain HTTP, for surfaces with no --allow-cleartext flag of their own. |
| `OLIVARES_API_TOKEN` | No | — | API token the Terraform provider authenticates with, when the provider block does not set one. |
| `OLIVARES_APPROVAL_BRIDGE_CONFIG` | No | — | Path to the JSON configuration of the bridge that routes approvals to an external system. |
| `OLIVARES_AUDIT_ARCHIVE_CONFIG` | No | — | Path to the JSON settings for the `s3archive` sink. Secret-bearing, so it is a file rather than a value. |
| `OLIVARES_AUDIT_ARCHIVE_DIR` | No | — | Root directory for the `dir` archive sink. |
| `OLIVARES_AUDIT_ARCHIVE_INTERVAL` | No | `24h` | How often sealed audit segments are archived, as a Go duration. |
| `OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS` | No | `2555` | How long archived audit segments are retained, in days. |
| `OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS` | No | — | How many events a sealed archive segment holds before the next one is started. |
| `OLIVARES_AUDIT_ARCHIVE_SINK` | No | — | Where sealed audit segments are archived: unset for off, `dir` for a local directory, `s3archive` for object storage. |
| `OLIVARES_AUDIT_LEGALHOLD_INTERVAL` | No | — | How often the long-horizon legal-hold sweep runs, as a Go duration. |
| `OLIVARES_AUDIT_META_BLINDING` | No | — | Whether audit metadata commitments are written blinded, and how strictly that is required. |
| `OLIVARES_AUDIT_SIGNING_KEY` | No | — | Audit checkpoint signing key, inline. Prefer the file form so the key never sits in a process environment. |
| `OLIVARES_AUDIT_SIGNING_KEY_FILE` | No | — | Path to the audit checkpoint signing key. This is the operator-held form. |
| `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the audit signing key wrapped by a key management service, unwrapped at boot. |
| `OLIVARES_AUDIT_SPOOL_MAX_BYTES` | No | — | Upper bound on the on-disk audit spool before the full-spool rule applies. |
| `OLIVARES_AUDIT_SPOOL_ON_FULL` | No | — | What happens when the audit spool is full: the deny-closed posture refuses the write rather than dropping the record. |
| `OLIVARES_AUTHZEN_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to reach the AuthZEN endpoints. Unset leaves the endpoint reachable wherever the listener is. |
| `OLIVARES_AUTHZEN_DISABLED` | No | — | Set to a true value to turn the AuthZEN decision endpoint off. |
| `OLIVARES_AUTHZEN_EXPORT_DISABLED` | No | — | Set to a true value to turn the AuthZEN export endpoint off. |
| `OLIVARES_AUTHZEN_SEARCH_DISABLED` | No | — | Set to a true value to turn the AuthZEN search endpoints off while leaving decisions on. |
| `OLIVARES_BASE_URL` | No | — | Public base URL of this control plane, used where an absolute link back to it has to be produced. |
| `OLIVARES_BUS_CONFIG` | No | — | Path to the JSON configuration of the message bus the engine publishes on. |
| `OLIVARES_CAEP_TRANSMITTER_CONFIG` | No | — | Path to the JSON configuration of the CAEP transmitter that pushes shared-signal events. |
| `OLIVARES_CATALOG_SIGNING_KEY` | No | — | Catalog signing key, inline. Prefer the file form. |
| `OLIVARES_CATALOG_SIGNING_KEY_FILE` | No | — | Path to the catalog signing key. |
| `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the catalog signing key wrapped by a key management service. |
| `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG` | No | — | Path to the JSON configuration of the administrative actuator that applies changes at the model provider. |
| `OLIVARES_CLAUDE_ADMIN_KEY` | No | — | Administrative API key used to read identity posture from the model provider. |
| `OLIVARES_CLAUDE_ERASER_CONFIG` | No | — | Path to the JSON configuration of the erasure actuator that carries out deletion requests. |
| `OLIVARES_CLAUDE_FILES_CONFIG` | No | — | Path to the JSON configuration of the provider file inventory scan. |
| `OLIVARES_CLAUDE_INFERENCE_KEY` | No | — | API key the engine uses for its own inference calls. Unset leaves the inference-backed features off. |
| `OLIVARES_CLAUDE_WORKSPACE_ID` | No | — | Workspace whose identity posture is read, when the administrative key spans several. |
| `OLIVARES_CLI_CONFIG` | No | — | Path to the CLI configuration file, replacing the default per-user location. Used by hermetic automation. |
| `OLIVARES_CLI_TRAMPOLINE` | No | — | Set to `1` inside a re-executed child process so the binary runs the requested subcommand instead of the outer test harness. |
| `OLIVARES_CODEX_HOOK_ACCOUNT` | No | — | Account the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_AGENT` | No | — | Agent identity the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_ORG` | No | — | Organization the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Codex hook enforcement point server. |
| `OLIVARES_CODEX_HOOK_TENANT` | No | — | Tenant the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_TOKEN` | No | — | Token the Codex hook client presents to the enforcement point. |
| `OLIVARES_CODEX_HOOK_URL` | No | — | Base URL of the enforcement point the Codex hook client calls. |
| `OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE` | Yes | — | Path to the JSON keyring the communication content sealer loads at boot (cmd/olivares/boot.go). Secret-bearing, so it is a file rather than a value: sealed message bodies are verified against the keys it carries, and an engine started without it cannot open content sealed by a peer that had one. |
| `OLIVARES_COMMUNICATION_TOKEN` | Yes | — | NOT an operator setting, and documented here precisely so nobody sets it. The engine MINTS this bearer and injects it into a conducted session's child process exactly once (modules/sessions/runtime_bridge.go); its tuple travels inside the authenticated principal. It is RESERVED on the launch path: validateLaunchInjectedEnv (modules/sessions/runtime.go) refuses any launch whose injected environment carries it, so a caller-supplied value is rejected rather than honoured. It appears in the roster because that reserved-name check mentions it, not because the engine reads it. |
| `OLIVARES_CONFIG_STRICT` | No | — | Set to `1` to make `olivares config effective` and `config validate` reject any unrecognized `OLIVARES_*` key. |
| `OLIVARES_CONTEXT_MAX_TOKENS` | No | — | Upper bound on the context a governed session may assemble, in tokens. |
| `OLIVARES_CONTEXT_STRATEGY` | No | — | Which strategy assembles a governed session's context when the bound is reached. |
| `OLIVARES_DATA_DIR` | No | — | Data directory used when `--data-dir` is not given: audit signing key, TLS material and, for SQLite, the store file. |
| `OLIVARES_DB_MAX_CONNS` | No | — | Upper bound on pooled database connections. Unset leaves the driver default. |
| `OLIVARES_DEPLOY_EXECUTOR_CONFIG` | No | — | Path to the JSON configuration of the executor that applies deployment changes. |
| `OLIVARES_DR_KEK_FILE` | No | — | Path to a raw 32-byte key-encryption key for backups, for the path where a key management service does the unwrapping. |
| `OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE` | No | — | Path to the file holding the offsite access key id, so the credential stays out of the environment. |
| `OLIVARES_DR_OFFSITE_BUCKET` | No | — | Offsite bucket for disaster-recovery bundles. Setting it turns offsite replication on. |
| `OLIVARES_DR_OFFSITE_ENDPOINT` | No | — | S3-compatible endpoint for offsite replication. Unset means AWS S3 in the configured region. |
| `OLIVARES_DR_OFFSITE_PREFIX` | No | — | Key prefix for bundles inside the offsite bucket. |
| `OLIVARES_DR_OFFSITE_REGION` | No | — | Region for offsite replication. |
| `OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE` | No | — | Path to the file holding the offsite secret access key. |
| `OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE` | No | — | Path to the file holding an offsite session token, for temporary credentials. |
| `OLIVARES_DR_PASSPHRASE_FILE` | No | — | Path to the backup passphrase file, from which the backup key-encryption key is derived. |
| `OLIVARES_DR_SCHEDULE_INTERVAL` | No | — | How often the scheduled backup runs, as a Go duration. |
| `OLIVARES_DSN` | No | — | Store connection string injected by the Kubernetes operator into the engine it manages. |
| `OLIVARES_DURABLE_BUS_CONFIG` | No | — | Path to the JSON configuration of the durable bus, for at-least-once delivery across replicas. |
| `OLIVARES_EMBEDDINGS_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_BASE_URL` | No | — | Endpoint the openai embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_DIM` | No | — | Vector dimension the openai provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_GEO` | No | — | Region or data-residency hint sent to the openai provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_KEY` | No | — | Api key for the openai embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_MODEL` | No | — | Embedding model requested from the openai provider. |
| `OLIVARES_EMBEDDINGS_PROVIDER` | No | — | Which embeddings provider is used, pinning one instead of taking the first that is configured. |
| `OLIVARES_EMBEDDINGS_REQUIRE` | No | — | Set to a true value to make a missing or unusable embeddings provider a refusal rather than a degraded index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL` | No | — | Endpoint the self-hosted embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM` | No | — | Vector dimension the self-hosted provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO` | No | — | Region or data-residency hint sent to the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY` | No | — | Api key for the self-hosted embeddings provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL` | No | — | Embedding model requested from the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL` | No | — | Endpoint the voyage embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_VOYAGE_DIM` | No | — | Vector dimension the voyage provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_VOYAGE_GEO` | No | — | Region or data-residency hint sent to the voyage provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_KEY` | No | — | Api key for the voyage embeddings provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_MODEL` | No | — | Embedding model requested from the voyage provider. |
| `OLIVARES_ENDPOINT` | No | — | Control-plane base URL the Terraform provider talks to, when the provider block does not set one. |
| `OLIVARES_ENGINE` | No | — | Store engine the Kubernetes operator selects for the engine it manages: `sqlite` or `postgres`. |
| `OLIVARES_EVALS_MONITOR_WINDOW` | No | — | Time window the evaluation monitor scores, as a Go duration. |
| `OLIVARES_EVENTING_ALLOW_LOOPBACK` | No | — | Set to a true value to allow loopback destinations. Single-box development only, because the default refusal is what blocks server-side request forgery. |
| `OLIVARES_EVENTING_DISPATCH_INTERVAL` | No | `15s` | How often queued events are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_EVENTING_EGRESS_POLICY` | No | — | Path to the JSON policy that decides which destinations outbound events may reach. A policy that does not parse leaves eventing unwired rather than open. |
| `OLIVARES_EVENTING_RETENTION` | No | `168h` | How long delivered events are kept for replay, as a Go duration. |
| `OLIVARES_EVENTING_SECRET_KEY` | No | — | Key that encrypts eventing subscription signing secrets at rest. |
| `OLIVARES_EXTRA_ARGS` | No | — | Extra `serve` arguments appended by the packaged service unit, for operators who configure the daemon through an environment file. |
| `OLIVARES_GROK_HOOK_ACCOUNT` | No | — | Account the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_AGENT` | No | — | Agent identity the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_ORG` | No | — | Organization the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Grok Build hook enforcement point server. Absent mounts nothing; a path given must be readable and valid or startup fails closed. |
| `OLIVARES_GROK_HOOK_TENANT` | No | — | Tenant the Grok Build hook client acts in. |
| `OLIVARES_GROK_HOOK_TOKEN` | No | — | Bearer credential the Grok Build hook client presents to the enforcement point. |
| `OLIVARES_GROK_HOOK_URL` | No | — | Endpoint of the enforcement point the Grok Build hook client calls; unset denies, deny-closed. |
| `OLIVARES_GUARDIAN_SWEEP_INTERVAL` | No | — | How often the guardian sweep runs, as a Go duration. `0` disables it. |
| `OLIVARES_HA_LEADER_GATE` | No | — | Set to `1` to make background loops run only on the elected leader, so a multi-replica deployment does not run them twice. |
| `OLIVARES_HA_LEADER_LABEL` | No | — | Label this replica publishes when it holds leadership, so an operator can route to the leader. |
| `OLIVARES_HITL_CONFIG` | No | — | Path to the JSON configuration of the human-in-the-loop review path. |
| `OLIVARES_HOOK_FIREWALL_CONFIG` | No | — | Path to the JSON configuration of the data-loss firewall that runs inside the hook. Unset leaves that half off. |
| `OLIVARES_HOOK_PEP_ACCOUNT` | No | — | Account the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_AGENT` | No | — | Agent identity the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the hook enforcement point server. |
| `OLIVARES_HOOK_PEP_ORG` | No | — | Organization the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TENANT` | No | — | Tenant the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TOKEN` | No | — | Token the Claude Code hook client presents to the enforcement point. |
| `OLIVARES_HOOK_PEP_URL` | No | — | Base URL of the enforcement point the Claude Code hook client calls. |
| `OLIVARES_INCIDENTLOOP_CONFIG` | No | — | Path to the JSON configuration of the incident close-the-loop subscriber. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_INFERENCE_PROXY_CONFIG` | No | — | Path to the JSON configuration of the governed inference proxy. |
| `OLIVARES_INGEST_TOKEN` | No | — | Bearer token the collector ingest endpoint requires from telemetry senders. |
| `OLIVARES_INSECURE` | No | — | Set to `1` to let the CLI talk to a plaintext or untrusted-TLS endpoint. Local development only. |
| `OLIVARES_KEY_CUSTODY` | No | — | Custody posture required of the audit signing key: whether a raw on-disk key is accepted or a wrapped one is demanded. |
| `OLIVARES_KEY_WRAP_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_OLD` | No | — | Previous key management backend during a rewrap migration, so keys wrapped by it can still be unwrapped. |
| `OLIVARES_LEDGER_CUSTODY` | No | — | Custody posture required of the ledger checkpoint signer, the ledger counterpart of the audit key posture. |
| `OLIVARES_LEDGER_KMS_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG` | No | — | Signing algorithm requested from AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_SIGNER` | No | — | Off-box checkpoint signer to use: which key management backend signs audit checkpoints instead of a local key. |
| `OLIVARES_LICENSE` | No | — | License document itself, inline, for deployments that cannot mount a file. |
| `OLIVARES_LICENSE_PATH` | No | — | Path to the license document on disk. Takes effect before the inline form. |
| `OLIVARES_LICENSE_PUBKEY` | No | — | Public key the engine verifies the license signature against. |
| `OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS` | No | — | Set to `1` to make live ingest inspect observed references, which costs more per event. |
| `OLIVARES_LOG_LEVEL` | No | — | Minimum log level the engine emits: `debug`, `info`, `warn` or `error`. |
| `OLIVARES_MCP_TASK_KILLSWITCH_SWEEP` | No | — | How often a running MCP task is re-checked against the kill switch, as a Go duration. |
| `OLIVARES_METRICS_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to scrape the metrics endpoint. |
| `OLIVARES_METRICS_TOKEN` | No | — | Bearer token the metrics endpoint requires. Unset leaves the endpoint unauthenticated behind whatever the listener exposes. |
| `OLIVARES_NHI_ACTUATORS_CONFIG` | No | — | Path to the JSON configuration of the actuators that act on non-human identities. |
| `OLIVARES_NIS2INCIDENT_CONFIG` | No | — | Path to the JSON configuration of NIS2 incident reporting. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_NOTIFY_CONFIG` | No | — | Path to the JSON list of notification destinations. Secret-bearing, so it stays out of the store. |
| `OLIVARES_NOTIFY_DISPATCH_INTERVAL` | No | — | How often queued notifications are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_OIDC_CLIENT_ID` | Yes | — | OIDC client id for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_CLIENT_SECRET` | Yes | — | OIDC client secret for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_GROUPS_CLAIM` | No | — | ID-token or UserInfo claim carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_OIDC_ISSUER` | Yes | — | OIDC issuer URL. Required when the protocol is `oidc`. |
| `OLIVARES_ORCH_CADENCE_INTERVAL` | No | — | How often the orchestration cadence loop runs, as a Go duration. `0` disables it. |
| `OLIVARES_ORCH_DISPATCH_CONFIG` | No | — | Path to the JSON configuration for orchestration dispatch targets. |
| `OLIVARES_ORCH_WORKFLOW_INTERVAL` | No | `15s` | How often the orchestration workflow loop advances waiting runs, as a Go duration. |
| `OLIVARES_ORCH_WORKFLOW_MAX` | No | — | Upper bound on concurrently advancing workflow runs. |
| `OLIVARES_ORCH_WORKFLOW_STEPS_MAX` | No | — | Upper bound on the steps one workflow run may take, which stops a loop from running forever. |
| `OLIVARES_OTA_PUBKEY` | No | — | Public key the engine verifies a downloaded update bundle against. |
| `OLIVARES_OTEL_ENABLED` | No | — | Set to a true value to export traces. Setting an endpoint turns export on as well. |
| `OLIVARES_OTEL_ENDPOINT` | No | — | OTLP endpoint traces are exported to. Falls back to the standard `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `OLIVARES_OTEL_GENAI_COMPAT` | No | — | Set to a true value to also emit the generative-AI semantic-convention attributes on spans. |
| `OLIVARES_OTEL_INSECURE` | No | — | Set to a true value to export traces over plaintext. Local development only. |
| `OLIVARES_OTEL_PROTOCOL` | No | — | OTLP protocol used for export. Falls back to the standard `OTEL_EXPORTER_OTLP_PROTOCOL`. |
| `OLIVARES_OTEL_SAMPLE_RATIO` | No | — | Fraction of traces sampled, between 0 and 1. |
| `OLIVARES_OTEL_SERVICE_NAME` | No | — | Service name reported on exported traces. |
| `OLIVARES_PDP_CEDAR_FILE` | No | — | Path to the Cedar policy file, for the `cedar` decision point. |
| `OLIVARES_PDP_ENGINE` | No | — | External policy decision point to add on top of the native engine: `cedar`, `opa` or `none`. |
| `OLIVARES_PDP_OPA_PATH` | No | — | Decision path queried under the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | No | — | Bearer token for the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_URL` | No | — | Base URL of the Open Policy Agent endpoint, for the `opa` decision point. |
| `OLIVARES_PIV_CONFIG` | No | — | Path to the JSON configuration for smart-card privileged login. |
| `OLIVARES_PLUGIN` | No | — | Handshake cookie an out-of-process connector plugin must present. Set by the engine when it launches the plugin, not by the operator. |
| `OLIVARES_POLICY_MAX_STALENESS` | No | — | How stale a cached policy decision may be before it is refused, as a Go duration. |
| `OLIVARES_POLICY_SIGNING_KEY` | No | — | Policy bundle signing key, inline. Prefer the file form. |
| `OLIVARES_POLICY_SIGNING_KEY_FILE` | No | — | Path to the policy bundle signing key. |
| `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the policy signing key wrapped by a key management service. |
| `OLIVARES_RATELIMIT_CONFIG` | No | — | Path to the JSON rate-limit policy the engine applies to its own endpoints. |
| `OLIVARES_RATELIMIT_STORE` | No | — | Where rate-limit counters live, which decides whether limits are per replica or shared. |
| `OLIVARES_REPORTING_CONFIG` | No | — | Path to the JSON configuration of the reporting add-on. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_REPORTING_SCHEDULE_INTERVAL` | No | — | How often scheduled reports are generated, as a Go duration. |
| `OLIVARES_REPORT_CACHE_DIR` | No | — | Directory where generated report artifacts are cached. |
| `OLIVARES_RETENTION_SWEEP_INTERVAL` | No | — | How often the retention sweep deletes data past its retention window, as a Go duration. |
| `OLIVARES_SAML_ACS_URL` | Yes | — | Assertion consumer service URL of this service provider, where the identity provider posts the assertion. |
| `OLIVARES_SAML_EMAIL_ATTRIBUTE` | No | — | Assertion attribute carrying the user's email. Unset tries the common attribute names. |
| `OLIVARES_SAML_GROUPS_ATTRIBUTE` | No | — | Multi-valued assertion attribute carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_SAML_IDP_METADATA_URL` | No | — | Identity-provider metadata URL, from which the SAML endpoints and certificate are read. |
| `OLIVARES_SAML_IDP_SSO_URL` | No | — | Identity-provider single sign-on URL, for the path where metadata is not fetched. |
| `OLIVARES_SAML_SP_CERT_PEM` | No | — | Service-provider encryption certificate in PEM, published as the encryption key descriptor. |
| `OLIVARES_SAML_SP_ENTITY_ID` | Yes | — | Entity id this control plane presents as the SAML service provider. Required when the protocol is `saml`. |
| `OLIVARES_SAML_SP_KEY_PEM` | No | — | Service-provider encryption private key in PEM, which decrypts encrypted assertions. |
| `OLIVARES_SAML_SP_SIGN_CERT_PEM` | No | — | Service-provider signing certificate in PEM, published as the signing key descriptor. |
| `OLIVARES_SAML_SP_SIGN_KEY_PEM` | No | — | Service-provider signing private key in PEM, which signs authentication requests. |
| `OLIVARES_SANDBOX_RUNTIME_CONFIG` | No | — | Path to the JSON configuration of the sandbox runtime that isolates agent execution. |
| `OLIVARES_SECRETREF_AWS_REGION` | No | — | Region used for AWS Secrets Manager references. Falls back to `AWS_REGION` and `AWS_DEFAULT_REGION`. |
| `OLIVARES_SECRETREF_AZURE_API_VERSION` | No | — | Azure Key Vault API version requested. |
| `OLIVARES_SECRETREF_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. |
| `OLIVARES_SECRETREF_AZURE_VAULT_URL` | No | — | Default Azure Key Vault URL for references that do not name one. |
| `OLIVARES_SECRETREF_GCP_ENDPOINT` | No | — | Endpoint override for Google Secret Manager. |
| `OLIVARES_SECRETREF_GCP_PROJECT` | No | — | Default Google Cloud project for Secret Manager references that do not name one. |
| `OLIVARES_SECRETREF_GCP_TOKEN` | No | — | Token used against Google Secret Manager. |
| `OLIVARES_SECRETREF_INFISICAL_ENV` | No | — | Default Infisical environment for references that do not name one. |
| `OLIVARES_SECRETREF_INFISICAL_TOKEN` | No | — | Token used against Infisical. |
| `OLIVARES_SECRETREF_INFISICAL_URL` | No | — | Base URL of the Infisical server secret references resolve against. |
| `OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID` | No | — | Default Infisical workspace for references that do not name one. |
| `OLIVARES_SECRETREF_K8S_APISERVER` | No | — | Kubernetes API server secret references resolve against. |
| `OLIVARES_SECRETREF_K8S_CA_FILE` | No | — | Path to the certificate authority bundle used to verify the Kubernetes API. |
| `OLIVARES_SECRETREF_K8S_TOKEN_FILE` | No | — | Path to the service-account token file used against the Kubernetes API. |
| `OLIVARES_SECRETREF_VAULT_ADDR` | No | — | Address of the HashiCorp Vault server secret references resolve against. Falls back to `VAULT_ADDR`. |
| `OLIVARES_SECRETREF_VAULT_NAMESPACE` | No | — | Vault namespace secret references resolve in. Falls back to `VAULT_NAMESPACE`. |
| `OLIVARES_SECRETREF_VAULT_TOKEN` | No | — | Token used against HashiCorp Vault. |
| `OLIVARES_SECRET_STORE_KEY` | No | — | Key that encrypts operator secrets held in the store. |
| `OLIVARES_SERVER_URL` | No | — | Base URL of the control plane the CLI talks to, when `--server` is not given. |
| `OLIVARES_SESSION_BUDGET_AVAILABILITY` | No | — | Whether session budget enforcement is required, and what happens when the budget service cannot answer. |
| `OLIVARES_SESSION_CONTEXT_AVAILABILITY` | No | — | Whether session context governance is required, and what happens when the context service cannot answer. |
| `OLIVARES_SESSION_KILLSWITCH_SWEEP` | No | `15s` | How often an active session is re-checked against the kill switch, as a Go duration. `0` leaves only the check at launch. |
| `OLIVARES_SESSION_PEP_TOKEN_FILE` | No | — | Path to the file holding the token the session enforcement point requires. |
| `OLIVARES_SESSION_PEP_URL` | No | — | Base URL of the policy enforcement point a governed agent session calls before acting. |
| `OLIVARES_SESSION_RUNTIME_BASE_URL` | No | — | Base URL the launched session runtime calls back to. |
| `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | No | `claude` | Executable the session runtime launches. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` | No | — | Path to the file holding the session runtime's credential, refreshed by rotation. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_TTL` | No | `15m` | Lifetime of a minted session runtime credential, as a Go duration. |
| `OLIVARES_SESSION_RUNTIME_WIF` | No | — | Whether the session runtime takes its credential from workload identity federation instead of a token file. |
| `OLIVARES_SESSION_RUNTIME_WIF_RULE` | No | — | Which federation rule the session runtime exchanges its workload identity under. |
| `OLIVARES_SIEM_FORWARD_INTERVAL` | No | — | How often signed ledger records are forwarded to the configured SIEM, as a Go duration. |
| `OLIVARES_SOURCES_CONFIG` | No | — | Path to the JSON file that wires real observation sources and identity roster providers before the engine starts. |
| `OLIVARES_SSO_PROTOCOL` | No | — | Single sign-on protocol to wire: `oidc` or `saml`. Unset means no federation, and the endpoints report it rather than half-wiring one. |
| `OLIVARES_SSO_SECRET_KEY` | No | — | Key that encrypts the federation client secret and service-provider private keys at rest. |
| `OLIVARES_TARGET_BINDING_KEY` | No | — | Key that binds an orchestration target to this deployment, inline. Prefer the file form. |
| `OLIVARES_TARGET_BINDING_KEY_FILE` | No | — | Path to the orchestration target binding key. |
| `OLIVARES_TENANT` | No | — | Default tenant for CLI commands, when `--tenant` is not given. |
| `OLIVARES_THREATINTEL_CONFIG` | No | — | Path to the JSON configuration of threat-intelligence ingest. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_THREATINTEL_SIGNING_KEY` | No | — | Signing key for threat-intelligence bundles the engine publishes. |
| `OLIVARES_TOKEN` | No | — | API token the CLI authenticates with, when `--token` is not given. |
| `OLIVARES_UPDATE_CHANNEL` | No | — | Release channel the update check asks for, such as `stable`. |
| `OLIVARES_UPDATE_ENDPOINT` | No | — | Base URL the update check queries. Unset leaves the update check off. |
| `OLIVARES_UPGRADE_TOKEN` | No | — | Download token `olivares upgrade` presents when fetching a build from a credentialed repository. |
| `OLIVARES_VECTOR_API_KEY` | No | — | API key for the external vector index. |
| `OLIVARES_VECTOR_BACKEND` | No | — | Which vector index backs knowledge search. Unset keeps the in-process index, which is the air-gapped default. |
| `OLIVARES_VECTOR_DIM` | No | — | Vector dimension of the index, which has to match the embeddings model. |
| `OLIVARES_VECTOR_DSN` | No | — | Connection string for the external vector index. |
| `OLIVARES_VECTOR_NAMESPACE` | No | `knowledge_ann` | Table or collection the vector index writes to. |
| `OLIVARES_VECTOR_TIMEOUT` | No | — | Per-request timeout for the vector index, as a Go duration. |
| `OLIVARES_VOICE_CALL_CONFIG` | No | — | Path to the JSON configuration of the inbound voice webhook. |
| `OLIVARES_VOICE_DISPATCH_CONFIG` | No | — | Path to the JSON configuration of outbound voice dispatch. |
| `OLIVARES_WEBAUTHN_ORIGINS` | No | — | Comma-separated origins accepted for WebAuthn ceremonies. |
| `OLIVARES_WEBAUTHN_RPID` | No | — | WebAuthn relying-party id for the privileged-login flow. It has to match the site's registrable domain. |
| `OLIVARES_WEBAUTHN_RP_NAME` | No | — | Display name of the WebAuthn relying party, as shown by the authenticator. |
| `OLIVARES_WIF_BASE_URL` | No | — | Endpoint the workload identity exchange is performed against. |
| `OLIVARES_WIF_REFRESH_SLACK` | No | `60s` | How long before expiry a federated credential is refreshed, as a Go duration. |
| `OLIVARES_WIF_SPIFFE_SOCKET` | No | — | Path to the SPIFFE workload API socket the engine fetches its identity from. |
| `OLIVARES_WIF_TRUST_DOMAIN` | No | — | SPIFFE trust domain accepted for workload identity. |
| `OLIVARES_WORK_OUTBOX_INTERVAL` | No | — | How often the work-kernel outbox is drained, as a Go duration. `0` disables the pump. |
| `OLIVARES_WORK_RUN_REF` | No | — | Run reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_SESSION_ID` | No | — | Session reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_TOKEN` | No | — | Scoped token the engine passes to a launched work session. Set by the engine per run, not by the operator. |

### Variable families

These prefixes name families whose member variables are built at runtime — the per-provider and per-backend keys the engine composes from a provider name. The concrete members it composes are in the table above.

| Prefix | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_AUDIT_ARCHIVE_` | No | — | Family prefix for the audit archive settings listed above. |
| `OLIVARES_CODEX_HOOK_` | No | — | Family prefix for the Codex hook client and server settings listed above. |
| `OLIVARES_DR_OFFSITE_` | No | — | Family prefix for the offsite replication settings listed above. |
| `OLIVARES_EMBEDDINGS` | No | — | Family stem for the unprefixed embeddings settings, which configure the OpenAI-compatible provider. |
| `OLIVARES_EMBEDDINGS_` | No | — | Family prefix from which the per-provider embeddings keys are built, by appending the provider name and then the setting. |
| `OLIVARES_GROK_HOOK_` | No | — | Family prefix for the Grok Build hook client and server settings listed above. |
| `OLIVARES_HOOK_PEP_` | No | — | Family prefix for the Claude Code hook client and server settings listed above. |
| `OLIVARES_KEY_WRAP` | No | — | Family stem naming the key management backend that wraps signing keys. |
| `OLIVARES_KEY_WRAP_` | No | — | Family prefix from which the per-backend key-wrapping keys are built. |
| `OLIVARES_LEDGER_KMS_` | No | — | Family prefix from which the per-backend ledger signer keys are built. |
| `OLIVARES_OIDC_` | No | — | Family prefix for the OIDC federation settings listed above. |
| `OLIVARES_OTEL_` | No | — | Family prefix for the trace export settings listed above. |
| `OLIVARES_SAML_` | No | — | Family prefix for the SAML federation settings listed above. |
| `OLIVARES_SESSION_RUNTIME_` | No | — | Family prefix for the session runtime settings listed above. |
| `OLIVARES_VECTOR_` | No | — | Family prefix for the vector index settings listed above. |
| `OLIVARES_WIF_` | No | — | Family prefix for the workload identity federation settings listed above. |
| `OLIVARES_WORK_` | No | — | Family prefix for the per-run values the engine passes into a launched work session. |

<!-- END GENERATED olivares-env-reference -->

## 存储选择

引擎从 `--engine` 选定其存储引擎。

| 引擎 | 何时使用 | 备注 |
| --- | --- | --- |
| `sqlite`（默认） | 单一二进制、单节点、离线（air-gapped）安装。 | 纯 Go 内嵌存储；零外部依赖。在没有 `--dsn` 时，存储文件位于数据目录中。 |
| `postgres` | 多租户与横向扩展（scale-out）部署。 | 增加行级安全（row-level-security）租户隔离。需要一个最小权限的应用角色。 |

SQLite 是默认值且不需要任何外部服务。选择 `postgres` 即选择启用隔离租户的行级安全兜底：引擎**拒绝**在一个 Postgres 超级用户或 `BYPASSRLS` 角色上启动，除非该守护被显式覆盖，因为这样的角色会禁用租户隔离兜底。Compose Postgres 覆盖层（overlay）在首次初始化时配置最小权限应用角色，使这道兜底真正生效。

:::tip[默认存储有意保持平淡]
这里的 SQLite 不是一个玩具默认项。它是面向单节点拓扑的、就绪即离线（air-gap-ready）、零依赖的存储，也是一键式 Docker Compose 部署所运行的存储。在你需要多租户隔离或横向扩展时再转向 Postgres，而不是更早。参见 [自托管](/zh/how-to/self-hosting/) 与 [架构概览](/zh/explanation/architecture/overview/)。
:::

## 审计检查点间隔

审计账本（audit ledger）是仅追加、哈希链式（hash-chained）的，并由 Ed25519 签名的检查点锚定。`--checkpoint-interval` 控制多久在每条租户链上写入一次签名检查点（默认 `1h`；`0` 禁用检查点）。在存储关闭之前会写入一个最终的关停检查点，因此该链在干净关停时与在间隔上同样得到锚定。签名导出与转发路径见 [将审计转发到 Splunk](/zh/how-to/forward-audit-to-splunk/)。

## 安全默认值

这些是在除 `serve` 之外没有任何配置时即生效的姿态。它们是产品的默认安全立场，而非可选的加固。

| 领域 | 默认值 | 含义 |
| --- | --- | --- |
| 凭据 | 不随附 | 不存在默认用户名或密码。在没有任何用户的首次启动时，引擎铸造一个单次使用的设置令牌，并仅将其打印到标准输出 —— 绝不打印到日志。 |
| 首次启动设置 | 一次性令牌 | 管理员用该令牌创建第一个用户，然后登录。该令牌只显示一次且单次使用。 |
| 传输 | TLS 开启 | HTTP 与 gRPC 默认通过 TLS 提供服务；若未提供证书，则在数据目录中生成一份自签名证书，并记录其证书指纹与其 `--pin-sha256` 值。 |
| 绑定地址 | Loopback | `--listen` 与 `--grpc-listen` 默认为 `127.0.0.1`。引擎绑定本机，直到你刻意将其发布。 |
| 明文模式 | 关 | `--insecure` 是提供明文服务的唯一途径，而 gRPC 路径失败时关闭（fail closed）而非降级。仅供 localhost 开发使用。 |
| 演示填充 | 关 | `--seed-demo` 默认关闭，并拒绝任何非 loopback 绑定，因为它会铸造一个公开密码的演示管理员。 |
| 遥测回传 | 关 | 引擎不向母公司回传（phone home）：不存在向厂商发送遥测的通道，运行过程中也不会作为副作用发送任何内容。出站连接存在于你所配置的源，以及你主动运行的 `olivares upgrade`——除非用 `--endpoint` 或 `--bundle` 指向别处，否则它会连接更新通道。这正是使[离线（air-gapped）安装](/zh/how-to/air-gap-install/)在零出口（egress）下成为可能的原因。 |

:::caution[默认 loopback，按需暴露]
默认的 loopback 绑定意味着在你更改它们之前，引擎在本机之外不可达。当你确实将其发布时 —— 例如通过在 Docker Compose 中映射一个主机端口 —— 那是一次刻意的运营决策，而 TLS 已经开启以保护它。不要把一个已发布的绑定与 `--insecure` 搭配使用。
:::

### 首次启动，实践中

在一次全新安装上，引擎会向标准输出打印一个 `FIRST-BOOT SETUP` 块，其中包含一次性设置令牌。管理员用它创建第一个用户，然后进行认证。在 Docker Compose 下，该令牌从容器日志中读取：

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# then open https://localhost:8443 (self-signed TLS by default)
```

设置端点与登录端点是产品 OpenAPI 契约的一部分；参见 [API 参考](/reference/api/)。它们背后不透明（opaque）的会话与 API 密钥令牌模型在 [安全模型](/zh/explanation/security/security-model/) 中描述。

## 本页未涵盖的内容

这是经过验证的、常见的配置接口面。它**不**枚举面向多节点与双向 TLS 拓扑的每一个高级标志 —— 那些属于 [架构概览](/zh/explanation/architecture/overview/) 中所述、并在 [CLI 参考](/zh/reference/cli/) 中完整列出的分布式与离线（air-gapped）部署。凡是设置处于设计阶段或与特定拓扑相关之处，都记录在那里，而非在此作为一个稳定的旋钮呈现。

关于产品所观测内容的边界以及覆盖在何处分层，请阅读 [诚实与局限](/zh/start/honesty-and-limits/)。

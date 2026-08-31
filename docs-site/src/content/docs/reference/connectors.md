---
title: Connector catalog & coverage tiers
description: >-
  The first-party connectors the control plane can wire today, grouped by the
  honest coverage tier each one supports — clean, lossy, impossible-passively,
  cooperative, and approximate-by-attribution — plus the output destinations.
---

This page is the **catalog** of first-party connectors and, for each, the **honest
coverage tier** it can support. It is the companion to
[connect a source](/how-to/connect-a-source/), which explains the connector *model*
(observe-only, minimal-data, the three observation kinds) — read that first. This page
answers the next question: *which sources exist, and how good is each one's signal?*

Coverage is **tiered by what a system's audit surface can honestly tell you**, never by
how much we wish it could. The tiers, as used throughout the docs:

- **Cooperative** — an agent or platform that reports what it did (OpenTelemetry, a
  vendor admin API). Highest fidelity *when present*; depends on the source cooperating.
- **Clean** — a store that classifies read vs write **natively**, taken verbatim from
  its own audit trail (SQL audit, object-store / warehouse data-access logs).
- **Lossy** — a store whose audit cannot cleanly separate read from write or caller from
  caller (document stores, lineage). Edges land, but often `approximate`.
- **Impossible passively** — a system with no usable passive audit surface (in-memory
  caches, embedded single-file databases). There is no honest read-first signal; the
  product does not pretend otherwise.
- **Approximate-by-attribution** — the access is real but the attribution is to a role,
  process or shared credential, not a resolved agent, so the edge is `approximate`.
- **Untrusted hint** — a declared capability (an MCP tool annotation), corroborated,
  never trusted alone.

:::caution[What this catalog reflects: connectors wired into the current build]
This lists connectors **registered in the default binary's connector set** today —
i.e. kinds you can name in `OLIVARES_SOURCES_CONFIG` and have the engine wire. The
product is pre-1.0. The canonical R/RW access-map connectors — **pgAudit**,
**S3/CloudTrail**, the **eBPF/Tetragon** backstop, the **runtime** inventory and **MCP**
introspection — and the **knowledge document sources** are now wired and configurable
in a stock `serve`; some carry **deployment requirements** (a Tetragon sensor, host
access) covered in [Deployment requirements](#deployment-requirements-and-honest-attribution)
below. Coverage is **tiered honestly**: a connector's presence here is not a claim of
firm per-agent attribution, which remains the hard dependency (a shared account collapses
even a clean-tier store to `approximate`).
:::

## Cooperative — Claude & vendor telemetry

The highest-fidelity sources when present. The Claude Code runtime source runs
**out-of-process** as an embedded plugin (a plain dev build omits it and the boot warns
honestly rather than appearing healthy).

| Kind | Observes | Notes |
|---|---|---|
| `claude` | Claude Code OTLP tool telemetry + MCP introspection → edges / cost / findings | Out-of-process plugin; `attributed` when a per-agent identity is present, else `approximate` |
| `claude-api` | Claude Admin-API cost samples + governance-posture findings | In-process; a no-op offline (no admin key) |
| `claude-compliance` | Claude Compliance activity-feed evidence → findings | GET-only by construction; no-op offline |
| `claude-config` | Static Claude config tree (subagents / Skills / plugins) → **declared-capability** edges | Metadata only — a capability surface, not an observed access |
| `claude-console` | Claude org IAM → SSO/SCIM posture findings (identity roster + source) | |
| `claude-wif` | Anthropic non-human-identity / workload-identity roster + permitted-scope edges | Models operator-declared federation; flags static-key footguns |
| `claude-managed-agents` | Claude managed-agents inventory + thread events (webhook receiver + GET pollers) | Streaming source (`poll_seconds: 0`); offline it is a no-op |
| `claude-projects` | Claude Organization Projects inventory (membership / API keys) + operator-declared project policy | Admin-API read-only; a no-op offline |
| `claude-apps-gateway` | Claude apps-gateway posture, declared model grants and audit-event ingest → topology + findings | Reads an existing `gateway.yaml` and optional JSONL audit export |
| `claude-batch` | Anthropic Message Batches + Files API inventory, batch policy enforcement, upload retention expiry | Never reads payloads or file content; an honest offline finding without an admin key |
| `claude-routines` | Claude Code Routines (scheduled triggers) inventory → edges + cadence/review findings | GET-only; prompt content is only hashed; streaming (`poll_seconds: 0`) |
| `cowork` | Claude Cowork OTLP/HTTP logs receiver → activity evidence | Out-of-process plugin (OTel-proto dependency isolation) |
| `cowork-analytics` | Claude Cowork engagement analytics | In-process (modelprovider client only) |
| `codex` | OpenAI Codex cost samples, usage/auth/admin-audit evidence, adoption findings | Admin-API read-only; sales-gated surfaces degrade to a posture finding |
| `cursor` | Cursor Admin-API billed cost, team audit logs, member inventory, budget posture | Plan-gated 403/404 degrades to a finding, never fails |

### Vendor-neutral GenAI framework profile (`gen_ai.*`) — opt-in

The agent frameworks the catalog promises — **LangGraph / LangChain, CrewAI,
AutoGen / Microsoft Agent Framework, Google ADK** (and the OpenAI SDK, LlamaIndex,
Pydantic-AI, Strands, …) — do **not** emit Claude's `claude_code.*` schema. They
converge on the [OpenTelemetry **GenAI** semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai)
(`gen_ai.*`). The same `claude` source ingests that profile too, so an OTel-instrumented
fleet feeds the **access map** and **FinOps** through one ingest rather than a bespoke
connector per framework — the highest-leverage integration.

**This profile is OPT-IN and honestly labelled experimental.** The whole `gen_ai`
area is OpenTelemetry **Development** status (not Stable, jun-2026), so it activates
only when you mirror the spec's own gate. Set the connector's `semconv_opt_in` to a
comma-separated list containing the token `gen_ai_latest_experimental` (mirroring
`OTEL_SEMCONV_STABILITY_OPT_IN`). Off by default, a `gen_ai.*` signal still feeds the
silence watchdog but maps no edge/cost — we never claim a stability the conventions
do not have.

Because the conventions are mid-churn, the ingest is **dual-name** (it reads the
current key *and* the deprecated predecessor still emitted in the wild) and
**multi-signal** (it maps trace **spans**, the `gen_ai.client.inference.operation.details`
log **event**, and recognizes the client **metrics**):

| What it reads | Current key | Also accepted (deprecated, still emitted by) |
|---|---|---|
| Provider | `gen_ai.provider.name` | `gen_ai.system` (v1.36.0-or-prior default; **Google ADK**, e.g. `gcp.gemini`) |
| Input tokens | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens` (**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI) |
| Output tokens | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens` (same) |

| gen_ai attribute | maps to | confidence |
|---|---|---|
| `gen_ai.usage.*` (tokens) | `CostSample` (provenance **estimated** — tokens, not billed cost) | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | cost provider + model (response preferred) | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | agent→tool **access edge** (mode `unknown`) | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | conversation→agent **attribution edge** + session ref | `attributed` |

#### Supported dialect matrix (multi-generation normalizer)

The GenAI conventions changed in **three generations that coexist** in real 2026
fleets. The ingest detects the generation **per signal** from generation-exclusive
markers and stamps the normalized event with the corresponding semconv pin
(`genai.semconv` posture finding records the active set per run; one info `drift`
finding per run flags each **deprecated** dialect seen, so you know which fleets
need their instrumentation upgraded). Message **content is never read from any
generation** — content keys act only as dialect markers (minimal-data posture).

| Dialect detected | Pin stamped | Exclusive markers (verified) | Emitted by (verified jun-2026) |
|---|---|---|---|
| Legacy **OpenLLMetry/Traceloop** (pre-semconv) | `openllmetry` | indexed `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*`, `gen_ai.usage.prompt_tokens`/`completion_tokens`, `llm.usage.total_tokens`, `llm.request.type`, `llm.vendor`, `traceloop.span.kind` | Traceloop-instrumented LangChain / LangGraph / CrewAI pinned **< openllmetry v0.55.0** (released 2026-03-29). Capitalized providers (`OpenAI`, `Langchain`) are lowercased so FinOps does not split on case |
| **v1.36-or-prior events** (the spec's own name) | `1.36.0` | `gen_ai.system`; the five per-message log events `gen_ai.{system,user,assistant,tool}.message`, `gen_ai.choice` (recognized **by name** — their one attribute is optional) | Google ADK LLM spans (`gcp.vertex.agent`), AutoGen (`autogen`), Microsoft Agent Framework — all still emit `gen_ai.system` |
| **v1.37+ messages** (current) | `1.41.1` | `gen_ai.provider.name`, `gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`, the `gen_ai.client.inference.operation.details` event, `gen_ai.workflow.name` | OTel-official instrumentations; openllmetry **≥ v0.55.0** |

A signal carrying only keys whose names are identical across generations (e.g. an
ADK `invoke_agent` span: operation + agent + conversation, no provider key at all)
is normalized under the current pin — the mapping applied is byte-identical, and the
producer's true release is not knowable from the wire.

#### MCP conventions (`mcp.*`, semconv v1.39 — Development)

Exactly four `mcp.*` attributes exist upstream (`mcp.method.name`,
`mcp.protocol.version`, `mcp.resource.uri`, `mcp.session.id`); the tool rides
`gen_ai.tool.name` and the prompt `gen_ai.prompt.name`. The ingest joins these
traces with the product's own MCP governance facts by reusing the same resource
kinds the Claude path emits:

| MCP signal | maps to |
|---|---|
| any client-side `mcp.*` span with `server.address` | session→`mcp.server` edge (joins the `claude_code.mcp_server_connection` edges) |
| `tools/call` + `gen_ai.tool.name` | `mcp.tool` access edge (`server.address/tool` when the endpoint is known) — the same kind as Claude's `mcp__server__tool` invocations |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | **read-mode** `mcp.resource` edge (URI sanitized: credentials/query stripped) |
| `prompts/get` + `gen_ai.prompt.name` | **read-mode** `mcp.prompt` edge (prompt surface) |
| SERVER-kind spans / `mcp.client|server.*.duration` metrics | liveness only (clean degradation — the server's view attributes no agent identity) |

#### Agent spans (`invoke_agent` client/internal split + `invoke_workflow`, semconv v1.41 — Development)

v1.41.0 split `invoke_agent` into a **CLIENT** variant (remote agent service) and an
**INTERNAL** variant (in-process). Real frameworks violate the kind today (AutoGen and
Microsoft Agent Framework hard-code CLIENT for in-process agents; Google ADK uses
INTERNAL), so the ingest classifies an invocation as **remote** only when the span is
CLIENT **and** carries a `server.address` — that yields a conversation→`genai.agent.remote`
delegation edge. Everything else stays an in-process invocation covered by the
conversation→`genai.agent` attribution edge: degraded cleanly, never a fabricated
"remote". `invoke_workflow` (new in v1.41; CrewAI-style crews) maps a
conversation→`genai.workflow` edge. Agent spans remain upstream **Development**
(experimental) — no stability is claimed.

**Stable vs experimental, honestly:** the **mechanism** (opt-in gate, dialect
detection + dual-name reads, span/event/metric mapping, the sealed
`CostSample`/`EdgeObservation` shapes) is stable in this product. The **vocabulary**
it maps (`gen_ai.*`/`mcp.*` keys, the operation enum) is upstream **Development** and
may rename again; that is exactly why the ingest normalizes every generation rather
than pinning one. v1.41.1 is the last *versioned* release of the gen-ai conventions
(they moved to `open-telemetry/semantic-conventions-genai`, which has no releases as
of jun-2026). Notes:

- **Cost is de-duplicated by W3C span id.** When one operation reports usage on *both*
  its span and its `operation.details` event (they share a span id), it is costed once,
  not twice.
- **Metrics feed liveness, never cost.** `gen_ai.client.token.usage` is an aggregate;
  the span/event is the authoritative per-operation usage, so costing the metric too
  would double-count. The v1.39 `mcp.*` duration histograms are recognized the same way.
- **Provider may be `unknown`.** If a span carries a model but no provider/system, the
  cost is attributed to `unknown` rather than guessed from the model id.
- **A total-only token count is not split.** Legacy `llm.usage.total_tokens` without a
  prompt/completion split is never guessed into input/output (no fabricated cost).
- **OpenInference (Arize/Phoenix) is a different convention** and is *not* ingested by
  this profile — the `llm.*` keys read here (`llm.request.type`, `llm.usage.total_tokens`,
  `llm.vendor`) are **OpenLLMetry legacy markers**, not OpenInference's `llm.*` namespace.

## Cooperative — local agent-surface config

These sources read a local agent's declared configuration and emit **permitted** edges plus
posture findings. They are not live execution traces; when a framework has native OTEL, live
usage still arrives through the `gen_ai.*` ingest above.

| Kind | Observes | Honest coverage |
|---|---|---|
| `opencode` | Local `opencode.json` / `opencode.jsonc` JSONC layers → permission posture, managed/admin-override posture, MCP/tool/custom-agent permitted edges, credential-in-config/share/autoupdate/OTEL findings, and an authoring fragment | Config-declared only. The managed layer is detected locally, but it is not an immutable lock: runtime `OPENCODE_PERMISSION`, test-dir redirection, and remote organization config remain outside this reader. Native OTEL, when enabled, can feed live `gen_ai.*` usage via the out-of-band `OTEL_*` exporter |
| `gemini-cli` | Gemini CLI `settings.json` layers (system/user/workspace) → permitted MCP/tool edges, enforcement-gap posture, effective-config inventory | Config-declared only; live usage rides the `gen_ai.*` ingest (the CLI emits it natively). Not the Gemini API (that is the hosted-provider surface) |
| `openhands` | OpenHands `config.toml` + env → sandbox/model-pinning/credential/telemetry posture, permitted MCP/action edges | Config-declared only; live usage via native OTEL `gen_ai.*` |
| `goose` | Goose (Block) `profiles.yaml` + env → admin-settings/model-pinning/extension/tool-approval posture, permitted extension edges | Config-declared only |
| `cline` | Cline / Kilo Code VSCode `settings.json` namespaces → auto-approve/MCP-allowlist/credential/model-pinning posture | Config-declared only; no native OTEL upstream |
| `grok` | Grok Build (xAI) — el agente de codificación de terminal, leído por su configuración LOCAL: wire de hooks, eventos con veto documentado y postura de gobierno declarable | **NO es el conector de la API de xAI** (`xai` lee catálogo y coste, con `grok-build-0.1` entre sus MODELOS). Éste lee el AGENTE, y no se solapan. La mitad de OBSERVACIÓN va por el ingest OTLP que Grok Build ya emite. `PostureEnforced` sólo lo reclama `PreToolUse`, el único evento con veto documentado; el resto es `observed` |
| `openclaw` | OpenClaw `openclaw.json` (JSON5 discovery, confined `$include`) → gateway/channel/tool/sandbox/skill/model posture per agent, declared channel/skill/model edges | Config-declared only; no inline PEP hook verified upstream |
| `hermes` | Hermes Agent `config.yaml` + profile trees + managed scope → terminal/channel/skill/security/model/MCP posture, declared edges | Config-declared only; no inline PEP hook or native OTEL verified upstream |
| `google-adk` | Exported Google ADK 2.0 Session JSON → agent/app inventory, sub-agents, tool function-calls, transfers, approved-tool drift, Vertex reasoningEngine correlation | Read-only export; never message content. Distinct from the `google-agent` platform surface |
| `agents-md` | Repo walk of agent-instruction files (AGENTS.md and per-agent memory/instruction files) → SHA-256 baseline drift + instruction-injection / hidden-Unicode / secret scan | Minimal-data: sanitized paths + hashed details, never content |
| `mcpb` | Installed / distributed `.mcpb` desktop extensions → manifest posture scan, enterprise-allowlist drift, PKCS#7 signature verification | PERMITTED-vs-OBSERVED on the extension surface |
| `codex-managed-config` | OpenAI Codex managed-config files → enforcement posture + drift vs the authored baseline | Observation-only: it cannot stop a developer bypassing the managed layer (the `managed-settings` mirror for Codex) |

## Clean — native store audit (verbatim read/write)

These read a store's **own** audit trail and take the read/write classification verbatim
— never inferred from query text. `pgaudit` and `s3cloudtrail` are the canonical R/RW
sources the [access map](/reference/modules/iii-access-map/) is built around (their
hyphenated `pg-audit` / `s3-cloudtrail` aliases resolve too).

| Kind | Observes |
|---|---|
| `pgaudit` | PostgreSQL **pgAudit** trail (csvlog/jsonlog) → R/RW table access, `READ`/`WRITE` verbatim from pgAudit's CLASS |
| `s3cloudtrail` | AWS **CloudTrail** S3 events → object R/RW, read/write from CloudTrail's `readOnly` flag (also surfaces Claude-on-Bedrock model invocations) |
| `snowflake-audit` | Snowflake native access history |
| `databricks-uc` | Databricks Unity Catalog audit |
| `bigquery-audit` | BigQuery data-access audit |
| `redshift-audit` | Amazon Redshift audit |
| `mssql-audit` | SQL Server audit |
| `oracle-audit` | Oracle unified audit |
| `gcs-audit` | Google Cloud Storage data-access audit |
| `azure-blob-audit` | Azure Blob Storage audit |

## Cloud management plane — org/tenant inventory + control-plane activity

The tri-cloud parity for the **management** plane — distinct from the per-resource
**data** plane the store-audit connectors above cover. Each is a live, **read-only** API
client of a cloud's org/tenant control plane: it discovers the resource **topology**
(inventory edges, `mode=unknown`, attributed) and reads the cloud's native **audit feed**
for control-plane **activity** (`identity→…api` edges, read/write classified). They
complete the matrix AWS already anchors with `s3cloudtrail` (data plane) plus the
account-level IAM/CloudTrail `aws` connector. Both run **in-process** and are
**offline-safe** (no credential ⇒ Gather is a no-op); both observe the control plane only
— never a payload, secret, key or resource property.

| Kind | Observes | Honest coverage |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM** (org→folder→project→service-account topology) + **Cloud Audit Logs** (Admin Activity + Data Access) → `identity→gcp.api` | **Clean** where logged: Admin Activity is a write by the log type's definition, Data Access is read/write from the standard method verb. **Lossy** where Data Access logging is disabled (off by default in GCP) or a method verb is non-standard (`unknown`, never guessed). `approximate` for declared shared principals; the `principalEmail` converges with the SPIFFE/SA roster |
| `azure-activity` | Azure **Resource Graph** (tenant→subscription→resource topology) + **Azure Monitor Activity Log** (control-plane operations) → `identity→azure.api` | **Clean** for control-plane writes/deletes (verbatim from the RBAC action). The generic `action` suffix is **lossy** (`unknown` — it can read or write). Data-plane **reads are not in** the Activity Log (the `azure-blob-audit` / `azurekeyvault` data plane covers those). `approximate` for shared callers; the caller `objectId`/`appId` converges with the Entra roster |
| `cloudflare` | Cloudflare edge estate — **Workers, R2 buckets, Logpush jobs** via the REST API v4 → topology edges | Inventory only (no audit feed in this connector); scoped read-only token. Distinct from the `cloudflare-ai-gateway` / MCP-portals AI surfaces |

The GCP **Data Access** opt-in and the Azure **read-not-logged** gaps are the honest
**opaque** edges of this plane: an absent activity edge is not proof of no access where
those logs are off. The full per-cloud tier table is in the shipped
`docs/contracts/S165-connectors-cloud-management.md` contract.

## Hosted model providers — catalog, posture and metering

These sources govern hosted model-provider accounts and catalogs. They do **not** proxy
inference; where a provider lacks a usable usage API, spend is estimated by the connector's
Meter around the inference path instead of pulled from an aggregate billing feed.

| Kind | Observes | Honest coverage |
|---|---|---|
| `openai` | OpenAI platform usage and cost (org API) plus the model and API-key catalog | Read-only org/admin key; no data-plane payloads. Distinct from `azure-openai`, which speaks the real Azure surfaces rather than OpenAI-org paths |
| `gemini` | Gemini (Google) hosted model catalog and an operator-wired usage export | The hosted-provider surface. Distinct from `gemini-cli`, which observes local CLI settings, and from `vertex`, which covers the enterprise Vertex surfaces. Google exposes no aggregate usage API on this path, so usage is whatever the operator wires |
| `deepseek` | DeepSeek hosted catalog, account balance availability, and PRC sovereignty posture | No aggregate usage API; cost is metered around inference from declared pricing |
| `mistral` | Mistral catalog and governance posture | No public usage/billing/spending-cap API; cost is metered around inference from list pricing |
| `xai` | xAI/Grok live catalog, billing endpoints, key/ACL inventory, credit and spending-limit posture | Uses the read-only management billing endpoints for cost; management and inference credentials are distinct |
| `glm` | Zhipu GLM / Z.ai declared catalog, USD list-pricing Meter, entitlement probe, and sovereignty posture | Catalog-only + Meter: GLM exposes no verified usage, billing, balance, admin, key, or organization API. The PRC-nexus / Entity-List caveat applies to both `z.ai` and `bigmodel.cn` surfaces |
| `vertex` | Google Vertex AI catalog, per-model token usage (Cloud Monitoring), opt-in billed cost (billing export) and opt-in Model Armor safety posture | The enterprise Google surface the AI-Studio path does not cover; GCP has no real-time cost API |
| `azure-openai` | Azure OpenAI / AI Foundry deployments + models (ARM), Azure Monitor token usage and cost surfaces | Read-only management-plane client; no data-plane payloads |
| `openrouter` | OpenRouter live catalog (USD/MTok pricing), account usage/limit posture, approved-model policy drift | Billed cost via the exported `MeterCall`; a no-op offline |
| `cohere` | Cohere live model catalog (cursor-paginated Models API) | No public usage/billing/org API (dashboard-only) — an honest coverage caveat; cost metered around inference from list pricing |
| `fal` | fal.ai API-key lifecycle inventory + rotation posture; cost metered around the queue API | No public usage/audit API — governance is by key lifecycle; deep surfaces are sales-gated and marked UNVERIFIED |

## Self-hosted inference — local catalogs and usage

Self-hosted inference is always in scope, so it is a first-class source rather than a
gateway afterthought. This tier observes what a local runtime is actually serving.

| Kind | Observes | Honest coverage |
|---|---|---|
| `local` | Ollama model catalog (`/api/tags`), **Ollama residency (`/api/ps`)** — which models are loaded right now, with their GPU/CPU split and unload deadline — and vLLM token usage over its OpenAI-compatible surface | Residency is reported as posture, and its severity is the PLACEMENT: a model fully in VRAM is informational, while one resident on the CPU or SPLIT across CPU and GPU is flagged, because that is the case an operator pays latency for without being told. Ollama publishes no aggregate token metrics, so it contributes no metering. There is still no per-call identity or policy on local inference from this source; governing those needs the gateway or OTel path. Ollama on localhost needs no credential, so an empty config is a working read-only default; disabling a server is an EXPLICIT empty URL, and both empty is a no-op |

## Kernel backstop — eBPF / Tetragon (clean signal, approximate attribution)

The **non-cooperative** half of the moat: where the cooperative path sees what an agent
*reports*, this sees what the kernel *did* — file reads/writes and outbound connections —
even when an agent disables its own telemetry. The **access** is kernel ground-truth (a
clean-tier signal of *what happened*); the **attribution** is deliberately honest about
its limit — the kernel attributes to a runtime identity (process/cgroup/container), never
to a resolved agent, so every eBPF edge is `approximate`. It never decrypts or inspects
payloads (it is blind to the TLS body).

| Kind | Observes | Honest limit |
|---|---|---|
| `ebpf` | Tetragon kernel events → file R/RW (`MAY_*` mask) and network edges; optional anti-evasion finding when an agent acts at the kernel without cooperative telemetry | Agent-anonymous → always `approximate`; a streaming backstop, not a per-agent ledger |

It does **not** load eBPF programs itself: the kernel capture is done by
[Tetragon](https://tetragon.io/) (a separate, hardened DaemonSet). See
[Deployment requirements](#deployment-requirements-and-honest-attribution).

## Lossy — edges land, often approximate

| Kind | Observes | Why lossy |
|---|---|---|
| `mongo-audit` | MongoDB audit | Document-store; caller separation is weak |
| `openlineage` | OpenLineage run events → dataset lineage | Lineage is not per-call audit |
| `delta-sharing` | Delta Sharing recipient activity | Shared-recipient attribution |

## Approximate-by-attribution & permitted-side sources

These emit either the **permitted** side (declared grants) or accesses attributed to a
role / process / shared credential rather than a resolved agent.

| Kind | Observes | Tier |
|---|---|---|
| `iceberg-catalog` | Iceberg REST catalog → permitted grants + vended-credential identities | permitted |
| `inference-gateway` | K8s Gateway API Inference-Extension routing → permitted inference routes | permitted |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | Cloud KMS audit → key-access edges (never key material) | approximate |
| `external-secrets` / `sops` / `kmip` | Secret-management manifests / KMIP locate → provisioning/custody edges | approximate (existence, not use) |
| `istio-telemetry` | Istio Telemetry CRDs → L7 mesh edges | approximate (parsed CRDs, not live flows) |
| `egress-proxy` | Egress-proxy verdict log → L7 egress edges | approximate |
| `kong-audit` | Kong audit logs → config-change findings | approximate |
| `ai-gateway` | Envoy AI Gateway usage records → **cost** samples (FinOps) | cost stream |
| `github` | GitHub repositories as agent data sources → observed R/RW access edges (webhook-first, API-poll reconciliation) + permitted ACL edges | observed + permitted; streaming (`poll_seconds: 0`) |
| `gitlab` | GitLab repositories → observed R/RW access edges + permitted ACL edges | observed + permitted; streaming (`poll_seconds: 0`) |

## Posture observers — findings, not access edges

Read-first observers that surface posture (sync/health/drift, auth anomalies) as
findings; they never mutate the estate.

| Kind | Observes |
|---|---|
| `runtime` | Where AI workloads run (Linux procfs, Docker daemon, Kubernetes API) → containment edges + health findings (needs host access — see [Deployment requirements](#deployment-requirements-and-honest-attribution)) |
| `argocd` / `flux` / `crossplane` | GitOps / control-plane CRDs → sync, health, drift, composition posture |
| `kerberos` | KDC auth telemetry → Kerberoasting findings |
| `aaa` | RADIUS / TACACS+ AAA observations |
| `ssf` | Shared-Signals / CAEP receiver (agent kill-switch) |
| `edugain` / `openidfed` | Federation aggregate / OpenID-Federation trust chains → federation posture |
| `managed-settings` | Claude `managed-settings` policy → permitted edges + drift findings |
| `envoy-ai-gateway` | Envoy AI Gateway **declared config** export → gateway posture + gateway-vs-Olivares policy drift (the config sibling of the `ai-gateway` usage stream) |
| `kong-agent-gateway` | Kong agent-gateway declared config export → posture + policy drift |
| `litellm` | LiteLLM proxy declared config export → posture + policy drift |
| `bedrock-kb` | Amazon Bedrock Knowledge Bases retrieval health/config (Agent Runtime Retrieve health-check) → per-KB posture findings + KB→data-source edges. Never `RetrieveAndGenerate` (no billable inference), never full document content |
| `tak` | TAK Server `CoreConfig.xml` posture (+ optional mTLS probe) and governed, minimal-data Cursor-on-Target ingest (positions digested, uid hashed) |
| `a2a` | Agent2Agent (A2A) v1.0 peers → Agent Card discovery + JWS/JCS signature verification (peer trust level) and observed task/message interactions as agent↔agent edges. Observe-only — never dispatches a task; emitting signed cards is a separate capability |

## Untrusted hint — MCP introspection

The `mcp` source introspects MCP servers (stdio + Streamable HTTP) and emits **capability
edges** carrying the server's *declared* R/RW hints, plus protocol-revision, feature-surface
and registry-provenance findings. Per the MCP specification a tool annotation is an
**untrusted** declaration — a capability *claim*, corroborated against an observed source,
**never trusted alone**. (The cooperative `claude` source also introspects MCP as part of
its OTLP path; `mcp` is the standalone introspector you point at a server list or a
`.mcp.json`.)

| Kind | Observes | Tier |
|---|---|---|
| `mcp` | MCP server tools/resources/prompts → declared-capability edges + posture findings | untrusted hint |

## Out-of-process broker & mesh observers

These carry heavy wire-protocol dependency trees, so each runs **out-of-process** (the
dependency never links into the core). One connector reaches many targets.

| Kind | Observes |
|---|---|
| `kafka` | Kafka / Event Hubs / Redpanda / MSK topic activity |
| `amqp` | AMQP brokers (RabbitMQ, Azure Service Bus) |
| `nats` / `mqtt` / `cloudqueue` | NATS, MQTT, cloud queue activity |
| `debezium` | Debezium change-data-capture streams |
| `envoy` | Envoy ALS / ext_authz / ext_proc observation services |
| `hubble` | Cilium Hubble flow data |

## Identity roster providers

These populate the non-human-identity **roster** that sharpens attribution (turning
`approximate` edges into `attributed` ones). Each source with a grant surface also
emits its **permitted-access** (`SignalPolicy`) edges from `Gather` — the PERMITTED
side of the permitted-vs-observed diff:

| Kind | Roster | Permitted edges |
|---|---|---|
| `vault` | entities, groups, policies | ACL policy path grants (`vault.path`), expanded per bound entity |
| `ldap` | users, service/computer accounts, groups | privileged-group membership → directory grants (`ldap.directory`) |
| `idp` (Okta / Entra) | users, apps/service principals, groups | app-assignment / scope grants (`okta.app` / `entra.app`) |
| `infisical` | machine identities, org members, projects | project grants (`infisical.project`) |
| `keycloak` | realms, clients, roles, groups, users | roster only (no-op `Gather`) |
| `pingone` / `forgerock` | PingOne / ForgeRock directory rosters via the same multi-provider reader (the kind seeds the matching `provider`; `ping` aliases `pingone`) | roster only (no-op `Gather`) |
| `spiffe` | SPIRE registration entries | roster only (no-op `Gather`) |

Wire `as_source: true` on the `identity` entry for a one-shot permitted-grant pass
per boot, or a separate `sources` entry with `poll_seconds` for periodic re-scans —
never both for one kind (`okta`/`entra` share the one `idp` connector, so only one
idp-family instance can register as a source per process). Group/role memberships
travel only the typed roster snapshot, never as edges.

### Agent identity federation

The hyperscaler **agent registries** federate read-only against the plane's SPIFFE/WIF
roster. Their per-agent rows (`agent_identity` / `workload_identity` kinds) are
dedicated, non-shared identities, so the access map treats them as **firm** per-agent
attribution; ancillary rows from the same sources (blueprint principals, credential
providers, service-account-backed agents) stay approximate. Federation never writes to
a registry; *export* to the control towers is a separate, later capability.

| Kind | Federates | Gather |
|---|---|---|
| `entra-agent` | Microsoft Entra Agent ID (agent identities, agent users, blueprints, blueprint principals, owners/sponsors, in-snapshot orphan computation, opt-in soft-deleted) via Graph v1.0 | `nhi_longlived_credential` drift findings, CA/risky-agent/governance/sponsorless posture findings, and opt-in beta `auditLogs/signIns` observed agent access edges — add a `sources` entry with `poll_seconds` |
| `agentcore` | AWS Bedrock AgentCore Identity (workload identities, token-vault credential providers) + AgentCore Policy engines/Cedar policies as collections | `nhi_longlived_credential` drift findings (static API-key providers) — add a `sources` entry with `poll_seconds` |
| `google-agent` | Google Agent Identity (Agent Runtime reasoning engines; SPIFFE-based agent identities) plus Agent Registry / Agent Gateway posture. Rows use the **full SPIFFE ID** as ref, converging with the `spiffe` roster; Gather detects unattributed registry agents, shadow reasoning engines outside a readable registry, risky MCP tool annotations, and gateway registry posture | registry/gateway posture findings and shadow-agent detection — add a `sources` entry with `poll_seconds` |
| `agent365` | Microsoft Agent 365 registry (package-level inventory incl. agents *without* an Entra identity) via Graph v1.0, app-permission client credentials or delegated token, opt-in package details | registry-hygiene findings (blocked deployed packages; external/shared packages deployed to all users) — add a `sources` entry with `poll_seconds` |
| `foundry-agents` | Microsoft Foundry projects, agent applications/deployments and current Agent Service agents via ARM + Foundry Agent Service v1; correlates app identity links to `entra-agent` | ARM-derived application posture findings (missing Entra agent identity; failed deployment on enabled app) — add a `sources` entry with `poll_seconds` |
| `ai-control-tower` | ServiceNow AI Control Tower digital-asset inventory (Table API, read-only) | no-op (roster only) |
| `oasf` | AGNTCY/OASF agent descriptors + Agent Badge verification — **EXPERIMENTAL** until the identity spec is VCDM 2.0 conformant | badge findings — add a `sources` entry with `poll_seconds` |
| `onepassword` | 1Password account as a `secret_store` custodian | item-usage secret-access edges — add a `sources` entry with `poll_seconds` |

For the seven kinds with a re-pollable Gather (`entra-agent`, `agent365`, `agentcore`,
`foundry-agents`, `google-agent`, `oasf`, `onepassword`), wire the **roster** half as an `identity` entry *without*
`as_source` and the **edges/findings** half as a separate `sources` entry with
`poll_seconds` — not both via `as_source: true`, which runs the scan only once per
boot (and a duplicate registration of the same kind is rejected).

Registry-declared **owner/sponsor** land on the NHI lifecycle records during roster
sync (the same semantics as `PUT /nhi/{ref}/ownership`), and a registry-asserted
**orphan** (an Entra agent whose blueprint is gone) lands on the same record's
`registry_orphaned` flag — the lifecycle sweep ORs it into `orphaned` and emits the
`nhi_orphaned` finding, so orphan detection watches federated agents with zero extra
wiring. The `vault-audit` *source* (under `sources`, not `identity`) tails the Vault
file audit device and emits the OBSERVED counterpart of `vault`'s permitted grants
for the same `entity:<name>` refs.

## Knowledge document sources (not access-map coverage)

These feed the **knowledge** module (module VIII), **not** the access map: they ingest
*document content* for governed retrieval, emit **no** R/RW edge and produce **no**
observation on the bus. The module *pulls* them (List → Fetch) on an ingest request
(`POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"}`), so they are wired into that
module — name them under `documents` in `OLIVARES_SOURCES_CONFIG`, not `sources`. Each is
read-only and minimal-data: it carries the source's ACL and provenance (never a personal
email; the module redacts the body before persisting).

| Kind | Ingests |
|---|---|
| `gdrive` | Google Drive documents (Docs/Sheets/Slides/files) |
| `confluence` | Atlassian Confluence spaces & pages |
| `notion` | Notion workspaces, databases & pages |
| `sharepoint` | Microsoft SharePoint / OneDrive sites & documents |
| `s3content` | Object-storage content (S3 / R2 / GCS objects) |
| `sap_odata` | SAP OData service entities as governed documents |
| `salesforce` | Salesforce objects/records as governed documents |
| `snowflake` | Snowflake tables/rows as governed documents (distinct from the `snowflake-audit` R/RW observer) |
| `azure_ai_search` | Azure AI Search index documents |
| `postgres` | PostgreSQL rows as governed documents — read-only by construction, declared per-row ACL, per-column classification (distinct from the `pgaudit` R/RW observer; not NL-to-SQL). See [Postgres as a governed context source](/how-to/govern-postgres-content/). |
| `filesystem` | File-server content (local / NFS / SMB) — read confined to the root by construction, POSIX owner/group/ACL mapped to Document ACLs, xattr classification (distinct from the `filelog` log sink). See [Govern your file server](/how-to/govern-your-file-server/). |

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents", never "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## Output destinations (not coverage)

Output connectors **deliver** findings and notifications; they observe nothing and have
no coverage tier. They are wired separately from sources.

In-process destination kinds: `slack`, `teams`, `pagerduty`, `opsgenie`, `webhook`,
`siem`, `splunkhec`, `syslog`, `servicenow`, `jira`, `email`, `twilio`, `chronicle`,
`datadog`, `elastic`, `snmp`, `filelog`, `otlplog` (OTLP/HTTP logs) and `s3archive`
(the S3 Object Lock WORM sink — one immutable, lock-verified object per notification).

Three broker egress kinds run **out-of-process** as embedded plugins (their wire-protocol
dependency trees never link into the engine, exactly like the plugin sources): `kafka`,
`amqp` and `cloudqueue` — the same kind names as their source twins; as a destination
each delivers the notification as a CloudEvent to the configured broker/queue. A plain
dev build without `task build:connectors` skips such a destination with an honest boot
warning instead of pretending it exists.

:::note[The outbound webhook is a destination, not an API webhook]
`webhook` is an output channel the control plane pushes to, not a callback you register
against the product's REST API — the OpenAPI document defines no `webhooks`. See
[Honesty & limits](/start/honesty-and-limits/).
:::

## Deployment requirements and honest attribution

The R/RW differential connectors are wired into the default binary, but two carry a
**deployment requirement** the rest do not — the connector code is host-agnostic, the
*data* it consumes is not:

- **`ebpf`** consumes [Tetragon](https://tetragon.io/)'s kernel-event export. **The
  connector needs no kernel capability** — it reads a `0600` file/FIFO/`stdin` that
  Tetragon owns (`events_path`, default `-`). Tetragon itself is a **separate, hardened
  DaemonSet** holding the minimal `CAP_BPF` + `CAP_PERFMON`, running non-root with
  seccomp/AppArmor and no inbound listener. So the deployment is: run Tetragon privileged
  (its bundled file-access + TCP-connect TracingPolicies), then point `ebpf` at its export.
  Minimum Tetragon: v1.0.
- **`runtime`** reads the host's procfs (`proc_root`, default `/proc`), the Docker daemon
  socket (`docker_socket`, **off by default** — read access to `docker.sock` is
  root-equivalent; opt in deliberately, ideally via a GET-allowlisted socket proxy) and/or
  the Kubernetes API (in-cluster ServiceAccount by default). Mount only what you enable.
- **`gcp-audit`** authenticates as a GCP service account (key JSON or a WIF/ADC-issued
  `access_token`) and needs only **read-only management** roles:
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` +
  `roles/logging.viewer` — reading **Data Access** entries additionally needs
  `roles/logging.privateLogViewer`. Scope `organization_id` (org walk + org-scoped audit)
  and/or `projects`. Data Access audit logs are **off by default in GCP**: enable them per
  the IAM/data-access config, or the activity feed honestly under-reports.
- **`azure-activity`** authenticates as an Entra service principal (client-credentials) or
  a managed-identity `access_token`, and needs only the **Reader** role at the tenant root
  (or per subscription) — that single role covers Resource Graph, subscription listing and
  the Activity Log. Subscriptions are auto-listed when `subscriptions` is unset.

Both still run **in-process** (transport A); the
`cmd/{pg-audit,s3-cloudtrail,ebpf-source}` go-plugin binaries exist for an out-of-process
**collector** deployment near the host if you prefer to isolate them there.

Every source is **opt-in, deny-closed**: a missing `log_path`/`path`/`events_path` is a
configuration error at startup (the source is not wired), never a silent no-op. The demo
estate ([quickstart](/start/quickstart/)) seeds equivalent synthetic observations through
the real bus so you can see the clean-tier signal end to end before wiring a live source.

:::caution[Honest limits across every tier]
- **An absent edge is not proof of no access** where coverage is lossy, impossible, or a
  source is not wired. The access map is honest about its own reach.
- **Per-agent identity is the hard dependency.** A shared service account behind a
  connection pool collapses attribution to `approximate` on even a clean-tier store —
  see [govern and approve](/how-to/govern-and-approve/).
- **MCP tool annotations are untrusted** by the MCP specification: a declared capability
  hint, corroborated against an observed source, never trusted alone.
:::

## Related

- [Connect a source](/how-to/connect-a-source/) — the connector model and how to wire one.
- [Connect Claude Code](/how-to/connect-claude-code/) — the cooperative path end to end.
- [Module III — the access map](/reference/modules/iii-access-map/) — what the edges become.
- [Honesty & limits](/start/honesty-and-limits/) — the product-wide honest contract.

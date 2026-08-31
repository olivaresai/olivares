---
title: Modules catalog
description: >-
  The 30 modules of Olivares AI — organized by the nine capability areas, with
  each module's honest maturity. Olivares AI integrates, manages and secures AI
  in the enterprise, one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside; this is the per-module
  reference.
---

Olivares AI integrates, manages and secures AI in the enterprise, one ground truth:
Claude Code at the deepest level, Codex and Grok Build alongside. It is a **modular platform** — one engine, one
console, and **30 modules** wired into a single binary — that observes where
agents run, governs what they are allowed to do, and (on a growing subset) acts
on your real infrastructure. Every module (a) consumes normalized events/data
from the core, (b) declares its entities in the shared data model, and (c)
exposes its own API endpoints and UI views — without touching the core or other
modules.

The 30 modules are organized by the **nine capability areas** below. Read each
module's status as **two halves**: *Govern/Observe* (catalog, observe, gate,
report) is built and wired today; *Actuate* (acting on real infrastructure —
deploy, dispatch, send, enforce, run) falls into honest states — **live** in the
default binary for a subset, **on-demand** for several (the backend is built and
wired to an injection point but stays deny-closed or degraded until an operator
provisions it via env config), **PARTIAL** where the surface is gated/opt-in,
and a declared **deny-closed seam** for the rest. In particular **deploy** plans
and governs deployments but does **not** apply them to live infrastructure until
an executor is provisioned: `apply`/`retire` return a clear `503`. Depth varies
by module, and much of the product is pre-1.0 / design-stage where noted (see
[Honesty & limits](/start/honesty-and-limits/)).

The **access map** (`iii-access-map`) — the read/read-write graph of what each
agent can and does touch, with least-privilege drift = `Permitted ≠ Observed` —
is **one of the most useful capabilities among the 30**, not the whole product.
The breadth is the point: nine areas, one engine, one console.

## The 30 modules, by capability area

Each row links to its module page (`/reference/modules/<slug>/`). The **Actuate**
column is the honest state of the acting half; `—` means the module
governs/observes by nature and has no actuation surface.

### Observe

| Module | Actuate | Purpose |
|---|---|---|
| [Inventory & discovery](/reference/modules/i-inventory/) | — | Discover and catalog every agent/session/MCP server/tool/model/identity in the estate. |
| [Live operation & sessions](/reference/modules/ii-sessions/) | — | Real-time state of each agent and session; also hosts the governed Claude Code session runtime. |
| [Access & resource map (R/RW)](/reference/modules/iii-access-map/) | — | What each agent accesses, and whether it reads or writes; least-privilege drift = `Permitted ≠ Observed`. |
| [Orchestration & A2A](/reference/modules/iv-orchestration/) | on-demand | Observe-and-govern the live delegation/communication graph; dispatch is wired on-demand, deny-closed until provisioned. |
| [MCP, skills & capabilities](/reference/modules/v-capabilities/) | — | Govern agents' tools and capabilities, visually. |
| [Health, SLA & uptime](/reference/modules/xxii-health/) | — | Reliability of the estate's agents and MCP servers; checks, incidents, dependency map. |
| [Observability read-model](/reference/modules/observability/) | — | The engine's read-model of itself: pinned interop standards, W3C-correlated ledger/trace view, supply-chain attestation. |
| [Claude Code adoption](/reference/modules/claudeadoption/) | — | Read-model of Claude Code adoption/productivity: sessions, lines of code, commits, PRs, tool accept-reject, per-model tokens, by team/developer/day; per-team default, per-developer drill-down opt-in. Claude-API-only boundary; never carries cost. |
| [Live-ingest](/reference/modules/live-ingest/) | PARTIAL | In-process producer of detective events a connector can't emit; env-gated, deny-closed, minimal-data. |

### Govern & enforce

| Module | Actuate | Purpose |
|---|---|---|
| [Identity, permissions & governance](/reference/modules/vi-governance/) | — | Who and what can do what, granular: Cedar RBAC + deny-overlay + scoped grants, roster reconciliation, scoped admin/custom roles, break-glass, kill-switch. |
| [Source & credential scoping](/reference/modules/sourcescope/) | — | Bind sources to a workspace/agent-group; deny-closed scoped resolver + scoped credentials at resolution time. |
| [Deployment & integration](/reference/modules/vii-deploy/) | on-demand (503) | Plan and govern deployments to real infrastructure; the executor is on-demand — live `apply`/`retire` return `503` until provisioned. |

> **Identity & access** lives inside [governance](/reference/modules/vi-governance/) —
> there is no separate module. NHI lifecycle, agent-identity federation, AAL3
> step-up, and SSO/SCIM are governance capabilities.

### Claude & agent ecosystem

| Module | Actuate | Purpose |
|---|---|---|
| [Model & provider management](/reference/modules/x-models/) | on-demand (503) | Govern across the whole model/provider stack: model-access, per-surface context-window, model-group gate; model *execution* is on-demand — `503` until an inference credential is provisioned. |
| [Inline inference proxy](/reference/modules/inferenceproxy/) | PARTIAL | Per-tenant inference-egress config + DLP for the inline `/v1/messages` PEP proxy; module config is live, the listener is opt-in, loopback-default, fail-CLOSED. |
| [Internal catalog & marketplace](/reference/modules/xiv-catalog/) | — | Curated marketplace of approved/signed agents, MCP servers and skills. |
| [Voice & realtime agents](/reference/modules/xvi-voice/) | on-demand | Observe-and-govern conversational/realtime agents (default-DENY, two-phase HITL); never opens a media stream; dispatch on-demand. |

### Security & data protection

| Module | Actuate | Purpose |
|---|---|---|
| [Security, guardrails & audit](/reference/modules/ix-security/) | live | Guardrails (PII/injection/jailbreak), anomalies, incident timelines; BYOK/DLP/RTBF/retention/WORM/residency live in this plane. |
| [Privileged-session recording](/reference/modules/recording/) | live | PAM-aligned recording of privileged sessions: hash-chained frames, redact-on-write, ledger-anchored. |
| [Data, knowledge & context](/reference/modules/viii-knowledge/) | on-demand | Governed data plane: KBs + RAG, governed retrieval, lineage, prompt registry, agent memory; model-backed semantic embeddings are on-demand. |

### Compliance & evidence

| Module | Actuate | Purpose |
|---|---|---|
| [Compliance & regulatory](/reference/modules/xiii-compliance/) | — | 26 framework catalogs + sealed, ledger-derived evidence with live chain-verify. |
| [SIEM/ITSM forwarder](/reference/modules/siemforward/) | live | Ships the sealed ledger + findings to SIEM towers (OCSF 1.8/CEF/LEEF/syslog/OTLP), leader-gated cursor walk, at-least-once. |
| [Posture export](/reference/modules/posture-export/) | PARTIAL | Read-only posture/inventory pull for control towers (neutral JSON); does **not** claim a verified downstream push. |
| [Reporting](/reference/modules/reporting/) | — | Professional PDF/HTML report generation from the platform's compliance, audit and FinOps data — five built-in report types; an auditor downloads a document instead of copy-pasting JSON. |

### FinOps

| Module | Actuate | Purpose |
|---|---|---|
| [Cost & AI FinOps](/reference/modules/xi-finops/) | live | Acting budgets that deny/throttle at the cap, cost-per-outcome, cancellation-risk; budget firm to identity. |

### Evals & safety

| Module | Actuate | Purpose |
|---|---|---|
| [Quality, evals & testing](/reference/modules/xii-evals/) | — | Calibrated LLM-judge + a blocking CI regression gate; offline judge → SKIPPED, never a silent pass. |
| [Agent sandbox](/reference/modules/xvii-sandbox/) | on-demand | Safe environment to test agents before production; real OS isolation (gVisor/Firecracker) is on-demand. |
| [Red-teaming & adversarial testing](/reference/modules/xviii-redteam/) | on-demand | Consent-gated adversarial battery; DEGRADED — never a false pass — until a sandbox runtime is provisioned. |

### Platform & integrations

| Module | Actuate | Purpose |
|---|---|---|
| [Output integrations & notifications](/reference/modules/xv-notify/) | live | Notification router to the systems the company already runs; dispatch is wired live, destinations operator-provisioned. |
| [Eventing](/reference/modules/eventing/) | live | External subscription surface over the bus: typed subscriptions, durable at-least-once delivery, retry/backoff, DLQ, cursor replay. |
| [Saved console views](/reference/modules/consoleviews/) | — | Named, shareable snapshots of console view state (filters, ranges), stored server-side per tenant: save an investigation, share it with the team. Accepts a size-capped (4096-byte) JSON object intended for view parameters — do not store sensitive data or query results in it. Create/update are owner-only; tenant admins/owners and superadmins can delete for cleanup; every mutation is audited. |

**Actuate** column: `live` = actuation is wired and live in the default binary,
no provisioning required (e.g. FinOps budget enforcement denies at the cap, the
notification router dispatches); `on-demand` / `on-demand (503)` = the backend is
built and wired to an injection point but stays **deny-closed or degraded until
an operator provisions it** via env config (deploy answers `503` until an
executor exists; orchestration/voice dispatch is deny-closed until configured;
red-team runs DEGRADED until a sandbox runtime is provisioned; model execution
and semantic embeddings return `503` until a credential is provisioned);
`PARTIAL` = the surface is real but gated/opt-in or does not claim a verified
downstream (the inference-proxy listener is opt-in and loopback-default;
live-ingest is env-gated; posture-export is a neutral read-only projection); `—`
= the module governs/observes by nature and has no actuation surface. This split
is the honest contract: the product **observes and governs broadly today, and
actuates on a growing, mostly provision-gated subset** — see
[Honesty & limits](/start/honesty-and-limits/). The catalog is derived from the
composition root (`cmd/olivares/wire.go`): all 30 modules are constructed there
and registered via `rt.AddModule` (verified 2026-07-24).

## Platform & core capabilities (not counted among the 30 modules)

These are real, shipped capabilities, but they are **engine/core/web capabilities**,
not modules in the `modules/` set — so they are not counted in the 30:

- [Own API + manage-as-code](/reference/modules/xix-api-manage-as-code/) —
  **Engine/core capability.** The engine's own versioned REST/gRPC API plus the
  Terraform provider; manage the platform itself by API and IaC.
- [Multi-tenancy & org management](/reference/modules/xx-multi-tenancy/) —
  **Engine/core capability.** Org hierarchy and delegated admin, with Postgres
  row-level-security tenant isolation.
- [Executive dashboards](/reference/modules/xxi-executive-dashboards/) — **Web capability.**
  Leadership console views alongside the technical UI. (Its report-generation
  backend is the [reporting](/reference/modules/reporting/) module, which IS
  counted among the 30.)
- [Model operations (own models)](/reference/modules/xxiii-model-operations/) —
  **Capability of the models module** (counted through module X's row, not a
  separate row): the governed registry of owned models, signed-model admission,
  dataset/fine-tune-job lineage records, local-inference deployment governance
  and AIBOM/model-card evidence.

**Planned:** own-model fine-tuning and local-inference **execution**
([xxiii-fine-tuning](/reference/modules/xxiii-fine-tuning/)) — the platform
governs and records that work today (see model operations above) but does not
run training or serve inference itself; the executing half is documented
**planned** work, **not shipped** and not one of the 30.

## How modules show up in the API and the bus

- **REST.** The [API reference](/reference/api/) renders the stable core REST
  surface from the product's OpenAPI 3.1 contract. The module routes
  (`/v1/m/<ns>/…`) are published separately as a **beta** document — the
  [module-route reference](/reference/api-beta/); their field-level contracts live
  in the product's typed interfaces.
- **Events.** Modules react to the [event bus](/reference/events/): the access
  map consumes `edge.observed`, FinOps consumes `cost.sampled`, and security
  consumes `finding.reported` and `guardrail.observed`.

## Layers

The 30 modules build on layers over the engine, alongside the engine/core and
web capabilities above:

- **Engine (layer 0)** — the own-API/manage-as-code and multi-tenancy
  capabilities (core, not counted in the 30).
- **Core (layer 1)** — inventory, sessions, access-map, models, health,
  observability.
- **Management (layer 2)** — capabilities, governance, sourcescope, deploy,
  knowledge.
- **Intelligence (layer 3)** — orchestration, security, recording, inference
  proxy, finops, evals, compliance, reporting, siemforward, posture-export, catalog, notify,
  eventing, voice, sandbox, redteam, live-ingest, consoleviews.
- **Web (layer 4)** — the UI and the executive-dashboards capability.

See the [architecture overview](/explanation/architecture/overview/) for how the
engine and these layers compose.

---
title: Modules catalog
description: The numbered module map (I–XXIII) of the Olivares AI control plane — name, layer,
  status and purpose. The product is a modular platform; this is the per-module
  reference.
slug: 2026-06/reference/modules/overview
---

Olivares AI is a **modular platform**: one engine plus modules plus connectors,
designed so any module attaches without re-architecting the rest. Every module
(a) consumes normalized events/data from the core, (b) declares its entities in the
shared data model, and (c) exposes its own API endpoints and UI views — without
touching the core or other modules.

Its numbered map has **23 modules**. One — module XXIII (model fine-tuning) — is
**post-v1**; the other 22 are v1-target. Read each module's status as **two halves**:
*Govern/Observe*
(catalog, observe, gate, report) is built and wired today for all 22; *Actuate*
(acting on your real infrastructure — deploy, fire, dispatch, send, enforce, run) falls
into three honest states — **live** in the default binary for a subset, **wired
on-demand** for several (the backend is built and wired to an injection point but stays
deny-closed or degraded until an operator provisions it via env config), and a declared,
**deny-closed seam** for the rest. In particular module **VII (deploy)** plans and
governs deployments but does **not** apply them to live infrastructure until an executor
is provisioned: `apply`/`retire` return a clear `503`. Depth also varies by module, and
much of the product is pre-1.0 design-stage (see
[Honesty & limits](/2026-06/start/honesty-and-limits/)).

## The 23 numbered modules

| # | Module | Layer | Govern/Observe | Actuate | Purpose |
|---|---|---|---|---|---|
| I | [Inventory & discovery](/2026-06/reference/modules/i-inventory/) | Core | v1 | — | Discover and catalog everything in the estate. |
| II | [Live operation & sessions](/2026-06/reference/modules/ii-sessions/) | Core | v1 | — | Real-time state of each agent and session. |
| **III** | **[Access & resource map (R/RW)](/2026-06/reference/modules/iii-access-map/)** ⭐ | Core | v1 | — | What each agent accesses, and whether it reads or writes — the **differentiator**. |
| IV | [Inter-agent communication & orchestration](/2026-06/reference/modules/iv-orchestration/) | Intelligence | v1 | on-demand | Coordinate and schedule how agents interact; live dispatch (*fire*) is wired on-demand — deny-closed until a dispatcher is provisioned. |
| V | [MCP, skills & capability management](/2026-06/reference/modules/v-capabilities/) | Management | v1 | — | Govern agents' tools and capabilities, visually. |
| VI | [Identity, permissions & governance](/2026-06/reference/modules/vi-governance/) | Management | v1 | — | Who and what can do what, granular. |
| VII | [Deployment & integration](/2026-06/reference/modules/vii-deploy/) | Management | v1 | on-demand (503) | Plan and govern deployments/wirings to real infrastructure; the executor is built and wired on-demand — live `apply`/`retire` return `503` until it is provisioned. |
| VIII | [Data, knowledge & context](/2026-06/reference/modules/viii-knowledge/) | Management | v1 | v1 | What agents know and use, governed (governed retrieval is live; model-backed **semantic** embeddings are wired on-demand — lexical + public-only by default). |
| IX | [Security, guardrails & audit](/2026-06/reference/modules/ix-security/) | Intelligence | v1 | v1 | Keep everything secure, auditable and defensible (cross-cutting); emits findings/guardrails live. |
| X | [Model & provider management](/2026-06/reference/modules/x-models/) | Core | v1 | routing only | Govern and route across the whole AI stack, not just one vendor; model *execution* is wired on-demand — `503` until an inference credential is provisioned. |
| XI | [Cost & AI FinOps](/2026-06/reference/modules/xi-finops/) | Intelligence | v1 | v1 | Control AI spend — budget enforcement (throttle/block) denies live. |
| XII | [Quality, evals & testing](/2026-06/reference/modules/xii-evals/) | Intelligence | v1 | — | "Is my agent still doing the right thing?" |
| XIII | [Compliance & regulatory](/2026-06/reference/modules/xiii-compliance/) | Intelligence | v1 | — | Open enterprise doors. |
| XIV | [Internal catalog & marketplace](/2026-06/reference/modules/xiv-catalog/) | Intelligence | v1 | — | Curate and reuse approved agents/capabilities. |
| XV | [Output integrations & notifications](/2026-06/reference/modules/xv-notify/) | Intelligence | v1 | v1 | The control plane talks to what the company already runs (notification dispatch is wired live; destinations are provisioned by the operator). |
| XVI | [Voice & realtime agents](/2026-06/reference/modules/xvi-voice/) | Intelligence | v1 | on-demand | Manage conversational/realtime agents; governed realtime dispatch is wired on-demand — deny-closed until a voice provider is provisioned (the same posture as IV orchestration). |
| XVII | [Agent simulation/testing sandbox](/2026-06/reference/modules/xvii-sandbox/) | Intelligence | v1 | v1 | A safe environment to test agents before production (in-process synthetic runner is live; the OS-isolated runtime is wired on-demand). |
| XVIII | [Red-teaming & adversarial testing](/2026-06/reference/modules/xviii-redteam/) | Intelligence | v1 | on-demand | Test agent security via controlled, defensive adversarial testing; isolated runs are wired on-demand — DEGRADED (never a false pass) until a sandbox runtime is provisioned. |
| XIX | [Own API + manage-as-code](/2026-06/reference/modules/xix-api-manage-as-code/) | Engine | v1 | — | Manage the control plane itself by API/IaC (foundational). |
| XX | [Multi-tenancy & org management](/2026-06/reference/modules/xx-multi-tenancy/) | Engine | v1 | — | Org hierarchy and delegated admin for MSPs/large orgs (foundational). |
| XXI | [Executive dashboards & reporting](/2026-06/reference/modules/xxi-executive-dashboards/) | Web | v1 | — | High-level views for leadership, alongside the technical UI. |
| XXII | [Health, SLA & uptime](/2026-06/reference/modules/xxii-health/) | Core | v1 | — | Reliability of agents and MCP servers. |
| XXIII | [Own-model management / fine-tuning](/2026-06/reference/modules/xxiii-fine-tuning/) | — | **post-v1** | — | Govern models trained/hosted by the company. |

**Actuate** column: `v1` = actuation is wired and **live in the default binary**, no
provisioning required (e.g. XI FinOps budget enforcement denies at the cap, XV
notification dispatch is wired); `on-demand` = the backend is **built and wired to an
injection point** but stays **deny-closed or degraded until an operator provisions it**
via env config (e.g. VII deploy answers `503` until an executor is provisioned; IV/XVI
dispatch is deny-closed until a dispatcher is configured; XVIII red-team runs DEGRADED
until a sandbox runtime is provisioned); `routing only` = X resolves a route but
executes the model call only on-demand (`503` until provisioned); `seam` = a
declared, **deny-closed** interface with no backend in the default binary at all; `—` =
the module governs/observes by nature and has no actuation surface. This split is the
honest contract: the product **observes and governs broadly today, and actuates on a
growing, mostly provision-gated subset** — see
[Honesty & limits](/2026-06/start/honesty-and-limits/). The matrix is derived from the
composition root (`cmd/olivares/wire.go`) and confirmed against a stock
`serve --seed-demo` boot (2026-06-08).

⭐ Module III is the differentiating pillar — see its
[dedicated reference](/2026-06/reference/modules/iii-access-map/).

Beyond the numbered capabilities, two **supporting** in-process modules exist for
architectural reasons: [**live-ingest**](/2026-06/reference/modules/live-ingest/) is the
deny-closed producer of the `guardrail.observed` (and dormant
`voice.telemetry.observed`) events that an out-of-process connector cannot publish,
feeding modules IX, II and XVI; and
[**observability**](/2026-06/reference/modules/observability/) is the engine's read-model of
itself — the pinned interop standards, the W3C-correlated ledger view of a trace, and
the running binary's supply-chain attestation. Neither is one of the numbered 28.

## How modules show up in the API and the bus

* **REST.** The [API reference](/reference/api/) renders the core REST surface from the
  product's OpenAPI 3.1 contract. Some module routes are reachable but **deliberately
  not** in that document; their field-level contracts live in the product's typed
  interfaces.
* **Events.** Modules react to the [event bus](/2026-06/reference/events/): module III consumes
  `edge.observed`, FinOps (XI) consumes `cost.sampled`, and security (IX) consumes
  `finding.reported` and `guardrail.observed`.

## Layers

Modules build on four layers over the engine:

* **Engine (Core layer 0)** — XIX, XX.
* **Core (layer 1)** — I, II, III, X, XXII.
* **Management (layer 2)** — V, VI, VII, VIII.
* **Intelligence (layer 3)** — IV, IX, XI, XII, XIII, XIV, XV, XVI, XVII, XVIII.
* **Web (layer 4)** — the UI and XXI.

See the [architecture overview](/2026-06/explanation/architecture/overview/) for how the
engine and these layers compose.

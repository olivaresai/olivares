---
title: Architecture overview
description: "How Olivares AI is built: one engine, modules, and connectors —
  the platform model, the eight core subsystems, the access map, and the
  deployment topologies."
slug: 2026-06/explanation/architecture/overview
---

This page explains how Olivares AI is structured and why. It is an *explanation*, not a how-to: it gives you the mental model you need to reason about the control plane before you install, configure, or extend it. For step-by-step instructions, follow the [how-to guides](/2026-06/how-to/self-hosting/); for exact contracts, see the [API reference](/reference/api/) and the [events reference](/2026-06/reference/events/).

:::note[Design stage]
Much of what follows describes a system that is **in beta** and design-stage in parts. The platform model, the data model, the cooperative ingest path, and the access-map differentiator are specified and being built incrementally; some module-level capabilities are planned rather than shipped. Where a capability is not yet built, this page says so. Treat this as the intended architecture, not a claim that every layer is production-complete today.
:::

## The platform model: one engine, modules, connectors

Olivares AI is not a single-purpose tool. It is a **modular platform** in the lineage of Grafana, Backstage, and the Kubernetes control plane: **one engine (core) plus modules plus connectors**. The product spans a catalog of modules — inventory, sessions, the access map, governance, FinOps, evaluations, guardrails, and more — but they all sit on top of a single shared engine.

The architecture's governing constraint is the **"no re-architecture" rule**: the engine is designed so that *any* module in the catalog can be added without touching the core or the other modules. Concretely, every new module:

1. **Consumes** normalized events and data from the engine;
2. **Declares** its own entities in the shared data model;
3. **Exposes** its own API endpoints and UI views.

No module reaches into the internals of another, and none of them re-shapes the core to fit. The engine pays the up-front cost of being multi-tenant, event-driven, and API-first on day one precisely so that breadth can be added later without a redesign. The same principle explains the build order — CLI engine first, web on top: the CLI *is* the engine and exposes the full functionality over CLI and API; the web is a presentation layer over the **same API**, with no duplicated logic. Building the engine and then the visual face on top is not a re-architecture.

The differentiating capability — the read/write access map with the permitted-versus-observed diff — is itself **a module** (module III) over the shared model, not a bespoke pipeline. That is what keeps the platform honest: the flagship feature obeys the same rules as everything else.

## The eight engine subsystems

The engine (the core, "Layer 0") is the set of shared subsystems from which everything else hangs. There are eight of them.

| Subsystem | What it does | Why it lives in the core |
|---|---|---|
| **Ingest + event bus** | Receives OTLP and connector input, normalizes it, and distributes events to modules | Modules react to events without coupling to each other |
| **Connector SDK** | A stable input/output connector interface — the breadth backbone | Third parties extend the platform without forking the core |
| **Module runtime** | Loads and runs modules: compiled in-process plus out-of-process plugins | Adds a module without re-architecting or recompiling the core |
| **General data model** | Multi-tenant entities and relations serving the whole catalog | One schema that all modules share and extend |
| **API (REST/gRPC) + manage-as-code** | All functionality over an API, plus a Terraform provider | The CLI and the web speak the same API; the panel is GitOps-able |
| **AuthN/Z + multi-tenancy** | RBAC/ABAC, orgs and tenants, isolation | Retrofitting permissions and tenancy is ruinously expensive — so, day one |
| **Audit + integrity** | Append-only, hash-chained ledger | Tamper-evidence is cross-cutting, never optional |
| **License / entitlement** | Offline Ed25519 license validation | Self-serve commercial, works air-gapped |

A few specifics worth calling out:

* **Module runtime.** Core modules are compiled into the binary; out-of-process modules and connectors run as plugins over gRPC using `hashicorp/go-plugin`. This gives fault isolation and lets a module be added without recompiling the core.
* **Event bus.** In-process by default (Go channels). The distributed binding over **NATS is optional**, not required — single-node deployments never touch it.
* **Manage-as-code.** The API is the contract of record; the manage-as-code surface adds a Terraform provider so the control plane itself can be declared and version-controlled.
* **Audit + integrity.** The ledger is **append-only and hash-chained**, with **Ed25519-signed checkpoints**. Entries carry a sequence number, the previous hash, the current hash, and a signature — and never carry PII. The ledger is consumed by **pull**: an export endpoint emits CEF, LEEF, syslog, OTLP, or OCSF for forwarding to a SIEM. See [how to forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/).
* **License.** Validation is **offline**, using Ed25519 — there is no phone-home, which is what makes air-gapped operation viable.

For the authentication and authorization details (opaque bearer tokens, first-boot setup token, the policy decision point) see the [security model](/2026-06/explanation/security/security-model/); they are summarized below only where the architecture depends on them.

### The general data model

A single multi-tenant schema serves the whole catalog. Every core entity carries a `tenant_id`, and isolation is enforced at the query / row level. The core entities cover orgs and tenants, agents, sessions, models and providers, MCP servers, skills and tools, resources (databases, servers, stores, APIs), identities, policies, cost records, evaluation results, findings, audit events, health status, and deployments — and, centrally for the differentiator, the **`AccessEdge`**.

Each module registers its own entities and relations through a type registry and per-module tables, without breaking the core. This is the mechanism behind the "no re-architecture" rule at the data layer.

The store starts as **SQLite** (the pure-Go `modernc` driver, so the binary needs no CGO and runs air-gapped) for single-node deployments, and moves to **Postgres with row-level security** for multi-tenant and scale.

## Module III: the access map as a view over the model

The flagship module is the **read/write access map** and its **permitted-versus-observed diff** — least-privilege drift. The critical architectural point is that this is **a view over the general data model, not a separate schema**. The map is materialized from `AccessEdge` entities, and the `AccessEdge` itself **carries both the permitted side and the observed side**, along with the signal source and a confidence level. The diff is therefore a query over the same multi-tenant model every other module uses.

### Read-first and minimal-data

The map is **read-first**: it observes from logs, OpenTelemetry, and (as a backstop) eBPF — it is never in the data path of the agent's calls. It is also **minimal-data**: it stores the *relation* (an agent reads/writes a resource), never payloads, secrets, or PII. The asymmetry is deliberate — high signal, low risk.

### Cooperative path crossed with native store audit

Fidelity comes from crossing two independent kinds of evidence:

* **The cooperative path** — Claude Code and agents emit telemetry over **OpenTelemetry (OTLP)**, complemented by **MCP introspection** of the tools and resources a server exposes. The OTLP receiver is part of core ingest and listens on loopback by default. See [connect Claude Code](/2026-06/how-to/connect-claude-code/).
* **Native store audit** — the store tells you what actually happened. **pgAudit classifies `READ` versus `WRITE`** verbatim on Postgres; **CloudTrail surfaces `readOnly`** for S3; equivalent native audit exists for other engines.

When the cooperative path and the store's own audit agree on an edge, you have a corroborated read/write relation.

### The eBPF backstop, untrusted annotations, and tiered coverage

Three further properties make the map trustworthy rather than naive:

* **eBPF / Tetragon is the non-cooperative backstop.** For paths that don't cooperate, a kernel-level observer provides ground truth on read/write intent at the process and host level. It runs outside the agent's control (anti-evasion) but is blind to TLS payloads — which is fine, because the map only needs the *relation*, not the content.
* **MCP annotations are untrusted.** The MCP read-only / destructive hints are a useful signal, but the MCP specification itself states clients must treat them as untrusted. The map therefore **corroborates** them against other sources and **never trusts an annotation alone**.
* **Coverage is tiered, and the product says so.** Some stores are **clean** to observe passively (SQL databases, object stores, warehouses); some are **lossy** (Mongo, vector databases); and some are **impossible to observe passively** (Redis, SQLite, D1). The map shows confidence levels (attributed versus approximate) rather than pretending to a precision it does not have.

:::caution[A hard dependency: identity per agent]
Native audit attributes activity to a credential or role, not to an agent. A shared service account plus a connection pool collapses attribution — you can no longer tell which agent did what. Resolving this requires issuing or enforcing **identity per agent**, which is the bridge from the access map to the governance module. This is design-stage, and a proof-of-concept on the cooperative path (Claude Code OTEL + MCP into Postgres pgAudit) is the make-or-break gate before the module is built out.
:::

### Reaching the map

Viewing the access graph is a **privileged action**: tenant-scoped, available to the editor role and above (never the lowest viewer role), and **every read is audited**. The map's routes — the graph and the drift result — are reachable on the engine but are **deliberately not published in the served OpenAPI document**; their contracts live in typed Go and TypeScript interfaces instead. The permitted-versus-observed result is exposed at the engine's `drift` route (`/v1/m/accessmap/drift`); there is no separate `diff` endpoint. The core REST surface — twenty paths rendered from the product's own OpenAPI 3.1 contract — is documented in the [API reference](/reference/api/). For the full module list, see the [modules catalog](/2026-06/reference/modules/overview/).

## Deployment topology

The same binary supports several topologies. One constraint holds across all of them: the **data plane — the collectors — always runs on the customer's infrastructure**. That is what makes privacy and air-gapped operation possible; the customer's data does not leave their estate.

### Single binary

The default. One static Go binary carries the CLI engine, the **web UI embedded via `go:embed`** (served from the same origin as the API), and **SQLite** as the store. You ship one artifact and self-host it. This is the topology behind the [zero-to-graph tutorial](/2026-06/tutorials/zero-to-graph/) and the [self-hosting guide](/2026-06/how-to/self-hosting/).

### Distributed

For multi-host, scale, and multi-tenant estates: collectors at the edge **push to a central core over gRPC with mutual TLS**, the store becomes **Postgres** (with row-level security), and the event bus runs on **NATS**. Collectors have no inbound listener — they push, they do not serve — which keeps the edge attack surface minimal.

### Air-gapped

Everything runs locally with **zero egress**. The store is local, the license is validated **offline**, and there is no phone-home. See [air-gap install](/2026-06/how-to/air-gap-install/).

### Managed (future)

A hosted control plane is on the roadmap. Even then, the constraint holds: **the collectors still run on the customer's infrastructure**, and only the control plane is hosted. This is design-stage.

:::tip[The topology, in one line]
The control plane (the engine) can be self-hosted as one binary or, in future, managed; the data plane (the collectors) is always on customer infrastructure. The web is always a view over the engine's own API — never a separate service with its own logic.
:::

## Trust boundaries and licensing

Two boundaries shape the architecture beyond the runtime topology:

* **The connector boundary.** A connector **never imports from the core** — it depends only on the SDK. This keeps third-party connectors from contaminating the core and keeps the license boundary clean.
* **The license boundary.** The core, the modules, and the web are **AGPL-3.0-only**; the SDK and the connectors are **Apache-2.0**; the enterprise tier is commercial. The connector boundary above is what makes the Apache/AGPL split enforceable in code. See [open core and licensing](/2026-06/explanation/open-core-and-licensing/).

## Security posture, in brief

The architecture is secure-by-design: read-first observation (low, asymmetric risk), push-only collectors with no inbound listener, mutual TLS between collector and core, minimal data (edges, never payloads), tamper-evidence through the append-only hash-chained ledger, multi-tenant isolation rooted in the data model, and self-hosting so the data does not leave. The full analysis — including how each trust boundary is defended and what is explicitly out of scope — lives in the [security model](/2026-06/explanation/security/security-model/) and the [threat model](/2026-06/explanation/security/threat-model/).

## Where to go next

* [Modules catalog](/2026-06/reference/modules/overview/) — the full set of modules and how they map to the layers above.
* [Events reference](/2026-06/reference/events/) — the normalized events the ingest layer distributes to modules.
* [Threat model](/2026-06/explanation/security/threat-model/) — the adversaries, the trust boundaries, and the mitigations.
* [Honesty and limits](/2026-06/start/honesty-and-limits/) — what runs today versus what is planned.

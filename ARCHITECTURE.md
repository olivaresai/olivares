# Architecture

> This is the architecture overview for contributors and operators. The deeper rationale, alternatives and per-module detail live in the [documentation site](docs-site/).

Olivares AI is a **modular platform** (the Grafana / Backstage / Kubernetes control-plane pattern): one core, plus modules, plus connectors. The core is designed so any of the 30 modules can plug in without re-architecting the rest. The differentiating access map (R/RW) is *one module*, not the product.

## Topology

Two planes:

- **Data plane (collectors)** always runs inside the customer's infrastructure, for privacy and air-gap. Collectors are read-first, least-privilege, and have no inbound listener: they push to the core. Sources include an OTLP receiver (Claude Code / agents), MCP/skills introspection, audit tails (pgAudit / CloudTrail), an eBPF backstop (Tetragon), and model/provider/cost probes.
- **Control plane (engine)** is self-hosted as a single binary. Collectors reach it over OTLP/gRPC with mTLS. The web UI hangs off the engine and speaks the same API.

## Engine subsystems

The shared core subsystems that everything else depends on:

| Subsystem | Function |
|---|---|
| Ingest + event bus | Receives OTLP / connector input, normalizes it, and distributes events so modules react without coupling to each other. |
| Connector SDK | Stable `SourceConnector` (gather) / `OutputConnector` (notify) / `Module` interfaces; the breadth moat. Apache-2.0. |
| Module runtime | Loads and runs modules: in-process compiled modules plus out-of-process plugins via `hashicorp/go-plugin` (gRPC). A new module adds nothing to the core. |
| General data model | Multi-tenant entities and relationships (every row carries `tenant_id`); one schema serving all 30 modules. |
| API + manage-as-code | All functionality over REST + gRPC; a Terraform provider (module XIX). The CLI and web speak the same API. |
| AuthN/Z + multi-tenancy | RBAC/ABAC, orgs/tenants, isolation enforced from the model via Postgres row-level security. |
| Audit + integrity | Append-only, hash-chained evidence ledger; cross-cutting, not optional. |
| License / entitlement | Offline Ed25519 validation for the commercial exception. |

The rule that makes "no re-architecture" real: every new module (a) consumes normalized events/data from the core, (b) declares its entities in the model, (c) exposes its own API endpoints and UI views — without touching the core or other modules.

## Data model

A general, multi-tenant, extensible schema. Core entities all carry `tenant_id`: `Org/Tenant`, `Agent`, `Session`, `Model/Provider`, `MCPServer`, `Skill`, `Tool`, `Resource`, `Identity`, `AccessEdge` (origin → resource, R/RW, signal source, confidence, permitted/observed), `Policy`, `CostRecord`, `EvalResult`, `Finding`, `AuditEvent`, `HealthStatus`, `Deployment`. The access graph (module III) is a *view* of this model, not a separate schema. Store: SQLite (single-node / air-gap) → Postgres (multi-tenant / scale).

## The `core` / `sdk` / `connectors` / `modules` / `web` / `enterprise` boundary, and why

The split serves two goals at once — a clean license frontier and an extensible architecture:

- `core`, `modules`, `web` are `AGPL-3.0-only`: the protected product.
- `sdk`, `connectors` are `Apache-2.0`: the breadth moat depends on the community building connectors without copyleft friction, so the interface and the connectors are permissive.
- `enterprise` is commercial (`LicenseRef-Olivares-Commercial`): additive features only.

The enforced invariant: **a connector imports only from `sdk/`, never from `core/`**. This is what keeps the AGPL/Apache line clean and lets third-party plugins exist without contaminating the core.

## Capture model: cooperative-first, eBPF backstop

The access map is built from layered signals with shown confidence levels, never faked:

1. **Cooperative path (high fidelity):** Claude Code / MCP telemetry (OTLP) crossed with native store audit — Postgres pgAudit classifies `READ`/`WRITE` verbatim, S3 CloudTrail exposes `readOnly`, MySQL/Snowflake similar. This is the primary path and needs no kernel privileges.
2. **eBPF backstop (ground truth):** Tetragon at the kernel level (`MAY_READ` / `MAY_WRITE`), for the non-cooperative path. It runs outside the agent's control, so an agent that disables its own telemetry does not blind the collector. It is TLS-body blind by design.
3. **MCP annotations** (`readOnlyHint` / `destructiveHint`) are a signal but optional and explicitly untrusted per the MCP spec, so they are corroborated, never trusted.

Coverage is tiered honestly: clean (SQL / object / warehouse) → lossy (Mongo / vector) → impossible passively (Redis / SQLite / D1). The killer feature is the `PERMITTED vs OBSERVED` diff = least-privilege drift. A make-or-break PoC (Claude Code OTEL + MCP → Postgres pgAudit, building the `agent → table R/RW` edge with attribution) gates the access-map module.

## Stack pointers

Go engine, single static binary; `hashicorp/go-plugin` (gRPC) module runtime; OTEL Go SDK ingest; Tetragon / cilium-ebpf for the eBPF connector; REST (chi/echo) + gRPC + Terraform provider; SQLite (modernc, pure-Go, no CGO) → Postgres (pgx) with RLS; React + TS + Vite + Tailwind + React Flow embedded via `go:embed`. Full rationale and alternatives live in the [documentation site](docs-site/).

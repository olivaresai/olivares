---
title: Explanation
description: "Understanding-oriented overview of the Olivares AI control plane:
  its modular architecture, read-first posture, the Permitted-vs-Observed
  pillar, and the open-core model."
slug: 2026-06/explanation
---

This section is understanding-oriented. It explains *why* the Olivares AI control
plane is shaped the way it is — the design principles, the security posture, and
the licensing model — rather than walking you through a task. If you want to *do*
something, start with the [tutorial](/2026-06/tutorials/zero-to-graph/) or the
[how-to guides](/2026-06/how-to/connect-claude-code/); if you need an exact contract, use
the [reference](/2026-06/reference/). For where each kind of page lives, see
[How the docs are organized](/2026-06/start/how-the-docs-are-organized/).

:::note[Design-stage product]
Much of the depth described here is pre-1.0 and design-stage. These pages are
honest about what runs today versus what is planned or post-v1. When a capability
is not built, or its coverage is partial, the page says so. See
[Honesty and limits](/2026-06/start/honesty-and-limits/) for the project's standing
disclosures.
:::

## A modular platform: engine + modules + connectors

Olivares AI is the open, self-hostable **control plane** for the AI agents already
running on your infrastructure. It ships as a single static Go binary
(`olivares`) with the web UI embedded and
served from the same origin as the API. The architecture is a platform, not a
single tool: a **core engine** provides the shared subsystems — ingest and an
in-process event bus, the connector SDK, the module runtime, a multi-tenant data
model, the REST/gRPC API, authentication and authorization, and the append-only
audit ledger — and every capability is a **module** that hangs off those
subsystems without re-architecting the core. **Connectors** feed the engine from
the outside through a stable SDK; a connector never imports from the core, which
keeps the licensing boundary clean.

The default store is SQLite (pure-Go) for single-node and air-gapped use, moving
to Postgres with row-level security for multi-tenant and scale. The event bus is
in-process by default; NATS is an optional distributed binding, not a
requirement. Of the 28 modules in scope, only one — model fine-tuning — is
post-v1.

→ Read [Architecture overview](/2026-06/explanation/architecture/overview/) for the full
engine, data model, and deployment topologies.

## The differentiator: read-first, minimal-data, Permitted-vs-Observed

The pillar that distinguishes the platform is the **R/RW access map**. It builds
a graph of which agent reads or writes which resource, and it does this with two
deliberate constraints:

* **Read-first.** The map observes through telemetry, native audit logs, and an
  eBPF kernel backstop — it sits outside the data path, never in it. It does not
  proxy, intercept, or gate live traffic.
* **Minimal-data.** It stores only the relation (agent → resource, read or
  write) along with the signal source and a confidence level. It does not store
  payloads, secrets, or PII.

On top of that graph sits the killer feature: the **Permitted-vs-Observed diff**,
which surfaces least-privilege drift by comparing what policy *permits* against
what agents are *observed* to do. The cooperative, high-fidelity path is Claude
Code via OpenTelemetry plus MCP introspection, corroborated by native store
audit (for example, pgAudit classifying reads and writes, or CloudTrail
exposing read-only access on object storage); the non-cooperative backstop is
eBPF at the kernel. MCP annotations are treated as untrusted per the MCP
specification and are corroborated, never trusted alone.

:::caution[Coverage is tiered]
Fidelity depends on the source. It is clean for SQL databases, object stores, and
warehouses; lossy for systems such as document and vector databases; and not
achievable passively for some stores (for example Redis, SQLite, or D1). The map
shows its confidence level rather than fabricating attribution it does not have.
:::

→ Read the [Security model](/2026-06/explanation/security/security-model/) for the
posture and the [Threat model](/2026-06/explanation/security/threat-model/) for the
assumptions and limits.

## Self-hosted and open-core

The data plane — the collectors — **always runs on customer infrastructure**, so
estate data does not have to leave the customer's boundary. The control plane can
run as a single self-hosted binary, as a distributed deployment (collectors
pushing to a central core over gRPC with mTLS, backed by Postgres), or fully
air-gapped with zero egress and an offline license; a managed option is future
work.

Licensing is open-core. The engine core, the modules, and the web UI are
AGPL-3.0-only; the SDK and connectors are Apache-2.0; an enterprise tier is
commercial. This split is what lets third parties build connectors without the
copyleft boundary reaching their code.

→ Read [Open core and licensing](/2026-06/explanation/open-core-and-licensing/) for the
per-directory license map and what it means in practice.

## Architecture decisions

The reasoning behind the load-bearing choices — opaque bearer tokens instead of
JWTs, the pluggable authorization PDP behind a single seam, SQLite-to-Postgres,
the hash-chained and signed audit ledger — is recorded as Architecture Decision
Records.

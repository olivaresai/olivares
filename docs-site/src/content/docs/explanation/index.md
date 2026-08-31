---
title: "Explanation"
description: "Understanding-oriented overview of Olivares AI: how it integrates, manages and secures enterprise AI one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside — its modular architecture across 30 modules, the read-first access map, and the open-core model."
---

This section is understanding-oriented. It explains *why* Olivares AI is shaped the
way it is — the design principles, the security posture, and the licensing model —
rather than walking you through a task. If you want to *do*
something, start with the [tutorial](/tutorials/zero-to-graph/) or the
[how-to guides](/how-to/connect-claude-code/); if you need an exact contract, use
the [reference](/reference/). For where each kind of page lives, see
[How the docs are organized](/start/how-the-docs-are-organized/).

:::note[Design-stage product]
Much of the depth described here is pre-1.0 and design-stage. These pages are
honest about what runs today versus what is planned or post-v1. When a capability
is not built, or its coverage is partial, the page says so. See
[Honesty and limits](/start/honesty-and-limits/) for the project's standing
disclosures.
:::

## A modular platform: engine + modules + connectors

Olivares AI helps enterprises **integrate, manage and secure the AI they already
run** — one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside, complementing them rather than
competing. It ships as a single static Go binary (`olivares`) with the web UI
embedded and served from the same origin as the API. The architecture is a
platform, not a single tool: a **core engine** provides the shared subsystems —
ingest and an in-process event bus, the connector SDK, the module runtime, a
multi-tenant data model, the REST/gRPC API, authentication and authorization, and
the append-only audit ledger — and every capability is one of **30 modules** that
hangs off those subsystems without re-architecting the core. **Connectors** feed
the engine from the outside through a stable SDK; a connector never imports from
the core, which keeps the licensing boundary clean.

The default store is SQLite (pure-Go) for single-node and air-gapped use, moving
to Postgres with row-level security for multi-tenant and scale. The event bus is
in-process by default; NATS is an optional distributed binding, not a
requirement. The platform ships **30 modules** today, each at its own honest
maturity — most live and wired end-to-end, some partial or opt-in — across nine
capability areas; own-model registry and fine-tuning is a **planned capability**,
not a shipped module.

→ Read [Architecture overview](/explanation/architecture/overview/) for the full
engine, data model, and deployment topologies.

## The access map: read-first, minimal-data, Permitted-vs-Observed

Among the most useful of the 30 capabilities is the **R/RW access map**. It builds
a graph of which agent reads or writes which resource, and it does this with two
deliberate constraints:

- **Read-first.** The map observes through telemetry, native audit logs, and an
  eBPF kernel backstop — it sits outside the data path, never in it. It does not
  proxy, intercept, or gate live traffic.
- **Minimal-data.** It stores only the relation (agent → resource, read or
  write) along with the signal source and a confidence level. It does not store
  payloads, secrets, or PII.

On top of that graph sits the most distinctive view: the **Permitted-vs-Observed diff**,
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

→ Read the [Security model](/explanation/security/security-model/) for the
posture and the [Threat model](/explanation/security/threat-model/) for the
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

→ Read [Open core and licensing](/explanation/open-core-and-licensing/) for the
per-directory license map and what it means in practice.

## Architecture decisions

The reasoning behind the load-bearing choices — opaque bearer tokens instead of
JWTs, the pluggable authorization PDP behind a single seam, SQLite-to-Postgres,
the hash-chained and signed audit ledger — is recorded as Architecture Decision
Records.

## Regulation, positioning & fit

Two more understanding-oriented threads sit alongside the architecture. The first
is **regulatory**: how the control plane turns the live behaviour of your estate
into the technical evidence an EU AI Act file needs, generated from runtime data
and stored by the control plane you run yourself.

→ Read [EU AI Act evidence from runtime data](/explanation/eu-ai-act-evidence/).

The second is **where the product sits in the market** — defined honestly, with
every statistic traced to a primary source. These pages explain the analyst
vocabulary (agent sprawl, guardian agents, AI TRiSM), how Olivares AI relates to
adjacent tools (LLM gateways/observability, AI control towers — we integrate, we
do not compete), the higher-education vertical, and where the data and claims come
from.

→ Browse [Positioning & fit](/explanation/positioning/market-context-and-sources/),
starting with the verified
[market context & sources](/explanation/positioning/market-context-and-sources/).

---
title: What is Olivares AI?
description: The open, self-hostable control plane for the AI agents already
  running on your infrastructure. Discover, operate and govern every agent,
  model and MCP, with a read/write access map that shows what each one can reach
  — without your data leaving your perimeter.
slug: 2026-06/start/what-is-olivares-ai
---

Olivares AI is the **control plane for the AI agents already running on your
infrastructure**. As teams run more agents, models, MCP servers and tools across
real, heterogeneous estates, one question gets hard to answer and impossible to
ignore:

> **What can each agent actually read and write — and is that what we intended?**

Olivares AI answers it. It **discovers** the agents, models, MCP servers and tools
in your estate, **maps what each one can read and write** against the resources it
touches (databases, object stores, APIs, integrations), and **diffs the access it
is *permitted* against the access it is *observed* using**. The gap between the two
is **least-privilege drift** — the thing you want to find before an auditor or an
attacker does.

Everything runs **self-hosted**, on your own hosts: the data that describes your
estate never leaves your perimeter.

## The core idea: the read/write access map (module III)

The differentiating pillar is the **R/RW access map** — module III of the product.
For every origin (an agent, a non-human identity, a session) it builds an edge to
each resource it touches, classified **read**, **write**, **read-write** or
**unknown**, and tagged with:

* **where the signal came from** (`SignalSource`) — OpenTelemetry from a
  cooperative agent, a Postgres pgAudit READ/WRITE classification, an AWS
  CloudTrail record, a kernel-level eBPF/Tetragon backstop, an MCP annotation
  (treated as **untrusted** and corroborated, never trusted alone), a declared
  policy grant, or an agent-to-agent (A2A) signal; and
* **how much to trust the attribution** (`Confidence`) — `attributed` when it is
  firmly tied to a per-agent identity, `approximate` when it is inferred (a shared
  service account, or a lossy store).

The **killer feature** is the diff: **Permitted vs Observed**. Permitted edges come
from declared grants; observed edges come from real telemetry and audit. Comparing
them surfaces *unexpected accesses* (an agent reading a table it was never granted),
*unused grants* (a permission no agent ever exercised), and *reconciliation-pending*
edges (an access the system cannot yet firmly attribute).

The product is **honest about fidelity**. Coverage is **tiered**: clean on stores
with native audit (SQL, object storage, warehouses), lossy on some stores
(document/vector), and impossible to reconstruct passively on others (e.g. Redis,
SQLite, D1). Where the read/write nature cannot be determined, the mode is
`unknown` — the product never fabricates a classification.

## A complete platform, not a single feature

The R/RW map is the pillar, but the product is the **complete control plane** — a
modular platform (in the spirit of Grafana, Backstage, or a Kubernetes control
plane): one engine plus modules plus connectors, designed so any module attaches
without re-architecting the rest. It ships **28 modules** spanning inventory,
live sessions, the R/RW map, agent orchestration, MCP/skill governance, identity
and access, deployment, knowledge, security and guardrails, model and provider
management, cost/FinOps, evals, compliance, an internal catalog, output
integrations, voice/realtime, a testing sandbox, red-teaming, its own API and
manage-as-code, multi-tenancy, executive dashboards, and agent health/SLA. One
module — model fine-tuning — is explicitly **post-v1**.

See the [modules catalog](/2026-06/reference/modules/overview/) for the full list, and the
[architecture overview](/2026-06/explanation/architecture/overview/) for how the engine and
modules fit together.

## How it observes: read-first, minimal-data

The access map observes logs, OpenTelemetry and eBPF out of band. A failure of
an observation collector creates a gap in visibility; it does not gate agent
traffic. The access graph stores relations — origin → resource, read/write,
source, confidence, timestamp — never payloads, SQL bodies, secrets or PII.

Enforcement points are inline and deny closed. Managed Claude Code sessions
install tool-call hooks that call the engine's policy enforcement point (PEP).
The engine mounts this hook PEP by default. If it is unreachable during an engine
outage or restart, every governed tool call is denied. Plan engine availability
accordingly. The inline inference proxy, MCP tools/call gate and A2A delegation
gate enforce the traffic routed through them; model calls do not pass through
the inference proxy by default.

This is also why it is self-hostable and air-gap friendly: the sensitive data stays
at the customer, and the vendor never sees it — a strong argument for data
residency, GDPR and air-gapped environments.

## Where to go next

* **Try it:** the [zero-to-graph tutorial](/2026-06/tutorials/zero-to-graph/) boots the
  single binary and reaches a populated Permitted-vs-Observed graph.
* **Understand it:** the [architecture overview](/2026-06/explanation/architecture/overview/)
  and the [security & threat model](/2026-06/explanation/security/threat-model/).
* **Operate it:** [self-hosting](/2026-06/how-to/self-hosting/) and
  [air-gapped installation](/2026-06/how-to/air-gap-install/).

:::note[Status]
Olivares AI is **pre-1.0**. The single binary builds, boots and reaches a populated
access graph today (this is exercised end-to-end by the test suite), but several
capabilities are design-stage or post-v1. The documentation is explicit about what
runs now versus what is planned — see [Honesty & limits](/2026-06/start/honesty-and-limits/).
:::

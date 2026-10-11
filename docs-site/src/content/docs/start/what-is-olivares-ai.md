---
title: What is Olivares AI?
description: >-
  Olivares AI runs AI tools and sessions, connects resources, and applies
  permissions, policies, budgets and audit on your own infrastructure.
---

Olivares AI runs AI tools such as Claude Code, Codex and Grok Build on your own
infrastructure. It connects their sessions to resources and identities, with
permissions, policies, budgets and audit evidence. The Go binary includes the
console.

Olivares AI has no mandatory telemetry. Your configuration selects model API
calls, SIEM and webhook outputs, and any external embedding provider.
This describes the architecture and configuration; it is not a guarantee.

## One capability: the read/write access map

The R/RW access map links each observed origin (agent, non-human identity or
session) to a resource. An edge is read, write, read-write or unknown and carries:

- **where the signal came from** (`SignalSource`) — OpenTelemetry from a
  cooperative agent, a Postgres pgAudit READ/WRITE classification, an AWS
  CloudTrail record, a kernel-level eBPF/Tetragon backstop, an MCP annotation
  (treated as **untrusted** and corroborated, never trusted alone), a declared
  policy grant, or an agent-to-agent (A2A) signal; and
- **how much to trust the attribution** (`Confidence`) — `attributed` when it is
  firmly tied to a per-agent identity, `approximate` when it is inferred (a shared
  service account, or a lossy store).

At its centre is the diff: **Permitted vs Observed**. Permitted edges come
from declared grants; observed edges come from real telemetry and audit. Comparing
them surfaces *unexpected accesses* (an agent reading a table it was never granted),
*unused grants* (a permission no agent ever exercised), and *reconciliation-pending*
edges (an access the system cannot yet firmly attribute).

The product is **honest about fidelity**. Coverage is **tiered**: clean on stores
with native audit (SQL, object storage, warehouses), lossy on some stores
(document/vector), and impossible to reconstruct passively on others (e.g. Redis,
SQLite, D1). Where the read/write nature cannot be determined, the mode is
`unknown` — the product never fabricates a classification.

## A platform, not a single feature

The 32 modules cover inventory, sessions, resource access, orchestration (in
development), MCP and skills, identity, deployment, knowledge, security, models
and providers, costs, evals, sandboxing, red-teaming, compliance, catalogs,
output integrations, voice and health. APIs, manage-as-code, multi-tenancy and
executive dashboards provide shared platform capabilities. There are 136
integrations; that count does not establish the readiness of each one. Some
capabilities are partial, planned or require configuration.

Community keeps local observability, stored settings and backup export. SIEM/ITSM push, external telemetry delivery and posture export are included in the Business base edition.

See the [modules catalog](/reference/modules/overview/) for the full list, and the
[architecture overview](/explanation/architecture/overview/) for how the engine and
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

The vendor is outside the observation data path. `olivares upgrade` and
commercial downloads reach the vendor when requested. Use `olivares upgrade --endpoint`
for your own mirror or carry an update bundle into an air-gapped environment.

## Where to go next

- **Try it:** the [zero-to-graph tutorial](/tutorials/zero-to-graph/) boots the
  single binary and reaches a populated Permitted-vs-Observed graph.
- **Understand it:** the [architecture overview](/explanation/architecture/overview/)
  and the [security & threat model](/explanation/security/threat-model/).
- **Operate it:** [self-hosting](/how-to/self-hosting/) and
  [air-gapped installation](/how-to/air-gap-install/).

:::note[Status]
The product is pre-1.0. The test suite exercises the binary from startup to a
populated access graph; several other capabilities remain design-stage or planned.
Check [Honesty & limits](/start/honesty-and-limits/) for their status.
:::

---
title: "Explanation"
description: "Olivares AI architecture, access map, security, deployment and licensing."
---

Read the architecture, security and licensing explanations here.
For instructions, use the [tutorial](/tutorials/zero-to-graph/) or
[how-to guides](/how-to/connect-claude-code/). Interface details are in the
[reference](/reference/); page types are explained in
[How the docs are organized](/start/how-the-docs-are-organized/).

:::note[Limits]
Some capabilities are partial or planned. Check each page and
[Honesty and limits](/start/honesty-and-limits/) before using them.
:::

## A modular platform: engine + modules + connectors

The `olivares` Go binary embeds the console, served from the same origin as the API.
The engine provides ingestion, an in-process event bus, the connector SDK, module
runtime, multi-tenant storage, REST/gRPC APIs, authentication, authorization and
the append-only audit ledger. Modules use those services; connectors feed them
through the SDK and do not import the core.

SQLite is the default store for single-node and air-gapped use. PostgreSQL adds
row-level security for multi-tenant deployments. NATS is an optional event-bus
binding. The 32 modules have different levels of readiness; own-model registry
and fine-tuning remain planned.

[Architecture overview](/explanation/architecture/overview/) describes the engine,
data model and deployment topologies.

## The access map: read-first, minimal-data, Permitted-vs-Observed

The R/RW access map shows which agent reads or writes each resource:

- **Read-first.** The map observes through telemetry, native audit logs, and an
  eBPF kernel backstop — it sits outside the data path, never in it. It does not
  proxy, intercept, or gate live traffic.
- **Minimal-data.** It stores only the relation (agent → resource, read or
  write) along with the signal source and a confidence level. It does not store
  payloads, secrets, or PII.

The Permitted-vs-Observed diff compares declared access with observed activity. The cooperative, high-fidelity path is Claude
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

Collectors run on customer infrastructure. Deploy the engine as one binary,
with distributed collectors using gRPC and mTLS plus PostgreSQL, or air-gapped
with an offline license. A managed option remains future work.

The engine, modules and console are AGPL-3.0-only. The SDK and connectors are
Apache-2.0; commercial modules are separate.
[Open core and licensing](/explanation/open-core-and-licensing/) explains the boundary.

## Architecture decisions

## Regulation, positioning & fit

[EU AI Act evidence](/explanation/eu-ai-act-evidence/) describes runtime evidence
for technical documentation. The positioning pages compare related tools and
link their sources.

→ Browse [Positioning & fit](/explanation/positioning/market-context-and-sources/),
starting with the verified
[market context & sources](/explanation/positioning/market-context-and-sources/).

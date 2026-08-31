---
title: Connect a source
description: Wire a real observation source into the control plane, understand
  the connector model, and choose the right signal per system.
slug: 2026-06/how-to/connect-a-source
---

This page explains the general connector model and how to wire a real source into the engine. If you only want to connect a coding agent, start with [Connect Claude Code](/2026-06/how-to/connect-claude-code/) — that is one specific source on the cooperative path, and this page is the model underneath it.

## The connector model

A source does one job: it **observes** an external system and **emits normalized observations**. It never sits in the data path, never proxies traffic, and never reads payloads. The R/RW access map is built from what the source reports, not from intercepting what flows.

Concretely, a source implements a small interface — `Open` (configure once), `Gather` (run, emitting), `Close` (release) — and during `Gather` it hands the engine one observation at a time through a sink. The engine owns scheduling: a streaming source (a log tail, a receiver) blocks in `Gather` and emits until it is cancelled; a batch source does its work and returns, and the engine decides when to run it again. The connector never owns its own timer.

There are exactly three kinds of observation a source can emit:

| Observation | What it carries | Used by |
|---|---|---|
| `edge` | An origin (agent / identity / session) touched a resource, with a read/write mode | The R/RW access map |
| `cost` | Model/provider usage cost | FinOps |
| `finding` | A guardrail / red-team / forensic finding | Security |

The set is closed by design — a third party cannot introduce a new observation kind. The engine **lifts** each emitted observation onto the in-process event bus, where modules consume it without coupling to the source that produced it. For the access map specifically, the engine resolves the connector's string references to entities and merges the observation into a persisted access edge.

:::note[Minimal data, by contract]
An edge observation carries only identifiers and a read/write classification — never SQL bodies, request payloads, secrets or PII. A finding carries a hash of any sensitive detail, never the detail itself. This is a property of the wire vocabulary the connector speaks, not a configuration option you can turn off. See the [architecture overview](/2026-06/explanation/architecture/overview/) for where this sits in the read-first design.
:::

### Connectors are Apache-2.0 and never import the core

A connector imports the connector SDK and nothing else from the product. It never imports `/core` (the AGPL engine). That boundary is enforced in CI, and it is what lets connectors ship under Apache-2.0 and lets third parties build their own without copyleft friction. The same connector binary runs in-process or out-of-process over gRPC identically. See [Open core and licensing](/2026-06/explanation/open-core-and-licensing/) for the full boundary.

## Provenance and confidence: why the source matters

Every edge records **which source produced it** and a **confidence** level, and the product shows both rather than collapsing them. A `pg_audit` READ and an `mcp_annotation` hint are not the same evidence and are never treated as the same.

The two confidence levels are honest, not cosmetic:

* **`attributed`** — the access is firmly tied to its origin (for example, a per-agent identity present in the audit trail).
* **`approximate`** — the attribution is inferred or lossy (a shared service account, or a store whose audit cannot cleanly separate callers).

The access mode is one of `unknown`, `read`, `write`, `readwrite`. `unknown` is explicit and never guessed — the product would rather show "we could not classify this" than fabricate a read/write label.

## Categories of first-party source, by signal

First-party sources differ by the **signal** they carry. Choose the source by what the system you are observing can honestly tell you.

### `pg_audit` — PostgreSQL READ/WRITE

The pgAudit source tails PostgreSQL's own structured audit log and emits one edge per audited data access. The read/write mode is taken **verbatim from pgAudit's CLASS field** (READ, WRITE, DDL) — never inferred from the SQL text. The origin is the role or `application_name` the log attributes the access to. The connector is read-only over the log file; it never connects to or writes to the database. This is the clean tier: an object/relational store that classifies access in its native trail.

### `cloudtrail` — AWS S3 readOnly

The CloudTrail source reads CloudTrail log files and emits one edge per S3 event. The read/write mode is taken **verbatim from CloudTrail's `readOnly` field**, never inferred. The origin is the IAM principal CloudTrail attributes the call to. An assumed role shared across many callers is marked `approximate`, deliberately, because the trail cannot separate the real callers behind it.

### `otel` — cooperative agents

This is the cooperative path: an agent that emits OpenTelemetry tool telemetry reports what it did, and the engine ingests it. Claude Code is the canonical first-party source here, combining OTLP telemetry with MCP introspection — see [Connect Claude Code](/2026-06/how-to/connect-claude-code/). Cooperative telemetry is the highest-fidelity signal when present, but it depends on the agent being cooperative, which is why a kernel backstop exists.

### `ebpf` — Tetragon kernel backstop (non-cooperative path)

The eBPF source is the anti-evasion half of the map: where the cooperative path sees what an agent *reports*, this sees what the kernel actually did — file reads/writes and network connects — even when an agent disables its own telemetry. It runs **outside the agent's control**.

Two honest constraints define it:

* It does **not** load eBPF programs itself. The kernel capture is done by Tetragon, deployed as a separate hardened service; this source is a read-only consumer of Tetragon's event stream and needs no kernel capabilities of its own.
* It is **blind to the TLS body**. It observes access relationships, never payloads.

Its edges are always `approximate`, for a specific reason: the kernel attributes an access to a process or container — a runtime identity — not to a resolved agent. The access itself is ground truth (the syscall happened); the confidence qualifies the *attribution*, which the access-map module upgrades once it ties the identity to an agent.

:::caution[The kernel backstop is design-stage in its non-cooperative depth]
The cooperative path (store-native audit, OTEL) is the verified, high-fidelity case. The kernel backstop is sound in design but its end-to-end attribution is the part still being proven out. Treat it as a backstop that raises the floor, not as a finished primary source. See [Honesty and limits](/2026-06/start/honesty-and-limits/).
:::

### `mcp_annotation` — untrusted

The MCP introspection source lists a server's tools, resources and prompts and derives a read/write *hint* from each tool's `readOnlyHint` / `destructiveHint`. Per the MCP specification a client **MUST consider these annotations untrusted** unless the server itself is trusted, and the defaults are asymmetric. So this signal is a **declared capability hint, never an observed access**: every such edge is `approximate` and is marked neither observed nor permitted. It supplies the *capability surface* to diff against — not evidence that anything was actually done. It must be corroborated by an observed source, never trusted alone.

## The hard dependency: per-agent identity

Attribution is only as good as the identity the underlying system records. Native audit attributes an access to a **credential or role**, not to an agent. If many agents share one service account or one connection pool, every observed access collapses onto that single identity and the attribution becomes `approximate` — the product will say so rather than pretend it can tell the agents apart.

To get `attributed` edges, give each agent its own identity. This is the bridge to governance: issuing or enforcing per-agent identity is what makes the access map sharp.

:::tip[If attribution looks coarse, check identity first]
Before suspecting the connector, check whether the agents share a credential. A shared service account is the most common reason a clean store still yields `approximate` edges.
:::

## Tiered coverage — be realistic

Coverage is tiered by what a system's audit surface can honestly support:

* **Clean** — SQL databases, object stores and warehouses that classify access natively (Postgres, S3, and peers). Read/write is taken verbatim.
* **Lossy** — stores whose audit cannot cleanly separate read from write or caller from caller (document and vector stores). Edges land, but often `approximate`.
* **Impossible passively** — systems with no usable passive audit surface (in-memory caches, embedded single-file databases). There is no honest read-first signal to capture; the product does not pretend otherwise.

Pick the tier deliberately. A clean-tier store with per-agent identity is where the map is sharpest.

## Wiring a real source

Real (non-demo) sources are wired from a single operator config file named by the environment variable `OLIVARES_SOURCES_CONFIG`, read **before the engine starts**. The config is a JSON document; secrets live in that file (referenced by value) and are never persisted by the engine.

The document declares a list of sources. Each source entry selects a connector by kind, names the tenant its observations belong to, and carries the connector's own settings. The general shape is:

```json
{
  "sources": [
    {
      "name": "prod-postgres",
      "kind": "pg_audit",
      "tenant": "acme",
      "config": {
        "...": "connector-specific settings"
      }
    }
  ]
}
```

The fields above the per-connector `config` block — a source name, the connector `kind`, the owning `tenant`, and an optional poll interval for batch sources — are the stable wiring contract.

:::caution[Per-connector config keys are described generically here on purpose]
The exact keys inside each connector's `config` block (log paths, endpoints, credential references) are owned by each connector and are not reproduced here, because publishing an unverified key would be worse than omitting it. Read the connector's own documentation for its settings, or describe it generically until you have confirmed the keys against the connector you are deploying. Do not copy schema you have not verified. See [Honesty and limits](/2026-06/start/honesty-and-limits/).
:::

### An unconfigured source warns honestly

The engine fails safe, not loud, when nothing is wired:

* If `OLIVARES_SOURCES_CONFIG` is **unset**, the engine starts with no sources.
* If the file is **missing, unreadable, or not valid JSON**, the engine **warns and continues** with no sources — it does not crash on boot.
* If the source list is **empty**, the engine warns that no connector will ingest and that the estate is running on no live traffic.

In every case the boot log tells you plainly that nothing real is wired, rather than silently appearing healthy with an empty map. An honest warning is the design: an empty access map should never look like a clean one.

## Where this runs

The data plane — the collectors that run these sources — **always runs on customer infrastructure**, whether the control plane is a single self-hosted binary, a distributed deployment, or air-gapped. The source observes locally and the engine ingests; the observed estate's data never leaves your boundary. See [Self-hosting](/2026-06/how-to/self-hosting/) and [Air-gap install](/2026-06/how-to/air-gap-install/) for the deployment topologies.

## Related

* [Connect Claude Code](/2026-06/how-to/connect-claude-code/) — the cooperative `otel` path, end to end.
* [Modules overview](/2026-06/reference/modules/overview/) — the modules that consume these observations (inventory, the R/RW access map, FinOps, security).
* [Architecture overview](/2026-06/explanation/architecture/overview/) — where the connector SDK, the event bus and the access map sit in the design.

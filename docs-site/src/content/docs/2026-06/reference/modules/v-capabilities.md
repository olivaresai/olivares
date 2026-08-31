---
title: Module V — MCP, skills & capability management
description: "The capability management overlay: which MCP server exposes which
  tool, what its transport and secret references are, which agent is wired to
  which capability, its version history, and its basic connection health —
  governed, audited, and untrusted by default where the MCP spec says it must
  be."
slug: 2026-06/reference/modules/v-capabilities
---

Module V is the **capability management overlay**: it governs the tools and
capabilities of your agents — which MCP server exposes which tool, what its
transport, scope and configuration are, which origin is wired to which capability,
its version history, and its basic connection health. It sits in the **Management
layer** and has **no actuation surface**: it catalogs, governs and audits, but never
runs a tool or mutates a live MCP runtime.

## What it is

The module is an overlay built **on top of** the passive discovery of module I and
the introspection of the connectors. It does **not** re-implement the MCP client, and
it deliberately does **not** re-materialize the core entities that inventory already
owns (the MCP server, skill, tool and resource records). Instead it reads those core
entities and stores only its **own** overlays, keyed by the connectors'
already-redacted natural references and resolved to core entities at read time — a
single-writer discipline that keeps it from racing inventory's materializer.

This is distinct from module III. Module V answers *"which capability is an agent
connected to"*; [module III](/2026-06/reference/modules/iii-access-map/) answers *"which
resource did an origin read or write"*. They are separate views and the product never
conflates them.

## Its contract & entities

Module V owns four overlay entities (each prefixed `capabilities.`):

| Entity | What it holds |
|---|---|
| **`mcp_config`** | The managed configuration of an MCP server — transport, scope, an endpoint **reference** and **secret references**. There is no column that can hold a usable credential. |
| **`config_revision`** | An append-only snapshot per config version — the immutable version history, surviving deletion of the config. |
| **`wiring`** | The capability-connection graph: an `origin → capability` edge stored by natural reference, never by core entity id. |
| **`health`** | The last observed connection signal of a capability (`connected` / `degraded` / `down` / `unknown`) — a basic signal, **not** an SLA. |

Two contract properties are non-negotiable. **MCP tool annotations are untrusted**:
a tool's `readOnlyHint`/`destructiveHint` are a *declared* hint from the server,
which the MCP specification says clients must treat as untrusted — every tool
projection carries an explicit untrusted flag, never a security badge. **No secret
values on the wire**: a config references secrets by name, kind and a masked hint;
the backend rejects inline credentials in an endpoint or spec rather than storing
them. Minimal-data is a property of the wire, not an afterthought.

Reading the catalog is RBAC-gated and tenant-scoped. Changing a config — and the
secrets it references — is a **privileged change** recorded in the append-only,
hash-chained ledger and attributed to the real principal.

## What it consumes & produces

Module V is fed by the [event bus](/2026-06/reference/events/), not by its own polling. It
reacts to two channels:

* **`edge.observed`** — runtime capability use becomes `wiring` edges. The
  `Source` field distinguishes **observed** signals (`otel`) from **declared** ones
  (`mcp_annotation`), and a newer config-discovery feeder tags statically-declared
  capabilities with a `config` source.
* **`finding.reported`** — the connectors' connection-health findings feed the
  `health` overlay's last-signal status.

It produces no events of its own and dispatches nothing to live infrastructure; its
output is read by the management UI and by other modules over its typed routes.

:::caution[Honest limits]
* **No actuation.** Module V governs and catalogs; it never executes a tool, dials an
  MCP server, or mutates a live runtime. It is a management overlay by nature.
* **Annotation trust ceiling.** `readOnlyHint`/`destructiveHint` are *declared* and
  surfaced as **untrusted** — corroboration of read/write intent against real signals
  is module III's job, not this module's.
* **Connection health is not SLA.** The `health` overlay is the last connection
  signal only; formal uptime, SLA and trend reporting belong to module XXII.
* **Discovery is as deep as the connectors are.** Runtime-observed capabilities
  surface only once an agent exercises them; statically-declared Claude Code surfaces
  (subagents, Skills, plugins, output styles) are now discovered ahead of execution by
  a dedicated config feeder, but it emits **structural metadata only** — names, never
  prompt bodies, skill/plugin contents, or secrets.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module V sits and its actuation status.
* [Module III — access & resource map](/2026-06/reference/modules/iii-access-map/) — the R/RW view this module is deliberately distinct from.
* [Event bus reference](/2026-06/reference/events/) — the `edge.observed` and `finding.reported` payloads it consumes.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine-plus-modules composition.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — acting on what the catalog surfaces.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the product's honest govern-vs-actuate contract.

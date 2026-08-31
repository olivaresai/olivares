---
title: Module I — inventory & discovery
description: Passive discovery and cataloging of everything in the estate —
  agents, sessions, MCP servers, skills, tools, models, providers and non-human
  identities. How entities are materialized from observations, what the catalog
  records, and the limits.
slug: 2026-06/reference/modules/i-inventory
---

Module I is the **catalog of the estate**: a passive, bus-driven inventory of everything
that exists — agents, sessions, Claude Code instances, MCP servers, skills, tools,
resources, models, providers and non-human identities. It discovers by *listening*, never
by probing, and records only relationships, identifiers and liveness — never payloads.
This page is the reference for what the catalog holds and what it deliberately does not.

## What it materializes

Connectors emit **observations**, not entities. They publish normalized
[`edge.observed`](/2026-06/reference/events/) and [`cost.sampled`](/2026-06/reference/events/) facts onto
the event bus; the entities they imply are never sent. Module I **materializes** the core
entity each observation names from its natural reference: an origin `session`/`agent`/
`identity`, an MCP server, a tool, a resource, a skill, and — from cost samples — a
provider and a model (discovered, **without pricing**; FinOps owns that). Materialization
is **idempotent** under at-least-once delivery: find-or-create on the natural key, so the
same observation seen twice never duplicates an entity.

## Its contract & entities

The module registers one entity of its own, `inventory.catalog_entry` — a discovery
overlay attached to each materialized core entity. It records *how* a thing was found, not
*what* it did: a list of signal sources, the hosts it was seen on, first- and last-seen
timestamps, an occurrence count, and a liveness `status` of `active` or `stale`. A
periodic **staleness sweep** marks an entry `stale` when it has not been seen within the
configured window, and flips it back to `active` the moment it reappears; the sweep runs
only over the tenants the module has actually observed (it cannot, and does not, enumerate
tenants). The read surface is small and read-only: a `summary` count by kind and source, a
paginated `entities` listing filterable by kind and status, and a single-entity detail
view. Every read requires a tenant-scoped, namespaced read permission (the lowest viewer
tier suffices); ingestion is high-frequency and not audited per write. Full shapes live in
the [event bus reference](/2026-06/reference/events/) and the product's typed interfaces.

## What it consumes and produces

Module I is a pure **consumer**. It subscribes to `edge.observed`, `cost.sampled` and
`finding.reported` and writes only its own catalog overlay and the core entities it
derives. It emits no events of its own and exposes no actuation surface — discovery is, by
nature, observe-and-catalog. The references it persists arrive **already redacted** from
the connectors; the module stores them verbatim and adds no raw detail of its own, so the
minimal-data property is a property of the wire, upheld end to end.

:::caution[Honest limits]
* **Inventory does not own the access graph.** As of decision A (2026-06-03), module III
  (the access map) is the **sole writer** of the read/write `AccessEdge` and the only
  owner of topology and the Permitted-vs-Observed diff. Inventory discovers and catalogs
  the *entities* an edge names; it no longer records the edge itself, and it serves no
  topology route. The graph is populated only when module III is wired at boot.
* **Discovery is only as complete as the signals.** An entity exists in the catalog only
  if some connector observed it. Absence from the catalog is **not** proof of absence in
  the estate where coverage is partial.
* **Liveness is staleness, not health.** `stale` means "not seen recently", nothing more;
  the silence of a session is normal, and formal health/SLA belongs to module XXII. The
  sweep never mutates the core entity's own lifecycle.
* **No fabricated detail.** The module stores identifiers, relationships and liveness
  counters only — never payloads, secrets, PII, commands, queries or URLs.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module I sits and the honest Actuate split.
* [Module III — the access map](/2026-06/reference/modules/iii-access-map/) — the sole owner of the R/RW graph and drift.
* [Event bus reference](/2026-06/reference/events/) — the `edge.observed`, `cost.sampled` and `finding.reported` events it consumes.
* [Zero to graph](/2026-06/tutorials/zero-to-graph/) — populating the catalog and the map on the demo estate.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine, the layers and the bus.

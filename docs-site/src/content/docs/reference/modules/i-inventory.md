---
title: "Module I — inventory & discovery"
description: >-
  Passive discovery and cataloging of agents, sessions, MCP servers, skills,
  tools, models, providers and non-human identities observed in the estate. How
  entities are materialized from observations, what the catalog and provenance
  records, how durable freshness works, and the limits.
---

Module I is the **catalog of observed entities** in the estate: a passive,
bus-driven inventory of agents, sessions, Claude Code instances, MCP servers,
skills, tools, resources, models, providers and non-human identities that
connectors have actually named. It discovers by *listening*, never by probing.
It records relationships, identifiers and liveness — not payloads — and it is
**not** a census of everything that exists. This page is the reference for what
the catalog holds, how observation provenance and durable freshness work in
development for the next release, and what the module deliberately does not claim.

Observation provenance and durable freshness are accepted for bounded
development composition. They are development capabilities for the next release,
not a published release, a complete RC, or a deployment.

## What it materializes

Connectors emit **observations**, not entities. They publish normalized
[`edge.observed`](/reference/events/) and [`cost.sampled`](/reference/events/)
facts onto the event bus; the entities they imply are never sent. Module I
**materializes** the core entity each observation names from its natural
reference: an origin `session`/`agent`/`identity`, an MCP server, a tool, a
resource, a skill, and — from cost samples — a provider and a model
(discovered, **without pricing**; FinOps owns that). Inventory itself
subscribes to those two event types; live findings belong to
[module II](/reference/modules/ii-sessions/).

Core find-or-create on the natural key avoids duplicating the catalog alias
under at-least-once delivery in the current single-writer model. Two sources
can still keep distinct observations that share that alias; sharing it does
not prove they are the same physical thing, and it does not transfer owner,
workspace or grants. A new event id with the same payload is a **new**
receipt — whether that is a distinct activity stays unknown. The catalog's
`occurrence_count` counts **deliveries**, including a replay of the same event
id, not unique activities.

## Observation provenance

Alongside the catalog alias, the module stores an additive, tenant-scoped
projection of how an observation arrived: a receipt keyed by event id, a
member row per materialized entity (native reference and, when the
registration snapshot is valid, a stable observational identity), and a
conflict row when the same event id later carries different projected facts.
The original receipt is kept; the conflict is recorded rather than overwritten.
A missing event id gets only a storage identity and is never de-duplicated by
payload. Replay of the same id and same facts refreshes the legacy catalog
delivery count without re-resolving aliases. The console can list stored
receipts for one catalog entity at
`GET /v1/m/inventory/entities/{kind}/{id}/observations` with the existing
tenant-wide `inventory:catalog:read` permission: one item per distinct receipt
in ascending receipt-id order, pages of at most 25. Each item is a historical
registration snapshot recorded at reception (not current source registration
or health), the source-declared occurrence instant when declared, first and
last reception of the retained facts, matching-fact deliveries, and whether a
conflicting redelivery is retained. An empty page does not prove the entity
was never observed. `has_more`, an empty list and the catalog total are not
coverage. Catalog freshness on the entity sheet comes from a successful
current point read; it is a separate read from the history. Per-source/family
coverage and the C4 reference estate remain open.

## Its contract & entities

The module registers `inventory.catalog_entry` — a discovery overlay attached
to each materialized core entity. It records *how* a thing was found, not
*what* it did: signal sources, hosts when known, first- and last-seen
timestamps, an optional source-declared `occurred_at` (omitted when the source
declared none; distinct from `last_seen`, which is when **this platform** saw
it), an occurrence count, and a liveness `status` of `active` or `stale`. The
read surface is small and read-only: a `summary` count by kind and source, a
paginated `entities` listing filterable by kind and status, a
single-entity detail view, and the per-entity observation history above. Every read requires a tenant-scoped, namespaced
read permission (the lowest viewer tier suffices). Catalog and provenance
writes are high-frequency and not audited per write.

Durable freshness keeps a second overlay, `inventory.freshness_sweep`: at most
one lazily created row per tenant, holding the cutoff of an open cycle, the
opaque catalog cursor, and the last cutoff whose cycle finished. That last
cutoff records a finished **sweep**, not that any source was completely
enumerated. Full shapes live in the [event bus reference](/reference/events/)
and the product's typed interfaces.

## Durable freshness

A periodic sweep marks a catalog entry `stale` when this platform has not seen
it since the cycle's cutoff, and flips it back to `active` the moment it
reappears. Default thresholds are 30 minutes of silence and a 5-minute
cadence; they are module defaults, not an operator YAML surface in the current
binary (the composition root registers the module with empty host settings).

The module does **not** enumerate tenants and does **not** fall back to the
tenants this process has happened to observe. A private composition adapter
reads the durable organization directory and returns **candidates** only:
active business tenants this instance's residency serves — never the system
partition, never a suspended org, never a tenant pinned to a region this
instance does not serve. Each turn still opens the ordinary tenant-scoped
store write, so residency, service withdrawal and leadership are checked
again; a tenant whose state changed between the snapshot and its turn is
refused there.

Without an authoritative directory the sweep fails visibly and mutates
nothing. On PostgreSQL that authority needs the dedicated `NOSUPERUSER`
`BYPASSRLS` admin read pool already used for cross-tenant directory reads
(`--admin-dsn`). Without that attested pool the sweep does not treat the rows
the application role can see as a complete directory. SQLite has no equivalent
pool requirement. New observations continue to persist when the data handle is
wired; a sweep that cannot run does not unsubscribe ingestion.

Each pass gives every candidate **one turn**. Each turn classifies at most one
page of 1,000 active entries older than the cutoff and stores the cursor so a
restart continues without a new event. Enumeration and each tenant turn have
**separate time budgets**, so one slow tenant does not spend the rest of the
pass. A local failure does not prevent later tenants from getting a turn
**in a live process**. There is no cross-tenant restart cursor, no fairness
under repeated restarts, and no high-availability claim.

If a turn does not complete successfully, that page is not counted as marked.
Page and progress stay together; the next turn rereads whatever was last
stored — the previous frontier, or one already advanced — and continues from
there. The product does not reconstruct progress from memory.

## What it consumes and produces

Module I is a **consumer**. It subscribes to `edge.observed` and
`cost.sampled` and writes its catalog overlay, the core entities it derives,
and the additive provenance and freshness rows above. It emits no events of
its own and exposes no actuation surface. It stores received references and selected observation facts without further
sanitizing those values. Producer-side data minimization is required before
observations are published; this module cannot certify it for every producer.
A persisted reference may still contain a query string, credentials or other
sensitive data if a producer published them. Inventory does not independently
collect complete raw activity content, and it adds no raw payload, secret,
prompt, command or SQL of its own.

:::caution[Honest limits]
- **Inventory does not own the access graph.** As of decision A (2026-06-03),
  module III (the access map) is the **sole writer** of the read/write
  `AccessEdge` and the only owner of topology and the Permitted-vs-Observed
  diff. Inventory discovers and catalogs the *entities* an edge names; it no
  longer records the edge itself, and it serves no topology route. The graph
  is populated only when module III is wired at boot.
- **Discovery is only as complete as the signals.** An entity exists in the
  catalog only if some connector observed it. Absence from the catalog is
  **not** proof of absence in the estate. Completing a freshness sweep is not
  source coverage, completeness of discovery, formal health, or proof that an
  entity was removed.
- **Liveness is staleness, not health.** `stale` means this platform has not
  observed the entity since the cutoff, nothing more. Reappearance restores
  `active`. The silence of a session is normal, and formal health/SLA belongs
  to module XXII. The sweep never mutates the core entity's own lifecycle.
- **No fabricated detail.** The module stores identifiers, relationships and
  liveness counters — never independently collected complete raw activity
  content — and it adds no payloads, secrets, prompts, commands or SQL of its
  own. Received resource URIs may be stored as references. Inventory does not
  re-sanitize those values and cannot certify the absence of query strings,
  credentials or PII inside them.
:::

## Related

- [Modules catalog](/reference/modules/overview/) — where module I sits and the honest Actuate split.
- [Module III — the access map](/reference/modules/iii-access-map/) — the sole owner of the R/RW graph and drift.
- [Event bus reference](/reference/events/) — the `edge.observed`, `cost.sampled` and `finding.reported` events the inventory and sessions modules consume.
- [Zero to graph](/tutorials/zero-to-graph/) — populating the catalog and the map on the demo estate.
- [Architecture overview](/explanation/architecture/overview/) — the engine, the layers and the bus.

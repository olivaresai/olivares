---
title: Module IV — inter-agent communication & orchestration
description: "The observe-and-govern plane for how agents coordinate: a derived
  communication & delegation graph, governed scheduled agents, and a two-phase
  HITL-gated fire. Live dispatch is a deny-closed seam, stated honestly."
slug: 2026-06/reference/modules/iv-orchestration
---

Module IV is the **observe-and-govern** plane for how agents coordinate. It does
**not** reimplement an agent framework (no LangGraph/CrewAI/AutoGen), it does not run
an agent, and it never spawns a process. It derives a live communication & delegation
graph from signals already on the bus, governs scheduled/autonomous agents as
desired-state declarations, and flags cadence-evasion — while the act of *running* an
agent leaves only through a deny-closed seam.

## What it is

Two things sit side by side. First, a **derived communication & delegation graph** —
who delegates to whom (supervisor→worker) and who talks to whom — built as a view over
already-observed access edges, a sibling of the access map ([module III](/2026-06/reference/modules/iii-access-map/)),
never a re-ingested second copy. Second, a register of **governed schedules**: a
scheduled or event-driven agent is a *desired-state declaration*, and firing one is the
single production-affecting action the module exposes.

## Contract & entities

The module owns three entity kinds, declared in the shared data model:

* **`orchestration.relation`** (upsert) — the derived graph edge: a `delegation`,
  `mcp_server` or `mcp_tool` link between two references, with a signal source, a
  read/write `mode`, a `confidence`, counts and first/last-seen timing.
* **`orchestration.schedule`** (lifecycle) — a governed declaration: subject, trigger
  kind (`cron`/`event`/`manual`), an **opaque cadence spec that is never parsed to
  self-fire**, an expected interval, a grace factor, a desired status, and the
  declaring principal recorded as the owner of any autonomous fire.
* **`orchestration.decision`** (**append-only**) — an immutable ledger of every fire
  request, fire and cadence-miss, carrying the `plan_hash`, gate status, `op_status`
  and the **real principal** (never `system`, except for the cadence-miss detection).

The module routes are reachable but deliberately **not** part of the served OpenAPI
contract; their field-level shapes live in the product's typed interfaces. **Firing is
two-phase and HITL-gated**: phase one requests approval; phase two re-verifies the
approval and a strict `plan_hash` match (anti-TOCTOU — a re-target or re-cadence
invalidates a stale approval) before any dispatch. Reading the graph and firing are
**privileged, tenant-scoped, fully-audited** actions, split by verb tier (read for
viewers, declare/retarget for editors, **fire** for admins only) — see
[govern and approve](/2026-06/how-to/govern-and-approve/).

## What it consumes & produces on the bus

It consumes exactly one channel: [`edge.observed`](/2026-06/reference/events/). A
session→Task edge becomes a delegation relation; MCP-topology edges become server/tool
relations; everything else is ignored. A subject's observed liveness for the
cadence check is derived from the relations themselves, so no schedule is queried
per edge. It produces findings on [`finding.reported`](/2026-06/reference/events/):
`orchestration_cadence_miss` when an **active, recurring** schedule stops emitting
versus its declared cadence (a one-shot or paused schedule that simply finished is
normal silence and emits nothing), and `orchestration_ungoverned_fire` when a fire
attempt finds no approval gate wired — the governance gap is made visible while the
fire stays denied. The check is read-time and scoped to the request's pinned tenant;
the module never runs a cross-tenant background scan.

:::caution[Honest limits]
* **Live fire is a deny-closed seam.** The module *governs and schedules*; it never
  actuates on its own. A fire leaves through a Dispatcher seam. With the dispatcher
  unconfigured (the default binary), an approved fire returns an honest `200` with
  status `declared_not_fired` — the safe state is "declared, not fired". A dispatcher
  built and configured by the operator routes an approved, plan-matched fire to
  the same deployment executor or a signed-card-verified A2A task; a dispatcher error
  returns `502` and never advances last-fired. Live A2A delegation adds its own
  deny-by-default policy enforcement point (signed card → allowlist → plan hash →
  approval) and is gated the same way.
* **Graph coverage is partial, and it says so.** Every graph response carries a
  coverage descriptor. The derived graph covers Task delegation, MCP topology and —
  where an A2A connector is wired — observed peer-to-peer A2A; swarm cross-talk and
  non-Task frameworks without an emitting connector are **absent, not zero**. The
  module never presents the graph as complete agent communications.
* **Minimal data on the wire.** The module persists relations and governance evidence
  only — who↔who, counts, timing, redacted refs — **never** message payloads, prompts,
  tool arguments or secrets. No such column exists; sensitive references are hashed
  before persistence. That is a property of the wire, not a setting.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — module IV's layer and honest actuation status.
* [Event bus reference](/2026-06/reference/events/) — `edge.observed` in, `finding.reported` out.
* [Access & resource map](/2026-06/reference/modules/iii-access-map/) — the sibling graph this one derives alongside.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — the two-phase, human-in-the-loop fire.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — what actuates today and what is still a seam.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — where module IV sits in the Intelligence layer.

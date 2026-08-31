---
title: Module XXI — executive dashboards & reporting
description: "The leadership view over the control plane: cost, usage, risk,
  compliance and reliability rolled up from the modules that own the math, gated
  by the same RBAC as the technical views, with on-demand PDF export. What it
  presents, what it never computes, and its honest limits."
slug: 2026-06/reference/modules/xxi-executive-dashboards
---

Module XXI is the **Web layer** (layer 4) leadership surface: a high-level read of the
estate — spend, usage, risk posture, compliance coverage and reliability — sitting
alongside the per-module technical UI. It **aggregates and presents; it never
recomputes** (the modules own every figure), and it inherits the same tenant scoping
and RBAC as the views it summarizes.

## What it is

Two read-only surfaces compose this module:

* the **executive dashboard** (`/dashboards`) — the full cross-module rollup, with a
  selectable cost range (7d / 30d / 90d / month-to-date), a spend breakdown by team,
  project, agent, model or provider, and a printable report cover;
* the **home overview** (`/`) — a deliberately lighter front door: a single grid of
  estate pillars (inventory, live sessions, security, compliance, spend run-rate,
  health/SLA), each a drill-down link into its module.

The home overview reuses the dashboard's read hooks, pure rollups and tile primitives
rather than duplicating them, and shares the same tenant-scoped query cache, so the
front door stays light (fewer queries) while staying consistent with the deep view.

## What it presents (and what it never computes)

The dashboard leads with KPIs across five pillars — **cost** (FinOps XI + Models X),
**usage** (Inventory I + Sessions II), **risk** (Security IX + Red-teaming XVIII +
Access map III), **compliance** (XIII) and **reliability** (Health & SLA XXII). The
rollup layer is a set of **pure functions** that only count, sum and rank what the
modules already decided: cost stays in the modules' integer units, finding severity,
red-team score, control status and health state are passed through unchanged.

Because it owns no math, it cannot launder away a source's honesty seam, and it does
not: a `truncated` aggregate stays flagged as a floor; a red-team run that could not
complete its probes is **never** counted as a pass; access observed with approximate or
opaque coverage is surfaced as a lower bound; compliance reads as **control coverage**,
never as a "compliant" claim, and keeps its standing disclaimer; a health subject with
no check reads `unknown`, not healthy.

## Export & the wire

Export is **on-demand, client-side**: the dashboard prints what is on screen via the
browser's Save-as-PDF (`window.print()`), with a print-only report cover (organization,
range, generation time) and a standing disclaimer footer. This is faithful to RBAC and
tenant scoping **by construction** — the report can only ever contain the sections the
role actually rendered. The exported document, like the dashboard itself, carries
**aggregated KPIs only — no payloads, no secrets**: minimal-data is a property of what
crosses the wire, not a promise about a viewer's good behaviour.

## Actuation

Module XXI has **no actuation surface** (`—` in the [modules catalog](/2026-06/reference/modules/overview/)).
It is a presentation layer over read endpoints the modules already serve; it issues no
write, fires nothing, and dispatches nothing.

:::caution[Honest limits]
* **No scheduled or delivered reports.** The catalog's design intent includes scheduled,
  exportable reports; what ships today is **on-demand client-side print-to-PDF only**.
  There is no server-side reporting endpoint, no recurring schedule and no email
  delivery — do not expect a report to arrive on its own.
* **It is only as honest as its sources.** Every coverage gap, truncation, pending
  attribution and disclaimer comes from the underlying modules and is shown, not
  smoothed; a low number can mean low risk *or* limited coverage. Read each pillar with
  the limits of its module (e.g. access-map coverage tiers).
* **RBAC gates every pillar.** A role that cannot read a source never sees its KPI and
  cannot print it. A reader with no permitted source sees an honest empty state, not a
  fabricated dashboard.
* **Point-in-time, single-source.** Risk, compliance and reliability are current-state
  snapshots; only cost spans the selected range. The view is a roll-up of this control
  plane's own data, not an external BI tool.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — layers, and the Govern/Actuate split.
* [Module XI — Cost & AI FinOps](/2026-06/reference/modules/xi-finops/) — the spend figures it rolls up.
* [Module XIII — Compliance](/2026-06/reference/modules/xiii-compliance/) — control coverage, never a compliance claim.
* [Module III — access & resource map](/2026-06/reference/modules/iii-access-map/) — the drift behind the risk pillar.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — where the Web layer sits.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — how the product states what it does and does not do.

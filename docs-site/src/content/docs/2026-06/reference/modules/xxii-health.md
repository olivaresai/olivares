---
title: Module XXII — health, SLA & uptime
description: "Reliability of the AI estate's agents and MCP servers: what is
  healthy, what is degraded or down, and what depends on what. How health is
  derived from signals the product can prove, what it materializes, and the
  honest limits."
slug: 2026-06/reference/modules/xxii-health
---

Module XXII answers three questions about the estate's AI components — **what is
healthy, what is degraded or down, and what depends on what**. It is bounded to the
**reliability of agents and MCP servers**, not host or infrastructure health in
general. This page is the reference for what the module measures, what it
materializes, and where its honest edges are.

## What it is

XXII is a **consumer of the core**, not a prober: opening sockets into customer
infrastructure is a connector concern, and the sealed observation set has no health
kind. So health is **derived** from signals the module can prove:

* **Liveness (passive).** A session or agent touching an MCP server — or an agent
  acting — is evidence the subject is alive. It refreshes the subject's last-seen
  marker and folds a dependency edge.
* **Active probe results.** An external health-checker or the agent itself posts a
  result to a per-check report endpoint — the honest ingest path for "health
  checks / OTEL metrics".
* **Staleness.** A known subject that stops being seen within its expected cadence
  is itself a signal. A background sweep transitions it to `degraded`, then `down`,
  and opens an incident. The sweep only **degrades or marks down**; recovery comes
  exclusively from real liveness, so a freshly created check never emits a spurious
  recovery.

## Its contract & entities

The module owns four entities. A **health check** is an operator-declared monitored
subject (an agent or an MCP server) with an expected cadence and an SLA target; it
carries the subject's current snapshot state — `healthy`, `degraded`, `down` or
`unknown`. A **health event** is an append-only transition ledger from which uptime
and SLA are *reconstructed* — never stored as a running counter. A **health
incident** is the open→resolved lifecycle of a degraded or down period, with one
open incident enforced per subject. A **health dependency** is an auto-discovered
`origin → target` edge — the dependency map, accumulated idempotently.

Health is **materialized only for declared checks**. A subject observed alive with
**no declared check** is surfaced honestly on the dependency map as `observed` —
*seen alive, health not measured* — a distinct state from `healthy` (a declared
check signalled) and from `unknown` (named, no liveness evidence). The product never
fabricates a measured-healthy state it did not compute. XXII also mirrors a subject's
current state into the core `HealthStatus` entity when the subject is a core id, so
other planes can read an agent's or MCP's health.

## What it consumes & produces

XXII consumes [`edge.observed`](/2026-06/reference/events/) from the bus for passive liveness
and the dependency map, plus the active probe reports that arrive on its API. It
**produces, it does not deliver**: down, degraded, recovered and SLA-breach signals
are emitted as minimal-data `FindingReport`s on the
[`finding.reported`](/2026-06/reference/events/) channel — the product-wide alert stream that
[module XV (notifications)](/2026-06/reference/modules/xv-notify/) routes to Slack, PagerDuty
or a SIEM. XXII never delivers, and never subscribes to its own findings.

:::caution[Honest limits]
* **It measures only what is declared.** Health is materialized for declared checks
  only. A live-but-undeclared subject reads `observed` (seen alive, not measured) —
  never `healthy`. Reliability is only as complete as the checks an operator declares.
* **It is not a prober.** XXII never opens sockets into your infrastructure. It
  derives reliability from liveness, posted probe results and silence — so for a
  subject that emits no telemetry and has no external checker, an absence of signal
  is treated as a signal (staleness), not as proof of health.
* **Uptime and SLA are reconstructed from an append-only ledger**, not kept as a live
  meter; figures reflect the transitions recorded for the requested window.
* **No actuation.** This module governs and observes by nature — it has no actuation
  surface (see [modules overview](/2026-06/reference/modules/overview/)). It detects and
  reports; remediation is a human or downstream concern.
* **Minimal data on the wire.** Stored state is status, reliability metrics and
  dependency relations — never payloads, prompts, secrets or PII. The one sensitive
  detail a probe may carry (an error message) is reduced to a one-way hash; only a
  short, non-sensitive summary is ever displayed.
:::

## Related

* [Event bus reference](/2026-06/reference/events/) — `edge.observed` (liveness) and `finding.reported` (the signals XXII emits).
* [Module XV — output integrations & notifications](/2026-06/reference/modules/xv-notify/) — routes XXII's health findings to destinations.
* [Modules overview](/2026-06/reference/modules/overview/) — where XXII sits and the actuation split.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine, the bus and the core layer.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — what the product observes today versus what it actuates.

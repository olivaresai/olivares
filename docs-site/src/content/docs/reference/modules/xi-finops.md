---
title: "Module XI — cost & AI FinOps"
description: >-
  Account for AI spend from the cost stream, slice it by any attribution
  dimension, forecast the period, and enforce budgets that deny the spend at the
  cap — money-free on the wire, opt-in and fail-open. What it does, and its limits.
---

Module XI is the **cost / FinOps** layer for AI: it accounts for what the model and
provider connectors report, lets you slice spend by any attribution dimension,
forecasts the current period, and turns a budget into real enforcement that **denies
the spend** at the cap rather than only flagging it. This page is the reference for
what FinOps does today and where its guarantees end.

## What it is

FinOps does **not** re-implement provider integration — it consumes the model/provider
cost stream and **accounts for what the connectors authoritatively derived or read**.
Money is always an **integer micro-USD** value (millionths of a dollar), never a float,
so totals never drift. It is an Intelligence-layer module: it owns ingestion, budgets
and analytics, and exposes them through its own RBAC-gated API namespace and UI views
without touching the core or its neighbours.

The module is **minimal-data by construction**: it stores token counts, derived costs
and attribution *references* — never a prompt, a completion, or a secret. Cost is
governance data, so reads are role-gated at the API, and **no USD amount is ever exposed
to an end user** (that is a property of the wire, not a UI setting).

## Its entities & contract

Each `cost.sampled` event (a `CostSample` — see the [event bus](/reference/events/)) is
recorded two ways:

- the canonical, normalized **CostRecord ledger** (a core entity, keyed by id),
  **de-duplicated by a natural key** — the bucket's *identity* (provider / model /
  session / instant plus every attribution dimension and provenance), never its
  *value* — so a re-pulled open bucket or a late-settled report **upserts in place**
  rather than double-counting on the at-least-once stream;
- a denormalized **FinOps read-model** row keyed by the natural attribution names
  (provider, model, agent, session, team, project), so spend aggregates efficiently by
  **any** of those dimensions — including the provider `service_tier`.

A **budget** is a core `Policy` of kind `budget`: a dimension (global / model / provider
/ agent / session / team / project), a limit, a period, and alert thresholds. Its
`action` is one of three — `alert` (showback-only, the safe default that never enforces),
`throttle`, or `block`. Analytics serve spend breakdown by any dimension, totals, a daily
trend series, a run-rate and trend forecast of the current period (with an explicit
confidence band), a prompt-cache efficiency view, and optimization recommendations —
each grounded in recorded data and **honest about its assumptions**.

## What it consumes & produces

FinOps **consumes** `cost.sampled` off the [event bus](/reference/events/) and **produces**
two effects. On ingest, when consumption crosses a budget threshold it has not crossed
this period, it records the alert and **emits a `FindingReport`** (`finding.reported`) —
the *signal only*; delivery to Slack / SIEM / PagerDuty is the output-connector module's
job, not FinOps'.

The second effect is **enforcement**. A budget whose `action` is `throttle` or `block`
denies the spend at the cap through a **`BudgetGate` seam** declared in each acting
module's own terms (orchestration's *fire*, voice's *open*, the model router's *resolve*);
no module imports FinOps. The gate runs **orthogonally to the approval gate** — an action
can be human-approved and still budget-denied — and answers on the cap-effective spend
with a **money-free reason** (no USD, no budget name on the read-only route). A hard
`block` denies with **HTTP 402**, a soft `throttle` with **HTTP 429**, and the denial is
written to the append-only ledger and audited. See [Govern and approve](/how-to/govern-and-approve/).

:::caution[Honest limits]
- **Enforcement is opt-in, not deny-closed by default.** With no enforcing budget that
  scopes a request, nothing is ever denied — that absence is the normal state, not a
  security hole. Only a budget *definitively* at its limit denies. This is deliberate and
  the inverse of the approval gate's deny-closed posture.
- **The gate fails open.** A FinOps read error never takes down an in-flight action — an
  approved fire/open proceeds and the router resolves. The durable backstop is the
  budget-cap finding emitted on ingest, not the pre-flight gate.
- **The router enforces only the scopes it knows pre-execution** (global / provider /
  model); finer scopes (agent, session, team, project) are enforced at the fire/open seams
  and the model gateway, not at route resolution.
- **FinOps accounts; it does not bill.** It records what the connectors report — `billed`
  vs `estimated` provenance is carried, not reconciled into an invoice — and a sample with
  zero/empty fields means *"not reported"*, never *"zero"*.
- **No actuation beyond denial.** FinOps neither executes a model call nor moves money; it
  observes the cost stream and gates spend it is configured to gate.
:::

## Related

- [Event bus reference](/reference/events/) — the `cost.sampled` / `CostSample` and `finding.reported` payloads.
- [Modules catalog](/reference/modules/overview/) — where module XI sits and its honest actuation status.
- [Architecture overview](/explanation/architecture/overview/) — the engine, layers and the cost stream.
- [Govern and approve](/how-to/govern-and-approve/) — acting on a budget-denied action.
- [Honesty & limits](/start/honesty-and-limits/) — the deny-closed-seam policy across modules.

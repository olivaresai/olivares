---
title: "Build a governed workflow (DAG)"
description: "Compose existing governed actions into a dependency graph, review its execution plan without side effects, and run it behind a human approval bound to the exact graph that was reviewed."
---

A **workflow** chains actions the platform already governs — firing a schedule,
signalling other modules, sending a test notification, pausing — into a
dependency graph (a DAG). Running one is a single privileged, human-approved
act, and every step that touches anything leaves a row in the same append-only
decision ledger a single fire would.

Workflows are **composition, not new power**. There is deliberately no step kind
that runs a command, calls an arbitrary URL or carries a payload: a graph can
only re-arrange verbs the estate already exposes, under gates that already
exist. Running a workflow is admin-tier *and* human-approved, so it is never a
way to reach something you could not reach directly.

## The shape of a graph

A workflow is a set of **steps**, each with a short `ref` unique in the
workflow, a `kind`, its typed `config`, and the refs it `depends_on`. The graph
must be acyclic; the server enforces that, along with reference existence and
fan-in/fan-out bounds, before anything is stored.

| Kind | What it does | Gates it passes through |
|---|---|---|
| `schedule-fire` | dispatches an existing governed schedule | kill switch, budget, the dispatcher seam |
| `eventing-emit` | publishes a `workflow.signal` event other modules can subscribe to | — |
| `notify-test` | sends the synthetic test through an alert route | the notify actuator seam |
| `wait` | pauses the run for a bounded time (1s–24h) | — |
| `approval-gate` | opens a human approval **mid-graph** and pauses until it is decided | the approval gate |

`eventing-emit` publishes a **fixed** event type. The step's config contributes
only a label, so a workflow author can never forge a first-party event such as
`edge.observed` into another module's ingestion.

## 1. Declare the workflow

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

Authoring is **write-tier**. A rejected graph comes back as a `400` naming the
offending step:

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

The console anchors that `step_ref` to the node on the canvas. Replacing the
graph later is a single atomic `PUT .../steps` — the graph is reviewed and
approved as a whole, never step by step.

Every change appends a full snapshot to a revision ledger, and any earlier
revision can be restored through the same validation the live verbs use.

## 2. Review the plan — no side effects

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

The dry-run returns the steps in topological order with what each would do, the
gates it would pass, and a warning where a reference has gone stale since the
graph was saved (a schedule retired last week). It writes nothing, dispatches
nothing and opens no approval, so it is a **read**, available to anyone who can
read workflows.

It also returns the `plan_hash` — the fingerprint of the exact graph. Keep
reading.

## 3. Run it — two phases, bound to what a human saw

Running is admin-tier **and** gated. Phase one opens the approval:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

A human decides through the governance decision API. Then phase two consumes
that decision by passing the reference back:

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

The approval is **bound to the plan hash**. Edit the graph between the two
phases and the hash changes, so the approval no longer authorizes anything and
the run is denied — a human's "yes" applies to the graph they reviewed, never to
one substituted afterwards. The run then executes a **snapshot** of that graph,
so an edit mid-run cannot change what is already executing.

Deny-by-default holds throughout: with no approval gate wired, a run is refused
and the governance gap is raised as a finding rather than silently allowed.

## 4. Watch the run

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Each step reports its own state. A step whose upstream failed is `skipped` — the
run never carries on past a failure and never reports success it did not have. A
`wait` shows when it resumes; an `approval-gate` shows the approval it is waiting
on. When an emergency stop is engaged the whole run **freezes** with a visible
`paused_reason` and resumes when the stop lifts; a stop is never silently
absorbed and never fails a run outright.

Steps advance on a background pump, so waits and mid-graph approvals progress
without anyone holding a request open.

### What the ledger records

Every actuating step appends an immutable row attributed to the human who
started the run. Two properties are worth knowing:

- A run that was **denied** is recorded too. Refusals are evidence.
- If an actuation's result arrives after the runner had already given up on it,
  the outcome is **reconciled** into the ledger with the real dispatch
  reference. The step may read "outcome unknown" — but the ledger never claims
  an actuation that did not happen, and never hides one that did.

## Deliberately out of scope

- **Automatic triggers.** A workflow runs when a human approves one. Wiring
  cron or an event to start a run adds an unattended actuation path and rides
  behind the existing schedule rail in its own change.
- **Arbitrary side-effect steps** (HTTP, exec). They would turn a composition
  surface into a general execution engine and defeat the property that a
  workflow can only re-arrange already-governed verbs.

## See also

- [Govern and approve](/how-to/govern-and-approve/) — the approval engine the
  run and the mid-graph gate travel through.
- [Events reference](/reference/events/) — `workflow.signal` and the permission
  a subscriber needs to receive it.

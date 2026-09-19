---
title: "Read a FinOps admission deny and reconciliation"
description: >-
  How a budget deny looks on the wire, how to set the unreachable posture,
  and how to read reservation-versus-commit drift.
sidebar:
  order: 21
---

A billable effect must **reserve** estimated spend before it runs. The
control plane then **commits** the measured cost or **releases** the hold.
This page is the operator view of that path.

## How a deny looks

A hard cap (`action=block`) answers **HTTP 402**. A soft cap (`action=throttle`)
answers **HTTP 429**. The body names the action, the budget and the headroom
that was missing:

```json
{
  "allowed": false,
  "action": "block",
  "budget_name": "eng-cap",
  "reason": "budget \"eng-cap\" block cap reached (monthly): no headroom to reserve 10000000 µUSD"
}
```

This endpoint needs the budget write permission, so it answers a budget
administrator and the figure is theirs to read. What an end user's request
receives is different: the inference proxy never echoes this reason. A cap
denies with `budget limit reached`, a per-seat limit with `spend limit
reached`, and neither carries a budget name or an amount.

CLI:

```bash
olivares finops admission reserve --data @reserve.json -o json
```

Exit status follows the HTTP code. A 402 or 429 is a definitive cap. Do not
retry the same effect without a new period or a higher limit.

## Unreachable store (default deny)

When the budget store cannot be read, admission **refuses**. The reason is
stable:

```json
{
  "allowed": false,
  "action": "block",
  "reason": "budget store unreachable (deny-closed)"
}
```

That deny is also an audit row `finops.admission.denied`. HTTP status is **503**.

The default posture is **deny**. To keep the historical fail-open behaviour
for one request, set `"unreachable": "allow"` on the reserve body. Session
launch also honours `OLIVARES_SESSION_BUDGET_AVAILABILITY=fail-open`. An
unknown value is deny.

## How to read reconciliation

Reservations that never receive Commit or Release expire. That is drift, not
a silent repair.

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

Read the fields:

| Field | Meaning |
|---|---|
| `active` | Holds still counting against the cap |
| `committed` | Settled after a completed effect |
| `released` | Returned because the effect failed |
| `expired_unsettled` | Caller never settled; TTL reclaimed the hold |
| `active_lapsed` | Still marked active after expiry (sweep lag) |
| `idempotency_orphans` | Index row whose handle has no ledger rows |
| `drift` | True when any of the last three is non-zero |

`reconciliation` READS: it reports the ledger and changes nothing, so a hold
that lapsed but has not been swept still shows as `active_lapsed`. `reconcile`
is the job: it sweeps those holds, needs the budget write permission, and is
what emits a finding of kind `finops_reservation_drift` when `drift` is true.
Treat the finding as posture, not as a rewrite of spend.

## Scopes

| Scope | Caller | Hold |
|---|---|---|
| `model_gateway` | Inference proxy | Estimate, then Commit/Release after the call |
| `session_launch` | Operated session launch | Cap check before the process starts |
| `scheduled_job` | Orchestration fire, evals judge, MCP task | Cap check before dispatch |

Retries use an **idempotency key**. The same key and payload replay the
original reservation. A different payload on the same key is a conflict
(HTTP 409).

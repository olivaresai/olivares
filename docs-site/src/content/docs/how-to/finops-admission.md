---
title: "Reserve, settle and reconcile FinOps admission"
description: >-
  How a billable effect reserves spend and receives one handle, how to commit
  or release that handle, how retries and late commits behave, and how to read
  the admission reconciliation.
sidebar:
  order: 21
---

FinOps budgets and spend analysis are **[Business](https://olivares.ai/pricing)** features. Community keeps per-session cost tracking and data export. Budgets stored before 0.1 remain readable and removable, and enforce while the FinOps module is on; Community cannot create or change them. Evaluations and sandboxes remain Community features.


A billable effect **reserves** its estimated spend before it runs and receives
**one handle**. When the effect has run, the caller **commits** the measured cost
with that handle. When it did not run, the caller **releases** the hold. The
engine's own gates (the inference proxy, session launch and scheduled jobs) do
this for themselves. This page is for a connector that calls the routes, and for
the operator who reads the result.

## Reserve

```bash
olivares finops admission reserve --data @reserve.json -o json
```

```json
{
  "scope": "model_gateway",
  "idempotency_key": "gateway/req-7f3a",
  "estimate_micro_usd": 2000000,
  "actor_ref": "alice",
  "dims": { "provider_ref": "anthropic", "model_ref": "claude-sonnet-4" }
}
```

The estimate is held against every enforcing budget that scopes the request and,
when `actor_ref` is set, against that actor's spend limits. The answer carries
the one handle:

```json
{
  "allowed": true,
  "handle": "0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c",
  "estimate_micro_usd": 2000000
}
```

An estimate of zero holds nothing and returns no handle. A cap already past its
limit still refuses it.

| Status | Meaning |
|---|---|
| 200 | Admitted. `handle` is empty when nothing was held. Under `"unreachable": "allow"`, an admission that could not be established is admitted with no hold and the reason `admission could not be established; admitted without a hold (unreachable=allow)`. |
| 402 | A block verdict: a budget or spend limit with `action=block` has no headroom (`spend_limit` is true when a per-seat spend limit refused), the budget set is too large to evaluate, or the tenant is under a lifecycle activation frontier or its frontier state cannot be read. The reason names which. |
| 429 | A budget with `action=throttle` has no headroom. |
| 503 | The admission could not be established: the budget store could not be read, the key stayed busy past the retries (another caller's claim in flight, or a pair an earlier build published whose remaining hold still withholds), the key holds a claim an earlier build left, or the key's owed list does not decode. The reason is `budget store unreachable (deny-closed)`. |
| 409 | The idempotency key was used with another payload. |
| 500 | The key's admission row failed its integrity check. The key is refused in every posture until the row is repaired. |
| 400 | The document cannot be acted on: an unknown scope, a missing key, a negative estimate, or a field the schema does not publish. |

A 402 or 429 for a cap is definitive: do not retry the same effect before a new
period or a higher limit. A 402 for an activation frontier stands while the
attempt lifecycle owns the tenant's ledger. A 402, 429, 503 or 500 refusal is
also an audit row `finops.admission.denied`.

The default posture when the admission cannot be established is **deny**. Set
`"unreachable": "allow"` on the reserve document to admit such a request with no
hold instead: the answer is 200 with the reason above, and the engine log
records it at ERROR with the tenant, the scope, `posture=allow outcome=admitted`
and the class of the failure, never the store's own message. An unknown value is
deny.

## Retry rule

- **Reserve.** The same idempotency key with the same payload, inside the replay
  window, is answered with the handle the first call received
  (`"replayed": true`). The window is five minutes from the answer, or from the
  commit once the hold is committed. Outside the window, and for a call that held
  nothing, the request is evaluated afresh.
- **Commit.** After an uncertain answer, repeat `commit` with the same handle and
  the same amount as often as needed. Every repeat ends in the same rows. Another
  amount after a commit is refused with **409**: the first measured cost stands.
- **Release.** Repeat freely. A release never undoes a commit.

## Commit and release

```bash
olivares finops admission commit --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c","actual_micro_usd":1500000}'
olivares finops admission release --data '{"handle":"0192f6c4-7d2e-7a31-9b7c-4d6f1e2a3b4c"}'
```

Ingest the measured cost before you commit, so the ceiling never under-counts.
The documents carry `handle` and nothing else that names a hold: any other field
is refused with 400. An empty handle settles nothing.

| Status | Meaning |
|---|---|
| 200 | Settled, or already settled the same way. |
| 409 | Another amount after a commit; a hold whose admission is still a claim in flight, so no caller was answered with it; or a hold whose money now belongs to the attempt lifecycle (`lifecycle_api_required`). |
| 500 | The admission row that names the hold failed its integrity check. Nothing was written: keep the ingested cost and retry the same call after the row is repaired. |
| 400 | A handle that is not a hold identity, or a negative amount. |

`committed: true` and `released: true` say the call was accepted, not that it
changed a row: a commit of an empty handle settles nothing, and a release of a
committed hold leaves it committed.

## Late-commit rule

A commit records that the effect ran, so it is accepted however late it arrives:
after a release, after the hold expired, or after another call took the key over.
The hold's rows become committed at the measured amount and keep the instant
their withholding ended. No budget ceiling changes, because a released or expired
row withholds nothing. A late commit of an expired hold lowers
`expired_unsettled` in the next reconciliation.

## Holds an earlier admission build left

A database that ran an earlier admission build can hold rows that build wrote. A
key it admitted under two holds is settled by either of them, and both settle
together. A claim it left in flight is retired only under a stop instant the
operator states:

```bash
OLIVARES_FINOPS_ADMISSION_LEGACY_WRITERS_STOPPED_AT=2026-09-20T09:00:00Z
```

Set it to the instant every writer of the earlier build stopped, as an RFC 3339
time in UTC ending in `Z`. It is read once at startup. Recovery retires such a
claim only once five minutes have passed since that instant, and only while no
row those writers left is dated later. Empty, the default, retires none. Text
that is not such an instant retires none and logs one error at startup. The
report shows the state as `legacy_stop`: `absent`, `invalid`, `future`,
`contradicted`, `waiting` or `usable`.

## Reconciliation

The engine runs recovery every minute and the reconciliation job every five
minutes, for every active tenant.

```bash
olivares finops admission reconciliation -o json
olivares finops admission reconcile -o json
```

`reconciliation` only reads, with budget read: nothing is recovered, swept or
filed. `reconcile` is the job, with budget write: it runs recovery, sweeps the
holds that expired unsettled, and files a finding of kind
`finops_reservation_drift` when `drift` is true. The console shows the same
report beside the budgets.

| Field | Meaning |
|---|---|
| `active`, `committed`, `released` | Ledger rows in each state |
| `expired_unsettled` | Rows whose caller never settled; the TTL returned the headroom |
| `active_lapsed` | Rows past their expiry that the job has not swept yet |
| `idempotency_orphans` | Reserved admission rows whose handle has no ledger rows |
| `owed_remaining` | Holds still owed by a claim in flight or by a published row |
| `legacy_pending`, `legacy_owes_release` | Claims and releases an earlier build left |
| `unresolved` | Recovery writes whose outcome is not established yet; the next pass decides again |
| `undecodable` | Admission rows whose owed list does not decode |
| `frontier_blocked` | Holds recovery can neither settle nor drop under an activation frontier |
| `corrupt` | Admission rows that fail their integrity check; they are counted and never written |
| `drift` | True when `expired_unsettled`, `active_lapsed`, `idempotency_orphans`, `unresolved`, `undecodable` or `corrupt` is not zero |

`unresolved` and `frontier_blocked` are counted only by `reconcile`, whose
recovery pass fills them; `reconciliation` reports them as 0.

Recovery has nothing left to do for a tenant when the counters from
`owed_remaining` to `corrupt` are all zero. Treat a `corrupt` row as an integrity
fault of that tenant's store, and compare it with the last backup. The holds it
names lapse by their TTL; nothing settles them until the row is repaired.

## Scopes

| Scope | Caller |
|---|---|
| `model_gateway` | The inference proxy and model routing: reserve before the call, then commit or release |
| `session_launch` | Operated session launch and voice open |
| `scheduled_job` | Orchestration fires, evaluation judges and MCP tasks |

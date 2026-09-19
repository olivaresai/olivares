---
title: "Reconstruct a historical authorization decision"
description: >-
  What a reconstruction proves, what it cannot prove, and how an auditor
  replays Monday's allow after Tuesday's revoke.
sidebar:
  order: 22
---

An auditor can ask what a policy **decided** on a past date. The control
plane answers from the **evidence ledger**: the recorded policy version and
the inputs that decision consumed. It does **not** evaluate the policy that
is active today.

## Command

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --source-instance pg-prod-1 \
  --action SELECT \
  --action-vocabulary postgres.sql.v1
```

Or reconstruct one stored row:

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP:

- `POST /v1/m/governance/decisions/replay`
- `GET /v1/m/governance/decisions/{id}/reconstruct`

There is no console reconstruct control in this cut. Use the CLI or the
HTTP read.

## What a reconstruction proves

A status of `reconstructed` means all of the following:

1. A recorded `live_authorization` decision exists for that question at or
   before `--at` (or the given decision id exists).
2. The policy artifact that decision named is **retained** with the record.
3. This binary can run that artifact's evaluator (Cedar).
4. Re-evaluation of the **original** artifact and question produced the
   printed outcome.
5. The reconstruction did **not** read the live PDP or the current
   authoring revision.

The printed `policy_version_id` is the retained artifact id. It is not the
route-witness `PolicyVersion` (a maximum of independent fact versions) and
it is not the authoring console's revision number.

## What a reconstruction cannot prove

- That the original enforcement point was honest. A stored allow is a
  historical fact about an evaluator, not a licence to repeat the effect.
- Producer authenticity or ledger integrity. Those are separate export
  and verification paths.
- External activity that Olivares never decided. Without a recorded
  decision the answer is `COULD NOT RECONSTRUCT`, not a guessed deny.
- Engines this binary cannot run (OPA/Rego is authoring-only here).

## COULD NOT RECONSTRUCT

When a required fact is absent the command prints:

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

The `missing` token names the fact. Known tokens:

| Token | Meaning |
|---|---|
| `authorization_decision` | No live decision for that question at `--at` |
| `policy_version_id` | The row did not name a policy version or artifact |
| `policy_artifact` | The named artifact does not resolve in this tenant |
| `policy_artifact.content` | Only a digest is retained; rules are not here |
| `evaluator` | This binary cannot run the named engine |
| `question` | Principal or action was omitted |
| `at` | No instant and no decision id |

A legacy row written before this stamp keeps `policy_version_id` and
`inputs_digest` **unknown**. The store does not backfill them from today's
policy.

## Monday, Tuesday, Wednesday

1. Monday: an allow policy is retained and a live allow is recorded.
2. Tuesday: a revoke (forbid-all) policy is retained and a live deny is
   recorded.
3. Wednesday: `olivares policy replay --at <Monday>` still answers
   **allow**, from Monday's artifact. The live policy is already the
   Tuesday revoke.

If Wednesday's replay answered deny for Monday's instant, the
reconstruction used the live policy. That is a defect.

## Console

This cut does not add a reconstruct button. Decision detail in the
console would call `GET /v1/m/governance/decisions/{id}/reconstruct`
when that surface is wired. Until then the CLI is the operator path.

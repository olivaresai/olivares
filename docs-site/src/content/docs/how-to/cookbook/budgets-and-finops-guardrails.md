---
title: "Recipe: budgets & FinOps guardrails"
description: >-
  Put a hard dollar limit on AI spend — per model, team, workspace or a single
  identity: alert at thresholds, then throttle or block at the cap. Plus
  cost-per-outcome so the spend has a denominator.
sidebar:
  order: 2
---

**Goal:** "this team's agents stop spending at $500/month" — declared once,
enforced live, with alert thresholds on the way up.

Budget enforcement is one of the actuations that is **live in the default
binary**: an enforcing budget at its cap denies the spend with no extra
provisioning ([the modules catalog](/reference/modules/overview/) marks it
`v1 | v1`).

## Create a budget

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **Money is micro-USD** (`limit_micro_usd: 500000000` = $500), so there is
  no float ambiguity in the contract.
- **`dimension` + `key`** scope the budget. Scoped dimensions include
  `global`, `model`, `provider`, `agent`, `session`, `team`, `project`,
  `workspace`, `api_key`, `actor`, `service_tier`, `context_window`,
  `inference_geo`, `gateway` and `identity`.
- **`action`** is the enforcement mode:

| `action` | At the cap |
|---|---|
| `alert` (default) | showback only — alerts fire, nothing is denied |
| `throttle` | the actuation seam slows new spend |
| `block` | the actuation seam denies new spend |

## Budget a single identity

`dimension: "identity"` scopes on the **external id of a firm roster
identity** — the workload or agent identity your
[identity sources](/how-to/connectors/sso-scim-identity/) registered:

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

The identity is resolved at cost-ingest from the sample's agent binding, API
key or actor — so the budget follows the identity across surfaces, not one
API key.

## Watch it work

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

At the cap, an enforcing budget's check returns `allowed: false` with the
action (`throttle` or `block`) and the budget that fired — the denial names
its reason. Alerts also ride the notification stream, so a Slack or
PagerDuty [destination](/how-to/forward-audit-to-splunk/) hears the 80%
crossing before the 100% denial.

In the console, **Cost & FinOps** shows spend by dimension with budget
status inline:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="The Cost & FinOps view with spend trends and budget posture." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="The Cost & FinOps view with spend trends and budget posture." />

## Give spend a denominator: outcomes

Cost-per-outcome is what makes a budget a business conversation. Report
outcomes (a resolved ticket, a merged PR, a closed case) and read the value
panels:

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

The value summary includes **cancellation risk** — burn with no outcomes —
which is the honest inverse of a success metric.

## Notes

- **Fail-open, deliberately:** if the budget check itself errors (a FinOps
  read failure), inference is allowed rather than silently blocked — a
  broken meter must not become an outage. The failure is logged and visible.
- Reserved capacity (`reserved_micro_usd`) counts toward the limit, so a
  budget cannot be dodged by pre-booking.
- `cost_type` is deliberately **not** a budget dimension — estimated-fallback
  lines ride the dimension they belong to instead of forming a parallel pool.
